package backup

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/shared/validate"
)

// Backups of the panel itself (docs/PANEL_BACKUP.md), against a real
// database: who may take one, what the Agent is sent, and what is refused.

const testEncryptionKey = "0f1e2d3c4b5a69788796a5b4c3d2e1f00f1e2d3c4b5a69788796a5b4c3d2e1f0"

// operator holds backup.manage and not server.manage - the role the gate
// exists for. admin holds both.
var (
	operator = Actor{UserID: "", CanManageServer: false}
	admin    = Actor{UserID: "", CanManageServer: true}
)

// oneSite and noDatabases stand in for the websites and databases packages,
// so a full backup can be planned without either.
type oneSite struct{}

func (oneSite) SiteFor(_ context.Context, id string) (Site, error) {
	return Site{ID: id, Domain: "example.com", DocumentRoot: "/var/www/example.com/public", SystemUser: "web_example"}, nil
}

func (oneSite) AllSites(ctx context.Context) ([]Site, error) {
	site, err := oneSite{}.SiteFor(ctx, "site-1")
	return []Site{site}, err
}

type noDatabases struct{}

func (noDatabases) DatabaseFor(context.Context, string) (DatabaseRef, error) {
	return DatabaseRef{}, ErrNotFound
}

func (noDatabases) DatabasesForWebsite(context.Context, string) ([]DatabaseRef, error) {
	return nil, nil
}

func (noDatabases) AllDatabases(context.Context) ([]DatabaseRef, error) { return nil, nil }

func panelService(t *testing.T) (*Service, *Repository, *jobs.Repository, *secrets.Encrypter, Destination) {
	t.Helper()
	repo, serverID, ctx := setup(t)
	enc, err := secrets.NewEncrypter(testEncryptionKey)
	if err != nil {
		t.Fatalf("NewEncrypter: %v", err)
	}
	// setup already reset the database and registered the server. Asking
	// testsupport for the pool again would reset it a second time and take
	// that server with it.
	jobRepo := jobs.NewRepository(repo.pool)
	service := NewService(ServiceOptions{
		Repository: repo,
		Jobs:       jobRepo,
		Crypto:     enc,
		Sites:      oneSite{},
		Databases:  noDatabases{},
		ServerID:   serverID,
	})
	return service, repo, jobRepo, enc, localDestination(t, repo, ctx, serverID, "disk")
}

func TestAnOperatorCannotTakeAPanelBackup(t *testing.T) {
	service, repo, _, _, dest := panelService(t)
	ctx := t.Context()

	_, err := service.Create(ctx, CreateRequest{Type: validate.BackupPanel, DestinationID: dest.ID}, operator)
	if !errors.Is(err, ErrPanelNeedsServerManage) {
		t.Fatalf("an operator's panel backup gave %v, want ErrPanelNeedsServerManage", err)
	}
	// Refused before anything was recorded or queued.
	if items, err := repo.ListBackups(ctx, ListBackupParams{ServerID: service.serverID}); err != nil || len(items) != 0 {
		t.Fatalf("a refused panel backup left %d rows (err %v)", len(items), err)
	}
}

func TestAPanelBackupSendsTheDerivedKeyAndNothingToDump(t *testing.T) {
	service, _, jobRepo, enc, dest := panelService(t)
	ctx := t.Context()

	item, err := service.Create(ctx, CreateRequest{Type: validate.BackupPanel, DestinationID: dest.ID}, admin)
	if err != nil {
		t.Fatalf("an administrator's panel backup was refused: %v", err)
	}
	if item.Type != validate.BackupPanel || item.JobID == nil {
		t.Fatalf("recorded type %q, job %v", item.Type, item.JobID)
	}

	// The stored job holds no key: it is added only when the job is sent.
	job, err := jobRepo.Get(ctx, *item.JobID)
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if _, stored := job.Payload["sealing_key"]; stored {
		t.Fatal("the sealing key was written into the job row")
	}

	payload, err := service.ResolvePayload(ctx, job)
	if err != nil {
		t.Fatalf("ResolvePayload: %v", err)
	}
	if got, want := payload["sealing_key"], hex.EncodeToString(enc.PanelBackupKey()); got != want {
		t.Fatal("the payload does not carry the key derived for panel backups")
	}
	// Never ENCRYPTION_KEY itself, which decrypts every credential in the
	// database the Agent is backing up.
	if payload["sealing_key"] == testEncryptionKey {
		t.Fatal("the Agent was sent ENCRYPTION_KEY itself")
	}
	if sites, _ := payload["sites"].([]agentclient.BackupSite); len(sites) != 0 {
		t.Fatalf("a panel backup named sites: %v", payload["sites"])
	}
}

