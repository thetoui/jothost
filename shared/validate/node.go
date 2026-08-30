package validate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Errors returned by Node.js validation.
var (
	ErrInvalidNodeVersion = errors.New("invalid Node.js version")
	ErrInvalidAppName     = errors.New("invalid application name")
	ErrInvalidPort        = errors.New("invalid port")
	ErrInvalidEnvKey      = errors.New("invalid environment variable name")
	ErrInvalidStartupFile = errors.New("invalid startup file")
)

// Port bounds for a Node.js application.
//
// The floor is 1024 because anything below it is privileged, and an
// application the panel runs is never privileged. The ceiling is the top of
// the range.
//
// The ephemeral range is excluded as well: the kernel hands those out to
// outgoing connections, so an application asked to listen there starts fine
// most days and fails with "address already in use" on the day something else
// got there first — a fault that looks random and is not.
const (
	MinAppPort = 1024
	MaxAppPort = 65535
	// EphemeralFloor matches the usual Linux default (net.ipv4.ip_local_port_range).
	EphemeralFloor = 32768
)

// nodeVersionPattern accepts a major version, optionally with a minor.
//
// Node is chosen by major version: nobody asks for "22.11.0", they ask for 22,
// and the host has whichever patch its packages provide.
var nodeVersionPattern = regexp.MustCompile(`^([1-9][0-9]?)(\.[0-9]{1,2})?$`)

// appNamePattern is the same shape as a systemd unit name's stem, because that
// is what it becomes.
var appNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,39}$`)

// envKeyPattern is the POSIX environment variable name.
var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)

// reservedEnvKeys are variables the panel sets itself, or that would change
// what runs rather than how it behaves.
//
// LD_PRELOAD and LD_LIBRARY_PATH load code into the process before its own
// entry point. NODE_OPTIONS can name a module to require at startup. PATH
// decides which binaries resolve. None of them belong to an application's
// configuration, and all of them are ways to turn "set an environment
// variable" into "run something else".
var reservedEnvKeys = map[string]struct{}{
	"LD_PRELOAD": {}, "LD_LIBRARY_PATH": {}, "LD_AUDIT": {},
	"NODE_OPTIONS": {}, "NODE_REPL_EXTERNAL_MODULE": {},
	"PATH": {}, "IFS": {}, "SHELL": {}, "BASH_ENV": {}, "ENV": {},
	// Set by the panel from the application's own record; accepting them here
	// would let one value contradict the other.
	"PORT": {}, "HOME": {}, "USER": {}, "PWD": {},
}

// NodeVersion checks a Node.js version.
func NodeVersion(version string) error {
	if version == "" {
		return fmt.Errorf("%w: version is required", ErrInvalidNodeVersion)
	}
	if !nodeVersionPattern.MatchString(version) {
		return fmt.Errorf("%w: %q (expected a major version such as 22)",
			ErrInvalidNodeVersion, version)
	}
	return nil
}

// NodeMajor returns the major version as a string.
func NodeMajor(version string) string {
	if index := strings.IndexByte(version, '.'); index > 0 {
		return version[:index]
	}
	return version
}

// AppName checks a Node.js application's name.
//
// The rules are tight because this becomes a systemd unit name, a log file
// name, and part of a directory path. A value containing a slash, a space, or
// an @ would produce a unit name systemd reads as something else entirely.
func AppName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidAppName)
	}
	if !appNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q must start with a letter and contain only "+
			"lowercase letters, digits, underscores, and dashes", ErrInvalidAppName, name)
	}
	return nil
}

// AppPort checks the port an application listens on.
func AppPort(port int) error {
	if port < MinAppPort {
		return fmt.Errorf("%w: %d is below %d, and a port below that needs root",
			ErrInvalidPort, port, MinAppPort)
	}
	if port > MaxAppPort {
		return fmt.Errorf("%w: %d is above %d", ErrInvalidPort, port, MaxAppPort)
	}
	if port >= EphemeralFloor {
		return fmt.Errorf("%w: %d is in the ephemeral range the kernel assigns to "+
			"outgoing connections, so it will eventually be taken already; choose "+
			"a port below %d", ErrInvalidPort, port, EphemeralFloor)
	}
	return nil
}

// EnvKey checks an environment variable name.
func EnvKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidEnvKey)
	}
	if !envKeyPattern.MatchString(key) {
		return fmt.Errorf("%w: %q must be upper case letters, digits, and "+
			"underscores, and must not start with a digit", ErrInvalidEnvKey, key)
	}
	if _, reserved := reservedEnvKeys[key]; reserved {
		return fmt.Errorf("%w: %q is set by the panel or changes what runs "+
			"rather than how it behaves", ErrInvalidEnvKey, key)
	}
	return nil
}

// EnvValue checks an environment variable's value.
//
// Anything is permitted except the two characters that are not data: a null
// byte truncates the value inside the kernel, and a newline would close the
// line in a systemd unit or an env file and start a directive of its own.
func EnvValue(value string) error {
	if strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("%w: the value contains a null byte", ErrInvalidEnvKey)
	}
	if strings.ContainsAny(value, "\n\r") {
		return fmt.Errorf("%w: the value contains a newline", ErrInvalidEnvKey)
	}
	if len(value) > MaxEnvValueLength {
		return fmt.Errorf("%w: the value is longer than %d characters",
			ErrInvalidEnvKey, MaxEnvValueLength)
	}
	return nil
}

// MaxEnvValueLength bounds one environment variable's value.
const MaxEnvValueLength = 4096

// startupPattern is a relative path inside the application's own directory.
//
// Relative, deliberately: an absolute path would let an application be started
// from a file outside its own root, which is the whole boundary.
var startupPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// StartupFile checks the entry point, relative to the application root.
func StartupFile(path string) error {
	if path == "" {
		return fmt.Errorf("%w: a startup file is required", ErrInvalidStartupFile)
	}
	if strings.HasPrefix(path, "/") {
		return fmt.Errorf("%w: %q must be relative to the application directory",
			ErrInvalidStartupFile, path)
	}
	if !startupPattern.MatchString(path) {
		return fmt.Errorf("%w: %q may contain only letters, digits, dots, dashes, "+
			"underscores, and slashes", ErrInvalidStartupFile, path)
	}
	// The pattern already refuses a path containing "..", because a segment is
	// only accepted when it has at least one character and ".." is two dots —
	// which the pattern does allow. So it is refused explicitly.
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." || segment == "." {
			return fmt.Errorf("%w: %q must not contain a relative segment",
				ErrInvalidStartupFile, path)
		}
	}
	return nil
}

// AppNameFor derives an application name from a domain.
func AppNameFor(domain, hash string) string {
	base := strings.NewReplacer(".", "-", "_", "-").Replace(NormalizeDomain(domain))
	base = strings.TrimLeft(base, "0123456789-")
	if base == "" {
		base = "app"
	}

	suffix := "-" + hash
	limit := 40 - len(suffix)
	if len(base) > limit {
		base = base[:limit]
	}
	return strings.TrimRight(base, "-") + suffix
}
