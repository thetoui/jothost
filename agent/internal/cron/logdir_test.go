package cron

import (
	"os"
	"syscall"
	"testing"

	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/testsupport"
)

// A job's account has to be able to reach its own log.
//
// Each crontab line appends the job's output to a file in the log directory,
// and runs as the site's account. If that account cannot enter the directory,
// the shell cannot open the file, and a command whose redirection fails is
// never run: cron fires on time and nothing happens. That was every scheduled
// job on a fresh install.
//
// The umask is the whole point. The Agent runs with 0077, which turns
// MkdirAll(0755) into a 0700 directory. Under the usual 0022 this test passes
// against the broken code, so it sets the Agent's umask itself.
func TestAJobsAccountCanReachItsLog(t *testing.T) {
	testsupport.RequireRoot(t)

	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	p, _ := provider(t)
	owner := sites.Account{Name: "web_shop", UID: 61001, GID: 61001}
	const id = "8f3c2b1a-4d5e-4a6b-8c7d-9e0f1a2b3c4d"

	if err := p.ensureLog(id, owner); err != nil {
		t.Fatalf("ensureLog: %v", err)
	}
	assertMode(t, p.LogDir(), logDirMode, "the log directory")
	assertMode(t, p.LogPath(id), jobLogMode, "the job's log")

	// A host that already has the directory at 0700 - every fresh install
	// made before this was fixed - is repaired by the next job saved, rather
	// than left broken because the directory "already exists".
	if err := os.Chmod(p.LogDir(), 0o700); err != nil {
		t.Fatalf("break the directory the way a fresh install had it: %v", err)
	}
	if err := p.ensureLog(id, owner); err != nil {
		t.Fatalf("ensureLog on an existing directory: %v", err)
	}
	assertMode(t, p.LogDir(), logDirMode, "an existing 0700 log directory")
}

func assertMode(t *testing.T, path string, want os.FileMode, what string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", what, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s is %04o, want %04o", what, got, want)
	}
}
