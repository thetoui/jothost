package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// websitePHPPayload describes a website whose PHP is changing.
type websitePHPPayload struct {
	WebsiteID    string   `json:"website_id"`
	Domain       string   `json:"domain"`
	Aliases      []string `json:"aliases"`
	DocumentRoot string   `json:"document_root"`
	SystemUser   string   `json:"system_user"`
	Version      string   `json:"version"`
	PoolName     string   `json:"pool_name"`
	SocketPath   string   `json:"socket_path"`
	MaxBodySize  string   `json:"max_body_size"`

	MemoryLimit       string `json:"memory_limit"`
	UploadMaxFilesize string `json:"upload_max_filesize"`
	MaxExecutionTime  int    `json:"max_execution_time"`
	OPcacheEnabled    *bool  `json:"opcache_enabled"`
	MaxChildren       int    `json:"max_children"`
}

// handleWebsitePHPSet gives a website an FPM pool and points its vhost at it.
//
// The pool is written and made live before the vhost changes. That order is
// deliberate: a vhost passing to a socket that does not exist yet returns 502
// to every visitor, whereas a pool nothing passes to is merely idle. If the
// vhost step then fails, the site keeps serving exactly as it did before.
func (r *Registry) handleWebsitePHPSet(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload websitePHPPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.PHPPools == nil || !r.deps.PHPPools.Available() {
		return nil, Fail(protocol.CodeUnsupported, "PHP-FPM is not available on this host", nil)
	}

	version := validate.NormalizePHPVersion(payload.Version)
	if err := validate.PHPVersion(version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload,
			"version must be a major.minor release such as 8.3", err)
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}
	if payload.SystemUser == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "system_user is required", nil)
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
	report(progressReport, 10, "Preparing the pool runtime")

	account, err := r.deps.Sites.LookupAccount(payload.SystemUser)
	if err != nil {
		return nil, phpError(err)
	}
	if err := r.deps.PHPPools.PrepareRuntime(parentDir(payload.DocumentRoot),
		account.UID, account.GID); err != nil {
		return nil, phpError(err)
	}

	report(progressReport, 35, "Writing the PHP-FPM pool")
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

	poolConfig, err := r.deps.PHPPools.WritePool(ctx, pool)
	if err != nil {
		return nil, phpError(err)
	}

	report(progressReport, 55, "Starting PHP-FPM")
	if err := r.startOrReload(ctx, version); err != nil {
		return nil, Fail(protocol.CodeInternal,
			"the pool was written but PHP-FPM is not running", err)
	}

	// The socket must exist before the vhost points at it, or the first
	// request after the reload is a 502.
	report(progressReport, 70, "Waiting for the pool socket")
	if err := waitForSocket(pool.SocketPath, socketWaitTimeout); err != nil {
		// The reload went to a master that is not serving after all. Starting a
		// fresh one is the recovery, and it is attempted before giving up
		// because the alternative is a site that stays static for no reason the
		// user can see.
		r.log.Warn("the pool socket did not appear after a reload; starting php-fpm",
			"version", version, "pool", poolName)

		if startErr := r.deps.PHPInstaller.StartFPM(ctx, version, r.deps.PHP); startErr != nil {
			return nil, Fail(protocol.CodeInternal,
				"PHP-FPM started but its socket never appeared", errors.Join(err, startErr))
		}
		if waitErr := waitForSocket(pool.SocketPath, socketWaitTimeout); waitErr != nil {
			return nil, Fail(protocol.CodeInternal,
				"PHP-FPM started but its socket never appeared", waitErr)
		}
	}

	report(progressReport, 85, "Pointing the website at PHP")
	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:       payload.Domain,
		Aliases:      payload.Aliases,
		DocumentRoot: payload.DocumentRoot,
		MaxBodySize:  payload.MaxBodySize,
		PHPSocket:    pool.SocketPath,
	}, nil)
	if err != nil {
		return nil, websiteError(err)
	}

	// Only now that the vhost points at the new socket are the pools for other
	// versions removed. Doing it earlier would take the site down for the
	// length of the switch; doing it not at all leaves an idle pool holding a
	// socket, which is what stops the next switch back from starting.
	report(progressReport, 95, "Removing pools for other versions")
	if stale, err := r.deps.PHPPools.RemoveOtherPools(ctx, version, poolName); err != nil {
		r.log.Warn("a pool for a previous php version could not be removed",
			"pool", poolName, "error", err.Error())
	} else {
		for _, previous := range stale {
			if err := r.deps.PHPInstaller.ReloadFPM(ctx, previous); err != nil {
				r.log.Warn("stale pool removed but php-fpm was not reloaded",
					"version", previous, "error", err.Error())
			}
		}
	}

	report(progressReport, 100, "PHP enabled")
	return map[string]any{
		"website_id":  payload.WebsiteID,
		"domain":      result.Domain,
		"version":     version,
		"pool_name":   poolName,
		"socket_path": pool.SocketPath,
		"pool_config": poolConfig,
		"config_path": result.ConfigPath,
		"reloaded":    result.Reloaded,
	}, nil
}

