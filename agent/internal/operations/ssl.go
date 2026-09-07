package operations

import (
	"context"
	"errors"
	"time"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/ssl"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// handleSSLCapabilities reports which certificate providers work on this host.
//
// The panel hides Let's Encrypt when certbot is absent rather than offering a
// button whose failure would look like a bug in the panel.
func (r *Registry) handleSSLCapabilities(_ context.Context, _ protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	if r.deps.SSL == nil {
		return nil, Fail(protocol.CodeUnsupported, "SSL management is not available on this host", nil)
	}

	capabilities := r.deps.SSL.Capabilities()
	return map[string]any{
		"selfsigned":  capabilities.SelfSigned,
		"letsencrypt": capabilities.LetsEncrypt,
	}, nil
}

// sslPayload describes a certificate operation.
type sslPayload struct {
	WebsiteID    string   `json:"website_id"`
	Domain       string   `json:"domain"`
	Domains      []string `json:"domains"`
	Provider     string   `json:"provider"`
	Email        string   `json:"email"`
	Staging      bool     `json:"staging"`
	DocumentRoot string   `json:"document_root"`
	// SystemUser is not acted on here — a certificate has no owner — but it is
	// part of every vhost payload the panel builds, and rejecting it would
	// make that one builder unusable for this operation.
	SystemUser  string   `json:"system_user"`
	PHPSocket   string   `json:"php_socket"`
	MaxBodySize string   `json:"max_body_size"`
	Aliases     []string `json:"aliases"`
	// AliasRoots are the names on this site served from directories of their
	// own. Carried here because this operation rewrites the whole vhost:
	// without them, every alias with a root of its own would quietly go back
	// to the site's as a side effect of an unrelated change.
	AliasRoots []aliasRootPayload `json:"alias_roots"`
	// ProxyPort keeps a reverse-proxied site proxied. Issuing a certificate
	// rewrites the whole vhost, so without this a Node.js site would go onto
	// HTTPS and stop reaching its own application in the same operation.
	ProxyPort int `json:"proxy_port"`
	// PrivateKeyPath is carried for symmetry with the rest of the payload; the
	// key this operation installs is the one the provider just wrote.
	PrivateKeyPath string `json:"private_key_path"`
	// The hybrid arrangement, for the same reason as the port above: issuing a
	// certificate rewrites the whole configuration, and a site would otherwise
	// come out of it served by nginx alone.
	ApachePort    int   `json:"apache_port"`
	AllowOverride bool  `json:"allow_override"`
	MaxBodyBytes  int64 `json:"max_body_bytes"`
	// RedirectToHTTPS sends plain HTTP to the secure site once the certificate
	// is live.
	RedirectToHTTPS bool `json:"redirect_to_https"`
	// CertificatePath is needed to revoke a certbot certificate.
	CertificatePath string `json:"certificate_path"`
}

// handleSSLIssue obtains a certificate and puts the site on HTTPS.
//
// The certificate is obtained first and the vhost rewritten second. That order
// is the whole safety of the operation: a vhost naming a certificate that does
// not exist stops nginx from starting at all, which would take down every other
// site on the host, not just this one.
func (r *Registry) handleSSLIssue(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload sslPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.SSL == nil {
		return nil, Fail(protocol.CodeUnsupported, "SSL management is not available on this host", nil)
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}

	provider := payload.Provider
	if provider == "" {
		provider = ssl.ProviderSelfSigned
	}

	// The certificate covers every name the site answers to. A visitor
	// reaching www.example.com on a certificate naming only example.com gets a
	// browser warning, which is indistinguishable from an attack.
	names := certificateNames(payload)

	progressReport := reporterFunc(reporter)

	certificate, err := r.deps.SSL.Issue(ctx, ssl.Request{
		Provider: provider,
		Domains:  names,
		Email:    payload.Email,
		Staging:  payload.Staging,
	}, progressReport)
	if err != nil {
		return nil, sslError(err)
	}

	result, err := r.applyCertificate(ctx, payload, certificate)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// handleSSLRenew replaces a certificate approaching expiry.
func (r *Registry) handleSSLRenew(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload sslPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.SSL == nil {
		return nil, Fail(protocol.CodeUnsupported, "SSL management is not available on this host", nil)
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}

	provider := payload.Provider
	if provider == "" {
		provider = ssl.ProviderSelfSigned
	}

	progressReport := reporterFunc(reporter)

	certificate, err := r.deps.SSL.Renew(ctx, provider, certificateNames(payload), progressReport)
	if err != nil {
		return nil, sslError(err)
	}

	// The vhost is rewritten even though the paths usually did not change: a
	// renewal that produced a new path with the old one still in the config is
	// a site serving an expired certificate with a valid one sitting beside it.
	if payload.DocumentRoot != "" {
		return r.applyCertificate(ctx, payload, certificate)
	}

	// Without a document root there is no vhost to rewrite, which is the case
	// when the sweep renews a certificate the panel tracks but this Agent did
	// not provision.
	return certificateResult(certificate, "", false), nil
}

// handleSSLRevoke withdraws a certificate and returns the site to HTTP.
//
// The vhost is rewritten first here, the reverse of issuing: the site must stop
// naming the certificate before the files are removed, or nginx is left
// pointing at something that is gone.
func (r *Registry) handleSSLRevoke(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload sslPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.SSL == nil {
		return nil, Fail(protocol.CodeUnsupported, "SSL management is not available on this host", nil)
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}

	provider := payload.Provider
	if provider == "" {
		provider = ssl.ProviderSelfSigned
	}

	progressReport := reporterFunc(reporter)
	report(progressReport, 30, "Returning the website to HTTP")

	reloaded := false
	if payload.DocumentRoot != "" {
		updated, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
			Domain:        payload.Domain,
			Aliases:       payload.Aliases,
			AliasRoots:    toAliasRoots(payload.AliasRoots),
			DocumentRoot:  payload.DocumentRoot,
			MaxBodySize:   payload.MaxBodySize,
			PHPSocket:     payload.PHPSocket,
			ProxyPort:     payload.ProxyPort,
			ApachePort:    payload.ApachePort,
			AllowOverride: payload.AllowOverride,
			MaxBodyBytes:  payload.MaxBodyBytes,
			// No SSL: the rendered vhost drops the HTTPS block entirely.
		}, nil)
		if err != nil {
			return nil, websiteError(err)
		}
		reloaded = updated.Reloaded
	}

	report(progressReport, 70, "Revoking the certificate")
	if err := r.deps.SSL.Revoke(ctx, provider, payload.Domain, payload.CertificatePath, progressReport); err != nil {
		// The site is already back on HTTP and serving. Reporting a failure
		// now would describe an outage that did not happen, but the leftover
		// key is worth an operator's attention.
		r.log.Warn("website returned to HTTP but the certificate was not revoked",
			"domain", payload.Domain, "error", err.Error())
		return map[string]any{
			"domain":   payload.Domain,
			"revoked":  false,
			"reloaded": reloaded,
			"detail":   "the site no longer serves HTTPS, but the certificate could not be revoked",
		}, nil
	}

	report(progressReport, 100, "Certificate revoked")
	return map[string]any{
		"domain":   payload.Domain,
		"revoked":  true,
		"reloaded": reloaded,
	}, nil
}

