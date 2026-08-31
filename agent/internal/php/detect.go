// Package php manages PHP versions and per-site FPM pools.
//
// Everything here reports what the host actually has rather than what the
// panel would like it to have. A version is "installed" because a binary was
// found and answered a version query, not because a row said so — a panel that
// offers a version nothing can run produces sites that return 502.
package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandPrefix namespaces the per-version FPM entries in the Agent's command
// allowlist, e.g. "php-fpm:8.3".
//
// One entry per version is deliberate. The Runner pins an absolute path per
// name and never consults PATH, so a request naming a version can only ever
// reach a binary that was resolved from the filesystem at startup — never a
// path assembled from the request itself.
const CommandPrefix = "php-fpm:"

// CommandFor returns the allowlist name for a version's FPM binary.
func CommandFor(version string) string { return CommandPrefix + version }

// Version describes one PHP version present on the host.
type Version struct {
	Version string `json:"version"`
	// CLIPath is the command-line interpreter, e.g. /usr/bin/php83. Empty when
	// the host has the FPM package but not the CLI one, which is a normal
	// arrangement — a web server needs FPM and nothing else. It matters here
	// because a scheduled PHP job runs the CLI, and a panel that offered to
	// schedule one on a host without it would be scheduling a job that fails
	// every night.
	CLIPath string `json:"cli_path"`
	// BinaryPath is the FPM binary, e.g. /usr/sbin/php-fpm83.
	BinaryPath string `json:"binary_path"`
	// FPMService is the service unit that runs it, when there is one.
	FPMService string `json:"fpm_service"`
	// PoolDir is where this version reads pool configuration from.
	PoolDir string `json:"pool_dir"`
	// ConfigPath is the version's php.ini.
	ConfigPath string `json:"config_path"`
	// Full is the version as PHP itself reports it, e.g. "8.3.19". It is empty
	// until the binary has been asked, which requires a runner.
	Full      string `json:"full_version"`
	Installed bool   `json:"installed"`
}

// layout describes where a distribution puts one PHP version.
//
// Distributions disagree in every particular, so each supported family
// contributes a naming scheme rather than the code guessing.
type layout struct {
	name       string
	binary     func(version string) string
	cli        func(version string) string
	poolDir    func(version string) string
	configPath func(version string) string
	service    func(version string) string
	pkg        func(version string) string
}

// layouts are probed in order. The first whose binary exists wins for a
// version, so a host running one scheme is never described by another's paths.
var layouts = []layout{
	{
		// Alpine: php83-fpm, /usr/sbin/php-fpm83, /etc/php83/php-fpm.d.
		name:       "alpine",
		binary:     func(v string) string { return "/usr/sbin/php-fpm" + validate.PHPVersionCompact(v) },
		cli:        func(v string) string { return "/usr/bin/php" + validate.PHPVersionCompact(v) },
		poolDir:    func(v string) string { return "/etc/php" + validate.PHPVersionCompact(v) + "/php-fpm.d" },
		configPath: func(v string) string { return "/etc/php" + validate.PHPVersionCompact(v) + "/php.ini" },
		service:    func(v string) string { return "php-fpm" + validate.PHPVersionCompact(v) },
		pkg:        func(v string) string { return "php" + validate.PHPVersionCompact(v) + "-fpm" },
	},
	{
		// Debian and Ubuntu, including Sury: php8.3-fpm,
		// /usr/sbin/php-fpm8.3, /etc/php/8.3/fpm/pool.d.
		name:       "debian",
		binary:     func(v string) string { return "/usr/sbin/php-fpm" + v },
		cli:        func(v string) string { return "/usr/bin/php" + v },
		poolDir:    func(v string) string { return "/etc/php/" + v + "/fpm/pool.d" },
		configPath: func(v string) string { return "/etc/php/" + v + "/fpm/php.ini" },
		service:    func(v string) string { return "php" + v + "-fpm" },
		pkg:        func(v string) string { return "php" + v + "-fpm" },
	},
}

