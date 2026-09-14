package backup

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/shared/seal"
	"github.com/jothost/panel/shared/validate"
)

// Opening a panel backup on the host (docs/PANEL_BACKUP.md, section 7).
//
// This is the Agent's half of `install.sh restore-panel`. It changes nothing on
// the host but the one file it writes: it proves the archive opens with the
// operator's key, that every member matches the manifest, and that the archive
// is a panel backup at all, and only then writes the SQL dump out for the
// installer to replay. Everything that replaces the running panel's database is
// the installer's, which is what runs with the panel stopped.

// ErrNotPanelBackup means an archive opened but is not a backup of the panel.
var ErrNotPanelBackup = errors.New("this archive is not a backup of the panel's database")

// OpenedPanelBackup describes the archive a dump was taken from, for the
// operator to confirm it is the one they meant.
type OpenedPanelBackup struct {
	Manifest Manifest
	Database string
	Bytes    int64
}

// ReadKeyFile reads the key written by `install.sh export-key`.
//
// The file is the 64 hexadecimal characters of ENCRYPTION_KEY, and may carry
// comment lines starting with "#" that say what it is. Anything else is
// refused, so a file that is not a key is reported as that rather than as an
// archive that "failed to open".
func ReadKeyFile(path string) ([]byte, error) {
	handle, err := os.Open(path) //nolint:gosec // a path the operator named on the host
	if err != nil {
		return nil, fmt.Errorf("read the key file: %w", err)
	}
	defer func() { _ = handle.Close() }()

	var found []string
	scanner := bufio.NewScanner(handle)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		found = append(found, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the key file: %w", err)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("the key file must hold exactly one key line, and %s holds %d", path, len(found))
	}
	key, err := seal.ParseHexKey(found[0])
	if err != nil {
		return nil, fmt.Errorf("the key file does not hold a panel key: %w", err)
	}
	return key, nil
}

// OpenPanelBackup unseals archivePath with the panel's ENCRYPTION_KEY, checks
// it, and writes its database dump to dumpTo, which must not exist.
//
// The unsealed archive is staged beside dumpTo and removed afterwards, so the
// directory the caller chooses decides where the plaintext ever sits; the
// installer gives it a 0700 directory of its own.
func OpenPanelBackup(archivePath string, encryptionKey []byte, dumpTo string) (OpenedPanelBackup, error) {
	if !filepath.IsAbs(dumpTo) {
		return OpenedPanelBackup{}, fmt.Errorf("the dump destination must be an absolute path")
	}

	sealed, err := archiveIsSealed(archivePath)
	if err != nil {
		return OpenedPanelBackup{}, err
	}
	if !sealed {
		return OpenedPanelBackup{}, fmt.Errorf("%w: it is not sealed", ErrNotPanelBackup)
	}

	masterKey, err := seal.PanelBackupKey(encryptionKey)
	if err != nil {
		return OpenedPanelBackup{}, err
	}

	staged, err := os.CreateTemp(filepath.Dir(dumpTo), ".panel-archive-*.tmp")
	if err != nil {
		return OpenedPanelBackup{}, fmt.Errorf("stage the unsealed archive: %w", err)
	}
	stagedPath := staged.Name()
	_ = staged.Close()
	// unsealFile creates with O_EXCL, so the name is reserved and then freed.
	if err := os.Remove(stagedPath); err != nil {
		return OpenedPanelBackup{}, fmt.Errorf("stage the unsealed archive: %w", err)
	}
	defer func() { _ = os.Remove(stagedPath) }()

	if err := unsealFile(archivePath, stagedPath, masterKey); err != nil {
		if errors.Is(err, seal.ErrOpen) || errors.Is(err, seal.ErrTruncated) ||
			errors.Is(err, seal.ErrTrailing) || errors.Is(err, seal.ErrFormat) {
			return OpenedPanelBackup{}, fmt.Errorf(
				"the archive could not be opened with this key: the key is not the one it was sealed with, or the archive is damaged (%w)", err)
		}
		return OpenedPanelBackup{}, err
	}

	manifest, _, detail, err := verifyMembers(stagedPath)
	if err != nil {
		return OpenedPanelBackup{}, err
	}
	if detail != "" {
		return OpenedPanelBackup{}, fmt.Errorf("%w: %s", ErrCorrupt, detail)
	}

	if manifest.Type != validate.BackupPanel || len(manifest.Sites) != 0 ||
		len(manifest.Databases) != 1 || manifest.Databases[0].Engine != validate.EnginePostgres {
		return OpenedPanelBackup{}, fmt.Errorf(
			"%w: it holds %d site(s) and %d database(s) of type %q, and a panel backup holds exactly one PostgreSQL database",
			ErrNotPanelBackup, len(manifest.Sites), len(manifest.Databases), manifest.Type)
	}
	member := manifest.Databases[0]

	if err := extractMember(stagedPath, member.Path, dumpTo); err != nil {
		return OpenedPanelBackup{}, err
	}
	return OpenedPanelBackup{Manifest: manifest, Database: member.Name, Bytes: member.Size}, nil
}
