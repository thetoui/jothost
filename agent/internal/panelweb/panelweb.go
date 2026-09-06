// Package panelweb runs the web stack the panel's own applications are served
// by, separately from the one that serves customer websites.
//
// The two used to share everything. phpMyAdmin's vhost sat in the same
// directory nginx reads customer sites from, and its PHP pool sat in the same
// directory — and therefore ran under the same master process — as every
// website's pool on that PHP version. Three consequences, all of which
// happened:
//
//   - A customer site whose configuration nginx refuses stops the reload that
//     would have published a panel change, and a customer pool that stops the
//     PHP master from starting takes phpMyAdmin down with it. The panel's
//     database console going dark is exactly when an operator most needs it.
//   - The panel had to identify its own vhosts by scanning a shared directory
//     for a marker comment, because there was nothing else distinguishing them
//     from a website somebody had created.
//   - A website named the same as a panel application would collide, and which
//     one won depended on filesystem order.
//
// So the panel gets its own nginx, bound to loopback, reading only its own
// directory; and its own PHP-FPM master, reading only its own pools. The
// public nginx proxies to it. A website can now break as thoroughly as it
// likes without taking the panel's applications with it, and nothing the
// website system enumerates can see in here.
package panelweb

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/php"
)

// Errors returned by the manager.
var (
	// ErrUnavailable means the host cannot run the panel's own stack.
	ErrUnavailable = errors.New("the panel's web stack cannot run on this host")
)

// The layout. Everything the panel serves itself lives under one root, so an
// operator reading the filesystem can tell at a glance which files belong to
// the panel and which to somebody's website.
const (
	// Root is the panel's own configuration tree.
	Root = "/etc/jothost/web"
	// ConfigPath is the nginx configuration for the panel's instance.
	ConfigPath = Root + "/nginx.conf"
	// SitesDir holds one file per panel application.
	SitesDir = Root + "/sites.d"
	// PoolDir holds the PHP pools the panel's own master reads. Separate from
	// the websites' pool directory, which is the point: a website's pool
	// cannot stop this master starting.
	PoolDir = Root + "/php-fpm.d"
	// FPMConfigPath is the panel PHP master's own configuration.
	FPMConfigPath = Root + "/php-fpm.conf"

	// RunDir holds the pid files and sockets.
	//
	// Beside /run/jothost rather than inside it. That directory is 0750
	// root:jothost so only the Agent's own account can reach the privileged
	// socket in it, and nginx is not in that group — the panel's nginx could
	// not traverse into a subdirectory of it and answered 502 to everything,
	// with a socket sitting there in plain view with exactly the right owner
	// and mode. Loosening /run/jothost to fix that would have traded the
	// Agent's socket directory for a database console.
	RunDir = "/run/jothost-web"
	// NginxPID is the panel nginx instance's pid file.
	NginxPID = RunDir + "/nginx.pid"
	// FPMPID is the panel PHP master's pid file.
	FPMPID = RunDir + "/php-fpm.pid"

	// LogDir holds the panel stack's logs, away from the websites' logs so a
	// log viewer scoped to a customer cannot be pointed at them.
	LogDir = "/var/log/jothost/web"

	// TempRoot is where the panel nginx keeps its scratch directories. nginx
	// refuses to start if it cannot create these.
	TempRoot = "/var/lib/jothost/web"
)

// DefaultPort is where the panel's nginx listens.
//
// On loopback only. These applications are reached through the panel's public
// vhost, which is the only thing that authenticates the visitor's route to
// them; a port open to the world would be a second front door with no lock on
// it. The number is above the range a distribution package is likely to claim.
const DefaultPort = 8791

// DefaultListenAddress keeps the panel's nginx off every public interface.
const DefaultListenAddress = "127.0.0.1"

// startTimeout bounds how long a start is waited for. A master that has not
// written its pid file by now is not going to.
const startTimeout = 10 * time.Second

