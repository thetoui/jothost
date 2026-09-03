package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A provider wired to temporary directories, which is what lets these tests
// exercise the real archive writer, the real local store and the real restore
// rather than a mock of any of them.
func testProvider(t *testing.T) (*Provider, string, string) {
	t.Helper()

	base := t.TempDir()
	siteRoot := filepath.Join(base, "www")
	work := filepath.Join(base, "work")
	dest := filepath.Join(base, "backups")

	for _, dir := range []string{siteRoot, work, dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("prepare %s: %v", dir, err)
		}
	}

	provider := NewProvider(Options{
		WorkDir:    work,
		SiteRoot:   siteRoot,
		LocalRoots: []string{dest},
		Hostname:   "test-host",
		Now:        func() time.Time { return time.Date(2026, 9, 3, 3, 15, 0, 0, time.UTC) },
	})
	if !provider.Available() {
		t.Fatal("provider is not available")
	}
	return provider, siteRoot, dest
}

// writeSite lays out a small website under the site root.
func writeSite(t *testing.T, siteRoot, domain string, files map[string]string) string {
	t.Helper()

	root := filepath.Join(siteRoot, domain)
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func localDestination(dir string) Destination {
	return Destination{Kind: "local", Directory: dir}
}

func TestCreateWritesAnArchiveAndReadsItBack(t *testing.T) {
	// The whole point of the phase in one test: the archive exists at its
	// destination, and the provider confirmed it by reading it back rather than
	// by trusting the write.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{
		"index.php":         "<?php echo 'hello';",
		"wp-content/x.txt":  "content",
		"nested/deep/y.txt": "more",
	})

	result, err := provider.Create(context.Background(), Request{
		Type:        "website",
		Key:         "website/example.com/2026-09-03/031500.tar.gz",
		Subject:     "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if !result.Verified {
		t.Errorf("Create reported verified=false: %s", result.VerifyDetail)
	}
	if result.Size <= 0 {
		t.Error("Create reported a zero-byte archive")
	}
	if len(result.Checksum) != 64 {
		t.Errorf("checksum = %q, want 64 hex characters", result.Checksum)
	}
	if result.Manifest.FileCount != 3 {
		t.Errorf("file count = %d, want 3", result.Manifest.FileCount)
	}
	if len(result.Manifest.Sites) != 1 || result.Manifest.Sites[0].Domain != "example.com" {
		t.Errorf("manifest sites = %+v", result.Manifest.Sites)
	}

	stored := filepath.Join(dest, "website/example.com/2026-09-03/031500.tar.gz")
	if _, err := os.Stat(stored); err != nil {
		t.Fatalf("the archive is not at its destination: %v", err)
	}
}

func TestCreateRefusesADocumentRootOutsideTheSiteRoot(t *testing.T) {
	// Without this, "back up the site at /etc" would archive the host's
	// configuration — every credential in it — and send it to a destination the
	// same request chose.
	provider, siteRoot, dest := testProvider(t)
	writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "x"})

	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "shadow"), []byte("secret"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := provider.Create(context.Background(), Request{
		Type:        "website",
		Key:         "website/example.com/2026-09-03/031500.tar.gz",
		Subject:     "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: outside}},
		Destination: localDestination(dest),
	}, nil)
	if !errors.Is(err, ErrNothingToBackUp) {
		t.Fatalf("Create = %v, want a refusal", err)
	}
}

func TestCreateRefusesADocumentRootThatSymlinksOutOfTheSiteRoot(t *testing.T) {
	// A path check that only looked at the text would be satisfied by
	// /var/www/site -> /etc.
	provider, siteRoot, dest := testProvider(t)
	outside := t.TempDir()

	link := filepath.Join(siteRoot, "escape.com")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	_, err := provider.Create(context.Background(), Request{
		Type:        "website",
		Key:         "website/escape.com/2026-09-03/031500.tar.gz",
		Subject:     "escape.com",
		Sites:       []SiteSpec{{Domain: "escape.com", DocumentRoot: link}},
		Destination: localDestination(dest),
	}, nil)
	if err == nil {
		t.Fatal("Create followed a symlink out of the site root")
	}
}

