// Package firewall manages the host's packet filter through ufw.
//
// CLAUDE.md section 19 is the requirement this package exists to satisfy, and
// it is the strictest in the project: every change backs up the current rules,
// validates the new ones, applies them temporarily, verifies connectivity,
// commits, and rolls back automatically on failure.
//
// The reason for that ceremony is specific. Every other mistake this panel can
// make is recoverable by using the panel: a broken vhost is fixed by writing
// another one, a stopped service is started again. A firewall mistake takes
// away the connection the fix would arrive over. There is no second attempt
// from here — so a change that cannot be shown to have kept the host reachable
// is undone by the Agent itself, without being asked.
package firewall

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandName is the allowlist key for ufw.
const CommandName = "ufw"

// Errors returned by the provider.
var (
	// ErrUnavailable means ufw is not installed, or is installed and cannot
	// reach the kernel's packet filter — which on a container means the Agent
	// is running without NET_ADMIN.
	ErrUnavailable = errors.New("no firewall is available on this host")
	// ErrRefused means ufw rejected the rule.
	ErrRefused = errors.New("the firewall rejected the rule")
	// ErrWouldLockOut means the change would take away the way this host is
	// administered.
	ErrWouldLockOut = errors.New("that change would lock this host out")
	// ErrNoSuchRule means the rule to delete is not there.
	ErrNoSuchRule = errors.New("no such firewall rule")
)

// Provider reads and writes the host's firewall.
type Provider struct {
	runner *command.Runner
	// stateDir holds rule backups and the pending-change marker.
	stateDir string
	// configDir is where ufw keeps its rules. Configurable so a test can point
	// it at a temporary tree instead of /etc/ufw.
	configDir string
	// guarded are the ports that must stay reachable. See guard.go.
	guarded []int
	// state is the provisional change and its rollback timer. See change.go.
	state *changeState
}

// Options configures a Provider.
type Options struct {
	Runner *command.Runner
	// StateDir defaults to /var/lib/jothost/firewall.
	StateDir string
	// GuardedPorts are the ports a change may never close. Empty means the
	// defaults in guard.go, which are the ones a host is administered through.
	GuardedPorts []int
	// ConfigDir defaults to /etc/ufw.
	ConfigDir string
	Log       *slog.Logger
}

// DefaultStateDir is where backups and the pending change live.
const DefaultStateDir = "/var/lib/jothost/firewall"

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = DefaultStateDir
	}
	guarded := opts.GuardedPorts
	if len(guarded) == 0 {
		guarded = DefaultGuardedPorts()
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	return &Provider{
		runner:    opts.Runner,
		stateDir:  stateDir,
		configDir: opts.ConfigDir,
		guarded:   guarded,
		state:     &changeState{log: log},
	}
}

// Available reports whether ufw is installed.
//
// Installed is not the same as usable: ufw needs to reach the kernel's packet
// filter, which a container without NET_ADMIN cannot. Status() is what finds
// that out, because finding out means running it.
func (p *Provider) Available() bool {
	return p != nil && p.runner != nil && p.runner.Available(CommandName)
}

// GuardedPorts returns the ports a change may never close.
func (p *Provider) GuardedPorts() []int {
	out := make([]int, len(p.guarded))
	copy(out, p.guarded)
	return out
}

// Status is the firewall as it currently stands.
type Status struct {
	// Available reports whether ufw is installed and can reach the packet
	// filter.
	Available bool `json:"available"`
	// Enabled reports whether it is switched on. A host with rules and a
	// disabled firewall is filtering nothing, which is worth saying plainly.
	Enabled bool `json:"enabled"`
	// DefaultIncoming and DefaultOutgoing are ufw's policies: "deny", "allow",
	// or "reject".
	DefaultIncoming string `json:"default_incoming"`
	DefaultOutgoing string `json:"default_outgoing"`
	// Rules are what the host is enforcing, with the IPv6 halves folded into
	// their IPv4 twins.
	Rules []Rule `json:"rules"`
	// Reason explains an unavailable firewall, for an operator who needs to
	// know whether to install something or to change how the Agent runs.
	Reason string `json:"reason,omitempty"`
}

// Status reads the firewall's current state.
func (p *Provider) Status(ctx context.Context) (Status, error) {
	if !p.Available() {
		return Status{Reason: "ufw is not installed"}, nil
	}

	result, err := p.runner.Run(ctx, CommandName, "status", "verbose")
	if err != nil {
		return Status{}, err
	}
	if !result.Succeeded() {
		// ufw exits non-zero when it cannot reach the packet filter, which is
		// a host the panel cannot manage rather than an error in the request.
		return Status{Reason: firstLine(result.Stderr)}, nil
	}

	status := Status{Available: true}
	for _, line := range strings.Split(result.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "Status:"):
			// Compared, not searched for: "inactive" contains "active", so a
			// substring test reports a disabled firewall as enabled — and then
			// every guard downstream reasons about a ruleset that is not being
			// enforced.
			state := strings.TrimSpace(strings.TrimPrefix(trimmed, "Status:"))
			status.Enabled = strings.EqualFold(state, "active")
		case strings.HasPrefix(trimmed, "Default:"):
			status.DefaultIncoming, status.DefaultOutgoing = parseDefaults(trimmed)
		}
	}

	rules, err := p.Rules(ctx, status.Enabled)
	if err != nil {
		return Status{}, err
	}
	status.Rules = rules
	return status, nil
}

