package sites

import (
	"context"
	"fmt"

	"github.com/jothost/panel/agent/internal/apache"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Hybrid mode: nginx in front, Apache behind.
//
// The decision of who serves what lives here, in one function, rather than in
// each operation that rewrites a vhost. Creating a website, switching its PHP
// version, and issuing its certificate all rewrite the whole configuration —
// and if each of them decided independently whether Apache was involved, they
// would disagree, and the disagreement would show up as a site that works
// until someone changes something unrelated.
//
// The rules, in order:
//
//  1. A site served by an application is served by that application. nginx
//     proxies straight to it and Apache is not in the path at all: Apache is
//     there for .htaccess and PHP, and a Node.js process needs neither.
//  2. Otherwise, in hybrid mode, nginx proxies to Apache and Apache serves the
//     files and the PHP.
//  3. Otherwise nginx serves the files and the PHP itself, which is what this
//     panel did before Phase 4.5 and still does by default.

// backend describes what nginx should be pointed at for a site.
type backend struct {
	// proxyPort is what nginx proxies to: an application's port, or Apache's.
	// Zero means nginx serves the site itself.
	proxyPort int
	// phpSocket is the FPM socket nginx should use. It is empty whenever
	// something else is in front of the files, because then that something
	// else owns PHP.
	phpSocket string
	// apachePort is set when Apache is serving this site, and zero when it is
	// not — including when it was and no longer is, which is what tells the
	// caller to take the vhost away again.
	apachePort int
}

// resolveBackend applies the rules above.
func resolveBackend(appPort, apachePort int, phpSocket string) backend {
	switch {
	case appPort != 0:
		// An application answers every path. Apache would have nothing to do.
		return backend{proxyPort: appPort}
	case apachePort != 0:
		return backend{proxyPort: apachePort, apachePort: apachePort}
	default:
		return backend{phpSocket: phpSocket}
	}
}

// applyApache writes or removes a site's Apache configuration.
//
// It is called before nginx is repointed when Apache is being brought in, and
// after nginx has been repointed when Apache is being taken out. That ordering
// is the whole difference between a configuration change and an outage: a site
// must never be proxied to a backend that is not there yet, and must never
// have its backend removed while it is still being proxied to.
func (m *Manager) applyApache(ctx context.Context, cfg apache.SiteConfig, wanted bool) error {
	if m.apache == nil || !m.apache.Available() {
		if wanted {
			return fmt.Errorf("%w: apache is not installed", ErrUnsupported)
		}
		// Nothing to remove on a host that has no Apache.
		return nil
	}

	if !wanted {
		return m.removeApacheSite(ctx, cfg.PrimaryDomain)
	}

	if err := m.apache.EnsureBase(ctx); err != nil {
		return err
	}
	if _, err := m.apache.WriteSite(ctx, cfg); err != nil {
		return err
	}
	return m.apache.Apply(ctx)
}

// removeApacheSite takes a site out of Apache and stops the server if that was
// the last one.
//
// Stopping is not tidiness. Apache's only listening sockets in this
// arrangement are the ones its site files declare, and a server with no
// listening socket refuses to start — so an Apache left "running" with no
// vhosts would fail its next start for a reason unrelated to the change being
// made then.
func (m *Manager) removeApacheSite(ctx context.Context, domain string) error {
	removed, err := m.apache.RemoveSite(ctx, domain)
	if err != nil {
		return err
	}
	if !removed {
		return nil
	}

	remaining, err := m.apache.SiteCount()
	if err != nil {
		return err
	}
	if remaining == 0 {
		return m.apache.Stop(ctx)
	}
	return m.apache.Apply(ctx)
}

// apacheConfigFor builds the Apache side of a site from its layout.
//
// Apache keeps its own logs, beside nginx's rather than in them. A request
// Apache serves never reaches an nginx handler, so nginx's access log would
// record the proxy hop and nothing about what was actually served.
func apacheConfigFor(domain string, aliases []string, layout Layout,
	port int, phpSocket string, maxBody int64, allowOverride bool,
) apache.SiteConfig {
	return apache.SiteConfig{
		PrimaryDomain: domain,
		Aliases:       aliases,
		DocumentRoot:  layout.Content,
		BackendPort:   port,
		AccessLog:     layout.ApacheAccessLog(),
		ErrorLog:      layout.ApacheErrorLog(),
		PHPSocket:     phpSocket,
		MaxBodySize:   maxBody,
		AllowOverride: allowOverride,
	}
}

// HybridStatus reports what Apache is doing for a site.
type HybridStatus struct {
	// Available reports whether this host can run hybrid mode at all.
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	// Running reports whether the backend is up.
	Running bool `json:"running"`
	// Sites is how many websites the panel has configured in Apache.
	Sites int `json:"sites"`
}

// ApacheStatus describes the Apache backend on this host.
func (m *Manager) ApacheStatus(ctx context.Context) HybridStatus {
	if m.apache == nil || !m.apache.Available() {
		return HybridStatus{}
	}

	status := HybridStatus{
		Available: true,
		Running:   m.apache.Running(ctx),
	}
	if version, err := m.apache.Version(ctx); err == nil {
		status.Version = version
	}
	if count, err := m.apache.SiteCount(); err == nil {
		status.Sites = count
	} else {
		m.log.Warn("could not count apache sites", logger.KeyError, err.Error())
	}
	return status
}

// validateBackendPort checks a port before it reaches a configuration file.
//
// The API allocates and validates it too. This is the boundary that cannot be
// skipped: the Agent runs as root, and a port is a value from a request.
func validateBackendPort(port int) error {
	if port == 0 {
		return nil
	}
	return validate.BackendPort(port)
}
