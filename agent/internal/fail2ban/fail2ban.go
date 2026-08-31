// Package fail2ban manages the host's intrusion prevention.
//
// # What it does, and what it does not
//
// fail2ban watches log files, matches failures with a filter, and when a host
// exceeds a threshold it runs an action — normally a firewall rule. The panel's
// job here is the *policy*: which jails are on, how many failures are allowed,
// how long a ban lasts, and who is never banned. The filters, the actions and
// the log formats belong to the distribution, which knows what its own daemons
// write; the panel does not offer a box to type a regular expression into,
// because a filter that matches the wrong line bans the wrong person.
//
// # The two things this package learned the hard way
//
// **fail2ban reads its drop-ins last-wins, and `.local` beats `.conf`.** Alpine
// ships `jail.d/alpine-ssh.conf` setting `maxretry = 10`. A panel file called
// `10-jothost.conf` is read *first* and loses; one called `99-jothost.conf` is
// still a `.conf` and loses too. The file that wins is
// `jail.d/99-jothost.local` — `.local` after every `.conf`, and the numeric
// prefix last among the `.local` files. Getting this wrong does not fail: the
// panel writes a correct file, reports success, and the daemon goes on using
// the distribution's numbers.
//
// **So every change is read back from the daemon.** After a reload the panel
// asks `fail2ban-client get <jail> maxretry` and compares. A change that did not
// take is reported as one, because the alternative is a panel that says a host
// bans after three attempts when it bans after ten.
//
// # Why bans are not ufw rules
//
// fail2ban ships a ufw action, and using it would put every ban in the panel's
// firewall list. It is not used. A ban is transient — minutes to hours, dozens a
// day on an exposed host — and the firewall page is the operator's own rules,
// with a backup taken before each change (Phase 16). Filling it with churn would
// make both features worse. fail2ban's own iptables chains sit in front of ufw's
// and do the same job without touching it.
package fail2ban

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

// Allowlist key for the tool this package drives.
const CommandName = "fail2ban-client"

// Where things live.
const (
	// DefaultConfigDir is fail2ban's configuration root on every distribution
	// that ships it.
	DefaultConfigDir = "/etc/fail2ban"
	// dropInName is the file the panel owns.
	//
	// ".local" and "99-" are both load-bearing: see the package comment.
	dropInDir  = "jail.d"
	dropInName = "99-jothost.local"
)

// Errors returned by this package.
var (
	// ErrUnavailable means fail2ban is not installed on this host.
	ErrUnavailable = errors.New("fail2ban is not installed on this host")
	// ErrNotRunning means the daemon is not up, so nothing can be asked of it.
	ErrNotRunning = errors.New("fail2ban is not running")
	// ErrUnknownJail means the jail is not one the panel offers.
	ErrUnknownJail = errors.New("that is not a jail this panel manages")
	// ErrInvalidConfig means fail2ban refused the configuration.
	ErrInvalidConfig = errors.New("fail2ban refused the configuration")
	// ErrNotApplied means the change was written and the daemon did not adopt
	// it, which means something else on this host sets it too.
	ErrNotApplied = errors.New("the change was written but fail2ban did not adopt it")
	// ErrNotBanned means the address is not banned in that jail.
	ErrNotBanned = errors.New("that address is not banned")
)

// Provider manages the host's intrusion prevention.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	// dir is /etc/fail2ban, or a temporary directory in tests.
	dir string
	// logs reports which log files exist, so a jail is only offered where the
	// thing it watches is there to watch.
	logs LogFinder
}

// LogFinder reports whether a path exists and is readable.
//
// An interface so a test can describe a host without creating its files, and so
// this package does not grow its own opinion about what a log is.
type LogFinder interface {
	LogExists(path string) bool
}

// realLogs answers from the filesystem.
type realLogs struct{}

func (realLogs) LogExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// Options configure a Provider.
type Options struct {
	Runner *command.Runner
	Log    *slog.Logger
	Dir    string
	Logs   LogFinder
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	dir := opts.Dir
	if dir == "" {
		dir = DefaultConfigDir
	}
	logs := opts.Logs
	if logs == nil {
		logs = realLogs{}
	}
	return &Provider{runner: opts.Runner, log: log, dir: dir, logs: logs}
}

