package validate_test

import (
	"errors"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// A document root is relative to the site's own directory, which is what makes
// it safe: an operator is not naming a directory, only a subpath of the one
// that is already theirs. These are mostly about what that refuses anyway,
// because "it cannot escape by construction" is a claim worth testing.

func TestADocumentRootCannotLeaveTheSite(t *testing.T) {
	for _, attempt := range []string{
		"..",
		"../",
		"../../etc",
		"public/../../..",
		"public/../../other-site/public",
		"./..",
		"a/./b",
		"/etc/passwd",
		"/",
		"/public",
		`public\..\..\windows`,
		"public\x00/etc",
	} {
		if _, err := validate.DocumentRoot(attempt); err == nil {
			t.Fatalf("%q was accepted", attempt)
		}
	}
}

func TestTheOrdinaryCasesAreAccepted(t *testing.T) {
	for input, want := range map[string]string{
		"public":              "public",
		"public/dist":         "public/dist",
		"public/dist/browser": "public/dist/browser",
		"public/":             "public",
		"  public  ":          "public",
		"web-root":            "web-root",
		"web_root.2":          "web_root.2",
		// The site's own directory, for a site whose index sits at the top.
		"": "",
	} {
		got, err := validate.DocumentRoot(input)
		if err != nil {
			t.Fatalf("%q was refused: %v", input, err)
		}
		if got != want {
			t.Fatalf("%q became %q, want %q", input, got, want)
		}
	}
}

func TestTheLogDirectoryIsRefused(t *testing.T) {
	// Serving it would publish every request anybody has ever made to the
	// site, query strings included.
	if _, err := validate.DocumentRoot("logs"); !errors.Is(err, validate.ErrDocumentRootReserved) {
		t.Fatalf("logs was not refused as reserved: %v", err)
	}
	if _, err := validate.DocumentRoot("logs/archive"); !errors.Is(err, validate.ErrDocumentRootReserved) {
		t.Fatalf("logs/archive was not refused as reserved: %v", err)
	}
	// A directory that merely starts with the same letters is somebody's own.
	if _, err := validate.DocumentRoot("logsomething"); err != nil {
		t.Fatalf("logsomething was refused: %v", err)
	}
}

func TestNamesThatWouldTravelBadly(t *testing.T) {
	for _, attempt := range []string{
		"pub lic",       // a space
		`pub"lic`,       // a quote, in a generated configuration file
		"pub;lic",       // a separator, wherever this ends up
		"pub$lic",       // an expansion, wherever this ends up
		"-rf",           // an option, wherever this ends up
		"public/-rf",    //
		"public\n/etc",  // a newline in a configuration file
		"a/b/c/d/e/f/g", // deeper than anything real
	} {
		if _, err := validate.DocumentRoot(attempt); err == nil {
			t.Fatalf("%q was accepted", attempt)
		}
	}
}

func TestALongNameIsRefused(t *testing.T) {
	long := ""
	for i := 0; i < 70; i++ {
		long += "a"
	}
	if _, err := validate.DocumentRoot(long); err == nil {
		t.Fatal("a 70-character directory name was accepted")
	}
}

func TestThePathIsComposedOnce(t *testing.T) {
	// The panel and the Agent must agree about where "public/dist" is. They
	// agree by calling the same function.
	if got := validate.DocumentRootPath("/var/www/example.com", "public/dist"); got != "/var/www/example.com/public/dist" {
		t.Fatalf("got %q", got)
	}
	// An empty root is the site's own directory.
	if got := validate.DocumentRootPath("/var/www/example.com", ""); got != "/var/www/example.com" {
		t.Fatalf("got %q", got)
	}
}
