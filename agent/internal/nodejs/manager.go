package nodejs

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/sites"
)

// Manager runs Node.js applications, through systemd where the host has it and
// through the Agent's own supervisor where it does not.
//
// Callers never choose. Which mechanism a host uses is a property of the host,
// and every operation answers the same way either way — so nothing above this
// package has to branch on it, and a panel moving between a real machine and a
// container behaves the same.
type Manager struct {
	detector   *Detector
	installer  *Installer
	systemd    *Systemd
	supervisor *Supervisor
	runner     *command.Runner
	users      *sites.UserProvider
	log        *slog.Logger
}

// ManagerOptions configure a Manager.
type ManagerOptions struct {
	Detector   *Detector
	Installer  *Installer
	Systemd    *Systemd
	Supervisor *Supervisor
	Runner     *command.Runner
	Users      *sites.UserProvider
	Log        *slog.Logger
}

// NewManager builds a Manager.
func NewManager(opts ManagerOptions) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		detector:   opts.Detector,
		installer:  opts.Installer,
		systemd:    opts.Systemd,
		supervisor: opts.Supervisor,
		runner:     opts.Runner,
		users:      opts.Users,
		log:        log,
	}
}

// Available reports whether this host can run Node.js applications.
func (m *Manager) Available() bool {
	return m.detector != nil && m.detector.Available()
}

// Runtime names the mechanism this host uses.
func (m *Manager) Runtime() string {
	if m.systemd != nil && m.systemd.Available() {
		return ManagedBySystemd
	}
	return ManagedBySupervisor
}

// usingSystemd reports which path an operation should take.
func (m *Manager) usingSystemd() bool {
	return m.systemd != nil && m.systemd.Available()
}

// Versions reports the runtimes installed, and what could be installed.
func (m *Manager) Versions(ctx context.Context) map[string]any {
	installed := []Version{}
	if m.detector != nil {
		installed = m.detector.Detect(ctx)
	}

	offers := []Offer{}
	canInstall := false
	if m.installer != nil {
		offers = m.installer.Offers()
		canInstall = m.installer.Available()
	}

	return map[string]any{
		"versions":        FormatVersions(installed),
		"count":           len(installed),
		"available":       len(installed) > 0,
		"offers":          offers,
		"can_install":     canInstall,
		"package_manager": m.installerManager(),
		"managed_by":      m.Runtime(),
	}
}

func (m *Manager) installerManager() string {
	if m.installer == nil {
		return ""
	}
	return m.installer.Manager()
}

// Install adds a Node.js release line to the host.
func (m *Manager) Install(ctx context.Context, pkg string, report func(int, string)) (Version, error) {
	if m.installer == nil {
		return Version{}, fmt.Errorf("%w: no package manager", ErrNoPackageManager)
	}
	return m.installer.Install(ctx, pkg, report)
}

// Uninstall removes a Node.js release line.
//
// It does not check whether any application is using it. That check belongs to
// the panel, which is the only thing that knows what applications exist; the
// Agent's job is to do what it was told, and refusing here would need it to
// keep a registry of its own that could disagree.
func (m *Manager) Uninstall(ctx context.Context, pkg string, report func(int, string)) error {
	if m.installer == nil {
		return fmt.Errorf("%w: no package manager", ErrNoPackageManager)
	}
	return m.installer.Remove(ctx, pkg, report)
}

