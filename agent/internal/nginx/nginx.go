package nginx

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandName is the allowlist key for the nginx binary.
const CommandName = "nginx"

// Errors returned by the provider.
var (
	// ErrUnavailable means nginx is not installed. It is reported explicitly
	// so a host without it degrades visibly rather than looking broken.
	ErrUnavailable = errors.New("nginx is not available on this host")
	// ErrInvalidConfig means nginx rejected the generated configuration.
	ErrInvalidConfig = errors.New("nginx rejected the configuration")
)

// configMode keeps a vhost readable by nginx but not writable by anything
// else. The file is generated; nothing should be editing it in place.
const configMode os.FileMode = 0o644

// Provider writes and applies nginx configuration.
type Provider struct {
	runner *command.Runner
	// sitesDir holds one file per website.
	sitesDir string
	// procRoot is where worker processes are observed, so a drain can be
	// waited for. Configurable so tests need no real nginx.
	procRoot string
	// configPath and prefix name a private nginx instance. Empty means the
	// host's own nginx, started by its init system and reading /etc/nginx.
	//
	// The panel runs a second instance for its own applications, so a website
	// whose configuration nginx refuses cannot take phpMyAdmin down with it,
	// and a website the panel writes can never be confused with one of the
	// panel's own. Both instances are driven through this one type, so the
	// allowlisting, the validate-before-reload rule and the drain wait are
	// written once.
	instanceConfig string
	instancePrefix string
	instanceLog    string
}

// Options configures a Provider.
type Options struct {
	Runner *command.Runner
	// SitesDir defaults to /etc/nginx/conf.d. It is configurable so tests can
	// write to a temporary tree instead of the real one.
	SitesDir string
	// ProcRoot defaults to /proc.
	ProcRoot string
	// ConfigPath and Prefix select a private instance. Both empty means the
	// host's own nginx; setting them makes every command carry -c and -p.
	ConfigPath string
	Prefix     string
	// ErrorLog is where a private instance writes errors it hits before it has
	// parsed its own error_log directive.
	//
	// Without it nginx opens its compiled-in default, which is relative and so
	// resolves against Prefix — a path inside the panel's tree that nothing
	// created, and the instance refuses to start over it.
	ErrorLog string
}

// DefaultSitesDir is where generated vhosts live.
const DefaultSitesDir = "/etc/nginx/conf.d"

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	sitesDir := opts.SitesDir
	if sitesDir == "" {
		sitesDir = DefaultSitesDir
	}
	procRoot := opts.ProcRoot
	if procRoot == "" {
		procRoot = DefaultProcRoot
	}
	provider := &Provider{
		runner:   opts.Runner,
		sitesDir: filepath.Clean(sitesDir),
		procRoot: filepath.Clean(procRoot),
	}
	if opts.ConfigPath != "" {
		provider.instanceConfig = filepath.Clean(opts.ConfigPath)
	}
	if opts.Prefix != "" {
		provider.instancePrefix = filepath.Clean(opts.Prefix)
	}
	if opts.ErrorLog != "" {
		provider.instanceLog = filepath.Clean(opts.ErrorLog)
	}
	return provider
}

// Private reports whether this provider drives an instance of the panel's own
// rather than the host's nginx.
func (p *Provider) Private() bool { return p.instanceConfig != "" }

// args prefixes a command with the flags that select this instance.
//
// Every nginx invocation goes through here. An instance flag left off one
// command is the failure that is hardest to see: the command succeeds, against
// the wrong nginx.
func (p *Provider) args(rest ...string) []string {
	if p.instanceConfig == "" {
		return rest
	}
	out := make([]string, 0, len(rest)+4)
	if p.instancePrefix != "" {
		out = append(out, "-p", p.instancePrefix)
	}
	out = append(out, "-c", p.instanceConfig)
	if p.instanceLog != "" {
		out = append(out, "-e", p.instanceLog)
	}
	return append(out, rest...)
}

// Available reports whether nginx can be used.
func (p *Provider) Available() bool {
	return p.runner != nil && p.runner.Available(CommandName)
}

// SitesDir returns the configured vhost directory.
func (p *Provider) SitesDir() string { return p.sitesDir }

