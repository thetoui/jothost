package dns_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/dns"
	"github.com/jothost/panel/api/internal/testsupport"
)

// The built-in template on a fresh install.
//
// The shared test database is migrated with an empty servers table and reset
// before each test, so a server inserted here has no template - the state a
// new installation is in once the API has registered the host it runs on,
// because on a fresh database migration 0028 ran before that host existed.

func freshServer(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool := testsupport.Require(t).Pool

	var id string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO servers (hostname, status)
		VALUES ('fresh-install', 'online')
		RETURNING id::text`).Scan(&id); err != nil {
		t.Fatalf("register the server: %v", err)
	}
	return pool, id
}

func templatesOf(t *testing.T, pool *pgxpool.Pool, serverID string) []dns.Template {
	t.Helper()
	templates, err := dns.NewRepository(pool, nil).Templates(context.Background(), serverID)
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	return templates
}

func TestAFreshInstallGetsTheBuiltinTemplateWhenItsServerIsRegistered(t *testing.T) {
	pool, serverID := freshServer(t)
	ctx := context.Background()

	// The defect, stated: the migration alone leaves a new host with nothing.
	// If this ever starts failing, the migration seeds fresh installs after
	// all and EnsureBuiltinTemplate is a harmless no-op - not a problem, but
	// worth knowing.
	if got := templatesOf(t, pool, serverID); len(got) != 0 {
		t.Fatalf("expected the migration to leave a server registered after it with no templates, found %d", len(got))
	}

	created, err := dns.EnsureBuiltinTemplate(ctx, pool, serverID)
	if err != nil {
		t.Fatalf("EnsureBuiltinTemplate: %v", err)
	}
	if !created {
		t.Fatal("a server with no templates was not given one")
	}

	templates := templatesOf(t, pool, serverID)
	if len(templates) != 1 {
		t.Fatalf("want exactly the built-in template, got %d", len(templates))
	}
	builtin := templates[0]
	if !builtin.Builtin || !builtin.IsDefault {
		t.Fatalf("the seeded template is builtin=%v default=%v; new zones would not use it",
			builtin.Builtin, builtin.IsDefault)
	}

	// The same two records migration 0028 writes, so a new install and an
	// upgraded one start their zones identically.
	want := [][3]string{{"@", "A", dns.PlaceholderIP}, {"www", "A", dns.PlaceholderIP}}
	if len(builtin.Records) != len(want) {
		t.Fatalf("want %d records, got %d: %+v", len(want), len(builtin.Records), builtin.Records)
	}
	for i, w := range want {
		r := builtin.Records[i]
		if r.Name != w[0] || r.Type != w[1] || r.Value != w[2] {
			t.Fatalf("record %d is %s %s %s, want %s %s %s", i, r.Name, r.Type, r.Value, w[0], w[1], w[2])
		}
	}
}

func TestSeedingTheBuiltinTemplateTwiceWritesItOnce(t *testing.T) {
	// It runs on every start of the API.
	pool, serverID := freshServer(t)
	ctx := context.Background()

	for attempt := 1; attempt <= 3; attempt++ {
		if _, err := dns.EnsureBuiltinTemplate(ctx, pool, serverID); err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
	}

	templates := templatesOf(t, pool, serverID)
	if len(templates) != 1 || len(templates[0].Records) != 2 {
		t.Fatalf("after three starts: %d templates, first with %d records", len(templates), len(templates[0].Records))
	}
}

func TestAServerWithItsOwnTemplateIsLeftAlone(t *testing.T) {
	// An operator who made their own template the default has chosen what new
	// zones get. Adding a built-in beside it would either fight their default
	// for the flag or sit there looking like something they had deleted.
	pool, serverID := freshServer(t)
	ctx := context.Background()

	if _, err := pool.Exec(ctx, `
		INSERT INTO dns_templates (server_id, name, is_default)
		VALUES ($1::uuid, 'Mine', TRUE)`, serverID); err != nil {
		t.Fatalf("write the operator's template: %v", err)
	}

	created, err := dns.EnsureBuiltinTemplate(ctx, pool, serverID)
	if err != nil {
		t.Fatalf("EnsureBuiltinTemplate: %v", err)
	}
	if created {
		t.Fatal("a built-in template was added to a server that already had one of its own")
	}
	if got := templatesOf(t, pool, serverID); len(got) != 1 || got[0].Name != "Mine" {
		t.Fatalf("the operator's templates changed: %+v", got)
	}
}
