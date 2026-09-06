// Package grafana installs and provisions Grafana for the panel's metrics.
//
// The arrangement, and the reasoning behind each part, because "embed Grafana"
// can mean several very different things:
//
//   - Grafana reads the panel's own PostgreSQL. The collector has been writing
//     system_metrics since Phase 4 and the alert engine reads the same table;
//     adding Prometheus would mean a second copy of the same numbers, two
//     retention policies, and two answers to "what was the CPU at nine o'clock".
//   - The alert engine stays where it is. Grafana draws; Phase 19 decides. A
//     panel whose alerts came from Grafana would have its notification routing,
//     its thresholds and its acknowledgement state living in a tool the panel
//     does not own.
//   - Grafana authenticates its own visitors, exactly as phpMyAdmin does. The
//     panel serves it on one server_name an operator names and does not turn on
//     anonymous access. An embedded panel therefore shows a chart to somebody
//     with a Grafana session and a login prompt to everybody else, which is the
//     correct behaviour rather than a limitation.
//   - The datasource is read-only. Grafana connects as its own PostgreSQL role
//     with SELECT on one table, so a dashboard query — or somebody with
//     Grafana's editor rights — cannot write to the panel's database.
package grafana

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Errors returned by this package.
var (
	// ErrUnsupported means the host has no package manager the panel drives.
	ErrUnsupported = errors.New("this host cannot install Grafana")
	// ErrNotInstalled means Grafana is not on the host.
	ErrNotInstalled = errors.New("Grafana is not installed")
)

// DaemonName is what the service layer knows Grafana by.
const DaemonName = "grafana"

// DefaultPort is where Grafana listens. Loopback only: it is reached through
// the vhost the panel writes, not directly.
const DefaultPort = 3000

// Status is what the panel knows about Grafana on this host.
type Status struct {
	Installed bool `json:"installed"`
	Running   bool `json:"running"`
	// Provisioned reports whether the panel's datasource and dashboard are in
	// place. Installed and unprovisioned is a Grafana somebody else set up.
	Provisioned bool `json:"provisioned"`
	// CanInstall reports whether the panel could install it.
	CanInstall bool `json:"can_install"`
	// ServerName is the hostname Grafana is served on, empty until one is set.
	ServerName string `json:"server_name,omitempty"`
	// EmbedURL is where the panel's iframes point, empty until it is served.
	EmbedURL string `json:"embed_url,omitempty"`
	Version  string `json:"version,omitempty"`
	// Detail explains anything above that is false.
	Detail string `json:"detail,omitempty"`
}

// PackageInstaller installs a package. The same interface the mail and DNS
// installs use, for the same reason: exactly one thing on this host installs
// packages and it is not this package.
type PackageInstaller interface {
	Available() bool
	InstallPackage(ctx context.Context, name string, report func(int, string)) error
}

// Services controls the daemon.
type Services interface {
	Restart(ctx context.Context, name string) error
	Running(ctx context.Context, name string) bool
	Enable(ctx context.Context, name string) error
}

// Options configure a Manager.
type Options struct {
	Installer PackageInstaller
	Services  Services
	Log       *slog.Logger
	// ConfigDir overrides the location, for tests.
	ConfigDir string
}

// Manager installs and provisions Grafana.
type Manager struct {
	installer PackageInstaller
	services  Services
	log       *slog.Logger
	configDir string
}

// NewManager builds a Manager.
func NewManager(opts Options) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	// Empty means "discover it". Only a test sets this, and it sets it to a
	// temporary directory: assuming a path on a real host is exactly the
	// mistake paths.go exists to record.
	return &Manager{
		installer: opts.Installer,
		services:  opts.Services,
		log:       log,
		configDir: opts.ConfigDir,
	}
}

// SetServices supplies the daemon controller after construction.
//
// The same shape the mail provider uses, and for the same reason: resolving a
// catalogue key to a unit belongs to the operations registry, which does not
// exist yet when this manager is built.
func (m *Manager) SetServices(s Services) { m.services = s }

// binaries are where the distributions put the server.
var binaries = []string{
	"/usr/sbin/grafana-server",
	"/usr/bin/grafana-server",
	"/usr/share/grafana/bin/grafana-server",
	"/usr/bin/grafana",
}

// Status reports what is on the host.
func (m *Manager) Status(ctx context.Context) Status {
	status := Status{CanInstall: m.installer != nil && m.installer.Available()}

	for _, path := range binaries {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			status.Installed = true
			break
		}
	}
	if !status.Installed {
		if _, err := os.Stat(m.configDir); err == nil {
			// The configuration without the binary is a package removed and
			// not purged. Reported as not installed, because it is.
			status.Detail = "Grafana's configuration is here but its server is not"
		} else {
			status.Detail = "Grafana is not installed on this host"
		}
		return status
	}

	status.Provisioned = m.provisioned()
	if !status.Provisioned {
		status.Detail = "Grafana is installed but the panel has not provisioned its " +
			"datasource, so its dashboards would have nothing to read"
	}

	if m.services != nil {
		status.Running = m.services.Running(ctx, DaemonName)
		if status.Installed && !status.Running {
			status.Detail = "Grafana is installed and not running, so an embedded " +
				"panel will not load"
		}
	}
	return status
}

