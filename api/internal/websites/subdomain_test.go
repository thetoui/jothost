package websites_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// activeParent creates a website and moves it to active, which is the state a
// subdomain can be added to.
func activeParent(t *testing.T, service *websites.Service, repo *websites.Repository,
	domain string,
) websites.Website {
	t.Helper()
	ctx := context.Background()

	result, err := service.Create(ctx, websites.CreateRequest{Domain: domain})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	if err := repo.SetStatus(ctx, result.Website.ID, websites.StatusActive); err != nil {
		t.Fatalf("activate parent: %v", err)
	}

	parent, err := repo.Get(ctx, result.Website.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	return parent
}

func TestCreateSubdomainRecordsASiteBeneathItsParent(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "example.test")

	result, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop",
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}

	sub := result.Website
	if sub.PrimaryDomain != "shop.example.test" {
		t.Fatalf("domain = %q, want shop.example.test", sub.PrimaryDomain)
	}
	if !sub.IsSubdomain() || *sub.ParentWebsiteID != parent.ID {
		t.Fatalf("the subdomain is not linked to its parent: %+v", sub.ParentWebsiteID)
	}
	// Nested by default: the files belong with the parent's, and beside its
	// content rather than inside it — inside would mean the parent serves the
	// subdomain's files at its own URLs.
	if sub.DocumentRoot != "/var/www/example.test/shop.example.test/public" {
		t.Fatalf("document root = %q, want it nested beside the parent's content",
			sub.DocumentRoot)
	}
	if strings.HasPrefix(sub.DocumentRoot, parent.DocumentRoot) {
		t.Fatalf("document root %q is inside the parent's document root",
			sub.DocumentRoot)
	}
	// Inheriting by default, so the parent's file manager and PHP reach it.
	if sub.SystemUser != parent.SystemUser {
		t.Fatalf("system user = %q, want the parent's %q", sub.SystemUser, parent.SystemUser)
	}
	if !sub.InheritsSystemUser() || !sub.InheritsPHPPool() {
		t.Fatal("a subdomain should inherit its parent's account and pool by default")
	}
	// A subdomain is a website, so it has its own primary domain row: that row
	// is what stops the same name being attached to another site as an alias.
	if len(sub.Domains) != 1 || sub.Domains[0].Type != websites.DomainPrimary {
		t.Fatalf("expected one primary domain row, got %+v", sub.Domains)
	}
}

func TestCreateSubdomainSupportsIsolatedAndDedicated(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "isolated.test")

	result, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID:         parent.ID,
		Name:             "app",
		DocumentRootMode: validate.DocumentRootIsolated,
		PHPPoolMode:      validate.PHPPoolDedicated,
		SystemUserMode:   validate.SystemUserDedicated,
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}

	sub := result.Website
	if sub.DocumentRoot != "/var/www/app.isolated.test/public" {
		t.Fatalf("document root = %q, want a top-level directory", sub.DocumentRoot)
	}
	// Its own account is what isolates it: a compromised subdomain cannot read
	// the parent's files.
	if sub.SystemUser == parent.SystemUser {
		t.Fatal("a dedicated subdomain must not share its parent's account")
	}
	if err := validate.SystemUser(sub.SystemUser); err != nil {
		t.Fatalf("derived account %q is invalid: %v", sub.SystemUser, err)
	}
}

// A dedicated account with an inherited pool runs PHP as one user over files
// owned by another: every write fails, and it looks like a broken application.
func TestCreateSubdomainRefusesIncoherentOwnership(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "ownership.test")

	_, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID:       parent.ID,
		Name:           "app",
		PHPPoolMode:    validate.PHPPoolInherit,
		SystemUserMode: validate.SystemUserDedicated,
	})
	if !errors.Is(err, websites.ErrInvalidState) {
		t.Fatalf("expected the combination to be refused, got %v", err)
	}
}

