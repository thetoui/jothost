package php

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Package manager identifiers.
const (
	ManagerAPK = "apk"
	ManagerAPT = "apt-get"
)

// Command names for package managers in the Agent's allowlist.
const (
	CommandAPK = "apk"
	CommandAPT = "apt-get"
)

// managerPaths are the fixed locations a package manager may live at.
//
// As everywhere else in the Agent, the path is pinned rather than resolved
// through PATH: this program is invoked as root and installs software.
var managerPaths = map[string]string{
	ManagerAPK: "/sbin/apk",
	ManagerAPT: "/usr/bin/apt-get",
}

// Errors returned by the installer.
var (
	ErrNoPackageManager = errors.New("no supported package manager was found on this host")
	ErrInstallFailed    = errors.New("the PHP package could not be installed")
)

// Installer installs and removes PHP versions through the host's package
// manager.
//
// The version is validated and then mapped to a package name by a table. It is
// never concatenated into a shell string, and no part of a request reaches a
// shell: the package manager is executed directly with an argument vector
// (CLAUDE.md section 6).
type Installer struct {
	runner *command.Runner
	// manager is the detected package manager, or "" when none was found.
	manager string
}

// NewInstaller builds an Installer over whichever package manager is present.
func NewInstaller(runner *command.Runner) *Installer {
	return &Installer{runner: runner, manager: detectManager(runner)}
}

// detectManager reports which package manager this host uses.
func detectManager(runner *command.Runner) string {
	if runner == nil {
		return ""
	}
	// apk is checked first: an Alpine host has no apt-get, and a Debian host
	// has no apk, so the order only matters on a system carrying both — where
	// preferring the one whose PHP layout is also present is what the caller
	// would want anyway.
	for _, manager := range []string{ManagerAPK, ManagerAPT} {
		if runner.Available(managerCommand(manager)) {
			return manager
		}
	}
	return ""
}

func managerCommand(manager string) string {
	switch manager {
	case ManagerAPK:
		return CommandAPK
	case ManagerAPT:
		return CommandAPT
	default:
		return ""
	}
}

// Available reports whether this host can install PHP at all.
func (i *Installer) Available() bool { return i.manager != "" }

// Manager returns the detected package manager name, or "".
func (i *Installer) Manager() string { return i.manager }

// ManagerSpecs builds allowlist entries for the package managers present.
//
// Only paths that exist are returned, so the Agent's allowlist describes what
// this host can actually do.
func ManagerSpecs() []command.Spec {
	specs := make([]command.Spec, 0, len(managerPaths))
	for manager, path := range managerPaths {
		if !fileExists(path) {
			continue
		}
		specs = append(specs, command.Spec{
			Name: managerCommand(manager),
			Path: path,
			// Installing a package downloads and unpacks it. The default
			// command timeout is far too short for that.
			Timeout: installTimeout,
		})
	}
	return specs
}

// installTimeout bounds a package operation.
const installTimeout = 10 * 60 * 1_000_000_000 // 10 minutes

// Install adds a PHP version to the host.
//
// It is idempotent: a version already installed is reported as such rather
// than reinstalled, so a retried job converges instead of doing work twice.
func (i *Installer) Install(ctx context.Context, version string, report func(int, string)) error {
	if err := validate.PHPVersion(version); err != nil {
		return err
	}
	if !i.Available() {
		return ErrNoPackageManager
	}

	pkg, ok := PackageFor(version, i.manager)
	if !ok {
		return fmt.Errorf("%w: no package name is known for PHP %s under %s",
			ErrInstallFailed, version, i.manager)
	}

	progress(report, 20, "Installing "+pkg)

	args, err := i.installArgs(pkg)
	if err != nil {
		return err
	}

	result, err := i.runner.Run(ctx, managerCommand(i.manager), args...)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInstallFailed, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInstallFailed,
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}

	progress(report, 90, "Verifying the installation")
	return nil
}

