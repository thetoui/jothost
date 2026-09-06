// Package pma installs and serves phpMyAdmin on the managed host.
//
// phpMyAdmin is a third-party PHP application, and a well-known target: it is
// one of the most probed paths on the public internet. Everything here is
// shaped by that.
//
//   - It is installed from the host's own package manager, never downloaded.
//     The package name is a constant in this file; nothing from a request
//     reaches the installer.
//   - It is never enabled by default. An operator asks for it.
//   - Its configuration holds no credentials. Authentication is phpMyAdmin's
//     cookie mode, so a visitor logs in with a database account the panel
//     created — reaching it proves nothing on its own.
//   - It runs under its own system account and its own FPM pool, like every
//     website the panel provisions. It is not run as root, and not as an
//     account shared with anything else.
//   - It is served on one server_name the operator names, not on every site.
package pma

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Package names, by package manager. This table is the only place a package
// name comes from.
var packageNames = map[string]string{
	php.ManagerAPK: "phpmyadmin",
	php.ManagerAPT: "phpmyadmin",
}

// webroots are where the distributions put phpMyAdmin. The first that exists
// after installation is the one served.
var webroots = []string{
	"/usr/share/webapps/phpmyadmin", // Alpine
	"/usr/share/phpmyadmin",         // Debian and Ubuntu
	"/usr/share/phpMyAdmin",         // RHEL family
}

// Fixed locations the panel owns.
const (
	// ConfigName is the file phpMyAdmin reads its settings from. It goes in
	// the webroot, which is where phpMyAdmin looks and where every
	// distribution's packaging points: putting it elsewhere would mean adding
	// that directory to open_basedir for no gain.
	ConfigName = "config.inc.php"
	// StateDir is everything about phpMyAdmin that the panel owns rather than
	// the package manager.
	StateDir = "/var/lib/jothost/phpmyadmin"
	// TempDir is phpMyAdmin's scratch space. It is outside the webroot, so
	// nothing written there can ever be served.
	TempDir = "/var/lib/jothost/phpmyadmin/tmp"
	// SessionDir holds PHP sessions for this pool, away from every site's.
	SessionDir = "/var/lib/jothost/phpmyadmin/sessions"

	// Account is the system user the application runs as.
	Account = "jothost_pma"
	// PoolName names its FPM pool. The socket it listens on carries the PHP
	// version, like every other pool's, so switching version leaves no stale
	// socket for nginx to keep passing requests to.
	PoolName = "jothost-pma"

	// LogDir holds its access and error logs.
	LogDir = "/var/log/jothost/phpmyadmin"
)

// Errors returned by the manager.
var (
	// ErrUnsupported means this host cannot run phpMyAdmin at all.
	ErrUnsupported = errors.New("phpMyAdmin cannot be managed on this host")
	// ErrNotInstalled means it has not been installed yet.
	ErrNotInstalled = errors.New("phpMyAdmin is not installed")
	// ErrNoPHP means no PHP version is available to run it.
	ErrNoPHP = errors.New("phpMyAdmin needs PHP, and no version is installed")
	// ErrInvalidServerName means the requested host name is not usable.
	ErrInvalidServerName = errors.New("invalid server name for phpMyAdmin")
)

// MinPHPVersion is the oldest PHP phpMyAdmin 5 will run on.
const MinPHPVersion = "7.2"

// PackageInstaller installs a named package through the host's package manager.
//
// A narrow interface rather than a concrete type, so this package does not have
// to repeat the package-manager detection the PHP installer already does. The
// package name always comes from packageNames above.
type PackageInstaller interface {
	Available() bool
	Manager() string
	InstallPackage(ctx context.Context, pkg string, report func(int, string)) error
	RemovePackage(ctx context.Context, pkg string, report func(int, string)) error
}

// FPMControl starts and reloads PHP-FPM.
//
// Writing a pool file does nothing on its own: FPM has to be told to read it,
// and the socket has to exist before nginx is pointed at it. Skipping either
// produces a site that returns 502 with a configuration that looks correct.
// BootPersister makes a service start again after a reboot.
//
// Narrow on purpose: this package has no business stopping or restarting
// anything, and the one thing it needs is the one thing here.
type BootPersister interface {
	Enable(ctx context.Context, name string) error
}

type FPMControl interface {
	StartFPM(ctx context.Context, version string, detector *php.Detector) error
	ReloadFPM(ctx context.Context, version string) error
}

