// Package files exposes the Agent's file manager over HTTP.
//
// The API holds no filesystem privilege of its own (CLAUDE.md section 7): every
// operation here is a request to the Agent, which owns the disk. What this
// package adds is the streaming — a browser downloads a whole file in one
// response and uploads one in one request, while the Agent transport moves it a
// chunk at a time.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit actions. Reads are not audited: browsing a directory is not a sensitive
// action and recording every listing would bury the writes that matter.
const (
	ActionUpload  = "file.upload"
	ActionWrite   = "file.write"
	ActionDelete  = "file.delete"
	ActionCreate  = "file.create"
	ActionMkdir   = "file.mkdir"
	ActionCopy    = "file.copy"
	ActionMove    = "file.move"
	ActionChmod   = "file.chmod"
	ActionArchive = "file.archive"
	ActionExtract = "file.extract"
)

// ResourceTypeFile labels audit events from this package.
const ResourceTypeFile = "file"

// MaxUploadBytes bounds a single upload.
//
// The Agent never holds a whole file, but a request that never ends still ties
// up a worker and fills a disk, so the size is capped at the edge where it can
// be refused cheaply.
const MaxUploadBytes int64 = 512 << 20 // 512 MiB

// Errors this package distinguishes.
var (
	ErrPathRequired = errors.New("a path is required")
	ErrNoFile       = errors.New("the request contained no file")
)

// Actor is who is asking, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Service performs file operations through the Agent.
type Service struct {
	agent *agentclient.Client
	audit *audit.Recorder
}

// ServiceOptions configures a Service.
type ServiceOptions struct {
	Agent *agentclient.Client
	Audit *audit.Recorder
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	return &Service{agent: opts.Agent, audit: opts.Audit}
}

// Download streams a file to w, pulling it from the Agent one chunk at a time.
//
// Nothing here accumulates the file. A 2 GB download costs one chunk of memory
// on the API and one on the Agent, which is the only way a control panel can
// serve a file larger than the machine's RAM.
func (s *Service) Download(ctx context.Context, requestID, path string, w io.Writer) (int64, error) {
	var offset int64

	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}

		chunk, err := s.agent.FileRead(ctx, requestID, path, offset, agentclient.MaxChunkBytes)
		if err != nil {
			return offset, err
		}

		if len(chunk.Data) > 0 {
			if _, err := w.Write(chunk.Data); err != nil {
				// The client hung up. That is not a server error, and the loop
				// must stop rather than keep pulling chunks nobody will read.
				return offset, fmt.Errorf("write to client: %w", err)
			}
			offset += int64(len(chunk.Data))
		}

		if chunk.EOF {
			return offset, nil
		}
		if len(chunk.Data) == 0 {
			// No progress and no EOF would loop forever. This should be
			// unreachable; it is here so that a bug in the Agent cannot pin a
			// core on the API.
			return offset, errors.New("the agent returned an empty chunk before the end of the file")
		}
	}
}

// Upload streams a request body into a file, a chunk at a time.
//
// The first chunk truncates and the last one sets the length, so an upload
// replacing a larger file leaves none of the old content behind.
func (s *Service) Upload(ctx context.Context, requestID, path string, body io.Reader, actor Actor) (agentclient.FileEntry, error) {
	if strings.TrimSpace(path) == "" {
		return agentclient.FileEntry{}, ErrPathRequired
	}

	buffer := make([]byte, agentclient.MaxChunkBytes)
	var offset int64
	var entry agentclient.FileEntry
	first := true

	for {
		if err := ctx.Err(); err != nil {
			return agentclient.FileEntry{}, err
		}

		read, readErr := io.ReadFull(body, buffer)
		atEnd := errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF)
		if readErr != nil && !atEnd {
			return agentclient.FileEntry{}, fmt.Errorf("read upload: %w", readErr)
		}

		// An empty file still needs one write, or the upload would create
		// nothing at all and report success.
		if read > 0 || first {
			written, err := s.agent.FileWrite(ctx, requestID, agentclient.FileWriteRequest{
				Path:     path,
				Offset:   offset,
				Data:     buffer[:read],
				Truncate: first,
				Final:    atEnd,
			})
			if err != nil {
				return agentclient.FileEntry{}, err
			}
			entry = written
			offset += int64(read)
			first = false
		}

		if atEnd {
			break
		}
	}

	s.record(ctx, actor, ActionUpload, path, map[string]any{"bytes": offset})
	return entry, nil
}

// UploadFromMultipart streams the first file part of a multipart body.
//
// The part is streamed rather than buffered: ParseMultipartForm would write the
// whole upload to the API's own temp directory first, which is both a second
// copy and a place for a large upload to fill a disk the operator is not
// watching.
func (s *Service) UploadFromMultipart(ctx context.Context, requestID, directory string, r *http.Request, actor Actor) (agentclient.FileEntry, error) {
	reader, err := r.MultipartReader()
	if err != nil {
		return agentclient.FileEntry{}, fmt.Errorf("read multipart body: %w", err)
	}

	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return agentclient.FileEntry{}, ErrNoFile
		}
		if err != nil {
			return agentclient.FileEntry{}, fmt.Errorf("read upload part: %w", err)
		}

		if part.FormName() != "file" || part.FileName() == "" {
			if err := part.Close(); err != nil {
				return agentclient.FileEntry{}, err
			}
			continue
		}

		// The filename comes from the browser and is used as a path segment.
		// Anything but a plain name is refused rather than repaired: a caller
		// sending "../../etc/cron.d/x" is not making a typo.
		name, err := SafeName(part.FileName())
		if err != nil {
			_ = part.Close()
			return agentclient.FileEntry{}, err
		}

		target := JoinPath(directory, name)
		entry, err := s.Upload(ctx, requestID, target,
			io.LimitReader(part, MaxUploadBytes), actor)
		_ = part.Close()
		return entry, err
	}
}

// record writes an audit event for a change to the filesystem.
func (s *Service) record(ctx context.Context, actor Actor, action, path string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["path"] = path

	// ResourceID is deliberately left empty: the column is a uuid, and a file
	// has no id — it has a path. Passing the path here made every file audit
	// write fail on a type cast, which the panel would have carried silently as
	// "no file operations were ever performed" (CLAUDE.md section 15). The path
	// is in the metadata, which is where a value of that shape belongs.
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeFile,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	})
}

// Record exposes the audit trail to the handlers for operations that are a
// single Agent call rather than a stream.
func (s *Service) Record(ctx context.Context, actor Actor, action, path string, metadata map[string]any) {
	s.record(ctx, actor, action, path, metadata)
}