// Remove takes a PHP version off the host.
func (i *Installer) Remove(ctx context.Context, version string, report func(int, string)) error {
	if err := validate.PHPVersion(version); err != nil {
		return err
	}
	if !i.Available() {
		return ErrNoPackageManager
	}

	pkg, ok := PackageFor(version, i.manager)
	if !ok {
		return fmt.Errorf("%w: no package name is known for PHP %s", ErrInstallFailed, version)
	}

	progress(report, 20, "Removing "+pkg)

	args, err := i.removeArgs(pkg)
	if err != nil {
		return err
	}

	result, err := i.runner.Run(ctx, managerCommand(i.manager), args...)
	if err != nil {
		return fmt.Errorf("remove %s: %w", pkg, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("could not remove %s: %s", pkg,
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// InstallPackage installs a named package through the host's package manager.
//
// It exists so another part of the Agent — the phpMyAdmin installer — can use
// the package-manager detection and the allowlisted runner already assembled
// here, rather than duplicating both.
//
// The package name must come from a fixed table in the calling package. It is
// still never interpreted by a shell: like every other execution in the Agent,
// the manager is run with an argument vector.
func (i *Installer) InstallPackage(ctx context.Context, pkg string, report func(int, string)) error {
	if !i.Available() {
		return ErrNoPackageManager
	}
	if err := validatePackageName(pkg); err != nil {
		return err
	}

	args, err := i.installArgs(pkg)
	if err != nil {
		return err
	}

	result, err := i.runner.Run(ctx, managerCommand(i.manager), args...)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInstallFailed, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInstallFailed,
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// RemovePackage takes a named package off the host.
func (i *Installer) RemovePackage(ctx context.Context, pkg string, report func(int, string)) error {
	if !i.Available() {
		return ErrNoPackageManager
	}
	if err := validatePackageName(pkg); err != nil {
		return err
	}

	args, err := i.removeArgs(pkg)
	if err != nil {
		return err
	}

	result, err := i.runner.Run(ctx, managerCommand(i.manager), args...)
	if err != nil {
		return fmt.Errorf("remove %s: %w", pkg, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("could not remove %s: %s", pkg,
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// packageNamePattern is what a package name may look like.
//
// A second line of defence: the caller is meant to pass a constant, and this
// makes a future caller that passes something else fail here rather than hand
// an option-looking string to a package manager running as root.
var packageNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._+-]{0,63}$`)

func validatePackageName(pkg string) error {
	if !packageNamePattern.MatchString(pkg) {
		return fmt.Errorf("%w: %q is not a valid package name", ErrInstallFailed, pkg)
	}
	return nil
}

// installArgs builds the argument vector for an install.
//
// The package name is the only variable, and it came from a table keyed by a
// validated version — never from the request text.
func (i *Installer) installArgs(pkg string) ([]string, error) {
	switch i.manager {
	case ManagerAPK:
		return []string{"add", "--no-cache", pkg}, nil
	case ManagerAPT:
		// -y because there is no terminal to answer a prompt, and
		// --no-install-recommends to avoid pulling a web server or a mail
		// transport agent onto a host that already has its own.
		return []string{"install", "-y", "--no-install-recommends", pkg}, nil
	default:
		return nil, ErrNoPackageManager
	}
}

func (i *Installer) removeArgs(pkg string) ([]string, error) {
	switch i.manager {
	case ManagerAPK:
		return []string{"del", pkg}, nil
	case ManagerAPT:
		return []string{"remove", "-y", pkg}, nil
	default:
		return nil, ErrNoPackageManager
	}
}

// progress reports a step if the caller supplied a reporter.
func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}

// StartFPM launches a version's FPM daemon if it is not already running.
//
// In production systemd owns this, and the Agent asks it through the services
// provider. In a container there is no init, so the Agent starts the daemon
// itself — otherwise a pool would be written that nothing ever reads.
func (i *Installer) StartFPM(ctx context.Context, version string, detector *Detector) error {
	if detector == nil {
		return ErrPoolUnavailable
	}
	if _, err := detector.Lookup(ctx, version); err != nil {
		return err
	}

	name := CommandFor(version)
	if !i.runner.Available(name) {
		return ErrVersionNotInstalled
	}

	if err := os.MkdirAll(pidDir, 0o755); err != nil {
		return fmt.Errorf("create pid directory: %w", err)
	}

	// The pid file path is passed explicitly rather than relying on the
	// distribution's default, which ships commented out. Without a pid file
	// there is no reliable way to reload the master later.
	//
	// FPM daemonises on its own, so this returns once the master has forked.
	result, err := i.runner.Run(ctx, name, "--daemonize", "--pid", fpmPIDPath(version))
	if err != nil {
		return fmt.Errorf("start php-fpm %s: %w", version, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("php-fpm %s did not start: %s", version,
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// ReloadFPM asks a running FPM to re-read its pools.
//
// A reload is what makes a written pool live. Without it the file is correct
// on disk and the site still returns 502, which is the confusing failure this
// exists to prevent.
func (i *Installer) ReloadFPM(ctx context.Context, version string) error {
	pid, err := fpmPID(version)
	if err != nil {
		return err
	}

	// USR2 is FPM's graceful reload: it re-reads configuration and restarts
	// workers without dropping the listening socket, so requests in flight are
	// not lost.
	process, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find php-fpm %s: %w", version, err)
	}
	if err := process.Signal(reloadSignal); err != nil {
		return fmt.Errorf("reload php-fpm %s: %w", version, err)
	}
	return nil
}

// pidDir holds the pid files of FPM masters the Agent started.
const pidDir = "/run/php-fpm"

// fpmPIDPath returns where a version's FPM master records its pid.
func fpmPIDPath(version string) string {
	return pidDir + "/php-fpm" + validate.PHPVersionCompact(version) + ".pid"
}

// fpmPID reads a running FPM master's process id.
func fpmPID(version string) (int, error) {
	if err := validate.PHPVersion(version); err != nil {
		return 0, err
	}

	content, err := os.ReadFile(fpmPIDPath(version))
	if err != nil {
		if os.IsNotExist(err) {
			return 0, fmt.Errorf("%w: php-fpm %s is not running", ErrPoolUnavailable, version)
		}
		return 0, fmt.Errorf("read php-fpm pid: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("php-fpm %s wrote an unreadable pid file", version)
	}
	return pid, nil
}

// FPMRunning reports whether a version's FPM master is actually up.
//
// A pid file outlives the process that wrote it, and signal 0 is not enough to
// tell the difference. The Agent runs as PID 1 in a container and does not reap
// its children, so an FPM master that exits becomes a zombie — and a zombie
// still accepts signal 0. Treating one as running means the next pool change
// takes the reload path, sends SIGUSR2 to a corpse, and waits for a socket that
// will never appear.
//
// The process name is checked too, which closes the same hole from the other
// side: a pid file can outlive its process long enough for the id to be reused
// by something entirely unrelated.
func FPMRunning(version string) bool {
	pid, err := fpmPID(version)
	if err != nil {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if process.Signal(syscall.Signal(0)) != nil {
		return false
	}
	return isLiveFPM(pid)
}

// isLiveFPM reports whether a pid is a running (non-zombie) php-fpm process.
//
// It reads /proc directly rather than shelling out: this runs on every pool
// change, and the answer decides whether a site gets a working socket.
func isLiveFPM(pid int) bool {
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		// Without /proc there is nothing better to go on than the signal check
		// that already succeeded.
		if os.IsNotExist(err) {
			return false
		}
		return true
	}

	name, state := "", ""
	for _, line := range strings.Split(string(status), "\n") {
		field, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		switch field {
		case "Name":
			name = strings.TrimSpace(value)
		case "State":
			state = strings.TrimSpace(value)
		}
	}

	// "Z (zombie)" — the process has exited and is only waiting to be reaped.
	if strings.HasPrefix(state, "Z") {
		return false
	}
	return strings.Contains(name, "php-fpm")
}