// Deploy makes an application ready to run: its state, its environment file,
// and — on a systemd host — its unit.
//
// It does not start it. Creating and starting are separate because a
// deployment that is not ready to serve should be created, looked at, and
// started deliberately.
func (m *Manager) Deploy(ctx context.Context, app App) error {
	if !m.Available() {
		return fmt.Errorf("%w: no Node.js runtime is installed", ErrUnsupported)
	}
	if err := app.Validate(); err != nil {
		return err
	}

	version, err := m.detector.Lookup(ctx, app.Version)
	if err != nil {
		return err
	}

	account, found, err := m.users.Lookup(app.User)
	if err != nil {
		return fmt.Errorf("look up %s: %w", app.User, err)
	}
	if !found {
		return fmt.Errorf("%w: the account %s does not exist", ErrUnsupported, app.User)
	}

	if err := os.MkdirAll(filepath.Dir(app.LogDir()), 0o755); err != nil {
		return fmt.Errorf("create the Node log directory: %w", err)
	}
	if err := os.MkdirAll(app.LogDir(), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", app.LogDir(), err)
	}
	if err := os.Chown(app.LogDir(), account.UID, account.GID); err != nil {
		return fmt.Errorf("own %s: %w", app.LogDir(), err)
	}
	if err := WriteEnvFile(app, account.UID, account.GID); err != nil {
		return err
	}

	if m.usingSystemd() {
		if err := m.systemd.Install(ctx, app, version.BinaryPath); err != nil {
			return err
		}
		// Enabled at deploy: an application that does not come back after a
		// reboot is one an operator finds out about from a customer.
		if err := m.systemd.services.Enable(ctx, app.UnitName()); err != nil {
			m.log.Warn("could not enable the unit at boot", "app", app.Name, "error", err.Error())
		}
	}
	return nil
}

// Start runs an application.
func (m *Manager) Start(ctx context.Context, app App) (Status, error) {
	if !m.Available() {
		return Status{}, fmt.Errorf("%w: no Node.js runtime is installed", ErrUnsupported)
	}

	if m.usingSystemd() {
		if err := m.systemd.Start(ctx, app); err != nil {
			return Status{}, err
		}
		return m.systemd.Status(ctx, app), nil
	}

	version, err := m.detector.Lookup(ctx, app.Version)
	if err != nil {
		return Status{}, err
	}
	return m.supervisor.Start(ctx, app, version.BinaryPath)
}

// Stop ends an application.
func (m *Manager) Stop(ctx context.Context, app App) (Status, error) {
	if m.usingSystemd() {
		if err := m.systemd.Stop(ctx, app); err != nil {
			return Status{}, err
		}
		return m.systemd.Status(ctx, app), nil
	}

	if err := m.supervisor.Stop(app); err != nil {
		return Status{}, err
	}
	return m.supervisor.Status(app), nil
}

// Restart stops and starts an application.
//
// On systemd this is one operation, which is what makes it atomic there. The
// supervisor does it in two, with the stop's grace period in between, because
// starting before the old process has released the port would fail on the
// thing that matters most.
func (m *Manager) Restart(ctx context.Context, app App) (Status, error) {
	if m.usingSystemd() {
		if err := m.systemd.Restart(ctx, app); err != nil {
			return Status{}, err
		}
		return m.systemd.Status(ctx, app), nil
	}

	if err := m.supervisor.Stop(app); err != nil {
		return Status{}, err
	}
	return m.Start(ctx, app)
}

// Status reports what an application is doing.
func (m *Manager) Status(ctx context.Context, app App) Status {
	if m.usingSystemd() {
		return m.systemd.Status(ctx, app)
	}
	return m.supervisor.Status(app)
}

// Remove takes an application's runtime state off the host.
//
// It stops the process and removes what the panel wrote — the unit, the
// environment file, the pid file. The application's own directory is left
// alone: that is the customer's code, and deleting it is the website's
// business, not the process manager's.
func (m *Manager) Remove(ctx context.Context, app App) error {
	if _, err := m.Stop(ctx, app); err != nil {
		m.log.Warn("could not stop an application before removing it",
			"app", app.Name, "error", err.Error())
	}

	if m.usingSystemd() {
		if err := m.systemd.services.Disable(ctx, app.UnitName()); err != nil {
			m.log.Warn("could not disable the unit", "app", app.Name, "error", err.Error())
		}
		if err := m.systemd.Remove(ctx, app); err != nil {
			return err
		}
	}
	return RemoveState(app)
}

