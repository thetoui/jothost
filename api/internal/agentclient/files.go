package agentclient

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// MaxChunkBytes mirrors the Agent's own chunk bound.
//
// Content travels base64-encoded inside a JSON message, and the socket refuses
// anything over 1 MiB. Both sides have to agree on this or a transfer fails
// halfway through with a truncated message rather than a clear refusal.
const MaxChunkBytes = 256 * 1024

// FileEntry is one file or directory as the Agent described it.
type FileEntry struct {
	Name             string    `json:"name"`
	Path             string    `json:"path"`
	Type             string    `json:"type"`
	Size             int64     `json:"size"`
	Mode             string    `json:"mode"`
	Modified         time.Time `json:"modified"`
	Owner            string    `json:"owner"`
	Group            string    `json:"group"`
	UID              int       `json:"uid"`
	GID              int       `json:"gid"`
	Target           string    `json:"target,omitempty"`
	TargetInsideRoot bool      `json:"target_inside_root,omitempty"`
	Editable         bool      `json:"editable"`
}

// FileListing is one page of a directory.
type FileListing struct {
	Path      string      `json:"path"`
	Parent    string      `json:"parent"`
	Entries   []FileEntry `json:"entries"`
	Total     int         `json:"total"`
	Offset    int         `json:"offset"`
	Limit     int         `json:"limit"`
	Truncated bool        `json:"truncated"`
}

// FileChunk is one slice of a file, already decoded.
type FileChunk struct {
	Path   string
	Offset int64
	Size   int64
	EOF    bool
	Data   []byte
}

// fileChunkWire is the chunk as it travels: base64, because the protocol is
// JSON and file content is arbitrary bytes.
type fileChunkWire struct {
	Path   string `json:"path"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	EOF    bool   `json:"eof"`
	Length int    `json:"length"`
	Data   string `json:"data"`
}

// FileEntryResult wraps the single entry most mutations return.
type FileEntryResult struct {
	Entry FileEntry `json:"entry"`
}

// FileArchiveResult reports what an archive operation did.
type FileArchiveResult struct {
	Path    string `json:"path"`
	Entries int    `json:"entries"`
	Size    int64  `json:"size"`
}

// FileMatch is one search hit.
type FileMatch struct {
	Entry      FileEntry `json:"entry"`
	Line       string    `json:"line,omitempty"`
	LineNumber int       `json:"line_number,omitempty"`
}

// FileSearchResult is a bounded set of matches.
type FileSearchResult struct {
	Path      string      `json:"path"`
	Query     string      `json:"query"`
	Matches   []FileMatch `json:"matches"`
	Truncated bool        `json:"truncated"`
	Scanned   int         `json:"scanned"`
}

// FileList returns one page of a directory.
func (c *Client) FileList(ctx context.Context, requestID, path string, offset, limit int) (FileListing, error) {
	var listing FileListing
	err := c.call(ctx, requestID, protocol.OperationFileList, map[string]any{
		"path": path, "offset": offset, "limit": limit,
	}, &listing)
	return listing, err
}

// FileStat describes one path.
func (c *Client) FileStat(ctx context.Context, requestID, path string) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileStat,
		map[string]any{"path": path}, &result)
	return result.Entry, err
}

// FileRead returns one chunk of a file.
func (c *Client) FileRead(ctx context.Context, requestID, path string, offset int64, length int) (FileChunk, error) {
	if length <= 0 || length > MaxChunkBytes {
		length = MaxChunkBytes
	}

	var wire fileChunkWire
	err := c.call(ctx, requestID, protocol.OperationFileRead, map[string]any{
		"path": path, "offset": offset, "length": length,
	}, &wire)
	if err != nil {
		return FileChunk{}, err
	}

	data, err := base64.StdEncoding.DecodeString(wire.Data)
	if err != nil {
		return FileChunk{}, fmt.Errorf("decode file chunk: %w", err)
	}

	return FileChunk{
		Path:   wire.Path,
		Offset: wire.Offset,
		Size:   wire.Size,
		EOF:    wire.EOF,
		Data:   data,
	}, nil
}

// FileWriteRequest is one chunk being sent to the Agent.
type FileWriteRequest struct {
	Path     string
	Offset   int64
	Data     []byte
	Truncate bool
	Final    bool
}

// FileWrite sends one chunk.
func (c *Client) FileWrite(ctx context.Context, requestID string, req FileWriteRequest) (FileEntry, error) {
	if len(req.Data) > MaxChunkBytes {
		return FileEntry{}, fmt.Errorf("chunk of %d bytes exceeds the %d byte limit",
			len(req.Data), MaxChunkBytes)
	}

	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileWrite, map[string]any{
		"path":     req.Path,
		"offset":   req.Offset,
		"data":     base64.StdEncoding.EncodeToString(req.Data),
		"truncate": req.Truncate,
		"final":    req.Final,
	}, &result)
	return result.Entry, err
}

// FileMkdir creates a directory.
func (c *Client) FileMkdir(ctx context.Context, requestID, path string, parents bool) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileMkdir,
		map[string]any{"path": path, "parents": parents}, &result)
	return result.Entry, err
}

// FileCreate makes an empty file.
func (c *Client) FileCreate(ctx context.Context, requestID, path string) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileCreate,
		map[string]any{"path": path}, &result)
	return result.Entry, err
}

// FileDelete removes a file or directory.
func (c *Client) FileDelete(ctx context.Context, requestID, path string, recursive bool) error {
	return c.call(ctx, requestID, protocol.OperationFileDelete,
		map[string]any{"path": path, "recursive": recursive}, nil)
}

// FileCopy duplicates a file or directory.
func (c *Client) FileCopy(ctx context.Context, requestID, source, destination string, overwrite bool) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileCopy, map[string]any{
		"path": source, "destination": destination, "overwrite": overwrite,
	}, &result)
	return result.Entry, err
}

// FileMove renames or moves a file or directory.
func (c *Client) FileMove(ctx context.Context, requestID, source, destination string, overwrite bool) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileMove, map[string]any{
		"path": source, "destination": destination, "overwrite": overwrite,
	}, &result)
	return result.Entry, err
}

// FileChmod changes permission bits.
func (c *Client) FileChmod(ctx context.Context, requestID, path, mode string, recursive bool) (FileEntry, error) {
	var result FileEntryResult
	err := c.call(ctx, requestID, protocol.OperationFileChmod, map[string]any{
		"path": path, "mode": mode, "recursive": recursive,
	}, &result)
	return result.Entry, err
}

// FileArchive writes sources into a zip.
func (c *Client) FileArchive(ctx context.Context, requestID string, sources []string, destination string) (FileArchiveResult, error) {
	var result FileArchiveResult
	err := c.call(ctx, requestID, protocol.OperationFileArchive, map[string]any{
		"sources": sources, "destination": destination,
	}, &result)
	return result, err
}

// FileExtract unpacks a zip.
func (c *Client) FileExtract(ctx context.Context, requestID, archive, destination string) (FileArchiveResult, error) {
	var result FileArchiveResult
	err := c.call(ctx, requestID, protocol.OperationFileExtract, map[string]any{
		"path": archive, "destination": destination,
	}, &result)
	return result, err
}

// FileSearch walks a tree for names or content.
func (c *Client) FileSearch(ctx context.Context, requestID, path, query string, content bool, limit int) (FileSearchResult, error) {
	var result FileSearchResult
	err := c.call(ctx, requestID, protocol.OperationFileSearch, map[string]any{
		"path": path, "query": query, "content": content, "limit": limit,
	}, &result)
	return result, err
}
