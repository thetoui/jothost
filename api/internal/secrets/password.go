// Package secrets provides the cryptographic primitives used by
// authentication: password hashing, opaque token generation, authenticated
// encryption for secrets at rest, and TOTP.
//
// No primitive is implemented here from scratch. Argon2id comes from
// golang.org/x/crypto, AES-GCM and HMAC from the standard library.
package secrets

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters. These follow the OWASP Password Storage Cheat Sheet's
// second recommended option (46 MiB, t=1, p=1), which resists GPU cracking
// while remaining comfortable for a control panel that authenticates rarely.
const (
	argonMemoryKiB  uint32 = 47104 // 46 MiB
	argonIterations uint32 = 1
	argonThreads    uint8  = 1
	argonSaltLength        = 16
	argonKeyLength  uint32 = 32
)

// Password policy. A control panel guards root-equivalent access, so the
// minimum is deliberately above the common 8-character default.
const (
	MinPasswordLength = 12
	MaxPasswordLength = 1024 // bounds Argon2 work from a hostile input
)

// ErrInvalidHash indicates a stored hash that cannot be parsed.
var ErrInvalidHash = errors.New("invalid password hash")

// ErrPasswordTooShort and ErrPasswordTooLong report policy violations.
var (
	ErrPasswordTooShort = fmt.Errorf("password must be at least %d characters", MinPasswordLength)
	ErrPasswordTooLong  = fmt.Errorf("password must be at most %d characters", MaxPasswordLength)
)

// ValidatePassword enforces the length policy. Length is the only mechanical
// rule applied: composition rules push users toward predictable substitutions
// without adding real entropy.
func ValidatePassword(password string) error {
	switch {
	case len(password) < MinPasswordLength:
		return ErrPasswordTooShort
	case len(password) > MaxPasswordLength:
		return ErrPasswordTooLong
	default:
		return nil
	}
}

// HashPassword derives an Argon2id hash in the standard PHC string format, so
// the parameters travel with the hash and can be changed without invalidating
// existing credentials.
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}

	salt := make([]byte, argonSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, argonIterations, argonMemoryKiB, argonThreads, argonKeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encodedHash. The comparison
// is constant time, and the parameters are read from the hash rather than the
// current constants so old hashes keep verifying after a parameter change.
func VerifyPassword(password, encodedHash string) (bool, error) {
	params, salt, want, err := decodeHash(encodedHash)
	if err != nil {
		return false, err
	}
	if len(password) > MaxPasswordLength {
		// Refuse to spend Argon2 work on an oversized candidate.
		return false, nil
	}

	got := argon2.IDKey([]byte(password), salt, params.iterations, params.memory, params.threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// NeedsRehash reports whether a stored hash uses weaker parameters than the
// current policy, so it can be upgraded on the next successful login.
func NeedsRehash(encodedHash string) bool {
	params, _, _, err := decodeHash(encodedHash)
	if err != nil {
		return true
	}
	return params.memory < argonMemoryKiB ||
		params.iterations < argonIterations ||
		params.threads != argonThreads
}

type argonParams struct {
	memory     uint32
	iterations uint32
	threads    uint8
}

// decodeHash parses the PHC string representation.
func decodeHash(encodedHash string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return argonParams{}, nil, nil, fmt.Errorf("%w: unsupported argon2 version %d", ErrInvalidHash, version)
	}

	var params argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.iterations, &params.threads); err != nil {
		return argonParams{}, nil, nil, ErrInvalidHash
	}
	if params.memory == 0 || params.iterations == 0 || params.threads == 0 {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return argonParams{}, nil, nil, ErrInvalidHash
	}

	return params, salt, key, nil
}
