package ssl_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/ssl"
)

func TestGenerateSelfSignedCoversEveryName(t *testing.T) {
	now := time.Now()

	certPEM, keyPEM, err := ssl.GenerateSelfSigned([]string{"example.test", "www.example.test"}, now)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(keyPEM) == 0 {
		t.Fatal("no private key was produced")
	}

	parsed, err := ssl.ParseCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// A visitor reaching www on a certificate naming only the apex gets a
	// browser warning indistinguishable from an attack.
	if len(parsed.Domains) != 2 {
		t.Fatalf("certificate covers %v, want both names", parsed.Domains)
	}
	if !parsed.SelfSigned {
		t.Fatal("a self-signed certificate must report itself as such")
	}
	if parsed.Fingerprint == "" {
		t.Fatal("no fingerprint was computed")
	}
}

// A clock difference of a few seconds between this host and a client must not
// make a certificate that was just issued "not yet valid".
func TestGenerateSelfSignedIsBackdated(t *testing.T) {
	now := time.Now()

	certPEM, _, err := ssl.GenerateSelfSigned([]string{"example.test"}, now)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	parsed, err := ssl.ParseCertificate(certPEM)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !parsed.IssuedAt.Before(now) {
		t.Fatalf("certificate starts at %s, which is not before %s", parsed.IssuedAt, now)
	}
	if parsed.ExpiresAt.Before(now.Add(300 * 24 * time.Hour)) {
		t.Fatalf("certificate expires at %s, sooner than expected", parsed.ExpiresAt)
	}
}

func TestGenerateSelfSignedRejectsBadNames(t *testing.T) {
	now := time.Now()

	for _, domains := range [][]string{
		{},
		{""},
		{"localhost"},
		{"example.test", "not a domain"},
		{"example.test\nDNS:evil.test"},
		{"../../etc/passwd"},
	} {
		if _, _, err := ssl.GenerateSelfSigned(domains, now); err == nil {
			t.Fatalf("domains %v must be rejected", domains)
		}
	}
}

// ------------------------------------------------------------------- store

func TestStoreWritesKeyUnreadableByAnyoneElse(t *testing.T) {
	root := t.TempDir()
	store := ssl.NewStore(root)

	certPEM, keyPEM, err := ssl.GenerateSelfSigned([]string{"example.test"}, time.Now())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	_, keyPath, err := store.Write("example.test", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	// The returned path is logical; the test root is where it actually is.
	info, err := os.Stat(filepath.Join(root, keyPath))
	if err != nil {
		t.Fatalf("stat key: %v", err)
	}

	// Anyone who can read this file can impersonate the site until the
	// certificate expires, and no later action can undo that.
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("private key is mode %04o, want 0600", mode)
	}

	dir, err := os.Stat(filepath.Dir(filepath.Join(root, keyPath)))
	if err != nil {
		t.Fatalf("stat directory: %v", err)
	}
	if mode := dir.Mode().Perm(); mode != 0o700 {
		t.Fatalf("certificate directory is mode %04o, want 0700", mode)
	}
}

// os.WriteFile applies its mode only when it creates the file, so replacing a
// key would otherwise silently keep whatever permissions it already had.
func TestStoreRewritesAKeyWithTightPermissions(t *testing.T) {
	root := t.TempDir()
	store := ssl.NewStore(root)

	certPEM, keyPEM, err := ssl.GenerateSelfSigned([]string{"example.test"}, time.Now())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, keyPath, err := store.Write("example.test", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}

	// Loosen it the way a restored backup or a careless operator might.
	full := filepath.Join(root, keyPath)
	if err := os.Chmod(full, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, _, err := store.Write("example.test", certPEM, keyPEM); err != nil {
		t.Fatalf("second write: %v", err)
	}

	info, err := os.Stat(full)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("rewritten key is mode %04o, want 0600", mode)
	}
}

func TestCheckKeyPermissionsRefusesAnExposedKey(t *testing.T) {
	root := t.TempDir()
	store := ssl.NewStore(root)

	certPEM, keyPEM, err := ssl.GenerateSelfSigned([]string{"example.test"}, time.Now())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	_, keyPath, err := store.Write("example.test", certPEM, keyPEM)
	if err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := store.CheckKeyPermissions(keyPath); err != nil {
		t.Fatalf("a freshly written key must pass: %v", err)
	}

	// A key from certbot, a restored backup, or an operator's own hand may be
	// wider than this package wrote it.
	if err := os.Chmod(filepath.Join(root, keyPath), 0o640); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := store.CheckKeyPermissions(keyPath); err == nil {
		t.Fatal("a group-readable private key must be refused")
	}
}

