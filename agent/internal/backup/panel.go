package backup

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jothost/panel/shared/seal"
	"github.com/jothost/panel/shared/validate"
)

// Backups of the panel's own database (docs/PANEL_BACKUP.md).
//
// The same archive as every other backup, with one database member, sealed
// before it leaves the host. What is dumped is this Agent's configuration and
// never the request: a panel request that names a site or a database is
// refused rather than honoured, so the type cannot be turned into a way to
// dump any database on the host.

// Errors specific to panel backups.
var (
	// ErrPanelUnavailable means this host does not know which database is the
	// panel's. It is the expected state of the development stack, where the
	// panel's PostgreSQL is a separate container the Agent cannot reach.
	ErrPanelUnavailable = errors.New(
		"this host does not know which database is the panel's (AGENT_PANEL_DATABASE is not set)")
	// ErrPanelRequest means a panel backup request named something to include.
	ErrPanelRequest = errors.New("a panel backup names no websites and no databases")
	// ErrSealingKey means a panel backup arrived without a usable key.
	ErrSealingKey = errors.New("a panel backup needs a 32-byte sealing key")
	// ErrPanelRestoreOnHost means a sealed archive was sent to be restored
	// through the panel.
	ErrPanelRestoreOnHost = errors.New(
		"a panel backup is restored from the host with install.sh restore-panel, not through the panel")
)

// panelRequest turns a panel backup request into the archive it describes,
// and returns the key to seal it with.
func (p *Provider) panelRequest(req Request) (Request, []byte, error) {
	if p.panelDatabase == "" {
		return Request{}, nil, ErrPanelUnavailable
	}
	if len(req.Sites) > 0 || len(req.Databases) > 0 {
		return Request{}, nil, ErrPanelRequest
	}
	key, err := sealingKey(req.SealingKey)
	if err != nil {
		return Request{}, nil, err
	}
	effective := req
	effective.Databases = []DatabaseSpec{{Engine: validate.EnginePostgres, Name: p.panelDatabase}}
	return effective, key, nil
}

func sealingKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrSealingKey
	}
	key, err := seal.ParseHexKey(encoded)
	if err != nil {
		return nil, ErrSealingKey
	}
	return key, nil
}

// checkDatabaseName refuses a database a backup may not include.
//
// The panel's own database is reserved, so that no website backup can include
// it (validate.DatabaseName). A panel backup is the one thing that must, and
// only that one database: the exemption applies to the panel type dumping
// exactly the configured name, and to nothing a request can choose.
func (p *Provider) checkDatabaseName(spec DatabaseSpec, backupType string) error {
	if backupType == validate.BackupPanel && p.panelDatabase != "" && spec.Name == p.panelDatabase {
		return validate.DatabaseIdentifier(spec.Name)
	}
	return validate.DatabaseName(spec.Name)
}

// sealFile writes plainPath, sealed, to a new file at sealedPath.
func sealFile(plainPath, sealedPath string, key []byte) error {
	in, err := os.Open(plainPath) //nolint:gosec // an agent-owned staging path
	if err != nil {
		return fmt.Errorf("open the archive to seal it: %w", err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.OpenFile(sealedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the sealed archive: %w", err)
	}
	sealer, err := seal.NewWriter(out, key)
	if err != nil {
		_ = out.Close()
		return fmt.Errorf("seal the archive: %w", err)
	}
	if _, err := io.Copy(sealer, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("seal the archive: %w", err)
	}
	if err := sealer.Close(); err != nil {
		_ = out.Close()
		return fmt.Errorf("seal the archive: %w", err)
	}
	// Synced before it is digested and sent: a sealed archive whose last chunk
	// was still in the page cache when the host lost power is a truncated one,
	// and the whole point of the final chunk is that truncation is caught.
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("sync the sealed archive: %w", err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close the sealed archive: %w", err)
	}
	return nil
}

// unsealFile opens sealedPath with key into a new file at plainPath.
//
// The output is only ever a staging copy. Nothing reads it unless this returns
// nil, because a chunk that fails to open can come after earlier chunks were
// already written out.
func unsealFile(sealedPath, plainPath string, key []byte) error {
	in, err := os.Open(sealedPath) //nolint:gosec // an agent-owned staging path
	if err != nil {
		return fmt.Errorf("open the sealed archive: %w", err)
	}
	defer func() { _ = in.Close() }()

	opener, err := seal.NewReader(in, key)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(plainPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the unsealed copy: %w", err)
	}
	if _, err := io.Copy(out, opener); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close the unsealed copy: %w", err)
	}
	return nil
}

// archiveIsSealed reports whether the file at path claims to be sealed.
func archiveIsSealed(path string) (bool, error) {
	handle, err := os.Open(path) //nolint:gosec // an agent-owned staging path
	if err != nil {
		return false, fmt.Errorf("read the archive: %w", err)
	}
	defer func() { _ = handle.Close() }()

	prefix := make([]byte, seal.MagicSize)
	n, err := io.ReadFull(handle, prefix)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read the archive: %w", err)
	}
	return seal.HasMagic(prefix[:n]), nil
}