// KnownVersions are the versions probed for, newest first.
//
// The list is explicit rather than a filesystem glob: these strings become
// package names and configuration paths, and a glob over /usr/sbin would let
// whatever happens to be named php-fpm* decide what the panel offers.
var KnownVersions = []string{"8.5", "8.4", "8.3", "8.2", "8.1", "8.0", "7.4"}

// versionOutput matches the version PHP prints, e.g. "PHP 8.3.19 (fpm-fcgi)".
var versionOutput = regexp.MustCompile(`PHP\s+(\d+\.\d+\.\d+)`)

// ErrVersionNotInstalled means the host has no such PHP.
var ErrVersionNotInstalled = errors.New("this PHP version is not installed on the host")

// Probe finds installed PHP versions by inspecting the filesystem only.
//
// It executes nothing, which is what lets it run before the command allowlist
// exists: the Agent probes, builds an allowlist from what it found, and only
// then asks those binaries anything.
//
// root prefixes every path and is empty in production; tests set it to a
// temporary directory so detection can be exercised without a real host.
func Probe(root string) []Version {
	found := make([]Version, 0, len(KnownVersions))

	for _, version := range KnownVersions {
		for _, scheme := range layouts {
			binary := scheme.binary(version)
			if !fileExists(filepath.Join(root, binary)) {
				continue
			}
			// The CLI is reported only where it exists: an empty path is how a
			// caller learns this version cannot run a scheduled script.
			cli := scheme.cli(version)
			if !fileExists(filepath.Join(root, cli)) {
				cli = ""
			}

			found = append(found, Version{
				Version:    version,
				CLIPath:    cli,
				BinaryPath: binary,
				FPMService: scheme.service(version),
				PoolDir:    scheme.poolDir(version),
				ConfigPath: scheme.configPath(version),
				Installed:  true,
			})
			break
		}
	}

	sortVersions(found)
	return found
}

// Detector reports the host's PHP versions and confirms they run.
type Detector struct {
	runner *command.Runner
	root   string
}

// DetectorOptions configure a Detector.
type DetectorOptions struct {
	Runner *command.Runner
	Root   string
}

// NewDetector builds a Detector.
func NewDetector(opts DetectorOptions) *Detector {
	return &Detector{runner: opts.Runner, root: opts.Root}
}

// Available reports whether the host has any PHP at all.
func (d *Detector) Available() bool {
	return len(Probe(d.root)) > 0
}

// Detect returns every installed version, newest first.
//
// Each binary is asked for its version. One that will not execute is dropped:
// a file at the expected path that cannot run is not an installed version,
// whatever the filesystem suggests, and offering it would produce a site that
// fails at its first request instead of at selection time.
func (d *Detector) Detect(ctx context.Context) []Version {
	probed := Probe(d.root)
	confirmed := make([]Version, 0, len(probed))

	for _, version := range probed {
		full, ok := d.queryVersion(ctx, version.Version)
		if !ok {
			continue
		}
		version.Full = full
		confirmed = append(confirmed, version)
	}
	return confirmed
}

// Lookup returns one installed version.
func (d *Detector) Lookup(ctx context.Context, version string) (Version, error) {
	if err := validate.PHPVersion(version); err != nil {
		return Version{}, err
	}

	for _, candidate := range Probe(d.root) {
		if candidate.Version != version {
			continue
		}
		if full, ok := d.queryVersion(ctx, version); ok {
			candidate.Full = full
		}
		return candidate, nil
	}
	return Version{}, ErrVersionNotInstalled
}

