package sites

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where a site's logs go once an operator can choose the document root.
//
// LayoutFor infers the site root from the document root's parent, which is
// right only while the document root is exactly one level down. Once
// "public/dist" is a thing somebody can set, that inference puts the log
// directory inside the served one — and a site's own access log becomes a file
// anybody can fetch by guessing its name. LayoutIn is the answer, and these are
// what say so.

func provisionerIn(t *testing.T) (*Provisioner, string) {
	t.Helper()
	root := t.TempDir()
	provisioner, err := NewProvisioner(root, "")
	if err != nil {
		t.Fatalf("NewProvisioner: %v", err)
	}
	return provisioner, root
}

func TestANestedDocumentRootDoesNotMoveTheLogs(t *testing.T) {
	provisioner, root := provisionerIn(t)

	site := filepath.Join(root, "example.com")
	content := filepath.Join(site, "public", "dist")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	layout, err := provisioner.LayoutIn(site, content)
	if err != nil {
		t.Fatalf("LayoutIn: %v", err)
	}

	if layout.Content != content {
		t.Fatalf("content = %q, want %q", layout.Content, content)
	}
	if layout.Root != site {
		t.Fatalf("root = %q, want %q", layout.Root, site)
	}

	// The whole point. Logs beside the site, never underneath what is served.
	if strings.HasPrefix(layout.Logs, content+string(filepath.Separator)) || layout.Logs == content {
		t.Fatalf("the log directory %q is inside the served directory %q", layout.Logs, content)
	}
	if layout.Logs != filepath.Join(site, LogsDir) {
		t.Fatalf("logs = %q, want %q", layout.Logs, filepath.Join(site, LogsDir))
	}
}

func TestTheOldInferenceWouldHavePublishedTheLogs(t *testing.T) {
	// Not a test of desired behaviour: a record of why LayoutIn exists. If
	// LayoutFor ever stops doing this, the reason for the other function has
	// gone and somebody should notice.
	provisioner, root := provisionerIn(t)

	site := filepath.Join(root, "example.com")
	content := filepath.Join(site, "public", "dist")
	if err := os.MkdirAll(content, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	inferred, err := provisioner.LayoutFor(content)
	if err != nil {
		t.Fatalf("LayoutFor: %v", err)
	}
	if inferred.Logs != filepath.Join(site, "public", LogsDir) {
		t.Fatalf("LayoutFor now puts logs at %q; LayoutIn may no longer be needed",
			inferred.Logs)
	}
}

func TestADocumentRootOutsideTheSiteIsRefused(t *testing.T) {
	provisioner, root := provisionerIn(t)

	site := filepath.Join(root, "example.com")
	other := filepath.Join(root, "other.example")
	if err := os.MkdirAll(filepath.Join(site, "public"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(other, "public"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	// Another site's directory is inside the panel's root, so the root check
	// alone would let it through. It is the site check that refuses it.
	if _, err := provisioner.LayoutIn(site, filepath.Join(other, "public")); err == nil {
		t.Fatal("a document root in another site was accepted")
	}
}

func TestASymlinkCannotCarryTheDocumentRootOut(t *testing.T) {
	provisioner, root := provisionerIn(t)

	site := filepath.Join(root, "example.com")
	if err := os.MkdirAll(site, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	outside := t.TempDir()

	link := filepath.Join(site, "public")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks are not available here: %v", err)
	}

	// The check is on the resolved path. A link that appears to be inside the
	// site but lands elsewhere is what this is for.
	if _, err := provisioner.LayoutIn(site, link); err == nil {
		t.Fatal("a symlinked document root pointing outside the site was accepted")
	}
}

func TestTheSiteDirIsTheOneConvention(t *testing.T) {
	provisioner, root := provisionerIn(t)

	if got := provisioner.SiteDir("example.com"); got != filepath.Join(root, "example.com") {
		t.Fatalf("SiteDir = %q", got)
	}
}
