package files_test

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/jothost/panel/agent/internal/files"
)

// What the file manager creates has to be servable.
//
// Site content is owned by the site's account and read by nginx through the
// web server's group, so a file needs group read and a folder group
// traversal. The Agent runs with umask 0077, which quietly removed both: a
// file created, uploaded or extracted in the panel came out 0600 or 0700, and
// the site answered 403 for it. Under the usual 0022 these tests pass against
// that code, so they set the Agent's umask themselves.

func agentUmask(t *testing.T) {
	t.Helper()
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
}

func wantPerm(t *testing.T, path string, want os.FileMode, what string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s is %04o, want %04o: the web server could not serve it", what, got, want)
	}
}

func TestWhatTheFileManagerCreatesCanBeServed(t *testing.T) {
	manager, root := tree(t)
	agentUmask(t)
	public := filepath.Join(root, "site", "public")

	if _, err := manager.Create(filepath.Join(public, "new.html")); err != nil {
		t.Fatalf("Create: %v", err)
	}
	wantPerm(t, filepath.Join(public, "new.html"), 0o640, "a created file")

	// The parents branch, at the depth the manager allows: it resolves the
	// parent before creating, so only the last level can be missing. That
	// every level fsperm.MkdirAll creates gets the mode is fsperm's own test.
	if _, err := manager.Mkdir(filepath.Join(public, "img"), true); err != nil {
		t.Fatalf("Mkdir with parents: %v", err)
	}
	wantPerm(t, filepath.Join(public, "img"), 0o750, "a folder created with parents")

	if _, err := manager.Mkdir(filepath.Join(public, "css"), false); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	wantPerm(t, filepath.Join(public, "css"), 0o750, "a single created folder")
}

func TestAnUploadCanBeServedAndAnOverwriteKeepsItsMode(t *testing.T) {
	manager, root := tree(t)
	agentUmask(t)
	public := filepath.Join(root, "site", "public")

	upload := filepath.Join(public, "logo.svg")
	if _, err := manager.Write(files.WriteRequest{
		Path: upload, Data: []byte("<svg/>"), Truncate: true, Final: true,
	}); err != nil {
		t.Fatalf("upload: %v", err)
	}
	wantPerm(t, upload, 0o640, "an uploaded file")

	// Somebody chose this mode; replacing the content is not a reason to take
	// the choice away.
	if err := os.Chmod(upload, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Write(files.WriteRequest{
		Path: upload, Data: []byte("<svg></svg>"), Truncate: true, Final: true,
	}); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	wantPerm(t, upload, 0o644, "an overwritten file")
}

func TestAnExtractedArchiveCanBeServed(t *testing.T) {
	manager, root := tree(t)
	archive := filepath.Join(root, "site", "public.zip")
	if _, err := manager.Archive([]string{filepath.Join(root, "site", "public")}, archive); err != nil {
		t.Fatalf("archive: %v", err)
	}

	agentUmask(t)
	destination := filepath.Join(root, "site", "restored")
	if _, err := manager.Extract(archive, destination); err != nil {
		t.Fatalf("extract: %v", err)
	}
	wantPerm(t, filepath.Join(destination, "public"), 0o750, "an extracted folder")
	wantPerm(t, filepath.Join(destination, "public", "index.php"), 0o640, "an extracted file")
}
