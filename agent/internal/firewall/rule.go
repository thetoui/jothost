package firewall

import (
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Rule is one firewall rule, in the panel's own shape.
//
// Structured rather than a ufw command line. ufw's syntax is a small language —
// "allow from 10.0.0.0/8 to any port 22 proto tcp comment 'office'" — and a
// panel that let a caller supply that string would be letting them write rules
// the panel cannot read back, cannot render, and cannot check for lockouts.
// Every field here is validated, and the command line is built from them.
type Rule struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	// Port is a single port, an inclusive range ("7080:7090"), or empty for
	// every port.
	Port string `json:"port"`
	// Source is "any", an address, or a CIDR block.
	Source  string `json:"source"`
	Comment string `json:"comment,omitempty"`
	// V6 marks a rule ufw reported as the IPv6 half of a pair. The panel folds
	// the two together for display; this says which is which when it cannot.
	V6 bool `json:"v6,omitempty"`
}

// Validate checks a rule before it becomes a command.
func (r Rule) Validate() error {
	if err := validate.FirewallAction(r.Action); err != nil {
		return err
	}
	if err := validate.FirewallDirection(r.Direction); err != nil {
		return err
	}
	if err := validate.FirewallProtocol(r.Protocol); err != nil {
		return err
	}
	if err := validate.FirewallPort(r.Port); err != nil {
		return err
	}
	if err := validate.FirewallSource(r.Source); err != nil {
		return err
	}
	if err := validate.FirewallComment(r.Comment); err != nil {
		return err
	}

	// ufw has no way to express "this protocol, every port": the protocol is
	// written as part of the port term. A rule that says tcp with no port is
	// one the panel would send and ufw would refuse, so it is refused here
	// where the message can say why.
	if r.Port == "" && r.Protocol != validate.ProtocolAny {
		return fmt.Errorf("%w: a rule for every port cannot name a protocol; "+
			"give it a port, or set the protocol to any", validate.ErrInvalidFirewallRule)
	}
	return nil
}

// ID is a stable identifier for a rule, derived from what it does.
//
// Rules are addressed by this rather than by ufw's position numbers. Those
// renumber the moment anything is inserted or deleted, so "delete rule 3" is a
// request that means something different by the time it arrives — and what it
// means is "delete whichever rule is third now".
func (r Rule) ID() string {
	source := r.Source
	if source == "" {
		source = validate.SourceAny
	}
	port := r.Port
	if port == "" {
		port = "any"
	}
	return strings.Join([]string{r.Direction, r.Action, r.Protocol, port, source}, "|")
}

// Args builds the ufw command line for a rule.
//
// Every token is a separate argv entry: nothing here is a string that a shell
// or ufw could re-split, and the values were validated before they arrived.
//
// The two forms are ufw's own. The short one ("allow 22/tcp") is what ufw
// writes for a rule from anywhere; the long one is needed as soon as a source
// is involved, and ufw will not accept the short form with one.
func (r Rule) Args(verb string) []string {
	args := make([]string, 0, 12)
	if verb != "" {
		args = append(args, verb)
	}
	args = append(args, r.Action)

	// "in" is ufw's default and it rejects the word on the short form, so it is
	// written only when it is "out".
	if r.Direction == validate.FirewallOut {
		args = append(args, "out")
	}

	simple := (r.Source == "" || r.Source == validate.SourceAny) && r.Port != ""
	if simple {
		port := r.Port
		if r.Protocol != validate.ProtocolAny {
			port += "/" + r.Protocol
		}
		args = append(args, port)
	} else {
		args = append(args, "from")
		if r.Source == "" {
			args = append(args, validate.SourceAny)
		} else {
			args = append(args, r.Source)
		}
		if r.Port != "" {
			args = append(args, "to", "any", "port", r.Port)
			if r.Protocol != validate.ProtocolAny {
				args = append(args, "proto", r.Protocol)
			}
		}
	}

	if r.Comment != "" {
		args = append(args, "comment", r.Comment)
	}
	return args
}

