package pathsec

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func newValidator(t *testing.T, roots ...string) *Validator {
	t.Helper()

	v, err := NewValidator(roots...)
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	return v
}

// TestCleanRejectsTraversal is the core of the Phase 2 path-traversal
// requirement: the Agent runs as root, so a path that escapes its root is a
// full compromise rather than a bug.
func TestCleanRejectsTraversal(t *testing.T) {
	traversals := []string{
		"/var/www/../../etc/shadow",
		"/var/www/..",
		"/..",
		"/var/../etc/passwd",
		"/var/www/./../../root/.ssh/id_rsa",
		"/a/b/c/../../../../etc/hosts",
	}

	for _, path := range traversals {
		if _, err := Clean(path); !errors.Is(err, ErrTraversal) {
			t.Fatalf("path %q must be rejected as traversal, got %v", path, err)
		}
	}
}

func TestCleanRejectsMalformedPaths(t *testing.T) {
	cases := []struct {
		name, path string
		want       error
	}{
		{"empty", "", ErrEmptyPath},
		{"relative", "var/www", ErrNotAbsolute},
		{"dot relative", "./var", ErrNotAbsolute},
		// A null byte truncates the path inside the kernel, so a check on the
		// Go string could pass while the syscall sees something else entirely.
		{"null byte", "/var/www\x00/../../etc/shadow", ErrNullByte},
	}

	for _, tc := range cases {
		_, err := Clean(tc.path)
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.want, err)
		}
	}
}

func TestCleanRejectsOversizedPath(t *testing.T) {
	long := "/" + string(make([]byte, maxPathLength))
	for i := 1; i < len(long); i++ {
		long = long[:i] + "a" + long[i+1:]
	}

	if _, err := Clean(long); err == nil {
		t.Fatal("an oversized path must be rejected")
	}
}

func TestCleanNormalisesRedundantSeparators(t *testing.T) {
	cleaned, err := Clean("/var//www/./site/")
	if err != nil {
		t.Fatalf("Clean: %v", err)
	}
	if cleaned != "/var/www/site" {
		t.Fatalf("Clean = %q, want /var/www/site", cleaned)
	}
}

func TestResolveAcceptsPathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	v := newValidator(t, root)

	inside := filepath.Join(root, "site", "index.php")
	resolved, err := v.Resolve(inside)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != inside {
		t.Fatalf("Resolve = %q, want %q", resolved, inside)
	}

	// The root itself is inside its own root.
	if _, err := v.Resolve(root); err != nil {
		t.Fatalf("the root must resolve: %v", err)
	}
}

func TestResolveRejectsPathsOutsideRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "allowed")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	v := newValidator(t, root)

	for _, path := range []string{"/etc/shadow", "/", "/tmp", filepath.Dir(root)} {
		if _, err := v.Resolve(path); !errors.Is(err, ErrOutsideRoot) {
			t.Fatalf("path %q must be outside the root, got %v", path, err)
		}
	}
}

func TestResolveRejectsSiblingPrefix(t *testing.T) {
	// "/var/wwwevil" shares a string prefix with "/var/www" but is a different
	// directory. A naive strings.HasPrefix check would accept it.
	base := t.TempDir()
	root := filepath.Join(base, "www")
	sibling := filepath.Join(base, "wwwevil")

	for _, dir := range []string{root, sibling} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	v := newValidator(t, root)
	if _, err := v.Resolve(filepath.Join(sibling, "payload")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("a sibling with a shared prefix must be rejected, got %v", err)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}

	base := t.TempDir()
	root := filepath.Join(base, "allowed")
	outside := filepath.Join(base, "secret")

	for _, dir := range []string{root, outside} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "key"), []byte("private"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	// A symlink inside the root pointing out of it: the lexical path looks
	// safe, which is exactly why resolution has to happen before the check.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	v := newValidator(t, root)
	if _, err := v.Resolve(filepath.Join(link, "key")); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("a symlink escaping the root must be rejected, got %v", err)
	}
}

func TestResolveFollowsSymlinksInsideRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}

	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.MkdirAll(target, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	link := filepath.Join(root, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	// A symlink that stays inside the root is legitimate and must resolve to
	// its target rather than being refused.
	resolved, err := newValidator(t, root).Resolve(filepath.Join(link, "file.txt"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != filepath.Join(target, "file.txt") {
		t.Fatalf("Resolve = %q, want %q", resolved, filepath.Join(target, "file.txt"))
	}
}

func TestResolveWorksForPathsNotYetCreated(t *testing.T) {
	root := t.TempDir()
	v := newValidator(t, root)

	// Validating a destination before creating it must be possible, or nothing
	// could ever be written safely.
	target := filepath.Join(root, "new", "deep", "file.txt")
	if _, err := v.Resolve(target); err != nil {
		t.Fatalf("a not-yet-created path must validate: %v", err)
	}
}

func TestResolveExistingRequiresThePathToExist(t *testing.T) {
	root := t.TempDir()
	v := newValidator(t, root)

	if _, err := v.ResolveExisting(filepath.Join(root, "missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolveDirAndFileCheckTheType(t *testing.T) {
	root := t.TempDir()
	v := newValidator(t, root)

	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	file := filepath.Join(root, "file.txt")
	if err := os.WriteFile(file, []byte("content"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := v.ResolveDir(dir); err != nil {
		t.Fatalf("ResolveDir on a directory: %v", err)
	}
	if _, err := v.ResolveDir(file); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("ResolveDir on a file: expected ErrNotDirectory, got %v", err)
	}

	if _, err := v.ResolveFile(file); err != nil {
		t.Fatalf("ResolveFile on a file: %v", err)
	}
	if _, err := v.ResolveFile(dir); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("ResolveFile on a directory: expected ErrNotRegular, got %v", err)
	}
}

func TestResolveFileRejectsDevices(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("device semantics differ on Windows")
	}

	// /dev/zero would return unbounded data and /dev/stdin can block forever,
	// so only regular files are acceptable targets.
	v := newValidator(t, "/dev")

	if _, err := v.ResolveFile("/dev/null"); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("a device must be refused, got %v", err)
	}
}

func TestNewValidatorRequiresAbsoluteRoots(t *testing.T) {
	if _, err := NewValidator("relative/root"); err == nil {
		t.Fatal("a relative root must be rejected")
	}
	if _, err := NewValidator(); err == nil {
		t.Fatal("a validator with no roots must be rejected")
	}
}

func TestZeroValidatorAllowsNothing(t *testing.T) {
	// A caller that forgets to configure roots must get refusals, not
	// unrestricted access.
	var v Validator

	if _, err := v.Resolve("/etc/passwd"); !errors.Is(err, ErrOutsideRoot) {
		t.Fatalf("the zero validator must allow nothing, got %v", err)
	}
}

func TestMultipleRoots(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	v := newValidator(t, first, second)

	for _, root := range []string{first, second} {
		if _, err := v.Resolve(filepath.Join(root, "file")); err != nil {
			t.Fatalf("path under %s must be allowed: %v", root, err)
		}
	}
	if _, err := v.Resolve("/etc/passwd"); err == nil {
		t.Fatal("a path outside every root must be rejected")
	}
}

func TestRootsAccessorCopies(t *testing.T) {
	root := t.TempDir()
	v := newValidator(t, root)

	roots := v.Roots()
	roots[0] = "/mutated"

	// Mutating the returned slice must not widen the validator.
	if v.Roots()[0] == "/mutated" {
		t.Fatal("Roots must return a copy")
	}
}
