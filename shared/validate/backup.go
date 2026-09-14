package validate

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"
)

// Errors returned by backup validation.
var (
	// ErrInvalidBackupType covers a kind of backup the panel does not take.
	ErrInvalidBackupType = errors.New("invalid backup type")
	// ErrInvalidDestination covers a destination the panel will not write to.
	ErrInvalidDestination = errors.New("invalid backup destination")
	// ErrInvalidBackupKey covers an object key within a destination.
	ErrInvalidBackupKey = errors.New("invalid backup key")
	// ErrInvalidRetention covers a retention setting that would lose data.
	ErrInvalidRetention = errors.New("invalid retention")
	// ErrInvalidBackupSchedule covers a backup schedule.
	ErrInvalidBackupSchedule = errors.New("invalid backup schedule")
	// ErrInvalidChecksum covers a digest that is not a SHA-256 hex string.
	ErrInvalidChecksum = errors.New("invalid checksum")
)

// What a backup can be of.
//
// Four, and deliberately no "files under an arbitrary path". A backup names a
// thing the panel manages, so restoring it has somewhere unambiguous to go; a
// backup of a path somebody typed is a restore with nowhere to put it back.
const (
	// BackupWebsite is a website's files together with the databases attached
	// to it. The pair is one backup rather than two because restoring the files
	// of a PHP application without the schema they expect produces a site that
	// is broken in a more confusing way than one that is simply gone.
	BackupWebsite = "website"
	// BackupDatabase is one database's dump.
	BackupDatabase = "database"
	// BackupFull is every website and every database on the host.
	BackupFull = "full"
	// BackupPanel is the panel's own database: its users, websites, schedules,
	// credentials and audit trail. Its archive is sealed, it needs
	// server.manage as well as backup.manage, and it is restored from the host
	// rather than from the panel (docs/PANEL_BACKUP.md).
	BackupPanel = "panel"
)

// BackupTypes is the supported set, in the order a UI should offer them.
var BackupTypes = []string{BackupWebsite, BackupDatabase, BackupFull, BackupPanel}

// BackupType checks what is being backed up.
func BackupType(kind string) error {
	switch kind {
	case BackupWebsite, BackupDatabase, BackupFull, BackupPanel:
		return nil
	default:
		return fmt.Errorf("%w: %q is not one of %s",
			ErrInvalidBackupType, kind, strings.Join(BackupTypes, ", "))
	}
}

// Where a backup can be written.
const (
	// DestinationLocal is a directory on the host itself.
	DestinationLocal = "local"
	// DestinationS3 is an S3-compatible bucket.
	DestinationS3 = "s3"
	// DestinationSFTP is a directory on another machine, over SSH.
	DestinationSFTP = "sftp"
)

// DestinationKinds is the supported set, in the order a UI should offer them.
var DestinationKinds = []string{DestinationLocal, DestinationS3, DestinationSFTP}

// DestinationKind checks where a backup is to be written.
func DestinationKind(kind string) error {
	switch kind {
	case DestinationLocal, DestinationS3, DestinationSFTP:
		return nil
	default:
		return fmt.Errorf("%w: %q is not one of %s",
			ErrInvalidDestination, kind, strings.Join(DestinationKinds, ", "))
	}
}

// MaxDestinationNameLength bounds a destination's display name.
const MaxDestinationNameLength = 100

// DestinationName checks the name an operator gives a destination.
func DestinationName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalidDestination)
	}
	if len(trimmed) > MaxDestinationNameLength {
		return fmt.Errorf("%w: a name may be at most %d characters",
			ErrInvalidDestination, MaxDestinationNameLength)
	}
	for _, r := range trimmed {
		// Control characters would corrupt a log line and a listing. Anything
		// printable is allowed: this is a label, not an identifier, and it never
		// becomes a path or an argument.
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: a name may not contain control characters",
				ErrInvalidDestination)
		}
	}
	return nil
}

// MaxBackupKeyLength bounds an object key.
const MaxBackupKeyLength = 512