// handleWebsitePHPUnset turns a website back into a static site.
//
// The vhost is rewritten first, so nothing is passing to the pool by the time
// it is removed. Removing the pool first would leave a window in which every
// request for a .php file returned 502.
func (r *Registry) handleWebsitePHPUnset(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload websitePHPPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}

	progressReport := reporterFunc(reporter)
	report(progressReport, 30, "Rewriting the website as static")

	// No socket in the request, so the rendered vhost omits the PHP block.
	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:       payload.Domain,
		Aliases:      payload.Aliases,
		DocumentRoot: payload.DocumentRoot,
		MaxBodySize:  payload.MaxBodySize,
	}, nil)
	if err != nil {
		return nil, websiteError(err)
	}

	report(progressReport, 70, "Removing the PHP-FPM pool")
	version := validate.NormalizePHPVersion(payload.Version)
	removed := false

	if payload.PoolName != "" && validate.PHPVersion(version) == nil && r.deps.PHPPools != nil {
		if err := r.deps.PHPPools.RemovePool(ctx, version, payload.PoolName); err != nil {
			// The site is already static and serving. Failing the job now would
			// report an outage that did not happen; the leftover pool is idle,
			// and it is logged for an operator to clear.
			r.log.Warn("website is static but its pool could not be removed",
				"domain", payload.Domain, "pool", payload.PoolName, "error", err.Error())
		} else {
			removed = true
			if err := r.deps.PHPInstaller.ReloadFPM(ctx, version); err != nil {
				r.log.Warn("pool removed but php-fpm was not reloaded",
					"version", version, "error", err.Error())
			}
		}
	}

	report(progressReport, 100, "PHP disabled")
	return map[string]any{
		"website_id":   payload.WebsiteID,
		"domain":       result.Domain,
		"config_path":  result.ConfigPath,
		"reloaded":     result.Reloaded,
		"pool_removed": removed,
	}, nil
}

// startOrReload makes a version's FPM pick up a pool change.
func (r *Registry) startOrReload(ctx context.Context, version string) error {
	if r.deps.PHPInstaller == nil {
		return php.ErrPoolUnavailable
	}
	if php.FPMRunning(version) {
		return r.deps.PHPInstaller.ReloadFPM(ctx, version)
	}
	return r.deps.PHPInstaller.StartFPM(ctx, version, r.deps.PHP)
}

// socketWaitTimeout bounds the wait for a pool socket to appear.
//
// FPM creates it shortly after the master forks, so this is generous rather
// than long: exceeding it means the pool was rejected, not that it is slow.
const socketWaitTimeout = 10 * time.Second

// waitForSocket blocks until a pool socket exists.
func waitForSocket(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if socketReady(path) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("socket %s did not appear within %s", path, timeout)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
