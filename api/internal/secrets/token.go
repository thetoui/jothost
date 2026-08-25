package secrets

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// tokenBytes is the entropy in every opaque token. 32 bytes (256 bits) makes
// guessing infeasible and matches the SHA-256 digest used to store it.
const tokenBytes = 32

// NewToken returns a URL-safe random token. Access tokens, refresh tokens, and
// the short-lived MFA challenge token all use this: they are opaque bearer
// secrets with no structure for an attacker to exploit.
func NewToken() (string, error) {
	buf := make([]byte, tokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken returns the hex-encoded SHA-256 of a token.
//
// Tokens are high-entropy random values, so a fast hash is correct here: there
// is nothing to brute force, and a slow KDF would only add latency to every
// authenticated request. This is why tokens are hashed with SHA-256 while
// passwords use Argon2id.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// EqualTokenHash compares two token hashes in constant time.
func EqualTokenHash(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
