package files_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/files"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/logger"
)

// A file has no id — it has a path. Putting the path in resource_id, which is a
// uuid column, made every file audit write fail on a type cast: the API logged
// it and the panel carried on with no audit trail for file operations at all.
//
// CLAUDE.md section 15 requires those events, so this asserts the row actually
// lands rather than that the call was made.
func TestFileAuditEventsAreActuallyWritten(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	var logs bytes.Buffer
	log := logger.New(logger.Options{Service: "api", Level: "error", Output: &logs})

	service := files.NewService(files.ServiceOptions{
		Audit: audit.NewRecorder(deps.Pool, log),
	})

	const path = "/var/www/example.test/public/index.php"
	service.Record(ctx, files.Actor{IPAddress: "192.0.2.10"}, files.ActionDelete, path,
		map[string]any{"recursive": false})

	// RecordAsync writes off the request path, so the row appears shortly after.
	var action, resourceType string
	var metadata map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := deps.Pool.QueryRow(ctx, `
			SELECT action, resource_type, metadata
			FROM audit_logs
			WHERE action = $1
			ORDER BY created_at DESC
			LIMIT 1`, files.ActionDelete).Scan(&action, &resourceType, &metadata)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no audit row was written for a file deletion: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if resourceType != files.ResourceTypeFile {
		t.Fatalf("resource type = %q, want %q", resourceType, files.ResourceTypeFile)
	}
	// The path has to survive somewhere, or the event says a file was deleted
	// without saying which.
	if metadata["path"] != path {
		t.Fatalf("metadata path = %v, want %q", metadata["path"], path)
	}

	// The write must not have failed and been logged instead: that is exactly
	// how this went unnoticed the first time.
	if logs.Len() > 0 {
		t.Fatalf("the audit write logged an error: %s", logs.String())
	}
}
