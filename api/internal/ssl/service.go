package ssl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for certificate work.
const (
	ActionSSLIssue      = "ssl.issue"
	ActionSSLRenew      = "ssl.renew"
	ActionSSLRevoke     = "ssl.revoke"
	ActionSSLConfigure  = "ssl.configure"
	ResourceTypeWebsite = "website"
)

// Errors returned by the service.
var (
	// ErrProviderUnavailable means the host cannot use that provider.
	ErrProviderUnavailable = errors.New("that certificate provider is not available on this server")
	// ErrWebsiteNotReady means the site cannot take a certificate yet.
	ErrWebsiteNotReady = errors.New("this website is not in a state that allows changing its certificate")
	// ErrNoCertificate means there is nothing to renew or revoke.
	ErrNoCertificate = errors.New("this website has no certificate")
	// ErrInvalidProvider means the provider name is not one of ours.
	ErrInvalidProvider = errors.New("unknown certificate provider")
)

// Service coordinates certificate records with the work that realises them.
type Service struct {
	repo     *Repository
	websites *websites.Repository
	php      *php.Repository
	jobs     *jobs.Repository
	audit    *audit.Recorder
	log      *slog.Logger
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Websites   *websites.Repository
	PHP        *php.Repository
	Jobs       *jobs.Repository
	Audit      *audit.Recorder
	Log        *slog.Logger
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repository,
		websites: opts.Websites,
		php:      opts.PHP,
		jobs:     opts.Jobs,
		audit:    opts.Audit,
		log:      log,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// IssueRequest asks for a certificate.
type IssueRequest struct {
	WebsiteID string
	Provider  string
	Email     string
	Staging   bool
	// RedirectToHTTPS sends plain HTTP to the secure site once it is live.
	RedirectToHTTPS bool
	AutoRenew       bool
	Actor           Actor
}

// Issue queues issuance of a certificate for a website.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (jobs.Job, error) {
	site, err := s.readySite(ctx, req.WebsiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	provider := req.Provider
	if provider == "" {
		provider = ProviderSelfSigned
	}
	if provider != ProviderSelfSigned && provider != ProviderLetsEncrypt {
		return jobs.Job{}, fmt.Errorf("%w: %q", ErrInvalidProvider, provider)
	}

	names, err := s.certificateNames(ctx, site)
	if err != nil {
		return jobs.Job{}, err
	}

	// The record is written before the job so the panel can show what the site
	// is becoming while issuance runs.
	if _, err := s.repo.Upsert(ctx, UpsertParams{
		WebsiteID: site.ID,
		Provider:  provider,
		Domains:   names,
		AutoRenew: req.AutoRenew,
		Status:    StatusIssuing,
	}); err != nil {
		return jobs.Job{}, err
	}

	payload, err := s.agentPayload(ctx, site, names)
	if err != nil {
		return jobs.Job{}, err
	}
	payload["provider"] = provider
	payload["email"] = req.Email
	payload["staging"] = req.Staging
	payload["redirect_to_https"] = req.RedirectToHTTPS

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeSSLIssue,
		Payload:      payload,
		CreatedBy:    req.Actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		if statusErr := s.repo.SetStatus(ctx, site.ID, StatusFailed,
			"the issuance job could not be queued"); statusErr != nil {
			s.log.Error("failed to reset certificate status after a queue failure",
				"website_id", site.ID, logger.KeyError, statusErr.Error())
		}
		return jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionSSLIssue, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "provider": provider, "job_id": job.ID,
	})
	return job, nil
}

// Renew queues renewal of an existing certificate.
func (s *Service) Renew(ctx context.Context, websiteID string, actor Actor) (jobs.Job, error) {
	site, err := s.readySite(ctx, websiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	existing, err := s.repo.Get(ctx, websiteID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return jobs.Job{}, ErrNoCertificate
		}
		return jobs.Job{}, err
	}

	job, err := s.queueRenewal(ctx, site, existing, actor)
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionSSLRenew, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "job_id": job.ID,
	})
	return job, nil
}

// queueRenewal builds and queues a renewal job.
//
// Shared with the automatic sweep, which must produce exactly the same work as
// a person clicking renew — two code paths for one operation is how they drift.
func (s *Service) queueRenewal(ctx context.Context, site websites.Website,
	existing Certificate, actor Actor,
) (jobs.Job, error) {
	payload, err := s.agentPayload(ctx, site, existing.Domains)
	if err != nil {
		return jobs.Job{}, err
	}
	payload["provider"] = existing.Provider
	payload["redirect_to_https"] = site.HTTPSRedirect

	if err := s.repo.MarkRenewalAttempted(ctx, site.ID); err != nil {
		return jobs.Job{}, err
	}

	return s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeSSLRenew,
		Payload:      payload,
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
}

// Revoke queues withdrawal of a certificate.
//
// Revocation is irreversible: the certificate is dead for every client that
// checks it, and the only way back is to issue a new one. The handler confirms
// with the user before reaching here.
func (s *Service) Revoke(ctx context.Context, websiteID string, actor Actor) (jobs.Job, error) {
	site, err := s.websites.Get(ctx, websiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	existing, err := s.repo.Get(ctx, websiteID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return jobs.Job{}, ErrNoCertificate
		}
		return jobs.Job{}, err
	}

	names := existing.Domains
	if len(names) == 0 {
		names = []string{site.PrimaryDomain}
	}

	payload, err := s.agentPayload(ctx, site, names)
	if err != nil {
		return jobs.Job{}, err
	}
	payload["provider"] = existing.Provider
	if existing.CertificatePath != nil {
		payload["certificate_path"] = *existing.CertificatePath
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeSSLRevoke,
		Payload:      payload,
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionSSLRevoke, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "job_id": job.ID,
	})
	return job, nil
}

