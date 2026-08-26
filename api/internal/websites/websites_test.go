package websites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// newFixture returns a service wired to a live database and a registered
// server, which is the shape the API always runs in.
func newFixture(t *testing.T) (*websites.Service, *websites.Repository, *jobs.Repository) {
	t.Helper()

	deps := testsupport.Require(t)
	serverID := registerServer(t, deps.Pool)

	repo := websites.NewRepository(deps.Pool)
	jobRepo := jobs.NewRepository(deps.Pool)

	service := websites.NewService(websites.ServiceOptions{
		Repository: repo,
		Jobs:       jobRepo,
		ServerID:   serverID,
	})
	return service, repo, jobRepo
}

// registerServer inserts the host these websites belong to.
func registerServer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()

	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO servers (hostname, status)
		VALUES ('test-host', 'online')
		RETURNING id::text`).Scan(&id)
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return id
}

func TestCreateRecordsASiteAndQueuesItsWork(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	result, err := service.Create(ctx, websites.CreateRequest{Domain: "Example.TEST"})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}

	// The domain is normalised before storage, so a site is not created twice
	// under two spellings of one name.
	if result.Website.PrimaryDomain != "example.test" {
		t.Fatalf("primary domain = %q, want example.test", result.Website.PrimaryDomain)
	}
	// A site is not claimed to work until the Agent says it does.
	if result.Website.Status != websites.StatusCreating {
		t.Fatalf("status = %q, want creating", result.Website.Status)
	}
	if result.Website.DocumentRoot != "/var/www/example.test/public" {
		t.Fatalf("document root = %q, want it under the site root",
			result.Website.DocumentRoot)
	}
	if err := validate.SystemUser(result.Website.SystemUser); err != nil {
		t.Fatalf("derived system user %q is invalid: %v", result.Website.SystemUser, err)
	}

	// The primary domain is written with the site, in the same transaction.
	stored, err := repo.Get(ctx, result.Website.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if len(stored.Domains) != 1 || stored.Domains[0].Type != websites.DomainPrimary {
		t.Fatalf("domains = %+v, want one primary", stored.Domains)
	}

	queued, err := jobRepo.Get(ctx, result.Job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if queued.Type != jobs.TypeWebsiteCreate {
		t.Fatalf("job type = %q, want website.create", queued.Type)
	}
	if queued.Status != jobs.StatePending {
		t.Fatalf("job status = %q, want PENDING", queued.Status)
	}
	if got := queued.Payload["document_root"]; got != result.Website.DocumentRoot {
		t.Fatalf("job payload document_root = %v, want %q",
			got, result.Website.DocumentRoot)
	}
	// The job points at the site so a detail page can show what is happening
	// to it without scanning every payload.
	if queued.ResourceID == nil || *queued.ResourceID != result.Website.ID {
		t.Fatalf("job resource = %v, want the website id", queued.ResourceID)
	}
}

func TestCreateRejectsInvalidDomains(t *testing.T) {
	service, _, _ := newFixture(t)

	for _, domain := range []string{
		"",
		"localhost",             // a single label never matches a real request
		"exa mple.test",         // whitespace
		"example.test;rm -rf /", // a shell payload
		"../../etc/passwd",      // a path pretending to be a name
		"-example.test",         // a label may not start with a hyphen
	} {
		if _, err := service.Create(context.Background(),
			websites.CreateRequest{Domain: domain}); err == nil {
			t.Fatalf("domain %q must be rejected", domain)
		}
	}
}

// SSL is Phase 6. Accepting the flag and quietly ignoring it would leave a
// user believing their site is encrypted when it is served over plain HTTP.
func TestCreateRefusesSSLRatherThanIgnoringIt(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	_, err := service.Create(ctx, websites.CreateRequest{
		Domain:     "secure.test",
		SSLEnabled: true,
	})
	if !errors.Is(err, websites.ErrSSLUnsupported) {
		t.Fatalf("create with ssl = %v, want ErrSSLUnsupported", err)
	}

	// Nothing may be left behind by a refused request.
	sites, err := repo.List(ctx, websites.ListParams{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(sites) != 0 {
		t.Fatalf("refused create left %d websites behind", len(sites))
	}
}

func TestCreateRejectsADuplicateDomain(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	if _, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"}); err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Two sites answering to one hostname makes the vhost's server_name
	// ambiguous, so the second must be refused rather than shadowing the first.
	_, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if !errors.Is(err, websites.ErrDomainTaken) {
		t.Fatalf("duplicate create = %v, want ErrDomainTaken", err)
	}
}

func TestEachSiteGetsItsOwnSystemUser(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	first, err := service.Create(ctx, websites.CreateRequest{Domain: "one.test"})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := service.Create(ctx, websites.CreateRequest{Domain: "two.test"})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	// A shared account would let a compromised site read its neighbour's files.
	if first.Website.SystemUser == second.Website.SystemUser {
		t.Fatalf("both sites got the system user %q", first.Website.SystemUser)
	}
}

// Two long domains truncate to the same base within useradd's 32-character
// limit; the random suffix is what keeps them apart.
func TestLongSimilarDomainsGetDistinctUsers(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	first, err := service.Create(ctx, websites.CreateRequest{
		Domain: "a-very-long-domain-name-indeed.example",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := service.Create(ctx, websites.CreateRequest{
		Domain: "a-very-long-domain-name-indeed.test",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	if first.Website.SystemUser == second.Website.SystemUser {
		t.Fatalf("truncation collided on %q", first.Website.SystemUser)
	}
	for _, name := range []string{first.Website.SystemUser, second.Website.SystemUser} {
		if len(name) > 32 {
			t.Fatalf("system user %q is %d characters, over useradd's limit",
				name, len(name))
		}
	}
}

func TestDeleteKeepsTheRowUntilTheHostConfirms(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	job, err := service.Delete(ctx, websites.DeleteRequest{WebsiteID: created.Website.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	// The files are still on the host at this point. Removing the row now
	// would strand them with nothing in the panel pointing at them.
	site, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("website was removed before the host confirmed: %v", err)
	}
	if site.Status != websites.StatusDeleting {
		t.Fatalf("status = %q, want deleting", site.Status)
	}

	queued, err := jobRepo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if queued.Type != jobs.TypeWebsiteDelete {
		t.Fatalf("job type = %q, want website.delete", queued.Type)
	}
}

func TestDeleteRefusesTwice(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := service.Delete(ctx,
		websites.DeleteRequest{WebsiteID: created.Website.ID}); err != nil {
		t.Fatalf("first delete: %v", err)
	}

	// A second delete would queue a duplicate job removing an account the
	// first job is already removing.
	_, err = service.Delete(ctx, websites.DeleteRequest{WebsiteID: created.Website.ID})
	if !errors.Is(err, websites.ErrInvalidState) {
		t.Fatalf("second delete = %v, want ErrInvalidState", err)
	}
}

// ------------------------------------------------------------------ domains

func TestAddDomainQueuesAVhostUpdate(t *testing.T) {
	service, _, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	domain, job, err := service.AddDomain(ctx, websites.AddDomainRequest{
		WebsiteID: created.Website.ID,
		Domain:    "WWW.example.test",
		Type:      websites.DomainAlias,
	})
	if err != nil {
		t.Fatalf("add domain: %v", err)
	}
	if domain.Domain != "www.example.test" {
		t.Fatalf("domain = %q, want it normalised", domain.Domain)
	}

	queued, err := jobRepo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if queued.Type != jobs.TypeWebsiteUpdate {
		t.Fatalf("job type = %q, want website.update", queued.Type)
	}

	// The payload carries the full desired alias set, not a delta, so a
	// retried or reordered job still converges on the same vhost.
	aliases, ok := queued.Payload["aliases"].([]any)
	if !ok || len(aliases) != 1 || aliases[0] != "www.example.test" {
		t.Fatalf("payload aliases = %v, want the full set", queued.Payload["aliases"])
	}
}

func TestAddDomainRejectsASecondPrimary(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A second primary would make the certificate subject undecidable later
	// and the server_name ambiguous now.
	_, _, err = service.AddDomain(ctx, websites.AddDomainRequest{
		WebsiteID: created.Website.ID,
		Domain:    "other.test",
		Type:      websites.DomainPrimary,
	})
	if !errors.Is(err, websites.ErrInvalidState) {
		t.Fatalf("second primary = %v, want ErrInvalidState", err)
	}
}

// The Agent can write a redirect vhost but cannot remove a stale one, so the
// type is refused rather than accepted into configuration the panel could
// never take back.
func TestRedirectDomainsAreRefusedForNow(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	_, _, err = service.AddDomain(ctx, websites.AddDomainRequest{
		WebsiteID:  created.Website.ID,
		Domain:     "redirect.test",
		Type:       websites.DomainRedirect,
		RedirectTo: "example.test",
	})
	if !errors.Is(err, websites.ErrRedirectUnsupported) {
		t.Fatalf("redirect domain = %v, want ErrRedirectUnsupported", err)
	}

	// A refused request must leave nothing behind.
	stored, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(stored.Domains) != 1 {
		t.Fatalf("refused redirect left %d domains", len(stored.Domains))
	}
}

func TestRemoveDomainRefusesThePrimary(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	stored, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Removing it would leave a site nothing can reach, with a vhost that has
	// no server_name.
	_, err = service.RemoveDomain(ctx,
		websites.RemoveDomainRequest{DomainID: stored.Domains[0].ID})
	if !errors.Is(err, websites.ErrPrimaryDomain) {
		t.Fatalf("remove primary = %v, want ErrPrimaryDomain", err)
	}
}

// ----------------------------------------------------------- reconciliation

func TestJobSuccessActivatesTheWebsite(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	job, err := jobRepo.Get(ctx, created.Job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	service.JobFinished(ctx, job, jobs.StateSuccess, map[string]any{"reloaded": true}, "")

	site, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	if site.Status != websites.StatusActive {
		t.Fatalf("status = %q, want active", site.Status)
	}
}

func TestJobFailureMarksTheWebsiteFailed(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job, err := jobRepo.Get(ctx, created.Job.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}

	service.JobFinished(ctx, job, jobs.StateFailed, nil, "nginx configuration test failed")

	site, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("get website: %v", err)
	}
	// "failed" rather than "active": a panel that claims a site works when its
	// vhost was never written is worse than one that shows an error.
	if site.Status != websites.StatusFailed {
		t.Fatalf("status = %q, want failed", site.Status)
	}
}

func TestSuccessfulDeleteRemovesTheRow(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	deleteJob, err := service.Delete(ctx,
		websites.DeleteRequest{WebsiteID: created.Website.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	job, err := jobRepo.Get(ctx, deleteJob.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	service.JobFinished(ctx, job, jobs.StateSuccess, nil, "")

	if _, err := repo.Get(ctx, created.Website.ID); !errors.Is(err, websites.ErrNotFound) {
		t.Fatalf("website still present after a confirmed delete: %v", err)
	}
}

// A failed delete keeps the row: the files may still be on the host, and a
// vanished row would leave orphaned directories nobody knows about.
func TestFailedDeleteKeepsTheRowVisible(t *testing.T) {
	service, repo, jobRepo := newFixture(t)
	ctx := context.Background()

	created, err := service.Create(ctx, websites.CreateRequest{Domain: "example.test"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	deleteJob, err := service.Delete(ctx,
		websites.DeleteRequest{WebsiteID: created.Website.ID})
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	job, err := jobRepo.Get(ctx, deleteJob.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	service.JobFinished(ctx, job, jobs.StateFailed, nil, "could not remove the site user")

	site, err := repo.Get(ctx, created.Website.ID)
	if err != nil {
		t.Fatalf("website vanished after a failed delete: %v", err)
	}
	if site.Status != websites.StatusFailed {
		t.Fatalf("status = %q, want failed", site.Status)
	}
}
