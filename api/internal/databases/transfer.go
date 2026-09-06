package databases

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/jothost/panel/api/internal/agentclient"
)

// Exporting and importing a database as a .sql file.
//
// The panel's Export and Import tiles used to be links to the Backups page.
// That is a different thing wearing the same word: a backup is an archive of
// the host, taken on a schedule, restored as a whole. An operator who wants to
// send somebody a dump of one database, or who has been sent one, wants a
// file — and had no way to get one out or put one in.
//
// Both directions stream. A dump is arbitrarily large and the API is not the
// place for it to accumulate: bytes move from the Agent to the client, or from
// the client to the Agent, a chunk at a time.

// ErrExportUnsupported means the host has no dump tool for this engine.
var ErrExportUnsupported = errors.New("this host cannot dump that database")

// ExportInfo describes a dump that is ready to be sent.
type ExportInfo struct {
	// Filename is what the browser should save it as.
	Filename string
	Size     int64
	// token is the Agent's handle. Not exported: nothing outside this package
	// has any use for it, and it must not reach a URL or a log.
	token string
}

// BeginExport dumps a database and returns what is needed to send it.
//
// The dump is made before a byte is written to the response, so a failure is
// still an error the client can be told about. Dumping while streaming would
// mean discovering half way through that mysqldump had failed, with the
// headers already sent and no way to say so.
func (s *Service) BeginExport(ctx context.Context, databaseID string, actor Actor,
	requestID string,
) (ExportInfo, error) {
	database, err := s.repo.Get(ctx, databaseID)
	if err != nil {
		return ExportInfo{}, err
	}

	transfer, err := s.agent.DatabaseExport(ctx, requestID, database.Engine, database.Name)
	if err != nil {
		return ExportInfo{}, err
	}

	// Audited when the dump is made, not when it finishes sending. A download
	// the operator cancelled still means the data left the database.
	s.record(ctx, actor, ActionDatabaseExport, database.ID, map[string]any{
		"database": database.Name,
		"engine":   database.Engine,
		"bytes":    transfer.Size,
	})

	return ExportInfo{Filename: transfer.Name, Size: transfer.Size, token: transfer.Token}, nil
}

// StreamExport writes a prepared dump to w and then discards it.
//
// The transfer is discarded even when the client hangs up: it is somebody's
// data sitting on the host's disk, and the only thing that wanted it has gone.
func (s *Service) StreamExport(ctx context.Context, info ExportInfo, requestID string,
	w io.Writer,
) (int64, error) {
	defer func() {
		// A background context: the request's may already be cancelled, which
		// is exactly the case where the cleanup matters most.
		if err := s.agent.DatabaseTransferFinish(context.WithoutCancel(ctx), requestID, info.token); err != nil {
			s.log.Warn("a database export could not be discarded",
				"request_id", requestID, "error", err.Error())
		}
	}()

	var offset int64
	for {
		if err := ctx.Err(); err != nil {
			return offset, err
		}

		chunk, err := s.agent.DatabaseTransferRead(ctx, requestID, info.token, offset,
			agentclient.TransferChunkBytes)
		if err != nil {
			return offset, err
		}

		if len(chunk.Data) > 0 {
			if _, err := w.Write(chunk.Data); err != nil {
				// The client hung up. Not a server error, and the loop has to
				// stop rather than keep pulling chunks nobody will read.
				return offset, fmt.Errorf("write to client: %w", err)
			}
			offset += int64(len(chunk.Data))
		}

		if chunk.EOF {
			return offset, nil
		}
		if len(chunk.Data) == 0 {
			// No progress and no end. Unreachable unless the Agent is wrong;
			// it is here so that a bug there cannot pin a core here.
			return offset, errors.New("the agent returned an empty chunk before the end of the dump")
		}
	}
}

// Import loads a .sql file into a database.
//
// The upload streams to the Agent as it arrives rather than being held here.
// The API runs unprivileged with no business holding a copy of somebody's
// database, and a dump that had to fit in the API's memory would put a ceiling
// on imports for no reason.
func (s *Service) Import(ctx context.Context, databaseID string, actor Actor,
	requestID, filename string, body io.Reader,
) (int64, error) {
	database, err := s.repo.Get(ctx, databaseID)
	if err != nil {
		return 0, err
	}

	transfer, err := s.agent.DatabaseTransferBegin(ctx, requestID, filename)
	if err != nil {
		return 0, err
	}

	written, err := s.uploadTo(ctx, requestID, transfer.Token, body)
	if err != nil {
		// The partial upload goes, whatever happened. Left behind it is
		// somebody's data on disk that nothing will ever ask for again.
		if discardErr := s.agent.DatabaseTransferFinish(context.WithoutCancel(ctx), requestID,
			transfer.Token); discardErr != nil {
			s.log.Warn("a failed database import could not be discarded",
				"request_id", requestID, "error", discardErr.Error())
		}
		return written, err
	}

	// The Agent discards the transfer itself once it has replayed it, whether
	// or not the replay worked.
	if err := s.agent.DatabaseImport(ctx, requestID, database.Engine, database.Name,
		transfer.Token); err != nil {
		s.record(ctx, actor, ActionDatabaseImport, database.ID, map[string]any{
			"database": database.Name,
			"engine":   database.Engine,
			"bytes":    written,
			"outcome":  "failed",
		})
		return written, err
	}

	s.record(ctx, actor, ActionDatabaseImport, database.ID, map[string]any{
		"database": database.Name,
		"engine":   database.Engine,
		"bytes":    written,
		"filename": filename,
	})
	return written, nil
}

// uploadTo streams a reader into a transfer.
func (s *Service) uploadTo(ctx context.Context, requestID, token string, body io.Reader) (int64, error) {
	buffer := make([]byte, agentclient.TransferChunkBytes)
	var written int64

	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}

		read, readErr := body.Read(buffer)
		if read > 0 {
			if _, err := s.agent.DatabaseTransferWrite(ctx, requestID, token, buffer[:read]); err != nil {
				return written, err
			}
			written += int64(read)
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, fmt.Errorf("read the upload: %w", readErr)
		}
	}
}