// configPath returns the vhost file for a domain.
//
// The domain is validated before it becomes part of a path, so a value
// containing a slash or "…" can never place the file outside sitesDir.
//
// A wildcard name is written to a file with the asterisk replaced. The
// character is legal in a server_name and legal in a filename, but every tool
// that later touches this directory — a shell loop over *.conf, a backup, an
// rsync — would expand it against whatever else is there. The name in the file
// is still the wildcard; only the filename is literal.
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

	path := filepath.Join(p.sitesDir, filename+".conf")
	if filepath.Dir(path) != p.sitesDir {
		// Unreachable given the validation above; kept because the cost of
		// being wrong here is writing a root-owned config anywhere on disk.
		return "", fmt.Errorf("refusing to write outside %s", p.sitesDir)
	}
	return path, nil
}

// WriteSite renders and installs a website's configuration.
//
// The config is validated before it is made live and the previous version is
// restored if validation fails, so a bad generated config can never leave
// nginx unable to reload — which would take every other site down with it.
func (p *Provider) WriteSite(ctx context.Context, cfg SiteConfig) (string, error) {
	rendered, err := Render(cfg)
	if err != nil {
		return "", err
	}
	return p.install(ctx, cfg.PrimaryDomain, rendered)
}

// WriteRedirect renders and installs a redirecting domain.
func (p *Provider) WriteRedirect(ctx context.Context, redirect Redirect) (string, error) {
	rendered, err := RenderRedirect(redirect)
	if err != nil {
		return "", err
	}
	return p.install(ctx, redirect.Domain, rendered)
}

// WildcardConfigPrefix names the vhost file of a wildcard site.
//
// The leading underscore also sorts it before ordinary names, which matches
// how nginx treats it: an exact server_name always wins over a wildcard,
// whatever order the files are included in.
const WildcardConfigPrefix = "_wildcard."

// ProxyMapName is the file holding the map every proxied site needs.
//
// A leading zero keeps it first in the include order. nginx reads conf.d in
// sorted order and a server block referring to $connection_upgrade before the
// map defines it is a configuration nginx refuses to start with.
const ProxyMapName = "00-jothost-proxy.conf"

// EnsureProxyMap writes the map a reverse-proxied site depends on.
//
// It is written once for the host rather than per site, because nginx refuses
// to start with a duplicate map. Called at startup so a proxy vhost written
// later always has it, rather than the first one failing validation for a
// reason that has nothing to do with that site.
func (p *Provider) EnsureProxyMap() error {
	if p.sitesDir == "" {
		return nil
	}
	if err := os.MkdirAll(p.sitesDir, 0o755); err != nil {
		return fmt.Errorf("create the sites directory: %w", err)
	}

	path := filepath.Join(p.sitesDir, ProxyMapName)
	existing, err := os.ReadFile(path)
	if err == nil && string(existing) == ConnectionUpgradeMap {
		return nil
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	if err := os.WriteFile(path, []byte(ConnectionUpgradeMap), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// WriteSiteRaw installs a configuration the caller has already rendered.
//
// It exists for a site the panel serves that is not a customer website — the
// phpMyAdmin vhost — where the content is composed by its own package but the
// writing, validation, and rollback have to be the same as for every other
// site. The domain is still validated, and the file is still kept only if
// nginx accepts the result.
func (p *Provider) WriteSiteRaw(ctx context.Context, domain, rendered string) (string, error) {
	return p.install(ctx, domain, rendered)
}

// install writes a config, validates it, and rolls back on failure.
func (p *Provider) install(ctx context.Context, domain, rendered string) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}

	path, err := p.configPath(domain)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(p.sitesDir, 0o755); err != nil {
		return "", fmt.Errorf("create sites directory: %w", err)
	}

	// The previous content is kept in memory so a failed validation can put it
	// back exactly, including "no file existed".
	previous, hadPrevious, err := readIfExists(path)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(rendered), configMode); err != nil { //nolint:gosec // generated config must be world-readable by nginx workers
		return "", fmt.Errorf("write site config: %w", err)
	}

	if err := p.Validate(ctx); err != nil {
		// Roll back before returning: leaving an invalid file on disk means
		// the next legitimate reload — by anyone, for any site — fails.
		if restoreErr := restore(path, previous, hadPrevious); restoreErr != nil {
			return "", errors.Join(err, restoreErr)
		}
		return "", err
	}

	return path, nil
}

