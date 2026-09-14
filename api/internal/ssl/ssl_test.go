package ssl_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/ssl"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

type fixture struct {
	service  *ssl.Service
	repo     *ssl.Repository
	websites *websites.Repository
	jobs     *jobs.Repository
	renewer  *ssl.Renewer
	siteSvc  *websites.Service
	pool     *pgxpool.Pool
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	serverID := registerServer(t, deps.Pool)

	repo := ssl.NewRepository(deps.Pool)
	websiteRepo := websites.NewRepository(deps.Pool)
	phpRepo := php.NewRepository(deps.Pool)
	jobRepo := jobs.NewRepository(deps.Pool)

	service := ssl.NewService(ssl.ServiceOptions{
		Repository: repo,
		Websites:   websiteRepo,
		PHP:        phpRepo,
		Jobs:       jobRepo,
	})

	return &fixture{
		service:  service,
		repo:     repo,
		websites: websiteRepo,
		jobs:     jobRepo,
		renewer:  ssl.NewRenewer(repo, websiteRepo, service, nil),
		pool:     deps.Pool,
		siteSvc: websites.NewService(websites.ServiceOptions{
			Repository: websiteRepo,
			Jobs:       jobRepo,
			ServerID:   serverID,
		}),
	}
}

func registerServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO servers (hostname, status)
		VALUES ('ssl-test-host', 'online')
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return id
}

// activeSite creates a website and marks it active, which is the state a real
// site is in by the time anyone issues a certificate for it.
func (f *fixture) activeSite(t *testing.T, domain string) websites.Website {
	t.Helper()
	ctx := context.Background()

	created, err := f.siteSvc.Create(ctx, websites.CreateRequest{Domain: domain})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	if err := f.websites.SetStatus(ctx, created.Website.ID, websites.StatusActive); err != nil {
		t.Fatalf("activate website: %v", err)
	}

	site, err := f.websites.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	return site
}

// issued records a certificate as the Agent would have reported it.
func (f *fixture) issued(t *testing.T, websiteID string, expiresIn time.Duration) {
	t.Helper()
	ctx := context.Background()

	if _, err := f.repo.Upsert(ctx, ssl.UpsertParams{
		WebsiteID: websiteID,
		Provider:  ssl.ProviderSelfSigned,
		Domains:   []string{"example.test"},
		AutoRenew: true,
		Status:    ssl.StatusIssuing,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	now := time.Now()
	err := f.repo.MarkIssued(ctx, ssl.IssuedParams{
		WebsiteID:       websiteID,
		Provider:        ssl.ProviderSelfSigned,
		Domains:         []string{"example.test"},
		CertificatePath: "/etc/jothost/ssl/example.test/fullchain.pem",
		PrivateKeyPath:  "/etc/jothost/ssl/example.test/privkey.pem",
		Fingerprint:     "AA:BB",
		Issuer:          "example.test",
		IssuedAt:        now,
		ExpiresAt:       now.Add(expiresIn),
	}, now)
	if err != nil {
		t.Fatalf("mark issued: %v", err)
	}
}

// ---------------------------------------------------------------- issuance

func TestIssueRecordsTheCertificateAndQueuesTheWork(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")

	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderSelfSigned,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	job := issued.Job

	if job.Type != jobs.TypeSSLIssue {
		t.Fatalf("job type = %q, want ssl.issue", job.Type)
	}

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get certificate: %v", err)
	}
	// Issuing, not valid: nothing has handshaken yet, and a panel that claims
	// HTTPS works before that is worse than one that says nothing.
	if certificate.Status != ssl.StatusIssuing {
		t.Fatalf("status = %q, want issuing", certificate.Status)
	}
}

// A visitor reaching www on a certificate naming only the apex gets a browser
// warning indistinguishable from an attack.
func TestIssueCoversAliasesButNotRedirects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	if _, _, err := f.siteSvc.AddDomain(ctx, websites.AddDomainRequest{
		WebsiteID: site.ID,
		Domain:    "www.example.test",
		Type:      websites.DomainAlias,
	}); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	if _, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderSelfSigned,
	}); err != nil {
		t.Fatalf("issue: %v", err)
	}

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(certificate.Domains) != 2 {
		t.Fatalf("domains = %v, want the apex and its alias", certificate.Domains)
	}
}

func TestIssueRejectsAnUnknownProvider(t *testing.T) {
	f := newFixture(t)
	site := f.activeSite(t, "example.test")

	_, err := f.service.Issue(context.Background(), ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  "acme-corp",
	})
	if !errors.Is(err, ssl.ErrInvalidProvider) {
		t.Fatalf("= %v, want ErrInvalidProvider", err)
	}
}

