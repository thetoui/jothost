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
}

// Options configures a Provider.
type Options struct {
	Runner *command.Runner
	// SitesDir defaults to /etc/nginx/conf.d. It is configurable so tests can
	// write to a temporary tree instead of the real one.
	SitesDir string
	// ProcRoot defaults to /proc.
	ProcRoot string
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
	return &Provider{
		runner:   opts.Runner,
		sitesDir: filepath.Clean(sitesDir),
		procRoot: filepath.Clean(procRoot),
	}
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
func (p *Provider) configPath(domain string) (string, error) {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return "", err
	}

	path := filepath.Join(p.sitesDir, normalized+".conf")
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

	result, err := p.runner.Run(ctx, CommandName, "-t")
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

	result, err := p.runner.Run(ctx, CommandName, "-s", "reload")
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
