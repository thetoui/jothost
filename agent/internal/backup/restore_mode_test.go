package backup

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// A restore puts back the modes the archive recorded.
//
// The Agent runs with umask 0077. Restored files were created with their
// archived mode, which the umask then narrowed: a site's 0644 files came back
// 0600, directories the archive did not describe came back 0700, and the
// restored site answered 403 to every visitor. The umask is set only for the
// restore, so the backup itself records ordinary modes.
func TestARestoredSiteKeepsItsModes(t *testing.T) {
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{
		"index.html":       "original",
		"assets/css/a.css": "body{}",
	})
	css := filepath.Join(root, "assets", "css")
	for path, mode := range map[string]os.FileMode{
		filepath.Join(root, "assets"):     0o755,
		css:                               0o755,
		filepath.Join(root, "index.html"): 0o644,
		filepath.Join(css, "a.css"):       0o644,
	} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
	}

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The damage a restore is for: the whole assets tree gone, and the index.
	if err := os.RemoveAll(filepath.Join(root, "assets")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "index.html")); err != nil {
		t.Fatal(err)
	}

	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	if _, err := provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	for path, want := range map[string]os.FileMode{
		filepath.Join(root, "index.html"): 0o644,
		filepath.Join(css, "a.css"):       0o644,
		filepath.Join(root, "assets"):     0o755,
		css:                               0o755,
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Errorf("%s came back %04o, want %04o", path, got, want)
		}
	}
}
