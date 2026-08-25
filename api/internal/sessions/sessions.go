// Package sessions persists refresh-token families.
//
// One row is one login. The refresh token is stored only as a SHA-256 hash,
// and every refresh rotates it. Presenting a rotated token is treated as theft
// and revokes the whole session (see Rotate).
package sessions

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session is a refresh-token family.
type Session struct {
	ID        string
	UserID    string
	TokenHash string
	IPAddress *string
	UserAgent *string
	ExpiresAt time.Time
	CreatedAt time.Time
	RevokedAt *time.Time
}

// IsUsable reports whether the session can still mint tokens.
func (s Session) IsUsable(now time.Time) bool {
	return s.RevokedAt == nil && now.Before(s.ExpiresAt)
}

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("session not found")
	// ErrReuseDetected means a rotated (already-superseded) refresh token was
	// presented, which indicates the token leaked.
	ErrReuseDetected = errors.New("refresh token reuse detected")
)

// maxUserAgentLength bounds a client-controlled string before storage.
const maxUserAgentLength = 512

// Repository reads and writes sessions.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectColumns = `
	id::text, user_id::text, token_hash, host(ip_address), user_agent,
	expires_at, created_at, revoked_at`

func scanSession(row pgx.Row) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.UserID, &s.TokenHash, &s.IPAddress, &s.UserAgent,
		&s.ExpiresAt, &s.CreatedAt, &s.RevokedAt)
	return s, err
}

// CreateParams describes a new session.
type CreateParams struct {
	UserID    string
	TokenHash string
	IPAddress string
	UserAgent string
	ExpiresAt time.Time
}

// Create opens a session.
func (r *Repository) Create(ctx context.Context, params CreateParams) (Session, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, token_hash, ip_address, user_agent, expires_at)
		VALUES ($1::uuid, $2, $3::inet, $4, $5)
		RETURNING `+selectColumns,
		params.UserID,
		params.TokenHash,
		normalizeIP(params.IPAddress),
		truncate(params.UserAgent, maxUserAgentLength),
		params.ExpiresAt)

	session, err := scanSession(row)
	if err != nil {
		return Session{}, fmt.Errorf("insert session: %w", err)
	}
	return session, nil
}

// GetByTokenHash looks up a session by its current refresh-token hash.
func (r *Repository) GetByTokenHash(ctx context.Context, tokenHash string) (Session, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM sessions WHERE token_hash = $1`, tokenHash)

	session, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("select session: %w", err)
	}
	return session, nil
}

// GetByID looks up a session by id.
func (r *Repository) GetByID(ctx context.Context, id string) (Session, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM sessions WHERE id = $1::uuid`, id)

	session, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("select session by id: %w", err)
	}
	return session, nil
}

// Rotate atomically swaps a session's refresh token for a new one and records
// the retired hash in session_token_history.
//
// The UPDATE matches on the *old* hash and on the session still being usable,
// so two concurrent refreshes with the same token cannot both succeed: the
// loser matches no row. Both steps share one transaction, so a session can
// never rotate without its old hash becoming detectable as retired.
func (r *Repository) Rotate(ctx context.Context, oldHash, newHash string, expiresAt time.Time) (Session, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Session{}, fmt.Errorf("begin rotate: %w", err)
	}
	defer func() {
		// A rollback after a successful commit is a no-op error.
		_ = tx.Rollback(ctx)
	}()

	row := tx.QueryRow(ctx, `
		UPDATE sessions
		SET token_hash = $2, expires_at = $3
		WHERE token_hash = $1
		  AND revoked_at IS NULL
		  AND expires_at > now()
		RETURNING `+selectColumns,
		oldHash, newHash, expiresAt)

	session, err := scanSession(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("rotate session: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO session_token_history (token_hash, session_id)
		VALUES ($1, $2::uuid)
		ON CONFLICT (token_hash) DO NOTHING`, oldHash, session.ID); err != nil {
		return Session{}, fmt.Errorf("record retired token: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Session{}, fmt.Errorf("commit rotate: %w", err)
	}
	return session, nil
}

// FindRetiredToken resolves a previously-rotated refresh-token hash to the
// session it belonged to.
//
// A hit means someone presented a token that has already been superseded,
// which is the signature of a stolen refresh token being replayed.
func (r *Repository) FindRetiredToken(ctx context.Context, tokenHash string) (string, error) {
	var sessionID string
	err := r.pool.QueryRow(ctx,
		`SELECT session_id::text FROM session_token_history WHERE token_hash = $1`,
		tokenHash).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select retired token: %w", err)
	}
	return sessionID, nil
}

// Revoke ends a single session. Revoking an already-revoked session is a no-op
// so logout is idempotent.
func (r *Repository) Revoke(ctx context.Context, id string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeAllForUser ends every active session for a user and returns their IDs
// so the caller can also drop the matching access tokens.
func (r *Repository) RevokeAllForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		UPDATE sessions SET revoked_at = now()
		WHERE user_id = $1::uuid AND revoked_at IS NULL
		RETURNING id::text`, userID)
	if err != nil {
		return nil, fmt.Errorf("revoke sessions for user: %w", err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan revoked session id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revoked sessions: %w", err)
	}
	return ids, nil
}

// DeleteExpired removes sessions that expired before cutoff, returning how
// many rows were removed.
func (r *Repository) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}

// normalizeIP returns a valid IP for the INET column, or nil.
//
// The address ultimately derives from a network peer, so an unparsable value
// is dropped rather than passed to Postgres, where it would raise an error.
func normalizeIP(raw string) *string {
	if raw == "" {
		return nil
	}
	// Strip a port if the caller passed host:port.
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	if net.ParseIP(raw) == nil {
		return nil
	}
	return &raw
}

// truncate bounds a client-supplied string.
func truncate(value string, max int) *string {
	if value == "" {
		return nil
	}
	if len(value) > max {
		value = value[:max]
	}
	return &value
}
