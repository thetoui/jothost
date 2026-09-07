package websites

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for website work.
const (
	ActionWebsiteCreate = "website.create"
	ActionWebsiteUpdate = "website.update"
	ActionWebsiteDelete = "website.delete"
	ActionDomainCreate  = "domain.create"
	ActionDomainDelete  = "domain.delete"
	ResourceTypeWebsite = "website"
)

// SiteRoot is where website directories live on the managed host.
//
// The API computes the document root rather than accepting one: a path from a
// client is a path the Agent would resolve as root. The Agent validates it
// again on arrival — this is the first of two checks, not the only one.
const SiteRoot = "/var/www"

// ContentDir is the directory served within a site's root. It matches the
// Agent's sites.ContentDir; the two must agree or the vhost points nowhere.
const ContentDir = "public"

// Errors returned by the service.
var (
	// ErrSSLUnsupported is returned for ssl_enabled until Phase 6 issues
	// certificates. Accepting the flag and silently ignoring it would leave a
	// user believing their site is encrypted when it is not.
	ErrSSLUnsupported = errors.New("SSL is not available yet")
	// ErrInvalidState means the website is not in a state the action allows.
	ErrInvalidState = errors.New("website is not in a state that allows this")
	// ErrRedirectUnsupported is returned for redirect domains.
	//
	// The Agent can write a redirect vhost but has no way to remove a stale
	// one, so accepting the type would create configuration the panel could
	// never take back. Refusing is honest; a redirect that cannot be undone is
	// worse than one that does not exist yet.
	ErrRedirectUnsupported = errors.New("redirect domains are not available yet")
)

// Service coordinates website records with the work that realises them.
type Service struct {
	repo          *Repository
	jobs          *jobs.Repository
	audit         *audit.Recorder
	dns           DNSPublisher
	subscriptions Subscriptions
	log           *slog.Logger
	serverID      string
}

// Subscriptions records which subscription a newly created website belongs to.
//
// An interface, and nil-able, for the reason DNSPublisher is: it keeps this
// package from importing the tenancy package, and a panel with no tenancy
// configured must still be able to create a website — the server's own
// administrator owns the machine rather than a slice of it, and their sites
// belong to no subscription.
//
// It matters more than it looks. Without it the quota that authorised a
// creation would never see the site it authorised, the count would never rise,
// and a limit of one website would let somebody create as many as they liked.
type Subscriptions interface {
	AssignNewWebsite(ctx context.Context, ownerUserID, subscriptionID, websiteID string) error
}

// SetSubscriptions wires the tenancy service in after construction.
//
// After rather than through ServiceOptions, because the tenancy service is
// built later — it needs the Agent client, which needs configuration this
// service is already constructed from. The alternative was reordering half of
// server.go to satisfy one field.
func (s *Service) SetSubscriptions(subscriptions Subscriptions) {
	s.subscriptions = subscriptions
}

// DNSPublisher puts a subdomain's record into its parent's zone, and takes it
// out again.
//
// This is the item Phase 4.1 deferred: there was no zone to put a record in
// then, and docs/PHASE4.1.md section 7 said it would be a small addition once
// Phase 13 existed.
//
// An interface, and nil-able, for two reasons. It keeps this package from
// importing the dns package, which is the arrangement every other cross-feature
// dependency here uses. And a panel that does not serve DNS for the parent must
// still be able to create a subdomain: a subdomain is reached through whatever
// already resolves the parent, so DNS here is a convenience and never a
// requirement.
type DNSPublisher interface {
	EnsureSubdomainRecords(ctx context.Context, requestID, parent, name, address string) error
	RemoveSubdomainRecords(ctx context.Context, requestID, parent, name string) error
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repository *Repository
	Jobs       *jobs.Repository
	Audit      *audit.Recorder
	// DNS publishes a subdomain in its parent's zone. Nil on a panel that does
	// not serve DNS, which is not an error: see DNSPublisher.
	DNS DNSPublisher
	Log *slog.Logger
	// ServerID is the host these websites live on. Phase 4 manages a single
	// server; the column exists so multi-server does not need a migration.
	ServerID string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repository,
		jobs:     opts.Jobs,
		audit:    opts.Audit,
		dns:      opts.DNS,
		log:      log,
		serverID: opts.ServerID,
	}
}

