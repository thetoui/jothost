package backup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/validate"
)

// The panel's record of what has been backed up, against a real database.
//
// The properties worth a database to check are the ones that decide whether an
// operator still has anything on the day they need it: what "completed" is
// allowed to mean, what retention is allowed to remove, and what survives a
// website being deleted.

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "backup-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func localDestination(t *testing.T, repo *Repository, ctx context.Context,
	serverID, name string,
) Destination {
	t.Helper()

	dest, err := repo.CreateDestination(ctx, CreateDestinationParams{
		ServerID: serverID,
		Name:     name,
		Kind:     "local",
		Config:   map[string]any{"directory": "/var/lib/jothost/backups"},
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	return dest
}

func TestADestinationNeverReturnsItsSecret(t *testing.T) {
	// The struct handlers serialise has no credential field at all. A field
	// that is only sometimes cleared is a field that will one day be returned.
	repo, serverID, ctx := setup(t)

	dest, err := repo.CreateDestination(ctx, CreateDestinationParams{
		ServerID:    serverID,
		Name:        "offsite",
		Kind:        "s3",
		Config:      map[string]any{"bucket": "backups", "access_key": "AKIA"},
		Credentials: "sealed-ciphertext",
	})
	if err != nil {
		t.Fatalf("create destination: %v", err)
	}
	if !dest.HasCredentials {
		t.Error("the destination does not report that it holds a credential")
	}

	// The secret is reachable only through its own method, which is the one
	// call site that needs it.
	secret, err := repo.DestinationSecret(ctx, dest.ID)
	if err != nil {
		t.Fatalf("DestinationSecret: %v", err)
	}
	if secret != "sealed-ciphertext" {
		t.Errorf("secret = %q", secret)
	}
}

func TestALocalDestinationMayNotCarryACredential(t *testing.T) {
	// The schema says so, because an S3 destination with no secret key is one
	// that fails at the worst possible moment and a local one with a secret is
	// a secret nothing will ever use.
	repo, serverID, ctx := setup(t)

	_, err := repo.CreateDestination(ctx, CreateDestinationParams{
		ServerID: serverID, Name: "confused", Kind: "local",
		Config: map[string]any{"directory": "/backups"}, Credentials: "why",
	})
	if err == nil {
		t.Fatal("a local destination with a credential was accepted")
	}
}

func TestTwoDestinationsCannotShareAName(t *testing.T) {
	repo, serverID, ctx := setup(t)
	localDestination(t, repo, ctx, serverID, "Local disk")

	_, err := repo.CreateDestination(ctx, CreateDestinationParams{
		ServerID: serverID, Name: "local disk", Kind: "local",
		Config: map[string]any{"directory": "/backups"},
	})
	if !errors.Is(err, ErrNameTaken) {
		t.Fatalf("create = %v, want ErrNameTaken", err)
	}
}

func TestAnUnverifiedBackupIsRecordedAsFailed(t *testing.T) {
	// The bytes may well be there. The panel has no basis for saying so, and a
	// listing where "completed" sometimes means "probably" is a listing nobody
	// can use to decide whether they are safe.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	item, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: serverID, Subject: "example.com", Type: "website",
		DestinationID: dest.ID, DestinationName: dest.Name,
		Key: "website/example.com/2026-09-03/031500.tar.gz",
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}

	if err := repo.CompleteBackup(ctx, item.ID, CompleteBackupParams{
		Path:         "website/example.com/2026-09-03/031500.tar.gz",
		Size:         4096,
		Checksum:     "a1b2c3",
		Verified:     false,
		VerifyDetail: "the copy read back does not match the archive's checksum",
	}); err != nil {
		t.Fatalf("complete backup: %v", err)
	}

	stored, err := repo.GetBackup(ctx, item.ID)
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}
	if stored.Status != StatusFailed {
		t.Errorf("status = %q, want failed", stored.Status)
	}
	if stored.VerifiedAt != nil {
		t.Error("an unverified backup carries a verified_at")
	}
	if stored.Restorable() {
		t.Error("an unverified backup is offered as restorable")
	}
	if stored.Error == nil {
		t.Error("the reason was not recorded")
	}
}

func TestAVerifiedBackupIsRestorable(t *testing.T) {
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	item := completedBackup(t, repo, ctx, serverID, dest, "example.com", time.Now())
	if !item.Restorable() {
		t.Fatalf("a completed, verified backup is not restorable: %+v", item)
	}
}

