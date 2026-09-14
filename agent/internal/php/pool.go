package php

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/jothost/panel/agent/internal/fsperm"
	"github.com/jothost/panel/shared/validate"
)

// Pool describes one website's FPM pool.
type Pool struct {
	// Name identifies the pool to FPM and names its config file.
	Name string
	// Version is the PHP that runs it.
	Version string
	// User and Group are the account the pool's workers run as. This is the
	// site's own account: a pool running as root, or as a shared account,
	// would let any PHP script on the site read every other site's files.
	User  string
	Group string
	// SocketPath is the Unix socket nginx connects to.
	SocketPath string
	// ListenGroup owns the socket so the web server can reach it. Without
	// this the pool starts, nginx cannot open the socket, and every request
	// to the site returns 502 with nothing obviously wrong.
	ListenGroup string
	// DocumentRoot is the only directory PHP may open files under.
	DocumentRoot string
	// Settings are the per-site php.ini values.
	Settings Settings
	// MaxChildren caps concurrent workers for this site.
	MaxChildren int

	// ExtraPaths are directories added to open_basedir beyond the document
	// root and its site's own scratch space.
	//
	// Empty for a website, which is what almost every pool is. It exists for
	// an application the panel installs from a package, whose scratch space
	// necessarily lives outside a directory the package manager owns.
	ExtraPaths []string
	// ErrorLog and SessionPath override the paths otherwise derived from the
	// site layout. Empty means that layout, which is what every website uses;
	// a pool whose document root is not inside one has to say where they go,
	// or PHP writes them beside somebody else's files.
	ErrorLog    string
	SessionPath string
}

// Settings are the php.ini values a site may choose.
type Settings struct {
	MemoryLimit       string
	UploadMaxFilesize string
	MaxExecutionTime  int
	OPcacheEnabled    bool
}

// Defaults for a new pool. They are modest on purpose: a shared host runs many
// pools, and a generous default multiplied by every site exhausts the host.
const (
	DefaultMemoryLimit       = "256M"
	DefaultUploadMaxFilesize = "64M"
	DefaultMaxExecutionTime  = 30
	DefaultMaxChildren       = 5
	MaxChildrenLimit         = 512
)

// DefaultSettings returns the settings a site gets when it chooses none.
func DefaultSettings() Settings {
	return Settings{
		MemoryLimit:       DefaultMemoryLimit,
		UploadMaxFilesize: DefaultUploadMaxFilesize,
		MaxExecutionTime:  DefaultMaxExecutionTime,
		OPcacheEnabled:    true,
	}
}

// SocketDir is where pool sockets live.
const SocketDir = "/run/php-fpm"

// SocketPathFor returns the socket path for a pool on a given version.
//
// The version is part of the name so that switching a site from one PHP to
// another never has two FPM masters contending for one path. Without it the
// new version refuses to start ("Another FPM instance seems to already listen
// on ..."), and the only way to avoid that is to stop the old pool first,
// which takes the site down for the length of the switch.
func SocketPathFor(poolName, version string) string {
	return filepath.Join(SocketDir,
		poolName+"-"+validate.PHPVersionCompact(version)+".sock")
}

// poolTemplate renders an FPM pool.
//
// Every value reaching it is validated first. A pool file is read by a process
// that spawns workers as a named user, so a value carrying a newline would let
// a caller append directives of their choosing — including `user = root`.
//
// php_admin_value is used rather than php_value for the limits that matter:
// admin values cannot be overridden by ini_set() from inside the application,
// so a compromised or careless script cannot raise its own memory ceiling or
// escape open_basedir.
var poolTemplate = template.Must(template.New("pool").Parse(
	`; Managed by JotHost Panel. Manual edits are overwritten.
[{{ .Name }}]

; Workers run as the site's own account, which is what isolates one site's
; files from every other site on this host.
user = {{ .User }}
group = {{ .Group }}

listen = {{ .SocketPath }}
; The socket is owned by the site and group-owned by the web server, so nginx
; can connect and nothing else can.
listen.owner = {{ .User }}
listen.group = {{ .ListenGroup }}
listen.mode = 0660

pm = dynamic
pm.max_children = {{ .MaxChildren }}
pm.start_servers = {{ .StartServers }}
pm.min_spare_servers = {{ .MinSpare }}
pm.max_spare_servers = {{ .MaxSpare }}
; Recycle workers so a slow leak in an application does not accumulate for the
; lifetime of the pool.
pm.max_requests = 500

; PHP may only open files beneath the site's own document root. This is the
; backstop for a path traversal in application code: without it, a bug in one
; site can read another site's configuration.
php_admin_value[open_basedir] = {{ .OpenBasedir }}

php_admin_value[memory_limit] = {{ .Settings.MemoryLimit }}
php_admin_value[upload_max_filesize] = {{ .Settings.UploadMaxFilesize }}
php_admin_value[post_max_size] = {{ .Settings.UploadMaxFilesize }}
php_admin_value[max_execution_time] = {{ .Settings.MaxExecutionTime }}

; Errors belong in the log, never in the response: a stack trace in a page
; hands an attacker paths, versions, and query structure.
php_admin_flag[display_errors] = off
php_admin_flag[log_errors] = on
php_admin_value[error_log] = {{ .ErrorLog }}

; Functions that exist only to run other programs. A hosting panel that leaves
; these enabled gives every site shell access to the host.
php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen,proc_nice,dl

php_admin_flag[opcache.enable] = {{ if .Settings.OPcacheEnabled }}on{{ else }}off{{ end }}

; The session directory is per-site, so one site cannot read or forge another
; site's sessions by reading /tmp.
php_admin_value[session.save_path] = {{ .SessionPath }}
`))