// CreateRequest is a request to host a new website.
type CreateRequest struct {
	Domain     string
	Name       string
	SSLEnabled bool
	// SubscriptionID puts the site inside a named subscription. Empty means
	// the creator's own, if they have one.
	SubscriptionID string
	// Actor is the user making the request, for the audit trail.
	Actor     string
	IPAddress string
	UserAgent string
}

// CreateResult is a queued website creation.
type CreateResult struct {
	Website Website  `json:"website"`
	Job     jobs.Job `json:"job"`
}

// Create records a website and queues the work to provision it.
//
// The row is written first and the job second, both before anything touches
// the host. If the process dies between them the site sits in "creating" with
// no job, which is visible and recoverable; queuing first would risk
// provisioning a site the panel has no record of.
func (s *Service) Create(ctx context.Context, req CreateRequest) (CreateResult, error) {
	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.Domain(domain); err != nil {
		return CreateResult{}, err
	}

	if req.SSLEnabled {
		return CreateResult{}, ErrSSLUnsupported
	}

	systemUser, err := deriveSystemUser(domain)
	if err != nil {
		return CreateResult{}, err
	}

	documentRoot := path.Join(SiteRoot, domain, ContentDir)

	site, err := s.repo.Create(ctx, CreateParams{
		ServerID:      s.serverID,
		Name:          strings.TrimSpace(req.Name),
		PrimaryDomain: domain,
		DocumentRoot:  documentRoot,
		SystemUser:    systemUser,
	})
	if err != nil {
		return CreateResult{}, err
	}

	// A site created on a host running the hybrid arrangement is served
	// through Apache from the moment it exists. Leaving it out until the next
	// mode change would make "which sites use Apache" depend on when they were
	// created, which is not a fact anyone could reason about.
	site, err = s.joinArrangement(ctx, site)
	if err != nil {
		return CreateResult{}, err
	}

	payload, err := s.repo.VhostPayload(ctx, site)
	if err != nil {
		return CreateResult{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsiteCreate,
		Payload:      payload,
		CreatedBy:    req.Actor,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		// The website row exists but nothing will provision it. Marking it
		// failed is more honest than leaving it "creating" forever.
		if statusErr := s.repo.SetStatus(ctx, site.ID, StatusFailed); statusErr != nil {
			s.log.Error("failed to mark unqueued website as failed",
				"website_id", site.ID, logger.KeyError, statusErr.Error())
		}
		return CreateResult{}, err
	}

	// The site joins the creator's subscription, if they have one.
	//
	// After the job is queued rather than before, and a failure here does not
	// fail the creation: the site exists and is being provisioned, and a panel
	// that unwound a working website because a membership row could not be
	// written would be doing the customer no favours. It is logged loudly,
	// because a site outside its subscription is a site outside its quota.
	if s.subscriptions != nil && req.Actor != "" {
		if err := s.subscriptions.AssignNewWebsite(ctx, req.Actor, req.SubscriptionID,
			site.ID); err != nil {
			s.log.Error("created a website but could not record its subscription",
				"website_id", site.ID, "domain", domain, logger.KeyError, err.Error())
		}
	}

	s.record(ctx, req.Actor, ActionWebsiteCreate, site.ID, audit.StatusSuccess,
		map[string]any{"domain": domain, "job_id": job.ID}, req.IPAddress, req.UserAgent)

	return CreateResult{Website: site, Job: job}, nil
}

// deriveSystemUser builds the account name a site runs as.
//
// The random suffix is what keeps two similar domains from colliding after the
// name is truncated to useradd's 32-character limit: "averylongdomain.example"
// and "averylongdomain.test" both truncate to the same base.
func deriveSystemUser(domain string) (string, error) {
	suffix := make([]byte, 3)
	if _, err := rand.Read(suffix); err != nil {
		return "", fmt.Errorf("generate system user suffix: %w", err)
	}

	name := validate.SystemUserFor(domain, hex.EncodeToString(suffix))
	if err := validate.SystemUser(name); err != nil {
		return "", err
	}
	return name, nil
}

// DeleteRequest is a request to remove a website.
type DeleteRequest struct {
	WebsiteID string
	Actor     string
	IPAddress string
	UserAgent string
	// RemoveFiles deletes the site's content along with it.
	//
	// Off by default, and that default is the deliberate one: a vhost can be
	// recreated and content cannot. But keeping the files has a cost that used
	// to be hidden — the directory stays, provisioning refuses to adopt it, and
	// without this the domain could never be created again through the panel at
	// all. So the choice is offered rather than made silently in either
	// direction.
	RemoveFiles bool
}

// Delete queues removal of a website.
//
// The row is moved to "deleting" and kept until the Agent confirms the site is
// gone. Deleting it now would leave the files, the account, and the vhost on
// the host with nothing in the panel pointing at them.
func (s *Service) Delete(ctx context.Context, req DeleteRequest) (jobs.Job, error) {
	site, err := s.repo.Get(ctx, req.WebsiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	if site.Status == StatusDeleting {
		return jobs.Job{}, fmt.Errorf("%w: it is already being deleted", ErrInvalidState)
	}

	// A parent with subdomains is refused rather than cascaded.
	//
	// The database would delete the rows, but the rows are not the site: each
	// subdomain has a vhost and possibly an account on the host, and nothing
	// would be queued to remove them. The panel would forget about names nginx
	// is still serving — and with a nested layout, the parent's deletion takes
	// the subdomain's files while its configuration stays live, which is a
	// 403 for a site nobody can find in the panel any more.
	subdomains, err := s.repo.ListSubdomains(ctx, site.ID)
	if err != nil {
		return jobs.Job{}, err
	}
	if len(subdomains) > 0 {
		names := make([]string, 0, len(subdomains))
		for _, sub := range subdomains {
			names = append(names, sub.PrimaryDomain)
		}
		return jobs.Job{}, fmt.Errorf("%w: remove %s first",
			ErrHasSubdomains, strings.Join(names, ", "))
	}

	if err := s.repo.SetStatus(ctx, site.ID, StatusDeleting); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type: jobs.TypeWebsiteDelete,
		Payload: map[string]any{
			"domain":        site.PrimaryDomain,
			"document_root": site.DocumentRoot,
			"system_user":   site.SystemUser,
			// Removing the account is deliberate and separate: a site's files
			// can be deleted while its user is still referenced elsewhere.
			"remove_user": true,
			// Kept by default. When they are kept the Agent reassigns them to
			// root before the account goes, so the uid about to be reused stops
			// owning anything.
			"remove_files": req.RemoveFiles,
		},
		CreatedBy:    req.Actor,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
	if err != nil {
		// Put the site back so it is not stuck in "deleting" with no job.
		if statusErr := s.repo.SetStatus(ctx, site.ID, site.Status); statusErr != nil {
			s.log.Error("failed to restore website status after a queue failure",
				"website_id", site.ID, logger.KeyError, statusErr.Error())
		}
		return jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionWebsiteDelete, site.ID, audit.StatusSuccess,
		map[string]any{"domain": site.PrimaryDomain, "job_id": job.ID},
		req.IPAddress, req.UserAgent)

	return job, nil
}

// AddDomainRequest attaches a hostname to a website.
type AddDomainRequest struct {
	WebsiteID  string
	Domain     string
	Type       string
	RedirectTo string
	Actor      string
	IPAddress  string
	UserAgent  string
}

// AddDomain attaches a hostname and queues a vhost update.
func (s *Service) AddDomain(ctx context.Context, req AddDomainRequest) (Domain, jobs.Job, error) {
	site, err := s.repo.Get(ctx, req.WebsiteID)
	if err != nil {
		return Domain{}, jobs.Job{}, err
	}

	domain := validate.NormalizeDomain(req.Domain)
	if err := validate.Domain(domain); err != nil {
		return Domain{}, jobs.Job{}, err
	}

	domainType := req.Type
	if domainType == "" {
		domainType = DomainAlias
	}
	switch domainType {
	case DomainAlias, DomainSubdomain:
	case DomainRedirect:
		return Domain{}, jobs.Job{}, ErrRedirectUnsupported
	case DomainPrimary:
		return Domain{}, jobs.Job{}, fmt.Errorf(
			"%w: a website already has its primary domain", ErrInvalidState)
	default:
		return Domain{}, jobs.Job{}, fmt.Errorf("%w: unknown domain type %q",
			ErrInvalidState, domainType)
	}

	redirectTo := strings.TrimSpace(req.RedirectTo)
	if domainType == DomainRedirect {
		// The target is rendered into an nginx return directive, so it is
		// validated as a hostname rather than accepted as free text.
		redirectTo = validate.NormalizeDomain(redirectTo)
		if err := validate.Domain(redirectTo); err != nil {
			return Domain{}, jobs.Job{}, fmt.Errorf("redirect target: %w", err)
		}
	} else {
		redirectTo = ""
	}

	created, err := s.repo.AddDomain(ctx, AddDomainParams{
		WebsiteID:  site.ID,
		Domain:     domain,
		Type:       domainType,
		RedirectTo: redirectTo,
	})
	if err != nil {
		return Domain{}, jobs.Job{}, err
	}

	job, err := s.queueVhostUpdate(ctx, site, req.Actor)
	if err != nil {
		return Domain{}, jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionDomainCreate, site.ID, audit.StatusSuccess,
		map[string]any{"domain": domain, "type": domainType, "job_id": job.ID},
		req.IPAddress, req.UserAgent)

	return created, job, nil
}

// RemoveDomainRequest detaches a hostname.
type RemoveDomainRequest struct {
	DomainID  string
	Actor     string
	IPAddress string
	UserAgent string
}

// RemoveDomain detaches a hostname and queues a vhost update.
func (s *Service) RemoveDomain(ctx context.Context, req RemoveDomainRequest) (jobs.Job, error) {
	domain, err := s.repo.GetDomain(ctx, req.DomainID)
	if err != nil {
		return jobs.Job{}, err
	}

	site, err := s.repo.Get(ctx, domain.WebsiteID)
	if err != nil {
		return jobs.Job{}, err
	}

	if err := s.repo.DeleteDomain(ctx, req.DomainID); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.queueVhostUpdate(ctx, site, req.Actor)
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, req.Actor, ActionDomainDelete, site.ID, audit.StatusSuccess,
		map[string]any{"domain": domain.Domain, "job_id": job.ID},
		req.IPAddress, req.UserAgent)

	return job, nil
}

// queueVhostUpdate asks the Agent to rewrite a site's nginx configuration.
//
// The payload carries the site's full desired state rather than a delta: the
// Agent rewrites the vhost from it, so a job that is retried or arrives out of
// order still converges on the same configuration.
//
// "Full" includes the PHP socket, the certificate and the application port,
// which is why this goes through VhostPayload. Sending only the names — which
// is all this function used to send — turned PHP, HTTPS and the reverse proxy
// off as a side effect of adding an alias.
func (s *Service) queueVhostUpdate(ctx context.Context, site Website, actor string) (jobs.Job, error) {
	payload, err := s.repo.VhostPayload(ctx, site)
	if err != nil {
		return jobs.Job{}, err
	}

	return s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsiteUpdate,
		Payload:      payload,
		CreatedBy:    actor,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   site.ID,
	})
}

