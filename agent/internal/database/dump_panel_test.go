package database

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The panel's own database is reserved, and the ordinary dump - reached from
// a database download as well as a backup - must keep refusing it.
func TestDumpRefusesThePanelDatabase(t *testing.T) {
	provider := &Postgres{}
	err := provider.Dump(context.Background(), "jothost", filepath.Join(t.TempDir(), "d.sql"))
	if !errors.Is(err, validate.ErrInvalidDatabaseName) {
		t.Fatalf("Dump(jothost) = %v, want ErrInvalidDatabaseName", err)
	}
}

// DumpPanel accepts that name, and gets as far as looking for pg_dump, which
// this test host deliberately lacks. It still checks the name's syntax.
func TestDumpPanelAcceptsThePanelDatabaseButNotAnyString(t *testing.T) {
	provider := &Postgres{}
	destination := filepath.Join(t.TempDir(), "d.sql")

	err := provider.DumpPanel(context.Background(), "jothost", destination)
	if !errors.Is(err, ErrDumpUnsupported) {
		t.Fatalf("DumpPanel(jothost) = %v, want it past validation to ErrDumpUnsupported", err)
	}

	err = provider.DumpPanel(context.Background(), "jothost; DROP", destination)
	if !errors.Is(err, validate.ErrInvalidDatabaseName) {
		t.Fatalf("DumpPanel(malformed) = %v, want ErrInvalidDatabaseName", err)
	}
}

func TestPostgresIsAPanelDumper(t *testing.T) {
	var _ PanelDumper = (*Postgres)(nil)
}