// poolView is the template's input, with derived values computed once.
type poolView struct {
	Pool
	StartServers int
	MinSpare     int
	MaxSpare     int
	OpenBasedir  string
	ErrorLog     string
	SessionPath  string
}

// Errors returned by the pool provider.
var (
	ErrPoolUnavailable = errors.New("php-fpm is not available on this host")
	ErrInvalidPool     = errors.New("invalid pool configuration")
)

// RenderPool produces an FPM pool configuration.
//
// This is the last point before text becomes a file FPM will act on, so every
// value is re-validated here even though callers validate too.
func RenderPool(pool Pool) (string, error) {
	if err := validate.PHPPoolName(pool.Name); err != nil {
		return "", err
	}
	if err := validate.PHPVersion(pool.Version); err != nil {
		return "", err
	}
	if err := validate.SystemUser(pool.User); err != nil {
		return "", fmt.Errorf("pool user: %w", err)
	}
	if pool.Group != "" {
		if err := validate.SystemUser(pool.Group); err != nil {
			return "", fmt.Errorf("pool group: %w", err)
		}
	}
	if pool.ListenGroup == "" {
		return "", fmt.Errorf("%w: no web server group, so nginx could not reach the socket",
			ErrInvalidPool)
	}
	// GroupName, not SystemUser: the web server group is legitimately one of
	// the reserved names a *site* may never be given.
	if err := validate.GroupName(pool.ListenGroup); err != nil {
		return "", fmt.Errorf("listen group: %w", err)
	}
	if err := validateConfigPath(pool.SocketPath); err != nil {
		return "", fmt.Errorf("socket path: %w", err)
	}
	if err := validateConfigPath(pool.DocumentRoot); err != nil {
		return "", fmt.Errorf("document root: %w", err)
	}

	settings := pool.Settings
	if settings.MemoryLimit == "" {
		settings.MemoryLimit = DefaultMemoryLimit
	}
	if settings.UploadMaxFilesize == "" {
		settings.UploadMaxFilesize = DefaultUploadMaxFilesize
	}
	if settings.MaxExecutionTime == 0 {
		settings.MaxExecutionTime = DefaultMaxExecutionTime
	}
	if err := validate.PHPMemoryLimit(settings.MemoryLimit); err != nil {
		return "", err
	}
	if err := validate.PHPUploadSize(settings.UploadMaxFilesize); err != nil {
		return "", err
	}
	if err := validate.PHPExecutionTime(settings.MaxExecutionTime); err != nil {
		return "", err
	}
	pool.Settings = settings

	children := pool.MaxChildren
	if children <= 0 {
		children = DefaultMaxChildren
	}
	if children > MaxChildrenLimit {
		return "", fmt.Errorf("%w: max_children may not exceed %d",
			ErrInvalidPool, MaxChildrenLimit)
	}
	pool.MaxChildren = children

	// The site root is the document root's parent, which is where logs and
	// sessions live — alongside content rather than inside it, so neither is
	// ever served to a visitor who guesses the name.
	siteRoot := filepath.Dir(pool.DocumentRoot)

	// The temp directory is included because PHP needs somewhere to put
	// uploads before the application moves them; without it every upload fails
	// with an open_basedir violation.
	openBasedir := pool.DocumentRoot + ":" + siteRoot + "/tmp:/tmp"
	for _, extra := range pool.ExtraPaths {
		if err := validateConfigPath(extra); err != nil {
			return "", fmt.Errorf("extra path: %w", err)
		}
		openBasedir += ":" + extra
	}

	errorLog := pool.ErrorLog
	if errorLog == "" {
		errorLog = filepath.Join(siteRoot, "logs", "php-error.log")
	} else if err := validateConfigPath(errorLog); err != nil {
		return "", fmt.Errorf("error log: %w", err)
	}

	sessionPath := pool.SessionPath
	if sessionPath == "" {
		sessionPath = filepath.Join(siteRoot, "tmp", "sessions")
	} else if err := validateConfigPath(sessionPath); err != nil {
		return "", fmt.Errorf("session path: %w", err)
	}

	view := poolView{
		Pool:         pool,
		StartServers: max(1, children/2),
		MinSpare:     1,
		MaxSpare:     max(1, children-1),
		OpenBasedir:  openBasedir,
		ErrorLog:     errorLog,
		SessionPath:  sessionPath,
	}

	var out bytes.Buffer
	if err := poolTemplate.Execute(&out, view); err != nil {
		return "", fmt.Errorf("render pool config: %w", err)
	}
	return out.String(), nil
}