// Options configure a Manager.
type Options struct {
	Installer PackageInstaller
	FPM       FPMControl
	// Boot makes the PHP-FPM unit start at boot. Optional: a host with no init
	// system has nothing to ask.
	Boot  BootPersister
	PHP   *php.Detector
	Pools *php.Provider
	Nginx *nginx.Provider
	Users *sites.UserProvider
	// WebGroup owns the FPM socket so nginx can open it. Without it every
	// request returns 502 with nothing obviously wrong.
	WebGroup string
	// ServerName is the host phpMyAdmin answers on. Empty means the operator
	// has not chosen one, and installation asks for it.
	ServerName string
	Log        *slog.Logger
}

// Manager installs, configures, and serves phpMyAdmin.
type Manager struct {
	installer PackageInstaller
	fpm       FPMControl
	// boot makes the PHP-FPM unit start again after a reboot. Optional: a host
	// with no init system has nothing to ask.
	boot     BootPersister
	php      *php.Detector
	pools    *php.Provider
	nginx    *nginx.Provider
	users    *sites.UserProvider
	webGroup string
	log      *slog.Logger
}

// NewManager builds a Manager.
func NewManager(opts Options) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Manager{
		installer: opts.Installer,
		fpm:       opts.FPM,
		boot:      opts.Boot,
		php:       opts.PHP,
		pools:     opts.Pools,
		nginx:     opts.Nginx,
		users:     opts.Users,
		webGroup:  opts.WebGroup,
		log:       log,
	}
}