func TestLocalDestinationOutsideTheAllowedRootsIsRefused(t *testing.T) {
	// Without this bound, "back up to /etc/nginx" would be a way to write a
	// file anywhere on the host as root.
	provider, siteRoot, _ := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "x"})

	elsewhere := t.TempDir()
	_, err := provider.Create(context.Background(), Request{
		Type:        "website",
		Key:         "website/example.com/2026-09-03/031500.tar.gz",
		Subject:     "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(elsewhere),
	}, nil)
	if !errors.Is(err, ErrDestinationFailed) {
		t.Fatalf("Create = %v, want ErrDestinationFailed", err)
	}
}

func TestVerifyCatchesAnArchiveSomebodyChanged(t *testing.T) {
	// The failure this phase exists to catch: the archive is still there, still
	// the right size, and no longer the same bytes.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "hello"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	stored := filepath.Join(dest, filepath.FromSlash(key))
	content, err := os.ReadFile(stored) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// One byte in the middle, so the length is unchanged: a check that only
	// compared sizes would pass.
	content[len(content)/2] ^= 0xff
	if err := os.WriteFile(stored, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	verified, err := provider.Verify(context.Background(), VerifyRequest{
		Key: key, Checksum: result.Checksum, Size: result.Size,
		Destination: localDestination(dest),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.OK {
		t.Fatal("Verify passed an archive that had been changed")
	}
	if verified.Detail == "" {
		t.Error("Verify gave no reason")
	}
}

func TestVerifyReportsAnArchiveThatIsNoLongerThere(t *testing.T) {
	// A bucket somebody emptied, or a retention rule on the storage provider's
	// side. The panel's record still says "verified" from the day it was
	// written, and only asking again finds it.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "hello"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.Remove(filepath.Join(dest, filepath.FromSlash(key))); err != nil {
		t.Fatalf("remove: %v", err)
	}

	verified, err := provider.Verify(context.Background(), VerifyRequest{
		Key: key, Checksum: result.Checksum, Destination: localDestination(dest),
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if verified.OK {
		t.Fatal("Verify passed a backup that is not there")
	}
	if !strings.Contains(verified.Detail, "not at its destination") {
		t.Errorf("detail = %q, want it to say the backup is missing", verified.Detail)
	}
}

func TestRestorePutsFilesBack(t *testing.T) {
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{
		"index.php":    "original",
		"assets/a.css": "body{}",
	})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// The site is then damaged, which is the situation a restore is for.
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("broken"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Remove(filepath.Join(root, "assets", "a.css")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	restored, err := provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored.SitesRestored) != 1 {
		t.Fatalf("sites restored = %v", restored.SitesRestored)
	}

	content, err := os.ReadFile(filepath.Join(root, "index.php")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "original" {
		t.Errorf("index.php = %q, want the archived content", content)
	}
	if _, err := os.Stat(filepath.Join(root, "assets", "a.css")); err != nil {
		t.Errorf("the deleted file was not restored: %v", err)
	}
}

func TestRestoreRefusesACorruptArchiveWithoutTouchingAnything(t *testing.T) {
	// A half-restored site is worse than an untouched broken one, because it
	// looks repaired. So the archive is verified in full before anything on the
	// host changes.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "original"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("live"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stored := filepath.Join(dest, filepath.FromSlash(key))
	content, err := os.ReadFile(stored) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	content[len(content)/2] ^= 0xff
	if err := os.WriteFile(stored, content, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err == nil {
		t.Fatal("Restore accepted a corrupt archive")
	}
	if !strings.Contains(err.Error(), "nothing has been changed") {
		t.Errorf("error = %v, want it to say nothing was changed", err)
	}

	// The live site is exactly as it was.
	live, err := os.ReadFile(filepath.Join(root, "index.php")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(live) != "live" {
		t.Errorf("index.php = %q, want the untouched live content", live)
	}
}

func TestRestoreKeepsWhatWasThereWhenAskedTo(t *testing.T) {
	// The first thing anybody wants after a restore that turns out to be the
	// wrong backup is what was there before.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "archived"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.php"), []byte("live"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	restored, err := provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum, KeepPrevious: true,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored.PreviousMoved) != 1 {
		t.Fatalf("previous kept = %v, want one directory", restored.PreviousMoved)
	}

	kept, err := os.ReadFile(filepath.Join(restored.PreviousMoved[0], "index.php")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read the kept copy: %v", err)
	}
	if string(kept) != "live" {
		t.Errorf("kept copy = %q, want what was live before the restore", kept)
	}
}

func TestRestoreRemovesWhatWasThereByDefault(t *testing.T) {
	// A complete second copy of every site on the same disk, left behind after
	// every restore, is how a panel fills a host.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "archived"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	restored, err := provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored.PreviousMoved) != 0 {
		t.Errorf("previous kept = %v, want none", restored.PreviousMoved)
	}

	entries, err := os.ReadDir(siteRoot)
	if err != nil {
		t.Fatalf("read site root: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "before-restore") {
			t.Errorf("a copy was left behind: %s", entry.Name())
		}
	}
}

func TestRestoreNamesWhatItDidNotPutBack(t *testing.T) {
	// A restore that put back less than somebody expected has to say so rather
	// than looking complete.
	provider, siteRoot, dest := testProvider(t)
	rootA := writeSite(t, siteRoot, "a.example", map[string]string{"index.php": "a"})
	rootB := writeSite(t, siteRoot, "b.example", map[string]string{"index.php": "b"})

	key := "full/server/2026-09-03/031500.tar.gz"
	result, err := provider.Create(context.Background(), Request{
		Type: "full", Key: key, Subject: "server",
		Sites: []SiteSpec{
			{Domain: "a.example", DocumentRoot: rootA},
			{Domain: "b.example", DocumentRoot: rootB},
		},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	restored, err := provider.Restore(context.Background(), RestoreRequest{
		Key: key, Checksum: result.Checksum,
		Sites:       []SiteSpec{{Domain: "a.example", DocumentRoot: rootA}},
		Destination: localDestination(dest),
	}, nil)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(restored.Skipped) != 1 || !strings.Contains(restored.Skipped[0], "b.example") {
		t.Errorf("skipped = %v, want b.example named", restored.Skipped)
	}
}

func TestSafeMemberPathRefusesEveryShapeOfEscape(t *testing.T) {
	// The tar equivalent of zip-slip, and the single most important function in
	// a restore: an archive is data from somewhere else.
	root := filepath.Clean(t.TempDir())
	for _, name := range []string{
		"../etc/shadow",
		"../../etc/shadow",
		"a/../../etc/shadow",
		"/etc/shadow",
		"a\\..\\..\\etc",
		"a/\x00b",
	} {
		if _, err := safeMemberPath(root, name); err == nil {
			t.Errorf("safeMemberPath accepted %q", name)
		}
	}

	target, err := safeMemberPath(root, "sites/example.com/index.php")
	if err != nil {
		t.Fatalf("safeMemberPath refused a good name: %v", err)
	}
	if !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		t.Errorf("target %q is not under %q", target, root)
	}
}

func TestSafeLinkTargetRefusesALinkOutOfTheDestination(t *testing.T) {
	// The danger is not the link but what a later member writes through it: an
	// archive with "site/config -> /etc" followed by "site/config/passwd"
	// writes to /etc/passwd through a path that passed every check on its own.
	root := filepath.Clean(t.TempDir())
	for _, target := range []string{"/etc", "../../etc", "../../../anything"} {
		if err := safeLinkTarget(root, "site/config", target); err == nil {
			t.Errorf("safeLinkTarget accepted a link to %q", target)
		}
	}
	// A link that goes up one level and back down is still inside the site, and
	// refusing it would break the ordinary "../shared/vendor" arrangement.
	if err := safeLinkTarget(root, "site/config", "../shared"); err != nil {
		t.Errorf("safeLinkTarget refused a link that stays inside: %v", err)
	}
}

func TestRestoreRefusesAnArchiveWithAnEscapingMember(t *testing.T) {
	// A hand-made archive, because this Agent will never write one — which is
	// exactly why the restore has to defend against it.
	provider, siteRoot, dest := testProvider(t)
	root := filepath.Join(siteRoot, "example.com")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	key := "website/example.com/2026-09-03/031500.tar.gz"
	stored := filepath.Join(dest, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(stored), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeHostileArchive(t, stored, "sites/example.com/../../../../etc/shadow")

	_, err := provider.Restore(context.Background(), RestoreRequest{
		Key:         key,
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil)
	if err == nil {
		t.Fatal("Restore accepted an archive with a member that escapes")
	}
	if !errors.Is(err, ErrArchiveMalformed) && !errors.Is(err, ErrCorrupt) {
		t.Errorf("error = %v, want it refused as malformed", err)
	}
}

// writeHostileArchive builds a valid tar.gz whose manifest declares one member
// with an escaping name.
func writeHostileArchive(t *testing.T, path, memberName string) {
	t.Helper()

	file, err := os.Create(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)

	body := []byte("root::0:0:::")
	if err := tw.WriteHeader(&tar.Header{
		Name: memberName, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatalf("write: %v", err)
	}

	manifest := `{"version":1,"type":"website","subject":"example.com",` +
		`"sites":[{"domain":"example.com","prefix":"sites/example.com"}],` +
		`"databases":[],"files":[],"skipped":[]}`
	if err := tw.WriteHeader(&tar.Header{
		Name: ManifestName, Mode: 0o600, Size: int64(len(manifest)), Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatalf("header: %v", err)
	}
	if _, err := tw.Write([]byte(manifest)); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestArchiveKeepsSymlinksAsLinksRatherThanFollowingThem(t *testing.T) {
	// Following them would duplicate whatever they point at, and a link out of
	// the document root would pull the rest of the host into a website backup.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{
		"real/file.txt": "content",
	})
	if err := os.Symlink("real/file.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	key := "website/example.com/2026-09-03/031500.tar.gz"
	if _, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	links := 0
	forEachMember(t, filepath.Join(dest, filepath.FromSlash(key)), func(header *tar.Header) {
		if header.Typeflag == tar.TypeSymlink {
			links++
			if header.Linkname != "real/file.txt" {
				t.Errorf("link target = %q", header.Linkname)
			}
		}
	})
	if links != 1 {
		t.Errorf("symlink members = %d, want 1", links)
	}
}

func TestTheManifestIsTheLastMember(t *testing.T) {
	// It has to be: the digests it records are not known until the members have
	// been written, and a manifest written first could only describe what was
	// intended.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{
		"a.txt": "a", "b.txt": "b", "c.txt": "c",
	})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	if _, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	last := ""
	forEachMember(t, filepath.Join(dest, filepath.FromSlash(key)), func(header *tar.Header) {
		last = header.Name
	})
	if last != ManifestName {
		t.Errorf("last member = %q, want %q", last, ManifestName)
	}
}

func forEachMember(t *testing.T, path string, fn func(*tar.Header)) {
	t.Helper()

	file, err := os.Open(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = file.Close() }()

	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer func() { _ = gz.Close() }()

	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("tar: %v", err)
		}
		fn(header)
	}
}

func TestCreateRefusesWhenThereIsNothingToArchive(t *testing.T) {
	provider, _, dest := testProvider(t)
	_, err := provider.Create(context.Background(), Request{
		Type:        "full",
		Key:         "full/server/2026-09-03/031500.tar.gz",
		Subject:     "server",
		Destination: localDestination(dest),
	}, nil)
	if !errors.Is(err, ErrNothingToBackUp) {
		t.Fatalf("Create = %v, want ErrNothingToBackUp", err)
	}
}

func TestDeleteIsIdempotent(t *testing.T) {
	// Retention runs repeatedly, and an object somebody already removed by hand
	// must not make it fail on every run afterwards.
	provider, siteRoot, dest := testProvider(t)
	root := writeSite(t, siteRoot, "example.com", map[string]string{"index.php": "x"})

	key := "website/example.com/2026-09-03/031500.tar.gz"
	if _, err := provider.Create(context.Background(), Request{
		Type: "website", Key: key, Subject: "example.com",
		Sites:       []SiteSpec{{Domain: "example.com", DocumentRoot: root}},
		Destination: localDestination(dest),
	}, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := provider.Delete(context.Background(), key, localDestination(dest)); err != nil {
			t.Fatalf("Delete (attempt %d): %v", i+1, err)
		}
	}
}

func TestCheckDestinationWritesReadsAndTidiesUp(t *testing.T) {
	// This is what turns "the operator typed a directory" into "the panel has
	// written there and read the same bytes back".
	provider, _, dest := testProvider(t)

	if err := provider.CheckDestination(context.Background(), localDestination(dest)); err != nil {
		t.Fatalf("CheckDestination: %v", err)
	}

	// Nothing is left behind: a probe object per check is litter this panel
	// produced in somebody's bucket.
	var found []string
	_ = filepath.Walk(dest, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			found = append(found, path)
		}
		return nil
	})
	if len(found) != 0 {
		t.Errorf("the check left files behind: %v", found)
	}
}

func TestCheckDestinationFailsOnADirectoryItCannotUse(t *testing.T) {
	provider, _, _ := testProvider(t)
	err := provider.CheckDestination(context.Background(),
		localDestination("/definitely/not/an/allowed/root"))
	if err == nil {
		t.Fatal("CheckDestination accepted a directory outside the allowed roots")
	}
}

func TestKeyIsReadableWithoutThePanel(t *testing.T) {
	// A backup nobody can find without the software that made it is a backup
	// that fails on the day the software is what broke.
	at := time.Date(2026, 9, 3, 3, 15, 0, 0, time.UTC)
	key := Key("prefix", "website", "example.com", at, "abc123")
	want := "prefix/website/example.com/2026-09-03/031500-abc123.tar.gz"
	if key != want {
		t.Errorf("Key = %q, want %q", key, want)
	}
}

func TestKeySegmentsSurviveAnAwkwardName(t *testing.T) {
	// A site or database whose name is unusual still gets backed up: the
	// segment is reduced to something a key accepts rather than refused.
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	key := Key("", "database", "my db/../weird name", at, "id")
	if strings.Contains(key, "..") || strings.Contains(key, " ") {
		t.Errorf("Key produced something unsafe: %q", key)
	}
}

func TestS3PutSignsTheRequestAndSendsTheArchive(t *testing.T) {
	// The signature is the only hard part of talking to S3, so it is exercised
	// against a server that checks the shape of what arrives rather than mocked
	// away.
	var (
		gotAuth   string
		gotDigest string
		gotPath   string
		gotBody   []byte
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotDigest = r.Header.Get("X-Amz-Content-Sha256")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	provider, _, _ := testProvider(t)
	store, err := provider.s3Store(Destination{
		Kind: "s3", Endpoint: server.URL, Bucket: "backups", Region: "us-east-1",
		AccessKey: "AKIAEXAMPLE", SecretKey: "secret", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3Store: %v", err)
	}

	source := filepath.Join(t.TempDir(), "archive.tar.gz")
	if err := os.WriteFile(source, []byte("archive bytes"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	key := "website/example.com/2026-09-03/031500.tar.gz"
	if err := store.Put(context.Background(), key, source); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/") {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.Contains(gotAuth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("signed headers are wrong: %q", gotAuth)
	}
	// The payload digest in the header must be the archive's own, which is what
	// lets the service reject a body that was changed in flight.
	want, _, err := digestFile(source)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	if gotDigest != want {
		t.Errorf("payload digest = %q, want %q", gotDigest, want)
	}
	if gotPath != "/backups/"+key {
		t.Errorf("path = %q, want the bucket then the key", gotPath)
	}
	if string(gotBody) != "archive bytes" {
		t.Errorf("body = %q", gotBody)
	}
}

func TestS3ReportsAMissingObjectAsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	provider, _, _ := testProvider(t)
	store, err := provider.s3Store(Destination{
		Kind: "s3", Endpoint: server.URL, Bucket: "backups", Region: "us-east-1",
		AccessKey: "k", SecretKey: "s", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3Store: %v", err)
	}

	_, err = store.Stat(context.Background(), "website/x/2026-01-01/000000.tar.gz")
	if !IsNotFound(err) {
		t.Fatalf("Stat = %v, want ErrNotFound", err)
	}
}

func TestS3IncludesTheServicesOwnErrorInAFailure(t *testing.T) {
	// The service names the bucket, the key and the reason far better than any
	// message written here could, and an operator debugging a destination needs
	// it.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("<Error><Code>SignatureDoesNotMatch</Code></Error>"))
	}))
	defer server.Close()

	provider, _, _ := testProvider(t)
	store, err := provider.s3Store(Destination{
		Kind: "s3", Endpoint: server.URL, Bucket: "backups", Region: "us-east-1",
		AccessKey: "k", SecretKey: "s", PathStyle: true,
	})
	if err != nil {
		t.Fatalf("s3Store: %v", err)
	}

	_, err = store.Stat(context.Background(), "website/x/2026-01-01/000000.tar.gz")
	if err == nil {
		t.Fatal("Stat succeeded against a 403")
	}
	// A HEAD has no body, so the code comes from the status; a GET carries the
	// XML. Both must name something an operator can act on.
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error = %v, want the status in it", err)
	}
}

func TestURIEncodingMatchesTheSignatureRules(t *testing.T) {
	// Slashes stay, unreserved characters stay, everything else becomes
	// two-digit uppercase percent-encoding. A byte below 0x10 written as one
	// digit produces a signature that is wrong only for those bytes — the kind
	// of bug that works for a year and then does not.
	cases := map[string]string{
		"/backups/a-b_c.d~e": "/backups/a-b_c.d~e",
		"/a b":               "/a%20b",
		"/a+b":               "/a%2Bb",
		"/a\nb":              "/a%0Ab",
		"/a=b":               "/a%3Db",
	}
	for input, want := range cases {
		if got := uriEncodePath(input); got != want {
			t.Errorf("uriEncodePath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSFTPParentDirsBuildsEachLevel(t *testing.T) {
	// sftp has no "mkdir -p", so each level is created in turn.
	got := parentDirs("/srv/backups/website/example.com/2026-09-03/031500.tar.gz")
	want := []string{
		"/srv", "/srv/backups", "/srv/backups/website",
		"/srv/backups/website/example.com",
		"/srv/backups/website/example.com/2026-09-03",
	}
	if len(got) != len(want) {
		t.Fatalf("parentDirs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("parentDirs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSFTPRefusesADestinationWithNoHostKey(t *testing.T) {
	// A backup sent to whatever answered on port 22 is a copy of every site on
	// the host handed to a stranger.
	provider, _, _ := testProvider(t)
	_, err := provider.sftpStore(Destination{
		Kind: "sftp", Host: "backup.example.com", User: "backup",
		PrivateKey: "-----BEGIN OPENSSH PRIVATE KEY-----\nx\n-----END OPENSSH PRIVATE KEY-----",
	})
	if err == nil {
		t.Fatal("an SFTP destination with no host key was accepted")
	}
}

func TestParseListingSizeReadsTheServersFormat(t *testing.T) {
	output := "-rw-------    1 backup   backup       4096 Sep  3 03:15 031500.tar.gz"
	size, ok := parseListingSize(output)
	if !ok || size != 4096 {
		t.Errorf("parseListingSize = %d, %v; want 4096, true", size, ok)
	}
	if _, ok := parseListingSize("nothing useful"); ok {
		t.Error("parseListingSize claimed to read a size it could not")
	}
}

func TestCapabilitiesSayWhatIsMissingRatherThanHidingIt(t *testing.T) {
	// A destination kind that cannot work has to be greyed out with a reason,
	// not accepted and failed at the first scheduled run.
	provider := NewProvider(Options{})
	caps := provider.Capabilities()
	if caps.Available {
		t.Error("a provider with no working directory claimed to be available")
	}
	if caps.Reason == "" {
		t.Error("capabilities gave no reason")
	}
}
