package validate

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Errors returned by intrusion-prevention validation.
var (
	ErrInvalidJail     = errors.New("invalid jail")
	ErrInvalidBanIP    = errors.New("invalid address")
	ErrInvalidIgnoreIP = errors.New("invalid ignored address")
	ErrInvalidDuration = errors.New("invalid duration")
	ErrInvalidRetries  = errors.New("invalid retry count")
)

// Bounds on the policy the panel writes.
//
// These are not fail2ban's limits; they are the range within which the setting
// still does what an operator thinks it does. A ban time of two seconds bans
// nobody, a maxretry of a thousand never triggers, and a findtime longer than a
// ban time is a window that has already closed.
const (
	MinRetries = 1
	MaxRetries = 100
	// MinBanSeconds is a minute: anything shorter is a rule that is removed
	// before the attacker's next packet arrives.
	MinBanSeconds = 60
	// MaxBanSeconds is a year. "Permanent" in fail2ban is a negative number,
	// which the panel does not write — a ban nobody can remember setting is a
	// support ticket eighteen months from now.
	MaxBanSeconds = 365 * 24 * 60 * 60
	// MinFindSeconds is ten seconds; below that a burst of legitimate retries
	// looks like an attack.
	MinFindSeconds = 10
	MaxFindSeconds = 7 * 24 * 60 * 60
	// MaxIgnoredAddresses bounds the ignore list, which becomes a config line.
	MaxIgnoredAddresses = 64
)

// JailName checks a jail identifier.
//
// A jail name becomes a section header in a configuration file and an argument
// to fail2ban-client, so it is restricted to what fail2ban itself uses: letters,
// digits, hyphens, underscores and dots.
func JailName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a jail is required", ErrInvalidJail)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: %q is too long", ErrInvalidJail, name)
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
		default:
			return fmt.Errorf("%w: %q is not a jail name", ErrInvalidJail, name)
		}
	}
	// A name that is only dots would be a path fragment rather than a jail.
	if strings.Trim(name, ".") == "" {
		return fmt.Errorf("%w: %q is not a jail name", ErrInvalidJail, name)
	}
	return nil
}

// BanIP checks an address before it becomes an argument to fail2ban-client.
//
// A single address or a CIDR block, and nothing else. fail2ban accepts host
// names here and resolves them; the panel does not pass one, because what gets
// banned would then depend on what DNS said at the moment of the call.
func BanIP(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("%w: an address is required", ErrInvalidBanIP)
	}

	if strings.Contains(trimmed, "/") {
		prefix, err := netip.ParsePrefix(trimmed)
		if err != nil {
			return fmt.Errorf("%w: %q is not an address or CIDR block", ErrInvalidBanIP, trimmed)
		}
		if prefix.Addr().Zone() != "" {
			return fmt.Errorf("%w: an interface zone is not an address", ErrInvalidBanIP)
		}
		return nil
	}

	address, err := netip.ParseAddr(trimmed)
	if err != nil {
		return fmt.Errorf("%w: %q is not an address", ErrInvalidBanIP, trimmed)
	}
	if address.Zone() != "" {
		return fmt.Errorf("%w: an interface zone is not an address", ErrInvalidBanIP)
	}
	return nil
}

// IgnoredAddresses checks the list of addresses that must never be banned, and
// returns it normalised.
//
// Loopback is added if it is missing. That is not tidying: fail2ban bans by
// inserting a firewall rule, and a host that has banned its own loopback has
// broken every local service that talks to another over it — while the panel
// that did it goes on reporting success.
func IgnoredAddresses(values []string) ([]string, error) {
	if len(values) > MaxIgnoredAddresses {
		return nil, fmt.Errorf("%w: at most %d addresses may be ignored",
			ErrInvalidIgnoreIP, MaxIgnoredAddresses)
	}

	seen := map[string]bool{}
	out := make([]string, 0, len(values)+2)

	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if err := BanIP(trimmed); err != nil {
			return nil, fmt.Errorf("%w: %s", ErrInvalidIgnoreIP, err.Error())
		}
		if seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}

	for _, required := range []string{"127.0.0.1/8", "::1"} {
		if !seen[required] {
			out = append(out, required)
		}
	}
	return out, nil
}

// BanSeconds checks how long a ban lasts.
func BanSeconds(seconds int) error {
	if seconds < MinBanSeconds || seconds > MaxBanSeconds {
		return fmt.Errorf("%w: a ban must last between %d seconds and a year",
			ErrInvalidDuration, MinBanSeconds)
	}
	return nil
}

// FindSeconds checks the window failures are counted in.
func FindSeconds(seconds int) error {
	if seconds < MinFindSeconds || seconds > MaxFindSeconds {
		return fmt.Errorf("%w: the window must be between %d seconds and a week",
			ErrInvalidDuration, MinFindSeconds)
	}
	return nil
}

// Retries checks how many failures are allowed before a ban.
func Retries(count int) error {
	if count < MinRetries || count > MaxRetries {
		return fmt.Errorf("%w: between %d and %d failures", ErrInvalidRetries,
			MinRetries, MaxRetries)
	}
	return nil
}