// parseDefaults reads ufw's policy line.
//
// "Default: deny (incoming), allow (outgoing), deny (routed)" — the routed
// policy is not carried, because the panel does not write routed rules and
// reporting a policy nothing here can change would be an invitation to try.
func parseDefaults(line string) (incoming, outgoing string) {
	for _, part := range strings.Split(strings.TrimPrefix(line, "Default:"), ",") {
		part = strings.TrimSpace(part)
		policy, scope, found := strings.Cut(part, " ")
		if !found {
			continue
		}
		switch strings.Trim(scope, "()") {
		case "incoming":
			incoming = policy
		case "outgoing":
			outgoing = policy
		}
	}
	return incoming, outgoing
}

// Rules lists the firewall's rules.
//
// Which question to ask depends on whether the firewall is switched on, and the
// difference is not cosmetic. "ufw status" describes what is being *enforced*,
// so a disabled firewall answers with one line and no rules — while the rules
// themselves are still there, staged, waiting to take effect. Reading only that
// would mean the panel showed nothing to an operator who had just written five
// rules, and worse: the guard would conclude that nothing allows SSH and refuse
// to let the firewall be switched on at all.
//
// So an enabled firewall is read from "status numbered", which is the truth
// about what is enforced, and a disabled one from "show added", which is the
// truth about what is staged.
//
// The IPv6 half of each pair is dropped either way: ufw writes both when a rule
// names no address family, and showing an operator two rows for one rule they
// wrote once is a list that does not match what they did.
func (p *Provider) Rules(ctx context.Context, enabled bool) ([]Rule, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}

	args := []string{"status", "numbered"}
	parse := parseStatusLine
	if !enabled {
		args = []string{"show", "added"}
		parse = parseAddedLine
	}

	result, err := p.runner.Run(ctx, CommandName, args...)
	if err != nil {
		return nil, err
	}
	if !result.Succeeded() {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, firstLine(result.Stderr))
	}

	seen := make(map[string]struct{})
	rules := make([]Rule, 0, 16)
	for _, line := range strings.Split(result.Stdout, "\n") {
		rule, ok := parse(line)
		if !ok {
			continue
		}
		if rule.V6 {
			continue
		}
		if _, duplicate := seen[rule.ID()]; duplicate {
			continue
		}
		seen[rule.ID()] = struct{}{}
		rules = append(rules, rule)
	}
	return rules, nil
}

// addRule runs ufw for one rule. It is unexported because nothing should add a
// rule without the change protocol in change.go around it.
func (p *Provider) addRule(ctx context.Context, rule Rule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	return p.run(ctx, rule.Args("")...)
}

// deleteRule removes a rule by its specification rather than its number.
//
// ufw renumbers on every change, so a number taken from a listing means
// something different by the time a second request arrives. Deleting by
// specification is idempotent and cannot remove the wrong rule.
func (p *Provider) deleteRule(ctx context.Context, rule Rule) error {
	if err := rule.Validate(); err != nil {
		return err
	}
	return p.run(ctx, rule.Args("delete")...)
}

// SetDefault changes an incoming or outgoing policy.
func (p *Provider) setDefault(ctx context.Context, direction, policy string) error {
	switch policy {
	case validate.FirewallAllow, validate.FirewallDeny, validate.FirewallReject:
	default:
		return fmt.Errorf("%w: a default policy must be allow, deny, or reject",
			validate.ErrInvalidFirewallRule)
	}
	if err := validate.FirewallDirection(direction); err != nil {
		return err
	}

	incoming := "incoming"
	if direction == validate.FirewallOut {
		incoming = "outgoing"
	}
	return p.run(ctx, "default", policy, incoming)
}

// enable switches the firewall on.
func (p *Provider) enable(ctx context.Context) error {
	// --force skips ufw's own "this may disrupt existing ssh connections"
	// prompt, which has nobody to answer it here. The question it asks is the
	// one this package answers with the guard and the rollback.
	return p.run(ctx, "--force", "enable")
}

// disable switches the firewall off.
func (p *Provider) disable(ctx context.Context) error {
	return p.run(ctx, "--force", "disable")
}

// run executes ufw and turns a refusal into an error worth showing.
func (p *Provider) run(ctx context.Context, args ...string) error {
	if !p.Available() {
		return ErrUnavailable
	}

	result, err := p.runner.Run(ctx, CommandName, args...)
	if err != nil {
		return err
	}
	if !result.Succeeded() {
		detail := firstLine(result.Stderr)
		if detail == "" {
			detail = firstLine(result.Stdout)
		}
		return fmt.Errorf("%w: %s", ErrRefused, detail)
	}

	// ufw reports some refusals on stdout with a zero exit, which is a success
	// that changed nothing — the worst possible answer to give a caller.
	if strings.Contains(result.Stdout, "ERROR:") {
		return fmt.Errorf("%w: %s", ErrRefused, firstErrorLine(result.Stdout))
	}
	return nil
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

// firstErrorLine finds the line ufw put its complaint on.
func firstErrorLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "ERROR:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "ERROR:"))
		}
	}
	return firstLine(text)
}