// A site mid-provision has no vhost to rewrite and one mid-delete is going
// away; issuing for either produces work that cannot succeed.
func TestIssueRefusesASiteThatIsNotReady(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	created, err := f.siteSvc.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err = f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: created.Website.ID,
		Provider:  ssl.ProviderSelfSigned,
	})
	if !errors.Is(err, ssl.ErrWebsiteNotReady) {
		t.Fatalf("= %v, want ErrWebsiteNotReady", err)
	}
}

func TestRenewAndRevokeNeedACertificate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	site := f.activeSite(t, "example.test")

	if _, err := f.service.Renew(ctx, site.ID, ssl.Actor{}); !errors.Is(err, ssl.ErrNoCertificate) {
		t.Fatalf("renew without a certificate = %v, want ErrNoCertificate", err)
	}
	if _, err := f.service.Revoke(ctx, site.ID, ssl.Actor{}); !errors.Is(err, ssl.ErrNoCertificate) {
		t.Fatalf("revoke without a certificate = %v, want ErrNoCertificate", err)
	}
}

// ------------------------------------------------------------- lifecycle

// The status comes from the certificate's own expiry, not from the fact that
// issuance succeeded: a certificate can be issued already inside its renewal
// window, and calling that "valid" would hide it from the sweep.
func TestStatusIsDerivedFromTheExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	for _, tc := range []struct {
		name      string
		expiresIn time.Duration
		want      string
	}{
		{"comfortable", 60 * 24 * time.Hour, ssl.StatusValid},
		{"inside the warning window", 10 * 24 * time.Hour, ssl.StatusExpiring},
		{"already lapsed", -24 * time.Hour, ssl.StatusExpired},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := f.activeSite(t, "example.test")
			f.issued(t, site.ID, tc.expiresIn)

			certificate, err := f.repo.Get(ctx, site.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if certificate.Status != tc.want {
				t.Fatalf("status = %q, want %q", certificate.Status, tc.want)
			}

			// Clean up so the next case starts from an empty table: the unique
			// index means one certificate per website.
			if err := f.websites.Delete(ctx, site.ID); err != nil {
				t.Fatalf("cleanup: %v", err)
			}
		})
	}
}

func TestDaysRemainingCountsPastExpiry(t *testing.T) {
	now := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	expired := now.Add(-3 * 24 * time.Hour)

	certificate := ssl.Certificate{ExpiresAt: &expired}
	days := certificate.DaysRemaining(now)
	if days == nil || *days != -3 {
		t.Fatalf("days remaining = %v, want -3", days)
	}
}

// ----------------------------------------------------------------- renewal

func TestSweepQueuesRenewalInsideTheWindow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	// Inside the 30-day renewal window.
	f.issued(t, site.ID, 10*24*time.Hour)

	if queued := f.renewer.Sweep(ctx); queued != 1 {
		t.Fatalf("swept %d certificates, want 1", queued)
	}

	found, err := f.jobs.List(ctx, jobs.ListParams{ResourceType: "website", ResourceID: site.ID})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}

	renewals := 0
	for _, job := range found {
		if job.Type == jobs.TypeSSLRenew {
			renewals++
		}
	}
	if renewals != 1 {
		t.Fatalf("queued %d renewals, want 1", renewals)
	}
}

func TestSweepLeavesAComfortableCertificateAlone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 80*24*time.Hour)

	if queued := f.renewer.Sweep(ctx); queued != 0 {
		t.Fatalf("swept %d certificates, want 0", queued)
	}
}

// Without a retry gap a persistently failing certificate is retried on every
// sweep, which for Let's Encrypt means walking into a rate limit.
func TestSweepDoesNotRetryImmediately(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 10*24*time.Hour)

	if queued := f.renewer.Sweep(ctx); queued != 1 {
		t.Fatalf("first sweep queued %d, want 1", queued)
	}
	// The attempt was just recorded, so a second sweep must hold off.
	if queued := f.renewer.Sweep(ctx); queued != 0 {
		t.Fatalf("second sweep queued %d, want 0", queued)
	}
}

func TestSweepSkipsAutoRenewalWhenTurnedOff(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 10*24*time.Hour)

	if err := f.repo.SetAutoRenew(ctx, site.ID, false); err != nil {
		t.Fatalf("set auto renew: %v", err)
	}
	if queued := f.renewer.Sweep(ctx); queued != 0 {
		t.Fatalf("swept %d certificates, want 0", queued)
	}
}

