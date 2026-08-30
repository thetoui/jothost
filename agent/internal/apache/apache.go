// Package apache runs Apache httpd as a backend behind nginx.
//
// The arrangement is the one hosting panels have settled on: nginx owns port
// 80 and 443 and proxies to Apache on the loopback. Apache is there for what
// only Apache does — .htaccess, and the module ecosystem built around it —
// and nginx is there because it is the thing that should be facing the
// internet.
//
// Everything here is generated from a template and validated by httpd itself
// before it is made live, for the same reason the nginx package does it: a
// configuration file is executed by a process running as root.
package apache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Allowlist keys for the Apache binary.
//
// Both are listed because distributions disagree on the name: Alpine and RHEL
// ship "httpd", Debian and Ubuntu ship "apache2". The provider uses whichever
// is actually installed, and neither path is ever built from a request.
const (
	CommandHTTPD   = "httpd"
	CommandApache2 = "apache2"
)

// Errors returned by the provider.
var (
	// ErrUnavailable means Apache is not installed. It is reported explicitly
	// so a host without it degrades visibly rather than looking broken.
	ErrUnavailable = errors.New("apache is not available on this host")
	// ErrInvalidConfig means Apache rejected the generated configuration.
	ErrInvalidConfig = errors.New("apache rejected the configuration")
	// ErrNoWebGroup means the group Apache must read site files through was
	// not resolved, so a site would be provisioned and then serve 403.
	ErrNoWebGroup = errors.New("the web server group could not be resolved")
)

// configMode keeps a vhost readable by Apache but not writable by anything
// else. The file is generated; nothing should be editing it in place.
const configMode os.FileMode = 0o644

// Filenames the panel owns inside Apache's config directory.
const (
	// BaseConfigName holds everything that is true of the whole server. The
	// numeric prefix makes it sort before the site files, which matters:
	// LoadModule has to have happened before a vhost uses the module.
	BaseConfigName = "00-jothost.conf"
	// sitePrefix marks the files this panel generates, so a vhost written by
	// hand next to them is never removed by a reconcile.
	sitePrefix = "jothost-"
	// WildcardConfigPrefix names the vhost file of a wildcard site: the
	// asterisk is a glob to every tool that later walks this directory.
	WildcardConfigPrefix = "_wildcard."
)

// Provider writes and applies Apache configuration.
type Provider struct {
	runner *command.Runner
	// command is the allowlist key that resolved, or "" when Apache is absent.
	command string
	// sitesDir holds the panel's base config and one file per website.
	sitesDir string
	// mainConfig is httpd.conf, which the panel edits in the narrow, marked
	// way described in base.go.
	mainConfig string
	// webGroup is the group Apache reads site content through. Sites are
	// group-owned by it, which is how nginx reads them too.
	webGroup string
}

// Options configures a Provider.
type Options struct {
	Runner *command.Runner
	// SitesDir defaults to /etc/apache2/conf.d.
	SitesDir string
	// MainConfig defaults to /etc/apache2/httpd.conf.
	MainConfig string
	// WebGroup is the group Apache runs as, so it can read what the panel's
	// sites make readable to the web server.
	WebGroup string
}

// Defaults for a stock Alpine or RHEL layout.
const (
	DefaultSitesDir   = "/etc/apache2/conf.d"
	DefaultMainConfig = "/etc/apache2/httpd.conf"
)

// NewProvider builds a Provider.
//
// The Apache binary is resolved once, here, from the allowlist — never from a
// request, and never through PATH.
func NewProvider(opts Options) *Provider {
	sitesDir := opts.SitesDir
	if sitesDir == "" {
		sitesDir = DefaultSitesDir
	}
	mainConfig := opts.MainConfig
	if mainConfig == "" {
		mainConfig = DefaultMainConfig
	}

	provider := &Provider{
		runner:     opts.Runner,
		sitesDir:   filepath.Clean(sitesDir),
		mainConfig: filepath.Clean(mainConfig),
		webGroup:   opts.WebGroup,
	}

	if opts.Runner != nil {
		for _, name := range []string{CommandHTTPD, CommandApache2} {
			if opts.Runner.Available(name) {
				provider.command = name
				break
			}
		}
	}
	return provider
}

