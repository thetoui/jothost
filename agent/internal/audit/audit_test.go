package audit

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jothost/panel/shared/logger"
)

func newWriter(t *testing.T, path string) (*Writer, *bytes.Buffer) {
	t.Helper()

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Level: "debug", Output: &buf})

	writer, err := NewWriter(Options{Path: path, Log: log})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	t.Cleanup(func() {
		if err := writer.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return writer, &buf
}

func readRecords(t *testing.T, path string) []Record {
	t.Helper()

	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}

	var records []Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line is not valid JSON: %v (%s)", err, line)
		}
		records = append(records, record)
	}
	return records
}

func TestWriteAppendsJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, _ := newWriter(t, path)

	writer.Write(Record{Operation: "metrics.cpu", RequestID: "req_1", Status: StatusSuccess, PeerUID: 1000})
	writer.Write(Record{Operation: "service.status", RequestID: "req_2", Status: StatusFailure, ErrorCode: "NOT_FOUND"})

	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}
	if records[0].Operation != "metrics.cpu" || records[0].PeerUID != 1000 {
		t.Fatalf("unexpected first record: %+v", records[0])
	}
	if records[1].ErrorCode != "NOT_FOUND" {
		t.Fatalf("unexpected second record: %+v", records[1])
	}
	// A timestamp is filled in when the caller omits it, so no record can be
	// written without one.
	if records[0].Timestamp == "" {
		t.Fatal("records must carry a timestamp")
	}
}

func TestWriteAlsoReachesTheStructuredLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, logBuf := newWriter(t, path)

	writer.Write(Record{Operation: "metrics.cpu", RequestID: "req_1", Status: StatusSuccess})

	// journald and docker capture the structured log, so it must carry the
	// record too, not only the file.
	if !strings.Contains(logBuf.String(), "agent_audit") {
		t.Fatalf("expected an audit log line: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "metrics.cpu") {
		t.Fatalf("log line must name the operation: %s", logBuf.String())
	}
}

func TestRecordsSurviveReopening(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	first, _ := newWriter(t, path)
	first.Write(Record{Operation: "one", RequestID: "req_1", Status: StatusSuccess})
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second, _ := newWriter(t, path)
	second.Write(Record{Operation: "two", RequestID: "req_2", Status: StatusSuccess})

	// O_APPEND means a restart adds to the trail rather than truncating it:
	// an audit log that a restart erases is not an audit log.
	records := readRecords(t, path)
	if len(records) != 2 {
		t.Fatalf("expected the earlier record to survive, got %d", len(records))
	}
	if records[0].Operation != "one" || records[1].Operation != "two" {
		t.Fatalf("unexpected ordering: %+v", records)
	}
}

func TestFileModeIsRestrictive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, _ := newWriter(t, path)
	writer.Write(Record{Operation: "x", RequestID: "req_1", Status: StatusSuccess})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Audit records name processes and operations; they are not world data.
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Fatalf("audit log mode = %#o, must grant no world access", perm)
	}
}

func TestMissingPathFallsBackToTheLogger(t *testing.T) {
	// An empty path disables the file without disabling auditing.
	writer, logBuf := newWriter(t, "")

	writer.Write(Record{Operation: "metrics.cpu", RequestID: "req_1", Status: StatusSuccess})

	if writer.Path() != "" {
		t.Fatalf("Path = %q, want empty", writer.Path())
	}
	if !strings.Contains(logBuf.String(), "agent_audit") {
		t.Fatalf("records must still be logged: %s", logBuf.String())
	}
}

func TestUnwritablePathIsReportedButNotFatal(t *testing.T) {
	// A directory where a file is expected cannot be opened for append.
	dir := t.TempDir()
	blocked := filepath.Join(dir, "audit.log")
	if err := os.MkdirAll(blocked, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})

	writer, err := NewWriter(Options{Path: blocked, Log: log})
	if err == nil {
		t.Fatal("an unwritable audit path must be reported")
	}
	// The Agent must still be able to audit: refusing to start would turn a
	// logging problem into an outage.
	if writer == nil {
		t.Fatal("a usable writer must still be returned")
	}
	writer.Write(Record{Operation: "x", RequestID: "req_1", Status: StatusSuccess})
	if !strings.Contains(buf.String(), "agent_audit") {
		t.Fatalf("records must still be logged: %s", buf.String())
	}
}

func TestDirectoryIsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deep", "audit.log")
	writer, _ := newWriter(t, path)

	writer.Write(Record{Operation: "x", RequestID: "req_1", Status: StatusSuccess})

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the audit directory must be created: %v", err)
	}
}

func TestMetadataIsSerialised(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, _ := newWriter(t, path)

	writer.Write(Record{
		Operation: "job.submit",
		RequestID: "req_1",
		Status:    StatusSuccess,
		Detail:    map[string]any{"job_id": "job_abc", "mode": "async"},
	})

	records := readRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Detail["job_id"] != "job_abc" {
		t.Fatalf("detail not preserved: %+v", records[0].Detail)
	}
}

func TestConcurrentWritesProduceWholeRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	writer, _ := newWriter(t, path)

	// The Agent serves several connections at once; interleaved writes must
	// not corrupt each other into unparseable lines.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writer.Write(Record{
				Operation: "metrics.cpu",
				RequestID: "req_concurrent",
				Status:    StatusSuccess,
				Detail:    map[string]any{"padding": strings.Repeat("x", 200)},
			})
		}()
	}
	wg.Wait()

	records := readRecords(t, path)
	if len(records) != 50 {
		t.Fatalf("expected 50 whole records, got %d", len(records))
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")

	var buf bytes.Buffer
	writer, err := NewWriter(Options{
		Path: path,
		Log:  logger.New(logger.Options{Service: "agent", Output: &buf}),
	})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}

	if err := writer.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("second Close must be a no-op: %v", err)
	}

	// Writing after close must not panic; the record still reaches the log.
	writer.Write(Record{Operation: "x", RequestID: "req_1", Status: StatusSuccess})
}
