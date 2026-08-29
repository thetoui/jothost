package operations

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/jothost/panel/agent/internal/files"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/pathsec"
	"github.com/jothost/panel/shared/protocol"
)

// filePayload carries the arguments every file operation may need.
//
// One struct rather than thirteen: the fields are the same handful of paths and
// flags, and a single decode point means every operation rejects an unknown
// field the same way.
type filePayload struct {
	Path        string   `json:"path"`
	Destination string   `json:"destination"`
	Sources     []string `json:"sources"`

	Offset int64  `json:"offset"`
	Length int    `json:"length"`
	Limit  int    `json:"limit"`
	Data   string `json:"data"`

	Truncate  bool `json:"truncate"`
	Final     bool `json:"final"`
	Recursive bool `json:"recursive"`
	Parents   bool `json:"parents"`
	Overwrite bool `json:"overwrite"`
	Content   bool `json:"content"`

	Mode  string `json:"mode"`
	Query string `json:"query"`
}

// fileManager returns the manager, or the error a caller should see when file
// management is not configured.
func (r *Registry) fileManager() (*files.Manager, error) {
	if r.deps.Files == nil || !r.deps.Files.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"File management is not available on this host", nil)
	}
	return r.deps.Files, nil
}

func (r *Registry) handleFileList(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	listing, err := manager.List(payload.Path, int(payload.Offset), payload.Limit)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(listing)
}

func (r *Registry) handleFileStat(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Stat(payload.Path)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

// handleFileRead returns one chunk of a file, base64-encoded.
//
// The protocol is JSON and file content is arbitrary bytes, so it cannot travel
// as a string. Encoding costs a third more, which is why the chunk size is set
// well below the socket's own limit.
func (r *Registry) handleFileRead(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	chunk, err := manager.Read(payload.Path, payload.Offset, payload.Length)
	if err != nil {
		return nil, fileError(err)
	}

	return map[string]any{
		"path":   chunk.Path,
		"offset": chunk.Offset,
		"size":   chunk.Size,
		"eof":    chunk.EOF,
		"length": len(chunk.Data),
		"data":   base64.StdEncoding.EncodeToString(chunk.Data),
	}, nil
}

func (r *Registry) handleFileWrite(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	data, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return nil, Fail(protocol.CodeInvalidPayload,
			"data must be base64-encoded", err)
	}

	entry, err := manager.Write(files.WriteRequest{
		Path:     payload.Path,
		Offset:   payload.Offset,
		Data:     data,
		Truncate: payload.Truncate,
		Final:    payload.Final,
	})
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry, "written": len(data)})
}

