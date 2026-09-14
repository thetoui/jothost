package agentclient

import (
	"context"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// The backup operations.
//
// These are the only calls in this client that carry a secret — an S3 secret
// key, or the private key for an SFTP destination. They travel over the Agent's
// Unix socket, which is mode 0660 and group-owned, and they are never logged at
// either end. The panel decrypts them for the length of one call and holds them
// nowhere else.
//
// Creating and restoring are asynchronous, because both move an unbounded
// amount of data: a backup of a busy host takes as long as it takes, and a
// request that waited for it would time out long before the work did.

// BackupDestination is where an archive is written.
type BackupDestination struct {
	Kind string `json:"kind"`

	Directory string `json:"directory,omitempty"`

	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	PathStyle bool   `json:"path_style,omitempty"`
	// AllowInsecure permits a plain-http endpoint that is not on this machine.
	AllowInsecure bool `json:"allow_insecure,omitempty"`

	Host       string `json:"host,omitempty"`
	Port       int    `json:"port,omitempty"`
	User       string `json:"user,omitempty"`
	Path       string `json:"path,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	HostKey    string `json:"host_key,omitempty"`
}

// BackupSite names one website in a backup or a restore.
type BackupSite struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user,omitempty"`
}

// BackupDatabase names one database in a backup or a restore.
type BackupDatabase struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

// BackupCapabilities is what the host can actually do.
type BackupCapabilities struct {
	Available    bool     `json:"available"`
	Reason       string   `json:"reason,omitempty"`
	Local        bool     `json:"local"`
	S3           bool     `json:"s3"`
	SFTP         bool     `json:"sftp"`
	MySQLDump    bool     `json:"mysql_dump"`
	PostgresDump bool     `json:"postgres_dump"`
	Engines      []string `json:"engines"`
	WorkDir      string   `json:"work_dir,omitempty"`
	// Panel says whether this host can back up the panel's own database: the
	// Agent knows which database that is and can dump it. PanelReason says why
	// not, so the page can explain rather than hide the option.
	Panel       bool   `json:"panel"`
	PanelReason string `json:"panel_reason,omitempty"`
}

// BackupMember is one file inside an archive.
type BackupMember struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum,omitempty"`
	Mode     uint32 `json:"mode"`
	Link     string `json:"link,omitempty"`
}

// BackupDatabaseMember is one dump inside an archive.
type BackupDatabaseMember struct {
	Engine   string `json:"engine"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

// BackupSiteEntry describes one website inside an archive.
type BackupSiteEntry struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user,omitempty"`
	Prefix       string `json:"prefix"`
	FileCount    int    `json:"file_count"`
	Bytes        int64  `json:"bytes"`
}

// BackupManifest describes an archive.
//
// Files is deliberately not carried into the panel's own record: an archive of
// a WordPress site has forty thousand of them, and storing that per backup
// would make the backups table larger than the thing it describes. The panel
// keeps the counts and the summaries; the per-file digests stay in the archive,
// where a verify reads them.
type BackupManifest struct {
	Version      int                    `json:"version"`
	Type         string                 `json:"type"`
	Subject      string                 `json:"subject"`
	CreatedAt    time.Time              `json:"created_at"`
	Hostname     string                 `json:"hostname"`
	AgentVersion string                 `json:"agent_version,omitempty"`
	Sites        []BackupSiteEntry      `json:"sites"`
	Databases    []BackupDatabaseMember `json:"databases"`
	Files        []BackupMember         `json:"files"`
	FileCount    int                    `json:"file_count"`
	FileBytes    int64                  `json:"file_bytes"`
	Skipped      []string               `json:"skipped"`
}

// BackupResult reports a completed backup.
type BackupResult struct {
	Key          string         `json:"key"`
	Size         int64          `json:"size"`
	Checksum     string         `json:"checksum"`
	Verified     bool           `json:"verified"`
	VerifyDetail string         `json:"verify_detail,omitempty"`
	Manifest     BackupManifest `json:"manifest"`
	DurationMS   int64          `json:"duration_ms"`
}

// BackupVerifyResult reports on reading an archive back.
type BackupVerifyResult struct {
	Key      string         `json:"key"`
	OK       bool           `json:"ok"`
	Size     int64          `json:"size"`
	Bytes    int64          `json:"bytes_read"`
	Checksum string         `json:"checksum"`
	Expected string         `json:"expected,omitempty"`
	Detail   string         `json:"detail,omitempty"`
	Manifest BackupManifest `json:"manifest"`
	Members  int            `json:"members"`
}

// BackupRestoreResult reports what a restore put back.
type BackupRestoreResult struct {
	Key             string   `json:"key"`
	SitesRestored   []string `json:"sites_restored"`
	DatabasesLoad   []string `json:"databases_restored"`
	FilesRestored   int      `json:"files_restored"`
	BytesRestored   int64    `json:"bytes_restored"`
	Skipped         []string `json:"skipped"`
	PreviousMoved   []string `json:"previous_kept"`
	DurationMS      int64    `json:"duration_ms"`
	ManifestSubject string   `json:"subject"`
}

// BackupCapabilities asks the host what it can do.
func (c *Client) BackupCapabilities(ctx context.Context, requestID string) (
	BackupCapabilities, error,
) {
	var caps BackupCapabilities
	err := c.call(ctx, requestID, protocol.OperationBackupCapabilities,
		map[string]any{}, &caps)
	return caps, err
}

// BackupVerify reads a stored backup back and checks it.
// sealingKey is empty for every archive except a panel backup's, which the
// Agent has to open to verify. It is a secret and is sent only when needed.
func (c *Client) BackupVerify(ctx context.Context, requestID, key, checksum string,
	size int64, destination BackupDestination, sealingKey string,
) (BackupVerifyResult, error) {
	var result BackupVerifyResult
	payload := map[string]any{
		"key":         key,
		"checksum":    checksum,
		"size":        size,
		"destination": destination,
	}
	if sealingKey != "" {
		payload["sealing_key"] = sealingKey
	}
	err := c.call(ctx, requestID, protocol.OperationBackupVerify, payload, &result)
	return result, err
}

// BackupDelete removes one archive from its destination.
func (c *Client) BackupDelete(ctx context.Context, requestID, key string,
	destination BackupDestination,
) error {
	var discarded struct{}
	return c.call(ctx, requestID, protocol.OperationBackupDelete, map[string]any{
		"key":         key,
		"destination": destination,
	}, &discarded)
}

// BackupCheckDestination writes a small object to a destination and reads it
// back, so a destination that cannot work says so while somebody is watching.
func (c *Client) BackupCheckDestination(ctx context.Context, requestID string,
	destination BackupDestination,
) error {
	var discarded struct{}
	return c.call(ctx, requestID, protocol.OperationBackupCheckTarget, map[string]any{
		"destination": destination,
	}, &discarded)
}
