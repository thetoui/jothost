package sites

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
	log   *slog.Logger
}

// ManagerOptions configures a Manager.
type ManagerOptions struct {
	Filesystem *Provisioner
	Users      *UserProvider
	Nginx      *nginx.Provider
	Log        *slog.Logger
}

// NewManager builds a Manager.
func NewManager(opts ManagerOptions) *Manager {
	return &Manager{
		fs:    opts.Filesystem,
		users: opts.Users,
		nginx: opts.Nginx,
		log:   opts.Log,
	}
}

// Capabilities reports what this host can actually do.
type Capabilities struct {
	Nginx bool `json:"nginx"`
	Users bool `json:"users"`
	// WebGroup reports whether site directories can be made readable by the
	// web server. Without it a site is created and then serves nothing.
	WebGroup bool `json:"web_group"`
}

// Capabilities describes the host's website support.
func (m *Manager) Capabilities() Capabilities {
	return Capabilities{
		Nginx:    m.nginx != nil && m.nginx.Available(),
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
	if err := validate.Domain(domain); err != nil {
		return CreateResult{}, err
	}
	if err := validate.SystemUser(req.SystemUser); err != nil {
		return CreateResult{}, err
	}

	aliases := make([]string, 0, len(req.Aliases))
	for _, alias := range req.Aliases {
		normalized := validate.NormalizeDomain(alias)
		if err := validate.Domain(normalized); err != nil {
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

	progress(report, 70, "Writing the web server configuration")
	configPath, err := m.nginx.WriteSite(ctx, nginx.SiteConfig{
		PrimaryDomain: domain,
		Aliases:       aliases,
		DocumentRoot:  layout.Content,
		AccessLog:     layout.AccessLog,
		ErrorLog:      layout.ErrorLog,
		MaxBodySize:   req.MaxBodySize,
		PHPSocket:     req.PHPSocket,
		SSL:           req.SSL,
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
	FilesRemoved  bool   `json:"files_removed"`
	UserRemoved   bool   `json:"user_removed"`
	Reloaded      bool   `json:"reloaded"`
}

// Delete removes a website.
//
// The vhost goes first and nginx is reloaded immediately, so the site stops
// being served before its files are touched. Removing content from under a
// live server would serve half-deleted directories to whoever is browsing.
func (m *Manager) Delete(ctx context.Context, req DeleteRequest, report func(int, string)) (DeleteResult, error) {
	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.Domain(domain); err != nil {
		return DeleteResult{}, err
	}

	result := DeleteResult{Domain: domain}

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

	if req.RemoveFiles && req.DocumentRoot != "" {
		progress(report, 65, "Removing site files")
		layout, err := m.fs.LayoutFor(req.DocumentRoot)
		if err != nil {
			return result, err
		}
		if err := m.fs.Remove(layout); err != nil {
			return result, err
		}
		result.FilesRemoved = true
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
	if err := validate.Domain(normalized); err != nil {
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
}

// UpdateResult reports what was rewritten.
type UpdateResult struct {
	Domain     string `json:"domain"`
	ConfigPath string `json:"config_path"`
	Reloaded   bool   `json:"reloaded"`
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
	if err := validate.Domain(domain); err != nil {
		return UpdateResult{}, err
	}

	aliases := make([]string, 0, len(req.Aliases))
	for _, alias := range req.Aliases {
		normalized := validate.NormalizeDomain(alias)
		if err := validate.Domain(normalized); err != nil {
			return UpdateResult{}, fmt.Errorf("alias %q: %w", alias, err)
		}
		if normalized == domain {
			return UpdateResult{}, fmt.Errorf("alias %q duplicates the primary domain", alias)
		}
		aliases = append(aliases, normalized)
	}

	progress(report, 20, "Resolving site layout")
	layout, err := m.fs.LayoutFor(req.DocumentRoot)
	if err != nil {
		return UpdateResult{}, err
	}

	progress(report, 60, "Rewriting the web server configuration")
	configPath, err := m.nginx.WriteSite(ctx, nginx.SiteConfig{
		PrimaryDomain: domain,
		Aliases:       aliases,
		DocumentRoot:  layout.Content,
		AccessLog:     layout.AccessLog,
		ErrorLog:      layout.ErrorLog,
		MaxBodySize:   req.MaxBodySize,
		PHPSocket:     req.PHPSocket,
		SSL:           req.SSL,
	})
	if err != nil {
		return UpdateResult{}, err
	}

	progress(report, 90, "Reloading the web server")
	if err := m.nginx.Reload(ctx); err != nil {
		return UpdateResult{}, fmt.Errorf("configuration written but not applied: %w", err)
	}

	progress(report, 100, "Website updated")
	return UpdateResult{Domain: domain, ConfigPath: configPath, Reloaded: true}, nil
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