// A certificate that is expiring must be reported as such without anyone
// touching it: time passing is the only input.
func TestRefreshStatusesMovesCertificatesOnTime(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 60*24*time.Hour)

	// Backdate the expiry the way the passage of time would.
	if _, err := f.pool.Exec(ctx,
		`UPDATE ssl_certificates SET expires_at = now() + interval '5 days'
		 WHERE website_id = $1::uuid`, site.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if _, err := f.repo.RefreshStatuses(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if certificate.Status != ssl.StatusExpiring {
		t.Fatalf("status = %q, want expiring", certificate.Status)
	}
}

// ----------------------------------------------------------- reconciliation

func TestJobSuccessRecordsWhatTheAgentProduced(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderSelfSigned,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	job := issued.Job

	stored, err := f.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	expires := time.Now().Add(90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	f.renewer.JobFinished(ctx, stored, jobs.StateSuccess, map[string]any{
		"domain":           "example.test",
		"domains":          []any{"example.test"},
		"provider":         ssl.ProviderSelfSigned,
		"certificate_path": "/etc/jothost/ssl/example.test/fullchain.pem",
		"private_key_path": "/etc/jothost/ssl/example.test/privkey.pem",
		"fingerprint":      "AA:BB:CC",
		"issuer":           "example.test",
		"issued_at":        time.Now().UTC().Format(time.RFC3339),
		"expires_at":       expires,
	}, "")

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if certificate.Status != ssl.StatusValid {
		t.Fatalf("status = %q, want valid", certificate.Status)
	}
	if certificate.ExpiresAt == nil {
		t.Fatal("no expiry was recorded")
	}
	if certificate.Fingerprint == nil || *certificate.Fingerprint != "AA:BB:CC" {
		t.Fatalf("fingerprint = %v", certificate.Fingerprint)
	}
}

// A result with no usable expiry is refused rather than stored as year zero,
// which would be renewed on every sweep forever.
func TestJobSuccessWithoutAnExpiryIsAFailure(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderSelfSigned,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	job := issued.Job
	stored, err := f.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	f.renewer.JobFinished(ctx, stored, jobs.StateSuccess,
		map[string]any{"domain": "example.test"}, "")

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if certificate.Status != ssl.StatusFailed {
		t.Fatalf("status = %q, want failed", certificate.Status)
	}
}

func TestJobFailureRecordsTheReason(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	issued, err := f.service.Issue(ctx, ssl.IssueRequest{
		WebsiteID: site.ID,
		Provider:  ssl.ProviderLetsEncrypt,
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	job := issued.Job
	stored, err := f.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	f.renewer.JobFinished(ctx, stored, jobs.StateFailed, nil,
		"the challenge was not reachable")

	certificate, err := f.repo.Get(ctx, site.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if certificate.Status != ssl.StatusFailed {
		t.Fatalf("status = %q, want failed", certificate.Status)
	}
	if certificate.LastError == nil || *certificate.LastError != "the challenge was not reachable" {
		t.Fatalf("last error = %v, want the agent's message", certificate.LastError)
	}
}

func TestRevocationRemovesTheRecord(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 60*24*time.Hour)

	job, err := f.service.Revoke(ctx, site.ID, ssl.Actor{})
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	stored, err := f.jobs.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	f.renewer.JobFinished(ctx, stored, jobs.StateSuccess,
		map[string]any{"revoked": true}, "")

	if _, err := f.repo.Get(ctx, site.ID); !errors.Is(err, ssl.ErrNotFound) {
		t.Fatalf("the certificate record survived revocation: %v", err)
	}
}

// Deleting a website takes its certificate with it, or the panel keeps trying
// to renew a certificate for a site that no longer exists.
func TestDeletingAWebsiteRemovesItsCertificate(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	site := f.activeSite(t, "example.test")
	f.issued(t, site.ID, 60*24*time.Hour)

	if err := f.websites.Delete(ctx, site.ID); err != nil {
		t.Fatalf("delete website: %v", err)
	}

	if _, err := f.repo.Get(ctx, site.ID); !errors.Is(err, ssl.ErrNotFound) {
		t.Fatalf("the certificate outlived its website: %v", err)
	}
}

// The list leads with what runs out first, because that is what needs
// attention; sorting by name buries it.
func TestListOrdersBySoonestExpiry(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	far := f.activeSite(t, "far.test")
	f.issued(t, far.ID, 80*24*time.Hour)
	near := f.activeSite(t, "near.test")
	f.issued(t, near.ID, 5*24*time.Hour)

	certificates, err := f.repo.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(certificates) != 2 {
		t.Fatalf("listed %d certificates, want 2", len(certificates))
	}
	if certificates[0].PrimaryDomain != "near.test" {
		t.Fatalf("first is %q, want the soonest to expire", certificates[0].PrimaryDomain)
	}
}
