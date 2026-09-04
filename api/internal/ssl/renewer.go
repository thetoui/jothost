package ssl

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/logger"
)

// Renewal timings.
const (
	// RenewWithin is how close to expiry a certificate is renewed.
	//
	// Thirty days is the Let's Encrypt convention and exists for a reason: a
	// 90-day certificate renewed at 30 leaves two further chances to succeed
	// before anything is actually broken.
	RenewWithin = 30 * 24 * time.Hour

	// RetryAfter is the gap before a failed renewal is tried again.
	//
	// Without it a persistently failing certificate is retried on every sweep,
	// which for Let's Encrypt means walking into a rate limit and turning a
	// fixable problem into a blocked one.
	RetryAfter = 6 * time.Hour

	// SweepInterval is how often certificates are checked.
	//
	// Twice a day: the renewal window is 30 days wide, so nothing is gained by
	// checking more often, and a certificate issued between sweeps still has
	// weeks of slack.
	SweepInterval = 12 * time.Hour
)

// Notifier is told about a certificate running out of time.
//
// The renewer is where this belongs rather than a loop of its own: it already
// sweeps every certificate on a cadence and already knows which ones it could
// not renew, which is exactly the set worth telling somebody about.
type Notifier interface {
	SSLExpiring(ctx context.Context, domain string, daysRemaining int)
}

// Renewer keeps certificates from expiring.
//
// It is the difference between a panel that issues certificates and one that
// keeps sites working: an unattended certificate expires in 90 days, and the
// failure is total — every visitor gets a browser warning at once.
type Renewer struct {
	repo     *Repository
	websites *websites.Repository
	service  *Service
	log      *slog.Logger
	notifier Notifier
	now      func() time.Time
}

// NewRenewer builds a Renewer.
func NewRenewer(repo *Repository, sites *websites.Repository, service *Service, log *slog.Logger) *Renewer {
	if log == nil {
		log = slog.Default()
	}
	return &Renewer{
		repo: repo, websites: sites, service: service, log: log,
		now: time.Now,
	}
}

// Run sweeps at startup and then on an interval.
func (r *Renewer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = SweepInterval
	}

	r.Sweep(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Sweep(ctx)
		}
	}
}

// Sweep renews everything due and reports how many it queued.
func (r *Renewer) Sweep(ctx context.Context) int {
	// Statuses move only because time passed, so they are refreshed first:
	// otherwise a certificate that expired overnight still reads "valid" until
	// something else touches it.
	if moved, err := r.repo.RefreshStatuses(ctx); err != nil {
		r.log.Error("failed to refresh certificate statuses", logger.KeyError, err.Error())
	} else if moved > 0 {
		r.log.Info("certificate statuses updated", "count", moved)
	}

	// Whoever is going to be told about a certificate running out of time is
	// told here, before anything is renewed. A renewal that is about to succeed
	// costs one notification that says thirty days remain; a renewal that keeps
	// failing is the case this exists for, and it would otherwise be silent
	// right up to the morning every visitor sees a warning.
	r.warnAboutExpiry(ctx)

	due, err := r.repo.DueForRenewal(ctx, RenewWithin, RetryAfter)
	if err != nil {
		r.log.Error("failed to find renewable certificates", logger.KeyError, err.Error())
		return 0
	}
	if len(due) == 0 {
		return 0
	}

	queued := 0
	for _, certificate := range due {
		site, err := r.websites.Get(ctx, certificate.WebsiteID)
		if err != nil {
			if errors.Is(err, websites.ErrNotFound) {
				// The site is gone; its certificate row goes with it rather
				// than being renewed forever for nothing.
				if deleteErr := r.repo.Delete(ctx, certificate.WebsiteID); deleteErr != nil {
					r.log.Error("failed to remove a certificate for a deleted website",
						"website_id", certificate.WebsiteID, logger.KeyError, deleteErr.Error())
				}
				continue
			}
			r.log.Error("failed to read a website for renewal",
				"website_id", certificate.WebsiteID, logger.KeyError, err.Error())
			continue
		}

		if site.Status != websites.StatusActive {
			// A site mid-change has no stable vhost to rewrite. It will be
			// picked up by the next sweep, and there are weeks of slack.
			continue
		}

		// Actor is empty: this was not a person's request, and attributing it
		// to one in the audit trail would be a lie.
		job, err := r.service.queueRenewal(ctx, site, certificate, Actor{})
		if err != nil {
			r.log.Error("failed to queue an automatic renewal",
				"website_id", certificate.WebsiteID, logger.KeyError, err.Error())
			continue
		}

		queued++
		r.log.Info("queued an automatic certificate renewal",
			"domain", site.PrimaryDomain,
			"job_id", job.ID,
			"expires_at", expiryString(certificate))
	}

	if queued > 0 {
		r.log.Info("automatic certificate renewal swept", "queued", queued)
	}
	return queued
}

func expiryString(certificate Certificate) string {
	if certificate.ExpiresAt == nil {
		return "unknown"
	}
	return certificate.ExpiresAt.UTC().Format(time.RFC3339)
}