func TestCreateSubdomainSupportsAWildcard(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "wild.test")

	result, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "*",
	})
	if err != nil {
		t.Fatalf("create wildcard subdomain: %v", err)
	}

	if result.Website.PrimaryDomain != "*.wild.test" {
		t.Fatalf("domain = %q, want *.wild.test", result.Website.PrimaryDomain)
	}
	// The asterisk is a glob to every tool that later walks this directory, so
	// it must not reach the filesystem.
	if strings.Contains(result.Website.DocumentRoot, "*") {
		t.Fatalf("document root %q contains a wildcard", result.Website.DocumentRoot)
	}
	if !strings.HasPrefix(result.Website.DocumentRoot, "/var/www/wild.test/") {
		t.Fatalf("document root = %q, want it nested under the parent",
			result.Website.DocumentRoot)
	}
}

// The label is a label, not a hostname. Deriving the full name from the
// parent's own record is what stops a site being created under a domain the
// parent does not own — a label that looks like someone else's domain still
// lands beneath the parent.
func TestCreateSubdomainAlwaysLandsBeneathTheParent(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "owned.test")

	result, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop.other.test",
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}
	if result.Website.PrimaryDomain != "shop.other.test.owned.test" {
		t.Fatalf("domain = %q: a label that looks like another domain must still "+
			"land under the parent", result.Website.PrimaryDomain)
	}
}

func TestCreateSubdomainRefusesUnusableNames(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "refuses.test")

	invalid := []struct{ name, why string }{
		{"", "no name at all"},
		{"..", "empty labels"},
		{"sh op", "a space, which would close the server_name directive"},
		{"shop\nserver{}", "a newline, which would open a directive"},
		{"shop.*", "a wildcard that is not the leading label"},
		{"-bad", "a leading dash, which is not a valid label"},
		{strings.Repeat("a", 64), "a label past the 63-character limit"},
	}
	for _, tc := range invalid {
		if _, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
			ParentID: parent.ID,
			Name:     tc.name,
		}); err == nil {
			t.Errorf("name %q was accepted: %s", tc.name, tc.why)
		}
	}
}

func TestCreateSubdomainRefusesASecondLevel(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "levels.test")

	first, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop",
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}
	if err := repo.SetStatus(ctx, first.Website.ID, websites.StatusActive); err != nil {
		t.Fatalf("activate subdomain: %v", err)
	}

	_, err = service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: first.Website.ID,
		Name:     "dev",
	})
	if !errors.Is(err, websites.ErrNestedSubdomain) {
		t.Fatalf("expected ErrNestedSubdomain, got %v", err)
	}

	// The name is still reachable, as one subdomain with a dotted label.
	deeper, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "dev.shop",
	})
	if err != nil {
		t.Fatalf("create a deeper name under the top-level site: %v", err)
	}
	if deeper.Website.PrimaryDomain != "dev.shop.levels.test" {
		t.Fatalf("domain = %q, want dev.shop.levels.test", deeper.Website.PrimaryDomain)
	}
}

func TestCreateSubdomainRefusesAParentThatIsNotReady(t *testing.T) {
	service, _, _ := newFixture(t)
	ctx := context.Background()

	// Straight from Create, so it is still "creating": there is no directory
	// for a nested subdomain to live in yet.
	parent, err := service.Create(ctx, websites.CreateRequest{Domain: "pending.test"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}

	_, err = service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.Website.ID,
		Name:     "shop",
	})
	if !errors.Is(err, websites.ErrParentNotReady) {
		t.Fatalf("expected ErrParentNotReady, got %v", err)
	}
}

// Deleting the parent would take a nested subdomain's files with it while its
// vhost stayed live, and the panel would have no record of the name nginx is
// still serving.
func TestDeleteRefusesAWebsiteThatStillHasSubdomains(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "busy.test")
	sub, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop",
	})
	if err != nil {
		t.Fatalf("create subdomain: %v", err)
	}

	_, err = service.Delete(ctx, websites.DeleteRequest{WebsiteID: parent.ID})
	if !errors.Is(err, websites.ErrHasSubdomains) {
		t.Fatalf("expected ErrHasSubdomains, got %v", err)
	}
	// The refusal names what is in the way, so the user knows what to remove.
	if !strings.Contains(err.Error(), "shop.busy.test") {
		t.Fatalf("the error should name the subdomain: %v", err)
	}

	// The parent is still deletable once the subdomain is gone.
	if _, err := service.DeleteSubdomain(ctx, websites.DeleteRequest{
		WebsiteID: sub.Website.ID,
	}); err != nil {
		t.Fatalf("delete subdomain: %v", err)
	}
	if err := repo.Delete(ctx, sub.Website.ID); err != nil {
		t.Fatalf("remove subdomain row: %v", err)
	}
	if _, err := service.Delete(ctx, websites.DeleteRequest{WebsiteID: parent.ID}); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
}

