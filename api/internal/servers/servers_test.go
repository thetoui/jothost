package servers

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

func TestRegisterCreatesAServer(t *testing.T) {
	repo, ctx := newRepo(t)

	server, err := repo.Register(ctx, RegisterParams{
		Hostname:     "web01.example.com",
		OSName:       "Debian GNU/Linux",
		OSVersion:    "12",
		Kernel:       "6.1.0-18-amd64",
		Architecture: "amd64",
		IPv4:         "192.0.2.10",
		AgentVersion: "0.1.0-dev",
		Status:       StatusOnline,
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if server.ID == "" {
		t.Fatal("a registered server must have an id")
	}
	if server.Hostname != "web01.example.com" {
		t.Fatalf("hostname = %q", server.Hostname)
	}
	if server.Status != StatusOnline {
		t.Fatalf("status = %q", server.Status)
	}
	if server.IPv4 == nil || *server.IPv4 != "192.0.2.10" {
		t.Fatalf("ipv4 = %v", server.IPv4)
	}
	if server.OSName == nil || *server.OSName != "Debian GNU/Linux" {
		t.Fatalf("os_name = %v", server.OSName)
	}
}

func TestRegisterIsIdempotent(t *testing.T) {
	repo, ctx := newRepo(t)

	first, err := repo.Register(ctx, RegisterParams{Hostname: "web01", OSVersion: "11"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A restart must refresh the existing row rather than accumulate a new one
	// every time the API starts.
	second, err := repo.Register(ctx, RegisterParams{Hostname: "web01", OSVersion: "12"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if second.ID != first.ID {
		t.Fatalf("re-registration must keep the same id: %s vs %s", first.ID, second.ID)
	}
	if second.OSVersion == nil || *second.OSVersion != "12" {
		t.Fatalf("details must be refreshed, got %v", second.OSVersion)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected exactly one server, got %d", len(list))
	}
}

func TestRegisterKeepsAKnownAddressWhenTheNewOneIsMissing(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.Register(ctx, RegisterParams{Hostname: "web01", IPv4: "192.0.2.10"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// A later registration that could not determine the address must not erase
	// what was already known.
	server, err := repo.Register(ctx, RegisterParams{Hostname: "web01"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if server.IPv4 == nil || *server.IPv4 != "192.0.2.10" {
		t.Fatalf("the known address must be preserved, got %v", server.IPv4)
	}
}

func TestRegisterStoresUnknownFactsAsNull(t *testing.T) {
	repo, ctx := newRepo(t)

	// "Unknown" and "empty" must stay distinguishable.
	server, err := repo.Register(ctx, RegisterParams{Hostname: "web01"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if server.OSName != nil {
		t.Fatalf("os_name should be null, got %q", *server.OSName)
	}
	if server.AgentVersion != nil {
		t.Fatalf("agent_version should be null, got %q", *server.AgentVersion)
	}
	if server.Status != StatusUnknown {
		t.Fatalf("status should default to unknown, got %q", server.Status)
	}
}

func TestRegisterRejectsBadInput(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.Register(ctx, RegisterParams{Hostname: ""}); !errors.Is(err, ErrInvalidHostname) {
		t.Fatalf("an empty hostname must be rejected, got %v", err)
	}
	if _, err := repo.Register(ctx, RegisterParams{Hostname: "   "}); !errors.Is(err, ErrInvalidHostname) {
		t.Fatalf("a blank hostname must be rejected, got %v", err)
	}
	if _, err := repo.Register(ctx, RegisterParams{Hostname: strings.Repeat("a", 256)}); !errors.Is(err, ErrInvalidHostname) {
		t.Fatalf("an oversized hostname must be rejected, got %v", err)
	}
	if _, err := repo.Register(ctx, RegisterParams{Hostname: "web01", Status: "haunted"}); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("an invalid status must be rejected, got %v", err)
	}
}

func TestRegisterDropsAnUnparsableAddress(t *testing.T) {
	repo, ctx := newRepo(t)

	// The address comes from host inspection; a malformed one must not fail
	// the whole registration.
	server, err := repo.Register(ctx, RegisterParams{Hostname: "web01", IPv4: "not-an-ip"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if server.IPv4 != nil {
		t.Fatalf("an unparsable address must be stored as null, got %q", *server.IPv4)
	}
}

func TestGetAndGetByHostname(t *testing.T) {
	repo, ctx := newRepo(t)

	created, err := repo.Register(ctx, RegisterParams{Hostname: "web01"})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	byID, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if byID.Hostname != "web01" {
		t.Fatalf("unexpected server: %+v", byID)
	}

	byHostname, err := repo.GetByHostname(ctx, "web01")
	if err != nil {
		t.Fatalf("GetByHostname: %v", err)
	}
	if byHostname.ID != created.ID {
		t.Fatal("GetByHostname returned a different server")
	}
}

func TestGetMissingServer(t *testing.T) {
	repo, ctx := newRepo(t)

	if _, err := repo.Get(ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if _, err := repo.GetByHostname(ctx, "nowhere"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestListIsEmptyOnAFreshDatabase(t *testing.T) {
	repo, ctx := newRepo(t)

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected no servers, got %d", len(list))
	}
	// A nil slice would serialise as null rather than [].
	if list == nil {
		t.Fatal("List must return a non-nil slice")
	}
}

func TestSetStatus(t *testing.T) {
	repo, ctx := newRepo(t)

	server, err := repo.Register(ctx, RegisterParams{Hostname: "web01", Status: StatusOnline})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if err := repo.SetStatus(ctx, server.ID, StatusOffline); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}

	updated, err := repo.Get(ctx, server.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if updated.Status != StatusOffline {
		t.Fatalf("status = %q, want offline", updated.Status)
	}

	if err := repo.SetStatus(ctx, server.ID, "haunted"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("an invalid status must be rejected, got %v", err)
	}
	if err := repo.SetStatus(ctx, "00000000-0000-0000-0000-000000000000", StatusOnline); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