// Rebind re-resolves the Apache binary.
//
// The allowlist is fixed at startup and both Apache paths are in it whether or
// not the binary exists, so this is only re-asking the question "is it there
// now" — which is exactly what changes when the panel installs Apache. Without
// it, enabling hybrid mode on a host that had no Apache would report success
// and then serve nothing until the Agent was restarted.
func (p *Provider) Rebind() {
	if p == nil || p.runner == nil {
		return
	}
	p.command = ""
	for _, name := range []string{CommandHTTPD, CommandApache2} {
		if p.runner.Available(name) {
			p.command = name
			return
		}
	}
}

// Available reports whether Apache can be used on this host.
func (p *Provider) Available() bool { return p != nil && p.command != "" }

// SitesDir returns the configured configuration directory.
func (p *Provider) SitesDir() string { return p.sitesDir }

// Version returns the running Apache's version string.
func (p *Provider) Version(ctx context.Context) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}

	result, err := p.runner.Run(ctx, p.command, "-v")
	if err != nil {
		return "", err
	}
	if !result.Succeeded() {
		return "", fmt.Errorf("apache -v: %s", firstLine(result.Stderr))
	}

	// "Server version: Apache/2.4.68 (Unix)" — the number is what a panel
	// shows, and the rest is noise to everyone but a packager.
	for _, line := range strings.Split(result.Stdout, "\n") {
		if _, rest, found := strings.Cut(line, "Apache/"); found {
			version, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
			return version, nil
		}
	}
	return "", fmt.Errorf("apache -v: could not read a version from %q",
		firstLine(result.Stdout))
}

// configPath returns the vhost file for a domain.
//
// The domain is validated before it becomes part of a path, so a value
// containing a slash can never place a root-owned config outside sitesDir.
func (p *Provider) configPath(domain string) (string, error) {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.ServerName(normalized); err != nil {
		return "", err
	}

	filename := normalized
	if validate.IsWildcard(filename) {
		filename = WildcardConfigPrefix +
			strings.TrimPrefix(filename, validate.WildcardPrefix)
	}

	path := filepath.Join(p.sitesDir, sitePrefix+filename+".conf")
	if filepath.Dir(path) != p.sitesDir {
		// Unreachable given the validation above; kept because the cost of
		// being wrong here is writing a root-owned config anywhere on disk.
		return "", fmt.Errorf("refusing to write outside %s", p.sitesDir)
	}
	return path, nil
}

// WriteSite renders and installs a website's Apache configuration.
//
// The config is validated before it is made live and the previous version is
// restored if validation fails, so a bad generated config can never leave
// Apache unable to start — which in hybrid mode means every site on the host
// answering 502, not just this one.
func (p *Provider) WriteSite(ctx context.Context, cfg SiteConfig) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}
	if p.webGroup == "" {
		// Apache would run as its own account, which is in none of the site
		// groups: every request would be 403 on a site that looks correct.
		return "", ErrNoWebGroup
	}

	rendered, err := Render(cfg)
	if err != nil {
		return "", err
	}
	return p.install(ctx, cfg.PrimaryDomain, rendered)
}

// install writes a config, validates it, and restores the previous one if
// Apache rejects the result.
func (p *Provider) install(ctx context.Context, domain, rendered string) (string, error) {
	path, err := p.configPath(domain)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(p.sitesDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", p.sitesDir, err)
	}

	previous, hadPrevious := readIfExists(path)

	if err := os.WriteFile(path, []byte(rendered), configMode); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}

	if err := p.Validate(ctx); err != nil {
		// Put back exactly what was there. Leaving a rejected file behind
		// would mean the next unrelated reload — another site's — fails too.
		if hadPrevious {
			if restoreErr := os.WriteFile(path, previous, configMode); restoreErr != nil {
				return "", fmt.Errorf("%w (and the previous config could not be "+
					"restored: %v)", err, restoreErr)
			}
		} else if removeErr := os.Remove(path); removeErr != nil {
			return "", fmt.Errorf("%w (and the rejected config could not be "+
				"removed: %v)", err, removeErr)
		}
		return "", err
	}

	return path, nil
}

// RemoveSite deletes a website's Apache configuration.
//
// It reports whether a file was actually removed, so a caller can tell "there
// was nothing there" from "it is gone now" — the difference between a
// no-op and an operation that did something.
func (p *Provider) RemoveSite(ctx context.Context, domain string) (bool, error) {
	path, err := p.configPath(domain)
	if err != nil {
		return false, err
	}

	previous, hadPrevious := readIfExists(path)
	if !hadPrevious {
		return false, nil
	}

	if err := os.Remove(path); err != nil {
		return false, fmt.Errorf("remove %s: %w", path, err)
	}

	// Removing a vhost can break the configuration as surely as adding one:
	// its Listen directive may be the only thing another file's directives
	// were valid against. If Apache now refuses the tree, the file goes back.
	if err := p.Validate(ctx); err != nil {
		if restoreErr := os.WriteFile(path, previous, configMode); restoreErr != nil {
			return false, fmt.Errorf("%w (and the config could not be restored: %v)",
				err, restoreErr)
		}
		return false, err
	}
	return true, nil
}

