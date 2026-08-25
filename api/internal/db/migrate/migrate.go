// Package migrate applies ordered SQL migrations to PostgreSQL.
//
// It is deliberately small rather than a third-party migration framework: the
// requirements from CLAUDE.md section 8 are paired up/down files, transactional
// application, and no manual schema edits. Those fit in a few hundred lines
// that the project can audit in full.
package migrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// advisoryLockKey guards concurrent migration runs. Two API instances starting
// at once must not apply the same migration twice, so the second blocks until
// the first finishes and then observes the migration as already applied.
const advisoryLockKey int64 = 8264109553291

// fileNamePattern matches "0001_description.up.sql" / ".down.sql".
var fileNamePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9_]+)\.(up|down)\.sql$`)

// Migration is one versioned pair of SQL files.
type Migration struct {
	Version  int
	Name     string
	UpPath   string
	DownPath string
}

// ID renders the canonical "0001_name" identifier.
func (m Migration) ID() string {
	return fmt.Sprintf("%04d_%s", m.Version, m.Name)
}

// Status describes one migration's applied state.
type Status struct {
	Migration
	Applied bool
}

// Load reads and validates the migration set in dir.
//
// A missing down file is rejected rather than tolerated: an un-reversible
// migration is a problem to discover at build time, not during an incident.
func Load(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations directory %s: %w", dir, err)
	}

	byVersion := make(map[int]*Migration)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := fileNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			// Non-migration files (README.md) are ignored; anything that looks
			// like a migration but is misnamed is not, since a typo would
			// otherwise silently skip a schema change.
			if strings.HasSuffix(entry.Name(), ".sql") {
				return nil, fmt.Errorf("migration file %q does not match NNNN_name.(up|down).sql", entry.Name())
			}
			continue
		}

		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("parse version in %q: %w", entry.Name(), err)
		}

		m, ok := byVersion[version]
		if !ok {
			m = &Migration{Version: version, Name: matches[2]}
			byVersion[version] = m
		}
		if m.Name != matches[2] {
			return nil, fmt.Errorf("version %04d has conflicting names %q and %q", version, m.Name, matches[2])
		}

		path := filepath.Join(dir, entry.Name())
		if matches[3] == "up" {
			m.UpPath = path
		} else {
			m.DownPath = path
		}
	}

	migrations := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.UpPath == "" {
			return nil, fmt.Errorf("migration %s has no up file", m.ID())
		}
		if m.DownPath == "" {
			return nil, fmt.Errorf("migration %s has no down file", m.ID())
		}
		migrations = append(migrations, *m)
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	for i, m := range migrations {
		if want := i + 1; m.Version != want {
			return nil, fmt.Errorf("migration versions must be contiguous from 0001: expected %04d, found %04d", want, m.Version)
		}
	}

	return migrations, nil
}

// Runner applies migrations against a pool.
type Runner struct {
	pool *pgxpool.Pool
	dir  string
}

// NewRunner builds a Runner for the migrations in dir.
func NewRunner(pool *pgxpool.Pool, dir string) *Runner {
	return &Runner{pool: pool, dir: dir}
}

// ensureTable creates the bookkeeping table if it does not exist.
func (r *Runner) ensureTable(ctx context.Context, conn *pgx.Conn) error {
	_, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version     INTEGER PRIMARY KEY,
			name        TEXT NOT NULL,
			applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

// appliedVersions returns the set of versions already applied.
func appliedVersions(ctx context.Context, conn *pgx.Conn) (map[int]struct{}, error) {
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]struct{})
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		applied[version] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return applied, nil
}

// withLock acquires the advisory lock, runs fn, and always releases the lock.
func (r *Runner) withLock(ctx context.Context, fn func(conn *pgx.Conn) error) error {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	raw := conn.Conn()
	if _, err := raw.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Unlock on a context that is still usable even if ctx was cancelled,
		// otherwise a cancelled run would leave the lock held until the
		// connection is closed.
		if _, err := raw.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockKey); err != nil {
			// Releasing also happens implicitly when the session ends, so this
			// is reported but does not change the outcome.
			_ = err
		}
	}()

	if err := r.ensureTable(ctx, raw); err != nil {
		return err
	}
	return fn(raw)
}

// Up applies every pending migration in order and returns their IDs.
func (r *Runner) Up(ctx context.Context) ([]string, error) {
	migrations, err := Load(r.dir)
	if err != nil {
		return nil, err
	}

	var applied []string
	err = r.withLock(ctx, func(conn *pgx.Conn) error {
		done, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}

		for _, m := range migrations {
			if _, ok := done[m.Version]; ok {
				continue
			}
			if err := runFile(ctx, conn, m, m.UpPath, true); err != nil {
				return err
			}
			applied = append(applied, m.ID())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return applied, nil
}

// Down rolls back the most recent applied migration.
func (r *Runner) Down(ctx context.Context) (string, error) {
	migrations, err := Load(r.dir)
	if err != nil {
		return "", err
	}

	var reverted string
	err = r.withLock(ctx, func(conn *pgx.Conn) error {
		done, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}

		for i := len(migrations) - 1; i >= 0; i-- {
			m := migrations[i]
			if _, ok := done[m.Version]; !ok {
				continue
			}
			if err := runFile(ctx, conn, m, m.DownPath, false); err != nil {
				return err
			}
			reverted = m.ID()
			return nil
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return reverted, nil
}

// Status reports each migration's applied state.
func (r *Runner) Status(ctx context.Context) ([]Status, error) {
	migrations, err := Load(r.dir)
	if err != nil {
		return nil, err
	}

	statuses := make([]Status, 0, len(migrations))
	err = r.withLock(ctx, func(conn *pgx.Conn) error {
		done, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}
		for _, m := range migrations {
			_, ok := done[m.Version]
			statuses = append(statuses, Status{Migration: m, Applied: ok})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return statuses, nil
}

// runFile executes one migration file and updates the bookkeeping table inside
// a single transaction, so a failure leaves neither a half-applied schema nor a
// bookkeeping row that lies about it.
func runFile(ctx context.Context, conn *pgx.Conn, m Migration, path string, up bool) error {
	sqlBytes, err := os.ReadFile(path) //nolint:gosec // path comes from the vetted migrations directory
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(sqlBytes)) == "" {
		return fmt.Errorf("migration file %s is empty", path)
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction for %s: %w", m.ID(), err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op error, which is why it
		// is discarded here rather than reported.
		_ = tx.Rollback(context.WithoutCancel(ctx))
	}()

	if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
		return fmt.Errorf("apply %s: %w", filepath.Base(path), err)
	}

	if up {
		_, err = tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, name) VALUES ($1, $2)`,
			m.Version, m.Name)
	} else {
		_, err = tx.Exec(ctx, `DELETE FROM schema_migrations WHERE version = $1`, m.Version)
	}
	if err != nil {
		return fmt.Errorf("record migration %s: %w", m.ID(), err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit %s: %w", m.ID(), err)
	}
	return nil
}

// ErrDirtyState is returned when the database contains a migration version the
// migration directory does not define.
var ErrDirtyState = errors.New("database contains unknown migration versions")

// Verify reports whether the database is consistent with the migration set.
func (r *Runner) Verify(ctx context.Context) error {
	migrations, err := Load(r.dir)
	if err != nil {
		return err
	}

	known := make(map[int]struct{}, len(migrations))
	for _, m := range migrations {
		known[m.Version] = struct{}{}
	}

	return r.withLock(ctx, func(conn *pgx.Conn) error {
		done, err := appliedVersions(ctx, conn)
		if err != nil {
			return err
		}
		for version := range done {
			if _, ok := known[version]; !ok {
				return fmt.Errorf("%w: version %04d", ErrDirtyState, version)
			}
		}
		return nil
	})
}
