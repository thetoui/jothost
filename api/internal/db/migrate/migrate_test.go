package migrate_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/db/migrate"
	"github.com/jothost/panel/api/internal/testsupport"
)

// repoMigrations is the project's real migration directory.
const repoMigrations = "../../../../migrations"

func migrationsDir() string {
	if dir := os.Getenv(testsupport.EnvMigrationsDir); dir != "" {
		return dir
	}
	return repoMigrations
}

func TestLoadProjectMigrations(t *testing.T) {
	migrations, err := migrate.Load(migrationsDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("the project must define migrations")
	}

	// Versions must be contiguous and ordered, and every migration must be
	// reversible (CLAUDE.md section 8).
	for i, m := range migrations {
		if m.Version != i+1 {
			t.Fatalf("migration %d has version %d", i, m.Version)
		}
		if m.UpPath == "" || m.DownPath == "" {
			t.Fatalf("migration %s is missing a file", m.ID())
		}
	}
}

func TestLoadRejectsMissingDownFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_only_up.up.sql", "SELECT 1;")

	_, err := migrate.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "no down file") {
		t.Fatalf("an un-reversible migration must be rejected, got %v", err)
	}
}

func TestLoadRejectsMissingUpFile(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_only_down.down.sql", "SELECT 1;")

	_, err := migrate.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "no up file") {
		t.Fatalf("a down-only migration must be rejected, got %v", err)
	}
}

func TestLoadRejectsGaps(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_first.up.sql", "SELECT 1;")
	write(t, dir, "0001_first.down.sql", "SELECT 1;")
	write(t, dir, "0003_third.up.sql", "SELECT 1;")
	write(t, dir, "0003_third.down.sql", "SELECT 1;")

	// A gap usually means a migration was deleted or never committed, which
	// would silently skip a schema change.
	_, err := migrate.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("a version gap must be rejected, got %v", err)
	}
}

func TestLoadRejectsMisnamedSQLFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_ok.up.sql", "SELECT 1;")
	write(t, dir, "0001_ok.down.sql", "SELECT 1;")
	write(t, dir, "add_index.sql", "SELECT 1;")

	_, err := migrate.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("a misnamed SQL file must be rejected, got %v", err)
	}
}

func TestLoadIgnoresNonSQLFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_ok.up.sql", "SELECT 1;")
	write(t, dir, "0001_ok.down.sql", "SELECT 1;")
	write(t, dir, "README.md", "# notes")

	migrations, err := migrate.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("expected 1 migration, got %d", len(migrations))
	}
}

func TestLoadRejectsConflictingNames(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "0001_alpha.up.sql", "SELECT 1;")
	write(t, dir, "0001_beta.down.sql", "SELECT 1;")

	_, err := migrate.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "conflicting names") {
		t.Fatalf("mismatched names for one version must be rejected, got %v", err)
	}
}

func TestLoadMissingDirectory(t *testing.T) {
	if _, err := migrate.Load(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("a missing directory must be reported")
	}
}

func TestMigrationIDFormat(t *testing.T) {
	m := migrate.Migration{Version: 7, Name: "add_widgets"}
	if got := m.ID(); got != "0007_add_widgets" {
		t.Fatalf("ID() = %q, want 0007_add_widgets", got)
	}
}

func TestStatusReportsAppliedMigrations(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	runner := migrate.NewRunner(deps.Pool, migrationsDir())

	statuses, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) == 0 {
		t.Fatal("expected migration statuses")
	}
	// testsupport already ran them.
	for _, s := range statuses {
		if !s.Applied {
			t.Fatalf("migration %s should be applied", s.ID())
		}
	}
}

func TestUpIsIdempotent(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	runner := migrate.NewRunner(deps.Pool, migrationsDir())

	// Everything is already applied, so a second run must be a no-op rather
	// than an error or a duplicate application.
	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("expected no pending migrations, got %v", applied)
	}
}

func TestVerifyAcceptsAKnownSchema(t *testing.T) {
	deps := testsupport.Require(t)

	if err := migrate.NewRunner(deps.Pool, migrationsDir()).Verify(context.Background()); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestVerifyRejectsUnknownVersions(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	// Simulate a database migrated by a newer build than the one running.
	if _, err := deps.Pool.Exec(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (9999, 'from_the_future')`); err != nil {
		t.Fatalf("insert future migration: %v", err)
	}
	t.Cleanup(func() {
		if _, err := deps.Pool.Exec(ctx, `DELETE FROM schema_migrations WHERE version = 9999`); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})

	err := migrate.NewRunner(deps.Pool, migrationsDir()).Verify(ctx)
	if err == nil || !strings.Contains(err.Error(), "unknown migration versions") {
		t.Fatalf("expected a dirty-state error, got %v", err)
	}
}

func TestFailedMigrationDoesNotRecordItself(t *testing.T) {
	// An empty schema is required: against the shared one, version 0001 is
	// already recorded and this migration would be skipped rather than run.
	pool := testsupport.IsolatedSchema(t)
	ctx := context.Background()

	// A migration directory whose only migration is invalid SQL.
	dir := t.TempDir()
	write(t, dir, "0001_broken.up.sql", "CREATE TABLE ( this is not sql;")
	write(t, dir, "0001_broken.down.sql", "SELECT 1;")

	if _, err := migrate.NewRunner(pool, dir).Up(ctx); err == nil {
		t.Fatal("invalid SQL must fail the migration")
	}

	// The bookkeeping table must not claim a migration that did not apply.
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM schema_migrations WHERE name = 'broken'`).Scan(&count); err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if count != 0 {
		t.Fatal("a failed migration must not be recorded as applied")
	}
}

func TestEmptyMigrationIsRejected(t *testing.T) {
	pool := testsupport.IsolatedSchema(t)
	ctx := context.Background()

	dir := t.TempDir()
	write(t, dir, "0001_empty.up.sql", "   \n")
	write(t, dir, "0001_empty.down.sql", "SELECT 1;")

	if _, err := migrate.NewRunner(pool, dir).Up(ctx); err == nil {
		t.Fatal("an empty migration file must be rejected")
	}
}

func TestUpAndDownRoundTrip(t *testing.T) {
	pool := testsupport.IsolatedSchema(t)
	ctx := context.Background()

	runner := migrate.NewRunner(pool, migrationsDir())

	applied, err := runner.Up(ctx)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if len(applied) == 0 {
		t.Fatal("expected migrations to be applied in an empty schema")
	}

	// Every down migration must actually reverse its up, or a rollback during
	// an incident leaves the schema wedged.
	for i := len(applied) - 1; i >= 0; i-- {
		reverted, err := runner.Down(ctx)
		if err != nil {
			t.Fatalf("Down: %v", err)
		}
		if reverted != applied[i] {
			t.Fatalf("rolled back %q, expected %q", reverted, applied[i])
		}
	}

	statuses, err := runner.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, s := range statuses {
		if s.Applied {
			t.Fatalf("migration %s should have been rolled back", s.ID())
		}
	}

	// Re-applying after a full rollback must work, which is what makes a
	// rollback recoverable rather than terminal.
	if _, err := runner.Up(ctx); err != nil {
		t.Fatalf("re-applying after a full rollback must work: %v", err)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}
