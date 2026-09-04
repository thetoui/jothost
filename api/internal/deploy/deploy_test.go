package deploy

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/validate"
)

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "deploy-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

// makeWebsite creates the site a repository deploys into.
//
// A repository needs a real website row: its foreign key is what stops a
// repository outliving the document root it writes into.
func makeWebsite(t *testing.T, repo *Repository, ctx context.Context,
	serverID, domain string,
) string {
	t.Helper()
	var id string
	err := repo.pool.QueryRow(ctx, `
		INSERT INTO websites (server_id, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, $2, $3, $4, 'active')
		RETURNING id`,
		serverID, domain, "/var/www/"+domain+"/public", "web_test").Scan(&id)
	if err != nil {
		t.Fatalf("create website: %v", err)
	}
	return id
}

func makeRepository(t *testing.T, repo *Repository, ctx context.Context,
	serverID, websiteID string,
) GitRepository {
	t.Helper()
	created, err := repo.CreateRepository(ctx, GitRepository{
		ServerID:             serverID,
		WebsiteID:            websiteID,
		RemoteURL:            "https://github.com/owner/repo.git",
		Branch:               "main",
		Provider:             validate.ProviderNone,
		ScriptTimeoutSeconds: 600,
	}, "", "")
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	return created
}

// ------------------------------------------------------------- repositories

func TestOneRepositoryPerWebsite(t *testing.T) {
	// Two would mean two things writing into one document root, and the second
	// deployment would silently undo the first.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "one.example")
	makeRepository(t, repo, ctx, serverID, website)

	if _, err := repo.CreateRepository(ctx, GitRepository{
		ServerID: serverID, WebsiteID: website,
		RemoteURL: "https://github.com/owner/other.git", Branch: "main",
		Provider: validate.ProviderNone, ScriptTimeoutSeconds: 600,
	}, "", ""); err == nil {
		t.Error("a second repository was attached to one website")
	}
}

func TestARemoteThatIsAnOptionIsRefusedByTheSchemaToo(t *testing.T) {
	// Mirrored in the database because this column becomes an argument to a
	// program: a value starting with a hyphen is an option to git, and
	// "--upload-pack=..." is remote code execution spelled as a URL.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "shape.example")

	for _, remote := range []string{"--upload-pack=/tmp/evil", "-u/tmp/x", "ext::sh -c id"} {
		if _, err := repo.CreateRepository(ctx, GitRepository{
			ServerID: serverID, WebsiteID: website, RemoteURL: remote, Branch: "main",
			Provider: validate.ProviderNone, ScriptTimeoutSeconds: 600,
		}, "", ""); err == nil {
			t.Errorf("the schema accepted %q as a remote", remote)
		}
	}
}

func TestAWebhookNeedsBothHalves(t *testing.T) {
	// A token nothing verifies is an unauthenticated endpoint that deploys,
	// and a secret nothing addresses is a secret no request can reach.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "hook.example")

	if _, err := repo.CreateRepository(ctx, GitRepository{
		ServerID: serverID, WebsiteID: website,
		RemoteURL: "https://github.com/owner/repo.git", Branch: "main",
		Provider: validate.ProviderGitHub, ScriptTimeoutSeconds: 600,
	}, "sometoken", ""); err == nil {
		t.Error("a webhook token was stored with no secret to verify against")
	}
}

func TestDeletingAWebsiteTakesItsRepositoryWithIt(t *testing.T) {
	// Nothing irreplaceable goes: the source is in the repository, which is
	// somewhere else by definition.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "gone.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	if _, err := repo.pool.Exec(ctx, `DELETE FROM websites WHERE id = $1::uuid`, website); err != nil {
		t.Fatalf("delete website: %v", err)
	}
	if _, err := repo.GetRepository(ctx, created.ID); err == nil {
		t.Error("a repository outlived the website it deployed into")
	}
}

// ------------------------------------------------------------------ actions

func TestTheStepsKeepTheOrderTheyWereGivenIn(t *testing.T) {
	// "npm ci" after "npm run build" is not a deployment, it is a deployment
	// that fails — so the order is explicit rather than implied by insertion.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "order.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	saved, err := repo.ReplaceActions(ctx, created.ID, []Action{
		{Kind: validate.ActionNpmCI, Enabled: true},
		{Kind: validate.ActionNpmBuild, Enabled: true},
		{Kind: validate.ActionScript, Enabled: true},
	})
	if err != nil {
		t.Fatalf("ReplaceActions: %v", err)
	}
	if len(saved) != 3 {
		t.Fatalf("expected three steps, got %d", len(saved))
	}
	if saved[0].Kind != validate.ActionNpmCI || saved[2].Kind != validate.ActionScript {
		t.Errorf("the steps came back in a different order: %+v", saved)
	}
}

