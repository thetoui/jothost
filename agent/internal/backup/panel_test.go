package backup

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/database"
	"github.com/jothost/panel/shared/seal"
	"github.com/jothost/panel/shared/validate"
)

// Backups of the panel's own database, against a real archive, a real sealed
// file on disk and a stand-in PostgreSQL that records what it was asked to
// dump.

// The dump's content is recognisable, so the tests can prove it is nowhere in
// what leaves the host.
const panelDumpContent = "-- PostgreSQL dump\nINSERT INTO users VALUES ('admin', '$argon2id$v=19$secret-hash');\n"

type fakePostgres struct {
	database.Provider // unused methods panic, which is what a test wants
	dumped            []string
}

func (f *fakePostgres) Engine() string      { return validate.EnginePostgres }
func (f *fakePostgres) Available() bool     { return true }
func (f *fakePostgres) Unavailable() string { return "" }
func (f *fakePostgres) CanDump() bool       { return true }
func (f *fakePostgres) Reload(context.Context, string, string) error {
	return errors.New("a panel backup is never reloaded through the Agent's restore")
}

func (f *fakePostgres) Dump(_ context.Context, name, destination string) error {
	f.dumped = append(f.dumped, name)
	return os.WriteFile(destination, []byte(panelDumpContent), 0o600)
}

func panelProvider(t *testing.T, panelDatabase string) (*Provider, *fakePostgres, string) {
	t.Helper()

	base := t.TempDir()
	work := filepath.Join(base, "work")
	dest := filepath.Join(base, "backups")
	for _, dir := range []string{filepath.Join(base, "www"), work, dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("prepare %s: %v", dir, err)
		}
	}

	fake := &fakePostgres{}
	provider := NewProvider(Options{
		Databases:     database.NewManager(database.ManagerOptions{Providers: []database.Provider{fake}}),
		WorkDir:       work,
		SiteRoot:      filepath.Join(base, "www"),
		LocalRoots:    []string{dest},
		Hostname:      "test-host",
		PanelDatabase: panelDatabase,
		Now:           func() time.Time { return time.Date(2026, 9, 14, 3, 0, 0, 0, time.UTC) },
	})
	return provider, fake, dest
}

func hexKey(t *testing.T, fill byte) string {
	t.Helper()
	return strings.Repeat(string("0123456789abcdef"[fill%16]), 64)
}

const panelKey = "panel/panel/2026-09-14/030000.tar.gz"

func panelRequest(dest, key string) Request {
	return Request{
		Type:        validate.BackupPanel,
		Key:         panelKey,
		Subject:     "panel",
		Destination: localDestination(dest),
		SealingKey:  key,
	}
}

func TestAPanelBackupIsSealedAndDumpsOnlyTheConfiguredDatabase(t *testing.T) {
	provider, fake, dest := panelProvider(t, "jothost")
	key := hexKey(t, 7)

	result, err := provider.Create(context.Background(), panelRequest(dest, key), nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !result.Verified {
		t.Fatalf("the panel backup was not verified: %s", result.VerifyDetail)
	}
	if len(fake.dumped) != 1 || fake.dumped[0] != "jothost" {
		t.Fatalf("dumped %v, want exactly the configured panel database", fake.dumped)
	}
	if result.Manifest.Type != validate.BackupPanel || len(result.Manifest.Databases) != 1 {
		t.Fatalf("manifest: type %q with %d databases", result.Manifest.Type, len(result.Manifest.Databases))
	}

	stored, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(panelKey)))
	if err != nil {
		t.Fatalf("read what was stored: %v", err)
	}
	if !seal.HasMagic(stored) {
		t.Fatal("what reached the destination is not sealed")
	}
	// The whole point: nothing of the database is readable at the destination,
	// not the SQL and not the password hash inside it.
	for _, secret := range []string{"INSERT INTO users", "secret-hash", "argon2id"} {
		if bytes.Contains(stored, []byte(secret)) {
			t.Fatalf("%q is readable in the stored archive", secret)
		}
	}
	// And the plain staging copies are gone from the Agent's work directory.
	if entries, _ := os.ReadDir(provider.workDir); len(entries) != 0 {
		t.Fatalf("staging files were left behind: %d entries", len(entries))
	}
}

func TestAPanelBackupRefusesARequestThatNamesADatabase(t *testing.T) {
	// A panel request is not a way to dump any database under an
	// administrator's permission.
	provider, fake, dest := panelProvider(t, "jothost")
	req := panelRequest(dest, hexKey(t, 7))
	req.Databases = []DatabaseSpec{{Engine: validate.EnginePostgres, Name: "somebody_elses"}}

	if _, err := provider.Create(context.Background(), req, nil); !errors.Is(err, ErrPanelRequest) {
		t.Fatalf("a panel request naming a database gave %v, want ErrPanelRequest", err)
	}
	if len(fake.dumped) != 0 {
		t.Fatalf("something was dumped anyway: %v", fake.dumped)
	}
}

