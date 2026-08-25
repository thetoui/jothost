package users

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/testsupport"
)

func newRepo(t *testing.T) (*Repository, context.Context) {
	t.Helper()
	deps := testsupport.Require(t)
	return NewRepository(deps.Pool), context.Background()
}

const testHash = "$argon2id$v=19$m=47104,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA"

func TestCreateAndGet(t *testing.T) {
	repo, ctx := newRepo(t)

	created, err := repo.Create(ctx, CreateParams{
		Username:     "admin",
		Email:        "admin@example.com",
		PasswordHash: testHash,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created user must have an id")
	}
	if created.Status != StatusActive {
		t.Fatalf("default status must be active, got %q", created.Status)
	}
	if created.LastLoginAt != nil {
		t.Fatal("a new user must have no last login")
	}

	byUsername, err := repo.GetByUsername(ctx, "admin")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if byUsername.ID != created.ID {
		t.Fatal("GetByUsername returned a different user")
	}

	byID, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if byID.Username != "admin" {
		t.Fatalf("unexpected username %q", byID.Username)
	}
}

func TestUsernameIsNormalized(t *testing.T) {
	repo, ctx := newRepo(t)

	created, err := repo.Create(ctx, CreateParams{
		Username:     "  MixedCase  ",
		PasswordHash: testHash,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Username != "mixedcase" {
		t.Fatalf("username must be normalised, got %q", created.Username)
	}

	// Lookup must find the account regardless of how it is typed.
	if _, err := repo.GetByUsername(ctx, "MIXEDCASE"); err != nil {
		t.Fatalf("lookup must be case-insensitive: %v", err)
	}
}

func TestDuplicateUsernameIsRejected(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.Create(ctx, CreateParams{Username: "admin", PasswordHash: testHash}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := repo.Create(ctx, CreateParams{Username: "ADMIN", PasswordHash: testHash})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("expected ErrUsernameTaken, got %v", err)
	}
}

func TestDuplicateEmailIsRejected(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.Create(ctx, CreateParams{
		Username: "first", Email: "shared@example.com", PasswordHash: testHash,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err := repo.Create(ctx, CreateParams{
		Username: "second", Email: "shared@example.com", PasswordHash: testHash,
	})
	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("expected ErrEmailTaken, got %v", err)
	}
}

func TestMultipleUsersMayOmitEmail(t *testing.T) {
	repo, ctx := newRepo(t)

	// An empty email must be stored as NULL, so the unique index does not
	// treat two address-less accounts as duplicates.
	for _, username := range []string{"first", "second"} {
		user, err := repo.Create(ctx, CreateParams{Username: username, PasswordHash: testHash})
		if err != nil {
			t.Fatalf("Create(%s): %v", username, err)
		}
		if user.Email != nil {
			t.Fatalf("expected a null email, got %v", *user.Email)
		}
	}
}

func TestInvalidUsernamesAreRejected(t *testing.T) {
	repo, ctx := newRepo(t)

	invalid := []string{
		"ab",                        // too short
		"-leading-dash",             // must start alphanumeric
		"has space",                 // space
		"has/slash",                 // path separator
		"admin;DROP TABLE users;--", // injection shaped
		"../../etc/passwd",          // traversal shaped
		strings.Repeat("a", 101),    // too long
		"",
	}

	for _, username := range invalid {
		if _, err := repo.Create(ctx, CreateParams{Username: username, PasswordHash: testHash}); err == nil {
			t.Fatalf("username %q must be rejected", username)
		}
	}
}

func TestInvalidEmailIsRejected(t *testing.T) {
	repo, ctx := newRepo(t)

	for _, email := range []string{"not-an-email", "@example.com", "user@", "a b@example.com"} {
		_, err := repo.Create(ctx, CreateParams{
			Username: "user", Email: email, PasswordHash: testHash,
		})
		if !errors.Is(err, ErrInvalidEmail) {
			t.Fatalf("email %q must be rejected, got %v", email, err)
		}
	}
}

func TestInvalidStatusIsRejected(t *testing.T) {
	repo, ctx := newRepo(t)

	_, err := repo.Create(ctx, CreateParams{
		Username: "user", PasswordHash: testHash, Status: "superuser",
	})
	if !errors.Is(err, ErrInvalidUserState) {
		t.Fatalf("expected ErrInvalidUserState, got %v", err)
	}
}

func TestGetMissingUser(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.GetByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := repo.GetByID(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTouchLastLogin(t *testing.T) {
	repo, ctx := newRepo(t)

	user, err := repo.Create(ctx, CreateParams{Username: "admin", PasswordHash: testHash})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := repo.TouchLastLogin(ctx, user.ID); err != nil {
		t.Fatalf("TouchLastLogin: %v", err)
	}

	updated, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.LastLoginAt == nil {
		t.Fatal("last_login_at must be set after TouchLastLogin")
	}
}

func TestUpdatePasswordHash(t *testing.T) {
	repo, ctx := newRepo(t)

	user, err := repo.Create(ctx, CreateParams{Username: "admin", PasswordHash: testHash})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	newHash := "$argon2id$v=19$m=47104,t=1,p=1$bmV3c2FsdG5ld3NhbHQ$bmV3aGFzaG5ld2hhc2huZXdoYXNo"
	if err := repo.UpdatePasswordHash(ctx, user.ID, newHash); err != nil {
		t.Fatalf("UpdatePasswordHash: %v", err)
	}

	updated, err := repo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if updated.PasswordHash != newHash {
		t.Fatal("password hash was not updated")
	}
}

func TestIsActive(t *testing.T) {
	if !(User{Status: StatusActive}).IsActive() {
		t.Fatal("an active user must report active")
	}
	for _, status := range []string{StatusDisabled, StatusLocked, ""} {
		if (User{Status: status}).IsActive() {
			t.Fatalf("status %q must not report active", status)
		}
	}
}

func TestCount(t *testing.T) {
	repo, ctx := newRepo(t)

	count, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("a reset database must have no users, got %d", count)
	}

	if _, err := repo.Create(ctx, CreateParams{Username: "admin", PasswordHash: testHash}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	count, err = repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 user, got %d", count)
	}
}
