package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by subdomain validation.
var (
	// ErrInvalidSubdomain covers a name that is not a valid subdomain of the
	// parent it is being created under.
	ErrInvalidSubdomain = errors.New("invalid subdomain")
	// ErrInvalidDocumentRootMode covers an unknown layout choice.
	ErrInvalidDocumentRootMode = errors.New("invalid document root mode")
	// ErrInvalidPHPPoolMode covers an unknown PHP pool strategy.
	ErrInvalidPHPPoolMode = errors.New("invalid PHP pool mode")
	// ErrInvalidSystemUserMode covers an unknown ownership strategy.
	ErrInvalidSystemUserMode = errors.New("invalid system user mode")
)

// WildcardPrefix is the only wildcard form this panel writes.
//
// nginx also accepts a trailing form ("www.*") and a leading dot (".example.com",
// meaning the name and everything under it). Neither is generated here: one
// wildcard shape is enough to serve a catch-all, and each extra shape is
// another thing that has to agree with the certificate, the DNS record, and
// the panel's idea of which site answers a request.
const WildcardPrefix = "*."

// IsWildcard reports whether a name is the wildcard form "*.example.com".
func IsWildcard(name string) bool {
	return strings.HasPrefix(name, WildcardPrefix)
}

// ServerName checks a name that will become an nginx server_name.
//
// It accepts an ordinary domain or the wildcard form. Everything else — a bare
// label, a name with a wildcard in the middle, anything carrying a character
// that could close a directive — is refused here, because this is the last
// point before the value is written into a configuration file a root process
// executes.
func ServerName(name string) error {
	if IsWildcard(name) {
		return WildcardDomain(name)
	}
	return Domain(name)
}

// WildcardDomain checks a "*.example.com" name.
//
// The wildcard must be the whole first label. "*example.com" is refused: nginx
// reads it as a literal name containing an asterisk, which matches nothing and
// produces a site that is configured, valid, and silently unreachable.
func WildcardDomain(name string) error {
	if !IsWildcard(name) {
		return fmt.Errorf("%w: a wildcard name must start with %q", ErrInvalidDomain, WildcardPrefix)
	}

	base := strings.TrimPrefix(name, WildcardPrefix)
	if strings.Contains(base, "*") {
		return fmt.Errorf("%w: only one leading wildcard label is allowed", ErrInvalidDomain)
	}
	if err := Domain(base); err != nil {
		return err
	}

	// The whole name, wildcard included, still has to fit what DNS and nginx
	// will carry.
	if len(name) > MaxDomainLength {
		return fmt.Errorf("%w: must be at most %d characters", ErrInvalidDomain, MaxDomainLength)
	}
	return nil
}

// Subdomain checks that name is a valid hostname beneath parent.
//
// The suffix check is the point of the function. Without it a "subdomain" of
// example.com could be created as anything at all, and the panel would happily
// write a vhost for a name its owner has no claim to — which on a shared host
// means one customer serving content at another customer's domain.
func Subdomain(name, parent string) error {
	name = NormalizeDomain(name)
	parent = NormalizeDomain(parent)

	if err := Domain(parent); err != nil {
		return fmt.Errorf("parent: %w", err)
	}
	if err := ServerName(name); err != nil {
		return err
	}

	if name == parent {
		return fmt.Errorf("%w: %q is the parent website's own domain", ErrInvalidSubdomain, name)
	}

	// The comparison includes the separating dot, so "notexample.com" is not
	// treated as a subdomain of "example.com".
	if !strings.HasSuffix(name, "."+parent) {
		return fmt.Errorf("%w: %q is not under %q", ErrInvalidSubdomain, name, parent)
	}

	prefix := strings.TrimSuffix(name, "."+parent)
	if prefix == "" {
		return fmt.Errorf("%w: %q has no name of its own", ErrInvalidSubdomain, name)
	}
	return nil
}

// SubdomainName builds a full hostname from a label and its parent.
//
// The label may itself contain dots ("dev.shop"), which is how a name several
// levels below the parent is created without nesting one subdomain inside
// another. The result is validated by the caller through Subdomain.
func SubdomainName(label, parent string) string {
	label = strings.Trim(strings.ToLower(strings.TrimSpace(label)), ".")
	return label + "." + NormalizeDomain(parent)
}

// Document root layouts for a subdomain.
//
// Nested keeps a subdomain's files inside its parent's directory, which is
// what an operator expects when the two belong to the same customer: one place
// to back up, one place to find. Isolated gives it a directory of its own at
// the top level, which is what an operator expects when a subdomain is really
// a separate site that happens to share a name.
const (
	DocumentRootNested   = "nested"
	DocumentRootIsolated = "isolated"
)

// DocumentRootMode checks a subdomain's layout choice.
func DocumentRootMode(mode string) error {
	switch mode {
	case DocumentRootNested, DocumentRootIsolated:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidDocumentRootMode, mode)
	}
}

// PHP pool strategies for a subdomain.
//
// Inherit points the subdomain's vhost at the parent's FPM pool: one set of
// workers, one memory limit, one PHP version, and a subdomain that cannot be
// given a different one. Dedicated gives it a pool and socket of its own,
// which is what makes a per-subdomain PHP version or memory limit possible —
// and what stops a busy subdomain exhausting the parent's workers.
const (
	PHPPoolInherit   = "inherit"
	PHPPoolDedicated = "dedicated"
)

// PHPPoolMode checks a subdomain's pool strategy.
func PHPPoolMode(mode string) error {
	switch mode {
	case PHPPoolInherit, PHPPoolDedicated:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPHPPoolMode, mode)
	}
}

// Ownership strategies for a subdomain.
//
// Inherit means the subdomain's files belong to the parent's account, so the
// parent's file manager, FTP login and PHP processes reach them as they would
// their own. Dedicated gives the subdomain its own account, which isolates it
// from the parent — a compromised subdomain cannot read the parent's files —
// at the cost that the parent cannot either.
//
// Inheriting is only coherent with an inherited pool or a dedicated pool
// running as the parent's user; a dedicated account with an inherited pool
// would run PHP as one account over files owned by another, which is the
// permission fault that looks like a broken deployment.
const (
	SystemUserInherit   = "inherit"
	SystemUserDedicated = "dedicated"
)

// SystemUserMode checks a subdomain's ownership strategy.
func SystemUserMode(mode string) error {
	switch mode {
	case SystemUserInherit, SystemUserDedicated:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSystemUserMode, mode)
	}
}