// handleSSLStatus reports the certificate currently on disk for a domain.
func (r *Registry) handleSSLStatus(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload sslPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.SSL == nil {
		return nil, Fail(protocol.CodeUnsupported, "SSL management is not available on this host", nil)
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}

	certificate, err := r.deps.SSL.Status(payload.Domain, payload.Provider)
	if err != nil {
		if errors.Is(err, ssl.ErrNotFound) {
			return map[string]any{"domain": payload.Domain, "present": false}, nil
		}
		return nil, sslError(err)
	}

	result := certificateResult(certificate, "", false)
	result["present"] = true
	result["needs_renewal"] = r.deps.SSL.NeedsRenewal(certificate)
	return result, nil
}

// applyCertificate points a site's vhost at a certificate and reloads.
func (r *Registry) applyCertificate(ctx context.Context, payload sslPayload, certificate ssl.Certificate) (map[string]any, error) {
	// The challenge path is exposed for every HTTPS site, not only ACME ones.
	//
	// Issuing a certificate happens while the *previous* configuration is
	// still live. A self-signed site with the redirect on would therefore send
	// the certificate authority's challenge fetch to HTTPS, where it will not
	// follow, and issuing Let's Encrypt on that site could never succeed. The
	// block costs one location and serves nothing but transient tokens.
	updated, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:        payload.Domain,
		Aliases:       payload.Aliases,
		AliasRoots:    toAliasRoots(payload.AliasRoots),
		DocumentRoot:  payload.DocumentRoot,
		MaxBodySize:   payload.MaxBodySize,
		PHPSocket:     payload.PHPSocket,
		ProxyPort:     payload.ProxyPort,
		ApachePort:    payload.ApachePort,
		AllowOverride: payload.AllowOverride,
		MaxBodyBytes:  payload.MaxBodyBytes,
		SSL: &nginx.SSLConfig{
			CertificatePath: certificate.CertPath,
			PrivateKeyPath:  certificate.KeyPath,
			RedirectToHTTPS: payload.RedirectToHTTPS,
			ChallengeRoot:   ssl.ACMEChallengeDir,
		},
	}, nil)
	if err != nil {
		return nil, websiteError(err)
	}

	return certificateResult(certificate, updated.ConfigPath, updated.Reloaded), nil
}

