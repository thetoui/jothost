package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// handlePHPVersions reports the PHP versions installed on the host.
func (r *Registry) handlePHPVersions(ctx context.Context, _ protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	if r.deps.PHP == nil {
		return nil, Fail(protocol.CodeUnsupported, "PHP management is not available on this host", nil)
	}

	versions := r.deps.PHP.Detect(ctx)

	manager := ""
	canInstall := false
	if r.deps.PHPInstaller != nil {
		manager = r.deps.PHPInstaller.Manager()
		canInstall = r.deps.PHPInstaller.Available()
	}

	return map[string]any{
		"versions": php.FormatVersions(versions),
		"count":    len(versions),
		// The panel hides the install control when the host cannot install,
		// rather than offering a button that always fails.
		"can_install":     canInstall,
		"package_manager": manager,
	}, nil
}

// phpVersionPayload names a PHP version.
type phpVersionPayload struct {
	Version string `json:"version"`
}

// handlePHPInstall adds a PHP version to the host.
func (r *Registry) handlePHPInstall(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload phpVersionPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no supported package manager, so PHP cannot be installed", nil)
	}

	// Already installed is success, not an error: a retried job must converge
	// rather than fail on its second attempt.
	if existing, err := r.deps.PHP.Lookup(ctx, version); err == nil {
		return map[string]any{
			"version":         existing.Version,
			"already_present": true,
			"binary_path":     existing.BinaryPath,
		}, nil
	}

	if err := r.deps.PHPInstaller.Install(ctx, version, reporterFunc(reporter)); err != nil {
		return nil, phpError(err)
	}

	// The install is only real if the binary now answers. A package manager
	// that reported success while leaving nothing runnable would otherwise
	// produce a version the panel offers and no site can use.
	installed, err := r.deps.PHP.Lookup(ctx, version)
	if err != nil {
		return nil, Fail(protocol.CodeInternal,
			"the package installed but no working PHP binary was found", err)
	}

	// A freshly installed FPM is not running yet, and a pool written for it
	// would be a file nothing reads.
	if err := r.deps.PHPInstaller.StartFPM(ctx, version, r.deps.PHP); err != nil {
		r.log.Warn("php-fpm was installed but did not start",
			"version", version, "error", err.Error())
	}

	return map[string]any{
		"version":      installed.Version,
		"full_version": installed.Full,
		"binary_path":  installed.BinaryPath,
		"fpm_service":  installed.FPMService,
		"installed":    true,
	}, nil
}

// handlePHPUninstall removes a PHP version from the host.
func (r *Registry) handlePHPUninstall(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload phpVersionPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no supported package manager, so PHP cannot be removed", nil)
	}

	if err := r.deps.PHPInstaller.Remove(ctx, version, reporterFunc(reporter)); err != nil {
		return nil, phpError(err)
	}
	return map[string]any{"version": version, "removed": true}, nil
}

// phpPoolPayload describes a website's FPM pool.
type phpPoolPayload struct {
	Version      string `json:"version"`
	PoolName     string `json:"pool_name"`
	SystemUser   string `json:"system_user"`
	DocumentRoot string `json:"document_root"`

	MemoryLimit       string `json:"memory_limit"`
	UploadMaxFilesize string `json:"upload_max_filesize"`
	MaxExecutionTime  int    `json:"max_execution_time"`
	OPcacheEnabled    *bool  `json:"opcache_enabled"`
	MaxChildren       int    `json:"max_children"`
}

// handlePHPPoolCreate writes a website's FPM pool and makes it live.
func (r *Registry) handlePHPPoolCreate(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload phpPoolPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHPPools == nil || !r.deps.PHPPools.Available() {
		return nil, Fail(protocol.CodeUnsupported, "PHP-FPM is not available on this host", nil)
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}
	if payload.SystemUser == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "system_user is required", nil)
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}

	poolName := payload.PoolName
	if poolName == "" {
		poolName = validate.PHPPoolNameFor(payload.SystemUser)
	}

	settings := php.DefaultSettings()
	if payload.MemoryLimit != "" {
		settings.MemoryLimit = payload.MemoryLimit
	}
	if payload.UploadMaxFilesize != "" {
		settings.UploadMaxFilesize = payload.UploadMaxFilesize
	}
	if payload.MaxExecutionTime > 0 {
		settings.MaxExecutionTime = payload.MaxExecutionTime
	}
	if payload.OPcacheEnabled != nil {
		settings.OPcacheEnabled = *payload.OPcacheEnabled
	}

	progressReport := reporterFunc(reporter)
	report(progressReport, 15, "Preparing the pool runtime")

	account, err := r.deps.Sites.LookupAccount(payload.SystemUser)
	if err != nil {
		return nil, phpError(err)
	}

	// The site root is the document root's parent; sessions and temp files
	// live beside content rather than inside it, so neither is ever served.
	siteRoot := parentDir(payload.DocumentRoot)
	if err := r.deps.PHPPools.PrepareRuntime(siteRoot, account.UID, account.GID); err != nil {
		return nil, phpError(err)
	}

	report(progressReport, 45, "Writing the pool configuration")
	pool := php.Pool{
		Name:         poolName,
		Version:      version,
		User:         payload.SystemUser,
		Group:        payload.SystemUser,
		SocketPath:   php.SocketPathFor(poolName, version),
		ListenGroup:  r.deps.WebGroup,
		DocumentRoot: payload.DocumentRoot,
		Settings:     settings,
		MaxChildren:  payload.MaxChildren,
	}

	configPath, err := r.deps.PHPPools.WritePool(ctx, pool)
	if err != nil {
		return nil, phpError(err)
	}

	report(progressReport, 80, "Reloading PHP-FPM")
	reloaded := true
	if err := r.deps.PHPInstaller.ReloadFPM(ctx, version); err != nil {
		// The master may simply not be running yet, which starting it fixes.
		if startErr := r.deps.PHPInstaller.StartFPM(ctx, version, r.deps.PHP); startErr != nil {
			// The pool is written and valid but nothing is serving it. Saying
			// this succeeded would tell the panel a site runs PHP when every
			// request to it will fail.
			return nil, Fail(protocol.CodeInternal,
				"the pool was written but PHP-FPM is not running", errors.Join(err, startErr))
		}
	}

	report(progressReport, 100, "Pool ready")
	return map[string]any{
		"pool_name":   poolName,
		"version":     version,
		"socket_path": pool.SocketPath,
		"config_path": configPath,
		"reloaded":    reloaded,
	}, nil
}

