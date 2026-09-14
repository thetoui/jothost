// Package backup takes, stores, verifies and restores backups of the things
// this panel manages.
//
// # What a backup is here
//
// One gzip-compressed tar archive holding a website's files, the databases it
// uses, or both, and — as its last member — a manifest describing everything
// that went into it with a SHA-256 digest per member. The archive as a whole
// has a digest of its own, computed while it is written.
//
// The manifest is last rather than first because the digests it records are not
// known until the members have been written, and a manifest that had to be
// written first could only describe what was *intended*. Reading it therefore
// costs a scan to the end of the archive, which is exactly what verifying does
// anyway.
//
// # The rule this package is built around
//
// A backup that has not been read back is not a backup. Every path here ends
// with the archive being read from where it was stored — not from the staging
// copy that is still in the page cache — and its digest recomputed. An upload
// that returned 200 and wrote nothing, a disk that silently dropped the last
// block, a destination configured to a directory that no longer exists: all
// three report success at the moment of writing and are only ever caught by
// reading back.
//
// This is also why CLAUDE.md section 18's rule ("never overwrite a valid backup
// before verifying the new backup") is not implemented as a special case. The
// panel prunes only after the new backup has been read back and matched, so
// there is no ordering in which a good archive is removed in favour of one that
// was never checked.
//
// # Restoring
//
// A restore reads the whole archive and verifies it *before* it changes
// anything, then extracts in a second pass. Two passes over a local file is a
// cheap price for never half-restoring an archive that turns out to be
// truncated — and a half-restored site is worse than an untouched broken one,
// because it looks repaired.
//
// The document root is moved aside rather than overwritten, and only removed
// once the restore has finished. A restore that fails partway puts the original
// back.
//
// # What never comes from a request
//
// No path executed, resolved or written here is taken from a request without
// passing shared/validate. Object keys are checked by validate.BackupKey, which
// refuses dot segments, backslashes and leading dashes, so one key is safe as a
// filesystem path under a local directory, as a remote path over SFTP, and as a
// URL path against S3. Document roots are resolved against the Agent's own site
// root. Nothing reaches a shell: there is no shell in this package.
package backup

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/database"
)

// ManifestName is the archive member describing the archive.
const ManifestName = "manifest.json"

// ManifestVersion is the manifest format this Agent writes.
//
// It is checked on restore. An archive from a newer panel may contain member
// kinds this one does not understand, and restoring it partially — quietly
// dropping what it could not read — would be the worst possible behaviour for
// a restore.
const ManifestVersion = 1

// Archive member prefixes.
const (
	// SitesPrefix holds one directory per website, named by its domain.
	SitesPrefix = "sites"
	// DatabasesPrefix holds one dump per database, under its engine.
	DatabasesPrefix = "databases"
)

// Errors a caller can act on.
var (
	// ErrUnavailable means this Agent cannot take backups at all, which in
	// practice means it has no working directory it can write to.
	ErrUnavailable = errors.New("this host cannot take backups")
	// ErrUnsupportedDestination covers a destination kind this Agent cannot
	// reach — SFTP on a host with no sftp client, for instance.
	ErrUnsupportedDestination = errors.New("this host cannot reach that destination")
	// ErrDestinationFailed wraps a failure talking to a destination.
	ErrDestinationFailed = errors.New("the backup destination could not be used")
	// ErrNotFound means the destination has no object under that key.
	ErrNotFound = errors.New("that backup is not at its destination")
	// ErrCorrupt means an archive did not match what was recorded about it.
	ErrCorrupt = errors.New("the backup does not match its checksum")
	// ErrNothingToBackUp means the request named nothing that exists.
	ErrNothingToBackUp = errors.New("there is nothing to back up")
	// ErrArchiveMalformed covers an archive this Agent cannot read.
	ErrArchiveMalformed = errors.New("the backup archive could not be read")
	// ErrRestoreFailed wraps a failure putting data back.
	ErrRestoreFailed = errors.New("the restore failed")
	// ErrTooLarge means a member exceeds what this Agent will extract.
	ErrTooLarge = errors.New("the backup is larger than this host will extract")
)

// Limits on what may be written or read back.
//
// A backup is a place where an unbounded input becomes an unbounded write: an
// archive claiming a member of a petabyte would fill the host's disk during a
// restore and take every site on it down, which is the opposite of what a
// restore is for.
const (
	// MaxArchiveBytes bounds one archive.
	MaxArchiveBytes int64 = 64 << 30 // 64 GiB
	// MaxMemberBytes bounds one file inside an archive.
	MaxMemberBytes int64 = 32 << 30 // 32 GiB
	// MaxMembers bounds how many entries an archive may hold.
	MaxMembers = 2_000_000
	// MaxManifestBytes bounds the manifest itself, which is read into memory.
	MaxManifestBytes int64 = 64 << 20 // 64 MiB
)

// Member is one file inside an archive.
type Member struct {
	// Path is the member's name within the archive, always slash-separated.
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Checksum is the SHA-256 of the member's contents, in lowercase hex.
	// Empty for a directory or a symlink, which have no contents to digest.
	Checksum string `json:"checksum,omitempty"`
	Mode     uint32 `json:"mode"`
	// Link is the target of a symlink, empty otherwise.
	Link string `json:"link,omitempty"`
}

// DatabaseMember is one database dump inside an archive.
type DatabaseMember struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// Path is where the dump sits inside the archive.
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

