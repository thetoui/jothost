package ssl

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// nginx serves ACME tokens as an unprivileged user, so every directory down to
// the token has to be traversable. Under the Agent's 0077 umask a plain
// MkdirAll left .well-known and acme-challenge at 0700, and every HTTP-01
// validation failed. Directories left at 0700 by that code persist on disk,
// so they must be repaired as well as created right.
func TestTheChallengeDirectoryCanBeServed(t *testing.T) {
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	root := t.TempDir()
	certbot := NewCertbot(nil, root)
	base := filepath.Join(root, ACMEChallengeDir)
	levels := []string{
		base,
		filepath.Join(base, ".well-known"),
		filepath.Join(base, ".well-known", "acme-challenge"),
	}

	check := func(when string) {
		t.Helper()
		for _, level := range levels {
			info, err := os.Stat(level)
			if err != nil {
				t.Fatalf("%s: stat %s: %v", when, level, err)
			}
			if got := info.Mode().Perm(); got != challengeDirMode {
				t.Fatalf("%s: %s is %04o, want %04o", when, level, got, challengeDirMode)
			}
		}
	}

	if err := certbot.prepareChallengeDir(); err != nil {
		t.Fatalf("prepareChallengeDir: %v", err)
	}
	check("on a fresh host")

	// A host that ran the earlier code.
	for _, level := range levels {
		if err := os.Chmod(level, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := certbot.prepareChallengeDir(); err != nil {
		t.Fatalf("prepareChallengeDir again: %v", err)
	}
	check("on a host left at 0700")
}