// JobFinished reconciles a certificate with the outcome of its job.
//
// It satisfies jobs.Observer. Jobs for other resources are ignored, so this can
// be registered alongside the websites and PHP reconcilers.
func (r *Renewer) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	result map[string]any, failure string,
) {
	switch job.Type {
	case jobs.TypeSSLIssue, jobs.TypeSSLRenew, jobs.TypeSSLRevoke:
	default:
		return
	}

	websiteID, _ := job.Payload["website_id"].(string)
	if websiteID == "" {
		return
	}

	log := r.log.With("website_id", websiteID, "job_id", job.ID, "type", job.Type)

	if state != jobs.StateSuccess {
		// The failure message is the Agent's own, written for display.
		if err := r.repo.SetStatus(ctx, websiteID, StatusFailed, failure); err != nil {
			log.Error("failed to mark a certificate as failed", logger.KeyError, err.Error())
		}
		log.Warn("certificate job failed", "reason", failure)
		return
	}

	if job.Type == jobs.TypeSSLRevoke {
		if err := r.repo.Delete(ctx, websiteID); err != nil {
			log.Error("failed to remove a revoked certificate", logger.KeyError, err.Error())
		}
		if err := r.repo.SetWebsiteSSL(ctx, websiteID, false, false); err != nil {
			log.Error("failed to clear the website ssl flag", logger.KeyError, err.Error())
		}
		log.Info("certificate revoked")
		return
	}

	// What the Agent actually produced is recorded, not what was asked for: the
	// expiry in particular is the certificate's own and cannot be predicted.
	issued, ok := issuedFrom(result)
	if !ok {
		log.Error("certificate job succeeded but reported no usable certificate")
		if err := r.repo.SetStatus(ctx, websiteID, StatusFailed,
			"the certificate was issued but could not be read back"); err != nil {
			log.Error("failed to mark a certificate as failed", logger.KeyError, err.Error())
		}
		return
	}
	issued.WebsiteID = websiteID

	if err := r.repo.MarkIssued(ctx, issued, time.Now()); err != nil {
		log.Error("failed to record an issued certificate", logger.KeyError, err.Error())
		return
	}

	redirect, _ := job.Payload["redirect_to_https"].(bool)
	if err := r.repo.SetWebsiteSSL(ctx, websiteID, true, redirect); err != nil {
		log.Error("failed to record the website ssl flag", logger.KeyError, err.Error())
	}

	log.Info("certificate reconciled", "expires_at", issued.ExpiresAt.UTC().Format(time.RFC3339))
}

// issuedFrom reads the Agent's result into a record.
//
// A result missing its expiry is refused rather than stored with a zero date:
// a certificate the panel thinks expired in year zero would be renewed on every
// sweep forever.
func issuedFrom(result map[string]any) (IssuedParams, bool) {
	if result == nil {
		return IssuedParams{}, false
	}

	expiresAt, err := time.Parse(time.RFC3339, stringField(result, "expires_at"))
	if err != nil {
		return IssuedParams{}, false
	}
	issuedAt, err := time.Parse(time.RFC3339, stringField(result, "issued_at"))
	if err != nil {
		issuedAt = time.Now()
	}

	domains := []string{}
	if raw, ok := result["domains"].([]any); ok {
		for _, entry := range raw {
			if name, ok := entry.(string); ok {
				domains = append(domains, name)
			}
		}
	}
	if len(domains) == 0 {
		if domain := stringField(result, "domain"); domain != "" {
			domains = []string{domain}
		}
	}

	return IssuedParams{
		Provider:        stringField(result, "provider"),
		Domains:         domains,
		CertificatePath: stringField(result, "certificate_path"),
		PrivateKeyPath:  stringField(result, "private_key_path"),
		Fingerprint:     stringField(result, "fingerprint"),
		Issuer:          stringField(result, "issuer"),
		IssuedAt:        issuedAt,
		ExpiresAt:       expiresAt,
	}, true
}

func stringField(result map[string]any, key string) string {
	value, _ := result[key].(string)
	return value
}

// SetNotifier attaches a notifier after construction.
//
// After, rather than as a constructor argument, because the renewer is built
// before the notification service in the server's wiring and reordering them
// would mean the notification service could not depend on anything the renewer
// needs. It is called once, at startup, before Run.
func (r *Renewer) SetNotifier(notifier Notifier) { r.notifier = notifier }

// warnAboutExpiry tells the notifier about certificates running out of time.
//
// Every certificate is offered, on every sweep, and the notifier's own dedupe
// keys decide what is actually sent — one message per certificate per
// threshold, rather than one a day for a month. Deciding that here instead
// would mean two places knowing the thresholds.
func (r *Renewer) warnAboutExpiry(ctx context.Context) {
	if r.notifier == nil {
		return
	}

	certificates, err := r.repo.List(ctx)
	if err != nil {
		r.log.Error("failed to read certificates to warn about expiry",
			logger.KeyError, err.Error())
		return
	}

	now := r.now()
	for _, certificate := range certificates {
		days := certificate.DaysRemaining(now)
		if days == nil {
			continue
		}
		domain := certificate.PrimaryDomain
		if domain == "" && len(certificate.Domains) > 0 {
			domain = certificate.Domains[0]
		}
		if domain == "" {
			continue
		}
		r.notifier.SSLExpiring(ctx, domain, *days)
	}
}
