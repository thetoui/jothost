package mail

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stubServices records what it was asked to do and answers a fixed state.
type stubServices struct {
	restarted []string
	running   bool
	failWith  error
}

func (s *stubServices) Restart(_ context.Context, name string) error {
	s.restarted = append(s.restarted, name)
	return s.failWith
}
func (s *stubServices) Reload(context.Context, string) error { return nil }
func (s *stubServices) Running(context.Context, string) bool { return s.running }

// withSignatureDir points the signature lookup at a temporary directory for
// the length of one test.
func withSignatureDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	original := signatureDirs
	signatureDirs = []string{dir}
	t.Cleanup(func() { signatureDirs = original })
	return dir
}

// TestHasSignaturesIsWhatDecidesWhetherTheScannerCanRun.
//
// clamd refuses to start with no database, so this is the difference between
// a scanner and a package. The panel installed the package and reported
// success, which is why it needed to become a question the code asks.
func TestHasSignaturesIsWhatDecidesWhetherTheScannerCanRun(t *testing.T) {
	dir := withSignatureDir(t)

	if HasSignatures() {
		t.Fatal("an empty directory must not count as a virus database")
	}

	if err := os.WriteFile(filepath.Join(dir, "daily.cld"), []byte("signatures"), 0o644); err != nil {
		t.Fatalf("write signature file: %v", err)
	}
	if !HasSignatures() {
		t.Fatal("a database that is present was not found")
	}
}

// TestAnEmptySignatureFileIsNotADatabase.
//
// freshclam creates the file before it has finished filling it. An interrupted
// download leaves something that exists, is useless, and would otherwise make
// the panel skip the fetch and start a scanner with nothing in it.
func TestAnEmptySignatureFileIsNotADatabase(t *testing.T) {
	dir := withSignatureDir(t)

	if err := os.WriteFile(filepath.Join(dir, "main.cvd"), nil, 0o644); err != nil {
		t.Fatalf("write empty file: %v", err)
	}
	if HasSignatures() {
		t.Fatal("a zero-length signature file was treated as a database")
	}
}

// TestPrepareRefusesWithNoWayToFetchSignatures.
//
// The honest answer on a host with no freshclam. Reporting success here would
// leave the panel claiming mail was scanned by a daemon that cannot start.
func TestPrepareRefusesWithNoWayToFetchSignatures(t *testing.T) {
	withSignatureDir(t)

	services := &stubServices{running: true}
	provider := NewProvider(Options{Services: services})

	prep := provider.PrepareAntivirus(context.Background(), nil)

	if prep.Started {
		t.Fatal("claimed to start a scanner with no virus database")
	}
	if prep.SignaturesPresent {
		t.Fatal("claimed signatures are present when the directory is empty")
	}
	if prep.Detail == "" {
		t.Fatal("a preparation that did not finish must say why")
	}
	if len(services.restarted) != 0 {
		t.Fatalf("started the scanner anyway: %v", services.restarted)
	}
}

// TestPrepareStartsTheScannerWhenSignaturesAreAlreadyThere.
//
// The download is hundreds of megabytes. A host that already has a database
// should not be made to wait for one it does not need.
func TestPrepareStartsTheScannerWhenSignaturesAreAlreadyThere(t *testing.T) {
	dir := withSignatureDir(t)
	if err := os.WriteFile(filepath.Join(dir, "daily.cvd"), []byte("db"), 0o644); err != nil {
		t.Fatalf("write signature file: %v", err)
	}

	services := &stubServices{running: true}
	provider := NewProvider(Options{Services: services})

	prep := provider.PrepareAntivirus(context.Background(), nil)

	if prep.SignaturesFetched {
		t.Error("downloaded signatures that were already present")
	}
	if !prep.SignaturesPresent {
		t.Error("did not see the database that is there")
	}
	if !prep.Started {
		t.Errorf("did not start the scanner: %s", prep.Detail)
	}
	if len(services.restarted) != 1 || services.restarted[0] != DaemonClamAV {
		t.Errorf("restarted %v, want just %s", services.restarted, DaemonClamAV)
	}
}

// TestPrepareReportsAScannerThatWillNotStay.
//
// Restart succeeding and the daemon not running is the case worth catching:
// clamd exits after start for its own reasons, and "started" is not "running".
func TestPrepareReportsAScannerThatWillNotStay(t *testing.T) {
	dir := withSignatureDir(t)
	if err := os.WriteFile(filepath.Join(dir, "daily.cvd"), []byte("db"), 0o644); err != nil {
		t.Fatalf("write signature file: %v", err)
	}

	provider := NewProvider(Options{Services: &stubServices{running: false}})

	prep := provider.PrepareAntivirus(context.Background(), nil)

	if prep.Started {
		t.Fatal("reported a scanner as started when it is not running")
	}
	if prep.Detail == "" {
		t.Fatal("a scanner that would not stay up must say so")
	}
}

// TestPrepareReportsAHostThatCannotStartAnything.
func TestPrepareReportsAHostThatCannotStartAnything(t *testing.T) {
	dir := withSignatureDir(t)
	if err := os.WriteFile(filepath.Join(dir, "daily.cvd"), []byte("db"), 0o644); err != nil {
		t.Fatalf("write signature file: %v", err)
	}

	// No services at all, which is a host with no init system.
	provider := NewProvider(Options{})

	prep := provider.PrepareAntivirus(context.Background(), nil)

	if prep.Started {
		t.Fatal("claimed to start a daemon with nothing to start it")
	}
	if prep.Detail == "" {
		t.Fatal("must say why the scanner was not started")
	}
}
