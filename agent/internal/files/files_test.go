package files_test

import (
	"archive/zip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/files"
)

// tree builds a manager over a temporary root with some content in it.
func tree(t *testing.T) (*files.Manager, string) {
	t.Helper()

	root := t.TempDir()
	// EvalSymlinks because macOS and some CI images hand out a /var/... temp
	// directory that is itself a symlink to /private/var/..., and the manager
	// resolves paths before comparing them.
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}

	mustMkdir(t, filepath.Join(resolved, "site"))
	mustMkdir(t, filepath.Join(resolved, "site", "public"))
	mustWrite(t, filepath.Join(resolved, "site", "public", "index.php"), "<?php echo 1;")
	mustWrite(t, filepath.Join(resolved, "site", "public", "style.css"), "body{}")
	mustWrite(t, filepath.Join(resolved, "site", "notes.txt"), "alpha\nbeta needle here\ngamma\n")

	manager := files.NewManager(files.ManagerOptions{Roots: []string{resolved}})
	if !manager.Available() {
		t.Fatal("manager is not available over a real directory")
	}
	return manager, resolved
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// ------------------------------------------------------ security: traversal

// TASKS.md Phase 7: path traversal test.
//
// Each of these resolves outside the root, or is malformed in a way that a
// naive join would accept. The Agent runs as root, so one that got through
// would be a full compromise rather than a bug.
func TestPathTraversalIsRefused(t *testing.T) {
	manager, root := tree(t)

	hostile := []string{
		"/etc/passwd",
		"/etc/shadow",
		root + "/../../../etc/passwd",
		root + "/site/../../etc/passwd",
		root + "/site/public/../../../../etc/passwd",
		"relative/path",
		"",
		root + "/site\x00/../../etc/passwd",
		"/",
	}

	for _, path := range hostile {
		if _, err := manager.List(path, 0, 0); err == nil {
			t.Fatalf("List(%q) was allowed", path)
		}
		if _, err := manager.Stat(path); err == nil {
			t.Fatalf("Stat(%q) was allowed", path)
		}
		if _, err := manager.Read(path, 0, 1024); err == nil {
			t.Fatalf("Read(%q) was allowed", path)
		}
		if _, err := manager.Write(files.WriteRequest{Path: path, Data: []byte("x")}); err == nil {
			t.Fatalf("Write(%q) was allowed", path)
		}
		if err := manager.Delete(path, true); err == nil {
			t.Fatalf("Delete(%q) was allowed", path)
		}
	}
}

