package firewall

import (
	"errors"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

func guardProvider() *Provider {
	return NewProvider(Options{GuardedPorts: []int{22, 80, 443}})
}

func allowRule(port string) Rule {
	return Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: port, Source: "any"}
}

// The accidents this catches are not subtle. They are the ones an operator
// makes at the end of a long day, and they are catastrophic because they take
// away the connection the fix would arrive over.
func TestGuardRefusesClosingThePortsTheHostIsAdministeredThrough(t *testing.T) {
	provider := guardProvider()
	current := Status{Enabled: true, DefaultIncoming: "deny", Rules: []Rule{allowRule("22")}}

	refused := []struct {
		name string
		rule Rule
	}{
		{"denying SSH outright", Rule{Action: "deny", Direction: "in", Protocol: "tcp",
			Port: "22", Source: "any"}},
		{"rejecting SSH", Rule{Action: "reject", Direction: "in", Protocol: "tcp",
			Port: "22", Source: "any"}},
		{"denying SSH from one address", Rule{Action: "deny", Direction: "in",
			Protocol: "tcp", Port: "22", Source: "203.0.113.7"}},
		{"a range that covers SSH", Rule{Action: "deny", Direction: "in",
			Protocol: "tcp", Port: "20:25", Source: "any"}},
		{"denying everything", Rule{Action: "deny", Direction: "in",
			Protocol: "any", Port: "", Source: "any"}},
		{"denying the panel's port", Rule{Action: "deny", Direction: "in",
			Protocol: "tcp", Port: "443", Source: "any"}},
	}

	for _, tc := range refused {
		err := provider.Guard(Change{Kind: ChangeAddRule, Rule: tc.rule}, current)
		if !errors.Is(err, ErrWouldLockOut) {
			t.Errorf("%s = %v, want ErrWouldLockOut", tc.name, err)
		}
	}
}

func TestGuardAllowsWhatCannotLockAnyoneOut(t *testing.T) {
	provider := guardProvider()
	current := Status{Enabled: true, DefaultIncoming: "deny", Rules: []Rule{allowRule("22")}}

	allowed := []struct {
		name string
		rule Rule
	}{
		{"allowing a port", allowRule("3306")},
		{"allowing SSH again from an office", Rule{Action: "allow", Direction: "in",
			Protocol: "tcp", Port: "22", Source: "10.0.0.0/8"}},
		{"denying a port nothing is administered through", Rule{Action: "deny",
			Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"}},
		// An outgoing rule cannot stop an administrator connecting in.
		{"denying outbound mail", Rule{Action: "deny", Direction: "out",
			Protocol: "tcp", Port: "25", Source: "any"}},
		// SSH and HTTP are TCP; a UDP rule on 22 does not touch them.
		{"denying UDP on a guarded number", Rule{Action: "deny", Direction: "in",
			Protocol: "udp", Port: "22", Source: "any"}},
	}

	for _, tc := range allowed {
		if err := provider.Guard(Change{Kind: ChangeAddRule, Rule: tc.rule}, current); err != nil {
			t.Errorf("%s was refused: %v", tc.name, err)
		}
	}
}

// Switching the firewall on applies whatever the default policy already is.
// On a stock ufw that is deny incoming, which closes SSH the moment it starts
// unless something allows it.
func TestGuardRefusesEnablingWithNothingAllowingSSH(t *testing.T) {
	provider := guardProvider()

	bare := Status{Enabled: false, DefaultIncoming: "deny"}
	err := provider.Guard(Change{Kind: ChangeEnable}, bare)
	if !errors.Is(err, ErrWouldLockOut) {
		t.Fatalf("enabling a default-deny firewall with no rules = %v, want ErrWouldLockOut", err)
	}
	// The message has to name what to do about it.
	if err != nil && !contains(err.Error(), "22") {
		t.Errorf("the refusal does not say which port is unprotected: %v", err)
	}

	ready := Status{
		Enabled:         false,
		DefaultIncoming: "deny",
		Rules:           []Rule{allowRule("22"), allowRule("80"), allowRule("443")},
	}
	if err := provider.Guard(Change{Kind: ChangeEnable}, ready); err != nil {
		t.Errorf("enabling with the guarded ports allowed was refused: %v", err)
	}
}

// A lockout with an extra step: remove the rule that keeps SSH open, and the
// default policy closes it.
func TestGuardRefusesRemovingTheRuleThatKeepsSSHOpen(t *testing.T) {
	provider := guardProvider()
	ssh := allowRule("22")
	current := Status{
		Enabled:         true,
		DefaultIncoming: "deny",
		Rules:           []Rule{ssh, allowRule("80"), allowRule("443")},
	}

	err := provider.Guard(Change{Kind: ChangeDeleteRule, Rule: ssh}, current)
	if !errors.Is(err, ErrWouldLockOut) {
		t.Fatalf("removing the only SSH rule = %v, want ErrWouldLockOut", err)
	}

	// With a second rule covering it, removing one is fine.
	office := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22",
		Source: "10.0.0.0/8"}
	current.Rules = append(current.Rules, office)
	if err := provider.Guard(Change{Kind: ChangeDeleteRule, Rule: ssh}, current); err != nil {
		t.Errorf("removing a redundant SSH rule was refused: %v", err)
	}
}

// Denying by default is how a host should run — but only once something allows
// the ports it is administered through.
func TestGuardRefusesADefaultDenyThatWouldCloseEverything(t *testing.T) {
	provider := guardProvider()

	err := provider.Guard(Change{
		Kind: ChangeDefault, Direction: validate.FirewallIn, Policy: "deny",
	}, Status{Enabled: true, DefaultIncoming: "allow"})
	if !errors.Is(err, ErrWouldLockOut) {
		t.Fatalf("default deny with no rules = %v, want ErrWouldLockOut", err)
	}

	protected := Status{
		Enabled:         true,
		DefaultIncoming: "allow",
		Rules:           []Rule{allowRule("22"), allowRule("80"), allowRule("443")},
	}
	if err := provider.Guard(Change{
		Kind: ChangeDefault, Direction: validate.FirewallIn, Policy: "deny",
	}, protected); err != nil {
		t.Errorf("default deny with the guarded ports allowed was refused: %v", err)
	}
}

// Switching the firewall off opens the host rather than closing it. It is a
// security decision, and the panel records it rather than refusing it.
func TestGuardAllowsDisabling(t *testing.T) {
	provider := guardProvider()
	if err := provider.Guard(Change{Kind: ChangeDisable},
		Status{Enabled: true, DefaultIncoming: "deny"}); err != nil {
		t.Errorf("disabling was refused: %v", err)
	}
}

// A host whose SSH is not on 22 says so, and the guard protects what it is told.
func TestGuardedPortsFromEnvAddsToTheDefaults(t *testing.T) {
	ports := GuardedPortsFromEnv("2222, 8443")

	for _, want := range []int{22, 80, 443, 2222, 8443} {
		if !containsPort(ports, want) {
			t.Errorf("port %d is not guarded: %v", want, ports)
		}
	}

	// A malformed entry is skipped rather than failing startup: an Agent that
	// will not start manages nothing at all.
	ports = GuardedPortsFromEnv("2222,,not-a-port,70000,-1")
	if !containsPort(ports, 2222) {
		t.Errorf("a valid port beside an invalid one was lost: %v", ports)
	}
	for _, unwanted := range []int{70000, -1, 0} {
		if containsPort(ports, unwanted) {
			t.Errorf("%d was accepted as a port: %v", unwanted, ports)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