// provisioned reports whether the panel's own files are in place, where this
// host's Grafana would look for them.
func (m *Manager) provisioned() bool {
	layout, err := m.layout()
	if err != nil {
		return false
	}
	for _, path := range []string{
		filepath.Join(layout.ProvisioningDir, "datasources", "jothost.yaml"),
		filepath.Join(layout.ProvisioningDir, "dashboards", "jothost.yaml"),
		filepath.Join(layout.DashboardDir, "jothost-host.json"),
	} {
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	// The settings block has to be in the file Grafana reads, not beside it.
	content, err := os.ReadFile(layout.ConfigFile)
	if err != nil {
		return false
	}
	return strings.Contains(string(content), blockStart)
}

// Provision writes the datasource, the dashboard and the embedding settings.
//
// It is idempotent and rewrites all three every time: they are the panel's
// files, they carry a header saying so, and reconciling them on every run is
// what keeps a hand-edited copy from surviving unnoticed.
//
// Everything is written where this host's Grafana actually reads from, which is
// discovered rather than assumed — see paths.go for what that cost the first
// time round.
func (m *Manager) Provision(ctx context.Context, cfg DatasourceConfig, report func(int, string)) error {
	progress := func(percent int, message string) {
		if report != nil {
			report(percent, message)
		}
	}

	if err := cfg.validate(); err != nil {
		return err
	}

	layout, err := m.layout()
	if err != nil {
		return err
	}

	progress(30, "Writing Grafana's datasource and dashboard")

	files := []struct {
		path    string
		content []byte
		mode    os.FileMode
	}{
		// The datasource holds a database password, so it is the one file here
		// that is not world-readable. Grafana runs as its own account and
		// reads it as that account.
		{filepath.Join(layout.ProvisioningDir, "datasources", "jothost.yaml"),
			renderDatasource(cfg), 0o640},
		{filepath.Join(layout.ProvisioningDir, "dashboards", "jothost.yaml"),
			renderDashboardProvider(layout.DashboardDir), 0o644},
		{filepath.Join(layout.DashboardDir, "jothost-host.json"),
			renderDashboard(), 0o644},
	}

	for _, file := range files {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o755); err != nil {
			return fmt.Errorf("create %s: %w", filepath.Dir(file.path), err)
		}
		if err := os.WriteFile(file.path, file.content, file.mode); err != nil {
			return fmt.Errorf("write %s: %w", file.path, err)
		}
	}

	// Grafana reads its provisioning as its own account, and the panel writes
	// as root. A datasource it cannot open is a datasource it does not have.
	if err := m.ownProvisioning(ctx, layout); err != nil {
		m.log.Warn("Grafana's provisioning files could not be given to its account",
			"error", err.Error())
	}

	progress(55, "Applying Grafana's settings")
	existing, err := os.ReadFile(layout.ConfigFile)
	if err != nil {
		return fmt.Errorf("read %s: %w", layout.ConfigFile, err)
	}
	merged := mergeSettings(existing, renderSettings(cfg.RootURL))
	if err := os.WriteFile(layout.ConfigFile, merged, 0o640); err != nil {
		return fmt.Errorf("write %s: %w", layout.ConfigFile, err)
	}

	if m.services == nil {
		return nil
	}

	progress(80, "Restarting Grafana")
	// Restart rather than reload: Grafana reads provisioning at startup and
	// has no signal that makes it read it again.
	if err := m.services.Restart(ctx, DaemonName); err != nil {
		return fmt.Errorf("restart Grafana: %w", err)
	}
	// Whatever is running should be running after a reboot. The Agent's own
	// sweep would catch this at its next start; doing it here means an
	// operator who installs Grafana and reboots an hour later still has it.
	if err := m.services.Enable(ctx, DaemonName); err != nil {
		m.log.Warn("Grafana was started but not set to start at boot",
			"error", err.Error())
	}
	return nil
}

// layout returns the discovered paths, or the override a test supplied.
func (m *Manager) layout() (Layout, error) {
	if m.configDir != "" {
		// A test points everything at one temporary directory. The shape is
		// the same; only the discovery is skipped.
		return Layout{
			ConfigFile:      filepath.Join(m.configDir, "grafana.ini"),
			ProvisioningDir: filepath.Join(m.configDir, "provisioning"),
			DashboardDir:    filepath.Join(m.configDir, "provisioning", "jothost-dashboards"),
			Source:          "an explicit configuration directory",
		}, nil
	}
	return DiscoverLayout()
}

// ownProvisioning hands the files to the account Grafana runs as.
func (m *Manager) ownProvisioning(ctx context.Context, layout Layout) error {
	account, err := user.Lookup("grafana")
	if err != nil {
		// No such account is not a failure on a host where Grafana runs as
		// something else; the files stay root-owned and world-readable where
		// they are meant to be.
		return nil
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}

	return filepath.WalkDir(layout.ProvisioningDir, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Lchown, following no link: this walks a directory tree as root.
		return os.Lchown(path, uid, gid)
	})
}

// Install puts Grafana on the host.
func (m *Manager) Install(ctx context.Context, report func(int, string)) error {
	if m.installer == nil || !m.installer.Available() {
		return ErrUnsupported
	}
	if report != nil {
		report(10, "Installing Grafana")
	}
	return m.installer.InstallPackage(ctx, "grafana", report)
}

// DatasourceConfig is what Grafana needs to read the panel's metrics.
type DatasourceConfig struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	// SSLMode is passed through to Grafana. "disable" is right for a database
	// on the same host and wrong for one anywhere else, which is why it is a
	// field rather than a constant.
	SSLMode string
	// RootURL is where Grafana is reached, which it needs to build the links
	// inside an embedded panel correctly.
	RootURL string
}

func (c DatasourceConfig) validate() error {
	switch {
	case strings.TrimSpace(c.Host) == "":
		return fmt.Errorf("the datasource needs a database host")
	case strings.TrimSpace(c.Database) == "":
		return fmt.Errorf("the datasource needs a database name")
	case strings.TrimSpace(c.User) == "":
		return fmt.Errorf("the datasource needs a database user")
	}
	// A password is not required: a host using peer authentication has none,
	// and demanding one would refuse a working configuration.
	return nil
}