// TASKS.md Phase 7: symlink escape test.
//
// pathsec resolves symlinks before comparing against the root, so a link
// planted inside the tree cannot be used as a door out of it. This asserts the
// property at the file manager's own boundary, because that is where a caller
// reaches it.
func TestSymlinkEscapeIsRefused(t *testing.T) {
	manager, root := tree(t)

	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	mustWrite(t, secret, "the host's business")

	// A link to a file outside the root, and a link to the directory itself.
	link := filepath.Join(root, "site", "public", "escape.txt")
	if err := os.Symlink(secret, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}
	dirLink := filepath.Join(root, "site", "public", "escape-dir")
	if err := os.Symlink(outside, dirLink); err != nil {
		t.Fatalf("symlink directory: %v", err)
	}

	if _, err := manager.Read(link, 0, 1024); err == nil {
		t.Fatal("reading through a symlink that leaves the root was allowed")
	}
	if _, err := manager.List(dirLink, 0, 0); err == nil {
		t.Fatal("listing through a symlink that leaves the root was allowed")
	}
	if _, err := manager.Read(filepath.Join(dirLink, "secret.txt"), 0, 1024); err == nil {
		t.Fatal("reading a file under an escaping symlink was allowed")
	}
	if _, err := manager.Write(files.WriteRequest{
		Path: filepath.Join(dirLink, "planted.txt"), Data: []byte("x"),
	}); err == nil {
		t.Fatal("writing under an escaping symlink was allowed")
	}

	// The link is still listed, so a person can see it exists and delete it.
	listing, err := manager.List(filepath.Join(root, "site", "public"), 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found bool
	for _, entry := range listing.Entries {
		if entry.Name == "escape.txt" {
			found = true
			if entry.Type != files.TypeSymlink {
				t.Fatalf("escape.txt is reported as %q, want a symlink", entry.Type)
			}
			if entry.TargetInsideRoot {
				t.Fatal("a link out of the root is reported as pointing inside it")
			}
		}
	}
	if !found {
		t.Fatal("a symlink is hidden from the listing rather than shown as a symlink")
	}
}

// TASKS.md Phase 7: permission test.
func TestPermissionsAreValidated(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public", "index.php")

	// setuid on a file in a document root is a local root exploit waiting for
	// someone to run it.
	for _, mode := range []string{"4755", "2755", "1777", "04755"} {
		if _, err := manager.Chmod(target, mode, false); err == nil {
			t.Fatalf("mode %q was accepted", mode)
		}
	}

	for _, mode := range []string{"", "abc", "999", "0o644", "-1", "77777"} {
		if _, err := manager.Chmod(target, mode, false); err == nil {
			t.Fatalf("malformed mode %q was accepted", mode)
		}
	}

	entry, err := manager.Chmod(target, "0640", false)
	if err != nil {
		t.Fatalf("chmod 0640: %v", err)
	}
	if entry.Mode != "0640" {
		t.Fatalf("mode = %q, want 0640", entry.Mode)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("on disk the mode is %04o, want 0640", info.Mode().Perm())
	}
}

// A manager with no roots must refuse everything rather than allow everything.
func TestAManagerWithoutRootsIsUnavailable(t *testing.T) {
	manager := files.NewManager(files.ManagerOptions{})

	if manager.Available() {
		t.Fatal("a manager with no roots reports itself available")
	}
	if _, err := manager.List("/", 0, 0); err == nil {
		t.Fatal("an unconfigured manager listed a directory")
	}
	if _, err := manager.Write(files.WriteRequest{Path: "/tmp/x", Data: []byte("x")}); err == nil {
		t.Fatal("an unconfigured manager wrote a file")
	}
}

// A path the panel manages itself must not be editable through the file
// manager: a vhost rewritten here takes down every site on the host.
func TestProtectedPathsCannotBeChanged(t *testing.T) {
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	protected := filepath.Join(resolved, "managed")
	mustMkdir(t, protected)
	mustWrite(t, filepath.Join(protected, "vhost.conf"), "server {}")

	manager := files.NewManager(files.ManagerOptions{
		Roots:     []string{resolved},
		Protected: []string{protected},
	})

	if err := manager.Delete(filepath.Join(protected, "vhost.conf"), false); err == nil {
		t.Fatal("a protected file was deleted")
	}
	if _, err := manager.Write(files.WriteRequest{
		Path: filepath.Join(protected, "vhost.conf"), Data: []byte("x"),
	}); err == nil {
		t.Fatal("a protected file was overwritten")
	}
	if _, err := manager.Chmod(protected, "0777", true); err == nil {
		t.Fatal("a protected directory's permissions were changed")
	}

	// It is still visible: hiding it would be more confusing than refusing it.
	if _, err := manager.Stat(filepath.Join(protected, "vhost.conf")); err != nil {
		t.Fatalf("a protected file should still be readable: %v", err)
	}
}

// ------------------------------------------------------------------ listing

func TestListPutsDirectoriesFirst(t *testing.T) {
	manager, root := tree(t)

	listing, err := manager.List(filepath.Join(root, "site"), 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listing.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(listing.Entries))
	}
	if listing.Entries[0].Name != "public" || listing.Entries[0].Type != files.TypeDirectory {
		t.Fatalf("first entry = %+v, want the directory", listing.Entries[0])
	}
	if listing.Parent == "" {
		t.Fatal("a directory below the root should report a parent")
	}
}

