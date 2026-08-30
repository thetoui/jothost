package websites_test

import (
	"context"
	"testing"

	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// fakePHP answers with a socket for the one website it is told about.
type fakePHP struct{ websiteID, socket string }

func (f fakePHP) SocketFor(_ context.Context, websiteID string) (string, error) {
	if websiteID == f.websiteID {
		return f.socket, nil
	}
	return "", nil
}

type fakeCertificates struct{ websiteID string }

func (f fakeCertificates) CertificateFor(_ context.Context, websiteID string) (string, string, bool, error) {
	if websiteID != f.websiteID {
		return "", "", false, nil
	}
	return "/etc/letsencrypt/live/example.test/fullchain.pem",
		"/etc/letsencrypt/live/example.test/privkey.pem", true, nil
}

type fakeProxy struct {
	websiteID string
	port      int
}

func (f fakeProxy) ProxyPortFor(_ context.Context, websiteID string) (int, error) {
	if websiteID == f.websiteID {
		return f.port, nil
	}
	return 0, nil
}

// The Agent rewrites the whole vhost from the payload it is given, so a field
// left out is a feature switched off. Adding an alias used to send only the
// names — which turned PHP, HTTPS and the reverse proxy off as a side effect.
func TestVhostPayloadCarriesEverythingThatShapesTheVhost(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	site := activeParent(t, service, repo, "serving.test")

	repo.SetServingSources(websites.ServingSources{
		PHP:          fakePHP{websiteID: site.ID, socket: "/run/php/pool.sock"},
		Certificates: fakeCertificates{websiteID: site.ID},
	})

	payload, err := repo.VhostPayload(ctx, site)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}

	if payload["php_socket"] != "/run/php/pool.sock" {
		t.Fatalf("php_socket = %v, want the site's pool", payload["php_socket"])
	}
	if payload["certificate_path"] == nil || payload["private_key_path"] == nil {
		t.Fatalf("the certificate is missing from the payload: %+v", payload)
	}
	if payload["document_root"] != site.DocumentRoot {
		t.Fatalf("document_root = %v, want %q", payload["document_root"], site.DocumentRoot)
	}
	if payload["system_user"] != site.SystemUser {
		t.Fatalf("system_user = %v, want %q", payload["system_user"], site.SystemUser)
	}
}

// A site served by an application has no PHP block: the renderer refuses a
// configuration carrying both, so resolving the conflict here is what keeps a
// proxied site's vhost valid rather than rejected.
func TestVhostPayloadPrefersTheApplicationOverPHP(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	site := activeParent(t, service, repo, "proxied.test")

	repo.SetServingSources(websites.ServingSources{
		PHP:   fakePHP{websiteID: site.ID, socket: "/run/php/pool.sock"},
		Proxy: fakeProxy{websiteID: site.ID, port: 3000},
	})

	payload, err := repo.VhostPayload(ctx, site)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload["proxy_port"] != 3000 {
		t.Fatalf("proxy_port = %v, want 3000", payload["proxy_port"])
	}
	if _, present := payload["php_socket"]; present {
		t.Fatal("a proxied site must not also carry a PHP socket")
	}
}

// Inheriting means serving through the parent's pool. Asking for the
// subdomain's own — which does not exist — would quietly turn PHP off for it.
func TestVhostPayloadOfAnInheritingSubdomainUsesTheParentsPool(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "inherit.test")
	repo.SetServingSources(websites.ServingSources{
		PHP: fakePHP{websiteID: parent.ID, socket: "/run/php/parent.sock"},
	})

	created, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID:    parent.ID,
		Name:        "shop",
		PHPPoolMode: validate.PHPPoolInherit,
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}

	payload, err := repo.VhostPayload(ctx, created.Website)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if payload["php_socket"] != "/run/php/parent.sock" {
		t.Fatalf("php_socket = %v, want the parent's pool", payload["php_socket"])
	}

	// A subdomain with its own pool must not fall back to the parent's: it has
	// none yet, and serving through the parent's would run its PHP as the
	// parent's user.
	dedicated, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID:       parent.ID,
		Name:           "own",
		PHPPoolMode:    validate.PHPPoolDedicated,
		SystemUserMode: validate.SystemUserDedicated,
	})
	if err != nil {
		t.Fatalf("create dedicated subdomain: %v", err)
	}
	payload, err = repo.VhostPayload(ctx, dedicated.Website)
	if err != nil {
		t.Fatalf("build payload: %v", err)
	}
	if _, present := payload["php_socket"]; present {
		t.Fatalf("a subdomain with its own pool must not use the parent's: %v",
			payload["php_socket"])
	}
}
