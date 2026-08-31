// Package ssh reads and changes the host's SSH server configuration.
//
// # What makes this phase different from the others
//
// Every other thing the panel changes can be put back from the panel. This one
// cannot: a wrong answer here locks the operator out of the machine, and the
// way back in is the thing that just broke. So the rules are stricter than
// elsewhere, and two of them are worth stating before any code:
//
//  1. **Nothing is written that sshd has not already accepted.** Every change is
//     validated with `sshd -t` against the whole configuration — includes and
//     all — before it is installed, and the effective configuration is read back
//     afterwards to check it actually took. A change that reports success and
//     did nothing is the worst outcome available here, because the operator will
//     believe the host is configured a way it is not.
//
//  2. **A change that would demonstrably lock the operator out is refused.**
//     Turning off password authentication on a host with no usable key, or
//     moving the port somewhere the firewall does not allow, are not warnings.
//     They are refusals with the reason and the fix.
//
// # Why there is no timed rollback here, unlike the firewall
//
// Phase 16 applies a firewall change provisionally and undoes it unless it is
// confirmed, because a bad rule severs the connection immediately and leaves no
// way to say so. SSH is not like that. Existing sessions survive a reload, and a
// configuration sshd refuses to parse is caught before it is installed — so the
// failure mode the timer exists for does not arise: at every moment either the
// old configuration is live, or a new one sshd has already accepted is.
package ssh

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

// Allowlist keys for the tools this package drives.
const (
	CommandName    = "sshd"
	CommandKeygen  = "ssh-keygen"
	defaultPathSSH = "/etc/ssh"
)

// The file the panel owns.
//
// A drop-in rather than the distribution's sshd_config, because the panel
// should not be editing a file the package manager also edits — and because the
// first occurrence of a directive wins in sshd's parser, so a file included at
// the top of the main config is the only place a value can be set with
// certainty. It is numbered low so it sorts first among the drop-ins and
// therefore wins against them too.
const (
	dropInDir  = "sshd_config.d"
	dropInName = "10-jothost.conf"
)

// Errors returned by this package.
var (
	// ErrUnavailable means this host has no SSH server to configure.
	ErrUnavailable = errors.New("this host has no SSH server")
	// ErrNoDropIn means the host's sshd_config does not include the drop-in
	// directory, so a file written there would be silently ignored.
	ErrNoDropIn = errors.New("this host's sshd_config does not include a drop-in directory")
	// ErrWouldLockOut means the change would leave nobody able to log in.
	ErrWouldLockOut = errors.New("this change would lock everybody out of the host")
	// ErrInvalidConfig means sshd refused the configuration.
	ErrInvalidConfig = errors.New("the SSH server refused the configuration")
	// ErrNotApplied means the change was installed and did not take effect,
	// which means something else on this host is overriding it.
	ErrNotApplied = errors.New("the change was written but the server did not adopt it")
)

// Provider reads and changes the host's SSH configuration.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	// dir is /etc/ssh, or a temporary directory in tests.
	dir string
	// accounts lists the host's login accounts. An interface so a test does
	// not need real users.
	accounts AccountLister
}

// Options configure a Provider.
type Options struct {
	Runner   *command.Runner
	Log      *slog.Logger
	Dir      string
	Accounts AccountLister
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	dir := opts.Dir
	if dir == "" {
		dir = defaultPathSSH
	}

	accounts := opts.Accounts
	if accounts == nil {
		accounts = passwdAccounts{path: "/etc/passwd"}
	}

	return &Provider{runner: opts.Runner, log: log, dir: dir, accounts: accounts}
}

// Available reports whether this host has an SSH server to configure.
func (p *Provider) Available() bool {
	if p == nil || p.runner == nil {
		return false
	}
	return p.runner.Available(CommandName)
}

// ConfigPath is the distribution's main configuration file.
func (p *Provider) ConfigPath() string { return filepath.Join(p.dir, "sshd_config") }

// DropInPath is the file the panel writes.
func (p *Provider) DropInPath() string {
	return filepath.Join(p.dir, dropInDir, dropInName)
}

// Config is the SSH server's effective configuration.
//
// Effective, not "what is in the file": a directive that is commented out is
// still in force at its default, and the defaults differ between versions. The
// only answer worth showing an operator is the one the running server would
// give, which is what `sshd -T` prints.
type Config struct {
	// Available reports whether this host has an SSH server at all.
	Available bool `json:"available"`
	// Managed reports whether the panel can change the configuration here. A
	// host whose sshd_config has no Include directive can be read and not
	// written, which the panel says rather than writing a file nothing reads.
	Managed bool `json:"managed"`
	// Reason explains an unavailable or unmanageable host.
	Reason string `json:"reason,omitempty"`

	// Ports are every port the server listens on. Usually one.
	Ports []int `json:"ports"`
	// RootLogin is sshd's own vocabulary: "yes", "without-password",
	// "forced-commands-only" or "no". "without-password" is what sshd calls
	// "prohibit-password", and it is what it prints.
	RootLogin string `json:"root_login"`
	// PasswordAuthentication and the rest are as the server resolved them.
	PasswordAuthentication bool `json:"password_authentication"`
	PubkeyAuthentication   bool `json:"pubkey_authentication"`
	PermitEmptyPasswords   bool `json:"permit_empty_passwords"`
	X11Forwarding          bool `json:"x11_forwarding"`
	MaxAuthTries           int  `json:"max_auth_tries"`
	LoginGraceTime         int  `json:"login_grace_time"`

	// ConfigPath and DropInPath say which files these came from and which one
	// the panel writes, so an operator can go and look.
	ConfigPath string `json:"config_path"`
	DropInPath string `json:"drop_in_path"`

	// Running reports whether the server is up. A configuration is only
	// interesting if something is serving it.
	Running bool `json:"running"`
}

