// Package sites provisions a website's filesystem and system account.
//
// This is where the Agent's root privilege is actually spent, so every path is
// validated against an allowed root before anything is created, and ownership
// is set with syscalls rather than by shelling out to chown.
package sites

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/jothost/panel/agent/internal/pathsec"
)

// Directory layout under a site's root.
//
// Content and logs are separate directories so the site's own user can be
// given write access to what it serves without also being able to rewrite the
// record of who visited it.
const (
	ContentDir = "public"
	LogsDir    = "logs"
)

// Permissions for a provisioned site.
//
// The layout is owner = the site's own account, group = the web server's
// group, nothing for anyone else. That is what gives a site both isolation and
// a working server: the site user owns its content, the web server reads it
// through the group, and another site's user gets nothing.
//
// Group-owning by the site's own group instead looks tidier and produces a
// site that refuses every visitor, because the web server has no way through
// the directory. That mistake is invisible until the first request.
const (
	// siteRootMode lets the owner work and the web server traverse, but keeps
	// the world out of a directory that may hold application configuration.
	siteRootMode os.FileMode = 0o750
	// contentMode is the same: the web server must read what it serves.
	contentMode os.FileMode = 0o750
	// logsMode allows the same access; logs record visitor addresses.
	logsMode os.FileMode = 0o750
	// indexMode is readable by the owner and the web server's group.
	indexMode os.FileMode = 0o640
)

// Errors returned by the provisioner.
var (
	ErrOutsideRoot = errors.New("site path is outside the allowed root")
	ErrExists      = errors.New("site directory already exists")
	// ErrNoWebGroup means the web server's group could not be resolved, so a
	// provisioned site would not be readable by the server meant to serve it.
	ErrNoWebGroup = errors.New("the web server group could not be resolved")
)

// Layout is the resolved set of paths for one site.
type Layout struct {
	Root      string `json:"root"`
	Content   string `json:"content"`
	Logs      string `json:"logs"`
	AccessLog string `json:"access_log"`
	ErrorLog  string `json:"error_log"`
}

// ApacheAccessLog and ApacheErrorLog are where the backend writes in hybrid
// mode.
//
// Separate files rather than the ones above, because both servers are in the
// path and each records a different half of the truth: nginx sees the request
// arrive and the proxy hop, Apache sees what was actually served, which
// .htaccess rewrote it, and what PHP did. Interleaving them into one file
// would produce two lines per request that look like two requests.
func (l Layout) ApacheAccessLog() string {
	return filepath.Join(l.Logs, "apache-access.log")
}

func (l Layout) ApacheErrorLog() string {
	return filepath.Join(l.Logs, "apache-error.log")
}

// Provisioner creates and removes site directories.
type Provisioner struct {
	validator *pathsec.Validator
	// root is where sites live, e.g. /var/www.
	root string
	// webGID is the web server's group. Site directories are group-owned by it
	// so the server can read what it serves; -1 means it was not found.
	webGID   int
	webGroup string
}

// DefaultRoot is where website directories are created.
const DefaultRoot = "/var/www"

// DefaultWebGroups are tried in order when no group is configured.
//
// Distributions disagree: Alpine and RHEL run nginx as "nginx", Debian and
// Ubuntu as "www-data".
var DefaultWebGroups = []string{"nginx", "www-data"}

// NewProvisioner builds a Provisioner confined to root.
//
// webGroup names the web server's group; empty probes the conventional names.
// A group that cannot be resolved is not fatal here — the Agent still starts
// and every other operation works — but provisioning refuses rather than
// creating a site the server cannot read.
func NewProvisioner(root, webGroup string) (*Provisioner, error) {
	if root == "" {
		root = DefaultRoot
	}
	clean := filepath.Clean(root)

	// The directory must exist before the validator resolves it, or a symlink
	// check later has nothing to resolve against.
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return nil, fmt.Errorf("create site root %s: %w", clean, err)
	}

	validator, err := pathsec.NewValidator(clean)
	if err != nil {
		return nil, err
	}

	gid, resolved := resolveWebGroup(webGroup)
	return &Provisioner{
		validator: validator,
		root:      clean,
		webGID:    gid,
		webGroup:  resolved,
	}, nil
}

// resolveWebGroup finds the web server's group id.
func resolveWebGroup(name string) (int, string) {
	candidates := DefaultWebGroups
	if name != "" {
		candidates = []string{name}
	}

	for _, candidate := range candidates {
		group, err := user.LookupGroup(candidate)
		if err != nil {
			continue
		}
		if gid, err := strconv.Atoi(group.Gid); err == nil {
			return gid, candidate
		}
	}
	return -1, ""
}

// WebGroup returns the resolved web server group, or "" when none was found.
func (p *Provisioner) WebGroup() string { return p.webGroup }

// HasWebGroup reports whether a site can be made readable by the web server.
func (p *Provisioner) HasWebGroup() bool { return p.webGID >= 0 }

// Root returns the configured site root.
func (p *Provisioner) Root() string { return p.root }