// Options configures a Manager.
type Options struct {
	Runner *command.Runner
	// PHP finds the interpreter the panel's own master runs.
	PHP *php.Detector
	// WebGroup owns the sockets so the panel's nginx can open them.
	WebGroup string
	// Port defaults to DefaultPort.
	Port int
	// ListenAddress defaults to 127.0.0.1. See config.PanelWebListen for why
	// it is configurable at all.
	ListenAddress string
	// Root overrides the configuration tree, for tests.
	Root string
	// RunDir overrides the pid directory, for tests.
	RunDir string
	Log    *slog.Logger
}

// Manager owns the panel's private nginx instance and PHP master.
type Manager struct {
	runner   *command.Runner
	php      *php.Detector
	nginx    *nginx.Provider
	webGroup string
	port     int
	listen   string
	root     string
	runDir   string
	log      *slog.Logger
}

// NewManager builds a Manager.
func NewManager(opts Options) *Manager {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	port := opts.Port
	if port == 0 {
		port = DefaultPort
	}
	listen := opts.ListenAddress
	if listen == "" {
		listen = DefaultListenAddress
	}
	root := opts.Root
	if root == "" {
		root = Root
	}
	runDir := opts.RunDir
	if runDir == "" {
		runDir = RunDir
	}

	return &Manager{
		runner: opts.Runner,
		php:    opts.PHP,
		nginx: nginx.NewProvider(nginx.Options{
			Runner:     opts.Runner,
			SitesDir:   filepath.Join(root, "sites.d"),
			ConfigPath: filepath.Join(root, "nginx.conf"),
			Prefix:     root,
			ErrorLog:   filepath.Join(LogDir, "error.log"),
		}),
		webGroup: opts.WebGroup,
		port:     port,
		listen:   listen,
		root:     root,
		runDir:   runDir,
		log:      log,
	}
}

// Nginx returns the provider for the panel's own instance.
//
// Callers write their vhost through this rather than through the host's
// provider. That is the whole separation: a file written here is invisible to
// the nginx that serves websites, and vice versa.
func (m *Manager) Nginx() *nginx.Provider { return m.nginx }

// Port reports the port the panel's nginx listens on.
func (m *Manager) Port() int { return m.port }

// ListenAddress reports the address the panel's nginx binds to.
func (m *Manager) ListenAddress() string { return m.listen }

// Address is the listen directive for the panel's own applications.
func (m *Manager) Address() string {
	return net.JoinHostPort(m.listen, strconv.Itoa(m.port))
}

// PoolDir reports where the panel's own PHP pools are written.
func (m *Manager) PoolDir() string { return filepath.Join(m.root, "php-fpm.d") }

// FastCGIParams is where this instance reads the standard FastCGI variables
// from.
//
// An absolute path, because nginx resolves a bare include against its prefix
// and this instance's prefix is the panel's own tree. EnsureLayout copies the
// distribution's file in, so the two nginxes still agree on what the variables
// are without this one reading out of the other's directory.
func (m *Manager) FastCGIParams() string {
	return filepath.Join(m.root, "fastcgi_params")
}

// Socket returns the path a panel application's FPM pool should listen on.
func (m *Manager) Socket(name string) string {
	return filepath.Join(m.runDir, name+".sock")
}

// Available reports whether the panel's stack can run here.
func (m *Manager) Available() bool {
	return m.runner != nil && m.runner.Available(nginx.CommandName)
}