func TestAPanelBackupNeedsAUsableKey(t *testing.T) {
	provider, fake, dest := panelProvider(t, "jothost")
	for _, key := range []string{"", "not-hex", strings.Repeat("ab", 16)} {
		if _, err := provider.Create(context.Background(), panelRequest(dest, key), nil); !errors.Is(err, ErrSealingKey) {
			t.Fatalf("key %q gave %v, want ErrSealingKey", key, err)
		}
	}
	if len(fake.dumped) != 0 {
		t.Fatalf("the database was dumped without a key to seal it: %v", fake.dumped)
	}
}

func TestAPanelBackupIsUnavailableWithoutAConfiguredDatabase(t *testing.T) {
	provider, fake, dest := panelProvider(t, "")
	if _, err := provider.Create(context.Background(), panelRequest(dest, hexKey(t, 7)), nil); !errors.Is(err, ErrPanelUnavailable) {
		t.Fatalf("got %v, want ErrPanelUnavailable", err)
	}
	if len(fake.dumped) != 0 {
		t.Fatalf("dumped %v on a host that names no panel database", fake.dumped)
	}
	if caps := provider.Capabilities(); caps.Panel || caps.PanelReason == "" {
		t.Fatalf("capabilities: panel=%v reason=%q", caps.Panel, caps.PanelReason)
	}
}

func TestVerifyingASealedArchiveOpensIt(t *testing.T) {
	provider, _, dest := panelProvider(t, "jothost")
	key := hexKey(t, 7)
	result, err := provider.Create(context.Background(), panelRequest(dest, key), nil)
	if err != nil {
		t.Fatal(err)
	}

	verify := func(sealingKey string) VerifyResult {
		t.Helper()
		got, err := provider.Verify(context.Background(), VerifyRequest{
			Key: panelKey, Checksum: result.Checksum, Size: result.Size,
			Destination: localDestination(dest), SealingKey: sealingKey,
		})
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		return got
	}

	if got := verify(key); !got.OK || got.Members == 0 {
		t.Fatalf("with the right key: ok=%v members=%d detail=%q", got.OK, got.Members, got.Detail)
	}
	// Not errors: a verify's job is to record exactly these findings.
	if got := verify(""); got.OK || !strings.Contains(got.Detail, "no key") {
		t.Fatalf("with no key: ok=%v detail=%q", got.OK, got.Detail)
	}
	if got := verify(hexKey(t, 9)); got.OK || !strings.Contains(got.Detail, "could not be opened") {
		t.Fatalf("with the wrong key: ok=%v detail=%q", got.OK, got.Detail)
	}
}

func TestASealedArchiveIsNotRestoredThroughThePanel(t *testing.T) {
	provider, _, dest := panelProvider(t, "jothost")
	result, err := provider.Create(context.Background(), panelRequest(dest, hexKey(t, 7)), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Restore(context.Background(), RestoreRequest{
		Key: panelKey, Checksum: result.Checksum, Destination: localDestination(dest),
	}, nil)
	if !errors.Is(err, ErrPanelRestoreOnHost) {
		t.Fatalf("restoring a sealed archive gave %v, want ErrPanelRestoreOnHost", err)
	}
}

func TestTheReservationStillProtectsThePanelDatabaseFromOtherBackups(t *testing.T) {
	// The exemption is for the panel type dumping its configured database,
	// and nothing else. A database backup that names the panel's database -
	// which is what a customer's request would look like - is still refused.
	provider, fake, dest := panelProvider(t, "jothost")
	_, err := provider.Create(context.Background(), Request{
		Type:        validate.BackupDatabase,
		Key:         "database/jothost/2026-09-14/030000.tar.gz",
		Subject:     "jothost",
		Databases:   []DatabaseSpec{{Engine: validate.EnginePostgres, Name: "jothost"}},
		Destination: localDestination(dest),
	}, nil)
	if !errors.Is(err, ErrNothingToBackUp) || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("a database backup of the panel's database gave %v, want it refused as reserved", err)
	}
	if len(fake.dumped) != 0 {
		t.Fatalf("the panel's database was dumped by an ordinary backup: %v", fake.dumped)
	}
}

type unusablePostgres struct{ fakePostgres }

func (u *unusablePostgres) CanDump() bool { return false }

func TestAPanelBackupFailsRatherThanSealingNothing(t *testing.T) {
	// A dump that cannot be taken is skipped for a website backup, which keeps
	// its files. A panel backup is nothing but the dump: skipping it would
	// produce a sealed, verified archive with no database inside - something
	// that looks exactly like protection.
	base := t.TempDir()
	dest := filepath.Join(base, "backups")
	for _, dir := range []string{filepath.Join(base, "www"), filepath.Join(base, "work"), dest} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	provider := NewProvider(Options{
		Databases: database.NewManager(database.ManagerOptions{
			Providers: []database.Provider{&unusablePostgres{}},
		}),
		WorkDir:       filepath.Join(base, "work"),
		SiteRoot:      filepath.Join(base, "www"),
		LocalRoots:    []string{dest},
		PanelDatabase: "jothost",
	})

	_, err := provider.Create(context.Background(), panelRequest(dest, hexKey(t, 7)), nil)
	if !errors.Is(err, ErrNothingToBackUp) {
		t.Fatalf("a panel backup whose dump could not be taken gave %v, want ErrNothingToBackUp", err)
	}
	if entries, _ := os.ReadDir(dest); len(entries) != 0 {
		t.Fatalf("something was stored anyway: %d entries at the destination", len(entries))
	}
}