// Available reports whether this host has fail2ban.
func (p *Provider) Available() bool {
	if p == nil || p.runner == nil {
		return false
	}
	return p.runner.Available(CommandName)
}

// DropInPath is the file the panel writes.
func (p *Provider) DropInPath() string {
	return filepath.Join(p.dir, dropInDir, dropInName)
}

// Running reports whether the daemon is answering.
//
// Asked of the daemon rather than of the process table: a fail2ban whose socket
// is stale looks alive to `ps` and cannot be told anything.
func (p *Provider) Running(ctx context.Context) bool {
	if !p.Available() {
		return false
	}
	result, err := p.runner.Run(ctx, CommandName, "ping")
	if err != nil {
		return false
	}
	return result.Succeeded() && strings.Contains(result.Stdout, "pong")
}

// Version reports which fail2ban this is.
func (p *Provider) Version(ctx context.Context) string {
	if !p.Available() {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandName, "--version")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "Fail2Ban v1.1.0" — the version is the last field of the first line.
	line := firstLine(result.Stdout, result.Stderr)
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return strings.TrimPrefix(fields[len(fields)-1], "v")
}

// Status is everything the panel shows about this host's intrusion prevention.
type Status struct {
	// Available reports whether fail2ban is installed.
	Available bool `json:"available"`
	// Running reports whether the daemon is answering.
	Running bool `json:"running"`
	// CanInstall reports whether the panel could install it.
	CanInstall bool   `json:"can_install"`
	Version    string `json:"version,omitempty"`
	// Reason explains an unavailable host.
	Reason string `json:"reason,omitempty"`
	// Jails are what this host has, whether the panel configured them or not:
	// a jail somebody added by hand is still banning people, and a page that
	// hid it would be describing a different machine.
	Jails []Jail `json:"jails"`
	// Ignored are the addresses that are never banned.
	Ignored []string `json:"ignored"`
	// DropInPath is where the panel writes, shown so an operator can go and
	// look.
	DropInPath string `json:"drop_in_path"`
	// Banned is the total across every jail, so the page has a headline that
	// does not require adding up.
	Banned int `json:"banned"`
}

// Jail is one of fail2ban's jails as this host has it.
type Jail struct {
	// Name is fail2ban's own, and what the panel acts on.
	Name string `json:"name"`
	// Label and Summary come from the panel's catalogue where it knows the
	// jail, and are empty for one somebody else configured.
	Label   string `json:"label,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Managed reports whether this is a jail the panel offers. A jail the
	// operator wrote themselves is listed and left alone.
	Managed bool `json:"managed"`
	// Enabled reports whether fail2ban is running it.
	Enabled bool `json:"enabled"`

	// The policy, as the daemon resolved it — not as the panel asked for it.
	MaxRetry  int `json:"max_retry"`
	FindTime  int `json:"find_time"`
	BanTime   int `json:"ban_time"`
	Currently int `json:"currently_banned"`
	Total     int `json:"total_banned"`
	// Failed is how many failures are being counted right now.
	Failed int `json:"currently_failed"`
	// TotalFailed is how many the filter has ever matched.
	TotalFailed int `json:"total_failed"`
	// LogPaths are the files it watches.
	LogPaths []string `json:"log_paths"`
	// Banned are the addresses currently banned by this jail.
	Banned []string `json:"banned"`
	// Available reports whether this host could run the jail at all — for a
	// catalogued jail, whether the log it watches exists.
	Available bool `json:"available"`
	// Reason explains an unavailable jail.
	Reason string `json:"reason,omitempty"`
}

// firstLine returns the first non-empty line of the values given.
func firstLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// atoi parses a decimal number, returning zero for anything else.
func atoi(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	negative := false
	if strings.HasPrefix(trimmed, "-") {
		negative = true
		trimmed = trimmed[1:]
	}

	total := 0
	for _, r := range trimmed {
		if r < '0' || r > '9' {
			return 0
		}
		total = total*10 + int(r-'0')
	}
	if negative {
		return -total
	}
	return total
}

// wrap adds context to an error from the client.
func wrap(action string, err error) error {
	return fmt.Errorf("%s: %w", action, err)
}
