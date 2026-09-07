package sites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/apache"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Manager provisions a complete website: account, directories, and vhost.
//
// The steps are ordered so that the reversible ones happen first and the one
// that makes the site live — the nginx reload — happens last. A failure part
// way through leaves an unserved directory rather than a half-configured
// server, which is the difference between a retry and an outage.
type Manager struct {
	fs    *Provisioner
	users *UserProvider
	nginx *nginx.Provider
	// apache is the backend in hybrid mode. Nil, or present but unavailable,
	// on a host that serves everything from nginx — which is the default.
	apache *apache.Provider
	log    *slog.Logger
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Filesystem *Provisioner
	Users      *UserProvider
	Nginx      *nginx.Provider
	Apache     *apache.Provider
	Log        *slog.Logger
}

// NewManager builds a Manager.
func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		fs:     opts.Filesystem,
		users:  opts.Users,
		nginx:  opts.Nginx,
		apache: opts.Apache,
		log:    opts.Log,
	}
}

// Capabilities reports what this host can actually do.
type Capabilities struct {
	Nginx bool `json:"nginx"`
	// Apache reports whether this host can run the hybrid arrangement. It is
	// advertised rather than discovered from a failed mode switch.
	Apache bool `json:"apache"`
	Users  bool `json:"users"`
	// WebGroup reports whether site directories can be made readable by the
	// web server. Without it a site is created and then serves nothing.
	WebGroup bool `json:"web_group"`
}

// Capabilities describes the host's website support.
func (m *Manager) Capabilities() Capabilities {
	return Capabilities{
		Nginx:    m.nginx != nil && m.nginx.Available(),
		Apache:   m.apache != nil && m.apache.Available(),
		Users:    m.users != nil && m.users.Available(),
		WebGroup: m.fs != nil && m.fs.HasWebGroup(),
	}
}

// CreateRequest describes a website to provision.
type CreateRequest struct {
	Domain       string
	Aliases      []string
	DocumentRoot string
	SystemUser   string
	MaxBodySize  string
	// PHPSocket is the FPM pool this site serves .php from. Empty means a
	// static site and the vhost omits PHP entirely.
	PHPSocket string
	// SSL is the certificate this site serves HTTPS with. Nil means HTTP only;
	// the HTTPS block is omitted rather than naming a certificate that is not
	// there, which nginx refuses to start with — taking every other site down.
	SSL *nginx.SSLConfig
	// ProxyPort makes this site a reverse proxy to an application on
	// 127.0.0.1 rather than a directory of files.
	ProxyPort int
	// ApachePort puts Apache in front of the files, with nginx proxying to it.
	// Zero is nginx serving the site itself, which is the default arrangement.
	//
	// An application takes precedence: a site served by one needs neither
	// .htaccess nor PHP, so Apache stays out of the path entirely.
	ApachePort int
	// AllowOverride enables .htaccess for this site, which is what hybrid mode
	// is usually turned on for.
	AllowOverride bool
	// MaxBodyBytes caps uploads at the Apache layer. Zero means Apache's own
	// default; nginx has its own limit from MaxBodySize.
	MaxBodyBytes int64
	// Directives is the operator's own nginx configuration for this site,
	// written into its server block after everything the panel generates.
	Directives string
}

// CreateResult is what provisioning produced.
type CreateResult struct {
	Domain     string  `json:"domain"`
	Layout     Layout  `json:"layout"`
	Account    Account `json:"account"`
	ConfigPath string  `json:"config_path"`
	// Reloaded reports whether nginx picked the site up. A site can be fully
	// written and still not served if the reload failed.
	Reloaded bool `json:"reloaded"`
	// ApachePort is the backend this site is served through, or zero when
	// nginx serves it directly.
	ApachePort int `json:"apache_port,omitempty"`
}

// ErrUnsupported means the host cannot provision websites.
var ErrUnsupported = errors.New("this host cannot manage websites")

