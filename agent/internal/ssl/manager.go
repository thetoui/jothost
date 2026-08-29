package ssl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// RenewBefore is how long before expiry a certificate is renewed.
//
// Thirty days is the Let's Encrypt convention and it exists for a reason: a
// certificate has 90 days of life, and renewing at 30 leaves two further
// chances to succeed before anything is actually broken.
const RenewBefore = 30 * 24 * time.Hour

// ExpiringSoon is when the panel starts calling a certificate "expiring".
//
// Wider than RenewBefore so an operator sees the warning while automatic
// renewal is still trying, rather than only once it has run out of road.
const ExpiringSoon = 21 * 24 * time.Hour

// Manager issues, renews, and revokes certificates.
type Manager struct {
	store   *Store
	certbot *Certbot
	log     *slog.Logger
	now     func() time.Time
}

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	Store   *Store
	Certbot *Certbot
	Log     *slog.Logger
	// Now is overridable so expiry handling can be tested without waiting.
	Now func() time.Time
}

// NewManager builds a Manager.
func NewManager(opts ManagerOptions) *Manager {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Manager{store: opts.Store, certbot: opts.Certbot, log: log, now: now}
}

// Capabilities describes what this host can do about certificates.
type Capabilities struct {
	// SelfSigned is always true: it needs nothing but the standard library.
	SelfSigned bool `json:"selfsigned"`
	// LetsEncrypt requires certbot, public DNS, and a reachable challenge path.
	LetsEncrypt bool `json:"letsencrypt"`
}

// Capabilities reports which providers are usable here.
func (m *Manager) Capabilities() Capabilities {
	return Capabilities{
		SelfSigned:  true,
		LetsEncrypt: m.certbot != nil && m.certbot.Available(),
	}
}

// Errors returned by the manager.
var (
	ErrUnknownProvider = errors.New("unknown certificate provider")
	ErrNotRevocable    = errors.New("this certificate cannot be revoked")
)

// Request describes a certificate to obtain.
type Request struct {
	Provider string
	Domains  []string
	Email    string
	Staging  bool
}

// Issue obtains a certificate and reports what was produced.
//
// The certificate is read back and parsed after it is written rather than
// being described from the request: what matters downstream is what the file
// actually says, including the expiry a caller cannot know in advance.
func (m *Manager) Issue(ctx context.Context, req Request, report func(int, string)) (Certificate, error) {
	names, err := normalizeDomains(req.Domains)
	if err != nil {
		return Certificate{}, err
	}

	progress(report, 10, "Preparing to issue a certificate")

	var certPath, keyPath string

	switch req.Provider {
	case ProviderSelfSigned:
		certPEM, keyPEM, err := GenerateSelfSigned(names, m.now())
		if err != nil {
			return Certificate{}, err
		}

		progress(report, 60, "Storing the certificate")
		certPath, keyPath, err = m.store.Write(names[0], certPEM, keyPEM)
		if err != nil {
			return Certificate{}, err
		}

	case ProviderLetsEncrypt:
		certPath, keyPath, err = m.certbot.Issue(ctx, IssueRequest{
			Domains: names,
			Email:   req.Email,
			Staging: req.Staging,
		}, report)
		if err != nil {
			return Certificate{}, err
		}

	default:
		return Certificate{}, fmt.Errorf("%w: %q", ErrUnknownProvider, req.Provider)
	}

	// A key that others can read is the whole point of the certificate lost,
	// so it is verified rather than assumed — certbot's output is not this
	// package's to guarantee.
	if err := m.store.CheckKeyPermissions(keyPath); err != nil {
		return Certificate{}, err
	}

	progress(report, 90, "Reading the issued certificate")
	certificate, err := m.store.Load(names[0], certPath, keyPath, req.Provider)
	if err != nil {
		return Certificate{}, err
	}

	progress(report, 100, "Certificate ready")
	return certificate, nil
}

// Renew replaces a certificate that is approaching expiry.
//
// A self-signed certificate is simply regenerated; there is nothing to ask.
func (m *Manager) Renew(ctx context.Context, provider string, domains []string, report func(int, string)) (Certificate, error) {
	switch provider {
	case ProviderSelfSigned:
		return m.Issue(ctx, Request{Provider: ProviderSelfSigned, Domains: domains}, report)

	case ProviderLetsEncrypt:
		names, err := normalizeDomains(domains)
		if err != nil {
			return Certificate{}, err
		}
		if err := m.certbot.Renew(ctx, names[0], report); err != nil {
			return Certificate{}, err
		}
		return m.Status(names[0], provider)

	default:
		return Certificate{}, fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
}

// Revoke withdraws a certificate and removes it from the host.
//
// A self-signed certificate has no authority to revoke it with, so it is
// simply deleted. Saying "revoked" for both is honest at the panel's level:
// either way the certificate stops being used and cannot come back.
func (m *Manager) Revoke(ctx context.Context, provider, domain, certPath string, report func(int, string)) error {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}

	switch provider {
	case ProviderSelfSigned:
		progress(report, 50, "Removing the certificate")
		return m.store.Remove(normalized)

	case ProviderLetsEncrypt:
		if err := m.certbot.Revoke(ctx, normalized, certPath, report); err != nil {
			return err
		}
		// certbot's --delete-after-revoke removes its own lineage; anything
		// this store wrote for the same domain goes too.
		return m.store.Remove(normalized)

	default:
		return fmt.Errorf("%w: %q", ErrUnknownProvider, provider)
	}
}

// Status reports what is currently on disk for a domain.
func (m *Manager) Status(domain, provider string) (Certificate, error) {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return Certificate{}, fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}

	certPath, keyPath, err := m.store.PathsFor(normalized)
	if err != nil {
		return Certificate{}, err
	}

	// A self-signed certificate lives in this store; a certbot one lives under
	// its own root, so both are tried before reporting nothing.
	certificate, err := m.store.Load(normalized, certPath, keyPath, provider)
	if err == nil {
		return certificate, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Certificate{}, err
	}

	return m.store.Load(
		normalized,
		fmt.Sprintf("%s/%s/%s", LetsEncryptRoot, normalized, CertFileName),
		fmt.Sprintf("%s/%s/%s", LetsEncryptRoot, normalized, KeyFileName),
		ProviderLetsEncrypt,
	)
}

// NeedsRenewal reports whether a certificate is close enough to expiry.
func (m *Manager) NeedsRenewal(certificate Certificate) bool {
	return certificate.ExpiresAt.Sub(m.now()) <= RenewBefore
}

// Remove deletes a domain's certificate material without revoking it.
//
// Used when a website is deleted: the certificate has nowhere left to be
// served from, and leaving the key on disk is a private key with no owner.
func (m *Manager) Remove(domain string) error {
	normalized := validate.NormalizeDomain(domain)
	if err := validate.Domain(normalized); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidDomain, err)
	}
	return m.store.Remove(normalized)
}
