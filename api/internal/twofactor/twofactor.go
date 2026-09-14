// Package twofactor persists per-user TOTP enrolment.
//
// The shared secret is stored encrypted (DATABASE.md section 30); this package
// is the only place that decrypts it, and it never returns the plaintext
// secret except through Setup, which the user must see once to enrol.
package twofactor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/secrets"
)

// ErrNotEnrolled means the user has no TOTP record.
var ErrNotEnrolled = errors.New("two-factor authentication is not configured")

// Enrolment is a user's TOTP state.
type Enrolment struct {
	ID        string
	UserID    string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// secretEncrypted stays unexported: callers receive a verified result, not
	// the secret itself.
	secretEncrypted string
}

// Repository reads and writes TOTP enrolment.
type Repository struct {
	pool      *pgxpool.Pool
	encrypter *secrets.Encrypter
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool, encrypter *secrets.Encrypter) *Repository {
	return &Repository{pool: pool, encrypter: encrypter}
}

// encryptionContext binds a ciphertext to its owner, so a secret copied
// between rows fails to decrypt.
func encryptionContext(userID string) string {
	return "two_factor_auth:" + userID
}

const selectColumns = `id::text, user_id::text, secret_encrypted, enabled, created_at, updated_at`

func scanEnrolment(row pgx.Row) (Enrolment, error) {
	var e Enrolment
	err := row.Scan(&e.ID, &e.UserID, &e.secretEncrypted, &e.Enabled, &e.CreatedAt, &e.UpdatedAt)
	return e, err
}

// Get returns a user's enrolment.
func (r *Repository) Get(ctx context.Context, userID string) (Enrolment, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM two_factor_auth WHERE user_id = $1::uuid`, userID)

	enrolment, err := scanEnrolment(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Enrolment{}, ErrNotEnrolled
	}
	if err != nil {
		return Enrolment{}, fmt.Errorf("select two-factor enrolment: %w", err)
	}
	return enrolment, nil
}

// IsEnabled reports whether a user has completed TOTP enrolment.
func (r *Repository) IsEnabled(ctx context.Context, userID string) (bool, error) {
	var enabled bool
	err := r.pool.QueryRow(ctx,
		`SELECT enabled FROM two_factor_auth WHERE user_id = $1::uuid`, userID).Scan(&enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check two-factor enabled: %w", err)
	}
	return enabled, nil
}

// StartEnrolment generates a new secret and stores it in the pending
// (disabled) state, replacing any previous pending secret.
//
// It refuses to overwrite an already-enabled enrolment: re-running setup must
// not be a way to silently swap out a working second factor.
func (r *Repository) StartEnrolment(ctx context.Context, userID string) (string, error) {
	enabled, err := r.IsEnabled(ctx, userID)
	if err != nil {
		return "", err
	}
	if enabled {
		return "", errors.New("two-factor authentication is already enabled")
	}

	secret, err := secrets.NewTOTPSecret()
	if err != nil {
		return "", err
	}

	ciphertext, err := r.encrypter.Encrypt([]byte(secret), encryptionContext(userID))
	if err != nil {
		return "", fmt.Errorf("encrypt totp secret: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		INSERT INTO two_factor_auth (user_id, secret_encrypted, enabled)
		VALUES ($1::uuid, $2, FALSE)
		ON CONFLICT (user_id) DO UPDATE
		SET secret_encrypted = EXCLUDED.secret_encrypted,
		    enabled = FALSE,
		    updated_at = now()`,
		userID, ciphertext)
	if err != nil {
		return "", fmt.Errorf("store totp secret: %w", err)
	}
	return secret, nil
}

// VerifyCode checks a TOTP code against the stored secret.
func (r *Repository) VerifyCode(ctx context.Context, userID, code string, now time.Time) (bool, error) {
	enrolment, err := r.Get(ctx, userID)
	if err != nil {
		return false, err
	}

	secret, err := r.encrypter.Decrypt(enrolment.secretEncrypted, encryptionContext(userID))
	if err != nil {
		return false, fmt.Errorf("decrypt totp secret: %w", err)
	}
	// The plaintext secret is zeroed once used so it does not linger in a heap
	// dump longer than necessary.
	defer zero(secret)

	return secrets.VerifyTOTP(string(secret), code, now)
}

// Disable removes a user's enrolment entirely, so re-enabling starts from a
// fresh secret rather than reusing one that may have been exposed. Its
// recovery codes go with it, by the foreign key's cascade.
func (r *Repository) Disable(ctx context.Context, userID string) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM two_factor_auth WHERE user_id = $1::uuid`, userID)
	if err != nil {
		return fmt.Errorf("disable two-factor: %w", err)
	}
	return nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
