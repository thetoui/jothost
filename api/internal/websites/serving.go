package websites

import (
	"context"
	"fmt"

	"github.com/jothost/panel/shared/validate"
)

// The Agent rewrites a site's whole vhost from the payload it is given. It
// does not merge with what is already there, and that is deliberate: a
// configuration assembled from the panel's record converges on the same file
// however many times it is applied, where a merge would accumulate whatever
// was left behind by a job that half-failed.
//
// The consequence is the reason this file exists. Any payload that omits a
// fact turns that fact off. Issuing a certificate with no PHP socket in the
// payload silently makes a PHP site serve its source; adding an alias to a
// Node site silently stops the proxy; enabling PHP on a site with a
// certificate silently drops it back to HTTP. Each of those was reachable
// while three packages built their own payloads from whichever fields they
// happened to care about.
//
// So the desired state is assembled in one place, from the panel's record, and
// every caller starts from it.

// ServingState is everything about a website that shapes its vhost.
type ServingState struct {
	// Aliases are the additional names this site answers to. The primary
	// domain is not among them.
	Aliases []string
	// AliasRoots are the aliases served from a directory of their own, each
	// with the path it is served from.
	//
	// Separate from Aliases rather than a map over it, because the two become
	// different things in the vhost: a plain alias is another server_name on
	// the site's own block, and one of these is a server block of its own.
	AliasRoots []AliasRoot
	// PHPSocket is the FPM pool the site's .php requests go to. Empty means
	// the site does not serve PHP.
	//
	// For a subdomain that inherits its parent's pool this is the *parent's*
	// socket: that is what inheriting means, and it is why this is resolved
	// from the record rather than from the site's own pool row, which does not
	// exist.
	PHPSocket string
	// ProxyPort points the vhost at an application on the loopback instead of
	// at files. Zero serves files.
	ProxyPort int
	// SSL is the certificate the site serves HTTPS with. Nil means HTTP only.
	SSL *ServingSSL
	// ApachePort is the loopback port Apache serves this site on, in the
	// hybrid arrangement. Zero means nginx serves it directly.
	//
	// It is zero on a site served by an application even in hybrid mode: the
	// application answers every path, and Apache is there for .htaccess and
	// PHP, neither of which an application uses.
	ApachePort int
	// AllowOverride is whether Apache reads .htaccess for this site.
	AllowOverride bool
}

// ServingSSL is a site's certificate as the vhost needs it.
type ServingSSL struct {
	CertificatePath string
	PrivateKeyPath  string
	RedirectToHTTPS bool
}

// PHPPoolSource resolves the FPM socket a website serves PHP through.
//
// It is an interface here, implemented by the php package and wired at
// startup, because php imports websites. Depending on it directly would make
// the two packages mutually dependent, and the compiler would refuse.
type PHPPoolSource interface {
	// SocketFor returns the site's socket, or "" when it serves no PHP.
	SocketFor(ctx context.Context, websiteID string) (string, error)
}

// CertificateSource resolves a website's installed certificate.
type CertificateSource interface {
	// CertificateFor returns the certificate and key paths, or ok=false when
	// the site has none installed.
	//
	// Both paths or neither: nginx needs the key to serve the certificate, and
	// a server block naming one without the other is refused at validation —
	// which on a reload means every site on the host keeps serving the
	// previous configuration.
	CertificateFor(ctx context.Context, websiteID string) (cert, key string, ok bool, err error)
}

// ProxySource resolves the application port a website proxies to.
type ProxySource interface {
	// ProxyPortFor returns the port, or 0 when nothing proxies for this site.
	// A stopped application returns 0: the site serves its files again, which
	// is the point of stopping it.
	ProxyPortFor(ctx context.Context, websiteID string) (int, error)
}

// ServingSources are the optional resolvers consulted when a vhost is built.
//
// Each is optional so a Repository can be built in a test, or in an early
// phase, without the feature that provides it. A nil source contributes
// nothing, which is the same answer as "this site has none of that".
type ServingSources struct {
	PHP          PHPPoolSource
	Certificates CertificateSource
	Proxy        ProxySource
}

// SetServingSources wires the resolvers used to build a vhost payload.
//
// They live on the Repository rather than the Service because ssl, php and
// node each hold the repository — they are the callers that most need a
// complete payload, and reaching them through the service would mean handing
// every feature a dependency it otherwise has no use for.
func (r *Repository) SetServingSources(sources ServingSources) {
	r.serving = sources
}

