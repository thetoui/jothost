package sessions

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/users"
)

const testHash = "$argon2id$v=19$m=47104,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA"

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	user, err := users.NewRepository(deps.Pool).Create(ctx, users.CreateParams{
		Username:     "sessionuser",
		PasswordHash: testHash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewRepository(deps.Pool), user.ID, ctx
}

func create(t *testing.T, repo *Repository, ctx context.Context, userID, tokenHash string) Session {
	t.Helper()

	session, err := repo.Create(ctx, CreateParams{
		UserID:    userID,
		TokenHash: tokenHash,
		IPAddress: "192.0.2.10",
		UserAgent: "test-agent",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return session
}

func TestCreateAndLookup(t *testing.T) {
	repo, userID, ctx := setup(t)

	created := create(t, repo, ctx, userID, "hash-1")
	if created.ID == "" {
		t.Fatal("session must have an id")
	}
	if created.RevokedAt != nil {
		t.Fatal("a new session must not be revoked")
	}
	if created.IPAddress == nil || *created.IPAddress != "192.0.2.10" {
		t.Fatalf("ip address not stored: %v", created.IPAddress)
	}

	byHash, err := repo.GetByTokenHash(ctx, "hash-1")
	if err != nil {
		t.Fatalf("GetByTokenHash: %v", err)
	}
	if byHash.ID != created.ID {
		t.Fatal("GetByTokenHash returned a different session")
	}

	byID, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.UserID != userID {
		t.Fatal("GetByID returned the wrong user")
	}
}

func TestRotateSwapsTheToken(t *testing.T) {
	repo, userID, ctx := setup(t)
	create(t, repo, ctx, userID, "old-hash")

	rotated, err := repo.Rotate(ctx, "old-hash", "new-hash", time.Now().Add(2*time.Hour))
	if err != nil {
		t.Fatalf("Rotate: %v", err)
	}
	if rotated.TokenHash != "new-hash" {
		t.Fatalf("token hash was not replaced: %q", rotated.TokenHash)
	}

	// The old hash must no longer resolve.
	if _, err := repo.GetByTokenHash(ctx, "old-hash"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the old token must stop working, got %v", err)
	}
	if _, err := repo.GetByTokenHash(ctx, "new-hash"); err != nil {
		t.Fatalf("the new token must resolve: %v", err)
	}
}

func TestRotateRejectsAStaleToken(t *testing.T) {
	repo, userID, ctx := setup(t)
	create(t, repo, ctx, userID, "hash-1")

	if _, err := repo.Rotate(ctx, "hash-1", "hash-2", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("first rotate: %v", err)
	}

	// Replaying the original token must fail: this is what makes refresh
	// reuse detectable.
	_, err := repo.Rotate(ctx, "hash-1", "hash-3", time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("replaying a rotated token must fail, got %v", err)
	}
}

func TestRotateRejectsRevokedSessions(t *testing.T) {
	repo, userID, ctx := setup(t)
	session := create(t, repo, ctx, userID, "hash-1")

	if err := repo.Revoke(ctx, session.ID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	_, err := repo.Rotate(ctx, "hash-1", "hash-2", time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("a revoked session must not rotate, got %v", err)
	}
}

func TestRotateRejectsExpiredSessions(t *testing.T) {
	repo, userID, ctx := setup(t)

	if _, err := repo.Create(ctx, CreateParams{
		UserID:    userID,
		TokenHash: "expired-hash",
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := repo.Rotate(ctx, "expired-hash", "new-hash", time.Now().Add(time.Hour))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("an expired session must not rotate, got %v", err)
	}
}

func TestConcurrentRotationHasASingleWinner(t *testing.T) {
	repo, userID, ctx := setup(t)
	create(t, repo, ctx, userID, "shared-hash")

	const attempts = 8
	results := make(chan error, attempts)

	for i := 0; i < attempts; i++ {
		go func(n int) {
			_, err := repo.Rotate(ctx, "shared-hash",
				"rotated-"+string(rune('a'+n)), time.Now().Add(time.Hour))
			results <- err
		}(i)
	}

	successes := 0
	for i := 0; i < attempts; i++ {
		if err := <-results; err == nil {
			successes++
		}
	}

	// Two clients presenting the same refresh token must not both receive a
	// new one, or the token family forks.
	if successes != 1 {
		t.Fatalf("expected exactly one successful rotation, got %d", successes)
	}
}

func TestRevokeIsIdempotent(t *testing.T) {
	repo, userID, ctx := setup(t)
	session := create(t, repo, ctx, userID, "hash-1")

	for i := 0; i < 2; i++ {
		if err := repo.Revoke(ctx, session.ID); err != nil {
			t.Fatalf("Revoke attempt %d: %v", i, err)
		}
	}

	revoked, err := repo.GetByID(ctx, session.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("revoked_at must be set")
	}
	if revoked.IsUsable(time.Now()) {
		t.Fatal("a revoked session must not be usable")
	}
}

func TestRevokeAllForUser(t *testing.T) {
	repo, userID, ctx := setup(t)

	for _, hash := range []string{"hash-1", "hash-2", "hash-3"} {
		create(t, repo, ctx, userID, hash)
	}

	revoked, err := repo.RevokeAllForUser(ctx, userID)
	if err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if len(revoked) != 3 {
		t.Fatalf("expected 3 revoked sessions, got %d", len(revoked))
	}

	// A second call must find nothing left to revoke.
	again, err := repo.RevokeAllForUser(ctx, userID)
	if err != nil {
		t.Fatalf("RevokeAllForUser: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected no sessions left to revoke, got %d", len(again))
	}
}

func TestIsUsable(t *testing.T) {
	now := time.Now()
	revokedAt := now.Add(-time.Minute)

	cases := []struct {
		name    string
		session Session
		want    bool
	}{
		{"active", Session{ExpiresAt: now.Add(time.Hour)}, true},
		{"expired", Session{ExpiresAt: now.Add(-time.Hour)}, false},
		{"revoked", Session{ExpiresAt: now.Add(time.Hour), RevokedAt: &revokedAt}, false},
	}

	for _, tc := range cases {
		if got := tc.session.IsUsable(now); got != tc.want {
			t.Fatalf("%s: IsUsable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestDeleteExpired(t *testing.T) {
	repo, userID, ctx := setup(t)

	if _, err := repo.Create(ctx, CreateParams{
		UserID: userID, TokenHash: "old", ExpiresAt: time.Now().Add(-2 * time.Hour),
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	create(t, repo, ctx, userID, "current")

	removed, err := repo.DeleteExpired(ctx, time.Now())
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 expired session removed, got %d", removed)
	}
	if _, err := repo.GetByTokenHash(ctx, "current"); err != nil {
		t.Fatalf("the live session must survive: %v", err)
	}
}

func TestMalformedClientDataIsStoredSafely(t *testing.T) {
	repo, userID, ctx := setup(t)

	// An unparsable IP must become NULL rather than failing the insert, and an
	// oversized user agent must be truncated instead of rejected.
	session, err := repo.Create(ctx, CreateParams{
		UserID:    userID,
		TokenHash: "hash-1",
		IPAddress: "not-an-ip-address",
		UserAgent: strings.Repeat("A", 4096),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.IPAddress != nil {
		t.Fatalf("an invalid ip must be stored as NULL, got %v", *session.IPAddress)
	}
	if session.UserAgent == nil || len(*session.UserAgent) != maxUserAgentLength {
		t.Fatal("an oversized user agent must be truncated")
	}
}

func TestIPWithPortIsNormalized(t *testing.T) {
	repo, userID, ctx := setup(t)

	session, err := repo.Create(ctx, CreateParams{
		UserID:    userID,
		TokenHash: "hash-1",
		IPAddress: "192.0.2.10:54321",
		ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if session.IPAddress == nil || *session.IPAddress != "192.0.2.10" {
		t.Fatalf("the port must be stripped, got %v", session.IPAddress)
	}
}