// completedBackup writes a backup that finished and verified.
func completedBackup(t *testing.T, repo *Repository, ctx context.Context,
	serverID string, dest Destination, subject string, at time.Time,
) Backup {
	t.Helper()

	key := "website/" + subject + "/" + at.UTC().Format("2006-01-02/150405.000000000") + ".tar.gz"
	item, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: serverID, Subject: subject, Type: "website",
		DestinationID: dest.ID, DestinationName: dest.Name, Key: key,
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if err := repo.CompleteBackup(ctx, item.ID, CompleteBackupParams{
		Path: key, Size: 4096, Checksum: "a1b2c3", Verified: true,
		Manifest: map[string]any{"version": 1, "file_count": 3},
	}); err != nil {
		t.Fatalf("complete backup: %v", err)
	}
	stored, err := repo.GetBackup(ctx, item.ID)
	if err != nil {
		t.Fatalf("get backup: %v", err)
	}
	return stored
}

func TestRetentionKeepsTheMostRecentWhateverTheirAge(t *testing.T) {
	// The case this exists for: a panel that was off for longer than the
	// retention window comes back, finds everything expired, and would delete
	// all of it — leaving nothing at the exact moment somebody most needs
	// something.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	schedule := makeSchedule(t, repo, ctx, serverID, dest, 7, 3)

	// Five backups, every one of them older than the retention window.
	for i := 0; i < 5; i++ {
		item := completedBackup(t, repo, ctx, serverID, dest, "example.com",
			time.Now().Add(-time.Duration(i)*time.Hour))
		mustAttach(t, repo, ctx, item.ID, schedule.ID)
		backdate(t, repo, ctx, item.ID, time.Now().AddDate(0, 0, -30+i))
	}

	prunable, err := repo.Prunable(ctx, PrunableParams{
		ScheduleID: schedule.ID,
		OlderThan:  time.Now(),
		KeepLast:   schedule.KeepLast,
	})
	if err != nil {
		t.Fatalf("Prunable: %v", err)
	}
	if len(prunable) != 2 {
		t.Fatalf("prunable = %d, want 2 (five taken, three kept)", len(prunable))
	}
}

func TestRetentionCountsOnlyVerifiedBackupsTowardsTheFloor(t *testing.T) {
	// Keeping three archives the panel could not read back is keeping nothing.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	schedule := makeSchedule(t, repo, ctx, serverID, dest, 7, 2)

	// Two good ones, then three that failed to verify.
	for i := 0; i < 2; i++ {
		item := completedBackup(t, repo, ctx, serverID, dest, "example.com",
			time.Now().Add(-time.Duration(i+10)*time.Hour))
		mustAttach(t, repo, ctx, item.ID, schedule.ID)
		backdate(t, repo, ctx, item.ID, time.Now().AddDate(0, 0, -30))
	}
	for i := 0; i < 3; i++ {
		item, err := repo.CreateBackup(ctx, CreateBackupParams{
			ServerID: serverID, Subject: "example.com", Type: "website",
			DestinationID: dest.ID, DestinationName: dest.Name,
			Key: "website/example.com/bad-" + time.Now().Format("150405.000000000") + ".tar.gz",
		})
		if err != nil {
			t.Fatalf("create backup: %v", err)
		}
		if err := repo.FailBackup(ctx, item.ID, "the destination could not be reached"); err != nil {
			t.Fatalf("fail backup: %v", err)
		}
		mustAttach(t, repo, ctx, item.ID, schedule.ID)
		backdate(t, repo, ctx, item.ID, time.Now().AddDate(0, 0, -1))
	}

	prunable, err := repo.Prunable(ctx, PrunableParams{
		ScheduleID: schedule.ID, OlderThan: time.Now(), KeepLast: schedule.KeepLast,
	})
	if err != nil {
		t.Fatalf("Prunable: %v", err)
	}

	// The three failures are prunable and the two good ones are the floor.
	if len(prunable) != 3 {
		t.Fatalf("prunable = %d, want the three failures", len(prunable))
	}
	for _, item := range prunable {
		if item.VerifiedAt != nil {
			t.Errorf("a verified backup inside the floor is prunable: %s", item.ID)
		}
	}
}