// Create provisions a website.
//
// Every step is idempotent, so re-running after a partial failure converges on
// the intended state rather than erroring on what already exists. That is what
// makes a failed job safe to retry.
func (m *Manager) Create(ctx context.Context, req CreateRequest, report func(int, string)) (CreateResult, error) {
	capabilities := m.Capabilities()
	if !capabilities.Nginx {
		return CreateResult{}, fmt.Errorf("%w: nginx is not installed", ErrUnsupported)
	}
	if !capabilities.Users {
		return CreateResult{}, fmt.Errorf("%w: no user management tool is available", ErrUnsupported)
	}
	if !capabilities.WebGroup {
		// Refusing here is the whole point: the alternative is a site that
		// provisions cleanly and then returns 403 to every request.
		return CreateResult{}, fmt.Errorf("%w: %v", ErrUnsupported, ErrNoWebGroup)
	}

	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.ServerName(domain); err != nil {
		return CreateResult{}, err
	}
	if err := validate.SystemUser(req.SystemUser); err != nil {
		return CreateResult{}, err
	}

	aliases := make([]string, 0, len(req.Aliases))
	for _, alias := range req.Aliases {
		normalized := validate.NormalizeDomain(alias)
		if err := validate.ServerName(normalized); err != nil {
			return CreateResult{}, fmt.Errorf("alias %q: %w", alias, err)
		}
		if normalized == domain {
			// nginx refuses a duplicate server_name, so this would produce a
			// config that fails validation with a confusing message.
			return CreateResult{}, fmt.Errorf("alias %q duplicates the primary domain", alias)
		}
		aliases = append(aliases, normalized)
	}

	progress(report, 10, "Resolving site layout")
	layout, err := m.fs.LayoutFor(req.DocumentRoot)
	if err != nil {
		return CreateResult{}, err
	}

	progress(report, 25, "Creating the system account")
	account, err := m.users.Ensure(ctx, req.SystemUser, layout.Root)
	if err != nil {
		return CreateResult{}, err
	}

	progress(report, 45, "Creating directories")
	if err := m.fs.Provision(layout, account.UID, account.GID); err != nil {
		return CreateResult{}, err
	}
	if err := m.fs.WritePlaceholder(layout, domain, account.UID, account.GID); err != nil {
		return CreateResult{}, err
	}

	if err := validateBackendPort(req.ApachePort); err != nil {
		return CreateResult{}, err
	}
	serve := resolveBackend(req.ProxyPort, req.ApachePort, req.PHPSocket)

	// Apache first, so nginx is never pointed at a backend that is not there.
	if serve.apachePort != 0 {
		progress(report, 60, "Configuring the Apache backend")
		if err := m.applyApache(ctx, apacheConfigFor(domain, aliases, layout,
			serve.apachePort, req.PHPSocket, req.MaxBodyBytes, req.AllowOverride),
			true); err != nil {
			return CreateResult{}, err
		}
	}

	progress(report, 70, "Writing the web server configuration")
	configPath, err := m.nginx.WriteSite(ctx, nginx.SiteConfig{
		PrimaryDomain: domain,
		Aliases:       aliases,
		DocumentRoot:  layout.Content,
		AccessLog:     layout.AccessLog,
		ErrorLog:      layout.ErrorLog,
		MaxBodySize:   req.MaxBodySize,
		PHPSocket:     serve.phpSocket,
		ProxyPort:     serve.proxyPort,
		SSL:           req.SSL,
		Directives:    req.Directives,
	})
	if err != nil {
		return CreateResult{}, err
	}

	progress(report, 90, "Reloading the web server")
	if err := m.nginx.Reload(ctx); err != nil {
		// The config is written and valid but not live. Reporting success here
		// would tell the panel a site works when no request will reach it.
		return CreateResult{}, fmt.Errorf("site configured but not served: %w", err)
	}

	progress(report, 100, "Website ready")
	return CreateResult{
		Domain:     domain,
		Layout:     layout,
		Account:    account,
		ConfigPath: configPath,
		Reloaded:   true,
		ApachePort: serve.apachePort,
	}, nil
}

// DeleteRequest describes a website to remove.
type DeleteRequest struct {
	Domain       string
	DocumentRoot string
	SystemUser   string
	// RemoveFiles deletes the site's content. It defaults to false because a
	// deleted vhost can be recreated, whereas deleted content cannot.
	RemoveFiles bool
	// RemoveUser deletes the system account.
	RemoveUser bool
}

// DeleteResult reports what was removed.
type DeleteResult struct {
	Domain        string `json:"domain"`
	ConfigRemoved bool   `json:"config_removed"`
	// FilesRetained reports that the site's files were kept and reassigned to
	// root rather than deleted, so the freed account uid no longer owns them.
	FilesRetained bool `json:"files_retained"`
	// ApacheRemoved reports whether the Apache backend was taken down with it.
	ApacheRemoved bool `json:"apache_removed"`
	FilesRemoved  bool `json:"files_removed"`
	UserRemoved   bool `json:"user_removed"`
	Reloaded      bool `json:"reloaded"`
}

