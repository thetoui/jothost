package firewall

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// The lockout guard.
//
// This is the cheapest of the six protections in CLAUDE.md section 19 and the
// one that catches the most: most firewall accidents are not subtle. They are
// "deny 22", "default deny incoming with nothing allowing SSH", and "deny
// everything from anywhere" — each of which is obvious before it is applied,
// and catastrophic afterwards.
//
// The rollback in change.go is what catches the rest. This is what means an
// operator is usually told "no" instead of watching their connection drop and
// come back sixty seconds later.

// DefaultGuardedPorts are the ports a change may never close.
//
// 22 because it is how the host is administered when the panel cannot be
// reached, and 80 and 443 because they are how the panel itself is reached —
// and a panel that has firewalled itself off is one that cannot be used to
// undo the rule that did it.
//
// A host with SSH somewhere else adds it through AGENT_FIREWALL_GUARDED_PORTS;
// Phase 17 will read it from sshd's own configuration.
func DefaultGuardedPorts() []int {
	return []int{22, 80, 443}
}

// GuardedPortsFromEnv reads the configured guard list.
//
// A malformed entry is skipped rather than failing startup: the consequence of
// refusing to start is an Agent that cannot manage anything, where the
// consequence of skipping is a port that has to be protected by the operator's
// own care. Neither is good, and the first is worse.
func GuardedPortsFromEnv(value string) []int {
	ports := DefaultGuardedPorts()
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		port, err := strconv.Atoi(field)
		if err != nil || port < 1 || port > 65535 {
			continue
		}
		if !containsPort(ports, port) {
			ports = append(ports, port)
		}
	}
	return ports
}

// Change is what a caller wants done, in a form the guard can reason about.
type Change struct {
	// Kind is what sort of change this is.
	Kind string `json:"kind"`
	// Rule is the rule being added or removed, for the rule kinds.
	Rule Rule `json:"rule,omitempty"`
	// Policy is the new default, for a policy change.
	Policy    string `json:"policy,omitempty"`
	Direction string `json:"direction,omitempty"`
}

// The kinds of change the panel can make.
const (
	ChangeAddRule    = "rule.add"
	ChangeDeleteRule = "rule.delete"
	ChangeEnable     = "enable"
	ChangeDisable    = "disable"
	ChangeDefault    = "default"
)

// Guard refuses a change that would take away the way this host is reached.
//
// It is deliberately conservative: it refuses rules that *might* close a
// guarded port, not only ones that certainly do. A rule denying a guarded port
// from one address is probably fine and is still refused, because the panel
// cannot tell whether that address is the one the operator is sitting at — and
// the cost of being wrong in the other direction is a host nobody can reach.
//
// The escape hatch is deliberate too: there is none here. An operator who
// genuinely needs to close SSH does it at the machine, where being wrong is
// recoverable.
func (p *Provider) Guard(change Change, current Status) error {
	switch change.Kind {
	case ChangeAddRule:
		return p.guardRule(change.Rule)

	case ChangeDefault:
		if change.Direction == validate.FirewallIn &&
			change.Policy != validate.FirewallAllow {
			// Denying by default is the right way to run a host — but only
			// once something allows the ports it is administered through.
			if missing := p.unprotected(current.Rules); len(missing) > 0 {
				return fmt.Errorf("%w: denying incoming traffic by default would close %s, "+
					"which nothing allows; add a rule for %s first",
					ErrWouldLockOut, portList(missing), portList(missing))
			}
		}
		return nil

	case ChangeEnable:
		// Enabling applies whatever ufw's default policy already is, which is
		// deny incoming on a stock installation.
		if current.DefaultIncoming != validate.FirewallAllow {
			if missing := p.unprotected(current.Rules); len(missing) > 0 {
				return fmt.Errorf("%w: the firewall denies incoming traffic by default "+
					"and nothing allows %s; add a rule for %s before switching it on",
					ErrWouldLockOut, portList(missing), portList(missing))
			}
		}
		return nil

	case ChangeDeleteRule:
		// Removing the rule that keeps SSH open is a lockout with an extra
		// step. It is caught by asking what would be left.
		remaining := make([]Rule, 0, len(current.Rules))
		for _, rule := range current.Rules {
			if rule.ID() != change.Rule.ID() {
				remaining = append(remaining, rule)
			}
		}
		if current.Enabled && current.DefaultIncoming != validate.FirewallAllow {
			if missing := p.unprotected(remaining); len(missing) > 0 {
				return fmt.Errorf("%w: removing this rule would leave nothing allowing %s",
					ErrWouldLockOut, portList(missing))
			}
		}
		return nil

	case ChangeDisable:
		// Switching the firewall off opens the host rather than closing it. It
		// is a security decision, not a lockout, and the panel records it
		// rather than refusing it.
		return nil

	default:
		return fmt.Errorf("%w: unknown change %q", validate.ErrInvalidFirewallRule, change.Kind)
	}
}

// guardRule refuses a rule that would close a guarded port.
func (p *Provider) guardRule(rule Rule) error {
	if rule.Action == validate.FirewallAllow {
		// Allowing never closes anything.
		return nil
	}
	if rule.Direction != validate.FirewallIn {
		// An outgoing rule cannot stop an administrator connecting in.
		return nil
	}

	for _, port := range p.guarded {
		if !validate.PortsOverlap(rule.Port, port) {
			continue
		}
		if rule.Protocol == validate.ProtocolUDP {
			// SSH and HTTP are TCP; a UDP rule on the same number does not
			// touch them.
			continue
		}
		return fmt.Errorf("%w: this rule would %s port %d, which is how this host is "+
			"administered", ErrWouldLockOut, rule.Action, port)
	}
	return nil
}

// unprotected returns the guarded ports no rule allows.
func (p *Provider) unprotected(rules []Rule) []int {
	missing := make([]int, 0, len(p.guarded))

	for _, port := range p.guarded {
		allowed := false
		for _, rule := range rules {
			if rule.Action != validate.FirewallAllow && rule.Action != validate.FirewallLimit {
				continue
			}
			if rule.Direction != validate.FirewallIn {
				continue
			}
			if rule.Protocol == validate.ProtocolUDP {
				continue
			}
			if validate.PortsOverlap(rule.Port, port) {
				allowed = true
				break
			}
		}
		if !allowed {
			missing = append(missing, port)
		}
	}
	return missing
}

// portList renders ports for a message an operator reads.
func portList(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return "port " + parts[0]
	default:
		return "ports " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
	}
}

func containsPort(ports []int, port int) bool {
	for _, candidate := range ports {
		if candidate == port {
			return true
		}
	}
	return false
}

// ensureStateDir creates the directory backups and the pending marker live in.
//
// 0700: a rule backup describes exactly which ports are open and to whom,
// which is a map of the host's attack surface.
func (p *Provider) ensureStateDir() error {
	if err := os.MkdirAll(p.stateDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", p.stateDir, err)
	}
	return nil
}