func TestReorderingTheStepsIsOneTransaction(t *testing.T) {
	// A reorder applied as a sequence of updates passes through states where
	// two steps share a position, which the unique index refuses — so a
	// legitimate edit would fail depending on which row was written first.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "reorder.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	if _, err := repo.ReplaceActions(ctx, created.ID, []Action{
		{Kind: validate.ActionNpmCI, Enabled: true},
		{Kind: validate.ActionNpmBuild, Enabled: true},
	}); err != nil {
		t.Fatalf("ReplaceActions: %v", err)
	}
	reversed, err := repo.ReplaceActions(ctx, created.ID, []Action{
		{Kind: validate.ActionNpmBuild, Enabled: true},
		{Kind: validate.ActionNpmCI, Enabled: true},
	})
	if err != nil {
		t.Fatalf("reordering failed: %v", err)
	}
	if reversed[0].Kind != validate.ActionNpmBuild {
		t.Errorf("the reorder did not apply: %+v", reversed)
	}
}

func TestAStepOutsideTheClosedSetIsRefused(t *testing.T) {
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "closed.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	if _, err := repo.ReplaceActions(ctx, created.ID, []Action{
		{Kind: "rm.everything", Enabled: true},
	}); err == nil {
		t.Error("a step outside the closed set was stored")
	}
}

// -------------------------------------------------------------- deployments

func TestOnlyOneDeploymentRunsAtATime(t *testing.T) {
	// Two deployments into one document root is a working tree being rewritten
	// by one process while another builds from it, and the result is neither
	// commit. Enforced by a unique partial index, because a check written in Go
	// is one two API processes could both pass.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "busy.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	first, err := repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerManual, Branch: "main",
	})
	if err != nil {
		t.Fatalf("StartDeployment: %v", err)
	}

	_, err = repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerWebhook, Branch: "main",
	})
	if err == nil {
		t.Fatal("a second deployment started while one was running")
	}
	if !strings.Contains(err.Error(), ErrInProgress.Error()) {
		t.Errorf("the conflict was not reported as one: %v", err)
	}

	// And once the first finishes, another can start — otherwise the index
	// would be a permanent lock rather than a mutex.
	if err := repo.FinishDeployment(ctx, first.ID, Deployment{Status: "success"}); err != nil {
		t.Fatalf("FinishDeployment: %v", err)
	}
	if _, err := repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerManual, Branch: "main",
	}); err != nil {
		t.Errorf("a deployment could not start after the previous one finished: %v", err)
	}
}

func TestADeploymentThatWasRunningAtARestartIsClosed(t *testing.T) {
	// Left alone it would hold the index for ever, so no further deployment of
	// that website could start — while the page showed one in progress that
	// nothing was progressing.
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "stale.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	if _, err := repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerManual, Branch: "main",
	}); err != nil {
		t.Fatalf("StartDeployment: %v", err)
	}

	released, err := repo.ReleaseStale(ctx)
	if err != nil {
		t.Fatalf("ReleaseStale: %v", err)
	}
	if released == 0 {
		t.Fatal("nothing was released")
	}
	if _, err := repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerManual, Branch: "main",
	}); err != nil {
		t.Errorf("the website is still blocked after the stale deployment was closed: %v", err)
	}
}