// validateConfigPath rejects a path that could break out of a directive.
//
// An FPM pool file is line-oriented and section-based. A path containing a
// newline would close the directive and start another; one containing a
// bracket could open a new pool section entirely, defining workers that run as
// whatever user it names.
func validateConfigPath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%q is not absolute", path)
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("%q traverses upward", path)
	}
	if strings.ContainsAny(path, "\x00\n\r[]$'\"") {
		return fmt.Errorf("%q contains a character that is not valid in a path", path)
	}
	return nil
}

// Provider writes, removes, and validates FPM pools.
type Provider struct {
	detector *Detector
	root     string
}

// ProviderOptions configure a Provider.
type ProviderOptions struct {
	Detector *Detector
	// Root prefixes every written path; empty in production.
	Root string
}

// NewProvider builds a Provider.
func NewProvider(opts ProviderOptions) *Provider {
	return &Provider{detector: opts.Detector, root: opts.Root}
}

// Available reports whether any PHP version can host a pool.
func (p *Provider) Available() bool {
	return p.detector != nil && p.detector.Available()
}

// poolMode is the permission on a pool file. It holds no secrets but describes
// how a site runs, and only root writes it.
const poolMode os.FileMode = 0o644

// PoolPath returns where a pool's configuration is written.
func (p *Provider) PoolPath(ctx context.Context, version, poolName string) (string, error) {
	if err := validate.PHPPoolName(poolName); err != nil {
		return "", err
	}

	installed, err := p.detector.Lookup(ctx, version)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.root, installed.PoolDir, poolName+".conf"), nil
}

// WritePool installs a pool configuration and reports its path.
//
// The write is validated by FPM before it is left in place: a pool file FPM
// refuses stops the whole service from starting, which would take down every
// PHP site on that version, not just this one.
func (p *Provider) WritePool(ctx context.Context, pool Pool) (string, error) {
	if !p.Available() {
		return "", ErrPoolUnavailable
	}

	installed, err := p.detector.Lookup(ctx, pool.Version)
	if err != nil {
		return "", err
	}

	rendered, err := RenderPool(pool)
	if err != nil {
		return "", err
	}

	path, err := p.PoolPath(ctx, pool.Version, pool.Name)
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create pool directory: %w", err)
	}

	// The previous content is kept so a failed validation can put it back
	// exactly, including "no file existed before".
	previous, hadPrevious, err := readIfExists(path)
	if err != nil {
		return "", err
	}

	if err := os.WriteFile(path, []byte(rendered), poolMode); err != nil {
		return "", fmt.Errorf("write pool config: %w", err)
	}

	if err := p.Validate(ctx, installed.Version); err != nil {
		if restoreErr := restore(path, previous, hadPrevious); restoreErr != nil {
			return "", errors.Join(err, restoreErr)
		}
		return "", err
	}

	return path, nil
}

