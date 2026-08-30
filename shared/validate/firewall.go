package validate

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// Errors returned by firewall validation.
var (
	ErrInvalidFirewallRule = errors.New("invalid firewall rule")
	// ErrInvalidPortSpec is distinct from ErrInvalidPort, which covers the
	// single number a Node application listens on. A firewall port is a
	// specification: it may be empty, and it may be a range.
	ErrInvalidPortSpec = errors.New("invalid port specification")
	ErrInvalidSource   = errors.New("invalid source address")
)

// What a rule may say.
//
// The set is closed and small on purpose. ufw understands more — routed rules,
// application profiles, interface matching — and every one of them is another
// shape the panel would have to render, validate, and be sure it had not made
// a hole in. What is here covers what a hosting panel actually needs.
const (
	FirewallAllow  = "allow"
	FirewallDeny   = "deny"
	FirewallReject = "reject"
	// FirewallLimit is deny-after-repeated-attempts: six connections from one
	// address in thirty seconds. It is what SSH should be behind, and it is
	// the reason this action is worth carrying.
	FirewallLimit = "limit"

	FirewallIn  = "in"
	FirewallOut = "out"

	ProtocolTCP = "tcp"
	ProtocolUDP = "udp"
	// ProtocolAny leaves the protocol off the rule, which ufw reads as both.
	ProtocolAny = "any"

	// SourceAny is the wildcard ufw spells "any".
	SourceAny = "any"
)

// FirewallAction checks what a rule does.
func FirewallAction(action string) error {
	switch action {
	case FirewallAllow, FirewallDeny, FirewallReject, FirewallLimit:
		return nil
	default:
		return fmt.Errorf("%w: action must be allow, deny, reject, or limit, not %q",
			ErrInvalidFirewallRule, action)
	}
}

// FirewallDirection checks which way a rule applies.
func FirewallDirection(direction string) error {
	switch direction {
	case FirewallIn, FirewallOut:
		return nil
	default:
		return fmt.Errorf("%w: direction must be in or out, not %q",
			ErrInvalidFirewallRule, direction)
	}
}

// FirewallProtocol checks a rule's protocol.
func FirewallProtocol(protocol string) error {
	switch protocol {
	case ProtocolTCP, ProtocolUDP, ProtocolAny:
		return nil
	default:
		return fmt.Errorf("%w: protocol must be tcp, udp, or any, not %q",
			ErrInvalidFirewallRule, protocol)
	}
}

// portPattern is a single port or an inclusive range.
//
// ufw writes a range with a colon; the shape is checked here and the bounds
// below, because "1:0" parses fine and means nothing.
var portPattern = regexp.MustCompile(`^([0-9]{1,5})(:([0-9]{1,5}))?$`)

// FirewallPort checks a port or port range.
//
// An empty port is allowed and means "every port", which is what a rule
// governing a whole address is. Port 0 is not a port; it is what a caller
// sends when a form was left blank and a number was parsed out of nothing.
func FirewallPort(port string) error {
	if port == "" {
		return nil
	}

	groups := portPattern.FindStringSubmatch(port)
	if groups == nil {
		return fmt.Errorf("%w: %q is not a port or a range like 7080:7090",
			ErrInvalidPortSpec, port)
	}

	low, err := strconv.Atoi(groups[1])
	if err != nil || low < 1 || low > 65535 {
		return fmt.Errorf("%w: %s is outside 1-65535", ErrInvalidPortSpec, groups[1])
	}
	if groups[3] == "" {
		return nil
	}

	high, err := strconv.Atoi(groups[3])
	if err != nil || high < 1 || high > 65535 {
		return fmt.Errorf("%w: %s is outside 1-65535", ErrInvalidPortSpec, groups[3])
	}
	if high <= low {
		// ufw accepts a reversed range and matches nothing, which is a rule
		// that looks present and does nothing — the worst kind.
		return fmt.Errorf("%w: the range %s ends before it starts", ErrInvalidPortSpec, port)
	}
	return nil
}

// FirewallSource checks where a rule applies from.
//
// "any", a single address, or a CIDR block. Parsed rather than pattern-matched:
// netip rejects 999.1.1.1 and 10.0.0.0/33, which a regular expression written
// in a hurry does not.
func FirewallSource(source string) error {
	if source == "" || source == SourceAny {
		return nil
	}

	if strings.Contains(source, "/") {
		prefix, err := netip.ParsePrefix(source)
		if err != nil {
			return fmt.Errorf("%w: %q is not a CIDR block", ErrInvalidSource, source)
		}
		if prefix.Addr().Zone() != "" {
			// A zone ("fe80::1%eth0") names an interface, which is state this
			// panel does not model and ufw would take literally.
			return fmt.Errorf("%w: an address zone is not supported", ErrInvalidSource)
		}
		return nil
	}

	address, err := netip.ParseAddr(source)
	if err != nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrInvalidSource, source)
	}
	if address.Zone() != "" {
		return fmt.Errorf("%w: an address zone is not supported", ErrInvalidSource)
	}
	return nil
}

// MaxFirewallComment bounds a rule's note.
const MaxFirewallComment = 120

// commentPattern is what a rule comment may contain.
//
// The comment reaches ufw as an argument and is written into its rules file,
// which ufw parses back on every reload. A newline or a quote in there is a
// rules file that does not parse — and a firewall that will not start is a
// host that is either wide open or unreachable, depending on which way it
// fails.
var commentPattern = regexp.MustCompile(`^[a-zA-Z0-9 ._:/@()+-]*$`)

// FirewallComment checks a rule's note.
func FirewallComment(comment string) error {
	if len(comment) > MaxFirewallComment {
		return fmt.Errorf("%w: a comment may be at most %d characters",
			ErrInvalidFirewallRule, MaxFirewallComment)
	}
	if !commentPattern.MatchString(comment) {
		return fmt.Errorf("%w: a comment may not contain quotes, newlines, or control characters",
			ErrInvalidFirewallRule)
	}
	return nil
}

// PortsOverlap reports whether a port specification covers a given port.
//
// This is what the lockout guard is built on: "does this rule affect the port
// I am administering this host through". A rule with no port covers every one
// of them, which is exactly the rule most likely to lock somebody out.
func PortsOverlap(spec string, port int) bool {
	if spec == "" {
		return true
	}

	groups := portPattern.FindStringSubmatch(spec)
	if groups == nil {
		return false
	}

	low, err := strconv.Atoi(groups[1])
	if err != nil {
		return false
	}
	high := low
	if groups[3] != "" {
		if parsed, err := strconv.Atoi(groups[3]); err == nil {
			high = parsed
		}
	}
	return port >= low && port <= high
}