// ServingState assembles a site's complete desired vhost configuration.
func (r *Repository) ServingState(ctx context.Context, site Website) (ServingState, error) {
	state := ServingState{}

	domains, err := r.ListDomains(ctx, site.ID)
	if err != nil {
		return ServingState{}, err
	}
	primary := validate.NormalizeDomain(site.PrimaryDomain)
	for _, domain := range domains {
		// A redirect is served by its own vhost, and the primary name is not
		// an alias of itself.
		if domain.Type == DomainPrimary || domain.Type == DomainRedirect {
			continue
		}
		if validate.NormalizeDomain(domain.Domain) == primary {
			continue
		}
		if domain.DocumentRoot != nil && *domain.DocumentRoot != "" {
			// Served from somewhere of its own, so it is not another name for
			// the site's block — it needs one of its own.
			state.AliasRoots = append(state.AliasRoots, AliasRoot{
				Domain:       domain.Domain,
				DocumentRoot: *domain.DocumentRoot,
			})
			continue
		}
		state.Aliases = append(state.Aliases, domain.Domain)
	}

	if r.serving.PHP != nil {
		// A subdomain that inherits its parent's pool has no pool row of its
		// own, so the socket is the parent's. Asking for the subdomain's would
		// return nothing and quietly turn PHP off for it.
		poolOwner := site.ID
		if site.InheritsPHPPool() && site.ParentWebsiteID != nil {
			poolOwner = *site.ParentWebsiteID
		}
		socket, err := r.serving.PHP.SocketFor(ctx, poolOwner)
		if err != nil {
			return ServingState{}, fmt.Errorf("resolve php socket: %w", err)
		}
		state.PHPSocket = socket
	}

	if r.serving.Proxy != nil {
		port, err := r.serving.Proxy.ProxyPortFor(ctx, site.ID)
		if err != nil {
			return ServingState{}, fmt.Errorf("resolve proxy port: %w", err)
		}
		state.ProxyPort = port
	}

	// A proxied site is served by its application, so it has no PHP block. The
	// renderer refuses a configuration carrying both; resolving it here means
	// the caller gets a working vhost rather than a rejected one.
	if state.ProxyPort != 0 {
		state.PHPSocket = ""
	}

	// The hybrid arrangement, if this host runs it and nothing else is already
	// in front of the files. The Agent decides who consumes the PHP socket
	// from this: with a backend port it is Apache's, without one it is
	// nginx's.
	if state.ProxyPort == 0 && site.ApachePort != nil {
		mode, err := r.WebserverMode(ctx, site.ServerID)
		if err != nil {
			return ServingState{}, fmt.Errorf("resolve webserver mode: %w", err)
		}
		if mode == validate.WebserverHybrid {
			state.ApachePort = *site.ApachePort
			state.AllowOverride = site.AllowOverride
		}
	}

	if r.serving.Certificates != nil {
		cert, key, ok, err := r.serving.Certificates.CertificateFor(ctx, site.ID)
		if err != nil {
			return ServingState{}, fmt.Errorf("resolve certificate: %w", err)
		}
		if ok {
			state.SSL = &ServingSSL{
				CertificatePath: cert,
				PrivateKeyPath:  key,
				// The redirect is a property of the website, not of the
				// certificate: it survives a certificate being replaced.
				RedirectToHTTPS: site.HTTPSRedirect,
			}
		}
	}

	return state, nil
}

// AliasRoot is a name on this site served from a directory of its own.
type AliasRoot struct {
	Domain       string
	DocumentRoot string
}

// VhostPayload builds the fields every vhost-rewriting job needs.
//
// Callers add their own fields on top — a certificate operation adds the
// provider and the paths it is about to write — but they start from the site's
// complete current state, so nothing is turned off as a side effect of
// changing something else.
func (r *Repository) VhostPayload(ctx context.Context, site Website) (map[string]any, error) {
	state, err := r.ServingState(ctx, site)
	if err != nil {
		return nil, err
	}

	aliases := state.Aliases
	if aliases == nil {
		aliases = []string{}
	}

	// Always present, empty list included. The Agent renders the whole file
	// from this payload, so a missing key on an unrelated change — adding an
	// alias, switching PHP version — would silently put every alias back on
	// the site's own root. That is the failure the nginx directives below are
	// commented for, and it applies here for the same reason.
	roots := make([]map[string]any, 0, len(state.AliasRoots))
	for _, alias := range state.AliasRoots {
		roots = append(roots, map[string]any{
			"domain":        alias.Domain,
			"document_root": alias.DocumentRoot,
		})
	}

	payload := map[string]any{
		"website_id":    site.ID,
		"alias_roots":   roots,
		"domain":        site.PrimaryDomain,
		"document_root": site.DocumentRoot,
		"system_user":   site.SystemUser,
		"aliases":       aliases,
	}
	if state.PHPSocket != "" {
		payload["php_socket"] = state.PHPSocket
	}
	if state.ProxyPort != 0 {
		payload["proxy_port"] = state.ProxyPort
	}
	if state.SSL != nil {
		payload["certificate_path"] = state.SSL.CertificatePath
		payload["private_key_path"] = state.SSL.PrivateKeyPath
		payload["redirect_to_https"] = state.SSL.RedirectToHTTPS
	}
	if state.ApachePort != 0 {
		payload["apache_port"] = state.ApachePort
		payload["allow_override"] = state.AllowOverride
	}
	// Carried on every vhost rewrite, not only the one that set it. The Agent
	// renders the whole file from this payload, so omitting the directives on
	// an unrelated change — adding an alias, switching PHP version — would
	// quietly drop them, which is the failure the SSL fields above are
	// commented for.
	if site.NginxDirectives != "" {
		payload["nginx_directives"] = site.NginxDirectives
	}
	return payload, nil
}
