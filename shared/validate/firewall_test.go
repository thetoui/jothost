package validate

import (
	"errors"
	"strings"
	"testing"
)

func TestFirewallPortAcceptsPortsAndRanges(t *testing.T) {
	for _, port := range []string{"", "1", "22", "65535", "7080:7090"} {
		if err := FirewallPort(port); err != nil {
			t.Errorf("FirewallPort(%q) = %v, want nil", port, err)
		}
	}
}

func TestFirewallPortRefusesWhatIsNotAPort(t *testing.T) {
	cases := []struct{ port, why string }{
		{"0", "port zero is what a blank form parses to"},
		{"65536", "past the top of the range"},
		{"-1", "not a number"},
		{"22/tcp", "the protocol belongs in its own field"},
		{"22 ", "a space would split the argument"},
		{"1:0", "a range that ends before it starts matches nothing"},
		{"80:80", "a range of one is a port, and ufw treats it as empty"},
		{"22;rm -rf /", "a shell attempt"},
		{"abc", "not a number at all"},
	}
	for _, tc := range cases {
		if err := FirewallPort(tc.port); err == nil {
			t.Errorf("FirewallPort(%q) was accepted: %s", tc.port, tc.why)
		}
	}
}

func TestFirewallSourceAcceptsAddressesAndBlocks(t *testing.T) {
	for _, source := range []string{"", "any", "10.0.0.1", "10.0.0.0/8", "::1", "2001:db8::/32"} {
		if err := FirewallSource(source); err != nil {
			t.Errorf("FirewallSource(%q) = %v, want nil", source, err)
		}
	}
}

// Parsed rather than pattern-matched: a regular expression written in a hurry
// accepts 999.1.1.1 and /33.
func TestFirewallSourceRefusesWhatIsNotAnAddress(t *testing.T) {
	cases := []string{
		"999.1.1.1",
		"10.0.0.0/33",
		"10.0.0.0/-1",
		"10.0.0.1 10.0.0.2",
		"fe80::1%eth0",
		"example.com",
		"10.0.0.1;ufw disable",
	}
	for _, source := range cases {
		if err := FirewallSource(source); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("FirewallSource(%q) = %v, want ErrInvalidSource", source, err)
		}
	}
}

// A comment reaches ufw's rules file, which ufw parses back on every reload. A
// firewall that will not start is a host that is either wide open or
// unreachable, depending on which way it fails.
func TestFirewallCommentRefusesWhatWouldBreakTheRulesFile(t *testing.T) {
	if err := FirewallComment("panel: allow SSH from the office (2026-08)"); err != nil {
		t.Errorf("an ordinary comment was refused: %v", err)
	}

	for _, comment := range []string{
		"has \"quotes\"",
		"has 'quotes'",
		"two\nlines",
		"carriage\rreturn",
		"tab\there",
		"back\\slash",
		strings.Repeat("a", MaxFirewallComment+1),
	} {
		if err := FirewallComment(comment); err == nil {
			t.Errorf("comment %q was accepted", comment)
		}
	}
}

func TestFirewallActionDirectionAndProtocol(t *testing.T) {
	for _, action := range []string{FirewallAllow, FirewallDeny, FirewallReject, FirewallLimit} {
		if err := FirewallAction(action); err != nil {
			t.Errorf("action %q: %v", action, err)
		}
	}
	for _, action := range []string{"", "ALLOW", "drop", "allow;"} {
		if err := FirewallAction(action); err == nil {
			t.Errorf("action %q was accepted", action)
		}
	}

	for _, direction := range []string{FirewallIn, FirewallOut} {
		if err := FirewallDirection(direction); err != nil {
			t.Errorf("direction %q: %v", direction, err)
		}
	}
	if err := FirewallDirection("routed"); err == nil {
		t.Error("routed was accepted; the panel does not model routed rules")
	}

	for _, protocol := range []string{ProtocolTCP, ProtocolUDP, ProtocolAny} {
		if err := FirewallProtocol(protocol); err != nil {
			t.Errorf("protocol %q: %v", protocol, err)
		}
	}
	if err := FirewallProtocol("icmp"); err == nil {
		t.Error("icmp was accepted; it takes no port and would make a port rule meaningless")
	}
}

// The lockout guard is built on this: does this rule affect the port I am
// administering the host through.
func TestPortsOverlap(t *testing.T) {
	cases := []struct {
		spec string
		port int
		want bool
	}{
		{"22", 22, true},
		{"22", 80, false},
		{"20:25", 22, true},
		{"20:25", 26, false},
		{"", 22, true}, // no port means every port, which is the dangerous one
		{"", 65535, true},
		{"nonsense", 22, false},
	}
	for _, tc := range cases {
		if got := PortsOverlap(tc.spec, tc.port); got != tc.want {
			t.Errorf("PortsOverlap(%q, %d) = %v, want %v", tc.spec, tc.port, got, tc.want)
		}
	}
}
