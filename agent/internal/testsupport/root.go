// Package testsupport holds helpers shared by the agent's tests. Nothing here
// is imported by the agent itself, so it is never linked into the binary.
package testsupport

import (
	"os"
	"runtime"
	"testing"
)

// RequireRoot skips the calling test unless the process can actually perform
// the privileged operation it is about to attempt.
//
// The agent's whole job is doing things only root may do — chowning a crontab
// to root:root so cron will read it, creating an FTP home under /var/www,
// laying out /var/log/jothost. A test of that work has to really do it: a
// version that stubbed the syscalls out would be checking the stub. So these
// tests need root, and on an unprivileged runner they fail with EPERM, which
// says nothing about whether the code is right.
//
// Skipping is therefore the honest outcome there — but a skip is not a pass,
// and the danger of this helper is a green CI that checked less than it looks
// like it did. These tests still run for real, as root, in the Docker agent
// suite (`make docker-test`); that run is what actually covers them, and the
// message below says so where somebody reading a CI log will see it.
func RequireRoot(t *testing.T) {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("this test performs Unix ownership operations")
	}
	if os.Geteuid() != 0 {
		t.Skipf("NOT CHECKED HERE: this test needs root (running as uid %d). "+
			"It is exercised as root by the Docker agent suite.", os.Geteuid())
	}
}