// Delete removes a website.
//
// The vhost goes first and nginx is reloaded immediately, so the site stops
// being served before its files are touched. Removing content from under a
// live server would serve half-deleted directories to whoever is browsing.
func (m *Manager) Delete(ctx context.Context, req DeleteRequest, report func(int, string)) (DeleteResult, error) {
	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.ServerName(domain); err != nil {
		return DeleteResult{}, err
	}

	result := DeleteResult{Domain: domain}

	// The backend goes first, before nginx stops proxying to it: a request in
	// flight should meet a closed site rather than a proxy pointing at a vhost
	// that has just been taken away.
	if m.apache != nil && m.apache.Available() {
		progress(report, 10, "Removing the Apache backend")
		if err := m.removeApacheSite(ctx, domain); err != nil {
			// Logged, not fatal: refusing to delete a website because a
			// backend file would not unlink leaves the user with a site they
			// cannot get rid of.
			m.log.Warn("the Apache backend could not be removed while deleting a website",
				"domain", domain, logger.KeyError, err.Error())
		} else {
			result.ApacheRemoved = true
		}
	}

	if m.nginx != nil && m.nginx.Available() {
		progress(report, 20, "Removing the web server configuration")
		removed, err := m.nginx.RemoveSite(ctx, domain)
		if err != nil {
			return result, err
		}
		result.ConfigRemoved = removed

		progress(report, 40, "Reloading the web server")
		if err := m.nginx.Reload(ctx); err != nil {
			// Deletion continues: the config is already gone, and refusing to
			// finish would leave the record and the host disagreeing.
			m.log.Warn("nginx reload failed during website deletion",
				"domain", domain, logger.KeyError, err.Error())
		} else {
			result.Reloaded = true
		}
	}

	if req.DocumentRoot != "" {
		layout, err := m.fs.LayoutFor(req.DocumentRoot)
		if err != nil {
			return result, err
		}

		if req.RemoveFiles {
			progress(report, 65, "Removing site files")
			if err := m.fs.Remove(layout); err != nil {
				return result, err
			}
			result.FilesRemoved = true
		} else if req.RemoveUser {
			// The files are being kept and the account is not, so the tree has
			// to stop belonging to that account before its uid goes back into
			// the pool. This is the same rule the pools, the crontab and the
			// certificate above already follow — deal with what refers to the
			// account while the account still exists — and the filesystem was
			// the one place it was not being followed.
			progress(report, 65, "Reassigning the retained site files")
			if err := m.fs.Neutralize(layout); err != nil {
				// Fatal, and deliberately so. Carrying on would remove the
				// account anyway and free a uid that the files still carry,
				// which is the exact outcome this exists to prevent. A website
				// that needs a retry is a far smaller problem than a directory
				// that silently changes hands.
				return result, fmt.Errorf("retained files could not be reassigned, "+
					"so the account was kept to stop its uid being reused: %w", err)
			}
			result.FilesRetained = true
		}
	}

	if req.RemoveUser && req.SystemUser != "" {
		progress(report, 85, "Removing the system account")
		removed, err := m.users.Remove(ctx, req.SystemUser)
		if err != nil {
			return result, err
		}
		result.UserRemoved = removed
	}

	progress(report, 100, "Website removed")
	return result, nil
}

// Status is a website's observed state on the host.
type Status struct {
	Domain string `json:"domain"`
	// ConfigInstalled reports whether the vhost file exists.
	ConfigInstalled bool `json:"config_installed"`
	// DocumentRootExists reports whether the content directory is present.
	DocumentRootExists bool `json:"document_root_exists"`
	UserExists         bool `json:"user_exists"`
	// Serving is the conclusion: everything needed for a request to succeed.
	Serving bool `json:"serving"`
}

// StatusOf inspects what a website actually looks like on the host.
//
// The panel's database records what was asked for; this reports what is there.
// Comparing the two is how a site that was edited or broken outside the panel
// becomes visible instead of silently disagreeing.
func (m *Manager) StatusOf(ctx context.Context, domain, documentRoot, systemUser string) (Status, error) {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.ServerName(normalized); err != nil {
		return Status{}, err
	}

	status := Status{Domain: normalized}

	if m.nginx != nil && m.nginx.Available() {
		installed, err := m.nginx.SiteExists(normalized)
		if err != nil {
			return Status{}, err
		}
		status.ConfigInstalled = installed
	}

	if documentRoot != "" {
		layout, err := m.fs.LayoutFor(documentRoot)
		if err == nil {
			exists, err := m.fs.Exists(layout)
			if err != nil {
				return Status{}, err
			}
			status.DocumentRootExists = exists
		}
	}

	if systemUser != "" && m.users != nil {
		if _, found, err := m.users.Lookup(systemUser); err == nil {
			status.UserExists = found
		}
	}

	status.Serving = status.ConfigInstalled && status.DocumentRootExists
	return status, nil
}