// BackupKey checks the name of an archive within a destination.
//
// A key is a relative, slash-separated path. It is checked here because it
// becomes a filesystem path under a local destination's directory, a remote
// path over SFTP, and a URL path against S3 — three different things that all
// go wrong the same way if a key may contain "..".
//
// The rules are deliberately narrow: lowercase-friendly filename characters,
// no leading slash, no dot segments, no empty segments, no backslash. A key
// that survives this cannot escape any of the three, and cannot be read as an
// option by anything it is passed to.
func BackupKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: a key is required", ErrInvalidBackupKey)
	}
	if len(key) > MaxBackupKeyLength {
		return fmt.Errorf("%w: a key may be at most %d characters",
			ErrInvalidBackupKey, MaxBackupKeyLength)
	}
	if strings.HasPrefix(key, "/") {
		return fmt.Errorf("%w: a key is relative to the destination, so it may not start with /",
			ErrInvalidBackupKey)
	}
	if strings.HasPrefix(key, "-") {
		return fmt.Errorf("%w: a key may not start with a dash", ErrInvalidBackupKey)
	}
	if strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("%w: a key may not contain a backslash or a null byte",
			ErrInvalidBackupKey)
	}

	for _, segment := range strings.Split(key, "/") {
		switch segment {
		case "":
			return fmt.Errorf("%w: a key may not contain an empty path segment",
				ErrInvalidBackupKey)
		case ".", "..":
			return fmt.Errorf("%w: a key may not contain %q", ErrInvalidBackupKey, segment)
		}
		for _, r := range segment {
			ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
			if !ok {
				return fmt.Errorf(
					"%w: a key may contain only letters, digits, dots, dashes, underscores and slashes",
					ErrInvalidBackupKey)
			}
		}
	}

	// path.Clean must agree, which catches anything the segment loop missed.
	if path.Clean(key) != key {
		return fmt.Errorf("%w: %q is not a normalised path", ErrInvalidBackupKey, key)
	}
	return nil
}

// MaxLocalRootLength bounds a local destination's directory.
const MaxLocalRootLength = 512

// LocalBackupRoot checks the directory a local destination writes into.
//
// It must be absolute and normalised. It is not resolved here — that is the
// Agent's job, against the roots it is configured with — but a path that is
// relative or contains ".." is refused before it can travel any further, so
// the failure happens where somebody typed it rather than three layers away.
func LocalBackupRoot(dir string) error {
	if dir == "" {
		return fmt.Errorf("%w: a directory is required", ErrInvalidDestination)
	}
	if len(dir) > MaxLocalRootLength {
		return fmt.Errorf("%w: a directory may be at most %d characters",
			ErrInvalidDestination, MaxLocalRootLength)
	}
	if !strings.HasPrefix(dir, "/") {
		return fmt.Errorf("%w: %q is not an absolute path", ErrInvalidDestination, dir)
	}
	if strings.Contains(dir, "\x00") {
		return fmt.Errorf("%w: a directory may not contain a null byte", ErrInvalidDestination)
	}
	if path.Clean(dir) != strings.TrimRight(dir, "/") && path.Clean(dir) != dir {
		return fmt.Errorf("%w: %q is not a normalised path", ErrInvalidDestination, dir)
	}
	for _, segment := range strings.Split(strings.Trim(dir, "/"), "/") {
		if segment == ".." || segment == "." {
			return fmt.Errorf("%w: a directory may not contain %q",
				ErrInvalidDestination, segment)
		}
	}
	return nil
}

// MaxBucketLength bounds an S3 bucket name.
const MaxBucketLength = 63

// S3Bucket checks a bucket name.
//
// The rules are AWS's, which every S3-compatible implementation accepts: 3 to
// 63 characters, lowercase letters, digits, dots and dashes, starting and
// ending alphanumeric. They are enforced rather than passed through because a
// bucket goes into a URL host or path, and a name with a slash in it would
// change which object is addressed.
func S3Bucket(bucket string) error {
	if len(bucket) < 3 || len(bucket) > MaxBucketLength {
		return fmt.Errorf("%w: a bucket name is between 3 and %d characters",
			ErrInvalidDestination, MaxBucketLength)
	}
	for i, r := range bucket {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf(
				"%w: a bucket name may contain only lowercase letters, digits, dots and dashes",
				ErrInvalidDestination)
		}
		first := i == 0
		last := i == len(bucket)-1
		if (first || last) && (r == '-' || r == '.') {
			return fmt.Errorf("%w: a bucket name starts and ends with a letter or digit",
				ErrInvalidDestination)
		}
	}
	if strings.Contains(bucket, "..") {
		return fmt.Errorf("%w: a bucket name may not contain two dots in a row",
			ErrInvalidDestination)
	}
	return nil
}

