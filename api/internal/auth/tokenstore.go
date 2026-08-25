package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/secrets"
)

// Access tokens are opaque random strings kept in Redis rather than signed
// JWTs. The trade-off is documented in docs/PHASE1.md: a lookup per request in
// exchange for revocation that takes effect immediately, and no signature
// verification code to get wrong.
const (
	accessTokenPrefix = "jothost:access:"
	// sessionIndexPrefix holds the set of access tokens minted for a session,
	// so revoking the session can drop all of them.
	sessionIndexPrefix = "jothost:session_tokens:"
	// mfaChallengePrefix holds the short-lived token issued between a correct
	// password and a correct TOTP code.
	mfaChallengePrefix = "jothost:mfa:"
)

// ErrTokenNotFound means the token is unknown, expired, or revoked. The three
// are deliberately indistinguishable to the caller.
var ErrTokenNotFound = errors.New("token not found")

// AccessClaims is the identity cached alongside an access token.
//
// Permissions are captured at login. A role change therefore takes effect on
// the next refresh (at most AccessTokenTTL later) rather than instantly; this
// is documented as a known limitation.
type AccessClaims struct {
	UserID      string    `json:"user_id"`
	Username    string    `json:"username"`
	SessionID   string    `json:"session_id"`
	Permissions []string  `json:"permissions"`
	Roles       []string  `json:"roles"`
	IssuedAt    time.Time `json:"issued_at"`
}

// TokenStore issues and validates opaque access tokens.
type TokenStore struct {
	redis *redis.Client
	ttl   time.Duration
}

// NewTokenStore builds a TokenStore.
func NewTokenStore(client *redis.Client, ttl time.Duration) *TokenStore {
	return &TokenStore{redis: client, ttl: ttl}
}

// TTL reports the access-token lifetime.
func (s *TokenStore) TTL() time.Duration { return s.ttl }

// Issue mints an access token for claims and returns the plaintext token.
//
// Only the hash is stored, so a dump of Redis does not yield usable tokens.
func (s *TokenStore) Issue(ctx context.Context, claims AccessClaims) (string, error) {
	token, err := secrets.NewToken()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode access claims: %w", err)
	}

	key := accessTokenPrefix + secrets.HashToken(token)
	indexKey := sessionIndexPrefix + claims.SessionID

	pipe := s.redis.TxPipeline()
	pipe.Set(ctx, key, payload, s.ttl)
	pipe.SAdd(ctx, indexKey, key)
	// The index outlives its tokens slightly so a revoke arriving late still
	// finds them; it is removed outright on revoke.
	pipe.Expire(ctx, indexKey, s.ttl+time.Minute)
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store access token: %w", err)
	}

	return token, nil
}

// Lookup resolves a plaintext access token to its claims.
func (s *TokenStore) Lookup(ctx context.Context, token string) (AccessClaims, error) {
	if token == "" {
		return AccessClaims{}, ErrTokenNotFound
	}

	payload, err := s.redis.Get(ctx, accessTokenPrefix+secrets.HashToken(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return AccessClaims{}, ErrTokenNotFound
	}
	if err != nil {
		return AccessClaims{}, fmt.Errorf("read access token: %w", err)
	}

	var claims AccessClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return AccessClaims{}, fmt.Errorf("decode access claims: %w", err)
	}
	return claims, nil
}

// RevokeToken drops a single access token.
func (s *TokenStore) RevokeToken(ctx context.Context, token string) error {
	if err := s.redis.Del(ctx, accessTokenPrefix+secrets.HashToken(token)).Err(); err != nil {
		return fmt.Errorf("revoke access token: %w", err)
	}
	return nil
}

// RevokeSession drops every access token issued for a session. This is what
// makes logout immediate rather than "effective once the token expires".
func (s *TokenStore) RevokeSession(ctx context.Context, sessionID string) error {
	indexKey := sessionIndexPrefix + sessionID

	members, err := s.redis.SMembers(ctx, indexKey).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("list session tokens: %w", err)
	}

	pipe := s.redis.TxPipeline()
	if len(members) > 0 {
		pipe.Del(ctx, members...)
	}
	pipe.Del(ctx, indexKey)
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("revoke session tokens: %w", err)
	}
	return nil
}

// RevokeSessions drops the access tokens of many sessions.
func (s *TokenStore) RevokeSessions(ctx context.Context, sessionIDs []string) error {
	for _, id := range sessionIDs {
		if err := s.RevokeSession(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// MFAChallenge is the state carried between password verification and TOTP
// verification.
type MFAChallenge struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	IPAddress string `json:"ip_address"`
}

// IssueMFAChallenge stores a challenge and returns its opaque token.
//
// The challenge proves only that the password was correct. It cannot be used
// as an access token: it lives under a different key prefix and is consumed by
// VerifyMFAChallenge.
func (s *TokenStore) IssueMFAChallenge(ctx context.Context, challenge MFAChallenge, ttl time.Duration) (string, error) {
	token, err := secrets.NewToken()
	if err != nil {
		return "", err
	}

	payload, err := json.Marshal(challenge)
	if err != nil {
		return "", fmt.Errorf("encode mfa challenge: %w", err)
	}

	key := mfaChallengePrefix + secrets.HashToken(token)
	if err := s.redis.Set(ctx, key, payload, ttl).Err(); err != nil {
		return "", fmt.Errorf("store mfa challenge: %w", err)
	}
	return token, nil
}

// PeekMFAChallenge resolves a challenge token without consuming it, so a
// mistyped TOTP code does not force the user back to the password step.
func (s *TokenStore) PeekMFAChallenge(ctx context.Context, token string) (MFAChallenge, error) {
	if token == "" {
		return MFAChallenge{}, ErrTokenNotFound
	}

	payload, err := s.redis.Get(ctx, mfaChallengePrefix+secrets.HashToken(token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return MFAChallenge{}, ErrTokenNotFound
	}
	if err != nil {
		return MFAChallenge{}, fmt.Errorf("read mfa challenge: %w", err)
	}

	var challenge MFAChallenge
	if err := json.Unmarshal(payload, &challenge); err != nil {
		return MFAChallenge{}, fmt.Errorf("decode mfa challenge: %w", err)
	}
	return challenge, nil
}

// ConsumeMFAChallenge deletes a challenge, so a successful verification cannot
// be replayed.
func (s *TokenStore) ConsumeMFAChallenge(ctx context.Context, token string) error {
	if err := s.redis.Del(ctx, mfaChallengePrefix+secrets.HashToken(token)).Err(); err != nil {
		return fmt.Errorf("consume mfa challenge: %w", err)
	}
	return nil
}
