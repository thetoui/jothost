// Package users owns the user model and its persistence.
package users

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/db"
)

// Status values for a user account.
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
	StatusLocked   = "locked"
)

// User is a panel account.
type User struct {
	ID       string  `json:"id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
	// PasswordHash is never serialised: the json tag keeps it out of every
	// response even if a handler returns the struct directly by mistake.
	PasswordHash string     `json:"-"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastLoginAt  *time.Time `json:"last_login_at"`
}

// IsActive reports whether the account may authenticate.
func (u User) IsActive() bool { return u.Status == StatusActive }

// Errors returned by the repository.
var (
	ErrNotFound         = errors.New("user not found")
	ErrUsernameTaken    = errors.New("username already exists")
	ErrEmailTaken       = errors.New("email already exists")
	ErrInvalidUsername  = errors.New("username must be 3-100 characters of lowercase letters, digits, dot, dash, or underscore, starting with a letter or digit")
	ErrInvalidEmail     = errors.New("email is not valid")
	ErrInvalidUserState = errors.New("invalid user status")
)

// usernamePattern mirrors the CHECK constraint in migration 0001 so a bad
// value is rejected before it reaches the database.
var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{2,99}$`)

// emailPattern is a deliberately loose sanity check. Real validation of an
// address is delivery, not a regular expression.
var emailPattern = regexp.MustCompile(`^[^@\s]+@[^@\s.]+\.[^@\s]+$`)

// NormalizeUsername lowercases and trims a username for storage and lookup.
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// ValidateUsername checks a username against the storage rules.
func ValidateUsername(username string) error {
	if !usernamePattern.MatchString(username) {
		return ErrInvalidUsername
	}
	return nil
}

// ValidateEmail checks an optional email address.
func ValidateEmail(email string) error {
	if email == "" {
		return nil
	}
	if len(email) > 255 || !emailPattern.MatchString(email) {
		return ErrInvalidEmail
	}
	return nil
}

// Repository reads and writes users.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// selectColumns is shared by every read so the scan order cannot drift.
const selectColumns = `
	id::text, username, email, password_hash, status,
	created_at, updated_at, last_login_at`

func scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.Status,
		&u.CreatedAt, &u.UpdatedAt, &u.LastLoginAt)
	return u, err
}

// CreateParams describes a new user.
type CreateParams struct {
	Username     string
	Email        string
	PasswordHash string
	Status       string
}

// Create inserts a user and returns it.
func (r *Repository) Create(ctx context.Context, params CreateParams) (User, error) {
	username := NormalizeUsername(params.Username)
	if err := ValidateUsername(username); err != nil {
		return User{}, err
	}

	email := strings.ToLower(strings.TrimSpace(params.Email))
	if err := ValidateEmail(email); err != nil {
		return User{}, err
	}

	status := params.Status
	if status == "" {
		status = StatusActive
	}
	switch status {
	case StatusActive, StatusDisabled, StatusLocked:
	default:
		return User{}, ErrInvalidUserState
	}

	// An empty email must be stored as NULL, not "", so the unique index does
	// not collide across users without an address.
	var emailArg *string
	if email != "" {
		emailArg = &email
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, status)
		VALUES ($1, $2, $3, $4)
		RETURNING `+selectColumns,
		username, emailArg, params.PasswordHash, status)

	user, err := scanUser(row)
	if err != nil {
		switch {
		case db.IsUniqueViolation(err, "users_username_key"):
			return User{}, ErrUsernameTaken
		case db.IsUniqueViolation(err, "users_email_key"):
			return User{}, ErrEmailTaken
		default:
			return User{}, fmt.Errorf("insert user: %w", err)
		}
	}
	return user, nil
}

// GetByUsername looks up a user by their normalised username.
func (r *Repository) GetByUsername(ctx context.Context, username string) (User, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM users WHERE username = $1`,
		NormalizeUsername(username))

	user, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user by username: %w", err)
	}
	return user, nil
}

// GetByID looks up a user by id.
func (r *Repository) GetByID(ctx context.Context, id string) (User, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM users WHERE id = $1::uuid`, id)

	user, err := scanUser(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user by id: %w", err)
	}
	return user, nil
}

// TouchLastLogin records a successful authentication.
func (r *Repository) TouchLastLogin(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET last_login_at = now(), updated_at = now() WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("update last_login_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePasswordHash replaces a user's password hash.
func (r *Repository) UpdatePasswordHash(ctx context.Context, id, hash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1::uuid`, id, hash)
	if err != nil {
		return fmt.Errorf("update password_hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Count returns the number of user accounts, used to detect a fresh install.
func (r *Repository) Count(ctx context.Context) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return count, nil
}