// JobFinished reconciles a website with the outcome of its job.
//
// This is the only place a site becomes "active": the panel does not claim a
// site works until the Agent has reported that it does.
//
// It satisfies jobs.Observer.
func (s *Service) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	_ map[string]any, failure string,
) {
	if job.ResourceType == nil || *job.ResourceType != ResourceTypeWebsite ||
		job.ResourceID == nil {
		return
	}
	websiteID := *job.ResourceID

	log := s.log.With("website_id", websiteID, "job_id", job.ID, "type", job.Type)

	switch job.Type {
	case jobs.TypeWebsiteCreate, jobs.TypeWebsiteUpdate:
		status := StatusActive
		if state != jobs.StateSuccess {
			status = StatusFailed
		}
		if err := s.repo.SetStatus(ctx, websiteID, status); err != nil {
			if errors.Is(err, ErrNotFound) {
				// The site was deleted while its job ran; nothing to reconcile.
				return
			}
			log.Error("failed to reconcile website status", logger.KeyError, err.Error())
			return
		}
		log.Info("website reconciled", "status", status)

	case jobs.TypeWebsiteDelete:
		if state != jobs.StateSuccess {
			// The files may still be on the host. The row stays so the failure
			// is visible and the delete can be retried, rather than vanishing
			// and leaving orphaned directories nobody knows about.
			if err := s.repo.SetStatus(ctx, websiteID, StatusFailed); err != nil &&
				!errors.Is(err, ErrNotFound) {
				log.Error("failed to mark website delete as failed",
					logger.KeyError, err.Error())
			}
			log.Warn("website delete failed; the row was kept", "reason", failure)
			return
		}
		if err := s.repo.Delete(ctx, websiteID); err != nil && !errors.Is(err, ErrNotFound) {
			log.Error("failed to remove website row", logger.KeyError, err.Error())
			return
		}
		log.Info("website removed")
	}
}