func (r *Registry) handleFileMkdir(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Mkdir(payload.Path, payload.Parents)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

func (r *Registry) handleFileCreate(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Create(payload.Path)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

func (r *Registry) handleFileDelete(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := manager.Delete(payload.Path, payload.Recursive); err != nil {
		return nil, fileError(err)
	}
	return map[string]any{"path": payload.Path, "deleted": true}, nil
}

func (r *Registry) handleFileCopy(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Copy(payload.Path, payload.Destination, payload.Overwrite)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

func (r *Registry) handleFileMove(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Move(payload.Path, payload.Destination, payload.Overwrite)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

func (r *Registry) handleFileChmod(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	entry, err := manager.Chmod(payload.Path, payload.Mode, payload.Recursive)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(map[string]any{"entry": entry})
}

func (r *Registry) handleFileArchive(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := manager.Archive(payload.Sources, payload.Destination)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(result)
}

func (r *Registry) handleFileExtract(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := manager.Extract(payload.Path, payload.Destination)
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(result)
}

// handleFileSearch walks a tree, bounded by the operation's own context.
//
// This is the one file operation whose cost is set by the size of the tree
// rather than by the request, so the context is passed through and honoured.
func (r *Registry) handleFileSearch(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	manager, err := r.fileManager()
	if err != nil {
		return nil, err
	}
	var payload filePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := manager.Search(ctx, files.SearchRequest{
		Path:    payload.Path,
		Query:   payload.Query,
		Content: payload.Content,
		Limit:   payload.Limit,
	})
	if err != nil {
		return nil, fileError(err)
	}
	return structToMap(result)
}

// fileError maps the files package's errors onto protocol codes.
//
// Every path failure collapses to NOT_FOUND. A caller must not be able to tell
// "this exists but is outside your root" from "this does not exist": the
// difference maps the host's filesystem one request at a time.
func fileError(err error) error {
	switch {
	case errors.Is(err, files.ErrUnavailable):
		return Fail(protocol.CodeUnsupported,
			"File management is not available on this host", err)

	case errors.Is(err, files.ErrNotFound),
		errors.Is(err, pathsec.ErrOutsideRoot),
		errors.Is(err, pathsec.ErrSymlinkEscape),
		errors.Is(err, pathsec.ErrNotFound):
		return Fail(protocol.CodeNotFound, "No such file or directory", err)

	case errors.Is(err, pathsec.ErrTraversal),
		errors.Is(err, pathsec.ErrNotAbsolute),
		errors.Is(err, pathsec.ErrNullByte),
		errors.Is(err, pathsec.ErrEmptyPath):
		return Fail(protocol.CodeInvalidPayload, "The path is not valid", err)

	case errors.Is(err, files.ErrExists):
		return Fail(protocol.CodeInvalidPayload,
			"A file or directory already exists at that path", err)
	case errors.Is(err, files.ErrNotDirectory):
		return Fail(protocol.CodeInvalidPayload, "That path is not a directory", err)
	case errors.Is(err, files.ErrNotRegular):
		return Fail(protocol.CodeInvalidPayload, "That path is not a regular file", err)
	case errors.Is(err, files.ErrNotEmpty):
		return Fail(protocol.CodeInvalidPayload,
			"The directory is not empty; deleting it must be explicit", err)
	case errors.Is(err, files.ErrDestInSource):
		return Fail(protocol.CodeInvalidPayload,
			"A directory cannot be copied or moved into itself", err)
	case errors.Is(err, files.ErrProtectedPath):
		return Fail(protocol.CodeInvalidPayload,
			"That path is managed by the panel and cannot be changed here", err)
	case errors.Is(err, files.ErrRootProtected):
		return Fail(protocol.CodeInvalidPayload,
			"The file manager's own root cannot be removed or moved", err)

	case errors.Is(err, files.ErrInvalidMode):
		return Fail(protocol.CodeInvalidPayload,
			"Permissions must be an octal mode such as 0644", err)
	case errors.Is(err, files.ErrUnsafeMode):
		return Fail(protocol.CodeInvalidPayload,
			"The setuid, setgid and sticky bits cannot be set from the panel", err)

	case errors.Is(err, files.ErrEmptyQuery):
		return Fail(protocol.CodeInvalidPayload, "A search needs something to search for", err)

	case errors.Is(err, files.ErrArchiveEscape):
		return Fail(protocol.CodeInvalidPayload,
			"The archive contains a path that would escape the destination", err)
	case errors.Is(err, files.ErrArchiveUnsafe):
		return Fail(protocol.CodeInvalidPayload,
			"The archive contains a link or device entry, which cannot be extracted safely", err)
	case errors.Is(err, files.ErrArchiveTooLarge):
		return Fail(protocol.CodeInvalidPayload,
			"The archive expands to more than this host allows", err)
	case errors.Is(err, files.ErrArchiveTooMany):
		return Fail(protocol.CodeInvalidPayload,
			"The archive contains more entries than this host allows", err)
	case errors.Is(err, files.ErrArchiveNotZip):
		return Fail(protocol.CodeInvalidPayload, "That file is not a zip archive", err)
	case errors.Is(err, files.ErrNothingToArchive):
		return Fail(protocol.CodeInvalidPayload, "No files were selected to archive", err)

	case errors.Is(err, files.ErrTooLarge):
		return Fail(protocol.CodeInvalidPayload, "That is larger than one chunk allows", err)

	default:
		return Fail(protocol.CodeInternal, "The file operation failed", err)
	}
}
