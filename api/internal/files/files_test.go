package files_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/files"
)

// The API's check is the cheap one; the Agent re-validates through pathsec,
// which is the only side that can resolve a symlink. What matters here is that
// the cheap check never accepts something the expensive one would have to
// catch.
func TestValidatePathRefusesHostileShapes(t *testing.T) {
	hostile := []string{
		"",
		"   ",
		"relative/path",
		"../etc/passwd",
		"/var/www/../../etc/passwd",
		"/var/www/..",
		"/var/www/a/../../b",
		"/var/www\x00/etc",
		"/" + strings.Repeat("a", 5000),
	}

	for _, path := range hostile {
		if _, err := files.ValidatePath(path); err == nil {
			t.Fatalf("ValidatePath(%q) was accepted", truncate(path))
		}
	}
}

func TestValidatePathAcceptsAndNormalises(t *testing.T) {
	cases := map[string]string{
		"/var/www":                "/var/www",
		"/var/www/":               "/var/www",
		"/var/www//site":          "/var/www/site",
		"/var/www/./site":         "/var/www/site",
		"/var/www/site/index.php": "/var/www/site/index.php",
		"/":                       "/",
	}

	for input, want := range cases {
		got, err := files.ValidatePath(input)
		if err != nil {
			t.Fatalf("ValidatePath(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ValidatePath(%q) = %q, want %q", input, got, want)
		}
	}
}

// A browser controls an uploaded filename completely.
func TestSafeNameRefusesAnythingButASingleSegment(t *testing.T) {
	hostile := []string{
		"",
		"   ",
		".",
		"..",
		"../escape.txt",
		"nested/child.txt",
		"/absolute.txt",
		"back\\slash.txt",
		"nul\x00.txt",
		strings.Repeat("a", 300),
	}

	for _, name := range hostile {
		if _, err := files.SafeName(name); err == nil {
			t.Fatalf("SafeName(%q) was accepted", truncate(name))
		}
	}
}

func TestSafeNameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{
		"index.php",
		"My Document.txt",
		".htaccess",
		"archive.tar.gz",
		"ünïcode.txt",
	} {
		got, err := files.SafeName(name)
		if err != nil {
			t.Fatalf("SafeName(%q): %v", name, err)
		}
		if got != name {
			t.Fatalf("SafeName(%q) = %q, want it unchanged", name, got)
		}
	}
}

// A name is joined to a directory, so the join must not be able to escape it
// even though SafeName already refused the shapes that would.
func TestJoinPathStaysUnderTheDirectory(t *testing.T) {
	got := files.JoinPath("/var/www/site", "index.php")
	if got != "/var/www/site/index.php" {
		t.Fatalf("JoinPath = %q", got)
	}
}

func truncate(value string) string {
	if len(value) > 40 {
		return value[:40] + "..."
	}
	return value
}
