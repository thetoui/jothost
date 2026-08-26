package validate

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Errors returned by PHP validation.
var (
	ErrInvalidPHPVersion = errors.New("invalid PHP version")
	ErrInvalidPHPSetting = errors.New("invalid PHP setting")
)

// phpVersionPattern matches a major.minor version such as "8.3".
//
// Only major.minor is accepted, because that is what selects a package, a
// binary, and a pool directory. A patch level ("8.3.19") is a property of what
// happens to be installed, not something a user chooses, and accepting one
// would let two spellings of the same choice exist.
var phpVersionPattern = regexp.MustCompile(`^[5-9]\.[0-9]{1,2}$`)

// PHPVersion checks a PHP version selector.
//
// The value reaches a package name, a binary path, and a configuration file
// path, so it is validated strictly rather than merely non-empty.
func PHPVersion(version string) error {
	if version == "" {
		return fmt.Errorf("%w: a version is required", ErrInvalidPHPVersion)
	}
	if !phpVersionPattern.MatchString(version) {
		return fmt.Errorf("%w: %q is not a major.minor version such as 8.3",
			ErrInvalidPHPVersion, version)
	}
	return nil
}

// NormalizePHPVersion trims a version and drops a leading "php".
//
// "PHP 8.3", "php8.3", and "8.3" all name one version; storing them as
// different strings would let a site select a version no pool matches.
func NormalizePHPVersion(version string) string {
	normalized := strings.ToLower(strings.TrimSpace(version))
	normalized = strings.TrimPrefix(normalized, "php")
	return strings.TrimSpace(normalized)
}

// PHPVersionCompact renders a version without its dot, as "83".
//
// Alpine names its packages, binaries, and configuration directories this way
// (php83, /usr/sbin/php-fpm83, /etc/php83); Debian keeps the dot. Callers pick
// the form the host uses.
func PHPVersionCompact(version string) string {
	return strings.ReplaceAll(version, ".", "")
}

// PHP setting bounds.
//
// These are limits on what a panel will configure, not on what PHP accepts.
// A site that can set max_execution_time to a day can hold an FPM worker for a
// day, and a handful of those exhaust the pool for every other request.
const (
	MaxExecutionTimeSeconds = 3600
	MaxMemoryLimitMB        = 4096
	MaxUploadSizeMB         = 2048
)

// byteSizePattern matches a PHP shorthand byte value such as "512M".
//
// The suffix is optional and, as in php.ini, a bare number means bytes.
var byteSizePattern = regexp.MustCompile(`^([0-9]+)([KMG]?)$`)

// PHPMemoryLimit checks a memory_limit value.
//
// "-1" is accepted because PHP uses it for "no limit" and a hosting panel has
// legitimate reason to allow it, but every other value is bounded: this string
// is written into a pool configuration file, and an unbounded one lets a
// single site exhaust the host's memory.
func PHPMemoryLimit(value string) error {
	if value == "-1" {
		return nil
	}
	megabytes, err := byteSizeMB(value)
	if err != nil {
		return fmt.Errorf("%w: memory_limit %v", ErrInvalidPHPSetting, err)
	}
	if megabytes > MaxMemoryLimitMB {
		return fmt.Errorf("%w: memory_limit may not exceed %dM",
			ErrInvalidPHPSetting, MaxMemoryLimitMB)
	}
	return nil
}

// PHPUploadSize checks an upload_max_filesize value.
func PHPUploadSize(value string) error {
	megabytes, err := byteSizeMB(value)
	if err != nil {
		return fmt.Errorf("%w: upload_max_filesize %v", ErrInvalidPHPSetting, err)
	}
	if megabytes > MaxUploadSizeMB {
		return fmt.Errorf("%w: upload_max_filesize may not exceed %dM",
			ErrInvalidPHPSetting, MaxUploadSizeMB)
	}
	return nil
}

// PHPExecutionTime checks a max_execution_time value in seconds.
func PHPExecutionTime(seconds int) error {
	if seconds < 0 {
		return fmt.Errorf("%w: max_execution_time may not be negative", ErrInvalidPHPSetting)
	}
	if seconds > MaxExecutionTimeSeconds {
		return fmt.Errorf("%w: max_execution_time may not exceed %d seconds",
			ErrInvalidPHPSetting, MaxExecutionTimeSeconds)
	}
	return nil
}

// byteSizeMB parses a PHP shorthand byte value and returns it in megabytes.
//
// The pattern is anchored, so a value carrying a newline, a semicolon, or a
// second directive cannot pass. That matters more than the arithmetic: this
// string is written verbatim into an FPM pool file, where a newline would let
// a caller append configuration of their choosing.
func byteSizeMB(value string) (int64, error) {
	match := byteSizePattern.FindStringSubmatch(value)
	if match == nil {
		return 0, fmt.Errorf("%q is not a size such as 512M", value)
	}

	amount, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		// The pattern guarantees digits, so this is an overflow rather than a
		// parse failure, and the value is far past any sane limit either way.
		return 0, fmt.Errorf("%q is too large", value)
	}

	switch match[2] {
	case "G":
		return amount * 1024, nil
	case "M":
		return amount, nil
	case "K":
		return amount / 1024, nil
	default:
		// A bare number is bytes.
		return amount / (1024 * 1024), nil
	}
}

// poolNamePattern matches an FPM pool name.
var poolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// PHPPoolName checks an FPM pool name.
//
// The name becomes both a section header inside a configuration file and part
// of a filename, so it is restricted to characters that are safe in each.
func PHPPoolName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a pool name is required", ErrInvalidPHPSetting)
	}
	if !poolNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q is not a valid pool name", ErrInvalidPHPSetting, name)
	}
	return nil
}

// PHPPoolNameFor derives a pool name from a site's system user.
//
// The site's account is already unique per website and already validated as a
// system user name, which makes it the natural pool identity: one pool, one
// account, one site.
func PHPPoolNameFor(systemUser string) string {
	return systemUser
}