// queryVersion runs a version's FPM binary to confirm it works.
func (d *Detector) queryVersion(ctx context.Context, version string) (string, bool) {
	if d.runner == nil {
		// Without a runner the binary cannot be confirmed. Reporting the
		// version as present would be a guess.
		return "", false
	}

	name := CommandFor(version)
	if !d.runner.Available(name) {
		return "", false
	}

	result, err := d.runner.Run(ctx, name, "-v")
	if err != nil || !result.Succeeded() {
		return "", false
	}

	match := versionOutput.FindStringSubmatch(result.Stdout)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// CommandSpecs builds allowlist entries for the versions found on the host.
//
// Called before the Runner exists, from the filesystem probe, so every PHP
// binary the Agent can ever execute is fixed at startup.
func CommandSpecs(root string) []command.Spec {
	probed := Probe(root)
	specs := make([]command.Spec, 0, len(probed))

	for _, version := range probed {
		specs = append(specs, command.Spec{
			Name: CommandFor(version.Version),
			Path: filepath.Join(root, version.BinaryPath),
		})
	}
	return specs
}

// PackageFor returns the package name for a version under a package manager.
func PackageFor(version, manager string) (string, bool) {
	if err := validate.PHPVersion(version); err != nil {
		return "", false
	}

	switch manager {
	case ManagerAPK:
		return layouts[0].pkg(version), true
	case ManagerAPT:
		return layouts[1].pkg(version), true
	default:
		return "", false
	}
}

// FormatVersions renders versions for a protocol response.
func FormatVersions(versions []Version) []map[string]any {
	out := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		out = append(out, map[string]any{
			"version":      version.Version,
			"full_version": version.Full,
			"binary_path":  version.BinaryPath,
			"cli_path":     version.CLIPath,
			"fpm_service":  version.FPMService,
			"pool_dir":     version.PoolDir,
			"config_path":  version.ConfigPath,
			"installed":    version.Installed,
		})
	}
	return out
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	// A directory named like a binary is not a binary.
	return !info.IsDir()
}

// sortVersions orders newest first.
func sortVersions(versions []Version) {
	sort.Slice(versions, func(i, j int) bool {
		return compareVersions(versions[i].Version, versions[j].Version) > 0
	})
}

// compareVersions orders two major.minor versions numerically.
//
// String comparison would sort "8.9" above "8.10", which is wrong and becomes
// visibly wrong the first time a PHP 8.10 exists.
func compareVersions(a, b string) int {
	aMajor, aMinor := splitVersion(a)
	bMajor, bMinor := splitVersion(b)

	if aMajor != bMajor {
		return aMajor - bMajor
	}
	return aMinor - bMinor
}

func splitVersion(version string) (int, int) {
	major, minor, found := strings.Cut(version, ".")
	if !found {
		return 0, 0
	}
	return atoi(major), atoi(minor)
}

func atoi(value string) int {
	total := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return total
		}
		total = total*10 + int(r-'0')
	}
	return total
}

// Extensions lists the extensions a version has loaded.
//
// The FPM binary is asked rather than a separate CLI binary: the CLI is a
// different package that a host need not have installed, and what matters is
// what the process actually serving requests has loaded, not what some other
// SAPI on the same host would load.
func (d *Detector) Extensions(ctx context.Context, version string) ([]string, error) {
	if _, err := d.Lookup(ctx, version); err != nil {
		return nil, err
	}
	if d.runner == nil {
		return nil, ErrVersionNotInstalled
	}

	name := CommandFor(version)
	if !d.runner.Available(name) {
		return nil, ErrVersionNotInstalled
	}

	result, err := d.runner.Run(ctx, name, "-m")
	if err != nil {
		return nil, fmt.Errorf("list php extensions: %w", err)
	}
	if !result.Succeeded() {
		return nil, fmt.Errorf("php-fpm %s could not list its extensions", version)
	}

	extensions := make([]string, 0, 32)
	for _, line := range strings.Split(result.Stdout, "\n") {
		entry := strings.TrimSpace(line)
		// The output carries blank lines and bracketed section headers such as
		// "[PHP Modules]", neither of which is an extension name.
		if entry == "" || strings.HasPrefix(entry, "[") {
			continue
		}
		extensions = append(extensions, entry)
	}
	sort.Strings(extensions)
	return extensions, nil
}
