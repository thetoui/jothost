package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by FTP validation.
var (
	ErrInvalidFTPUser     = errors.New("invalid FTP user name")
	ErrInvalidFTPPassword = errors.New("invalid FTP password")
	ErrInvalidFTPHome     = errors.New("invalid FTP home directory")
	ErrInvalidFTPAccess   = errors.New("invalid FTP access level")
	ErrInvalidFTPQuota    = errors.New("invalid FTP quota")
	ErrInvalidPortRange   = errors.New("invalid passive port range")
)

// What an FTP account may do.
//
// Two levels rather than a permission matrix. FTP has no meaningful middle
// ground — a client that can write can rename, overwrite and delete, because
// those are the same STOR/DELE/RNTO verbs — so offering more would be offering
// distinctions the protocol does not make.
const (
	// AccessFull may read and write.
	AccessFull = "full"
	// AccessReadOnly may list and download, and nothing else.
	AccessReadOnly = "readonly"
)

// FTPAccessLevels returns the levels, for a caller offering a choice.
func FTPAccessLevels() []string { return []string{AccessFull, AccessReadOnly} }

// FTPAccess checks an access level.
func FTPAccess(level string) error {
	switch level {
	case AccessFull, AccessReadOnly:
		return nil
	}
	return fmt.Errorf("%w: %q", ErrInvalidFTPAccess, level)
}

// Bounds.
const (
	// MinFTPUserLength keeps a name long enough to be deliberate.
	MinFTPUserLength = 3
	// MaxFTPUserLength is what the AuthUserFile format and most clients cope
	// with comfortably.
	MaxFTPUserLength = 32
	// MinFTPPasswordLength is what the panel will write. An FTP password is
	// sent over the wire in plain text unless the client uses TLS, and it opens
	// a door into a customer's files, so it is not a place for four characters.
	MinFTPPasswordLength = 12
	MaxFTPPasswordLength = 128
	// MaxFTPQuotaMB is a terabyte. A quota is a guard rail, not a disk.
	MaxFTPQuotaMB = 1024 * 1024
)

// FTPUsername checks an account name.
//
// The name becomes a field in a colon-separated file, an argument to ftpasswd,
// and the identity a client logs in with. A colon would end the field early and
// give the next one a value the panel did not write.
func FTPUsername(name string) error {
	if len(name) < MinFTPUserLength || len(name) > MaxFTPUserLength {
		return fmt.Errorf("%w: between %d and %d characters",
			ErrInvalidFTPUser, MinFTPUserLength, MaxFTPUserLength)
	}

	first := rune(name[0])
	if !(first >= 'a' && first <= 'z') && !(first >= 'A' && first <= 'Z') {
		return fmt.Errorf("%w: it must start with a letter", ErrInvalidFTPUser)
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return fmt.Errorf(
				"%w: letters, digits, dots, hyphens and underscores only", ErrInvalidFTPUser)
		}
	}
	return nil
}

// FTPPassword checks a password before it is handed to ftpasswd.
//
// Length rather than a character-class rule. A required symbol produces
// "Password1!" and nothing else; length is the property that actually costs an
// attacker something, and this password is one an FTP client stores in a
// profile rather than one a person types daily.
func FTPPassword(password string) error {
	if len(password) < MinFTPPasswordLength {
		return fmt.Errorf("%w: at least %d characters",
			ErrInvalidFTPPassword, MinFTPPasswordLength)
	}
	if len(password) > MaxFTPPasswordLength {
		return fmt.Errorf("%w: at most %d characters",
			ErrInvalidFTPPassword, MaxFTPPasswordLength)
	}
	// A newline would end the line in the password file; a colon would end the
	// field. Neither can reach the file.
	if strings.ContainsAny(password, "\n\r\x00:") {
		return fmt.Errorf(
			"%w: it cannot contain a colon, a line break or a null byte",
			ErrInvalidFTPPassword)
	}
	return nil
}

// FTPHome checks the subdirectory an account is confined to, and returns it
// cleaned.
//
// Relative to the website's own directory, always. An absolute path here would
// be an account rooted anywhere on the host, which is the whole thing the chroot
// exists to prevent — and ".." is refused rather than normalised, so that an
// operator who typed one is told rather than quietly given somewhere else.
func FTPHome(subpath string) (string, error) {
	cleaned := strings.TrimSpace(subpath)

	// Refused, not trimmed. Stripping the slash off "/etc" would silently turn
	// a request for the real /etc into one for "<website>/etc" — a different
	// directory, quite possibly one that does not exist, and never the one that
	// was asked for. An operator who typed an absolute path has misunderstood
	// what this field is, and should be told.
	if strings.HasPrefix(cleaned, "/") {
		return "", fmt.Errorf(
			"%w: it is relative to the website's own directory, so it cannot start with \"/\"",
			ErrInvalidFTPHome)
	}

	trimmed := strings.TrimSuffix(cleaned, "/")
	if trimmed == "" {
		// The site's own directory, which is the common case.
		return "", nil
	}
	if len(trimmed) > 255 {
		return "", fmt.Errorf("%w: it is too long", ErrInvalidFTPHome)
	}
	if strings.ContainsAny(trimmed, "\n\r\x00:\\") {
		return "", fmt.Errorf("%w: it contains a character a path cannot carry",
			ErrInvalidFTPHome)
	}

	for _, segment := range strings.Split(trimmed, "/") {
		switch segment {
		case "", ".":
			return "", fmt.Errorf("%w: it has an empty segment", ErrInvalidFTPHome)
		case "..":
			return "", fmt.Errorf(
				"%w: it may not contain \"..\" — an FTP account is confined to its website",
				ErrInvalidFTPHome)
		}
	}
	return trimmed, nil
}

// FTPQuotaMB checks a disk limit in megabytes. Zero means no limit.
func FTPQuotaMB(megabytes int) error {
	if megabytes < 0 || megabytes > MaxFTPQuotaMB {
		return fmt.Errorf("%w: between 0 (no limit) and %d MB",
			ErrInvalidFTPQuota, MaxFTPQuotaMB)
	}
	return nil
}

// Passive port bounds.
//
// The floor is above the privileged range and above the ports a panel-managed
// host already uses. The span has a minimum because each concurrent transfer
// takes one port: a range of ten is a server that refuses the eleventh
// simultaneous transfer with an error nobody can interpret.
const (
	MinPassivePort = 1024
	MaxPassivePort = 65535
	MinPassiveSpan = 16
	MaxPassiveSpan = 5000
)

// PassivePortRange checks the range the data connections use.
func PassivePortRange(from, to int) error {
	if from < MinPassivePort || from > MaxPassivePort ||
		to < MinPassivePort || to > MaxPassivePort {
		return fmt.Errorf("%w: ports must be between %d and %d",
			ErrInvalidPortRange, MinPassivePort, MaxPassivePort)
	}
	if to < from {
		return fmt.Errorf("%w: the range ends before it starts", ErrInvalidPortRange)
	}

	span := to - from + 1
	if span < MinPassiveSpan {
		return fmt.Errorf(
			"%w: at least %d ports — each transfer in progress uses one, and a range "+
				"this small refuses transfers under any real load",
			ErrInvalidPortRange, MinPassiveSpan)
	}
	if span > MaxPassiveSpan {
		return fmt.Errorf("%w: at most %d ports", ErrInvalidPortRange, MaxPassiveSpan)
	}
	return nil
}
