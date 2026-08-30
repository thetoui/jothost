package firewall

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The command line is built from validated fields, never taken from a caller.
// Each token is a separate argv entry, so nothing here can be re-split.
func TestArgsBuildUFWsOwnTwoForms(t *testing.T) {
	cases := []struct {
		name string
		rule Rule
		want []string
	}{
		{
			name: "a port from anywhere uses the short form",
			rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22", Source: "any"},
			want: []string{"allow", "22/tcp"},
		},
		{
			name: "any protocol leaves the protocol off",
			rule: Rule{Action: "allow", Direction: "in", Protocol: "any", Port: "22", Source: "any"},
			want: []string{"allow", "22"},
		},
		{
			name: "a range is a port like any other",
			rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "7080:7090", Source: "any"},
			want: []string{"allow", "7080:7090/tcp"},
		},
		{
			// ufw will not accept the short form once a source is involved.
			name: "a source needs the long form",
			rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22", Source: "10.0.0.0/8"},
			want: []string{"allow", "from", "10.0.0.0/8", "to", "any", "port", "22", "proto", "tcp"},
		},
		{
			name: "a whole address has no port term",
			rule: Rule{Action: "deny", Direction: "in", Protocol: "any", Port: "", Source: "203.0.113.7"},
			want: []string{"deny", "from", "203.0.113.7"},
		},
		{
			name: "outgoing says so",
			rule: Rule{Action: "deny", Direction: "out", Protocol: "tcp", Port: "25", Source: "any"},
			want: []string{"deny", "out", "25/tcp"},
		},
		{
			name: "a comment is one argument, not a quoted string",
			rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22", Source: "any",
				Comment: "office access"},
			want: []string{"allow", "22/tcp", "comment", "office access"},
		},
	}

	for _, tc := range cases {
		got := tc.rule.Args("")
		if strings.Join(got, "\x00") != strings.Join(tc.want, "\x00") {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

// Deleting is the same specification with a verb in front. Rules are never
// addressed by ufw's position numbers, which renumber on every change.
func TestArgsPrefixTheVerbForDeletion(t *testing.T) {
	rule := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22", Source: "any"}

	got := rule.Args("delete")
	if got[0] != "delete" {
		t.Fatalf("first argument = %q, want delete", got[0])
	}
	if strings.Join(got[1:], " ") != "allow 22/tcp" {
		t.Fatalf("the specification changed between add and delete: %q", got)
	}
}

// ufw writes the protocol as part of the port term, so there is no way to say
// "tcp, every port". A rule the panel would send and ufw would refuse is
// refused here, where the message can explain.
func TestValidateRefusesAProtocolWithoutAPort(t *testing.T) {
	rule := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "", Source: "any"}
	if err := rule.Validate(); err == nil {
		t.Fatal("a protocol with no port was accepted")
	}

	rule.Protocol = validate.ProtocolAny
	if err := rule.Validate(); err != nil {
		t.Fatalf("every port with any protocol should be valid: %v", err)
	}
}

// Real output, captured from ufw 0.36 in the development container.
const realStatus = `Status: active

     To                         Action      From
     --                         ------      ----
[ 1] 22/tcp                     ALLOW IN    Anywhere                   # office
[ 2] 8080/tcp                   ALLOW IN    Anywhere
[ 3] 7080:7090/tcp              ALLOW IN    10.0.0.0/8
[ 4] Anywhere                   DENY IN     203.0.113.7
[ 5] 25/tcp                     DENY OUT    Anywhere
[ 6] 22/tcp (v6)                ALLOW IN    Anywhere (v6)              # office
[ 7] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)
`

func TestParseStatusReadsWhatUFWPrints(t *testing.T) {
	var parsed []Rule
	for _, line := range strings.Split(realStatus, "\n") {
		if rule, ok := parseStatusLine(line); ok {
			parsed = append(parsed, rule)
		}
	}

	if len(parsed) != 7 {
		t.Fatalf("parsed %d rules, want 7: %+v", len(parsed), parsed)
	}

	first := parsed[0]
	if first.Port != "22" || first.Protocol != "tcp" || first.Action != "allow" ||
		first.Direction != "in" || first.Source != "any" {
		t.Errorf("first rule = %+v", first)
	}
	// The comment survives, spaces and all.
	if first.Comment != "office" {
		t.Errorf("comment = %q, want office", first.Comment)
	}

	if parsed[2].Port != "7080:7090" || parsed[2].Source != "10.0.0.0/8" {
		t.Errorf("the range rule was misread: %+v", parsed[2])
	}
	// "Anywhere" in the To column means every port, not a port named Anywhere.
	if parsed[3].Port != "" || parsed[3].Source != "203.0.113.7" ||
		parsed[3].Action != "deny" {
		t.Errorf("the address rule was misread: %+v", parsed[3])
	}
	if parsed[4].Direction != "out" {
		t.Errorf("an outgoing rule was read as incoming: %+v", parsed[4])
	}
	// The v6 halves are marked, so the listing can fold them away.
	if !parsed[5].V6 || !parsed[6].V6 {
		t.Errorf("the IPv6 rows were not recognised: %+v %+v", parsed[5], parsed[6])
	}
}

// A row the panel cannot express is a row it must not claim to manage: the
// delete it would build would not match what is there.
func TestParseStatusSkipsWhatItCannotExpress(t *testing.T) {
	skipped := []string{
		"     To                         Action      From",
		"Status: active",
		"",
		"[ 9] 22/tcp                     ALLOW FWD   Anywhere",
		"[10] Anywhere on eth0           ALLOW IN    Anywhere",
	}
	for _, line := range skipped {
		if rule, ok := parseStatusLine(line); ok {
			t.Errorf("line %q was parsed as %+v", line, rule)
		}
	}
}

// The identity of a rule is what it does, not where it sits in the list.
func TestRuleIDIgnoresPositionAndComment(t *testing.T) {
	first := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22",
		Source: "any", Comment: "office"}
	second := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22",
		Source: "any", Comment: "something else"}

	if first.ID() != second.ID() {
		t.Errorf("the same rule with a different note has a different id:\n%s\n%s",
			first.ID(), second.ID())
	}

	third := second
	third.Port = "23"
	if first.ID() == third.ID() {
		t.Error("two different rules share an id")
	}
}

