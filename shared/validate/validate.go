// Package validate holds input rules the API and the Agent must agree on.
//
// A domain name or a system user validated one way in the API and another way
// in the Agent is a gap: the API would accept a value the Agent then refuses,
// or worse, the Agent would accept one the API believed it had already
// rejected. Both import this package so there is a single definition.
package validate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Errors returned by validation.
var (
	ErrInvalidDomain     = errors.New("invalid domain name")
	ErrInvalidSystemUser = errors.New("invalid system user name")
	ErrInvalidPath       = errors.New("invalid path")
)

// Domain limits from RFC 1035.
const (
	MaxDomainLength = 253
	MaxLabelLength  = 63
)

// domainPattern accepts a lowercase name with at least two labels.
//
// A single label ("localhost") is refused: a hosting panel's vhosts are
// addressed by a real name, and accepting one would produce a server_name that
// silently never matches a request from the internet.
var domainPattern = regexp.MustCompile(
	`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// systemUserPattern matches the portable useradd rules: start with a letter or
// underscore, then lowercase alphanumerics, underscore, or dash.
var systemUserPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// NormalizeDomain lowercases and trims a domain, and drops a trailing dot.
//
// "Example.COM." and "example.com" are the same host; storing both would let
// two websites claim one name.
func NormalizeDomain(domain string) string {
	normalized := strings.ToLower(strings.TrimSpace(domain))
	return strings.TrimSuffix(normalized, ".")
}

// Domain checks a domain name.
func Domain(domain string) error {
	if domain == "" {
		return fmt.Errorf("%w: domain is required", ErrInvalidDomain)
	}
	if len(domain) > MaxDomainLength {
		return fmt.Errorf("%w: must be at most %d characters", ErrInvalidDomain, MaxDomainLength)
	}

	for _, label := range strings.Split(domain, ".") {
		if label == "" {
			return fmt.Errorf("%w: empty label", ErrInvalidDomain)
		}
		if len(label) > MaxLabelLength {
			return fmt.Errorf("%w: label exceeds %d characters", ErrInvalidDomain, MaxLabelLength)
		}
	}

	if !domainPattern.MatchString(domain) {
		return fmt.Errorf("%w: %q", ErrInvalidDomain, domain)
	}

	// A name ending in a numeric label is an IP address written as a domain.
	// nginx would accept it as a server_name that can never be resolved to.
	labels := strings.Split(domain, ".")
	last := labels[len(labels)-1]
	if isNumeric(last) {
		return fmt.Errorf("%w: the last label must not be numeric", ErrInvalidDomain)
	}

	return nil
}

// SystemUser checks a site's system account name.
//
// The rules are deliberately tighter than the domain rules: this value becomes
// a Unix account and reaches useradd as an argument, so anything that could be
// read as an option or a path is refused.
func SystemUser(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidSystemUser)
	}
	if !systemUserPattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidSystemUser, name)
	}

	// Refusing the accounts that already run the system prevents a website
	// being created that owns files as root or as the web server itself.
	if _, reserved := reservedUsers[name]; reserved {
		return fmt.Errorf("%w: %q is reserved", ErrInvalidSystemUser, name)
	}
	return nil
}

// reservedUsers are accounts a website must never be given.
var reservedUsers = map[string]struct{}{
	"root": {}, "daemon": {}, "bin": {}, "sys": {}, "sync": {}, "games": {},
	"man": {}, "lp": {}, "mail": {}, "news": {}, "uucp": {}, "proxy": {},
	"www-data": {}, "backup": {}, "list": {}, "irc": {}, "nobody": {},
	"systemd-network": {}, "systemd-resolve": {}, "messagebus": {},
	"sshd": {}, "postgres": {}, "redis": {}, "nginx": {}, "mysql": {},
	"jothost": {}, "adm": {}, "shutdown": {}, "halt": {}, "operator": {},
}

// SystemUserFor derives a system account name from a domain.
//
// The name must be unique per site and fit useradd's 32-character limit, so a
// long domain is truncated and given a short hash suffix rather than colliding
// with another site that shares its prefix.
func SystemUserFor(domain string, hash string) string {
	// Dots and dashes are not valid at the start of an account name and read
	// poorly in one; underscores keep it recognisable.
	base := strings.NewReplacer(".", "_", "-", "_").Replace(NormalizeDomain(domain))
	base = strings.TrimLeft(base, "0123456789_")

	if base == "" {
		base = "site"
	}

	// "web_" prefix keeps site accounts recognisable in /etc/passwd and
	// guarantees a valid leading character.
	name := "web_" + base
	suffix := "_" + hash

	limit := 32 - len(suffix)
	if len(name) > limit {
		name = name[:limit]
	}
	return name + suffix
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
