package websites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// hybridHost puts the fixture's server into the hybrid arrangement.
func hybridHost(t *testing.T, repo *websites.Repository, serverID string) {
	t.Helper()
	if err := repo.SetWebserverMode(context.Background(), serverID,
		validate.WebserverHybrid); err != nil {
		t.Fatalf("set hybrid mode: %v", err)
	}
}

func TestWebserverModeStartsAsNginxAlone(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "mode.test"})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}

	// Every host that existed before this phase keeps serving the way it did.
	mode, err := repo.WebserverMode(ctx, created.Website.ServerID)
	if err != nil {
		t.Fatalf("read mode: %v", err)
	}
	if mode != validate.WebserverNginx {
		t.Fatalf("mode = %q, want %q", mode, validate.WebserverNginx)
	}
}

func TestAssignBackendPortIsStableAndUnique(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	one, err := service.Create(ctx, websites.CreateRequest{Domain: "one.backend.test"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	two, err := service.Create(ctx, websites.CreateRequest{Domain: "two.backend.test"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}
	first, second := one.Website, two.Website

	firstPort, err := repo.AssignBackendPort(ctx, first)
	if err != nil {
		t.Fatalf("assign first: %v", err)
	}
	if err := validate.BackendPort(firstPort); err != nil {
		t.Fatalf("assigned port %d is outside the range: %v", firstPort, err)
	}

	secondPort, err := repo.AssignBackendPort(ctx, second)
	if err != nil {
		t.Fatalf("assign second: %v", err)
	}
	// Two sites on one port means one Apache vhost silently serving the
	// other's traffic: the first VirtualHost on a port answers for every name
	// that matches no other.
	if firstPort == secondPort {
		t.Fatalf("both sites were given port %d", firstPort)
	}

	// Asking again returns what the site already holds. Switching a host to
	// nginx and back should not renumber every backend on it.
	reloaded, err := repo.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	again, err := repo.AssignBackendPort(ctx, reloaded)
	if err != nil {
		t.Fatalf("assign first again: %v", err)
	}
	if again != firstPort {
		t.Fatalf("the port moved from %d to %d", firstPort, again)
	}
}

// The vhost payload is what the Agent writes the configuration from, so what
// is in it is what the host does.
func TestVhostPayloadCarriesTheBackendOnlyInHybridMode(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	site := activeParent(t, service, repo, "hybrid.test")

	port, err := repo.AssignBackendPort(ctx, site)
	if err != nil {
		t.Fatalf("assign port: %v", err)
	}
	site.ApachePort = &port

	// The port is allocated, but the host still runs nginx alone: the payload
	// must not mention Apache, or a site would be proxied to a backend nothing
	// was told to create.
	payload, err := repo.VhostPayload(ctx, site)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if _, present := payload["apache_port"]; present {
		t.Fatalf("nginx-only host produced an Apache backend: %v", payload)
	}

	hybridHost(t, repo, site.ServerID)

	payload, err = repo.VhostPayload(ctx, site)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["apache_port"] != port {
		t.Fatalf("apache_port = %v, want %d", payload["apache_port"], port)
	}
	// .htaccess is on by default: a host that enabled Apache and then found
	// rewrites ignored would have gained nothing but a second process.
	if payload["allow_override"] != true {
		t.Fatalf("allow_override = %v, want true", payload["allow_override"])
	}
}

// An application answers every path. Apache is there for .htaccess and PHP,
// neither of which an application uses, so it stays out of the path entirely.
func TestVhostPayloadPrefersAnApplicationOverApache(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	site := activeParent(t, service, repo, "app-over-apache.test")
	hybridHost(t, repo, site.ServerID)

	port, err := repo.AssignBackendPort(ctx, site)
	if err != nil {
		t.Fatalf("assign port: %v", err)
	}
	site.ApachePort = &port

	repo.SetServingSources(websites.ServingSources{
		Proxy: fakeProxy{websiteID: site.ID, port: 3000},
	})

	payload, err := repo.VhostPayload(ctx, site)
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload["proxy_port"] != 3000 {
		t.Fatalf("proxy_port = %v, want the application's 3000", payload["proxy_port"])
	}
	if _, present := payload["apache_port"]; present {
		t.Fatal("a site served by an application must not also go through Apache")
	}
}

func TestSetWebserverModeRefusesAnythingElse(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "modes.test"})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	serverID := created.Website.ServerID

	if err = repo.SetWebserverMode(ctx, serverID, "apache"); !errors.Is(
		err, validate.ErrInvalidWebserverMode) {
		t.Fatalf("expected ErrInvalidWebserverMode, got %v", err)
	}
	if err := repo.SetWebserverMode(ctx, serverID, validate.WebserverHybrid); err != nil {
		t.Fatalf("set hybrid: %v", err)
	}
	mode, err := repo.WebserverMode(ctx, serverID)
	if err != nil {
		t.Fatalf("read mode: %v", err)
	}
	if mode != validate.WebserverHybrid {
		t.Fatalf("mode = %q, want hybrid", mode)
	}
}

// A mode change rewrites every site, so the listing it works from has to be
// the sites that actually have configuration on the host — subdomains included.
func TestListForReconcileCoversEverySiteThatIsServed(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "reconcile.test")
	sub, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop",
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}
	if err := repo.SetStatus(ctx, sub.Website.ID, websites.StatusActive); err != nil {
		t.Fatalf("activate subdomain: %v", err)
	}

	// One still being created: it already has a job in flight that will write
	// its configuration from the record, which by then holds the new mode.
	pending, err := service.Create(ctx, websites.CreateRequest{Domain: "pending.reconcile.test"})
	if err != nil {
		t.Fatalf("create pending: %v", err)
	}

	sites, err := repo.ListForReconcile(ctx, parent.ServerID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	found := map[string]bool{}
	for _, site := range sites {
		found[site.PrimaryDomain] = true
	}
	if !found["reconcile.test"] || !found["shop.reconcile.test"] {
		t.Fatalf("a served site was left out: %v", found)
	}
	if found[pending.Website.PrimaryDomain] {
		t.Fatal("a site still being created was queued for a rewrite as well")
	}
}