// LayoutFor resolves the paths for a site without creating anything.
//
// The document root is supplied by the caller and must resolve inside the site
// root: the Agent runs as root, so a document root of "/etc" would otherwise
// hand a website's user ownership of the system's configuration.
func (p *Provisioner) LayoutFor(documentRoot string) (Layout, error) {
	resolved, err := p.validator.Resolve(documentRoot)
	if err != nil {
		if errors.Is(err, pathsec.ErrOutsideRoot) {
			return Layout{}, fmt.Errorf("%w: %s", ErrOutsideRoot, p.root)
		}
		return Layout{}, err
	}

	// The site root is the document root's parent, which is where logs live
	// alongside content rather than inside it — a log file under the document
	// root would be served to anyone who guessed its name.
	siteRoot := filepath.Dir(resolved)
	if siteRoot == p.root || siteRoot == "/" {
		return Layout{}, fmt.Errorf("%w: the document root must be nested inside %s",
			ErrOutsideRoot, p.root)
	}

	logs := filepath.Join(siteRoot, LogsDir)
	return Layout{
		Root:      siteRoot,
		Content:   resolved,
		Logs:      logs,
		AccessLog: filepath.Join(logs, "access.log"),
		ErrorLog:  filepath.Join(logs, "error.log"),
	}, nil
}

// Provision creates a site's directories and hands them to its account.
//
// It is idempotent: an existing directory is re-owned and re-permissioned
// rather than treated as an error, so a retried job converges instead of
// failing on its second attempt.
//
// gid is the site account's own group and is deliberately not used for the
// directories: they are group-owned by the web server so it can read them.
func (p *Provisioner) Provision(layout Layout, uid, gid int) error {
	if !p.HasWebGroup() {
		return fmt.Errorf("%w: set AGENT_WEB_GROUP to the group the web server runs as",
			ErrNoWebGroup)
	}
	gid = p.webGID

	directories := []struct {
		path string
		mode os.FileMode
	}{
		{layout.Root, siteRootMode},
		{layout.Content, contentMode},
		{layout.Logs, logsMode},
	}

	for _, dir := range directories {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("create %s: %w", dir.path, err)
		}
		// MkdirAll leaves an existing directory's mode alone, so it is set
		// explicitly; a directory created before a umask change would
		// otherwise keep the wrong permissions forever.
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return fmt.Errorf("chmod %s: %w", dir.path, err)
		}
		if uid >= 0 && gid >= 0 {
			// os.Chown rather than shelling out: one syscall, no argument to
			// validate, and nothing for a hostile path to exploit.
			if err := os.Chown(dir.path, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", dir.path, err)
			}
		}
	}

	return nil
}

// WritePlaceholder creates a landing page if the site has no content.
//
// A brand-new site with an empty document root returns 403 from nginx, which
// looks like a broken deployment rather than an empty one.
func (p *Provisioner) WritePlaceholder(layout Layout, domain string, uid, gid int) error {
	index := filepath.Join(layout.Content, "index.html")

	// The placeholder is served by the web server, so it carries the same
	// group ownership as the directory holding it.
	if p.HasWebGroup() {
		gid = p.webGID
	}

	if _, err := os.Stat(index); err == nil {
		// Content already exists; overwriting it would destroy a deployment.
		//
		// Its ownership is still corrected. A directory left behind by a
		// previous site holds files owned by an account that no longer exists,
		// and nginx cannot read them — so the new site returns 403 from the
		// moment it is created, which is precisely the outcome this function
		// exists to prevent. Ownership is the panel's to set at creation; the
		// content itself is not touched.
		if uid >= 0 && gid >= 0 {
			if err := os.Chown(index, uid, gid); err != nil {
				return fmt.Errorf("own the existing index: %w", err)
			}
		}
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat index: %w", err)
	}

	// The domain is inserted into HTML, so it is escaped. It is already
	// validated to be a hostname, but escaping costs nothing and means this
	// stays safe if the validation is ever relaxed.
	page := placeholderPage(domain)

	if err := os.WriteFile(index, []byte(page), indexMode); err != nil {
		return fmt.Errorf("write placeholder: %w", err)
	}
	if uid >= 0 && gid >= 0 {
		if err := os.Chown(index, uid, gid); err != nil {
			return fmt.Errorf("chown placeholder: %w", err)
		}
	}
	return nil
}

// Remove deletes a site's directory tree.
//
// The path is re-validated immediately before deletion rather than trusting
// the caller's earlier check: this is a recursive delete running as root, and
// the cost of being wrong is unbounded.
func (p *Provisioner) Remove(layout Layout) error {
	resolved, err := p.validator.Resolve(layout.Root)
	if err != nil {
		return fmt.Errorf("%w: refusing to remove %s", ErrOutsideRoot, layout.Root)
	}
	if resolved == p.root {
		return fmt.Errorf("%w: refusing to remove the site root itself", ErrOutsideRoot)
	}

	if err := os.RemoveAll(resolved); err != nil {
		return fmt.Errorf("remove site directory: %w", err)
	}
	return nil
}

// Exists reports whether a site's directory is present.
func (p *Provisioner) Exists(layout Layout) (bool, error) {
	if _, err := os.Stat(layout.Content); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat site content: %w", err)
	}
	return true, nil
}