// Real output, captured from ufw in the development container after adding one
// rule of each shape the panel can write.
const realAdded = `Added user rules (see 'ufw status' for running firewall):
ufw allow 22/tcp
ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
ufw allow 7080:7090/tcp
ufw deny out 25/tcp
ufw allow 443/tcp comment 'panel'
ufw deny from 203.0.113.7
`

// A disabled ufw reports no rules under "status", because it is describing what
// is being enforced. The rules are still staged, and the guard that decides
// whether the firewall may be switched on has to see them — without this the
// firewall could never be enabled from the panel at all.
func TestParseAddedReadsStagedRules(t *testing.T) {
	var parsed []Rule
	for _, line := range strings.Split(realAdded, "\n") {
		if rule, ok := parseAddedLine(line); ok {
			parsed = append(parsed, rule)
		}
	}

	if len(parsed) != 6 {
		t.Fatalf("parsed %d rules, want 6: %+v", len(parsed), parsed)
	}

	if parsed[0].Port != "22" || parsed[0].Protocol != "tcp" ||
		parsed[0].Action != "allow" || parsed[0].Direction != "in" {
		t.Errorf("the short form was misread: %+v", parsed[0])
	}
	if parsed[1].Source != "10.0.0.0/8" || parsed[1].Port != "8080" ||
		parsed[1].Protocol != "tcp" {
		t.Errorf("the long form was misread: %+v", parsed[1])
	}
	if parsed[2].Port != "7080:7090" {
		t.Errorf("a range was misread: %+v", parsed[2])
	}
	if parsed[3].Direction != "out" || parsed[3].Action != "deny" {
		t.Errorf("an outgoing rule was misread: %+v", parsed[3])
	}
	if parsed[4].Comment != "panel" {
		t.Errorf("the quoted comment was misread: %q", parsed[4].Comment)
	}
	if parsed[5].Source != "203.0.113.7" || parsed[5].Port != "" {
		t.Errorf("a whole-address rule was misread: %+v", parsed[5])
	}

	// The header is not a rule.
	if _, ok := parseAddedLine("Added user rules (see 'ufw status' for running firewall):"); ok {
		t.Error("the header was parsed as a rule")
	}
}

// What the panel writes and what it reads back have to agree, or a rule added
// through the panel could not be deleted through it.
func TestArgsAndParseAddedAgree(t *testing.T) {
	rules := []Rule{
		{Action: "allow", Direction: "in", Protocol: "tcp", Port: "22", Source: "any"},
		{Action: "allow", Direction: "in", Protocol: "tcp", Port: "8080", Source: "10.0.0.0/8"},
		{Action: "allow", Direction: "in", Protocol: "any", Port: "7080:7090", Source: "any"},
		{Action: "deny", Direction: "out", Protocol: "tcp", Port: "25", Source: "any"},
		{Action: "deny", Direction: "in", Protocol: "any", Port: "", Source: "203.0.113.7"},
	}

	for _, rule := range rules {
		line := "ufw " + strings.Join(rule.Args(""), " ")
		parsed, ok := parseAddedLine(line)
		if !ok {
			t.Errorf("the panel wrote %q and could not read it back", line)
			continue
		}
		if parsed.ID() != rule.ID() {
			t.Errorf("round trip changed the rule:\n wrote %s\n  read %s\n  line %q",
				rule.ID(), parsed.ID(), line)
		}
	}
}