// MaxEndpointLength bounds an S3 endpoint URL.
const MaxEndpointLength = 255

// S3Endpoint checks the base URL of an S3-compatible service.
//
// https is required, with two exceptions.
//
// The first is a loopback address, which needs no acknowledgement: a request
// that never leaves the machine cannot be read on the way.
//
// The second is allowInsecure, and it exists because the alternative was worse.
// Self-hosted object storage on a private network — a MinIO box in the same
// rack, reached by its internal name — is an ordinary arrangement, and a panel
// that flatly refused it would be worked around rather than obeyed. So plain
// http is possible and is never the default: somebody has to say, in the
// destination, that they accept it. The panel then labels that destination as
// insecure wherever it is shown, because a backup sent in the clear is a copy
// of every site on the host readable by anyone on the path.
func S3Endpoint(endpoint string, allowInsecure bool) error {
	if endpoint == "" {
		return fmt.Errorf("%w: an endpoint is required", ErrInvalidDestination)
	}
	if len(endpoint) > MaxEndpointLength {
		return fmt.Errorf("%w: an endpoint may be at most %d characters",
			ErrInvalidDestination, MaxEndpointLength)
	}

	parsed, err := url.Parse(endpoint)
	if err != nil {
		return fmt.Errorf("%w: %q is not a URL", ErrInvalidDestination, endpoint)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("%w: an endpoint is an http or https URL", ErrInvalidDestination)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: an endpoint needs a host", ErrInvalidDestination)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return fmt.Errorf("%w: an endpoint is a scheme and a host, without a path",
			ErrInvalidDestination)
	}
	if parsed.User != nil {
		return fmt.Errorf("%w: credentials belong in the destination's key and secret, not in its URL",
			ErrInvalidDestination)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%w: an endpoint has no query string or fragment",
			ErrInvalidDestination)
	}
	if parsed.Scheme == "http" && !isLoopbackHost(parsed.Hostname()) && !allowInsecure {
		return fmt.Errorf(
			"%w: an endpoint that is not on this machine must be https, or the backup travels "+
				"in the clear; accept that explicitly if the service is on a private network",
			ErrInvalidDestination)
	}
	return nil
}

// isLoopbackHost reports whether a hostname addresses this machine.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// MaxRegionLength bounds an S3 region.
const MaxRegionLength = 64

// S3Region checks a region name, which is part of the signature.
func S3Region(region string) error {
	if region == "" {
		return fmt.Errorf("%w: a region is required; use us-east-1 if the service ignores it",
			ErrInvalidDestination)
	}
	if len(region) > MaxRegionLength {
		return fmt.Errorf("%w: a region may be at most %d characters",
			ErrInvalidDestination, MaxRegionLength)
	}
	for _, r := range region {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
		if !ok {
			return fmt.Errorf("%w: a region may contain only lowercase letters, digits and dashes",
				ErrInvalidDestination)
		}
	}
	return nil
}

// SFTPHost checks the machine a backup is sent to.
func SFTPHost(host string) error {
	if host == "" {
		return fmt.Errorf("%w: a host is required", ErrInvalidDestination)
	}
	if ip := net.ParseIP(host); ip != nil {
		return nil
	}
	return Hostname(host)
}

// SFTPPort checks the port.
func SFTPPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: a port is between 1 and 65535", ErrInvalidDestination)
	}
	return nil
}

// MaxSFTPUserLength bounds an SFTP account name.
const MaxSFTPUserLength = 64

