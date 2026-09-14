package operations

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jothost/panel/agent/internal/database"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// Moving a dump between the panel and a database.
//
// The panel exported and imported through the backup system before this, which
// was the wrong shape twice over: an operator who wanted a .sql file to send
// somebody had to take a backup, and one who had been sent a .sql file had no
// way to load it at all. A dump is a file, not an archive of the host.
//
// Everything here is addressed by an opaque token. The API says "the file
// behind this token" and cannot say "the file at this path", so there is
// nothing to traverse and nothing to normalise. See database.TransferStore.

// maxChunkBytes bounds one read or write.
//
// The protocol is JSON, so bytes travel base64-encoded and cost a third more
// than they weigh. This is set well below the socket's own frame limit.
const maxChunkBytes = 1 << 20

// transferPayload is what every operation here takes.
type transferPayload struct {
	// Token names an existing transfer. Empty when one is being created.
	Token string `json:"token"`
	// Database and Engine name what is being exported or imported into.
	Database string `json:"database"`
	Engine   string `json:"engine"`
	// Offset and Length select a chunk to read.
	Offset int64 `json:"offset"`
	Length int   `json:"length"`
	// Data is base64-encoded bytes to append.
	Data string `json:"data"`
}

// transfers returns the store, creating it on first use.
//
// Lazily rather than in Dependencies: a host that never exports a database
// should not have a spool directory, and the store creates its directory when
// it first holds something.
func (r *Registry) transfers() *database.TransferStore {
	r.transferOnce.Do(func() {
		r.transferStore = database.NewTransferStore(r.deps.DatabaseSpoolDir)
	})
	return r.transferStore
}

// dumper resolves the dump tool for an engine, or says why it cannot.
func (r *Registry) dumper(engine string) (database.Dumper, error) {
	if r.deps.Databases == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Database management is not configured on this host", nil)
	}
	dumper, err := r.deps.Databases.DumperFor(engine)
	if err != nil {
		// "No dump tool" and "no such engine" are different problems with
		// different fixes, and DumperFor already distinguishes them.
		return nil, Fail(protocol.CodeUnsupported, err.Error(), err)
	}
	return dumper, nil
}

// handleDatabaseExport dumps one database into a transfer.
//
// Synchronous rather than a job: the caller is a browser waiting for a
// download, and a dump of a database small enough to send through a browser is
// quick. A database too large for that belongs in the backup system, which is
// why the transfer store caps what it will hold.
func (r *Registry) handleDatabaseExport(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(payload.Database); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	dumper, err := r.dumper(payload.Engine)
	if err != nil {
		return nil, err
	}

	// The name a browser will save it as. Built here from the database's own
	// validated name rather than taken from the request, so a caller cannot
	// choose what somebody's download is called.
	name := payload.Database + ".sql"

	transfer, path, err := r.transfers().Create(name)
	if err != nil {
		return nil, err
	}

	if err := dumper.Dump(ctx, payload.Database, path); err != nil {
		// A failed dump leaves a partial file, and a partial dump that stayed
		// here would be downloaded as if it were whole.
		_ = r.transfers().Discard(transfer.Token)
		return nil, Fail(protocol.CodeInternal, err.Error(), err)
	}

	finished, err := r.transfers().Finish(transfer.Token)
	if err != nil {
		_ = r.transfers().Discard(transfer.Token)
		return nil, err
	}

	return map[string]any{
		"token": finished.Token,
		"name":  finished.Name,
		"size":  finished.Size,
	}, nil
}

// handleDatabaseTransferBegin opens an empty transfer for an upload.
func (r *Registry) handleDatabaseTransferBegin(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	// The name is only ever shown back to an operator, so it is stripped to
	// its last segment: a caller sending "../../etc/passwd" gets "passwd" in a
	// label and nothing else, because the name is not used as a path anywhere.
	name := lastSegment(payload.Database)
	if name == "" {
		name = "upload.sql"
	}

	transfer, _, err := r.transfers().CreateEmpty(name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"token": transfer.Token, "name": transfer.Name}, nil
}

// handleDatabaseTransferRead returns one chunk of a transfer.
func (r *Registry) handleDatabaseTransferRead(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	length := payload.Length
	if length <= 0 || length > maxChunkBytes {
		length = maxChunkBytes
	}

	data, eof, err := r.transfers().Read(payload.Token, payload.Offset, length)
	if err != nil {
		return nil, transferError(err)
	}

	return map[string]any{
		"data":   base64.StdEncoding.EncodeToString(data),
		"offset": payload.Offset,
		"read":   len(data),
		"eof":    eof,
	}, nil
}

// handleDatabaseTransferWrite appends one chunk to a transfer.
func (r *Registry) handleDatabaseTransferWrite(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	data, err := base64.StdEncoding.DecodeString(payload.Data)
	if err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "the chunk is not valid base64", err)
	}
	if len(data) > maxChunkBytes {
		return nil, Fail(protocol.CodeInvalidPayload,
			fmt.Sprintf("a chunk may be at most %d bytes", maxChunkBytes), nil)
	}

	size, err := r.transfers().Append(payload.Token, data)
	if err != nil {
		return nil, transferError(err)
	}
	return map[string]any{"size": size}, nil
}

// handleDatabaseTransferFinish discards a transfer.
//
// Called after a download has been sent, and after an upload that was
// abandoned. Discarding a token that names nothing is not an error: it is what
// a caller cleaning up after a failure does.
func (r *Registry) handleDatabaseTransferFinish(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := r.transfers().Discard(payload.Token); err != nil {
		return nil, err
	}
	return map[string]any{"discarded": true}, nil
}

// handleDatabaseImport replays an uploaded dump into a database.
//
// The transfer is discarded whether or not the load worked. Leaving it would
// keep somebody's data on disk after the one operation that wanted it, and a
// failed load is not retried from the same token — the operator uploads again,
// having seen what went wrong.
func (r *Registry) handleDatabaseImport(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload transferPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.DatabaseName(payload.Database); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	dumper, err := r.dumper(payload.Engine)
	if err != nil {
		return nil, err
	}

	path, err := r.transfers().PathFor(payload.Token)
	if err != nil {
		return nil, transferError(err)
	}
	defer func() { _ = r.transfers().Discard(payload.Token) }()

	if reporter != nil {
		reporter.Report(20, "Loading "+payload.Database)
	}
	if err := dumper.Reload(ctx, payload.Database, path); err != nil {
		return nil, Fail(protocol.CodeInternal, err.Error(), err)
	}
	if reporter != nil {
		reporter.Report(100, "Loaded "+payload.Database)
	}

	return map[string]any{"database": payload.Database, "loaded": true}, nil
}

// transferError maps a store failure onto the wire.
func transferError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, database.ErrNoTransfer):
		return Fail(protocol.CodeNotFound, "no such transfer", err)
	case errors.Is(err, database.ErrTransferTooLarge):
		return Fail(protocol.CodeInvalidPayload,
			"the file is larger than this panel accepts", err)
	default:
		return err
	}
}

// lastSegment returns the final path component of a name, without ever
// treating it as a path afterwards.
func lastSegment(name string) string {
	trimmed := strings.TrimSpace(name)
	if index := strings.LastIndexAny(trimmed, `/\`); index >= 0 {
		trimmed = trimmed[index+1:]
	}
	return strings.TrimSpace(trimmed)
}
