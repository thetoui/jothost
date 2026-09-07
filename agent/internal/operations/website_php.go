package operations

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// websitePHPPayload describes a website whose PHP is changing.
type websitePHPPayload struct {
	WebsiteID string   `json:"website_id"`
	Domain    string   `json:"domain"`
	Aliases   []string `json:"aliases"`
	// AliasRoots are the names on this site served from directories of their
	// own. Carried here because this operation rewrites the whole vhost:
	// without them, every alias with a root of its own would quietly go back
	// to the site's as a side effect of an unrelated change.
	AliasRoots   []aliasRootPayload `json:"alias_roots"`
	DocumentRoot string             `json:"document_root"`
	SystemUser   string             `json:"system_user"`
	Version      string             `json:"version"`
	// The site's certificate. These operations rewrite the whole vhost, so
	// without them switching PHP version would drop an HTTPS site back to
	// plain HTTP — a working certificate turned off by an unrelated change.
	CertificatePath string `json:"certificate_path"`
	PrivateKeyPath  string `json:"private_key_path"`
	RedirectToHTTPS bool   `json:"redirect_to_https"`
	// ProxyPort is accepted so the payload the panel builds is decodable here,
	// and refused with it set: a site is served by an application or by PHP,
	// never both. The API refuses first; this is the boundary that cannot be
	// skipped.
	ProxyPort int `json:"proxy_port"`
	// The hybrid arrangement. Carried here because these operations rewrite
	// the whole configuration: without them, choosing a PHP version would take
	// a site out of Apache and lose its .htaccess handling.
	ApachePort    int    `json:"apache_port"`
	AllowOverride bool   `json:"allow_override"`
	MaxBodyBytes  int64  `json:"max_body_bytes"`
	PoolName      string `json:"pool_name"`
	SocketPath    string `json:"socket_path"`
	MaxBodySize   string `json:"max_body_size"`

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
// sslConfig builds the vhost's certificate section from the payload.
//
// It is the same rule as the website operations use: both paths or neither,
// because nginx refuses a server block naming one without the other.
func (p websitePHPPayload) sslConfig() *nginx.SSLConfig {
	return websiteCreatePayload{
		CertificatePath: p.CertificatePath,
		PrivateKeyPath:  p.PrivateKeyPath,
		RedirectToHTTPS: p.RedirectToHTTPS,
	}.sslConfig()
}

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
	workersBefore := r.nginxWorkers()
	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:        payload.Domain,
		Aliases:       payload.Aliases,
		AliasRoots:    toAliasRoots(payload.AliasRoots),
		DocumentRoot:  payload.DocumentRoot,
		MaxBodySize:   payload.MaxBodySize,
		SSL:           payload.sslConfig(),
		PHPSocket:     pool.SocketPath,
		ApachePort:    payload.ApachePort,
		AllowOverride: payload.AllowOverride,
		MaxBodyBytes:  payload.MaxBodyBytes,
	}, nil)
	if err != nil {
		return nil, websiteError(err)
	}

	// Only now that the vhost points at the new socket are the pools for other
	// versions removed. Doing it earlier would take the site down for the
	// length of the switch; doing it not at all leaves an idle pool holding a
	// socket, which is what stops the next switch back from starting.
	//
	// The wait is what makes the switch seamless. An nginx reload is graceful:
	// the workers started under the old vhost keep accepting and serving
	// requests until their connections end, and they are still passing to the
	// old socket. Removing that pool the moment the reload returns unlinks
	// their upstream underneath them, which is a burst of 502s on a site that
	// was never actually down.
	report(progressReport, 92, "Waiting for nginx to finish serving the old configuration")
	if result.Reloaded {
		r.awaitNginxDrain(ctx, workersBefore)
	}

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
	workersBefore := r.nginxWorkers()
	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:        payload.Domain,
		Aliases:       payload.Aliases,
		AliasRoots:    toAliasRoots(payload.AliasRoots),
		DocumentRoot:  payload.DocumentRoot,
		MaxBodySize:   payload.MaxBodySize,
		SSL:           payload.sslConfig(),
		ApachePort:    payload.ApachePort,
		AllowOverride: payload.AllowOverride,
		MaxBodyBytes:  payload.MaxBodyBytes,
	}, nil)
	if err != nil {
		return nil, websiteError(err)
	}

	// The vhost no longer mentions the pool, but the workers serving under the
	// previous one still do, so the pool outlives them by design.
	if result.Reloaded {
		r.awaitNginxDrain(ctx, workersBefore)
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

// nginxWorkers records which nginx workers are serving right now, so the
// caller can tell later which ones belong to the configuration it replaced.
//
// A host where the workers cannot be observed returns nothing, which makes the
// wait a no-op rather than an error: not being able to drain gracefully is a
// reason to fall back to the previous behaviour, not to fail a switch.
func (r *Registry) nginxWorkers() []int {
	if r.deps.Nginx == nil {
		return nil
	}
	workers, err := r.deps.Nginx.WorkerPIDs()
	if err != nil {
		r.log.Warn("could not read nginx workers; a pool may be removed while they drain",
			"error", err.Error())
		return nil
	}
	return workers
}

// awaitNginxDrain waits for the workers that were serving the previous vhost.
//
// Timing out is reported but not fatal. The alternative is holding a job open
// for as long as the slowest request on the site takes, and a switch that
// never finishes is a worse failure than a handful of requests that do.
func (r *Registry) awaitNginxDrain(ctx context.Context, previous []int) {
	if r.deps.Nginx == nil || len(previous) == 0 {
		return
	}
	remaining := r.deps.Nginx.WaitForWorkers(ctx, previous, nginx.DrainTimeout)
	if len(remaining) > 0 {
		r.log.Warn("nginx workers were still serving the old configuration; removing the pool anyway",
			"workers", len(remaining), "waited", nginx.DrainTimeout.String())
	}
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