// RemovePool deletes a pool configuration.
//
// A missing file is not an error: removal is idempotent so a retried job
// converges rather than failing on its second attempt.
func (p *Provider) RemovePool(ctx context.Context, version, poolName string) error {
	path, err := p.PoolPath(ctx, version, poolName)
	if err != nil {
		if errors.Is(err, ErrVersionNotInstalled) {
			// The version is gone, so its pool directory is too. Nothing to do.
			return nil
		}
		return err
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pool config: %w", err)
	}

	// FPM does not unlink a socket when its pool disappears from the
	// configuration, so the file would linger with nothing listening on it.
	// Removing it keeps /run honest about what is actually running.
	socket := filepath.Join(p.root, SocketPathFor(poolName, version))
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pool socket: %w", err)
	}
	return nil
}

// PoolExists reports whether a pool is configured.
func (p *Provider) PoolExists(ctx context.Context, version, poolName string) (bool, error) {
	path, err := p.PoolPath(ctx, version, poolName)
	if err != nil {
		return false, err
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat pool config: %w", err)
	}
	return true, nil
}

// Validate asks FPM to check its configuration.
func (p *Provider) Validate(ctx context.Context, version string) error {
	if p.detector == nil || p.detector.runner == nil {
		return ErrPoolUnavailable
	}

	name := CommandFor(version)
	if !p.detector.runner.Available(name) {
		return ErrVersionNotInstalled
	}

	result, err := p.detector.runner.Run(ctx, name, "-t")
	if err != nil {
		return fmt.Errorf("php-fpm configuration test: %w", err)
	}
	if !result.Succeeded() {
		// FPM writes its diagnostics to stderr. They name the offending file
		// and line, which is what makes a rejected config fixable.
		return fmt.Errorf("php-fpm rejected the configuration: %s",
			strings.TrimSpace(firstNonEmpty(result.Stderr, result.Stdout)))
	}
	return nil
}

// PrepareRuntime creates the directories a pool needs before it starts.
//
// The socket directory must exist and be traversable by the web server, and
// the per-site session and temp directories must exist and be owned by the
// site. FPM does not create any of them.
func (p *Provider) PrepareRuntime(siteRoot string, uid, gid int) error {
	socketDir := filepath.Join(p.root, SocketDir)
	// Stated, not requested: under the Agent's 0077 umask a plain MkdirAll
	// makes this 0700, nginx cannot reach the sockets inside, and every PHP
	// site answers 502. It is invisible until a reboot, because /run is
	// emptied at boot and this is the code that recreates the directory.
	if err := fsperm.MkdirAll(socketDir, 0o755); err != nil {
		return fmt.Errorf("create socket directory: %w", err)
	}

	if siteRoot == "" {
		return nil
	}
	if err := validateConfigPath(siteRoot); err != nil {
		return fmt.Errorf("site root: %w", err)
	}

	// Sessions hold logged-in users' identities, so the directory is readable
	// only by the site's own account — not by the web server group, which
	// every other site's pool can also reach.
	for _, dir := range []string{
		filepath.Join(p.root, siteRoot, "tmp"),
		filepath.Join(p.root, siteRoot, "tmp", "sessions"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("chmod %s: %w", dir, err)
		}
		if uid >= 0 && gid >= 0 {
			if err := os.Chown(dir, uid, gid); err != nil {
				return fmt.Errorf("chown %s: %w", dir, err)
			}
		}
	}
	return nil
}

func readIfExists(path string) ([]byte, bool, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return content, true, nil
}

func restore(path string, previous []byte, hadPrevious bool) error {
	if !hadPrevious {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove rejected pool config: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(path, previous, poolMode); err != nil {
		return fmt.Errorf("restore previous pool config: %w", err)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "no diagnostic output"
}

// RemoveOtherPools deletes a pool from every installed version except one.
//
// This is what finishes a version switch. It runs after the vhost already
// points at the new socket, so the pools it removes are ones nothing is using
// any more, and the site never stops serving.
//
// It returns the versions it removed a pool from, so the caller can reload
// each of them: an FPM that is not reloaded keeps the old pool running and
// holding its socket.
func (p *Provider) RemoveOtherPools(ctx context.Context, keepVersion, poolName string) ([]string, error) {
	if err := validate.PHPPoolName(poolName); err != nil {
		return nil, err
	}

	touched := make([]string, 0, 2)
	for _, installed := range Probe(p.root) {
		if installed.Version == keepVersion {
			continue
		}

		exists, err := p.PoolExists(ctx, installed.Version, poolName)
		if err != nil || !exists {
			continue
		}
		if err := p.RemovePool(ctx, installed.Version, poolName); err != nil {
			return touched, err
		}
		touched = append(touched, installed.Version)
	}
	return touched, nil
}