// Read returns the server's effective configuration.
func (p *Provider) Read(ctx context.Context) (Config, error) {
	config := Config{
		Ports:      []int{},
		ConfigPath: p.ConfigPath(),
		DropInPath: p.DropInPath(),
	}

	if !p.Available() {
		config.Reason = "the SSH server is not installed on this host"
		return config, nil
	}
	config.Available = true

	// -T prints the fully resolved configuration and exits. It reads the same
	// files the running server would, so an Include the panel does not know
	// about is still accounted for.
	result, err := p.runner.Run(ctx, CommandName, "-T", "-f", p.ConfigPath())
	if err != nil {
		return config, fmt.Errorf("read the SSH configuration: %w", err)
	}
	if !result.Succeeded() {
		// The usual cause is a host with no host keys yet, which is a real
		// state a fresh machine is in and not a fault of the panel's.
		config.Reason = firstLine(result.Stderr, result.Stdout)
		return config, nil
	}

	applyEffective(&config, result.Stdout)

	managed, reason := p.dropInSupported()
	config.Managed = managed
	if !managed {
		config.Reason = reason
	}
	return config, nil
}

// applyEffective fills a Config from `sshd -T` output.
//
// The keys are lowercase and the values are resolved; anything this package
// does not recognise is ignored rather than guessed at.
func applyEffective(config *Config, output string) {
	for _, line := range strings.Split(output, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		value = strings.TrimSpace(value)

		switch strings.ToLower(key) {
		case "port":
			if port, ok := parsePort(value); ok {
				config.Ports = append(config.Ports, port)
			}
		case "permitrootlogin":
			config.RootLogin = strings.ToLower(value)
		case "passwordauthentication":
			config.PasswordAuthentication = isYes(value)
		case "pubkeyauthentication":
			config.PubkeyAuthentication = isYes(value)
		case "permitemptypasswords":
			config.PermitEmptyPasswords = isYes(value)
		case "x11forwarding":
			config.X11Forwarding = isYes(value)
		case "maxauthtries":
			config.MaxAuthTries = atoi(value)
		case "logingracetime":
			config.LoginGraceTime = atoi(value)
		}
	}
}

// dropInSupported reports whether a file written to the drop-in directory would
// actually be read.
//
// This is checked rather than assumed because the failure it prevents is the
// one that matters most in this package: a panel that writes a file nothing
// includes reports success, changes nothing, and leaves an operator believing
// password authentication is off when it is on.
func (p *Provider) dropInSupported() (bool, string) {
	content, err := os.ReadFile(p.ConfigPath()) //nolint:gosec // a path this package owns
	if err != nil {
		return false, "the SSH configuration could not be read: " + err.Error()
	}

	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(strings.ToLower(trimmed), "include ") {
			continue
		}
		// The glob has to cover the file the panel writes. A host that includes
		// some other directory is a host the panel does not write to.
		pattern := strings.TrimSpace(trimmed[len("include "):])
		if matchesDropIn(pattern, p.DropInPath()) {
			return true, ""
		}
	}

	return false, "this host's sshd_config has no Include for " +
		filepath.Join(p.dir, dropInDir) + ", so the panel will not change it: " +
		"a file written there would be ignored, and reporting a change that did " +
		"nothing would be worse than refusing"
}

// matchesDropIn reports whether an Include pattern would pick up our file.
func matchesDropIn(pattern, dropIn string) bool {
	for _, candidate := range strings.Fields(pattern) {
		if !strings.HasPrefix(candidate, "/") {
			// A relative pattern is resolved against /etc/ssh.
			candidate = filepath.Join(defaultPathSSH, candidate)
		}
		if ok, err := filepath.Match(candidate, dropIn); err == nil && ok {
			return true
		}
		// A test's directory is not the pattern's directory, so the file name
		// is compared as well: "*.conf" covers "10-jothost.conf" wherever the
		// two live.
		if ok, err := filepath.Match(filepath.Base(candidate), filepath.Base(dropIn)); err == nil && ok {
			if filepath.Base(filepath.Dir(candidate)) == dropInDir {
				return true
			}
		}
	}
	return false
}

func firstLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return "the SSH server did not say why"
}

func isYes(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "yes") }

func parsePort(value string) (int, bool) {
	port := atoi(value)
	if port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func atoi(value string) int {
	total := 0
	for _, r := range strings.TrimSpace(value) {
		if r < '0' || r > '9' {
			return 0
		}
		total = total*10 + int(r-'0')
	}
	return total
}