// LogKind selects which of a site's logs to read.
type LogKind string

// Available site logs.
const (
	LogAccess LogKind = "access"
	LogError  LogKind = "error"
)

// maxLogLines bounds a log request so a caller cannot pull an entire file into
// the Agent's memory and then into a response.
const maxLogLines = 1000

// DefaultLogLines is returned when a caller does not ask for a count.
const DefaultLogLines = 100

// ReadLog returns the tail of one of a site's log files.
func (m *Manager) ReadLog(documentRoot string, kind LogKind, lines int) ([]string, error) {
	if lines <= 0 {
		lines = DefaultLogLines
	}
	if lines > maxLogLines {
		lines = maxLogLines
	}

	layout, err := m.fs.LayoutFor(documentRoot)
	if err != nil {
		return nil, err
	}

	var path string
	switch kind {
	case LogAccess:
		path = layout.AccessLog
	case LogError:
		path = layout.ErrorLog
	default:
		return nil, fmt.Errorf("unknown log %q", kind)
	}

	content, err := os.ReadFile(path) //nolint:gosec // path is derived from a validated, root-confined layout
	if err != nil {
		if os.IsNotExist(err) {
			// A site that has served no requests has no log yet, which is not
			// an error worth failing a page over.
			return []string{}, nil
		}
		return nil, fmt.Errorf("read %s log: %w", kind, err)
	}

	return tail(string(content), lines), nil
}

// tail returns the last n lines of a file.
func tail(content string, n int) []string {
	all := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		return []string{}
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// DocumentRootFor derives the conventional document root for a domain.
//
// Callers may supply their own, but having one definition keeps the layout
// consistent across sites and makes a site's location predictable to an
// operator looking for it on disk.
func DocumentRootFor(root, domain string) string {
	return filepath.Join(root, validate.NormalizeDomain(domain), ContentDir)
}

// progress reports a step if the caller supplied a reporter.
func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}

// UpdateRequest describes a configuration change to an existing website.
type UpdateRequest struct {
	Domain       string
	Aliases      []string
	DocumentRoot string
	MaxBodySize  string
	// PHPSocket is the FPM pool this site serves .php from. Empty rewrites the
	// vhost as a static site, which is how PHP is turned off.
	PHPSocket string
	// SSL is the certificate this site serves HTTPS with. Nil rewrites the
	// vhost as HTTP only, which is how a certificate is withdrawn.
	SSL *nginx.SSLConfig
	// ProxyPort makes this site a reverse proxy to an application listening on
	// 127.0.0.1. Zero serves files, which is how a Node.js site is turned back
	// into a static one.
	ProxyPort int
	// ApachePort puts Apache in front of the files. Zero takes it back out,
	// which is how hybrid mode is switched off for a site.
	ApachePort int
	// AllowOverride enables .htaccess for this site.
	AllowOverride bool
	// MaxBodyBytes caps uploads at the Apache layer.
	MaxBodyBytes int64
	// Directives is the operator's own nginx configuration for this site.
	Directives string
}

// Domains lists the sites this host has directories for.
func (m *Manager) Domains() ([]string, error) { return m.fs.Domains() }

// Root is the directory websites live under.
func (m *Manager) Root() string { return m.fs.Root() }

// UpdateResult reports what was rewritten.
type UpdateResult struct {
	Domain     string `json:"domain"`
	ConfigPath string `json:"config_path"`
	Reloaded   bool   `json:"reloaded"`
	// ApachePort is the backend this site is served through, or zero when
	// nginx serves it directly.
	ApachePort int `json:"apache_port,omitempty"`
}