// InstallDependencies runs npm install in the application's directory.
//
// As the application's own account, so what it writes into node_modules is
// owned by the account that will read it — running this as root is how a
// deployment ends up with a node_modules the application cannot update.
func (m *Manager) InstallDependencies(ctx context.Context, app App, report func(int, string)) error {
	if m.runner == nil || !m.runner.Available(CommandNPM) {
		return fmt.Errorf("%w: npm is not installed", ErrUnsupported)
	}
	if err := app.Validate(); err != nil {
		return err
	}

	if _, err := os.Stat(filepath.Join(app.Root, "package.json")); err != nil {
		return fmt.Errorf("%w: there is no package.json in %s", ErrStartupMissing, app.Root)
	}

	account, found, err := m.users.Lookup(app.User)
	if err != nil {
		return fmt.Errorf("look up %s: %w", app.User, err)
	}
	if !found {
		return fmt.Errorf("%w: the account %s does not exist", ErrUnsupported, app.User)
	}

	progress(report, 20, "Installing dependencies")

	// --omit=dev because this is a production deployment, and --no-audit and
	// --no-fund because both make network calls whose only output is advice.
	//
	// npm's cache defaults to $HOME/.npm, and HOME here is the application's
	// own directory, so nothing is written outside it.
	result, err := m.runner.RunWith(ctx, CommandNPM, command.Options{
		Dir:           app.Root,
		Env:           map[string]string{"HOME": app.Root, "USER": app.User},
		UID:           account.UID,
		GID:           account.GID,
		SetCredential: true,
	}, "install", "--omit=dev", "--no-audit", "--no-fund")
	if err != nil {
		return fmt.Errorf("npm install: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("npm install failed: %s", lastLines(result.Stderr, 3))
	}

	progress(report, 100, "Dependencies installed")
	return nil
}

// Logs returns the tail of an application's output.
//
// Under the Agent's supervisor the output is in files it opened. Under systemd
// it is in the journal, and reading that needs journalctl — which this panel
// does not allowlist yet, so the honest answer there is to say where the logs
// are rather than return nothing and imply there are none.
func (m *Manager) Logs(app App, lines int) (map[string]any, error) {
	if lines <= 0 || lines > MaxLogLines {
		lines = DefaultLogLines
	}

	if m.usingSystemd() {
		return map[string]any{
			"source": "journal",
			"lines":  []string{},
			"detail": "this host runs the application under systemd; its output is in the " +
				"journal, readable with: journalctl -u " + app.UnitName(),
		}, nil
	}

	out := tailFile(app.OutLog(), lines)
	errLines := tailFile(app.ErrLog(), lines)

	return map[string]any{
		"source":      "files",
		"lines":       out,
		"error_lines": errLines,
		"out_path":    app.OutLog(),
		"error_path":  app.ErrLog(),
	}, nil
}

// Log line bounds. The cap exists because the response crosses a socket with a
// 1 MiB limit.
const (
	DefaultLogLines = 200
	MaxLogLines     = 2000
)

// tailFile returns the last n lines of a file.
func tailFile(path string, n int) []string {
	content, err := os.ReadFile(path)
	if err != nil {
		return []string{}
	}

	// Bounded read of the tail rather than the whole file: an application that
	// has been logging for a month should not be loaded into memory to show
	// its last twenty lines.
	if len(content) > maxLogBytes {
		content = content[len(content)-maxLogBytes:]
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	if len(lines) == 1 && lines[0] == "" {
		return []string{}
	}
	return lines
}

// maxLogBytes bounds how much of a log file is read to find its tail.
const maxLogBytes = 512 * 1024

// lastLines returns the final n non-empty lines of a command's output.
func lastLines(value string, n int) string {
	lines := []string{}
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if strings.TrimSpace(line) != "" {
			lines = append(lines, strings.TrimSpace(line))
		}
	}
	if len(lines) == 0 {
		return "npm gave no reason"
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "; ")
}