// EnsureLayout creates the directories and writes the nginx configuration.
//
// Idempotent, and called on every Agent start rather than only at install:
// these directories are under /run and /var, which a reboot or a cleaner can
// empty, and an nginx that cannot create its temporary directories refuses to
// start with a message about a path nobody chose.
func (m *Manager) EnsureLayout() error {
	dirs := []struct {
		path string
		mode os.FileMode
	}{
		{m.root, 0o755},
		{filepath.Join(m.root, "sites.d"), 0o755},
		{m.PoolDir(), 0o755},
		{m.runDir, 0o750},
		{LogDir, 0o755},
		{TempRoot, 0o755},
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir.path, dir.mode); err != nil {
			return fmt.Errorf("create %s: %w", dir.path, err)
		}
		// MkdirAll applies the mode only to directories it creates, and a
		// directory left from an earlier version with the wrong mode is the
		// same 502.
		if err := os.Chmod(dir.path, dir.mode); err != nil {
			return fmt.Errorf("set the mode on %s: %w", dir.path, err)
		}
	}

	// The run directory belongs to the web group, so nginx can traverse it to
	// reach the pool socket and nothing else can.
	if err := m.ownRunDir(); err != nil {
		return err
	}

	if err := os.WriteFile(ConfigPathIn(m.root), []byte(m.renderNginxConfig()), 0o644); err != nil {
		return fmt.Errorf("write the panel nginx configuration: %w", err)
	}
	if err := m.copyFastCGIParams(); err != nil {
		return err
	}
	return nil
}

// copyFastCGIParams puts the distribution's FastCGI variable list where this
// instance can read it.
//
// Copied rather than included from /etc/nginx: an include reaching into the
// host nginx's tree is the kind of shared path this package exists to remove,
// and it would make the panel's applications depend on a directory the website
// system is free to reorganise.
func (m *Manager) copyFastCGIParams() error {
	destination := m.FastCGIParams()

	for _, source := range []string{
		"/etc/nginx/fastcgi_params",
		"/etc/nginx/fastcgi.conf",
		"/usr/local/nginx/conf/fastcgi_params",
	} {
		content, err := os.ReadFile(source)
		if err != nil {
			continue
		}
		if err := os.WriteFile(destination, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", destination, err)
		}
		return nil
	}

	// A host whose nginx package shipped no such file. Written empty rather
	// than left missing: nginx refuses to start on a missing include, and an
	// empty one degrades to PHP seeing fewer variables rather than to a
	// database console that will not come up at all.
	if _, err := os.Stat(destination); err == nil {
		return nil
	}
	if err := os.WriteFile(destination, []byte("# nginx shipped no fastcgi_params on this host.\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", destination, err)
	}
	return nil
}

// ownRunDir gives the run directory to the web group.
//
// A group that cannot be resolved is not fatal: the directory is then left
// root-owned, the panel's nginx cannot reach the socket, and the failure
// surfaces as a 502 with a line in the log saying why — which is better than
// refusing to install over a group name.
func (m *Manager) ownRunDir() error {
	if m.webGroup == "" {
		m.log.Warn("the web server's group is unknown: the panel's nginx may not reach its PHP socket")
		return nil
	}
	gid, ok := lookupGroup(m.webGroup)
	if !ok {
		m.log.Warn("the web server's group could not be resolved",
			"group", m.webGroup,
			"detail", "the panel's nginx may not be able to reach its PHP socket")
		return nil
	}
	if err := os.Chown(m.runDir, 0, gid); err != nil {
		return fmt.Errorf("give %s to the %s group: %w", m.runDir, m.webGroup, err)
	}
	return nil
}

// ConfigPathIn names the nginx configuration inside a root.
func ConfigPathIn(root string) string { return filepath.Join(root, "nginx.conf") }

// FPMConfigPathIn names the PHP master configuration inside a root.
func FPMConfigPathIn(root string) string { return filepath.Join(root, "php-fpm.conf") }

// NginxRunning reports whether the panel's instance is up.
//
// The pid file is read and the process checked, rather than trusting the file:
// a pid file left behind by a killed master is exactly the state that produces
// a 502 with a configuration that looks perfect. An empty file counts as not
// running for the same reason — nginx creates it before it has anything to
// write there.
func (m *Manager) NginxRunning() bool {
	return processAlive(filepath.Join(m.runDir, "nginx.pid"))
}

// FPMRunning reports whether the panel's PHP master is up.
func (m *Manager) FPMRunning() bool {
	return processAlive(filepath.Join(m.runDir, "php-fpm.pid"))
}

