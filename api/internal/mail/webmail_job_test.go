package mail

import (
	"context"
	"testing"

	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/websites"
)

// Webmail is recorded as installed when it is, and not before.
//
// The install used to be sent straight to the Agent and recorded the moment the
// Agent accepted it. A download that failed, or a release whose checksum did
// not match, left the panel reporting webmail on a site serving nothing.

type oneWebsite struct{ ref WebsiteRef }

func (w oneWebsite) LookupForMail(context.Context, string) (WebsiteRef, error) {
	return w.ref, nil
}

func webmailFixture(t *testing.T) (*Service, *Repository, string, string, context.Context) {
	t.Helper()
	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "webmail-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	repo := NewRepository(deps.Pool)
	if _, err := repo.SaveSettings(ctx, Settings{
		ServerID: server.ID, Enabled: true, Hostname: "mail.example.com",
		SpamRejectScore: 15, MaxMessageMB: 25,
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	// A real row: the recorded webmail site is a foreign key to it.
	site, err := websites.NewRepository(deps.Pool).Create(ctx, websites.CreateParams{
		ServerID: server.ID, Name: "webmail.example.com", PrimaryDomain: "webmail.example.com",
		DocumentRoot: "/var/www/webmail.example.com/public", SystemUser: "web_webmail",
	})
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	service := NewService(ServiceOptions{
		Repo: repo,
		Websites: oneWebsite{ref: WebsiteRef{
			ID: site.ID, Domain: "webmail.example.com",
			SystemUser: "web_webmail", DocumentRoot: "/var/www/webmail.example.com/public",
		}},
		Jobs:     jobs.NewRepository(deps.Pool),
		ServerID: server.ID,
	})
	return service, repo, site.ID, server.ID, ctx
}

func TestAWebmailInstallIsQueuedAndNotYetRecorded(t *testing.T) {
	service, repo, site, serverID, ctx := webmailFixture(t)

	job, err := service.InstallWebmail(ctx, Actor{}, site)
	if err != nil {
		t.Fatalf("InstallWebmail: %v", err)
	}
	if job.Type != jobs.TypeWebmailInstall {
		t.Fatalf("job type = %q, want %q", job.Type, jobs.TypeWebmailInstall)
	}
	if job.ResourceID == nil || *job.ResourceID != site {
		t.Fatalf("the job does not name the website: %v", job.ResourceID)
	}
	// The payload is what the Agent operation reads.
	for _, key := range []string{"document_root", "owner", "domain", "imap_host", "smtp_host"} {
		if _, ok := job.Payload[key]; !ok {
			t.Errorf("the job payload has no %s", key)
		}
	}
	if job.Payload["imap_host"] != "mail.example.com" {
		t.Errorf("webmail is not pointed at this host's mail server: %v", job.Payload["imap_host"])
	}

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.WebmailVersion != "" || settings.WebmailWebsiteID != "" {
		t.Fatalf("webmail was recorded as installed before anything was installed: %+v", settings)
	}
}

func TestWebmailIsRecordedWhenItsJobSucceeds(t *testing.T) {
	service, repo, site, serverID, ctx := webmailFixture(t)
	job, err := service.InstallWebmail(ctx, Actor{}, site)
	if err != nil {
		t.Fatalf("InstallWebmail: %v", err)
	}

	service.JobFinished(ctx, job, jobs.StateSuccess, nil, "")

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.WebmailWebsiteID != site || settings.WebmailVersion != webmailVersion {
		t.Fatalf("a successful install was not recorded: %+v", settings)
	}
}

func TestAFailedWebmailInstallLeavesTheRecordAsItWas(t *testing.T) {
	service, repo, site, serverID, ctx := webmailFixture(t)
	job, err := service.InstallWebmail(ctx, Actor{}, site)
	if err != nil {
		t.Fatalf("InstallWebmail: %v", err)
	}

	service.JobFinished(ctx, job, jobs.StateFailed, nil,
		"the webmail download did not match its expected checksum")

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.WebmailVersion != "" {
		t.Fatalf("a failed install was recorded as installed: %+v", settings)
	}
}

func TestOtherJobsDoNotTouchWebmail(t *testing.T) {
	service, repo, site, serverID, ctx := webmailFixture(t)
	service.JobFinished(ctx, jobs.Job{Type: jobs.TypeMailInstall, ResourceID: &site},
		jobs.StateSuccess, nil, "")

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.WebmailWebsiteID != "" {
		t.Fatalf("a mail-server install was recorded as webmail: %+v", settings)
	}
}
