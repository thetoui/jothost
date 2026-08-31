package operations

import (
	"context"
	"testing"
)

// The firewall interlock, in the one part of it that is arithmetic.
//
// Whether a port change is refused is decided by agent/internal/ssh and tested
// there against a stub. What is tested here is the half that reads ufw's own
// output: a rule's port field is a number, a comma-separated list, a range, or
// empty — and reading "7080:7090" as a single port would refuse a change that is
// perfectly safe, while reading an empty field as "no ports" would refuse every
// change on a host with an allow-all rule.
func TestPortCoveredReadsUFWsPortField(t *testing.T) {
	cases := []struct {
		field string
		port  int
		want  bool
		why   string
	}{
		{"22", 22, true, "the port itself"},
		{"22", 2222, false, "a different port"},
		{"", 2222, true, "a rule with no port covers every port"},
		{"22,80,443", 443, true, "one of a list"},
		{"22,80,443", 8080, false, "not in the list"},
		{"7080:7090", 7085, true, "inside a range"},
		{"7080:7090", 7080, true, "the start of a range"},
		{"7080:7090", 7090, true, "the end of a range"},
		{"7080:7090", 7091, false, "just past a range"},
		{"22,7080:7090", 7085, true, "a range inside a list"},
		{"not-a-port", 22, false, "a field that is not a port"},
		{"22:", 22, false, "a range with no end"},
	}

	for _, tc := range cases {
		t.Run(tc.why, func(t *testing.T) {
			if got := portCovered(tc.field, tc.port); got != tc.want {
				t.Fatalf("portCovered(%q, %d) = %v, want %v (%s)",
					tc.field, tc.port, got, tc.want, tc.why)
			}
		})
	}
}

// A host with nothing filtering admits every port. Refusing an SSH port change
// because no firewall could be found would refuse a change that is safe, and
// would do it on every host that does not run ufw.
func TestAHostWithNoFirewallAdmitsEveryPort(t *testing.T) {
	guard := firewallGuard{registry: &Registry{}}

	reachable, reason := guard.PortReachable(context.Background(), 2222)
	if !reachable {
		t.Fatalf("a host with no firewall refused a port change: %s", reason)
	}
}