// StartNginx brings the panel's instance up, or reloads it if it is already
// running.
func (m *Manager) StartNginx(ctx context.Context) error {
	if !m.Available() {
		return fmt.Errorf("%w: nginx is not installed", ErrUnavailable)
	}
	if err := m.EnsureLayout(); err != nil {
		return err
	}

	if m.NginxRunning() {
		// Reload validates first, so a broken panel vhost is refused rather
		// than taking down an instance that was serving.
		return m.nginx.Reload(ctx)
	}

	// Validated before starting for the same reason: a master that exits on a
	// bad configuration leaves nothing to read the error from.
	if err := m.nginx.Validate(ctx); err != nil {
		return err
	}

	result, err := m.runner.Run(ctx, nginx.CommandName, "-p", m.root, "-c", ConfigPathIn(m.root))
	if err != nil {
		return fmt.Errorf("start the panel's nginx: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("the panel's nginx did not start: %s",
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	if err := waitForFile(filepath.Join(m.runDir, "nginx.pid"), startTimeout); err != nil {
		return fmt.Errorf("the panel's nginx started but wrote no pid file: %w", err)
	}
	return nil
}

// StopNginx stops the panel's instance. A stopped instance is not an error.
func (m *Manager) StopNginx(ctx context.Context) error {
	if !m.NginxRunning() {
		return nil
	}
	result, err := m.runner.Run(ctx, nginx.CommandName,
		"-p", m.root, "-c", ConfigPathIn(m.root), "-s", "stop")
	if err != nil {
		return fmt.Errorf("stop the panel's nginx: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("the panel's nginx did not stop: %s",
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// StartFPM brings the panel's own PHP master up, or reloads it.
//
// The version is the one the caller's application needs. A panel that later
// serves two applications on two PHP versions would need a master each; this
// takes the version rather than assuming one so that change is a change here
// and not everywhere.
func (m *Manager) StartFPM(ctx context.Context, version string) error {
	if m.php == nil {
		return fmt.Errorf("%w: no PHP was found", ErrUnavailable)
	}
	if _, err := m.php.Lookup(ctx, version); err != nil {
		return err
	}
	if err := m.EnsureLayout(); err != nil {
		return err
	}
	if err := m.writeFPMConfig(version); err != nil {
		return err
	}

	binary := php.CommandFor(version)
	if !m.runner.Available(binary) {
		return fmt.Errorf("%w: php-fpm %s is not installed", ErrUnavailable, version)
	}

	pidPath := filepath.Join(m.runDir, "php-fpm.pid")

	if m.FPMRunning() {
		// USR2 makes the master re-read its pools. A restart would drop every
		// request in flight, which for a database console is somebody's import
		// halfway through.
		result, err := m.runner.Run(ctx, binary,
			"--fpm-config", FPMConfigPathIn(m.root), "--test")
		if err == nil && result.Succeeded() {
			if err := signalReload(pidPath); err == nil {
				return nil
			}
		}
		// Falling through to a start: the master is up but would not reload,
		// which is worse than a restart.
		m.log.Warn("the panel's PHP master would not reload; restarting it")
		_ = m.StopFPM(ctx)
	}

	result, err := m.runner.Run(ctx, binary,
		"--fpm-config", FPMConfigPathIn(m.root), "--daemonize", "--pid", pidPath)
	if err != nil {
		return fmt.Errorf("start the panel's php-fpm: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("the panel's php-fpm did not start: %s",
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	if err := waitForFile(pidPath, startTimeout); err != nil {
		return fmt.Errorf("the panel's php-fpm started but wrote no pid file: %w", err)
	}
	return nil
}

// StopFPM stops the panel's PHP master. A stopped master is not an error.
func (m *Manager) StopFPM(ctx context.Context) error {
	pidPath := filepath.Join(m.runDir, "php-fpm.pid")
	if !processAlive(pidPath) {
		return nil
	}
	return signalStop(pidPath)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// waitForFile waits for a path to appear.
func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s did not appear within %s", path, timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// readPID reads a pid file.
func readPID(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil {
		return 0, fmt.Errorf("%s does not contain a pid: %w", path, err)
	}
	return pid, nil
}