// SiteExists reports whether a website has an Apache configuration.
func (p *Provider) SiteExists(domain string) (bool, error) {
	path, err := p.configPath(domain)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	return true, nil
}

// SiteCount returns how many websites this panel has configured in Apache.
//
// It is what decides whether Apache should still be running: with no vhost of
// ours there is no Listen directive, and Apache refuses to start at all with
// no listening socket. Counting is therefore not a nicety — it is how the last
// site removed leads to a stopped server rather than a failed start.
func (p *Provider) SiteCount() (int, error) {
	entries, err := os.ReadDir(p.sitesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read %s: %w", p.sitesDir, err)
	}

	count := 0
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, sitePrefix) && strings.HasSuffix(name, ".conf") {
			count++
		}
	}
	return count, nil
}

// Validate asks Apache whether the configuration on disk is acceptable.
func (p *Provider) Validate(ctx context.Context) error {
	if !p.Available() {
		return ErrUnavailable
	}

	result, err := p.runner.Run(ctx, p.command, "-t")
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		// httpd writes its diagnosis to stderr; the exit code alone says
		// nothing an operator can act on.
		detail := strings.TrimSpace(result.Stderr)
		if detail == "" {
			detail = strings.TrimSpace(result.Stdout)
		}
		return fmt.Errorf("%w: %s", ErrInvalidConfig, detail)
	}
	return nil
}

// Running reports whether Apache is currently up.
func (p *Provider) Running(ctx context.Context) bool {
	if !p.Available() {
		return false
	}
	// "-k graceful" on a stopped server starts it, so it cannot be used as a
	// probe. httpd -S parses the config and lists the vhosts without touching
	// the running server, so the pid file is what actually answers this.
	data, err := os.ReadFile(p.pidFile())
	if err != nil {
		return false
	}
	pid := strings.TrimSpace(string(data))
	if pid == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join("/proc", pid)); err != nil {
		// A pid file left behind by a killed process. Apache would refuse to
		// start over it, so reporting "running" would be a lie that turns into
		// a confusing failure at the next reload.
		return false
	}
	return true
}

// pidFile is where Apache records its master process.
func (p *Provider) pidFile() string { return "/run/apache2/httpd.pid" }

// Apply makes the configuration live.
//
// It starts Apache if it is not running and reloads it gracefully if it is.
// Graceful rather than a restart: existing connections finish on the old
// configuration instead of being dropped, which is the same promise nginx
// makes and the reason a config change is not an outage.
func (p *Provider) Apply(ctx context.Context) error {
	if !p.Available() {
		return ErrUnavailable
	}

	if err := p.Validate(ctx); err != nil {
		// Applying an invalid configuration is how a whole host goes down.
		return err
	}

	action := "graceful"
	if !p.Running(ctx) {
		action = "start"
	}

	if err := os.MkdirAll(filepath.Dir(p.pidFile()), 0o755); err != nil {
		return fmt.Errorf("create the apache run directory: %w", err)
	}

	result, err := p.runner.Run(ctx, p.command, "-k", action)
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		return fmt.Errorf("apache -k %s: %s", action, firstLine(result.Stderr))
	}
	return nil
}

// Stop shuts Apache down.
//
// Used when the last site leaves hybrid mode: Apache with no vhost of ours has
// no Listen directive and cannot start, so leaving it "enabled" would mean
// every later reload failing for a reason that has nothing to do with the
// change being made.
func (p *Provider) Stop(ctx context.Context) error {
	if !p.Available() || !p.Running(ctx) {
		return nil
	}

	result, err := p.runner.Run(ctx, p.command, "-k", "stop")
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		return fmt.Errorf("apache -k stop: %s", firstLine(result.Stderr))
	}
	return nil
}

// readIfExists returns a file's contents, and whether it was there.
func readIfExists(path string) ([]byte, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path is built by configPath
	if err != nil {
		return nil, false
	}
	return data, true
}

// firstLine returns the first non-empty line of output.
func firstLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(text)
}
