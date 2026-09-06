package databases_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/databases"
)

// errWrite stands in for a socket or a client that has gone.
var errWrite = errors.New("the write failed")

// Export and import used to be links to the Backups page. These are about the
// difference: a dump of one database, as a file, in and out.

func TestAnExportStreamsTheDumpAndDiscardsIt(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()

	dump := []byte("-- dump\nCREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n")
	fixture.agent.dump = dump

	created, err := fixture.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := fixture.service.BeginExport(ctx, created.Database.ID,
		databases.Actor{}, "req-export")
	if err != nil {
		t.Fatalf("BeginExport: %v", err)
	}
	if info.Filename != "shop.sql" {
		t.Fatalf("filename = %q, want shop.sql", info.Filename)
	}
	if info.Size != int64(len(dump)) {
		t.Fatalf("size = %d, want %d", info.Size, len(dump))
	}

	var out bytes.Buffer
	written, err := fixture.service.StreamExport(ctx, info, "req-export", &out)
	if err != nil {
		t.Fatalf("StreamExport: %v", err)
	}
	if written != int64(len(dump)) || out.String() != string(dump) {
		t.Fatalf("streamed %d bytes: %q", written, out.String())
	}

	// The dump is somebody's data sitting on the host's disk. The one thing
	// that wanted it has now had it.
	if fixture.agent.discarded == 0 {
		t.Fatal("the export was never discarded")
	}
}

func TestAnExportIsDiscardedEvenWhenTheClientHangsUp(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()

	fixture.agent.dump = bytes.Repeat([]byte("x"), 4096)
	created, err := fixture.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	info, err := fixture.service.BeginExport(ctx, created.Database.ID,
		databases.Actor{}, "req-export")
	if err != nil {
		t.Fatalf("BeginExport: %v", err)
	}

	// A writer that refuses, standing in for a browser that went away.
	_, err = fixture.service.StreamExport(ctx, info, "req-export", refusingWriter{})
	if err == nil {
		t.Fatal("a failing write was not reported")
	}
	if fixture.agent.discarded == 0 {
		t.Fatal("the export was left on disk after the client hung up")
	}
}

func TestAnImportStreamsToTheAgentAndLoads(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()

	created, err := fixture.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	body := strings.Repeat("INSERT INTO t VALUES (1);\n", 500)

	written, err := fixture.service.Import(ctx, created.Database.ID,
		databases.Actor{}, "req-import", "given-to-me.sql", strings.NewReader(body))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if written != int64(len(body)) {
		t.Fatalf("wrote %d bytes, want %d", written, len(body))
	}
	if string(fixture.agent.uploaded) != body {
		t.Fatal("what reached the agent is not what was uploaded")
	}

	if !containsCall(fixture.agent.calls, "import:mariadb:shop:") {
		t.Fatalf("the load was never asked for: %v", fixture.agent.calls)
	}
}

func TestAFailedUploadLeavesNothingBehind(t *testing.T) {
	fixture := newFixture(t)
	ctx := context.Background()

	created, err := fixture.service.Create(ctx, databases.CreateRequest{Name: "shop"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fixture.agent.failWrite = errWrite

	_, err = fixture.service.Import(ctx, created.Database.ID,
		databases.Actor{}, "req-import", "given-to-me.sql", strings.NewReader("INSERT INTO t VALUES (1);"))
	if err == nil {
		t.Fatal("a failing upload was not reported")
	}
	// A partial upload left behind is somebody's data on disk that nothing
	// will ever ask for again.
	if fixture.agent.discarded == 0 {
		t.Fatal("the partial upload was not discarded")
	}
	// And nothing was loaded from it.
	if containsCall(fixture.agent.calls, "import:") {
		t.Fatalf("a load was attempted from a failed upload: %v", fixture.agent.calls)
	}
}

// refusingWriter stands in for a client that has gone away.
type refusingWriter struct{}

func (refusingWriter) Write([]byte) (int, error) { return 0, errWrite }

func containsCall(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
