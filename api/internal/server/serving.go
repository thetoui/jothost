package server

import (
	"context"
	"errors"
	"strings"

	cronpkg "github.com/jothost/panel/api/internal/cron"
	dnspkg "github.com/jothost/panel/api/internal/dns"
	ftppkg "github.com/jothost/panel/api/internal/ftp"
	nodepkg "github.com/jothost/panel/api/internal/node"
	phppkg "github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/servers"
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

// cronWebsites answers what the cron package needs to know about a site: the
// account a job runs as, the directory a script path is resolved inside, and
// the PHP version that account's scripts run under.
//
// Three fields rather than the whole website record, because a scheduled job
// has no business with the rest of it — and because the narrow interface is
// what lets the two packages change without each other.
type cronWebsites struct{ repo *websites.Repository }

func (c cronWebsites) LookupForCron(ctx context.Context, id string) (cronpkg.WebsiteRef, error) {
	site, err := c.repo.Get(ctx, id)
	if err != nil {
		return cronpkg.WebsiteRef{}, err
	}

	version := ""
	if site.PHPVersion != nil {
		version = *site.PHPVersion
	}
	return cronpkg.WebsiteRef{
		ID:           site.ID,
		ServerID:     site.ServerID,
		Domain:       site.PrimaryDomain,
		SystemUser:   site.SystemUser,
		DocumentRoot: site.DocumentRoot,
		PHPVersion:   version,
	}, nil
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

// ftpWebsites answers what the ftp package needs to know about a site: the
// account a session runs as, the directory the account is confined inside, and
// the certificate FTPS would present.
//
// The certificate is read from the SSL repository rather than stored with the
// FTP settings, so a renewal that moved the files is picked up rather than
// leaving FTPS presenting a path that no longer exists.
type ftpWebsites struct {
	repo *websites.Repository
	ssl  *sslpkg.Repository
}

func (f ftpWebsites) LookupForFTP(ctx context.Context, id string) (ftppkg.WebsiteRef, error) {
	site, err := f.repo.Get(ctx, id)
	if err != nil {
		return ftppkg.WebsiteRef{}, err
	}

	ref := ftppkg.WebsiteRef{
		ID:           site.ID,
		ServerID:     site.ServerID,
		Domain:       site.PrimaryDomain,
		SystemUser:   site.SystemUser,
		DocumentRoot: site.DocumentRoot,
	}

	certificate, err := f.ssl.Get(ctx, id)
	if err != nil {
		if errors.Is(err, sslpkg.ErrNotFound) {
			// No certificate is not an error here: most sites have none, and
			// the caller's question is whether FTPS can be offered.
			return ref, nil
		}
		return ftppkg.WebsiteRef{}, err
	}
	// A certificate row exists from the moment one is requested and the paths
	// are filled in when it is issued. A pending row is not something FTPS can
	// present, and naming it would stop proftpd from starting.
	if certificate.CertificatePath == nil || certificate.PrivateKeyPath == nil {
		return ref, nil
	}
	ref.SSLEnabled = true
	ref.CertificatePath = *certificate.CertificatePath
	ref.KeyPath = *certificate.PrivateKeyPath
	return ref, nil
}

// dnsWebsites answers what the dns package needs to know about a site a zone
// belongs to: which server it is on, and what it is called.
type dnsWebsites struct {
	repo *websites.Repository
}

func (d dnsWebsites) LookupForDNS(ctx context.Context, id string) (dnspkg.WebsiteRef, error) {
	site, err := d.repo.Get(ctx, id)
	if err != nil {
		return dnspkg.WebsiteRef{}, err
	}
	return dnspkg.WebsiteRef{
		ID:       site.ID,
		ServerID: site.ServerID,
		Domain:   site.PrimaryDomain,
	}, nil
}

// updateRuntimes tells the updates package which packages belong to the
// language runtimes this panel manages.
//
// It is a join rather than a second question: a PHP update *is* a package
// update, and asking the host separately would be two answers that can
// disagree. What the panel adds is knowing which packages are PHP, because
// Phase 5 installed them.
type updateRuntimes struct {
	php  *phppkg.Repository
	node *nodepkg.Repository
}

// PHPPackagePrefixes returns the prefixes this host's PHP is installed under.
//
// Alpine names them php84-fpm, php84-opcache and so on; Debian php8.4-fpm. Both
// start with the version's own prefix, which is what the panel already records,
// so the match is on that rather than on a list of package names this phase
// would have to keep in step with Phase 5's.
func (u updateRuntimes) PHPPackagePrefixes(ctx context.Context) []string {
	if u.php == nil {
		return nil
	}
	versions, err := u.php.ListVersions(ctx)
	if err != nil {
		return nil
	}

	prefixes := make([]string, 0, len(versions)*2)
	for _, version := range versions {
		if !version.Installed {
			continue
		}
		// "8.4" is written "php84" by Alpine and "php8.4" by Debian.
		compact := strings.ReplaceAll(version.Version, ".", "")
		prefixes = append(prefixes, "php"+compact, "php"+version.Version)
	}
	return prefixes
}

// NodePackageNames returns the packages the Node.js runtime uses.
//
// Fixed names rather than a lookup: both distributions call them the same
// thing, and a runtime the panel did not install is still the runtime its
// applications run on.
func (u updateRuntimes) NodePackageNames(context.Context) []string {
	return []string{"nodejs", "nodejs-current", "npm"}
}

// dnsHost answers what this machine's address is.
//
// Read from the servers table at the moment it is needed rather than captured
// at startup: the Agent refreshes that row, so a host that has been given a new
// address seeds its next zone with the new one rather than with whatever was
// true when the API last booted.
type dnsHost struct {
	repo     *servers.Repository
	serverID string
}

func (d dnsHost) Address(ctx context.Context) (string, error) {
	if d.serverID == "" {
		return "", nil
	}
	server, err := d.repo.Get(ctx, d.serverID)
	if err != nil {
		return "", err
	}
	// IPv4 first, because that is what a hosting customer's A record needs and
	// what almost every resolver on the far side will ask for.
	if server.IPv4 != nil && *server.IPv4 != "" {
		return *server.IPv4, nil
	}
	if server.IPv6 != nil && *server.IPv6 != "" {
		return *server.IPv6, nil
	}
	return "", nil
}