// ConfigureRequest changes settings that need no new certificate.
type ConfigureRequest struct {
	WebsiteID       string
	AutoRenew       *bool
	RedirectToHTTPS *bool
	Actor           Actor
}

// Configure changes auto-renewal and the HTTPS redirect.
//
// Changing the redirect rewrites the vhost, because the setting *is* the vhost;
// changing auto-renewal only touches a row, because nothing on the host reads
// it. They are one endpoint because to a user they are one panel of switches.
func (s *Service) Configure(ctx context.Context, req ConfigureRequest) (*jobs.Job, error) {
	site, err := s.websites.Get(ctx, req.WebsiteID)
	if err != nil {
		return nil, err
	}

	existing, err := s.repo.Get(ctx, req.WebsiteID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNoCertificate
		}
		return nil, err
	}

	if req.AutoRenew != nil {
		if err := s.repo.SetAutoRenew(ctx, req.WebsiteID, *req.AutoRenew); err != nil {
			return nil, err
		}
	}

	if req.RedirectToHTTPS == nil || *req.RedirectToHTTPS == site.HTTPSRedirect {
		s.record(ctx, req.Actor, ActionSSLConfigure, site.ID, map[string]any{
			"domain": site.PrimaryDomain,
		})
		return nil, nil
	}

	if err := s.repo.SetWebsiteSSL(ctx, site.ID, true, *req.RedirectToHTTPS); err != nil {
		return nil, err
	}

	// The vhost has to be rewritten for the redirect to mean anything, so this
	// goes through the same issue path that wrote it in the first place.
	payload, err := s.agentPayload(ctx, site, existing.Domains)
	if err != nil {
		return nil, err
	}
	payload["provider"] = existing.Provider
	payload["redirect_to_https"] = *req.RedirectToHTTPS

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeSSLRenew,
		Payload:      payload,
		CreatedBy:    req.Actor.UserID,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		return nil, err
	}

	s.record(ctx, req.Actor, ActionSSLConfigure, site.ID, map[string]any{
		"domain": site.PrimaryDomain, "https_redirect": *req.RedirectToHTTPS, "job_id": job.ID,
	})
	return &job, nil
}

// readySite loads a website and refuses one that cannot take a certificate.
func (s *Service) readySite(ctx context.Context, websiteID string) (websites.Website, error) {
	site, err := s.websites.Get(ctx, websiteID)
	if err != nil {
		return websites.Website{}, err
	}

	// A site mid-provision has no vhost to rewrite, and one mid-delete is going
	// away. Issuing for either produces work that cannot succeed.
	if site.Status != websites.StatusActive && site.Status != websites.StatusFailed {
		return websites.Website{}, fmt.Errorf("%w: it is %s", ErrWebsiteNotReady, site.Status)
	}
	return site, nil
}

// certificateNames returns every name the certificate must cover.
//
// A visitor reaching www.example.com on a certificate that names only
// example.com gets a browser warning indistinguishable from an attack, so the
// aliases are included rather than left to the user to remember.
func (s *Service) certificateNames(ctx context.Context, site websites.Website) ([]string, error) {
	domains, err := s.websites.ListDomains(ctx, site.ID)
	if err != nil {
		return nil, err
	}

	names := []string{validate.NormalizeDomain(site.PrimaryDomain)}
	for _, domain := range domains {
		// A redirect target is served by a different vhost and a subdomain may
		// point elsewhere; only names this site actually answers to belong on
		// its certificate.
		if domain.Type == websites.DomainPrimary || domain.Type == websites.DomainRedirect {
			continue
		}
		names = append(names, validate.NormalizeDomain(domain.Domain))
	}
	return names, nil
}

// agentPayload builds the fields every certificate operation needs.
//
// The site's PHP socket is included because the Agent rewrites the whole vhost:
// omitting it would silently turn PHP off for the site as a side effect of
// issuing a certificate.
func (s *Service) agentPayload(ctx context.Context, site websites.Website, names []string) (map[string]any, error) {
	aliases := make([]string, 0, len(names))
	for _, name := range names {
		if name != validate.NormalizeDomain(site.PrimaryDomain) {
			aliases = append(aliases, name)
		}
	}

	payload := map[string]any{
		"website_id":    site.ID,
		"domain":        site.PrimaryDomain,
		"domains":       names,
		"aliases":       aliases,
		"document_root": site.DocumentRoot,
	}

	pool, err := s.php.GetPool(ctx, site.ID)
	switch {
	case err == nil:
		payload["php_socket"] = pool.SocketPath
	case errors.Is(err, php.ErrPoolNotFound):
		// A static site; the vhost is rewritten without a PHP block, which is
		// what it already had.
	default:
		return nil, err
	}

	return payload, nil
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, websiteID string, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   websiteID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	})
}