// record writes an audit event, logging rather than failing the request.
//
// The action already happened by the time this runs; returning an error would
// tell the user their website was not created when it was.
func (s *Service) record(ctx context.Context, actor, action, resourceID, status string,
	metadata map[string]any, ip, userAgent string,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor,
		Action:       action,
		ResourceType: ResourceTypeWebsite,
		ResourceID:   resourceID,
		IPAddress:    ip,
		UserAgent:    userAgent,
		Status:       status,
		Metadata:     metadata,
	})
}

// UpdateActor identifies who asked, for the audit trail.
type UpdateActor struct {
	Actor     string
	IPAddress string
	UserAgent string
}

// Update applies a website's mutable settings.
//
// Most of them are records the host never reads. .htaccess is not: it is a
// line in the Apache vhost, so changing it queues the same rewrite every other
// configuration change goes through. A setting stored and never applied would
// be a switch in the panel that does nothing to the site.
func (s *Service) Update(ctx context.Context, id string, params UpdateParams,
	actor UpdateActor,
) (Website, error) {
	site, err := s.repo.Update(ctx, id, params)
	if err != nil {
		return Website{}, err
	}

	// Only the settings the host actually reads queue a rewrite. Renaming a
	// site changes a label in the panel and nothing on the machine.
	if params.AllowOverride == nil && params.NginxDirectives == nil && params.DocumentRoot == nil {
		return site, nil
	}

	job, err := s.queueVhostUpdate(ctx, site, actor.Actor)
	if err != nil {
		return Website{}, err
	}

	metadata := map[string]any{
		"domain": site.PrimaryDomain,
		"job_id": job.ID,
	}
	if params.AllowOverride != nil {
		metadata["allow_override"] = *params.AllowOverride
	}
	if params.NginxDirectives != nil {
		// The length and whether it is now empty, not the directives
		// themselves. An audit entry is read by more people than the setting
		// is, and a server configuration block pasted into it would be copied
		// into support tickets and log aggregators along with whatever the
		// operator put in it.
		metadata["nginx_directives_bytes"] = len(*params.NginxDirectives)
		metadata["nginx_directives_cleared"] = *params.NginxDirectives == ""
	}

	s.record(ctx, actor.Actor, ActionWebsiteUpdate, site.ID, audit.StatusSuccess,
		metadata, actor.IPAddress, actor.UserAgent)

	return site, nil
}