// statusLine matches one row of "ufw status numbered".
//
// The format is columnar and has been stable for a decade, but it is a human
// display rather than an interface — so the parser is deliberately forgiving
// about spacing and strict about what it will believe.
//
// A real row:
//
//	[ 1] 22/tcp                     ALLOW IN    Anywhere                   # office
//	[ 2] 7080:7090/tcp              ALLOW IN    10.0.0.0/8
//	[ 3] 8080/tcp (v6)              ALLOW IN    Anywhere (v6)
func parseStatusLine(line string) (Rule, bool) {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "[") {
		return Rule{}, false
	}

	// Drop the index: it is exactly the thing that is not stable.
	closing := strings.Index(line, "]")
	if closing < 0 {
		return Rule{}, false
	}
	line = strings.TrimSpace(line[closing+1:])

	rule := Rule{Protocol: validate.ProtocolAny, Source: validate.SourceAny}

	// The comment is last and may contain spaces, so it comes off first.
	if hash := strings.Index(line, " # "); hash >= 0 {
		rule.Comment = strings.TrimSpace(line[hash+3:])
		line = strings.TrimSpace(line[:hash])
	}

	if strings.Contains(line, "(v6)") {
		rule.V6 = true
		line = strings.ReplaceAll(line, "(v6)", " ")
	}

	fields := strings.Fields(line)
	if len(fields) < 3 {
		return Rule{}, false
	}

	// The target is first: "22/tcp", "7080:7090/tcp", "Anywhere".
	target := fields[0]
	if port, protocol, found := strings.Cut(target, "/"); found {
		rule.Port = port
		rule.Protocol = protocol
	} else if !strings.EqualFold(target, "Anywhere") {
		rule.Port = target
	}

	// Then the action and direction: "ALLOW IN", "DENY OUT", "LIMIT IN".
	rule.Action = strings.ToLower(fields[1])
	rule.Direction = strings.ToLower(fields[2])
	if rule.Direction != validate.FirewallIn && rule.Direction != validate.FirewallOut {
		// "ALLOW FWD" and anything else ufw grows later: reported rather than
		// guessed at, because a rule the panel renders wrongly is worse than
		// one it admits it does not understand.
		return Rule{}, false
	}

	// Whatever is left is the source.
	if len(fields) > 3 {
		source := strings.Join(fields[3:], " ")
		source = strings.TrimSpace(source)
		if !strings.EqualFold(source, "Anywhere") && source != "" {
			rule.Source = source
		}
	}

	// A row the panel cannot express is a row it must not claim to manage: the
	// delete it would build from this would not match what is there.
	if err := rule.Validate(); err != nil {
		return Rule{}, false
	}
	return rule, true
}

// parseAddedLine reads one line of "ufw show added".
//
// A disabled ufw lists nothing under "status": it reports "Status: inactive"
// and stops, because it is describing what is being enforced and nothing is.
// The rules are still there — staged, in user.rules — and "show added" is what
// prints them.
//
// That matters more than a listing being empty. The guard that decides whether
// the firewall may be switched on asks which rules would apply; against an
// empty list it concludes nothing allows SSH and refuses. Without this parser
// the firewall could never be enabled from the panel at all.
//
// The format is the command line ufw would take, which is the same one Args
// builds:
//
//	ufw allow 22/tcp
//	ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
//	ufw deny out 25/tcp
//	ufw allow 443/tcp comment 'panel'
//	ufw deny from 203.0.113.7
func parseAddedLine(line string) (Rule, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 3 || fields[0] != "ufw" {
		return Rule{}, false
	}

	rule := Rule{
		Direction: validate.FirewallIn,
		Protocol:  validate.ProtocolAny,
		Source:    validate.SourceAny,
	}

	fields = fields[1:]
	rule.Action = fields[0]
	fields = fields[1:]

	if len(fields) > 0 && fields[0] == "out" {
		rule.Direction = validate.FirewallOut
		fields = fields[1:]
	}

	// The comment is quoted and last, and may contain spaces.
	if index := indexOfField(fields, "comment"); index >= 0 {
		rule.Comment = strings.Trim(strings.Join(fields[index+1:], " "), "'\"")
		fields = fields[:index]
	}

	if len(fields) == 0 {
		return Rule{}, false
	}

	if fields[0] == "from" {
		if len(fields) < 2 {
			return Rule{}, false
		}
		rule.Source = fields[1]
		fields = fields[2:]

		// "to any port 8080 proto tcp", with the proto term optional.
		if len(fields) >= 3 && fields[0] == "to" && fields[2] == "port" {
			if len(fields) < 4 {
				return Rule{}, false
			}
			rule.Port = fields[3]
			if len(fields) >= 6 && fields[4] == "proto" {
				rule.Protocol = fields[5]
			}
		}
	} else {
		port, protocol, found := strings.Cut(fields[0], "/")
		rule.Port = port
		if found {
			rule.Protocol = protocol
		}
	}

	if err := rule.Validate(); err != nil {
		return Rule{}, false
	}
	return rule, true
}

// indexOfField returns where a word appears in a field list, or -1.
func indexOfField(fields []string, want string) int {
	for i, field := range fields {
		if field == want {
			return i
		}
	}
	return -1
}