// Update rewrites a website's configuration.
//
// It deliberately does not touch the account or the directory permissions: a
// deployment may have re-permissioned its own files, and an update that
// silently re-owned them would break the site it was asked to reconfigure.
func (m *Manager) Update(ctx context.Context, req UpdateRequest, report func(int, string)) (UpdateResult, error) {
	if !m.Capabilities().Nginx {
		return UpdateResult{}, fmt.Errorf("%w: nginx is not installed", ErrUnsupported)
	}

	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.ServerName(domain); err != nil {
		return UpdateResult{}, err
	}

	aliases := make([]string, 0, len(req.Aliases))
	for _, alias := range req.Aliases {
		normalized := validate.NormalizeDomain(alias)
		if err := validate.ServerName(normalized); err != nil {
			return UpdateResult{}, fmt.Errorf("alias %q: %w", alias, err)
		}
		if normalized == domain {
			return UpdateResult{}, fmt.Errorf("alias %q duplicates the primary domain", alias)
		}
		aliases = append(aliases, normalized)
	}

	progress(report, 20, "Resolving site layout")
	// Named from the domain rather than inferred from the document root's
	// parent. An operator can now set the document root themselves, and
	// inferring the site from it would put this site's logs inside whatever
	// directory they chose — publishing the access log to anyone who guessed
	// its name.
	layout, err := m.fs.LayoutIn(m.fs.SiteDir(domain), req.DocumentRoot)
	if err != nil {
		return UpdateResult{}, err
	}

	// A document root the operator has pointed at but not deployed into yet is
	// created rather than refused: naming where a build will land, then
	// deploying, is the ordinary order of doing this, and nginx pointed at a
	// missing directory answers 404 to everything with nothing to say why.
	//
	// Only when it is missing. An existing directory keeps whatever ownership
	// a deployment gave it, for the reason above this function.
	if err := m.fs.EnsureContent(layout); err != nil {
		return UpdateResult{}, err
	}

	if err := validateBackendPort(req.ApachePort); err != nil {
		return UpdateResult{}, err
	}
	serve := resolveBackend(req.ProxyPort, req.ApachePort, req.PHPSocket)
	apacheCfg := apacheConfigFor(domain, aliases, layout, serve.apachePort,
		req.PHPSocket, req.MaxBodyBytes, req.AllowOverride)

	// Bringing Apache in happens before nginx is repointed; taking it out
	// happens after. A site must never be proxied to a backend that is not
	// there yet, and must never lose its backend while it is still proxied to.
	if serve.apachePort != 0 {
		progress(report, 40, "Configuring the Apache backend")
		if err := m.applyApache(ctx, apacheCfg, true); err != nil {
			return UpdateResult{}, err
		}
	}

	progress(report, 60, "Rewriting the web server configuration")
	configPath, err := m.nginx.WriteSite(ctx, nginx.SiteConfig{
		PrimaryDomain: domain,
		Aliases:       aliases,
		DocumentRoot:  layout.Content,
		AccessLog:     layout.AccessLog,
		ErrorLog:      layout.ErrorLog,
		MaxBodySize:   req.MaxBodySize,
		PHPSocket:     serve.phpSocket,
		ProxyPort:     serve.proxyPort,
		SSL:           req.SSL,
		Directives:    req.Directives,
	})
	if err != nil {
		return UpdateResult{}, err
	}

	progress(report, 90, "Reloading the web server")
	if err := m.nginx.Reload(ctx); err != nil {
		return UpdateResult{}, fmt.Errorf("configuration written but not applied: %w", err)
	}

	if serve.apachePort == 0 {
		// nginx is now serving this site itself, or proxying it to an
		// application, so the backend can go. A failure here is logged rather
		// than returned: the site is already being served correctly, and
		// refusing the operation would report a change that did happen as one
		// that did not.
		if err := m.applyApache(ctx, apacheCfg, false); err != nil {
			m.log.Warn("the site was updated but its Apache backend could not be removed",
				"domain", domain, logger.KeyError, err.Error())
		}
	}

	progress(report, 100, "Website updated")
	return UpdateResult{
		Domain:     domain,
		ConfigPath: configPath,
		Reloaded:   true,
		ApachePort: serve.apachePort,
	}, nil
}

// LookupAccount returns a site's system account.
//
// PHP pool creation needs the site's uid and gid to own the runtime
// directories it creates, and an account that does not exist means the site
// was never provisioned — which is a clearer failure than a pool written for
// a user FPM will refuse to run as.
func (m *Manager) LookupAccount(name string) (Account, error) {
	if m.users == nil {
		return Account{}, fmt.Errorf("%w: no user management tool is available", ErrUnsupported)
	}

	account, found, err := m.users.Lookup(name)
	if err != nil {
		return Account{}, err
	}
	if !found {
		return Account{}, fmt.Errorf("system account %q does not exist", name)
	}
	return account, nil
}