// Status describes phpMyAdmin as it is on this host.
type Status struct {
	// Installed is true once the package's files are present.
	Installed bool `json:"installed"`
	// Served is true once the panel has written its vhost, which is what
	// actually makes it reachable.
	Served     bool   `json:"served"`
	Webroot    string `json:"webroot,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	URL        string `json:"url,omitempty"`
	PHPVersion string `json:"php_version,omitempty"`
	// CanInstall reports whether this host has what installation needs.
	CanInstall bool `json:"can_install"`
	// Detail explains why it cannot be installed, when it cannot.
	Detail string `json:"detail,omitempty"`
}

// Status reports what is on the host.
func (m *Manager) Status(ctx context.Context) Status {
	status := Status{}

	root, found := findWebroot()
	status.Installed = found
	status.Webroot = root

	if m.nginx != nil {
		if name, err := m.servedName(); err == nil && name != "" {
			status.Served = true
			status.ServerName = name
			status.URL = "http://" + name + "/"
		}
	}

	if version, err := m.pickPHP(ctx); err == nil {
		status.PHPVersion = version
	}

	status.CanInstall, status.Detail = m.readiness(ctx)
	return status
}

// readiness reports whether installation could succeed, and why not.
//
// Answering before anything is attempted lets the panel disable a control
// rather than offer one that fails halfway and leaves the host part-configured.
func (m *Manager) readiness(ctx context.Context) (bool, string) {
	switch {
	case m.installer == nil || !m.installer.Available():
		return false, "this host has no supported package manager, so phpMyAdmin cannot be installed"
	case m.nginx == nil || !m.nginx.Available():
		return false, "nginx was not found, so phpMyAdmin could not be served"
	case m.pools == nil || !m.pools.Available():
		return false, "no PHP-FPM was found, so phpMyAdmin could not run"
	case m.users == nil || !m.users.Available():
		return false, "no user management tool was found, so phpMyAdmin has no account to run as"
	case m.webGroup == "":
		return false, "the web server's group is unknown, so nginx could not reach phpMyAdmin"
	}
	if _, err := m.pickPHP(ctx); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// pickPHP chooses the PHP version to run phpMyAdmin under.
//
// The newest installed version at or above the minimum. Newest rather than
// oldest because the alternative is running a security-sensitive application
// on the version most likely to be near end of life.
func (m *Manager) pickPHP(ctx context.Context) (string, error) {
	if m.php == nil {
		return "", ErrNoPHP
	}

	best := ""
	for _, version := range m.php.Detect(ctx) {
		if compareVersions(version.Version, MinPHPVersion) < 0 {
			continue
		}
		if best == "" || compareVersions(version.Version, best) > 0 {
			best = version.Version
		}
	}
	if best == "" {
		return "", fmt.Errorf("%w: PHP %s or newer is required", ErrNoPHP, MinPHPVersion)
	}
	return best, nil
}

// Install puts phpMyAdmin on the host and serves it.
//
// Every step is idempotent, so a retried installation converges rather than
// failing on what the previous attempt already did (CLAUDE.md section 17).
func (m *Manager) Install(ctx context.Context, serverName string, report func(int, string)) (Status, error) {
	serverName = validate.NormalizeDomain(serverName)
	if err := validate.Domain(serverName); err != nil {
		return Status{}, fmt.Errorf("%w: %v", ErrInvalidServerName, err)
	}

	if ready, detail := m.readiness(ctx); !ready {
		return Status{}, fmt.Errorf("%w: %s", ErrUnsupported, detail)
	}

	version, err := m.pickPHP(ctx)
	if err != nil {
		return Status{}, err
	}

	pkg, ok := packageNames[m.installer.Manager()]
	if !ok {
		return Status{}, fmt.Errorf("%w: no phpMyAdmin package is known for %s",
			ErrUnsupported, m.installer.Manager())
	}

	progress(report, 10, "Installing "+pkg)
	if err := m.installer.InstallPackage(ctx, pkg, report); err != nil {
		return Status{}, err
	}

	// The package alone is not enough. phpMyAdmin will not start without
	// mysqli, and installing it without its extensions produces a page whose
	// only content is "the mysqli extension is missing" — which is what this
	// did before these were added.
	extensions, err := extensionPackages(m.installer.Manager(), version)
	if err != nil {
		return Status{}, err
	}
	for index, extension := range extensions {
		progress(report, 15+(index*30/len(extensions)), "Installing "+extension)
		if err := m.installer.InstallPackage(ctx, extension, report); err != nil {
			return Status{}, fmt.Errorf("install %s: %w", extension, err)
		}
	}

	root, found := findWebroot()
	if !found {
		return Status{}, fmt.Errorf("%w: the package installed but its files were not found",
			ErrUnsupported)
	}

	progress(report, 55, "Creating the phpMyAdmin account")
	account, err := m.users.Ensure(ctx, Account, TempDir)
	if err != nil {
		return Status{}, fmt.Errorf("create the phpMyAdmin account: %w", err)
	}

	progress(report, 65, "Writing the configuration")
	if err := m.writeConfig(root, account); err != nil {
		return Status{}, err
	}

	progress(report, 72, "Creating the PHP pool")
	socket := php.SocketPathFor(PoolName, version)
	if err := m.writePool(ctx, version, root, account, socket); err != nil {
		return Status{}, err
	}

	progress(report, 80, "Starting PHP-FPM")
	if err := m.startPHP(ctx, version, socket); err != nil {
		return Status{}, err
	}
	m.persistPHP(ctx, version)

	// The vhost goes last, and only once the socket is really there. Pointing
	// nginx at a socket that does not exist yet makes the first request a 502
	// with nothing obviously wrong in the configuration.
	// Anything this package published under another name goes first. Without
	// this, asking for a new name added a server block instead of moving one:
	// the console stayed reachable at the old name, and the panel reported
	// whichever the directory listed first.
	progress(report, 88, "Removing any previous site")
	if err := m.removeOtherVhosts(ctx, serverName); err != nil {
		return Status{}, err
	}

	progress(report, 90, "Publishing the site")
	if err := m.writeVhost(ctx, serverName, root, socket); err != nil {
		return Status{}, err
	}

	progress(report, 100, "phpMyAdmin is ready at "+serverName)
	return m.Status(ctx), nil
}

// Uninstall stops serving phpMyAdmin and removes it.
//
// The vhost goes first. Removing the package while nginx still points at its
// directory would leave every request 404ing against a live server block, which
// looks like a broken panel rather than a removed feature.
func (m *Manager) Uninstall(ctx context.Context, report func(int, string)) error {
	if m.nginx != nil {
		// Every one of them. A host that somehow ended up with two marked
		// vhosts must not be left serving the one this did not look at.
		names, err := m.servedNames()
		if err == nil && len(names) > 0 {
			progress(report, 15, "Removing the site")
			for _, name := range names {
				if _, err := m.nginx.RemoveSite(ctx, name); err != nil {
					return fmt.Errorf("remove the phpMyAdmin site %s: %w", name, err)
				}
			}
			if err := m.nginx.Reload(ctx); err != nil {
				return fmt.Errorf("reload nginx: %w", err)
			}
		}
	}

	if m.pools != nil {
		progress(report, 35, "Removing the PHP pool")
		version, err := m.pickPHP(ctx)
		if err == nil {
			if err := m.pools.RemovePool(ctx, version, PoolName); err != nil {
				m.log.Warn("could not remove the phpMyAdmin pool", "error", err.Error())
			}
		}
	}

	if m.installer != nil && m.installer.Available() {
		if pkg, ok := packageNames[m.installer.Manager()]; ok {
			progress(report, 60, "Removing "+pkg)
			if err := m.installer.RemovePackage(ctx, pkg, report); err != nil {
				return err
			}
		}
	}

	// The state directory goes last and deliberately. It holds cached data
	// from whatever anyone browsed, which has no reason to outlive the
	// installation. The configuration went with the package, and with it the
	// blowfish secret.
	progress(report, 85, "Removing the stored state")
	if err := os.RemoveAll(StateDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove the phpMyAdmin state: %w", err)
	}

	if m.users != nil && m.users.Available() {
		if _, err := m.users.Remove(ctx, Account); err != nil {
			m.log.Warn("could not remove the phpMyAdmin account", "error", err.Error())
		}
	}

	progress(report, 100, "phpMyAdmin has been removed")
	return nil
}

// servedName reports the server_name phpMyAdmin is published under. Empty
// means it is not being served.
//
// Where more than one vhost carries the marker this returns the first in
// directory order, which is what callers wanting a single answer need. Anything
// that has to act on all of them uses servedNames.
func (m *Manager) servedName() (string, error) {
	names, err := m.servedNames()
	if err != nil || len(names) == 0 {
		return "", err
	}
	return names[0], nil
}

// servedNames reports every vhost carrying the marker this package owns.
//
// There should only ever be one, and for a long time the code assumed so and
// returned the first it found. Installing under a second name wrote a second
// vhost and left the first published, so the panel reported one name, served
// two, and on uninstall removed whichever the filesystem happened to list
// first - leaving a database console live on a name the operator believed they
// had removed.
func (m *Manager) servedNames() ([]string, error) {
	entries, err := os.ReadDir(m.nginx.SitesDir())
	if err != nil {
		return nil, err
	}

	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".conf") {
			continue
		}
		content, err := os.ReadFile(filepath.Join(m.nginx.SitesDir(), entry.Name()))
		if err != nil {
			continue
		}
		if !strings.Contains(string(content), vhostMarker) {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".conf"))
	}
	// os.ReadDir already sorts, so the same host gives the same answer twice.
	return names, nil
}

// removeOtherVhosts deletes every vhost this package owns except the one about
// to be written.
//
// nginx is not reloaded here: the new vhost is written immediately afterwards
// and reloads once, so there is no moment where phpMyAdmin is unreachable
// because its old site was removed and its new one not yet published.
func (m *Manager) removeOtherVhosts(ctx context.Context, keep string) error {
	if m.nginx == nil {
		return nil
	}
	names, err := m.servedNames()
	if err != nil {
		return fmt.Errorf("read the published sites: %w", err)
	}
	for _, name := range names {
		if name == keep {
			continue
		}
		if _, err := m.nginx.RemoveSite(ctx, name); err != nil {
			return fmt.Errorf("remove the previous phpMyAdmin site %s: %w", name, err)
		}
	}
	return nil
}

// vhostMarker identifies the vhost this package owns, so uninstalling finds it
// again without the panel having to remember where it put it.
const vhostMarker = "# jothost:phpmyadmin"

// writeConfig writes config.inc.php into the webroot.
func (m *Manager) writeConfig(root string, account sites.Account) error {
	// The parent chain has to be traversable by the account, and MkdirAll
	// creates intermediate directories with the mode it was given and root as
	// their owner. Created 0700 that way, PHP could not reach the session
	// directory at all and every request failed at session_start with nothing
	// but "permission denied" to go on.
	if err := os.MkdirAll(filepath.Dir(StateDir), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(StateDir), err)
	}
	if err := os.MkdirAll(StateDir, 0o750); err != nil {
		return fmt.Errorf("create %s: %w", StateDir, err)
	}
	if err := os.Chown(StateDir, account.UID, account.GID); err != nil {
		return fmt.Errorf("own %s: %w", StateDir, err)
	}

	for _, dir := range []string{TempDir, SessionDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		// These belong to the account the pool runs as, and to nobody else:
		// they hold cached query results and live session data.
		if err := os.Chown(dir, account.UID, account.GID); err != nil {
			return fmt.Errorf("own %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("secure %s: %w", dir, err)
		}
	}

	path := filepath.Join(root, ConfigName)

	// An existing secret is reused. It encrypts the session cookie carrying a
	// signed-in user's database password, so replacing it logs everyone out —
	// pointless on a reinstall, and actively wrong on a live installation.
	secret, err := existingSecret(path)
	if err != nil {
		return err
	}
	if secret == "" {
		secret, err = generateSecret()
		if err != nil {
			return err
		}
	}

	// 0640, owned by the account that reads it. Anyone holding the blowfish
	// secret can forge a session cookie.
	if err := os.WriteFile(path, []byte(renderConfig(secret)), 0o640); err != nil {
		return fmt.Errorf("write the phpMyAdmin configuration: %w", err)
	}
	if err := os.Chown(path, account.UID, account.GID); err != nil {
		return fmt.Errorf("own the phpMyAdmin configuration: %w", err)
	}
	return nil
}

// writePool creates the FPM pool phpMyAdmin runs in.
func (m *Manager) writePool(ctx context.Context, version, root string, account sites.Account, socket string) error {
	settings := php.DefaultSettings()
	// Importing a dump is the one thing phpMyAdmin does that needs headroom,
	// and the defaults for a website are too tight for it.
	settings.UploadMaxFilesize = "256M"
	settings.MemoryLimit = "512M"
	settings.MaxExecutionTime = 300

	pool := php.Pool{
		Name:         PoolName,
		Version:      version,
		User:         account.Name,
		Group:        account.Name,
		SocketPath:   socket,
		ListenGroup:  m.webGroup,
		DocumentRoot: root,
		// The scratch and session directories are outside the webroot, so they
		// have to be named here or PHP refuses to open them.
		ExtraPaths:  []string{StateDir},
		ErrorLog:    filepath.Join(LogDir, "php-error.log"),
		SessionPath: SessionDir,
		Settings:    settings,
		MaxChildren: php.DefaultMaxChildren,
	}

	if _, err := m.pools.WritePool(ctx, pool); err != nil {
		return fmt.Errorf("write the phpMyAdmin pool: %w", err)
	}
	return nil
}

// writeVhost publishes phpMyAdmin under one server name.
// InternalName is the server_name the panel's reverse proxy addresses
// phpMyAdmin by. It must match the Host header in the panel's /phpmyadmin/
// location, in docker/nginx/dev.conf and in scripts/jothost-installer.sh.
const InternalName = "phpmyadmin.internal"

func (m *Manager) writeVhost(ctx context.Context, serverName, root, socket string) error {
	if err := os.MkdirAll(filepath.Dir(LogDir), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(LogDir), err)
	}
	if err := os.MkdirAll(LogDir, 0o755); err != nil {
		return fmt.Errorf("create the phpMyAdmin log directory: %w", err)
	}

	rendered, err := nginx.Render(nginx.SiteConfig{
		PrimaryDomain: serverName,
		// A second, fixed name so the panel's own vhost can proxy phpMyAdmin
		// under /phpmyadmin/ without knowing what the operator called it. The
		// panel's proxy passes this as the Host header; it is how a static
		// configuration file written at install time reaches a site created
		// later. Nothing new is exposed — this is the same site, and it is not
		// a name any resolver answers for.
		Aliases:      []string{InternalName},
		DocumentRoot: root,
		AccessLog:    filepath.Join(LogDir, "access.log"),
		ErrorLog:     filepath.Join(LogDir, "error.log"),
		MaxBodySize:  "256m",
		PHPSocket:    socket,
	})
	if err != nil {
		return fmt.Errorf("render the phpMyAdmin site: %w", err)
	}

	if _, err := m.nginx.WriteSiteRaw(ctx, serverName, vhostMarker+"\n"+rendered); err != nil {
		return fmt.Errorf("write the phpMyAdmin site: %w", err)
	}
	return m.nginx.Reload(ctx)
}

// persistPHP makes the PHP-FPM version phpMyAdmin runs on start after a reboot.
//
// startPHP starts it for this boot and nothing used to enable it, so the first
// restart stopped it and left a stale socket behind: nginx then answered every
// request with 502 and the panel still reported phpMyAdmin as installed and
// served, because the package and the vhost were both exactly where they
// should be.
//
// The Agent's boot sweep could not rescue this. Its rule is "running, not
// installed" — it persists what is up, deliberately, so that a service an
// operator stopped on purpose is not turned back on. A pool that is already
// down is invisible to it, which is why enabling has to happen here, while the
// thing is running and this code knows it is meant to be.
//
// A failure is logged rather than returned. On a host with no init system
// there is nothing to enable and the install is otherwise complete; refusing
// it would leave phpMyAdmin installed and unpublished over a reboot that host
// may never have.
func (m *Manager) persistPHP(ctx context.Context, version string) {
	if m.boot == nil {
		return
	}

	// The unit is named differently on each distribution, so every candidate
	// is tried and the first that takes is the answer. Asking which one exists
	// first would be two round trips to learn what one attempt tells us.
	definitions := services.PHPFPMDefinitions([]string{version})
	if len(definitions) == 0 {
		return
	}

	var lastErr error
	for _, unit := range definitions[0].Units {
		if err := m.boot.Enable(ctx, unit); err == nil {
			m.log.Info("php-fpm will start at boot", "version", version, "unit", unit)
			return
		} else {
			lastErr = err
		}
	}

	m.log.Warn("php-fpm could not be set to start at boot: phpMyAdmin will 502 after a reboot",
		"version", version, logger.KeyError, lastErr,
		"detail", "start it by hand after a restart, or enable the unit for this PHP version")
}

// startPHP makes FPM read the new pool and waits for its socket.
func (m *Manager) startPHP(ctx context.Context, version, socket string) error {
	if m.fpm == nil {
		return fmt.Errorf("%w: PHP-FPM cannot be controlled on this host", ErrUnsupported)
	}

	start := func() error {
		if php.FPMRunning(version) {
			return m.fpm.ReloadFPM(ctx, version)
		}
		return m.fpm.StartFPM(ctx, version, m.php)
	}

	if err := start(); err != nil {
		return fmt.Errorf("start PHP-FPM %s: %w", version, err)
	}

	if err := waitForSocket(socket, socketWaitTimeout); err == nil {
		return nil
	}

	// A reload that went to a master which is not actually serving. Starting a
	// fresh one is the recovery, and it is tried before giving up: the
	// alternative is phpMyAdmin left installed but permanently 502ing.
	m.log.Warn("the phpMyAdmin pool socket did not appear after a reload; starting php-fpm",
		"version", version)
	if err := m.fpm.StartFPM(ctx, version, m.php); err != nil {
		return fmt.Errorf("start PHP-FPM %s: %w", version, err)
	}
	if err := waitForSocket(socket, socketWaitTimeout); err != nil {
		return fmt.Errorf("PHP-FPM started but its socket never appeared: %w", err)
	}
	return nil
}

// socketWaitTimeout bounds the wait for the pool socket.
//
// FPM creates it shortly after the master forks, so this is generous rather
// than long: exceeding it means the pool was rejected, not that it is slow.
const socketWaitTimeout = 10 * time.Second

// waitForSocket blocks until the pool socket exists.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		info, err := os.Stat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not appear within %s", path, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// findWebroot returns the first known phpMyAdmin directory that exists.
func findWebroot() (string, bool) {
	for _, root := range webroots {
		info, err := os.Stat(filepath.Join(root, "index.php"))
		if err == nil && info.Mode().IsRegular() {
			return root, true
		}
	}
	return "", false
}

// secretLength is phpMyAdmin's required blowfish secret length.
const secretLength = 32

// secretAlphabet excludes the quote characters and the backslash, because the
// secret is written into a PHP string literal.
const secretAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!#%()*+,-.:;=?@^_~"

// generateSecret returns a new blowfish secret.
func generateSecret() (string, error) {
	limit := big.NewInt(int64(len(secretAlphabet)))
	out := make([]byte, secretLength)

	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate the phpMyAdmin secret: %w", err)
		}
		out[i] = secretAlphabet[n.Int64()]
	}
	return string(out), nil
}

// existingSecret reads back the secret from a configuration already on disk.
func existingSecret(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read the phpMyAdmin configuration: %w", err)
	}

	const marker = "$cfg['blowfish_secret'] = '"
	index := strings.Index(string(content), marker)
	if index < 0 {
		return "", nil
	}
	rest := string(content)[index+len(marker):]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return "", nil
	}
	return rest[:end], nil
}

// progress reports a step, tolerating a nil reporter.
func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}

// compareVersions orders two major.minor version strings.
func compareVersions(a, b string) int {
	majorA, minorA := splitVersion(a)
	majorB, minorB := splitVersion(b)

	switch {
	case majorA != majorB:
		return majorA - majorB
	default:
		return minorA - minorB
	}
}

func splitVersion(version string) (int, int) {
	parts := strings.SplitN(version, ".", 3)
	major, minor := 0, 0
	if len(parts) > 0 {
		major = atoi(parts[0])
	}
	if len(parts) > 1 {
		minor = atoi(parts[1])
	}
	return major, minor
}

func atoi(value string) int {
	n := 0
	for _, r := range value {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
