package fsperm

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

// Every test runs under the Agent's own umask. Under the usual 0022 the modes
// asked for here would come out right by accident, and the tests would pass
// against the very code this package replaces.
func agentUmask(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("umask is a Unix property")
	}
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

func TestTheUmaskReallyNarrowsWhatIsAsked(t *testing.T) {
	// The control. If this ever passes the rest of this file proves nothing.
	agentUmask(t)
	dir := filepath.Join(t.TempDir(), "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Fatalf("expected the umask to narrow 0755 to 0700 here, got %04o", got)
	}
}

func TestMkdirAllGivesEveryCreatedDirectoryTheMode(t *testing.T) {
	agentUmask(t)
	base := t.TempDir()
	leaf := filepath.Join(base, "a", "b", "c")

	if err := MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// The parents matter as much as the leaf: a web server that cannot
	// traverse "a" cannot reach "c", whatever "c" says.
	for _, dir := range []string{"a", "a/b", "a/b/c"} {
		if got := modeOf(t, filepath.Join(base, dir)); got != 0o755 {
			t.Fatalf("%s is %04o, want 0755", dir, got)
		}
	}
}

func TestMkdirAllLeavesExistingDirectoriesAlone(t *testing.T) {
	agentUmask(t)
	base := t.TempDir()
	existing := filepath.Join(base, "operator-set")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(existing, 0o710); err != nil {
		t.Fatal(err)
	}

	if err := MkdirAll(filepath.Join(existing, "new"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if got := modeOf(t, existing); got != 0o710 {
		t.Fatalf("an existing directory was changed to %04o", got)
	}
	if got := modeOf(t, filepath.Join(existing, "new")); got != 0o755 {
		t.Fatalf("the new directory is %04o, want 0755", got)
	}
}

func TestMkdirAllOnAnExistingPathIsANoOp(t *testing.T) {
	agentUmask(t)
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if got := modeOf(t, dir); got != 0o700 {
		t.Fatalf("an existing directory was changed to %04o", got)
	}
}

func TestMkdirAndSetCreated(t *testing.T) {
	agentUmask(t)
	base := t.TempDir()

	dir := filepath.Join(base, "one")
	if err := Mkdir(dir, 0o750); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if got := modeOf(t, dir); got != 0o750 {
		t.Fatalf("Mkdir left %04o, want 0750", got)
	}

	path := filepath.Join(base, "file")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := SetCreated(file, 0o640); err != nil {
		t.Fatalf("SetCreated: %v", err)
	}
	if got := modeOf(t, path); got != 0o640 {
		t.Fatalf("SetCreated left %04o, want 0640", got)
	}
}
