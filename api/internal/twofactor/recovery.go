package twofactor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Recovery codes: the way back into an account whose authenticator is lost.
//
// Each is a one-time replacement for a TOTP code. They are shown once, when
// two-factor is enabled or when the user asks for a new set, and only their
// hashes are stored.

const (
	// RecoveryCodeCount is how many codes a set holds.
	RecoveryCodeCount = 10

	// recoveryCodeLength is the characters in a code, not counting the dashes
	// it is displayed with. Sixteen characters from a 31-letter alphabet is
	// about 79 bits: far past what trying every code against a leaked hash can
	// reach, which is what lets a plain hash stand in for a password hash.
	recoveryCodeLength = 16

	// recoveryAlphabet leaves out 0, 1, i, l and o, which are misread when a
	// code is copied off paper.
	recoveryAlphabet = "23456789abcdefghjkmnpqrstuvwxyz"

	recoveryHashLabel = "jothost two-factor recovery code v1"
)

// ErrNotEnabled means an operation needs two-factor to be on, and it is not.
var ErrNotEnabled = errors.New("two-factor authentication is not enabled")

// GenerateRecoveryCodes returns a fresh set of codes, formatted for display.
func GenerateRecoveryCodes() ([]string, error) {
	alphabet := big.NewInt(int64(len(recoveryAlphabet)))
	codes := make([]string, 0, RecoveryCodeCount)
	for len(codes) < RecoveryCodeCount {
		var raw strings.Builder
		for i := 0; i < recoveryCodeLength; i++ {
			// rand.Int draws uniformly, so no letter is likelier than another.
			n, err := rand.Int(rand.Reader, alphabet)
			if err != nil {
				return nil, fmt.Errorf("generate recovery code: %w", err)
			}
			raw.WriteByte(recoveryAlphabet[n.Int64()])
		}
		codes = append(codes, formatRecoveryCode(raw.String()))
	}
	return codes, nil
}

// formatRecoveryCode groups a code in fours: "abcd-efgh-jkmn-pqrs".
func formatRecoveryCode(raw string) string {
	var out strings.Builder
	for i, r := range raw {
		if i > 0 && i%4 == 0 {
			out.WriteByte('-')
		}
		out.WriteRune(r)
	}
	return out.String()
}

// NormalizeRecoveryCode reduces what somebody typed to the code itself, or
// returns false if it cannot be one.
//
// Case, spaces and dashes are forgiven, because a code copied by hand keeps
// none of them reliably. Anything else is refused before the database is
// asked, so a malformed guess costs nothing but a rate-limit slot.
func NormalizeRecoveryCode(input string) (string, bool) {
	var out strings.Builder
	for _, r := range strings.ToLower(input) {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case strings.ContainsRune(recoveryAlphabet, r):
			out.WriteRune(r)
		default:
			return "", false
		}
	}
	if out.Len() != recoveryCodeLength {
		return "", false
	}
	return out.String(), true
}

// hashRecoveryCode binds a normalized code to its user.
func hashRecoveryCode(userID, normalized string) string {
	sum := sha256.Sum256([]byte(recoveryHashLabel + "\x00" + userID + "\x00" + normalized))
	return hex.EncodeToString(sum[:])
}

// EnableWithRecoveryCodes turns a verified enrolment on and issues its first
// set of recovery codes, together: an account never has two-factor on without
// a way back in, and never has codes for a factor that is off.
func (r *Repository) EnableWithRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}

	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE two_factor_auth SET enabled = TRUE, updated_at = now()
			WHERE user_id = $1::uuid`, userID)
		if err != nil {
			return fmt.Errorf("enable two-factor: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotEnrolled
		}
		return replaceRecoveryCodes(ctx, tx, userID, codes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

// RegenerateRecoveryCodes replaces a user's codes with a new set. Every code
// from the old set stops working, used or not.
func (r *Repository) RegenerateRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes, err := GenerateRecoveryCodes()
	if err != nil {
		return nil, err
	}

	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var enabled bool
		err := tx.QueryRow(ctx,
			`SELECT enabled FROM two_factor_auth WHERE user_id = $1::uuid FOR UPDATE`, userID).Scan(&enabled)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !enabled) {
			return ErrNotEnabled
		}
		if err != nil {
			return fmt.Errorf("lock two-factor enrolment: %w", err)
		}
		return replaceRecoveryCodes(ctx, tx, userID, codes)
	})
	if err != nil {
		return nil, err
	}
	return codes, nil
}

func replaceRecoveryCodes(ctx context.Context, tx pgx.Tx, userID string, codes []string) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM two_factor_recovery_codes WHERE user_id = $1::uuid`, userID); err != nil {
		return fmt.Errorf("remove old recovery codes: %w", err)
	}
	for _, code := range codes {
		normalized, ok := NormalizeRecoveryCode(code)
		if !ok {
			return fmt.Errorf("generated recovery code is malformed")
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO two_factor_recovery_codes (user_id, code_hash)
			VALUES ($1::uuid, $2)`, userID, hashRecoveryCode(userID, normalized)); err != nil {
			return fmt.Errorf("store recovery code: %w", err)
		}
	}
	return nil
}

// UseRecoveryCode spends a code, and reports whether it was one and how many
// the user has left.
//
// Checking and spending are one UPDATE, so two requests racing with the same
// code cannot both succeed. A code only works while two-factor is enabled.
func (r *Repository) UseRecoveryCode(ctx context.Context, userID, input string) (bool, int, error) {
	normalized, ok := NormalizeRecoveryCode(input)
	if !ok {
		return false, 0, nil
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE two_factor_recovery_codes c
		   SET used_at = now()
		  FROM two_factor_auth t
		 WHERE c.user_id = $1::uuid
		   AND c.code_hash = $2
		   AND c.used_at IS NULL
		   AND t.user_id = c.user_id
		   AND t.enabled`,
		userID, hashRecoveryCode(userID, normalized))
	if err != nil {
		return false, 0, fmt.Errorf("use recovery code: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return false, 0, nil
	}

	remaining, err := r.RecoveryCodesRemaining(ctx, userID)
	if err != nil {
		return true, 0, err
	}
	return true, remaining, nil
}

// RecoveryCodesRemaining counts a user's unused codes.
func (r *Repository) RecoveryCodesRemaining(ctx context.Context, userID string) (int, error) {
	var remaining int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*) FROM two_factor_recovery_codes
		 WHERE user_id = $1::uuid AND used_at IS NULL`, userID).Scan(&remaining)
	if err != nil {
		return 0, fmt.Errorf("count recovery codes: %w", err)
	}
	return remaining, nil
}