func TestRetentionLeavesBackupsInsideTheWindow(t *testing.T) {
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	schedule := makeSchedule(t, repo, ctx, serverID, dest, 30, 1)

	item := completedBackup(t, repo, ctx, serverID, dest, "example.com", time.Now())
	mustAttach(t, repo, ctx, item.ID, schedule.ID)
	backdate(t, repo, ctx, item.ID, time.Now().AddDate(0, 0, -5))

	prunable, err := repo.Prunable(ctx, PrunableParams{
		ScheduleID: schedule.ID,
		OlderThan:  time.Now().AddDate(0, 0, -schedule.RetentionDays),
		KeepLast:   schedule.KeepLast,
	})
	if err != nil {
		t.Fatalf("Prunable: %v", err)
	}
	if len(prunable) != 0 {
		t.Fatalf("prunable = %d, want none: it is five days old and the window is thirty",
			len(prunable))
	}
}

func makeSchedule(t *testing.T, repo *Repository, ctx context.Context,
	serverID string, dest Destination, retentionDays, keepLast int,
) Schedule {
	t.Helper()

	schedule, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "nightly", Type: "full",
		DestinationID: dest.ID, Hour: 3, Minute: 0, DayOfWeek: -1,
		RetentionDays: retentionDays, KeepLast: keepLast, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	return schedule
}

func mustAttach(t *testing.T, repo *Repository, ctx context.Context, id, scheduleID string) {
	t.Helper()
	if err := repo.attachSchedule(ctx, id, scheduleID); err != nil {
		t.Fatalf("attach schedule: %v", err)
	}
}

// backdate moves a backup's created_at, so retention can be tested without
// waiting a month.
func backdate(t *testing.T, repo *Repository, ctx context.Context, id string, at time.Time) {
	t.Helper()
	if _, err := repo.pool.Exec(ctx,
		`UPDATE backups SET created_at = $2 WHERE id = $1::uuid`, id, at); err != nil {
		t.Fatalf("backdate: %v", err)
	}
}

func TestDeletingAScheduleKeepsTheBackupsItTook(t *testing.T) {
	// They are the only reason it existed, and deleting a schedule is a
	// decision about the future.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	schedule := makeSchedule(t, repo, ctx, serverID, dest, 14, 3)

	item := completedBackup(t, repo, ctx, serverID, dest, "example.com", time.Now())
	mustAttach(t, repo, ctx, item.ID, schedule.ID)

	if err := repo.DeleteSchedule(ctx, schedule.ID); err != nil {
		t.Fatalf("delete schedule: %v", err)
	}

	stored, err := repo.GetBackup(ctx, item.ID)
	if err != nil {
		t.Fatalf("the backup went with its schedule: %v", err)
	}
	if stored.ScheduleID != nil {
		t.Error("the backup still points at a schedule that is gone")
	}
	if stored.Status != StatusCompleted {
		t.Errorf("status = %q, want it untouched", stored.Status)
	}
}

func TestADestinationAScheduleUsesCannotBeDeleted(t *testing.T) {
	// Deleting it would leave the schedule unable to run, which is a backup
	// that silently stops happening.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	makeSchedule(t, repo, ctx, serverID, dest, 14, 3)

	if err := repo.DeleteDestination(ctx, dest.ID); !errors.Is(err, ErrDestinationInUse) {
		t.Fatalf("delete = %v, want ErrDestinationInUse", err)
	}
}

func TestASchedulesRetentionCannotBeSetToLoseEverything(t *testing.T) {
	// The schema enforces it as well as Go does, so a value cannot reach the
	// table through some future code path that forgets to validate.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	if _, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "bad", Type: "full", DestinationID: dest.ID,
		Hour: 3, DayOfWeek: -1, RetentionDays: 0, KeepLast: 1, Enabled: true,
	}); err == nil {
		t.Error("a retention of zero days was accepted")
	}
	if _, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "bad2", Type: "full", DestinationID: dest.ID,
		Hour: 3, DayOfWeek: -1, RetentionDays: 7, KeepLast: 0, Enabled: true,
	}); err == nil {
		t.Error("keeping none was accepted")
	}
}

func TestDueSchedulesTakesAMissedWindowRatherThanSkippingIt(t *testing.T) {
	// A panel that was off at three in the morning takes the backup when it
	// comes back. The alternative is a day with no backup and nothing saying so.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	schedule, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "nightly", Type: "full", DestinationID: dest.ID,
		Hour: 3, Minute: 0, DayOfWeek: -1, RetentionDays: 14, KeepLast: 3, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	noon := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	due, err := repo.DueSchedules(ctx, serverID, noon)
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	if len(due) != 1 || due[0].ID != schedule.ID {
		t.Fatalf("due = %+v, want the nightly schedule", due)
	}
}

