// Package audit records every operation the Agent performs (CLAUDE.md
// section 15).
//
// The Agent keeps its own audit trail rather than relying on the API's
// database, for two reasons: it runs as root and must remain accountable even
// when Postgres is unreachable, and an attacker who reaches the API should not
// be able to erase evidence of what the Agent was asked to do.
//
// Records are JSON lines appended to a file opened O_APPEND, so concurrent
// writes interleave whole records rather than corrupting each other.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Outcome values.
const (
	StatusSuccess = "SUCCESS"
	StatusFailure = "FAILURE"
	StatusDenied  = "DENIED"
)

// Record is one audited operation.
type Record struct {
	Timestamp string `json:"timestamp"`
	Operation string `json:"operation"`
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	// PeerUID and PeerPID identify the calling process, as reported by the
	// kernel rather than by the caller.
	PeerUID    int    `json:"peer_uid"`
	PeerGID    int    `json:"peer_gid"`
	PeerPID    int    `json:"peer_pid"`
	DurationMS int64  `json:"duration_ms"`
	ErrorCode  string `json:"error_code,omitempty"`
	// Detail carries operation context. It must never hold secrets: the
	// token, passwords, and key material are excluded by construction.
	Detail map[string]any `json:"detail,omitempty"`
}

// Writer appends audit records.
type Writer struct {
	log *slog.Logger

	mu   sync.Mutex
	file io.WriteCloser
	path string
	mode os.FileMode
}

// Options configures a Writer.
type Options struct {
	// Path is the audit log file. An empty path disables file output and
	// records go only to the structured logger.
	Path string
	// Mode is the file permission. Audit records name processes and
	// operations, so the default keeps them owner-readable.
	Mode os.FileMode
	Log  *slog.Logger
}

// defaultMode keeps audit records readable only by the Agent's user.
const defaultMode os.FileMode = 0o640

// NewWriter opens the audit log.
//
// A file that cannot be opened is not fatal: the Agent still runs and still
// audits through its structured log, because refusing to start would turn a
// misconfigured log path into an outage. The failure is reported loudly.
func NewWriter(opts Options) (*Writer, error) {
	w := &Writer{log: opts.Log}

	if opts.Path == "" {
		return w, nil
	}

	mode := opts.Mode
	if mode == 0 {
		mode = defaultMode
	}

	dir := filepath.Dir(opts.Path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return w, fmt.Errorf("create audit directory %s: %w", dir, err)
	}

	// O_APPEND makes each write atomic up to PIPE_BUF, so concurrent
	// operations cannot interleave within a single record.
	file, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, mode) //nolint:gosec // path comes from validated configuration
	if err != nil {
		return w, fmt.Errorf("open audit log %s: %w", opts.Path, err)
	}

	w.file = file
	w.path = opts.Path
	w.mode = mode
	return w, nil
}

// Reopen closes the audit log and opens it again at the same path.
//
// It is what makes the log rotatable. The file is held open for the Agent's
// whole life, so once logrotate renames it, every record would go on landing
// in the renamed file and the new one would stay empty; logrotate sends
// SIGHUP after renaming, and the Agent calls this.
//
// The new file is opened before the old one is closed, and a failure keeps
// the old handle: records going to a rotated file are still records, where a
// failed reopen that had already closed the old file would lose every one
// until the Agent restarted.
func (w *Writer) Reopen() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.path == "" {
		return nil
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, w.mode) //nolint:gosec // path comes from validated configuration
	if err != nil {
		return fmt.Errorf("reopen audit log %s: %w", w.path, err)
	}
	old := w.file
	w.file = file
	if old != nil {
		if err := old.Close(); err != nil {
			return fmt.Errorf("close the rotated audit log: %w", err)
		}
	}
	return nil
}

// Path returns the audit log location, or "" when only the logger is used.
func (w *Writer) Path() string { return w.path }

// Write appends a record.
//
// Failures are logged rather than returned: an operation that has already run
// cannot be un-run because its audit write failed, and the caller has nothing
// useful to do with the error. Losing the record is still surfaced at error
// level so it is visible.
func (w *Writer) Write(record Record) {
	if record.Timestamp == "" {
		record.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}

	// Every record reaches the structured log, which is what journald and
	// docker capture.
	w.log.Info("agent_audit",
		"operation", record.Operation,
		"request_id", record.RequestID,
		"status", record.Status,
		"peer_uid", record.PeerUID,
		"peer_pid", record.PeerPID,
		"duration_ms", record.DurationMS,
		"error_code", record.ErrorCode,
	)

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return
	}

	line, err := json.Marshal(record)
	if err != nil {
		w.log.Error("failed to encode audit record", "error", err.Error())
		return
	}
	if _, err := w.file.Write(append(line, '\n')); err != nil {
		w.log.Error("failed to write audit record", "error", err.Error())
	}
}

// Close releases the audit file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
