package backup

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/seal"
	"github.com/jothost/panel/shared/validate"
)

// Opening a panel backup on the host, the way install.sh restore-panel does:
// with the ENCRYPTION_KEY the operator exported, not the derived key the API
// hands the Agent. That difference is what these tests pin - a restore that
// only worked with the derived key would work in tests and on no real host.

// panelEncryptionKey is a stand-in ENCRYPTION_KEY and the sealing key the API
// would derive from it.
func panelEncryptionKey(t *testing.T) ([]byte, string) {
	t.Helper()
	encryptionKey := []byte(strings.Repeat("k", seal.KeySize))
	derived, err := seal.PanelBackupKey(encryptionKey)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	return encryptionKey, hex.EncodeToString(derived)
}

func takePanelBackup(t *testing.T) (string, []byte) {
	t.Helper()
	provider, _, dest := panelProvider(t, "jothost")
	encryptionKey, sealingKey := panelEncryptionKey(t)
	if _, err := provider.Create(context.Background(), panelRequest(dest, sealingKey), nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return filepath.Join(dest, filepath.FromSlash(panelKey)), encryptionKey
}

func writeKeyFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "panel.key")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	return path
}

func TestAPanelBackupOpensWithTheExportedKey(t *testing.T) {
	archive, encryptionKey := takePanelBackup(t)
	keyFile := writeKeyFile(t, "# JotHost panel key\n\n"+hex.EncodeToString(encryptionKey)+"\n")

	key, err := ReadKeyFile(keyFile)
	if err != nil {
		t.Fatalf("ReadKeyFile: %v", err)
	}
	dumpDir := t.TempDir()
	dumpTo := filepath.Join(dumpDir, "panel.sql")
	opened, err := OpenPanelBackup(archive, key, dumpTo)
	if err != nil {
		t.Fatalf("OpenPanelBackup: %v", err)
	}

	dump, err := os.ReadFile(dumpTo)
	if err != nil {
		t.Fatalf("read the dump: %v", err)
	}
	if string(dump) != panelDumpContent {
		t.Fatalf("the dump is %q, want what was backed up", dump)
	}
	if opened.Database != "jothost" || opened.Manifest.Hostname != "test-host" {
		t.Fatalf("opened %+v", opened)
	}
	info, err := os.Stat(dumpTo)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the dump holds every password hash and is mode %v (%v), want 0600", info.Mode().Perm(), err)
	}
	// Only the dump is left: the unsealed archive staged beside it is gone.
	if entries, _ := os.ReadDir(dumpDir); len(entries) != 1 {
		t.Fatalf("%d entries beside the dump, want only the dump", len(entries))
	}
}

func TestAPanelBackupDoesNotOpenWithAnotherKey(t *testing.T) {
	archive, _ := takePanelBackup(t)
	wrong := []byte(strings.Repeat("w", seal.KeySize))
	dumpDir := t.TempDir()
	dumpTo := filepath.Join(dumpDir, "panel.sql")

	_, err := OpenPanelBackup(archive, wrong, dumpTo)
	if err == nil || !strings.Contains(err.Error(), "could not be opened with this key") {
		t.Fatalf("the wrong key gave %v, want it to say the key does not open it", err)
	}
	if entries, _ := os.ReadDir(dumpDir); len(entries) != 0 {
		t.Fatalf("a failed open left %d files behind", len(entries))
	}
}

// The derived key the Agent seals with is not what an operator holds, and must
// not be accepted in its place: that would make the Agent's key enough to
// read a panel backup.
func TestAPanelBackupDoesNotOpenWithTheAgentsDerivedKey(t *testing.T) {
	archive, _ := takePanelBackup(t)
	_, sealingKey := panelEncryptionKey(t)
	derived, _ := hex.DecodeString(sealingKey)

	_, err := OpenPanelBackup(archive, derived, filepath.Join(t.TempDir(), "panel.sql"))
	if err == nil {
		t.Fatal("the Agent's derived key opened the backup; only ENCRYPTION_KEY should")
	}
}

func TestOpeningRefusesAnArchiveThatIsNotAPanelBackup(t *testing.T) {
	// A database backup of a customer's database, sealed with the right key:
	// it opens, and is still not something to replace the panel's database with.
	provider, _, dest := panelProvider(t, "jothost")
	req := Request{
		Type: validate.BackupDatabase, Key: "database/shop/2026-09-14/030000.tar.gz", Subject: "shop",
		Databases:   []DatabaseSpec{{Engine: validate.EnginePostgres, Name: "shop"}},
		Destination: localDestination(dest),
	}
	if _, err := provider.Create(context.Background(), req, nil); err != nil {
		t.Fatalf("Create: %v", err)
	}
	encryptionKey, sealingKey := panelEncryptionKey(t)
	derived, _ := hex.DecodeString(sealingKey)

	plain := filepath.Join(dest, "database", "shop", "2026-09-14", "030000.tar.gz")
	sealed := plain + ".sealed"
	if err := sealFile(plain, sealed, derived); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := OpenPanelBackup(sealed, encryptionKey, filepath.Join(t.TempDir(), "d.sql")); !errors.Is(err, ErrNotPanelBackup) {
		t.Fatalf("a sealed database backup gave %v, want ErrNotPanelBackup", err)
	}
	if _, err := OpenPanelBackup(plain, encryptionKey, filepath.Join(t.TempDir(), "d.sql")); !errors.Is(err, ErrNotPanelBackup) {
		t.Fatalf("an unsealed archive gave %v, want ErrNotPanelBackup", err)
	}
}

func TestOpeningNeverOverwritesTheDumpDestination(t *testing.T) {
	archive, encryptionKey := takePanelBackup(t)
	dumpTo := filepath.Join(t.TempDir(), "panel.sql")
	if err := os.WriteFile(dumpTo, []byte("already here"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPanelBackup(archive, encryptionKey, dumpTo); err == nil {
		t.Fatal("an existing file at the dump destination was accepted")
	}
	if content, _ := os.ReadFile(dumpTo); string(content) != "already here" {
		t.Fatalf("the existing file was overwritten: %q", content)
	}
}

func TestAKeyFileHoldsExactlyOneKey(t *testing.T) {
	good := strings.Repeat("ab", seal.KeySize)
	for name, content := range map[string]string{
		"empty":         "# only a comment\n",
		"two keys":      good + "\n" + good + "\n",
		"not hex":       strings.Repeat("zz", seal.KeySize) + "\n",
		"too short":     "abcd\n",
		"a dotenv line": "ENCRYPTION_KEY=" + good + "\n",
	} {
		if _, err := ReadKeyFile(writeKeyFile(t, content)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := ReadKeyFile(writeKeyFile(t, "  "+good+"  \r\n")); err != nil {
		t.Errorf("a key with surrounding whitespace was refused: %v", err)
	}
}