func TestDueSchedulesDoesNotRunTheSameWindowTwice(t *testing.T) {
	// The loop ticks every minute. Without this, a nightly backup would be
	// taken sixty times an hour.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")
	schedule := makeSchedule(t, repo, ctx, serverID, dest, 14, 3)

	if err := repo.RecordScheduleRun(ctx, schedule.ID, "running", ""); err != nil {
		t.Fatalf("record run: %v", err)
	}

	due, err := repo.DueSchedules(ctx, serverID, time.Now().UTC())
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	for _, item := range due {
		if item.ID == schedule.ID {
			t.Fatal("a schedule that has already run in this window came back as due")
		}
	}
}

func TestADisabledScheduleIsNeverDue(t *testing.T) {
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	schedule, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "off", Type: "full", DestinationID: dest.ID,
		Hour: 0, Minute: 0, DayOfWeek: -1, RetentionDays: 14, KeepLast: 3, Enabled: false,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	due, err := repo.DueSchedules(ctx, serverID, time.Now().UTC())
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	for _, item := range due {
		if item.ID == schedule.ID {
			t.Fatal("a disabled schedule came back as due")
		}
	}
}

func TestAWeeklyScheduleIsOnlyDueOnItsDay(t *testing.T) {
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	// Sunday.
	schedule, err := repo.CreateSchedule(ctx, CreateScheduleParams{
		ServerID: serverID, Name: "weekly", Type: "full", DestinationID: dest.ID,
		Hour: 3, Minute: 0, DayOfWeek: 0, RetentionDays: 30, KeepLast: 4, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	// 2026-09-03 is a Thursday.
	thursday := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	due, err := repo.DueSchedules(ctx, serverID, thursday)
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	for _, item := range due {
		if item.ID == schedule.ID {
			t.Fatal("a Sunday schedule was due on a Thursday")
		}
	}

	// 2026-09-06 is the Sunday.
	sunday := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	due, err = repo.DueSchedules(ctx, serverID, sunday)
	if err != nil {
		t.Fatalf("DueSchedules: %v", err)
	}
	found := false
	for _, item := range due {
		if item.ID == schedule.ID {
			found = true
		}
	}
	if !found {
		t.Fatal("a Sunday schedule was not due on a Sunday")
	}
}

func TestObjectKeyIsReadableWithoutThePanel(t *testing.T) {
	// A backup nobody can find without the software that made it is a backup
	// that fails on the day the software is what broke.
	at := time.Date(2026, 9, 3, 3, 15, 0, 0, time.UTC)
	key := objectKey("website", "example.com", at)
	if key != "website/example.com/2026-09-03/031500.tar.gz" {
		t.Errorf("objectKey = %q", key)
	}
}

func TestObjectKeyReducesAnAwkwardSubject(t *testing.T) {
	// A site whose name is unusual still gets backed up: the segment is reduced
	// to something a key accepts rather than the backup being refused.
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, subject := range []string{"../etc/shadow", "a b c", "", "..", "-rf"} {
		key := objectKey("website", subject, at)
		if err := validate.BackupKey(key); err != nil {
			t.Errorf("objectKey(%q) produced %q, which is not a usable key: %v",
				subject, key, err)
		}
	}
}

func TestStatsSeparateVerifiedFromCompleted(t *testing.T) {
	// "How many backups do I have" and "how many do I know are intact" are
	// different questions, and only the second one matters.
	repo, serverID, ctx := setup(t)
	dest := localDestination(t, repo, ctx, serverID, "disk")

	completedBackup(t, repo, ctx, serverID, dest, "a.example", time.Now())

	failed, err := repo.CreateBackup(ctx, CreateBackupParams{
		ServerID: serverID, Subject: "b.example", Type: "website",
		DestinationID: dest.ID, DestinationName: dest.Name, Key: "website/b/x.tar.gz",
	})
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if err := repo.FailBackup(ctx, failed.ID, "no"); err != nil {
		t.Fatalf("fail backup: %v", err)
	}

	stats, err := repo.Stats(ctx, serverID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Total != 2 {
		t.Errorf("total = %d, want 2", stats.Total)
	}
	if stats.Completed != 1 || stats.Verified != 1 {
		t.Errorf("completed = %d, verified = %d, want 1 and 1",
			stats.Completed, stats.Verified)
	}
	if stats.Failed != 1 {
		t.Errorf("failed = %d, want 1", stats.Failed)
	}
}
