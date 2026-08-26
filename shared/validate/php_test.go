package validate_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

func TestPHPVersionAcceptsMajorMinor(t *testing.T) {
	for _, version := range []string{"7.4", "8.0", "8.1", "8.2", "8.3", "8.4", "8.10"} {
		if err := validate.PHPVersion(version); err != nil {
			t.Fatalf("version %q must be accepted: %v", version, err)
		}
	}
}

// The value becomes a package name, a binary path, and a configuration path,
// so anything that is not a bare version is refused.
func TestPHPVersionRejectsAnythingElse(t *testing.T) {
	for _, version := range []string{
		"",
		"8",
		"8.3.19", // a patch level is not a choice a user makes
		"8.3; rm -rf /",
		"8.3\nuser=root",
		"../8.3",
		"8.3 ",
		"latest",
		"$(id)",
		"8.300",
	} {
		if err := validate.PHPVersion(version); err == nil {
			t.Fatalf("version %q must be rejected", version)
		}
	}
}

func TestNormalizePHPVersionAcceptsCommonSpellings(t *testing.T) {
	for input, want := range map[string]string{
		"8.3":     "8.3",
		" 8.3 ":   "8.3",
		"PHP 8.3": "8.3",
		"php8.3":  "8.3",
		"PHP8.3":  "8.3",
	} {
		if got := validate.NormalizePHPVersion(input); got != want {
			t.Fatalf("normalize(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPHPVersionCompactMatchesAlpineNaming(t *testing.T) {
	if got := validate.PHPVersionCompact("8.3"); got != "83" {
		t.Fatalf("compact = %q, want 83", got)
	}
}

func TestPHPMemoryLimitAcceptsSizes(t *testing.T) {
	for _, value := range []string{"-1", "128M", "512M", "1G", "268435456", "512K"} {
		if err := validate.PHPMemoryLimit(value); err != nil {
			t.Fatalf("memory limit %q must be accepted: %v", value, err)
		}
	}
}

// These strings are written verbatim into an FPM pool file, where a newline
// would let a caller append directives of their choosing.
func TestPHPMemoryLimitRejectsInjection(t *testing.T) {
	for _, value := range []string{
		"",
		"512M\nuser = root",
		"512M\r\n[evil]",
		"512M; rm -rf /",
		"512M unlimited",
		"unlimited",
		"512MB",
		"-2",
		"9999G", // past the ceiling a single site may claim
	} {
		if err := validate.PHPMemoryLimit(value); err == nil {
			t.Fatalf("memory limit %q must be rejected", value)
		}
	}
}

func TestPHPUploadSizeIsBounded(t *testing.T) {
	if err := validate.PHPUploadSize("64M"); err != nil {
		t.Fatalf("64M must be accepted: %v", err)
	}
	// -1 means "no limit" for memory_limit but is not valid for a size.
	if err := validate.PHPUploadSize("-1"); err == nil {
		t.Fatal("-1 is not a valid upload size")
	}
	if err := validate.PHPUploadSize("9999G"); err == nil {
		t.Fatal("an unbounded upload size must be rejected")
	}
}

// A site that can hold a worker for a day exhausts the pool for every other
// request to that site.
func TestPHPExecutionTimeIsBounded(t *testing.T) {
	if err := validate.PHPExecutionTime(30); err != nil {
		t.Fatalf("30 seconds must be accepted: %v", err)
	}
	if err := validate.PHPExecutionTime(0); err != nil {
		t.Fatalf("0 means no limit in PHP and must be accepted: %v", err)
	}
	if err := validate.PHPExecutionTime(-1); err == nil {
		t.Fatal("a negative execution time must be rejected")
	}
	if err := validate.PHPExecutionTime(validate.MaxExecutionTimeSeconds + 1); err == nil {
		t.Fatal("an execution time past the ceiling must be rejected")
	}
}

func TestPHPPoolNameRejectsPathAndSectionCharacters(t *testing.T) {
	if err := validate.PHPPoolName("web_example_a1b2"); err != nil {
		t.Fatalf("a plain pool name must be accepted: %v", err)
	}

	// The name becomes both a section header and part of a filename.
	for _, name := range []string{
		"",
		"../escape",
		"web/example",
		"web example",
		"web[evil]",
		"Web_Example", // uppercase would produce two names for one pool
		strings.Repeat("a", 65),
	} {
		if err := validate.PHPPoolName(name); err == nil {
			t.Fatalf("pool name %q must be rejected", name)
		}
	}
}

// GroupName and SystemUser answer different questions, and using the wrong one
// rejects exactly the value the caller needs.
//
// SystemUser asks "may a website own this account", where "nginx" must be
// refused. GroupName asks "may a website's socket be readable by this group",
// where "nginx" is the only correct answer on most hosts.
func TestGroupNameAcceptsTheWebServerGroup(t *testing.T) {
	for _, group := range []string{"nginx", "www-data", "http"} {
		if err := validate.GroupName(group); err != nil {
			t.Fatalf("group %q must be accepted: %v", group, err)
		}
		// The same name as a site's own account stays refused.
		if group == "nginx" || group == "www-data" {
			if err := validate.SystemUser(group); err == nil {
				t.Fatalf("a website must not be allowed to own %q", group)
			}
		}
	}
}

func TestGroupNameStillRejectsMalformedNames(t *testing.T) {
	for _, group := range []string{
		"",
		"has space",
		"../etc",
		"UPPER",
		"9leading",
	} {
		if err := validate.GroupName(group); err == nil {
			t.Fatalf("group %q must be rejected", group)
		}
	}
}