// handlePHPPoolDelete removes a website's FPM pool.
func (r *Registry) handlePHPPoolDelete(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload phpPoolPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHPPools == nil {
		return nil, Fail(protocol.CodeUnsupported, "PHP-FPM is not available on this host", nil)
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}
	if payload.PoolName == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "pool_name is required", nil)
	}
	if err := validate.PHPPoolName(payload.PoolName); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "pool_name is not a valid pool name", err)
	}

	report(reporterFunc(reporter), 40, "Removing the pool configuration")
	if err := r.deps.PHPPools.RemovePool(ctx, version, payload.PoolName); err != nil {
		return nil, phpError(err)
	}

	// A reload is what makes the removal take effect. A failure here is worth
	// reporting but not worth failing the job: the file is gone, and the next
	// reload for any reason will pick that up.
	reloaded := true
	if err := r.deps.PHPInstaller.ReloadFPM(ctx, version); err != nil {
		reloaded = false
		r.log.Warn("pool removed but php-fpm was not reloaded",
			"version", version, "pool", payload.PoolName, "error", err.Error())
	}

	return map[string]any{
		"pool_name": payload.PoolName,
		"version":   version,
		"removed":   true,
		"reloaded":  reloaded,
	}, nil
}

// handlePHPPoolStatus reports whether a pool is configured and running.
func (r *Registry) handlePHPPoolStatus(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload phpPoolPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHPPools == nil {
		return nil, Fail(protocol.CodeUnsupported, "PHP-FPM is not available on this host", nil)
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}
	if err := validate.PHPPoolName(payload.PoolName); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "pool_name is not a valid pool name", err)
	}

	configured, err := r.deps.PHPPools.PoolExists(ctx, version, payload.PoolName)
	if err != nil {
		return nil, phpError(err)
	}

	socket := php.SocketPathFor(payload.PoolName, version)
	return map[string]any{
		"pool_name":    payload.PoolName,
		"version":      version,
		"configured":   configured,
		"socket_path":  socket,
		"socket_ready": socketReady(socket),
		"fpm_running":  php.FPMRunning(version),
	}, nil
}

// handlePHPExtensions lists the extensions a version has loaded.
func (r *Registry) handlePHPExtensions(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload phpVersionPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHP == nil {
		return nil, Fail(protocol.CodeUnsupported, "PHP management is not available on this host", nil)
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, "version must be a major.minor release such as 8.3", err)
	}

	extensions, err := r.deps.PHP.Extensions(ctx, version)
	if err != nil {
		return nil, phpError(err)
	}
	return map[string]any{
		"version":    version,
		"extensions": extensions,
		"count":      len(extensions),
	}, nil
}

// phpError maps a PHP failure to a structured protocol error.
//
// Only messages written deliberately here reach the caller; the underlying
// error travels as Cause, which is logged and never serialised.
func phpError(err error) error {
	switch {
	case errors.Is(err, php.ErrVersionNotInstalled):
		return Fail(protocol.CodeNotFound, "That PHP version is not installed on this host", err)
	case errors.Is(err, php.ErrNoPackageManager):
		return Fail(protocol.CodeUnsupported,
			"This host has no supported package manager", err)
	case errors.Is(err, php.ErrPoolUnavailable):
		return Fail(protocol.CodeUnsupported, "PHP-FPM is not available on this host", err)
	case errors.Is(err, php.ErrInvalidPool),
		errors.Is(err, validate.ErrInvalidPHPVersion),
		errors.Is(err, validate.ErrInvalidPHPSetting):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	case errors.Is(err, php.ErrInstallFailed):
		return Fail(protocol.CodeInternal, "The PHP package could not be installed", err)
	default:
		return Fail(protocol.CodeInternal, "The PHP operation failed", err)
	}
}

// report forwards a progress step when a reporter is present.
func report(fn func(int, string), percent int, message string) {
	if fn != nil {
		fn(percent, message)
	}
}

// parentDir returns a path's parent, which for a document root is the site
// root holding its logs and temp directories.
func parentDir(path string) string {
	return filepath.Dir(path)
}

// socketReady reports whether a pool's socket exists and is a socket.
//
// A pool can be configured, and FPM running, while the socket is still absent
// because the master rejected that pool. Checking the file itself is what
// tells "configured" apart from "actually serving".
func socketReady(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSocket != 0
}