// RemoveSite deletes a website's configuration.
//
// Removing a file is not validated the way writing one is: an absent vhost
// cannot make the remaining config invalid, and refusing to delete because
// something else is already broken would leave the site serving.
func (p *Provider) RemoveSite(ctx context.Context, domain string) (bool, error) {
	path, err := p.configPath(domain)
	if err != nil {
		return false, err
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			// Already gone: deletion is idempotent so a retried job succeeds.
			return false, nil
		}
		return false, fmt.Errorf("remove site config: %w", err)
	}
	return true, nil
}

// SiteExists reports whether a vhost is installed.
func (p *Provider) SiteExists(domain string) (bool, error) {
	path, err := p.configPath(domain)
	if err != nil {
		return false, err
	}

	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat site config: %w", err)
	}
	return true, nil
}

// Validate runs `nginx -t`.
func (p *Provider) Validate(ctx context.Context) error {
	if !p.Available() {
		return ErrUnavailable
	}

	result, err := p.runner.Run(ctx, CommandName, p.args("-t")...)
	if err != nil {
		return fmt.Errorf("run nginx -t: %w", err)
	}
	if !result.Succeeded() {
		// nginx writes its diagnostics to stderr. They name a file and line
		// and are what makes a failure actionable, so they are preserved for
		// the caller to log — the API still reports a generic message.
		return fmt.Errorf("%w: %s", ErrInvalidConfig, summarize(result.Stderr))
	}
	return nil
}

// Reload asks nginx to re-read its configuration.
//
// Validation runs first: `nginx -s reload` on a broken config leaves the old
// workers serving and reports success, which would make the panel claim a
// change was applied when it was not.
func (p *Provider) Reload(ctx context.Context) error {
	if err := p.Validate(ctx); err != nil {
		return err
	}

	result, err := p.runner.Run(ctx, CommandName, p.args("-s", "reload")...)
	if err != nil {
		return fmt.Errorf("reload nginx: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("nginx reload failed: %s", summarize(result.Stderr))
	}
	return nil
}

// Version reports the installed nginx version.
func (p *Provider) Version(ctx context.Context) (string, error) {
	if !p.Available() {
		return "", ErrUnavailable
	}

	result, err := p.runner.Run(ctx, CommandName, "-v")
	if err != nil {
		return "", err
	}

	// nginx writes its version banner to stderr.
	output := result.Stderr + result.Stdout
	if match := versionPattern.FindStringSubmatch(output); match != nil {
		return match[1], nil
	}
	return strings.TrimSpace(output), nil
}

var versionPattern = regexp.MustCompile(`nginx/([0-9]+\.[0-9]+\.[0-9]+)`)

// readIfExists returns a file's content, reporting whether it was there.
func readIfExists(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is built by configPath from a validated domain
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read existing config: %w", err)
	}
	return content, true, nil
}

// restore puts a config file back the way it was.
func restore(path string, previous []byte, hadPrevious bool) error {
	if !hadPrevious {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove invalid config: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, previous, configMode); err != nil { //nolint:gosec // restoring the previous generated config
		return fmt.Errorf("restore previous config: %w", err)
	}
	return nil
}

// maxDiagnosticLength bounds how much nginx output travels in an error.
const maxDiagnosticLength = 512

// summarize trims nginx's output to something loggable.
func summarize(output string) string {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "no diagnostic output"
	}
	if len(trimmed) > maxDiagnosticLength {
		return trimmed[:maxDiagnosticLength] + "…"
	}
	return trimmed
}

// validatePath rejects a path that is not absolute or contains traversal.
func validatePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("%w: path is required", validate.ErrInvalidPath)
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("%w: must be absolute", validate.ErrInvalidPath)
	case strings.Contains(path, ".."):
		return fmt.Errorf("%w: must not contain '..'", validate.ErrInvalidPath)
	case strings.ContainsAny(path, "\x00\n\r;{}"):
		// A newline or brace would let a path close the directive and open a
		// new one, which is how a path becomes arbitrary nginx configuration.
		return fmt.Errorf("%w: contains an illegal character", validate.ErrInvalidPath)
	default:
		return nil
	}
}

// validateSize checks an nginx size value such as "64m".
func validateSize(value string) error {
	if !sizePattern.MatchString(value) {
		return fmt.Errorf("invalid size %q: expected a number optionally followed by k, m, or g", value)
	}
	return nil
}

var sizePattern = regexp.MustCompile(`^[0-9]+[kmgKMG]?$`)