// SiteEntry describes one website inside an archive.
//
// DocumentRoot is recorded as it was on the machine the backup came from. It is
// not where a restore necessarily puts it back: restoring onto a new host, where
// the site root differs, is the case this phase exists to make possible.
type SiteEntry struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user,omitempty"`
	// Prefix is the directory inside the archive holding this site's files.
	Prefix    string `json:"prefix"`
	FileCount int    `json:"file_count"`
	Bytes     int64  `json:"bytes"`
}

// Manifest describes an archive, and is itself the archive's last member.
type Manifest struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	// Subject is what this is a backup of: a domain, a database name, or the
	// host's name for a full backup.
	Subject   string    `json:"subject"`
	CreatedAt time.Time `json:"created_at"`
	Hostname  string    `json:"hostname"`
	// AgentVersion records what wrote it, so an archive that cannot be read
	// can at least say what produced it.
	AgentVersion string `json:"agent_version,omitempty"`

	Sites     []SiteEntry      `json:"sites"`
	Databases []DatabaseMember `json:"databases"`
	Files     []Member         `json:"files"`

	FileCount int   `json:"file_count"`
	FileBytes int64 `json:"file_bytes"`
	// Skipped names things that were asked for and left out, with the reason.
	// An empty list is a stronger statement than a missing field: it says the
	// backup holds everything that was asked for.
	Skipped []string `json:"skipped"`
}

// SiteSpec names one website to back up or restore.
type SiteSpec struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user,omitempty"`
}

// DatabaseSpec names one database to back up or restore.
type DatabaseSpec struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
}

// Destination is where an archive is written and read back from.
//
// The credential fields are secrets. They arrive over the Agent's Unix socket
// as part of a request and are never logged, never written to the audit trail,
// and never persisted: the panel keeps them encrypted and hands them over for
// the length of one operation. Nothing in this package prints a Destination.
type Destination struct {
	Kind string `json:"kind"`

	// Local.
	Directory string `json:"directory,omitempty"`

	// S3-compatible.
	Endpoint  string `json:"endpoint,omitempty"`
	Region    string `json:"region,omitempty"`
	Bucket    string `json:"bucket,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	AccessKey string `json:"access_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"`
	// PathStyle addresses the bucket in the URL path rather than the host.
	// Every self-hosted S3 implementation needs it; AWS itself does not.
	PathStyle bool `json:"path_style,omitempty"`
	// AllowInsecure permits a plain-http endpoint that is not on this machine.
	//
	// It is never a default and never inferred. Somebody configuring a
	// destination has to accept that the archive — every file of every site on
	// this host — travels where anyone on the path can read it.
	AllowInsecure bool `json:"allow_insecure,omitempty"`

	// SFTP.
	Host string `json:"host,omitempty"`
	Port int    `json:"port,omitempty"`
	User string `json:"user,omitempty"`
	Path string `json:"path,omitempty"`
	// PrivateKey is an OpenSSH private key in PEM form. A password is
	// deliberately not offered: sftp reads one only from a terminal, so
	// supporting it would mean either a pseudo-terminal or sshpass, and both
	// put a password somewhere the process table can see.
	PrivateKey string `json:"private_key,omitempty"`
	// HostKey is the server's public key, as one known_hosts line without the
	// leading hostname. Without it there is no way to tell the intended server
	// from whatever answered, so it is required rather than optional and
	// StrictHostKeyChecking is never turned off.
	HostKey string `json:"host_key,omitempty"`
}

// Result reports a completed backup.
type Result struct {
	Key      string `json:"key"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
	// Verified reports whether the archive was read back from the destination
	// and matched. A false here with no error means the bytes were written and
	// could not be confirmed, which is not the same as a backup.
	Verified     bool     `json:"verified"`
	VerifyDetail string   `json:"verify_detail,omitempty"`
	Manifest     Manifest `json:"manifest"`
	DurationMS   int64    `json:"duration_ms"`
}

// VerifyResult reports on reading an archive back.
type VerifyResult struct {
	Key   string `json:"key"`
	OK    bool   `json:"ok"`
	Size  int64  `json:"size"`
	Bytes int64  `json:"bytes_read"`
	// Checksum is what was actually computed, so a mismatch can be reported
	// with both numbers rather than only "no".
	Checksum string   `json:"checksum"`
	Expected string   `json:"expected,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Manifest Manifest `json:"manifest"`
	// Members is how many archive entries were checked.
	Members int `json:"members"`
}

// RestoreResult reports what a restore put back.
type RestoreResult struct {
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

// Options configure a Provider.
type Options struct {
	Runner    *command.Runner
	Databases *database.Manager
	Log       *slog.Logger

	// WorkDir is where archives are staged before they are sent anywhere and
	// where they are downloaded to before a restore. It is the Agent's own,
	// never a value from a request.
	WorkDir string
	// SiteRoot is the directory websites live under. Every document root a
	// request names is resolved against it, so a request cannot ask for a
	// backup of /etc.
	SiteRoot string
	// LocalRoots are the directories a local destination may write into. A
	// local destination outside them is refused: without this, "back up to
	// /etc/nginx" would be a way to write an arbitrary file as root.
	LocalRoots []string

	// PanelDatabase names the panel's own database, which a panel backup
	// dumps. It comes from AGENT_PANEL_DATABASE and never from a request.
	// Empty means this host cannot take panel backups.
	PanelDatabase string

	// HTTPClient talks to S3. Nil builds one with a sane timeout.
	HTTPClient *http.Client
	// Hostname is recorded in manifests. Empty asks the OS.
	Hostname string
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

// Provider takes and restores backups.
type Provider struct {
	runner    *command.Runner
	databases *database.Manager
	log       *slog.Logger

	workDir       string
	siteRoot      string
	localRoots    []string
	panelDatabase string

	http     *http.Client
	hostname string
	now      func() time.Time
}
