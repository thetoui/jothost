package ssl

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/dns"
	"github.com/jothost/panel/api/internal/httpx"
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
	dns      DNSAligner
	log      *slog.Logger
}

// DNSAligner puts the zones this host serves in order before issuance.
//
// An interface, and allowed to be nil: a host with no name server installed
// issues certificates perfectly well against DNS kept somewhere else, and
// requiring the DNS package here would make that the exception rather than the
// ordinary case it is.
type DNSAligner interface {
	AlignForCertificate(ctx context.Context, actor dns.Actor, requestID string,
		names []string) (dns.Alignment, error)
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Websites   *websites.Repository
	PHP        *php.Repository
	Jobs       *jobs.Repository
	Audit      *audit.Recorder
	// DNS is optional: without it, issuance simply does not touch any zone.
	DNS DNSAligner
	Log *slog.Logger
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
		dns:      opts.DNS,
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

// IssueResult is the queued work, and what the panel did to DNS to make it
// able to succeed.
type IssueResult struct {
	Job jobs.Job `json:"job"`
	// DNS is nil where no name server is installed, or where the certificate
	// is self-signed and no authority has to reach anything.
	DNS *dns.Alignment `json:"dns,omitempty"`
}

// Issue queues issuance of a certificate for a website.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (IssueResult, error) {
	site, err := s.readySite(ctx, req.WebsiteID)
	if err != nil {
		return IssueResult{}, err
	}

	provider := req.Provider
	if provider == "" {
		provider = ProviderSelfSigned
	}
	if provider != ProviderSelfSigned && provider != ProviderLetsEncrypt {
		return IssueResult{}, fmt.Errorf("%w: %q", ErrInvalidProvider, provider)
	}

	names, err := s.certificateNames(ctx, site)
	if err != nil {
		return IssueResult{}, err
	}

	// Before anything is asked of a certificate authority: it will resolve
	// each of these names and fetch a file from whatever answers.
	alignment := s.alignDNS(ctx, req, provider, names)

	// The record is written before the job so the panel can show what the site
	// is becoming while issuance runs.
	if _, err := s.repo.Upsert(ctx, UpsertParams{
		WebsiteID: site.ID,
		Provider:  provider,
		Domains:   names,
		AutoRenew: req.AutoRenew,
		Status:    StatusIssuing,
	}); err != nil {
		return IssueResult{}, err
	}

	payload, err := s.agentPayload(ctx, site, names)
	if err != nil {
		return IssueResult{}, err
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
		return IssueResult{}, err
	}

	metadata := map[string]any{
		"domain": site.PrimaryDomain, "provider": provider, "job_id": job.ID,
	}
	if alignment != nil {
		metadata["dns_records_added"] = alignment.Added
		metadata["dns_names_blocked"] = alignment.Blocked
	}
	s.record(ctx, req.Actor, ActionSSLIssue, site.ID, metadata)

	return IssueResult{Job: job, DNS: alignment}, nil
}

// alignDNS points the certificate's names at this host in the zones the panel
// serves, before anything is asked of a certificate authority.
//
// A name that resolves nowhere fails the HTTP-01 challenge, and a failed
// challenge spends one of the few validation attempts Let's Encrypt allows per
// hour. Adding the record first is what an operator would otherwise do by hand
// in another tab a minute before wondering why issuance failed.
//
// A failure here does not stop issuance. The panel's zones are one of several
// places a name can be served from, and refusing to try because this one could
// not be read would block every host whose DNS lives elsewhere.
func (s *Service) alignDNS(ctx context.Context, req IssueRequest, provider string,
	names []string,
) *dns.Alignment {
	// Nothing resolves anything for a self-signed certificate: it is written
	// on this host, and no authority ever looks the name up.
	if s.dns == nil || provider != ProviderLetsEncrypt {
		return nil
	}

	// The request id is passed through because reloading the zone goes to the
	// Agent, which refuses a request without one. An empty string there fails
	// the publish, the whole report is discarded, and what is left is a record
	// in the panel's database that the name server has never read — which
	// looks, from outside, exactly like the panel having done nothing.
	alignment, err := s.dns.AlignForCertificate(ctx, dns.Actor{
		UserID:    req.Actor.UserID,
		IPAddress: req.Actor.IPAddress,
		UserAgent: req.Actor.UserAgent,
	}, httpx.RequestIDFromContext(ctx), names)
	if err != nil {
		s.log.Warn("could not prepare DNS for a certificate; issuing anyway",
			"website_id", req.WebsiteID, logger.KeyError, err.Error())
		return nil
	}
	return &alignment
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
// It starts from the site's complete serving state rather than assembling a
// payload here, because the Agent rewrites the whole vhost from what it is
// given: a field left out is a feature switched off. Issuing a certificate
// used to drop the reverse proxy of a Node site for exactly that reason, and
// the same shape of bug is available for every feature added later.
//
// The names are the exception. A certificate operation decides which names it
// covers — a renewal covers what the existing certificate covers, which is not
// necessarily today's alias list — so those are set here, over the top.
func (s *Service) agentPayload(ctx context.Context, site websites.Website, names []string) (map[string]any, error) {
	payload, err := s.websites.VhostPayload(ctx, site)
	if err != nil {
		return nil, err
	}

	aliases := make([]string, 0, len(names))
	for _, name := range names {
		if name != validate.NormalizeDomain(site.PrimaryDomain) {
			aliases = append(aliases, name)
		}
	}
	payload["domains"] = names
	payload["aliases"] = aliases

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
