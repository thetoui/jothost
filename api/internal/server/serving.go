package server

import (
	"context"
	"errors"

	nodepkg "github.com/jothost/panel/api/internal/node"
	phppkg "github.com/jothost/panel/api/internal/php"
	sslpkg "github.com/jothost/panel/api/internal/ssl"
	"github.com/jothost/panel/api/internal/websites"
)

// The adapters below let the websites package assemble a site's complete vhost
// without importing php, ssl or node — each of which imports websites, so a
// direct dependency would be a cycle the compiler refuses.
//
// They live in the server package because this is the layer that already knows
// about every feature: wiring is its job, and putting them here keeps the
// knowledge of which features exist in one place instead of spreading it
// through the packages themselves.

// phpPoolSource answers which FPM socket a site serves PHP through.
type phpPoolSource struct{ repo *phppkg.Repository }

func (p phpPoolSource) SocketFor(ctx context.Context, websiteID string) (string, error) {
	pool, err := p.repo.GetPool(ctx, websiteID)
	if err != nil {
		// A site with no pool serves no PHP. That is an answer, not a failure,
		// and returning an error here would make every vhost rewrite fail on
		// every static site on the host.
		if errors.Is(err, phppkg.ErrPoolNotFound) {
			return "", nil
		}
		return "", err
	}
	return pool.SocketPath, nil
}

// certificateSource answers which certificate a site serves HTTPS with.
type certificateSource struct{ repo *sslpkg.Repository }

func (c certificateSource) CertificateFor(ctx context.Context, websiteID string) (string, string, bool, error) {
	certificate, err := c.repo.Get(ctx, websiteID)
	if err != nil {
		if errors.Is(err, sslpkg.ErrNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}

	// A certificate row exists from the moment one is requested, and the paths
	// are filled in when it is actually issued. Treating a pending row as
	// installed would write a vhost naming files that are not there yet, and
	// nginx refuses to start on that — taking every other site down with it.
	if certificate.CertificatePath == nil || certificate.PrivateKeyPath == nil {
		return "", "", false, nil
	}
	return *certificate.CertificatePath, *certificate.PrivateKeyPath, true, nil
}

// proxySource answers which application port a site's vhost proxies to.
type proxySource struct{ repo *nodepkg.Repository }

func (p proxySource) ProxyPortFor(ctx context.Context, websiteID string) (int, error) {
	app, err := p.repo.GetByWebsite(ctx, websiteID)
	if err != nil {
		if errors.Is(err, nodepkg.ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}

	// Only a running application is proxied to. A stopped one means the site
	// serves its files again — which is the point of stopping it — and a vhost
	// still pointing at the dead port would answer every request with 502.
	if app.Status != nodepkg.StatusRunning {
		return 0, nil
	}
	return app.Port, nil
}

// servingSources builds the set the websites service consults.
func servingSources(php *phppkg.Repository, ssl *sslpkg.Repository,
	node *nodepkg.Repository,
) websites.ServingSources {
	return websites.ServingSources{
		PHP:          phpPoolSource{repo: php},
		Certificates: certificateSource{repo: ssl},
		Proxy:        proxySource{repo: node},
	}
}
