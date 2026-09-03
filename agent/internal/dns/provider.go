package dns

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
)

// Provider manages the host's name server.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	paths  Paths
	// service starts and reports on the daemon. It is an interface rather than
	// a direct dependency on the service manager so this package does not
	// reach into the one that owns daemon lifecycles.
	service Servicer
}

// Servicer reloads the name server and reports whether it is running.
type Servicer interface {
	// Start brings the daemon up. Unlike the FTP provider's Reloader this is
	// needed as well as a reload: a host that has just been given its first
	// zone usually has a name server that has never run.
	Start(ctx context.Context) error
	// Running reports whether the daemon is up.
	Running(ctx context.Context) bool
}

// Options configure a Provider.
type Options struct {
	Runner  *command.Runner
	Log     *slog.Logger
	Paths   Paths
	Service Servicer
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Provider{
		runner:  opts.Runner,
		log:     log,
		paths:   opts.Paths.withDefaults(),
		service: opts.Service,
	}
}

// SetService supplies the thing that starts the daemon.
//
// Set after construction for the reason the FTP provider's reloader is: the
// starter reaches back through the operation registry to the service manager,
// and the registry is built from this provider, so one of the two is wired
// second.
func (p *Provider) SetService(s Servicer) { p.service = s }

// Paths reports where this Provider keeps its files.
func (p *Provider) Paths() Paths { return p.paths }

// Available reports whether this host has a name server the panel can manage.
//
// Both the daemon and its checkers are required. A host with named but no
// named-checkzone would let the panel write zone files it cannot validate, and
// an unvalidated zone file is a name server that fails to start — taking every
// other zone on the host with it.
func (p *Provider) Available() bool {
	if p == nil || p.runner == nil {
		return false
	}
	return p.runner.Available(CommandNamed) &&
		p.runner.Available(CommandNamedCheckconf) &&
		p.runner.Available(CommandNamedCheckzone)
}

// Running reports whether the daemon is up.
func (p *Provider) Running(ctx context.Context) bool {
	if !p.Available() || p.service == nil {
		return false
	}
	return p.service.Running(ctx)
}

// Version reports which BIND this is.
func (p *Provider) Version(ctx context.Context) string {
	if !p.Available() {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandNamed, "-v")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "BIND 9.18.49 (Extended Support Version) <id:cd4a53b>" — the version is
	// the second field, and the rest is a build identifier nobody asked for.
	fields := strings.Fields(firstLine(result.Stdout, result.Stderr))
	if len(fields) >= 2 {
		return fields[1]
	}
	return ""
}

// SupportsDNSSEC reports whether this BIND can sign zones on its own.
//
// dnssec-policy arrived in BIND 9.16. Older servers can serve signed zones but
// cannot generate keys or roll them over themselves, and this phase does not
// offer to do that from outside — see the package comment for why.
func (p *Provider) SupportsDNSSEC(ctx context.Context) bool {
	version := p.Version(ctx)
	major, minor, ok := parseVersion(version)
	if !ok {
		return false
	}
	return major > 9 || (major == 9 && minor >= 16)
}

// parseVersion pulls the major and minor out of a BIND version string.
func parseVersion(version string) (int, int, bool) {
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// Validate asks named whether the whole configuration is one it will start
// with.
//
// The whole configuration, from named.conf down through every include, so a
// zone the panel added that collides with one somebody else defined is caught
// here rather than at the next restart — by which time the server is down and
// every zone on it is unanswered.
func (p *Provider) Validate(ctx context.Context) error {
	result, err := p.runner.Run(ctx, CommandNamedCheckconf, p.paths.MainConf())
	if err != nil {
		return fmt.Errorf("check the DNS configuration: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// ValidateZone asks named-checkzone whether a zone file is loadable.
//
// It is given the candidate file, before it is installed. Unlike proftpd, whose
// validator only reads the configuration in place, named-checkzone checks any
// file against any origin — so a zone that would stop the server can be caught
// while it is still a temporary file nothing reads.
func (p *Provider) ValidateZone(ctx context.Context, zone, path string) error {
	result, err := p.runner.Run(ctx, CommandNamedCheckzone, zone, path)
	if err != nil {
		return fmt.Errorf("check the zone %s: %w", zone, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s: %s", ErrInvalidZone, zone,
			firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// ensureDirs creates the directories this package owns, with the ownership
// named needs.
//
// The state directory belongs to the account named runs as, and it has to:
// inline signing makes named itself write the signed zone, its journal and its
// keys beside the file the panel wrote. A directory root owns is a directory
// named cannot sign into, and the failure is a zone that is configured for
// DNSSEC, reports no error, and is served unsigned.
func (p *Provider) ensureDirs() error {
	uid, gid, ok := namedAccount()

	for _, dir := range []string{p.paths.StateDir, p.paths.KeyDir()} {
		if err := os.MkdirAll(dir, 0o770); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if !ok {
			continue
		}
		if err := os.Chown(dir, uid, gid); err != nil {
			return fmt.Errorf("give %s to the name server's account: %w", dir, err)
		}
	}
	return nil
}

// namedAccounts are the names BIND runs as, in the order they are tried.
// Distributions disagree: Alpine and RHEL use "named", Debian uses "bind".
var namedAccounts = []string{"named", "bind"}

// namedAccount resolves the account BIND runs as.
func namedAccount() (int, int, bool) {
	for _, name := range namedAccounts {
		account, err := user.Lookup(name)
		if err != nil {
			continue
		}
		uid, err := strconv.Atoi(account.Uid)
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(account.Gid)
		if err != nil {
			continue
		}
		return uid, gid, true
	}
	return 0, 0, false
}

// chownNamed gives one file to the name server's account.
func chownNamed(path string) error {
	uid, gid, ok := namedAccount()
	if !ok {
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("give %s to the name server's account: %w", path, err)
	}
	return nil
}

// ServedZones lists the zones the running server is answering for.
//
// Read from the panel's include file rather than from the server, because
// `rndc zonestatus` answers about one zone at a time and there is no rndc
// command that lists them. This is the file the server was told to read, which
// is the closest thing to its own view that BIND offers.
func (p *Provider) ServedZones() []string {
	data, err := os.ReadFile(p.paths.Include())
	if err != nil {
		return nil
	}
	zones := make([]string, 0, 8)
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(trimmed, "zone \"")
		if !ok {
			continue
		}
		name, _, ok := strings.Cut(rest, "\"")
		if ok && name != "" {
			zones = append(zones, name)
		}
	}
	return zones
}

// firstLine returns the first non-empty line of the candidates given.
func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// zoneArtifacts are the files named keeps beside a zone file it signs.
//
// They are listed so that removing a zone removes them too. A leftover journal
// is not harmless: if the zone is ever recreated, named replays the old journal
// over the new file and serves records the panel does not have.
func zoneArtifacts(path string) []string {
	return []string{
		path,
		path + ".jnl",
		path + ".jbk",
		path + ".signed",
		path + ".signed.jnl",
	}
}

// removeZoneFiles deletes a zone's files.
func removeZoneFiles(path string) error {
	for _, artifact := range zoneArtifacts(path) {
		if err := os.Remove(artifact); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(artifact), err)
		}
	}
	return nil
}