// The domain becomes a directory name, so a value that is not a hostname must
// never get that far.
func TestStoreRejectsPathTraversalInDomains(t *testing.T) {
	store := ssl.NewStore(t.TempDir())

	for _, domain := range []string{
		"",
		"../../etc",
		"example.test/../../etc",
		"localhost",
		"example.test\x00",
	} {
		if _, err := store.DirFor(domain); err == nil {
			t.Fatalf("domain %q must be rejected", domain)
		}
	}
}

func TestStoreRemoveDeletesEverything(t *testing.T) {
	root := t.TempDir()
	store := ssl.NewStore(root)

	certPEM, keyPEM, err := ssl.GenerateSelfSigned([]string{"example.test"}, time.Now())
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, _, err := store.Write("example.test", certPEM, keyPEM); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := store.Remove("example.test"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	dir, err := store.DirFor("example.test")
	if err != nil {
		t.Fatalf("dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, dir)); !os.IsNotExist(err) {
		t.Fatal("certificate material survived removal")
	}
}

func TestLoadReportsNotFoundForAMissingCertificate(t *testing.T) {
	store := ssl.NewStore(t.TempDir())

	if _, err := store.Load("example.test", "/nowhere/fullchain.pem", "", "selfsigned"); err == nil {
		t.Fatal("a missing certificate must be reported as not found")
	}
}

// ----------------------------------------------------------------- manager

func TestManagerIssuesAndReadsBackASelfSignedCertificate(t *testing.T) {
	root := t.TempDir()
	manager := ssl.NewManager(ssl.ManagerOptions{Store: ssl.NewStore(root)})

	certificate, err := manager.Issue(context.Background(), ssl.Request{
		Provider: ssl.ProviderSelfSigned,
		Domains:  []string{"example.test", "www.example.test"},
	}, nil)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	// The certificate is read back from disk rather than described from the
	// request: the expiry in particular is the certificate's own.
	if certificate.ExpiresAt.IsZero() {
		t.Fatal("issued certificate has no expiry")
	}
	if certificate.Provider != ssl.ProviderSelfSigned {
		t.Fatalf("provider = %q", certificate.Provider)
	}
	if len(certificate.Domains) != 2 {
		t.Fatalf("domains = %v", certificate.Domains)
	}
}

func TestManagerRejectsAnUnknownProvider(t *testing.T) {
	manager := ssl.NewManager(ssl.ManagerOptions{Store: ssl.NewStore(t.TempDir())})

	_, err := manager.Issue(context.Background(), ssl.Request{
		Provider: "acme-corp",
		Domains:  []string{"example.test"},
	}, nil)
	if err == nil {
		t.Fatal("an unknown provider must be rejected")
	}
}

// Let's Encrypt cannot run without certbot, and offering it anyway would
// produce a failure that looks like a bug in the panel.
func TestCapabilitiesReportSelfSignedWithoutCertbot(t *testing.T) {
	manager := ssl.NewManager(ssl.ManagerOptions{Store: ssl.NewStore(t.TempDir())})

	capabilities := manager.Capabilities()
	if !capabilities.SelfSigned {
		t.Fatal("self-signed needs nothing and must always be available")
	}
	if capabilities.LetsEncrypt {
		t.Fatal("Let's Encrypt must not be offered without certbot")
	}
}

func TestNeedsRenewalUsesTheRenewalWindow(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manager := ssl.NewManager(ssl.ManagerOptions{
		Store: ssl.NewStore(t.TempDir()),
		Now:   func() time.Time { return now },
	})

	fresh := ssl.Certificate{ExpiresAt: now.Add(60 * 24 * time.Hour)}
	if manager.NeedsRenewal(fresh) {
		t.Fatal("a certificate with 60 days left does not need renewal")
	}

	// Renewing at 30 days leaves two further chances before anything breaks.
	due := ssl.Certificate{ExpiresAt: now.Add(20 * 24 * time.Hour)}
	if !manager.NeedsRenewal(due) {
		t.Fatal("a certificate with 20 days left needs renewal")
	}

	expired := ssl.Certificate{ExpiresAt: now.Add(-time.Hour)}
	if !manager.NeedsRenewal(expired) {
		t.Fatal("an expired certificate needs renewal")
	}
}

func TestDaysRemainingCountsPastExpiry(t *testing.T) {
	now := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)

	// Negative rather than clamped: "expired 3 days ago" tells an operator how
	// long the site has been broken.
	expired := ssl.Certificate{ExpiresAt: now.Add(-3 * 24 * time.Hour)}
	if days := expired.DaysRemaining(now); days != -3 {
		t.Fatalf("days remaining = %d, want -3", days)
	}
}

func TestParseCertificateRejectsNonCertificates(t *testing.T) {
	for _, content := range []string{
		"",
		"not pem at all",
		"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n",
	} {
		if _, err := ssl.ParseCertificate([]byte(content)); err == nil {
			t.Fatalf("content %q must be rejected", strings.TrimSpace(content))
		}
	}
}