func TestOnlyAPanelBackupCarriesAKey(t *testing.T) {
	service, repo, jobRepo, _, dest := panelService(t)
	ctx := t.Context()

	// A backup row of another type, queued directly: the property is about
	// what the payload builder adds, not about which types plan() can build.
	item, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: service.serverID, Subject: "server", Type: validate.BackupFull,
		DestinationID: dest.ID, DestinationName: dest.Name,
		Key: "full/server/2026-09-14/030000.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.dispatch(ctx, item, ""); err != nil {
		t.Fatal(err)
	}
	stored, err := repo.GetBackup(ctx, item.ID)
	if err != nil || stored.JobID == nil {
		t.Fatalf("no job: %v", err)
	}
	job, err := jobRepo.Get(ctx, *stored.JobID)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := service.ResolvePayload(ctx, job)
	if err != nil {
		t.Fatalf("ResolvePayload: %v", err)
	}
	if sites, _ := payload["sites"].([]agentclient.BackupSite); len(sites) != 1 {
		t.Fatalf("the full backup's payload was not built: sites %v", payload["sites"])
	}
	if _, has := payload["sealing_key"]; has {
		t.Fatal("a full backup was sent a sealing key")
	}
}

func TestAPanelBackupIsNeverRestoredThroughThePanel(t *testing.T) {
	service, repo, _, _, dest := panelService(t)
	ctx := t.Context()

	item, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: service.serverID, Subject: "panel", Type: validate.BackupPanel,
		DestinationID: dest.ID, DestinationName: dest.Name,
		Key: "panel/panel/2026-09-14/030000.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteBackup(ctx, item.ID, CompleteBackupParams{
		Path: "panel/panel/2026-09-14/030000.tar.gz", Size: 4096,
		Checksum: "a1b2c3", Verified: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Refused to an administrator, and with the confirmation given: it is
	// not a question of permission.
	_, err = service.Restore(ctx, item.ID, RestoreRequest{Confirm: item.ID}, admin)
	if !errors.Is(err, ErrPanelRestoreOnHost) {
		t.Fatalf("restoring a panel backup gave %v, want ErrPanelRestoreOnHost", err)
	}
}

func TestAnOperatorCannotVerifyOrDeleteAPanelBackup(t *testing.T) {
	// Refused before the Agent is asked anything: this service has no Agent
	// client, so reaching it would panic.
	service, repo, _, _, dest := panelService(t)
	ctx := t.Context()

	item, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: service.serverID, Subject: "panel", Type: validate.BackupPanel,
		DestinationID: dest.ID, DestinationName: dest.Name,
		Key: "panel/panel/2026-09-14/040000.tar.gz",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Verify(ctx, item.ID, "req_test", operator); !errors.Is(err, ErrPanelNeedsServerManage) {
		t.Fatalf("an operator's verify gave %v", err)
	}
	if err := service.Delete(ctx, item.ID, "req_test", operator); !errors.Is(err, ErrPanelNeedsServerManage) {
		t.Fatalf("an operator's delete gave %v", err)
	}
	if _, err := repo.GetBackup(ctx, item.ID); err != nil {
		t.Fatalf("the backup did not survive a refused delete: %v", err)
	}
}

func TestAPanelScheduleIsGatedAndItsRunsAreNot(t *testing.T) {
	service, _, _, _, dest := panelService(t)
	ctx := t.Context()

	input := ScheduleInput{
		Name: "Panel nightly", Type: validate.BackupPanel, DestinationID: dest.ID,
		Hour: 2, Minute: 30, DayOfWeek: -1, RetentionDays: 30, KeepLast: 7,
	}
	if _, err := service.CreateSchedule(ctx, input, operator); !errors.Is(err, ErrPanelNeedsServerManage) {
		t.Fatalf("an operator's panel schedule gave %v", err)
	}

	schedule, err := service.CreateSchedule(ctx, input, admin)
	if err != nil {
		t.Fatalf("an administrator's panel schedule was refused: %v", err)
	}

	for name, attempt := range map[string]func() error{
		"run":    func() error { _, err := service.RunSchedule(ctx, schedule.ID, operator); return err },
		"update": func() error { _, err := service.UpdateSchedule(ctx, schedule.ID, input, operator); return err },
		"delete": func() error { return service.DeleteSchedule(ctx, schedule.ID, operator) },
	} {
		if err := attempt(); !errors.Is(err, ErrPanelNeedsServerManage) {
			t.Fatalf("an operator's %s of a panel schedule gave %v", name, err)
		}
	}

	// The scheduler acts for nobody. Its runs must not be refused, or every
	// scheduled panel backup would fail at its first window.
	item, err := service.runSchedule(ctx, schedule, Actor{})
	if err != nil {
		t.Fatalf("the scheduler's run of a panel schedule was refused: %v", err)
	}
	if item.Type != validate.BackupPanel {
		t.Fatalf("the scheduled run recorded type %q", item.Type)
	}
}