// certificateNames returns every name a site's certificate must cover.
func certificateNames(payload sslPayload) []string {
	if len(payload.Domains) > 0 {
		return payload.Domains
	}

	names := make([]string, 0, len(payload.Aliases)+1)
	names = append(names, payload.Domain)
	names = append(names, payload.Aliases...)
	return names
}

// certificateResult renders a certificate for a protocol response.
//
// Paths are included because the panel stores them; the key's *contents* are
// never read here, let alone returned.
func certificateResult(certificate ssl.Certificate, configPath string, reloaded bool) map[string]any {
	return map[string]any{
		"domain":           certificate.Domain,
		"domains":          certificate.Domains,
		"provider":         certificate.Provider,
		"certificate_path": certificate.CertPath,
		"private_key_path": certificate.KeyPath,
		"issuer":           certificate.Issuer,
		"fingerprint":      certificate.Fingerprint,
		"issued_at":        certificate.IssuedAt.UTC().Format(time.RFC3339),
		"expires_at":       certificate.ExpiresAt.UTC().Format(time.RFC3339),
		"self_signed":      certificate.SelfSigned,
		"config_path":      configPath,
		"reloaded":         reloaded,
	}
}

// sslError maps a certificate failure to a structured protocol error.
//
// Only messages written deliberately here reach the caller; the underlying
// error travels as Cause, which is logged and never serialised.
func sslError(err error) error {
	switch {
	case errors.Is(err, ssl.ErrCertbotUnavailable):
		return Fail(protocol.CodeUnsupported,
			"certbot is not installed, so a publicly trusted certificate cannot be issued here", err)
	case errors.Is(err, ssl.ErrNotFound):
		return Fail(protocol.CodeNotFound, "No certificate was found for this domain", err)
	case errors.Is(err, ssl.ErrKeyExposed):
		return Fail(protocol.CodeInternal,
			"The private key was written with permissions that would expose it", err)
	case errors.Is(err, ssl.ErrUnknownProvider),
		errors.Is(err, ssl.ErrInvalidDomain),
		errors.Is(err, validate.ErrInvalidDomain):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	case errors.Is(err, ssl.ErrIssueFailed):
		// certbot's own diagnostic names the real obstacle, and it is what an
		// operator needs to fix it.
		return Fail(protocol.CodeInternal, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, "The certificate operation failed", err)
	}
}
