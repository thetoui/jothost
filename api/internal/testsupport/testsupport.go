// Package testsupport provides shared fixtures for tests that need a real
// PostgreSQL and Redis.
//
// Phase 1 authentication is mostly integration behaviour — schema constraints,
// transactional rotation, Redis expiry — so it is verified against the real
// engines rather than mocks that would encode assumptions instead of truth.
// Tests skip cleanly when the services are not configured, which keeps
// `go test ./...` usable on a bare checkout.
//
// # Running these tests
//
// The suite shares one database and one Redis instance, so package test
// binaries must not run concurrently:
//
//	go test -p 1 ./...
//
// Without -p 1, Go runs package binaries in parallel and their resets wipe
// each other's fixtures. The Makefile and CI both pass it.
package testsupport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/cache"
	"github.com/jothost/panel/api/internal/db"
	"github.com/jothost/panel/api/internal/db/migrate"
)

// TestEncryptionKey is a fixed AES-256 key for tests. It is not a secret and
// must never be used outside the test suite.
const TestEncryptionKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

// Env names the variables that point tests at their dependencies.
const (
	EnvDatabaseURL   = "TEST_DATABASE_URL"
	EnvRedisURL      = "TEST_REDIS_URL"
	EnvMigrationsDir = "TEST_MIGRATIONS_DIR"
)

// defaultMigrationsDir is the path from a package directory inside api/ up to
// the repository's migrations directory.
const defaultMigrationsDir = "../../../migrations"

// Deps holds the live dependencies handed to a test.
type Deps struct {
	Pool  *pgxpool.Pool
	Redis *redis.Client
}

// Connections are established once per test binary: migrations are expensive
// and a fresh pool per test would multiply that cost by the test count.
var (
	once      sync.Once
	shared    *Deps
	setupErr  error
	skipSetup bool
)

// Require returns connected dependencies with the schema migrated and all
// data reset, or skips the test when the services are not configured.
func Require(t *testing.T) *Deps {
	t.Helper()

	once.Do(connect)

	if skipSetup {
		t.Skipf("set %s and %s to run database-backed tests", EnvDatabaseURL, EnvRedisURL)
	}
	if setupErr != nil {
		t.Fatalf("test dependencies unavailable: %v", setupErr)
	}

	shared.Reset(t)
	return shared
}

// connect establishes the shared dependencies and applies migrations.
func connect() {
	databaseURL := os.Getenv(EnvDatabaseURL)
	redisURL := os.Getenv(EnvRedisURL)
	if databaseURL == "" || redisURL == "" {
		skipSetup = true
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := db.Connect(ctx, db.Options{
		URL:            databaseURL,
		MaxConns:       10,
		ConnectTimeout: 15 * time.Second,
	})
	if err != nil {
		setupErr = err
		return
	}

	redisClient, err := cache.Connect(ctx, cache.Options{
		URL:            redisURL,
		ConnectTimeout: 15 * time.Second,
	})
	if err != nil {
		pool.Close()
		setupErr = err
		return
	}

	migrationsDir := os.Getenv(EnvMigrationsDir)
	if migrationsDir == "" {
		migrationsDir = defaultMigrationsDir
	}
	if _, err := migrate.NewRunner(pool, migrationsDir).Up(ctx); err != nil {
		pool.Close()
		if closeErr := redisClient.Close(); closeErr != nil {
			err = closeErr
		}
		setupErr = err
		return
	}

	shared = &Deps{Pool: pool, Redis: redisClient}
}

// Reset clears all mutable data, leaving the seeded roles and permissions in
// place so RBAC behaves as it does in a real install.
func (d *Deps) Reset(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// audit_logs has triggers rejecting DELETE; TRUNCATE bypasses row triggers,
	// which is what makes a clean slate possible while the API itself still
	// cannot rewrite history.
	_, err := d.Pool.Exec(ctx, `
		TRUNCATE TABLE audit_logs, two_factor_auth, session_token_history, sessions, user_roles, users
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("reset database: %v", err)
	}

	if err := d.Redis.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("reset redis: %v", err)
	}
}

// IsolatedSchema returns a pool bound to a private, empty PostgreSQL schema.
//
// Migration tests need a database with no migrations applied. Reusing the
// shared schema would make them silently no-op — a pending version number
// would already be recorded as applied — so they get their own namespace,
// dropped when the test finishes.
func IsolatedSchema(t *testing.T) *pgxpool.Pool {
	t.Helper()

	deps := Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate schema name: %v", err)
	}
	schema := "test_" + hex.EncodeToString(suffix)

	if _, err := deps.Pool.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		if _, err := deps.Pool.Exec(dropCtx,
			`DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`); err != nil {
			t.Errorf("drop schema %s: %v", schema, err)
		}
	})

	cfg, err := pgxpool.ParseConfig(os.Getenv(EnvDatabaseURL))
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	cfg.MaxConns = 3
	// Every connection in this pool resolves unqualified names inside the
	// private schema.
	cfg.ConnConfig.RuntimeParams["search_path"] = schema

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open isolated pool: %v", err)
	}
	t.Cleanup(pool.Close)

	return pool
}
