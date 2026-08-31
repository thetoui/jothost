package ftp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
)

// Provider manages the host's FTP server.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	paths  Paths
	// reload restarts the daemon after a configuration change. It is a
	// function rather than a service-manager dependency so this package does
	// not reach into the one that owns daemon lifecycles — the Agent wires the
	// two together.
	reload Reloader
}

// Reloader restarts the FTP daemon and reports whether it is running.
type Reloader interface {
	// Reload applies a configuration change to the running daemon.
	//
	// proftpd is restarted rather than signalled: it reads PassivePorts and the
	// TLS material at startup, and a SIGHUP leaves a server that reports the
	// new configuration while still listening with the old one.
	Reload(ctx context.Context) error
	// Running reports whether the daemon is up.
	Running(ctx context.Context) bool
}

// Options configure a Provider.
type Options struct {
	Runner *command.Runner
	Log    *slog.Logger
	Paths  Paths
	Reload Reloader
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	paths := opts.Paths
	if paths.ConfigDir == "" {
		paths.ConfigDir = DefaultConfigDir
	}
	if paths.RunDir == "" {
		paths.RunDir = DefaultPaths().RunDir
	}
	return &Provider{runner: opts.Runner, log: log, paths: paths, reload: opts.Reload}
}

// SetReloader supplies the thing that restarts the daemon.
//
// It is set after construction because the restarter reaches back through the
// operation registry to the service manager, and the registry is built from
// this provider — so one of the two has to be wired second. The alternative,
// giving this package its own service-manager dependency, would mean two things
// on the host able to start the FTP daemon.
func (p *Provider) SetReloader(r Reloader) { p.reload = r }

// Paths reports where this Provider keeps its files.
func (p *Provider) Paths() Paths { return p.paths }

// Available reports whether this host has an FTP server the panel can manage.
func (p *Provider) Available() bool {
	if p == nil || p.runner == nil {
		return false
	}
	// Both are needed: the daemon to serve, and ftpasswd to maintain the
	// accounts. A host with proftpd but no proftpd-utils would let the panel
	// write a configuration and then fail at the first account, which is a
	// worse answer than "not installed".
	return p.runner.Available(CommandProftpd) && p.runner.Available(CommandFtpasswd)
}

// Running reports whether the daemon is up.
func (p *Provider) Running(ctx context.Context) bool {
	if !p.Available() || p.reload == nil {
		return false
	}
	return p.reload.Running(ctx)
}

// Version reports which proftpd this is.
func (p *Provider) Version(ctx context.Context) string {
	if !p.Available() {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandProftpd, "-v")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "ProFTPD Version 1.3.8b" — everything after the word "Version".
	line := firstLine(result.Stdout, result.Stderr)
	if _, rest, found := strings.Cut(line, "Version"); found {
		return strings.TrimSpace(rest)
	}
	return strings.TrimSpace(line)
}

// Modules reports which proftpd modules this host has, so the panel can say
// why a feature is not on offer instead of writing a configuration the daemon
// will silently ignore.
//
// Two sources, and both are needed. `proftpd -l` lists only the modules
// *compiled in*; mod_tls and mod_quotatab are shipped as separate packages and
// loaded at runtime through mod_dso, so on a normal host neither appears there
// while both work perfectly.
//
// Getting this wrong is quiet in the worst way: proftpd's <IfModule> block
// skips its contents when the module is absent, so a panel that configured FTPS
// without it would write a valid file, restart cleanly, report success, and
// serve plain FTP.
func (p *Provider) Modules(ctx context.Context) map[string]bool {
	found := map[string]bool{}
	if !p.Available() {
		return found
	}

	if result, err := p.runner.Run(ctx, CommandProftpd, "-l"); err == nil && result.Succeeded() {
		for _, line := range strings.Split(result.Stdout, "\n") {
			name := strings.TrimSpace(line)
			if strings.HasSuffix(name, ".c") {
				found[strings.TrimSuffix(name, ".c")] = true
			}
		}
	}

	for name := range p.loadedModules() {
		found[name] = true
	}
	return found
}

// loadedModules reads the LoadModule directives in the modules directory.
//
// The files are read rather than the daemon asked, because proftpd has no way
// to report its runtime modules — there is no equivalent of `sshd -T`. These
// are the same files the daemon reads at startup.
func (p *Provider) loadedModules() map[string]bool {
	found := map[string]bool{}

	dir := filepath.Join(p.paths.root(), modulesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		// A host with no modules directory has no loadable modules, which is a
		// true answer rather than a failure.
		return found
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			// A commented-out LoadModule is a module that is not loaded, and
			// the distribution ships several.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			rest, ok := strings.CutPrefix(trimmed, "LoadModule ")
			if !ok {
				continue
			}
			name := strings.TrimSuffix(strings.TrimSpace(rest), ".c")
			if name != "" {
				found[name] = true
			}
		}
	}
	return found
}

// SupportsTLS reports whether FTPS can be offered on this host.
func (p *Provider) SupportsTLS(ctx context.Context) bool {
	return p.Modules(ctx)["mod_tls"]
}

// SupportsQuota reports whether per-account disk limits can be enforced.
func (p *Provider) SupportsQuota(ctx context.Context) bool {
	m := p.Modules(ctx)
	return m["mod_quotatab"] && m["mod_quotatab_file"]
}

// Validate asks proftpd whether a configuration is one it will start with.
//
// It reads the *whole* configuration, the distribution's file included, so a
// panel setting that conflicts with something else on this host is caught here
// rather than at the next restart — by which time the daemon is down.
func (p *Provider) Validate(ctx context.Context) error {
	result, err := p.runner.Run(ctx, CommandProftpd, "-t")
	if err != nil {
		return fmt.Errorf("check the FTP configuration: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// ensureDirs creates the directories this package owns.
//
// The state directory is 0700 and owned by root: it holds password hashes, and
// proftpd reads it as root before dropping privileges, so nothing else needs to
// be able to look. The scoreboard directory has to exist before proftpd starts
// or ftpwho reports an empty server while sessions are in progress.
func (p *Provider) ensureDirs() error {
	if err := os.MkdirAll(p.paths.State(), 0o700); err != nil {
		return fmt.Errorf("create the FTP state directory: %w", err)
	}
	if err := os.Chmod(p.paths.State(), 0o700); err != nil {
		return fmt.Errorf("secure the FTP state directory: %w", err)
	}
	if err := os.MkdirAll(p.paths.DropInDir(), 0o755); err != nil {
		return fmt.Errorf("create the FTP configuration directory: %w", err)
	}
	if err := os.MkdirAll(p.paths.ScoreboardDir(), 0o755); err != nil {
		return fmt.Errorf("create the FTP run directory: %w", err)
	}
	// The panel names the log paths, so the panel makes the directory. proftpd
	// will not create it, and it refuses to start when SystemLog names a
	// directory that is not there — which would take the daemon down on a
	// change that was otherwise correct.
	if err := os.MkdirAll(p.paths.LogDirOrDefault(), 0o755); err != nil {
		return fmt.Errorf("create the FTP log directory: %w", err)
	}
	return nil
}

// firstLine returns the first non-empty line of the first non-empty input.
func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// wrap adds what was being attempted to an error from the command runner.
func wrap(what string, err error) error {
	if errors.Is(err, command.ErrNotAllowed) {
		return fmt.Errorf("%w (%s)", ErrUnavailable, what)
	}
	return fmt.Errorf("%s: %w", what, err)
}
