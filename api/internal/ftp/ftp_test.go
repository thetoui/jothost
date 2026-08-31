package ftp

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The panel's own rules about FTP accounts, tested without a database or a
// host: what a home directory resolves to, and what the generated passwords are
// made of.

func TestAbsoluteHomeResolvesAgainstTheWebsitesRoot(t *testing.T) {
	root := "/var/www/example.com/public"

	if got := AbsoluteHome(root, ""); got != root {
		t.Errorf("an empty subpath is the website's own directory, got %q", got)
	}
	if got := AbsoluteHome(root, "uploads"); got != root+"/uploads" {
		t.Errorf("wrong home: %q", got)
	}
	// A trailing slash on the root must not double up.
	if got := AbsoluteHome(root+"/", "uploads"); got != root+"/uploads" {
		t.Errorf("wrong home for a root with a trailing slash: %q", got)
	}
}

// The subpath is validated before it ever reaches AbsoluteHome, and this is the
// boundary that keeps an account inside its website.
func TestFTPHomeRefusesAPathThatLeavesTheWebsite(t *testing.T) {
	for _, subpath := range []string{
		"..",
		"../etc",
		"uploads/../../etc",
		"/etc", // absolute: refused, not silently trimmed
		"/var/www/other",
	} {
		if _, err := validate.FTPHome(subpath); err == nil {
			t.Errorf("%q should not be an acceptable FTP home", subpath)
		}
	}
}

func TestFTPHomeAcceptsAFolderInsideTheWebsite(t *testing.T) {
	for _, subpath := range []string{"", "uploads", "media/images", "uploads/"} {
		if _, err := validate.FTPHome(subpath); err != nil {
			t.Errorf("%q should be an acceptable FTP home: %v", subpath, err)
		}
	}
}

// The panel shows this password once, on a screen, to be typed into an FTP
// client. Characters a person reads as another are a support ticket.
func TestGeneratedPasswordsAreUnambiguousAndLongEnough(t *testing.T) {
	const ambiguous = "O0lI1"

	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		password, err := generatePassword()
		if err != nil {
			t.Fatalf("generatePassword: %v", err)
		}
		if len(password) != generatedPasswordLength {
			t.Fatalf("wrong length: %d", len(password))
		}
		if strings.ContainsAny(password, ambiguous) {
			t.Fatalf("a generated password contains a character read as another: %q", password)
		}
		// It also has to satisfy the rule the panel enforces on passwords an
		// operator supplies — a generated one the API would then refuse would
		// be an account that cannot be created.
		if err := validate.FTPPassword(password); err != nil {
			t.Fatalf("a generated password would be refused: %v", err)
		}
		if seen[password] {
			t.Fatalf("generatePassword repeated itself: %q", password)
		}
		seen[password] = true
	}
}

func TestDefaultSettingsCarryAUsablePassiveRange(t *testing.T) {
	settings := DefaultSettings("server-1")

	// There is no workable "no range": without one every passive transfer on a
	// firewalled host hangs, so the default has to be a valid one.
	if err := validate.PassivePortRange(settings.PassiveFrom, settings.PassiveTo); err != nil {
		t.Fatalf("the default passive range is not usable: %v", err)
	}
}