// The root has no parent a caller may navigate to; offering one invites a
// request the manager will refuse.
func TestListOfTheRootHasNoParent(t *testing.T) {
	manager, root := tree(t)

	listing, err := manager.List(root, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if listing.Parent != "" {
		t.Fatalf("parent = %q, want empty at the root", listing.Parent)
	}
}

// A response has to fit the socket's 1 MiB limit, so a large directory is
// paged rather than returned whole.
func TestListIsPagedAndReportsTruncation(t *testing.T) {
	manager, root := tree(t)
	many := filepath.Join(root, "many")
	mustMkdir(t, many)
	for i := 0; i < 25; i++ {
		mustWrite(t, filepath.Join(many, string(rune('a'+i%26))+"-"+itoa(i)+".txt"), "x")
	}

	first, err := manager.List(many, 0, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first.Entries) != 10 {
		t.Fatalf("page size = %d, want 10", len(first.Entries))
	}
	if first.Total != 25 {
		t.Fatalf("total = %d, want 25", first.Total)
	}
	if !first.Truncated {
		t.Fatal("a partial listing must say so")
	}

	last, err := manager.List(many, 20, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(last.Entries) != 5 {
		t.Fatalf("final page = %d entries, want 5", len(last.Entries))
	}
	if last.Truncated {
		t.Fatal("the final page must not be marked truncated")
	}

	// Paging must not repeat or skip: the order is stable.
	if first.Entries[0].Name == last.Entries[0].Name {
		t.Fatal("paging returned the same entry twice")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

// ------------------------------------------------------------- read / write

func TestReadReturnsChunksAndReportsEOF(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "big.bin")
	content := strings.Repeat("abcdefgh", 1000) // 8000 bytes
	mustWrite(t, target, content)

	first, err := manager.Read(target, 0, 3000)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(first.Data) != 3000 {
		t.Fatalf("read %d bytes, want 3000", len(first.Data))
	}
	if first.Size != 8000 {
		t.Fatalf("size = %d, want 8000", first.Size)
	}
	if first.EOF {
		t.Fatal("the first chunk of a 8000 byte file is not the end")
	}

	last, err := manager.Read(target, 6000, 3000)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(last.Data) != 2000 {
		t.Fatalf("tail = %d bytes, want 2000", len(last.Data))
	}
	if !last.EOF {
		t.Fatal("the chunk reaching the end must say so")
	}

	// Reading past the end is an empty chunk, not an error: a client walking
	// offsets must be able to stop cleanly.
	past, err := manager.Read(target, 9000, 100)
	if err != nil {
		t.Fatalf("read past end: %v", err)
	}
	if len(past.Data) != 0 || !past.EOF {
		t.Fatalf("past the end = %d bytes, eof=%v", len(past.Data), past.EOF)
	}
}

// Reading a device would return bytes forever and never reach EOF.
func TestReadRefusesSomethingThatIsNotARegularFile(t *testing.T) {
	manager, root := tree(t)

	if _, err := manager.Read(filepath.Join(root, "site"), 0, 100); err == nil {
		t.Fatal("reading a directory was allowed")
	}
}

func TestWriteInChunksBuildsAWholeFile(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public", "upload.bin")

	first := strings.Repeat("A", 1000)
	second := strings.Repeat("B", 500)

	if _, err := manager.Write(files.WriteRequest{
		Path: target, Offset: 0, Data: []byte(first), Truncate: true,
	}); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	entry, err := manager.Write(files.WriteRequest{
		Path: target, Offset: 1000, Data: []byte(second), Final: true,
	})
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if entry.Size != 1500 {
		t.Fatalf("size = %d, want 1500", entry.Size)
	}

	content, err := os.ReadFile(target) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != first+second {
		t.Fatal("the reassembled file does not match what was written")
	}
}

// Replacing a large file with a smaller one must not leave the old tail behind.
func TestFinalChunkTruncatesAnOldTail(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public", "replace.txt")
	mustWrite(t, target, strings.Repeat("old", 1000))

	if _, err := manager.Write(files.WriteRequest{
		Path: target, Offset: 0, Data: []byte("new"), Truncate: true, Final: true,
	}); err != nil {
		t.Fatalf("write: %v", err)
	}

	content, err := os.ReadFile(target) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "new" {
		t.Fatalf("content = %q, want the old tail gone", string(content))
	}
}

func TestWriteRefusesAnOversizedChunk(t *testing.T) {
	manager, root := tree(t)

	_, err := manager.Write(files.WriteRequest{
		Path: filepath.Join(root, "site", "huge.bin"),
		Data: make([]byte, files.MaxChunkBytes+1),
	})
	if err == nil {
		t.Fatal("a chunk larger than the transport allows was accepted")
	}
}

// ----------------------------------------------------------- create, delete

func TestCreateRefusesToClobberAnExistingFile(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public", "index.php")

	if _, err := manager.Create(target); err == nil {
		t.Fatal("creating over an existing file was allowed")
	}

	content, err := os.ReadFile(target) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(content) != "<?php echo 1;" {
		t.Fatal("the existing file was truncated by a refused create")
	}
}

func TestMkdirIsIdempotent(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public", "assets")

	if _, err := manager.Mkdir(target, false); err != nil {
		t.Fatalf("first mkdir: %v", err)
	}
	if _, err := manager.Mkdir(target, false); err != nil {
		t.Fatalf("second mkdir must reconcile, not fail: %v", err)
	}
	// But not over a file, which would report a directory that is not there.
	if _, err := manager.Mkdir(filepath.Join(root, "site", "notes.txt"), false); err == nil {
		t.Fatal("mkdir over an existing file was allowed")
	}
}

func TestDeletingANonEmptyDirectoryNeedsRecursive(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public")

	if err := manager.Delete(target, false); err == nil {
		t.Fatal("a non-empty directory was deleted without asking")
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("the directory was removed by a refused delete")
	}
	if err := manager.Delete(target, true); err != nil {
		t.Fatalf("recursive delete: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the directory survived a recursive delete")
	}
}

// ------------------------------------------------------------- copy / move

func TestCopyDuplicatesATree(t *testing.T) {
	manager, root := tree(t)

	if _, err := manager.Copy(
		filepath.Join(root, "site", "public"),
		filepath.Join(root, "site", "backup"), false); err != nil {
		t.Fatalf("copy: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(root, "site", "backup", "index.php")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(content) != "<?php echo 1;" {
		t.Fatal("the copy does not match the original")
	}
	// The original must still be there.
	if _, err := os.Stat(filepath.Join(root, "site", "public", "index.php")); err != nil {
		t.Fatal("copy removed the source")
	}
}

// Copying a directory into itself recurses until the disk is full.
func TestCopyRefusesADestinationInsideTheSource(t *testing.T) {
	manager, root := tree(t)

	_, err := manager.Copy(
		filepath.Join(root, "site"),
		filepath.Join(root, "site", "public", "nested"), false)
	if err == nil {
		t.Fatal("copying a directory into itself was allowed")
	}
}

func TestMoveRenamesWithinTheRoot(t *testing.T) {
	manager, root := tree(t)
	from := filepath.Join(root, "site", "notes.txt")
	to := filepath.Join(root, "site", "public", "notes.txt")

	if _, err := manager.Move(from, to, false); err != nil {
		t.Fatalf("move: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatal("the source survived a move")
	}
	if _, err := os.Stat(to); err != nil {
		t.Fatal("the destination is missing after a move")
	}
}

func TestMoveRefusesToOverwriteUnlessAsked(t *testing.T) {
	manager, root := tree(t)

	_, err := manager.Move(
		filepath.Join(root, "site", "public", "style.css"),
		filepath.Join(root, "site", "public", "index.php"), false)
	if err == nil {
		t.Fatal("a move silently replaced an existing file")
	}

	content, err := os.ReadFile(filepath.Join(root, "site", "public", "index.php")) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "<?php echo 1;" {
		t.Fatal("the destination was overwritten by a refused move")
	}
}

// A move out of the root would put a site's file somewhere the panel can never
// reach again — and, with the Agent running as root, anywhere at all.
func TestMoveOutOfTheRootIsRefused(t *testing.T) {
	manager, root := tree(t)
	outside := filepath.Join(t.TempDir(), "stolen.txt")

	if _, err := manager.Move(filepath.Join(root, "site", "notes.txt"), outside, false); err == nil {
		t.Fatal("a file was moved outside the root")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("the file landed outside the root")
	}
}

// ------------------------------------------------------------------ archive

func TestArchiveAndExtractRoundTrip(t *testing.T) {
	manager, root := tree(t)
	archive := filepath.Join(root, "site", "public.zip")

	result, err := manager.Archive([]string{filepath.Join(root, "site", "public")}, archive)
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if result.Entries == 0 {
		t.Fatal("the archive is empty")
	}

	extracted, err := manager.Extract(archive, filepath.Join(root, "site", "restored"))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if extracted.Entries == 0 {
		t.Fatal("nothing was extracted")
	}

	content, err := os.ReadFile( //nolint:gosec // test path
		filepath.Join(root, "site", "restored", "public", "index.php"))
	if err != nil {
		t.Fatalf("read extracted: %v", err)
	}
	if string(content) != "<?php echo 1;" {
		t.Fatal("the extracted file does not match the original")
	}
}

// Zip slip: an entry named ../../etc/cron.d/evil escapes the destination on any
// extractor that joins names blindly. The Agent runs as root, so this is the
// single most dangerous input the file manager accepts.
func TestExtractRefusesZipSlip(t *testing.T) {
	manager, root := tree(t)

	for _, name := range []string{
		"../escaped.txt",
		"../../escaped.txt",
		"../../../../../../etc/cron.d/evil",
		"/etc/cron.d/absolute",
		"nested/../../escaped.txt",
	} {
		archive := filepath.Join(root, "site", "slip.zip")
		writeZip(t, archive, map[string]string{name: "payload"})

		_, err := manager.Extract(archive, filepath.Join(root, "site", "out"))
		if err == nil {
			t.Fatalf("an archive entry named %q was extracted", name)
		}
		if _, statErr := os.Stat(filepath.Join(root, "escaped.txt")); statErr == nil {
			t.Fatalf("entry %q escaped the destination", name)
		}
		if err := os.Remove(archive); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}

// A symlink inside an archive is a delayed escape: it extracts harmlessly and
// then redirects the next write out of the tree.
func TestExtractRefusesASymlinkEntry(t *testing.T) {
	manager, root := tree(t)
	archive := filepath.Join(root, "site", "link.zip")

	handle, err := os.Create(archive) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	writer := zip.NewWriter(handle)
	header := &zip.FileHeader{Name: "escape"}
	header.SetMode(os.ModeSymlink | 0o777)
	entry, err := writer.CreateHeader(header)
	if err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if _, err := entry.Write([]byte("/etc/passwd")); err != nil {
		t.Fatalf("write entry: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close archive: %v", err)
	}

	if _, err := manager.Extract(archive, filepath.Join(root, "site", "out")); err == nil {
		t.Fatal("an archive containing a symlink was extracted")
	}
}

func TestExtractRefusesSomethingThatIsNotAZip(t *testing.T) {
	manager, root := tree(t)

	_, err := manager.Extract(filepath.Join(root, "site", "notes.txt"),
		filepath.Join(root, "site", "out"))
	if err == nil {
		t.Fatal("a text file was extracted as an archive")
	}
}

// writeZip builds a zip containing the given entries.
func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()

	handle, err := os.Create(path) //nolint:gosec // test path
	if err != nil {
		t.Fatalf("create zip: %v", err)
	}
	writer := zip.NewWriter(handle)
	for name, content := range entries {
		// CreateHeader rather than Create: Create sanitises the name, which
		// would defeat the very thing this is testing.
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatalf("create entry %q: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if err := handle.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
}

// ------------------------------------------------------------------- search

func TestSearchFindsByName(t *testing.T) {
	manager, root := tree(t)

	result, err := manager.Search(context.Background(), files.SearchRequest{
		Path: root, Query: "index",
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d, want 1: %+v", len(result.Matches), result.Matches)
	}
	if result.Matches[0].Entry.Name != "index.php" {
		t.Fatalf("match = %q", result.Matches[0].Entry.Name)
	}
}

func TestSearchFindsContentWithItsLine(t *testing.T) {
	manager, root := tree(t)

	result, err := manager.Search(context.Background(), files.SearchRequest{
		Path: root, Query: "needle", Content: true,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(result.Matches))
	}
	match := result.Matches[0]
	if match.LineNumber != 2 {
		t.Fatalf("line number = %d, want 2", match.LineNumber)
	}
	if !strings.Contains(match.Line, "needle") {
		t.Fatalf("line = %q, should contain the match", match.Line)
	}
}

// An empty query matches every file on the host.
func TestSearchRefusesAnEmptyQuery(t *testing.T) {
	manager, root := tree(t)

	if _, err := manager.Search(context.Background(), files.SearchRequest{Path: root}); err == nil {
		t.Fatal("an empty search was allowed")
	}
	if _, err := manager.Search(context.Background(), files.SearchRequest{
		Path: root, Query: "   ",
	}); err == nil {
		t.Fatal("a whitespace-only search was allowed")
	}
}

func TestSearchStopsAtItsLimit(t *testing.T) {
	manager, root := tree(t)
	many := filepath.Join(root, "many")
	mustMkdir(t, many)
	for i := 0; i < 30; i++ {
		mustWrite(t, filepath.Join(many, "match-"+itoa(i)+".txt"), "x")
	}

	result, err := manager.Search(context.Background(), files.SearchRequest{
		Path: root, Query: "match", Limit: 5,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(result.Matches) != 5 {
		t.Fatalf("matches = %d, want the limit of 5", len(result.Matches))
	}
	if !result.Truncated {
		t.Fatal("a search stopped by its limit must say so")
	}
}

// A cancelled search returns what it found rather than discarding it.
func TestSearchHonoursCancellation(t *testing.T) {
	manager, root := tree(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := manager.Search(ctx, files.SearchRequest{Path: root, Query: "index"})
	if err != nil {
		t.Fatalf("a cancelled search must not error: %v", err)
	}
	if !result.Truncated {
		t.Fatal("a cancelled search must be reported as incomplete")
	}
}

// A symlink loop back to an ancestor turns a walk into an infinite one.
func TestSearchDoesNotFollowSymlinks(t *testing.T) {
	manager, root := tree(t)

	if err := os.Symlink(root, filepath.Join(root, "site", "loop")); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := manager.Search(context.Background(), files.SearchRequest{
			Path: root, Query: "index",
		}); err != nil {
			t.Errorf("search: %v", err)
		}
	}()

	select {
	case <-done:
	case <-timeout(t):
		t.Fatal("the search did not finish: a symlink loop was followed")
	}
}

// timeout returns a channel that fires after a bound generous enough that only
// a genuinely stuck walk trips it.
func timeout(t *testing.T) <-chan time.Time {
	t.Helper()
	return time.After(30 * time.Second)
}

// ------------------------------------------------------------ the root itself

// Deleting the root deletes every website on the host in one request.
//
// pathsec accepts a root as being inside itself, which is right for reading and
// for creating things in it — and catastrophic for delete. This was found by
// the Phase 7 integration suite, which asked the live API to delete /var/www
// and watched it succeed.
func TestTheRootItselfCannotBeDeleted(t *testing.T) {
	manager, root := tree(t)

	if err := manager.Delete(root, true); err == nil {
		t.Fatal("the file manager deleted its own root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatal("the root is gone after a delete that should have been refused")
	}
	// Everything under it must still be there.
	if _, err := os.Stat(filepath.Join(root, "site", "public", "index.php")); err != nil {
		t.Fatal("the contents of the root were removed")
	}
}

// Moving the root away is the same loss as deleting it.
func TestTheRootItselfCannotBeMoved(t *testing.T) {
	manager, root := tree(t)

	if _, err := manager.Move(root, filepath.Join(root, "site", "moved"), false); err == nil {
		t.Fatal("the file manager moved its own root")
	}
	if _, err := os.Stat(filepath.Join(root, "site", "public", "index.php")); err != nil {
		t.Fatal("the root's contents were moved")
	}
}

// A directory inside the root is still removable: that is the file manager
// doing its job, and the guard must not have made everything read-only.
func TestADirectoryInsideTheRootIsStillRemovable(t *testing.T) {
	manager, root := tree(t)
	target := filepath.Join(root, "site", "public")

	if err := manager.Delete(target, true); err != nil {
		t.Fatalf("an ordinary directory could not be deleted: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the directory survived")
	}
}
