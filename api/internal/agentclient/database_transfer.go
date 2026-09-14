package agentclient

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/jothost/panel/shared/protocol"
)

// Moving a dump between the panel and a database.
//
// Everything here names an opaque token. The API never learns where the Agent
// keeps a dump and cannot ask for a file by path, so there is no traversal to
// attempt from this side of the boundary — which is the point of the shape.

// TransferChunkBytes is how much is moved per call.
//
// The protocol is JSON, so bytes travel base64-encoded and cost a third more
// than they weigh. Kept below the Agent's own frame limit.
const TransferChunkBytes = 1 << 20

// DatabaseTransfer is a dump waiting to be fetched or applied.
type DatabaseTransfer struct {
	Token string `json:"token"`
	// Name is what a browser should save the download as. Never a path.
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// TransferChunk is one piece of a transfer.
type TransferChunk struct {
	Data []byte
	EOF  bool
}

// DatabaseExport dumps a database and returns the transfer holding it.
func (c *Client) DatabaseExport(ctx context.Context, requestID, engine, name string) (DatabaseTransfer, error) {
	var result DatabaseTransfer
	err := c.call(ctx, requestID, protocol.OperationDatabaseExport,
		map[string]any{"engine": engine, "database": name}, &result)
	return result, err
}

// DatabaseTransferBegin opens an empty transfer for an upload.
//
// The name is a label shown back to an operator and is never used as a path;
// the Agent reduces it to its last segment regardless.
func (c *Client) DatabaseTransferBegin(ctx context.Context, requestID, name string) (DatabaseTransfer, error) {
	var result DatabaseTransfer
	err := c.call(ctx, requestID, protocol.OperationDatabaseTransferBegin,
		map[string]any{"database": name}, &result)
	return result, err
}

// transferChunkWire is the encoded form of a chunk.
type transferChunkWire struct {
	Data string `json:"data"`
	EOF  bool   `json:"eof"`
}

// DatabaseTransferRead returns one chunk of a transfer.
func (c *Client) DatabaseTransferRead(ctx context.Context, requestID, token string,
	offset int64, length int,
) (TransferChunk, error) {
	if length <= 0 || length > TransferChunkBytes {
		length = TransferChunkBytes
	}

	var wire transferChunkWire
	err := c.call(ctx, requestID, protocol.OperationDatabaseTransferRead,
		map[string]any{"token": token, "offset": offset, "length": length}, &wire)
	if err != nil {
		return TransferChunk{}, err
	}

	data, err := base64.StdEncoding.DecodeString(wire.Data)
	if err != nil {
		return TransferChunk{}, fmt.Errorf("decode transfer chunk: %w", err)
	}
	return TransferChunk{Data: data, EOF: wire.EOF}, nil
}

// DatabaseTransferWrite appends one chunk to a transfer.
func (c *Client) DatabaseTransferWrite(ctx context.Context, requestID, token string,
	data []byte,
) (int64, error) {
	var result struct {
		Size int64 `json:"size"`
	}
	err := c.call(ctx, requestID, protocol.OperationDatabaseTransferWrite, map[string]any{
		"token": token,
		"data":  base64.StdEncoding.EncodeToString(data),
	}, &result)
	return result.Size, err
}

// DatabaseTransferFinish discards a transfer.
//
// Called after a download has been sent and after an upload that was
// abandoned. A token that names nothing is not an error, so a caller cleaning
// up after a failure need not know whether there is anything to clean up.
func (c *Client) DatabaseTransferFinish(ctx context.Context, requestID, token string) error {
	var result struct {
		Discarded bool `json:"discarded"`
	}
	return c.call(ctx, requestID, protocol.OperationDatabaseTransferFinish,
		map[string]any{"token": token}, &result)
}

// DatabaseImport replays an uploaded dump into a database.
//
// The Agent discards the transfer whether or not the load worked: a failed
// load is not retried from the same token, because the operator uploads again
// having seen what went wrong.
func (c *Client) DatabaseImport(ctx context.Context, requestID, engine, name, token string) error {
	var result struct {
		Loaded bool `json:"loaded"`
	}
	return c.call(ctx, requestID, protocol.OperationDatabaseImport, map[string]any{
		"engine": engine, "database": name, "token": token,
	}, &result)
}