// An inherited account belongs to the parent. Removing it with the subdomain
// would leave the parent's files owned by a user that no longer exists, and
// the parent serving 403 to every visitor.
func TestDeleteSubdomainKeepsAnInheritedAccount(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "accounts.test")

	inherited, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shared",
	})
	if err != nil {
		t.Fatalf("create inheriting subdomain: %v", err)
	}
	job, err := service.DeleteSubdomain(ctx, websites.DeleteRequest{
		WebsiteID: inherited.Website.ID,
	})
	if err != nil {
		t.Fatalf("delete inheriting subdomain: %v", err)
	}
	if removeUser(t, job) {
		t.Fatal("deleting a subdomain must not remove the account it shares with its parent")
	}

	owned, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID:       parent.ID,
		Name:           "own",
		PHPPoolMode:    validate.PHPPoolDedicated,
		SystemUserMode: validate.SystemUserDedicated,
	})
	if err != nil {
		t.Fatalf("create owning subdomain: %v", err)
	}
	job, err = service.DeleteSubdomain(ctx, websites.DeleteRequest{
		WebsiteID: owned.Website.ID,
	})
	if err != nil {
		t.Fatalf("delete owning subdomain: %v", err)
	}
	if !removeUser(t, job) {
		t.Fatal("a subdomain with its own account must take it with it")
	}
}

// removeUser reads the remove_user flag from a delete job's payload.
func removeUser(t *testing.T, job jobs.Job) bool {
	t.Helper()
	flag, ok := job.Payload["remove_user"].(bool)
	if !ok {
		t.Fatalf("remove_user missing from the payload: %+v", job.Payload)
	}
	return flag
}

func TestDeleteSubdomainRefusesATopLevelWebsite(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "toplevel.test")

	_, err := service.DeleteSubdomain(ctx, websites.DeleteRequest{WebsiteID: parent.ID})
	if !errors.Is(err, websites.ErrNotSubdomain) {
		t.Fatalf("expected ErrNotSubdomain, got %v", err)
	}
}

// The websites listing is what the sites page shows. A site with twenty
// subdomains would otherwise fill it on its own.
func TestListLeavesSubdomainsOutUnlessAsked(t *testing.T) {
	service, repo, _ := newFixture(t)
	ctx := context.Background()

	parent := activeParent(t, service, repo, "listing.test")
	if _, err := service.CreateSubdomain(ctx, websites.CreateSubdomainRequest{
		ParentID: parent.ID,
		Name:     "shop",
	}); err != nil {
		t.Fatalf("create subdomain: %v", err)
	}

	top, err := repo.List(ctx, websites.ListParams{})
	if err != nil {
		t.Fatalf("list websites: %v", err)
	}
	for _, site := range top {
		if site.IsSubdomain() {
			t.Fatalf("the default listing included the subdomain %q", site.PrimaryDomain)
		}
	}

	all, err := repo.List(ctx, websites.ListParams{IncludeSubdomains: true})
	if err != nil {
		t.Fatalf("list websites with subdomains: %v", err)
	}
	if len(all) <= len(top) {
		t.Fatal("include_subdomains returned no more rows than the default listing")
	}

	// The parent carries its subdomains, so one request draws the whole card.
	loaded, err := repo.Get(ctx, parent.ID)
	if err != nil {
		t.Fatalf("get parent: %v", err)
	}
	if len(loaded.Subdomains) != 1 || loaded.Subdomains[0].PrimaryDomain != "shop.listing.test" {
		t.Fatalf("the parent should carry its subdomains, got %+v", loaded.Subdomains)
	}
}