// SFTPUser checks the account a backup logs in as.
//
// The leading-dash rule matters here for the same reason it does for a package
// name: the account becomes part of a "user@host" argument, and a value read as
// an option changes what the client does rather than where it connects.
func SFTPUser(user string) error {
	if user == "" {
		return fmt.Errorf("%w: a username is required", ErrInvalidDestination)
	}
	if len(user) > MaxSFTPUserLength {
		return fmt.Errorf("%w: a username may be at most %d characters",
			ErrInvalidDestination, MaxSFTPUserLength)
	}
	if strings.HasPrefix(user, "-") {
		return fmt.Errorf("%w: a username may not start with a dash", ErrInvalidDestination)
	}
	for _, r := range user {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_'
		if !ok {
			return fmt.Errorf(
				"%w: a username may contain only letters, digits, dots, dashes and underscores",
				ErrInvalidDestination)
		}
	}
	return nil
}

// Retention bounds.
const (
	// MinRetentionDays is one: a retention of zero would delete a backup the
	// moment it finished.
	MinRetentionDays = 1
	// MaxRetentionDays is ten years, which is longer than any hosting
	// arrangement this panel is for and short enough to be a number.
	MaxRetentionDays = 3650
	// MinKeepLast is one. A schedule that can prune its way to nothing is a
	// schedule that eventually does.
	MinKeepLast = 1
	// MaxKeepLast bounds the count floor.
	MaxKeepLast = 365
)

// RetentionDays checks how long backups are kept.
func RetentionDays(days int) error {
	if days < MinRetentionDays || days > MaxRetentionDays {
		return fmt.Errorf("%w: keep backups for between %d and %d days",
			ErrInvalidRetention, MinRetentionDays, MaxRetentionDays)
	}
	return nil
}

// KeepLast checks how many backups are kept regardless of age.
//
// This is the floor that makes retention by age safe. A panel that was off for
// a fortnight would otherwise come back, find every backup older than the
// retention window, and delete all of them — leaving nothing at the exact
// moment somebody is most likely to need something.
func KeepLast(count int) error {
	if count < MinKeepLast || count > MaxKeepLast {
		return fmt.Errorf("%w: always keep between %d and %d of the most recent backups",
			ErrInvalidRetention, MinKeepLast, MaxKeepLast)
	}
	return nil
}

// MaxScheduleNameLength bounds a schedule's name.
const MaxScheduleNameLength = 100

// ScheduleName checks what a schedule is called.
func ScheduleName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalidBackupSchedule)
	}
	if len(trimmed) > MaxScheduleNameLength {
		return fmt.Errorf("%w: a name may be at most %d characters",
			ErrInvalidBackupSchedule, MaxScheduleNameLength)
	}
	return nil
}

// ScheduleTime checks the hour and minute a schedule runs, in UTC.
func ScheduleTime(hour, minute int) error {
	if hour < 0 || hour > 23 {
		return fmt.Errorf("%w: the hour is between 0 and 23", ErrInvalidBackupSchedule)
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("%w: the minute is between 0 and 59", ErrInvalidBackupSchedule)
	}
	return nil
}

// ScheduleDay checks the day of week, where EveryDay means every day.
func ScheduleDay(day int) error {
	if day == EveryDay {
		return nil
	}
	if day < 0 || day > 6 {
		return fmt.Errorf("%w: the day is 0 to 6 with Sunday first, or every day",
			ErrInvalidBackupSchedule)
	}
	return nil
}

// Checksum checks a SHA-256 digest in lowercase hexadecimal.
//
// It is validated rather than compared loosely because it is the whole basis of
// verification: a digest in the wrong case, or with whitespace around it, would
// never match and would report every intact archive as corrupt.
func Checksum(digest string) error {
	const sha256HexLength = 64
	if len(digest) != sha256HexLength {
		return fmt.Errorf("%w: a SHA-256 digest is %d hex characters",
			ErrInvalidChecksum, sha256HexLength)
	}
	for _, r := range digest {
		ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !ok {
			return fmt.Errorf("%w: a digest is lowercase hexadecimal", ErrInvalidChecksum)
		}
	}
	return nil
}