func TestAFinishedDeploymentSaysHowItFinished(t *testing.T) {
	repo, serverID, ctx := setup(t)
	website := makeWebsite(t, repo, ctx, serverID, "outcome.example")
	created := makeRepository(t, repo, ctx, serverID, website)

	deployment, err := repo.StartDeployment(ctx, Deployment{
		RepositoryID: created.ID, WebsiteID: website,
		Trigger: validate.TriggerManual, Branch: "main", PreviousCommit: "aaaaaaa",
	})
	if err != nil {
		t.Fatalf("StartDeployment: %v", err)
	}

	exit := 2
	if err := repo.FinishDeployment(ctx, deployment.ID, Deployment{
		Status: "failed", CommitSHA: "bbbbbbb", ExitCode: &exit,
		Log: "npm run build\nError: no", RolledBack: true,
	}); err != nil {
		t.Fatalf("FinishDeployment: %v", err)
	}

	read, err := repo.GetDeployment(ctx, deployment.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if read.Status != "failed" || read.FinishedAt == nil {
		t.Errorf("the deployment does not say how it finished: %+v", read)
	}
	if !read.RolledBack {
		t.Error("the rollback was not recorded")
	}
	if read.PreviousCommit != "aaaaaaa" {
		t.Errorf("the previous commit was lost: %q", read.PreviousCommit)
	}
}

func TestALogIsNotInTheList(t *testing.T) {
	// A build prints whatever the build printed, which regularly includes a
	// token in a URL. The list says whether each deployment worked; reading
	// what it said is a separate request behind a separate permission.
	deployments := stripLogs([]Deployment{
		{ID: "one", Status: "failed", Log: "AWS_SECRET_ACCESS_KEY=abc123"},
	})
	if deployments[0].Log != "" {
		t.Error("a deployment log survived into the list")
	}
	encoded, err := json.Marshal(deployments[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(encoded), "AWS_SECRET") {
		t.Errorf("a secret from a build log is in the list body: %s", encoded)
	}
}

// --------------------------------------------------------------- webhooks

func TestAPushIsVerifiedBySignatureAndNotByItsAddress(t *testing.T) {
	// The token in the URL is an address, not a credential: anybody who can
	// read a forge's settings page can read it. The signature is what
	// authenticates.
	const secret = "a-webhook-secret-long-enough"
	body := []byte(`{"ref":"refs/heads/main"}`)

	if err := verify(validate.ProviderGitHub, secret, SignPayload(secret, body), body); err != nil {
		t.Errorf("a correctly signed push was refused: %v", err)
	}
	if err := verify(validate.ProviderGitHub, secret, SignPayload("wrong-secret", body), body); err == nil {
		t.Error("a push signed with the wrong secret was accepted")
	}
	if err := verify(validate.ProviderGitHub, secret, "", body); err == nil {
		t.Error("a push with no signature was accepted")
	}
	// The same signature over a *different* body must not verify: that is the
	// whole point of signing the body rather than the token.
	if err := verify(validate.ProviderGitHub, secret, SignPayload(secret, body),
		[]byte(`{"ref":"refs/heads/main","x":1}`)); err == nil {
		t.Error("a signature was accepted over a body it was not computed from")
	}
}

func TestASignatureWithoutItsPrefixIsNotAccepted(t *testing.T) {
	// The prefix is compared as part of the value rather than stripped first,
	// so a caller cannot get a match by sending the digest alone — or with a
	// different algorithm named.
	const secret = "a-webhook-secret-long-enough"
	body := []byte(`{"ref":"refs/heads/main"}`)
	bare := strings.TrimPrefix(SignPayload(secret, body), "sha256=")

	if err := verify(validate.ProviderGitHub, secret, bare, body); err == nil {
		t.Error("a bare digest was accepted as a signature")
	}
	if err := verify(validate.ProviderGitHub, secret, "sha1="+bare, body); err == nil {
		t.Error("a digest labelled with another algorithm was accepted")
	}
}

func TestARepositoryWithNoSecretVerifiesNothing(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	if err := verify(validate.ProviderGitHub, "", SignPayload("", body), body); err == nil {
		t.Error("a repository with no secret accepted a push")
	}
	// And a provider of "none" has no webhook at all.
	if err := verify(validate.ProviderNone, "secret", "anything", body); err == nil {
		t.Error("a repository with no webhook accepted a push")
	}
}

func TestOnlyTheBranchIsTakenFromAPushPayload(t *testing.T) {
	// The one field read from an unauthenticated body, and it is used for
	// exactly one thing: deciding whether the push is for the branch this
	// website deploys. It never becomes a branch to check out.
	if branch := pushBranch([]byte(`{"ref":"refs/heads/main"}`)); branch != "main" {
		t.Errorf("branch: %q", branch)
	}
	if branch := pushBranch([]byte(`{"ref":"refs/heads/feature/add-billing"}`)); branch != "feature/add-billing" {
		t.Errorf("branch: %q", branch)
	}
	// A tag push is not a branch.
	if branch := pushBranch([]byte(`{"ref":"refs/tags/v1.0"}`)); branch != "" {
		t.Errorf("a tag was read as a branch: %q", branch)
	}
	// A payload the panel cannot read is "no opinion" rather than a failure: a
	// forge that changes its payload shape should not stop deployments.
	if branch := pushBranch([]byte(`not json`)); branch != "" {
		t.Errorf("unreadable payload: %q", branch)
	}
}
