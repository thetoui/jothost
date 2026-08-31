package agentclient

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/jothost/panel/shared/protocol"
)

// The log operations.
//
// Every one of them names a *source key* from the Agent's catalogue. There is
// no path parameter anywhere in this file, and that is deliberate: the API is
// not in a position to decide which files on the host are logs, and a
// parameter it forwarded would be a parameter a caller could set.

// LogSource is one log the host has, or could have.
type LogSource struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Summary string `json:"summary"`
	Group   string `json:"group"`
	Format  string `json:"format"`
	// Path is where the Agent found it, shown so an operator can go and look
	// at the same file over SSH. Empty when this host does not have it.
	Path string `json:"path"`
	// Present is whether the file exists here. A catalogued log that does not
	// exist is normal — nginx writes its error log on the first error — so the
	// picker lists it and says so.
	Present bool  `json:"present"`
	Size    int64 `json:"size"`
	// Modified is when the file last changed, or nil when it does not exist.
	Modified *string `json:"modified"`
}

// LogSourceList is what this host offers.
type LogSourceList struct {
	Sources []LogSource `json:"sources"`
	Count   int         `json:"count"`
	// Levels is the filter vocabulary, so the panel does not hard-code a list
	// the Agent might disagree with.
	Levels []string `json:"levels"`
}

// LogLine is one line as the panel shows it.
type LogLine struct {
	Offset    int64  `json:"offset"`
	Text      string `json:"text"`
	Level     string `json:"level"`
	Truncated bool   `json:"truncated"`
}

// LogTailResult is one read of a log.
type LogTailResult struct {
	Key   string    `json:"key"`
	Path  string    `json:"path"`
	Lines []LogLine `json:"lines"`
	// Offset is where a follower continues from.
	Offset int64 `json:"offset"`
	Size   int64 `json:"size"`
	// Rotated says the file was replaced or truncated under the caller, so the
	// panel can say so rather than showing what looks like lost entries.
	Rotated bool  `json:"rotated"`
	Scanned int64 `json:"scanned"`
	// Partial says more arrived than one response may carry.
	Partial bool `json:"partial"`
	// Filtered is how many lines the search or level filter removed.
	Filtered int    `json:"filtered"`
	Format   string `json:"format"`
}

// LogTailOptions select what a read returns.
type LogTailOptions struct {
	Lines  int
	Search string
	Level  string
	After  int64
}

// LogChunk is one piece of a log being downloaded.
type LogChunk struct {
	Source string
	Offset int64
	Size   int64
	EOF    bool
	Data   []byte
}

type logChunkWire struct {
	Source string `json:"source"`
	Offset int64  `json:"offset"`
	Size   int64  `json:"size"`
	EOF    bool   `json:"eof"`
	Data   string `json:"data"`
}

// LogList asks the host which logs it has.
func (c *Client) LogList(ctx context.Context, requestID string) (LogSourceList, error) {
	var result LogSourceList
	err := c.call(ctx, requestID, protocol.OperationLogList, map[string]any{}, &result)
	return result, err
}

// LogTail returns the end of one log, or what is new since an offset.
func (c *Client) LogTail(ctx context.Context, requestID, source string,
	opts LogTailOptions,
) (LogTailResult, error) {
	var result LogTailResult
	err := c.call(ctx, requestID, protocol.OperationLogTail, map[string]any{
		"source": source,
		"lines":  opts.Lines,
		"search": opts.Search,
		"level":  opts.Level,
		"after":  opts.After,
	}, &result)
	return result, err
}

// LogRead returns one chunk of a log's raw bytes.
func (c *Client) LogRead(ctx context.Context, requestID, source string,
	offset int64, length int,
) (LogChunk, error) {
	if length <= 0 || length > MaxChunkBytes {
		length = MaxChunkBytes
	}

	var wire logChunkWire
	err := c.call(ctx, requestID, protocol.OperationLogRead, map[string]any{
		"source": source, "offset": offset, "length": length,
	}, &wire)
	if err != nil {
		return LogChunk{}, err
	}

	data, err := base64.StdEncoding.DecodeString(wire.Data)
	if err != nil {
		return LogChunk{}, fmt.Errorf("decode log chunk: %w", err)
	}

	return LogChunk{
		Source: wire.Source,
		Offset: wire.Offset,
		Size:   wire.Size,
		EOF:    wire.EOF,
		Data:   data,
	}, nil
}
