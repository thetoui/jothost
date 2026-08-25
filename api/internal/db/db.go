// Package db owns the PostgreSQL connection pool.
package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Options configures the pool.
type Options struct {
	// URL is the connection string. It carries credentials and is never logged.
	URL string
	// MaxConns bounds the pool size.
	MaxConns int32
	// ConnectTimeout bounds establishing the initial connection.
	ConnectTimeout time.Duration
}

// Connect opens the pool and verifies it with a ping, so a bad DSN or an
// unreachable database fails at startup rather than on the first request.
func Connect(ctx context.Context, opts Options) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(opts.URL)
	if err != nil {
		// The parse error can echo the DSN, so it is not wrapped.
		return nil, errors.New("invalid DATABASE_URL")
	}

	if opts.MaxConns > 0 {
		cfg.MaxConns = opts.MaxConns
	}
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("failed to create database pool")
	}

	pingCtx, cancel := context.WithTimeout(ctx, opts.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("database unreachable: %w", redactConnError(err))
	}
	return pool, nil
}

// redactConnError strips a driver error down to a safe summary. pgx errors can
// include the connection string, which would then reach logs.
func redactConnError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return fmt.Errorf("postgres error %s", pgErr.Code)
	}
	return errors.New("connection failed")
}

// IsUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation, optionally for a specific constraint name.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}
