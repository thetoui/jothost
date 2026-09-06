package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/ssl"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// websiteCreatePayload describes a website to provision.
type websiteCreatePayload struct {
	// WebsiteID is the panel's id for the site. The Agent does not act on it —
	// it works in domains and paths — but it is accepted and logged, because
	// every other vhost-writing operation carries it and rejecting it here
	// would make the panel's one payload builder unusable for this operation.
	WebsiteID    string   `json:"website_id"`
	Domain       string   `json:"domain"`
	Aliases      []string `json:"aliases"`
	DocumentRoot string   `json:"document_root"`
	SystemUser   string   `json:"system_user"`
	MaxBodySize  string   `json:"max_body_size"`
	// ProxyPort points the vhost at an application on 127.0.0.1 instead of at
	// files. Zero serves files, which is how a Node.js site becomes static
	// again.
	ProxyPort int `json:"proxy_port"`
	// PHPSocket is passed through by the PHP operations; a website operation
	// carrying both it and a proxy port is refused by the renderer, because a
	// site is served by one thing or the other.
	PHPSocket string `json:"php_socket"`

	// The site's certificate, carried by every operation that rewrites the
	// vhost rather than only by the certificate operations.
	//
	// The Agent renders the whole file from this payload, so a rewrite that
	// omitted these would drop the site back to plain HTTP as a side effect of
	// adding an alias or switching PHP version — a working HTTPS site turned
	// off by an unrelated change, with nothing in the panel saying so.
	CertificatePath string `json:"certificate_path"`
	PrivateKeyPath  string `json:"private_key_path"`
	RedirectToHTTPS bool   `json:"redirect_to_https"`

	// ApachePort puts Apache in front of this site's files, with nginx
	// proxying to it — the hybrid arrangement. Zero is nginx serving the site
	// itself, which is the default and what every earlier phase did.
	ApachePort int `json:"apache_port"`
	// AllowOverride enables .htaccess, which only means anything with Apache
	// in the path.
	AllowOverride bool `json:"allow_override"`
	// MaxBodyBytes caps uploads at the Apache layer, in bytes. nginx has its
	// own limit expressed the way nginx expresses it.
	MaxBodyBytes int64 `json:"max_body_bytes"`

	// Directives is the operator's own nginx configuration for this site.
	Directives string `json:"nginx_directives"`
}

// sslConfig builds the vhost's certificate section from the payload.
//
// Both paths are required: nginx needs the key to serve the certificate, and a
// server block naming one without the other is refused at validation — which
// on a reload means the whole host keeps the previous configuration.
func (p websiteCreatePayload) sslConfig() *nginx.SSLConfig {
	if p.CertificatePath == "" || p.PrivateKeyPath == "" {
		return nil
	}
	return &nginx.SSLConfig{
		CertificatePath: p.CertificatePath,
		PrivateKeyPath:  p.PrivateKeyPath,
		RedirectToHTTPS: p.RedirectToHTTPS,
		// The challenge path stays open on every HTTPS site: a renewal
		// arrives over plain HTTP, and a site that redirects it to HTTPS
		// cannot be renewed at all.
		ChallengeRoot: ssl.ACMEChallengeDir,
	}
}

func (r *Registry) handleWebsiteCreate(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload websiteCreatePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
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
	// Validated here as well as in the API. The Agent is the thing that writes
	// the file, and it does not get to assume the only caller is a well-behaved
	// panel: anything that can reach the socket can send this payload.
	if err := validate.NginxDirectives(payload.Directives); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload,
			"those additional nginx directives were refused", err)
	}

	result, err := r.deps.Sites.Create(ctx, sites.CreateRequest{
		Domain:        payload.Domain,
		Aliases:       payload.Aliases,
		DocumentRoot:  payload.DocumentRoot,
		SystemUser:    payload.SystemUser,
		MaxBodySize:   payload.MaxBodySize,
		PHPSocket:     payload.PHPSocket,
		ProxyPort:     payload.ProxyPort,
		SSL:           payload.sslConfig(),
		ApachePort:    payload.ApachePort,
		AllowOverride: payload.AllowOverride,
		MaxBodyBytes:  payload.MaxBodyBytes,
		Directives:    payload.Directives,
	}, reporterFunc(reporter))
	if err != nil {
		return nil, websiteError(err)
	}
	return structToMap(result)
}

// websiteDeletePayload describes a website to remove.
type websiteDeletePayload struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user"`
	RemoveFiles  bool   `json:"remove_files"`
	RemoveUser   bool   `json:"remove_user"`
}

func (r *Registry) handleWebsiteDelete(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload websiteDeletePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}
	// Removing files or an account without knowing which would be a guess with
	// unbounded consequences, so both require their subject.
	if payload.RemoveFiles && payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload,
			"document_root is required when remove_files is set", nil)
	}
	if payload.RemoveUser && payload.SystemUser == "" {
		return nil, Fail(protocol.CodeInvalidPayload,
			"system_user is required when remove_user is set", nil)
	}
	// Removing an account frees its uid for reuse, so the Agent has to be able
	// to find whatever still carries that uid before it does. Without a
	// document root it cannot, and the files would be left pointing at a number
	// the system is about to hand to somebody else.
	if payload.RemoveUser && payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload,
			"document_root is required when remove_user is set, "+
				"so the account's files can be reassigned before its uid is freed", nil)
	}

	// The site's FPM pools go first, while its account still exists.
	//
	// A pool left behind names a user that is about to be deleted, and FPM
	// validates its whole pool directory at once: from then on *every* site on
	// that PHP version fails to configure, because one orphaned file refers to
	// an account that is gone. Deleting one website would quietly break PHP for
	// all the others.
	poolsRemoved := r.removeSitePools(ctx, payload.SystemUser)

	// The site's scheduled jobs go too, while its account still exists.
	//
	// The rows in the panel's database go with the website through a foreign
	// key, but the crontab is a file on the host, and a file nothing deletes is
	// a set of jobs that keeps running as an account nobody owns — until the
	// account is removed a moment later, at which point cron starts failing
	// every minute against a user that is gone.
	cronRemoved := r.removeSiteJobs(ctx, payload.SystemUser)

	// The certificate goes with the site too. A private key left behind is a
	// key nobody owns, for a name nothing serves, and it would be picked up
	// again by a later site that happened to reuse the domain.
	certificateRemoved := r.removeSiteCertificate(payload.Domain)

	result, err := r.deps.Sites.Delete(ctx, sites.DeleteRequest{
		Domain:       payload.Domain,
		DocumentRoot: payload.DocumentRoot,
		SystemUser:   payload.SystemUser,
		RemoveFiles:  payload.RemoveFiles,
		RemoveUser:   payload.RemoveUser,
	}, reporterFunc(reporter))
	if err != nil {
		return nil, websiteError(err)
	}

	data, err := structToMap(result)
	if err != nil {
		return nil, err
	}
	data["pools_removed"] = poolsRemoved
	data["certificate_removed"] = certificateRemoved
	data["cron_removed"] = cronRemoved
	return data, nil
}

// removeSiteJobs drops an account's scheduled jobs from the host.
//
// Logged rather than fatal, for the same reason as the pools and the
// certificate: refusing to delete a website because a crontab would not unlink
// leaves the operator with a site they cannot get rid of.
func (r *Registry) removeSiteJobs(ctx context.Context, systemUser string) bool {
	if systemUser == "" || r.deps.Cron == nil || !r.deps.Cron.Available() {
		return false
	}

	if _, err := r.deps.Cron.Remove(ctx, systemUser); err != nil {
		r.log.Warn("the scheduled jobs could not be removed while deleting a website",
			"account", systemUser, "error", err.Error())
		return false
	}
	return true
}

// removeSiteCertificate deletes a site's certificate material.
//
// The certificate is not revoked, only removed: revocation is a statement to a
// certificate authority that the key was compromised, which deleting a website
// is not. It is logged rather than fatal, because refusing to delete a website
// over a leftover file would leave the user with a site they cannot remove.
func (r *Registry) removeSiteCertificate(domain string) bool {
	if domain == "" || r.deps.SSL == nil {
		return false
	}

	// A wildcard site has no certificate of its own: issuing one needs a DNS
	// challenge, which this panel does not do. Asking anyway produces a
	// warning about an invalid certificate domain on every wildcard deletion,
	// which is noise that would train an operator to ignore the log line.
	if validate.IsWildcard(domain) {
		return false
	}

	if err := r.deps.SSL.Remove(domain); err != nil {
		r.log.Warn("website deleted but its certificate could not be removed",
			"domain", domain, "error", err.Error())
		return false
	}
	return true
}

// removeSitePools deletes a site's FPM pool from every installed version.
//
// Failures are logged rather than returned: the website is being deleted, and
// refusing to remove it because a pool file would not unlink would leave the
// user with a site they cannot get rid of. The versions it touched are
// reloaded so the removal takes effect.
func (r *Registry) removeSitePools(ctx context.Context, systemUser string) []string {
	if systemUser == "" || r.deps.PHPPools == nil || !r.deps.PHPPools.Available() {
		return nil
	}

	poolName := validate.PHPPoolNameFor(systemUser)
	if err := validate.PHPPoolName(poolName); err != nil {
		return nil
	}

	// An empty keepVersion removes the pool from every version.
	removed, err := r.deps.PHPPools.RemoveOtherPools(ctx, "", poolName)
	if err != nil {
		r.log.Warn("a pool could not be removed while deleting a website",
			"pool", poolName, "error", err.Error())
	}

	for _, version := range removed {
		if err := r.deps.PHPInstaller.ReloadFPM(ctx, version); err != nil {
			r.log.Warn("pool removed but php-fpm was not reloaded",
				"version", version, "error", err.Error())
		}
	}
	return removed
}

// handleWebsiteUpdate rewrites a website's configuration.
//
// Update is create without the account and directory steps: the vhost is
// regenerated from the new values, validated, and reloaded. Reusing Create
// would be wrong — it would re-own directories a deployment may have
// deliberately re-permissioned.
func (r *Registry) handleWebsiteUpdate(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error) {
	var payload websiteCreatePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}
	// Checked on the update as well as the create. The rewrite path carries the
	// directives on every vhost change, so this is the one an attacker would
	// reach for: a create is a new site, an update is every other change to an
	// existing one.
	if err := validate.NginxDirectives(payload.Directives); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload,
			"those additional nginx directives were refused", err)
	}

	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:        payload.Domain,
		Aliases:       payload.Aliases,
		DocumentRoot:  payload.DocumentRoot,
		MaxBodySize:   payload.MaxBodySize,
		PHPSocket:     payload.PHPSocket,
		ProxyPort:     payload.ProxyPort,
		SSL:           payload.sslConfig(),
		ApachePort:    payload.ApachePort,
		AllowOverride: payload.AllowOverride,
		MaxBodyBytes:  payload.MaxBodyBytes,
		Directives:    payload.Directives,
	}, reporterFunc(reporter))
	if err != nil {
		return nil, websiteError(err)
	}
	return structToMap(result)
}

// websiteStatusPayload identifies a website to inspect.
type websiteStatusPayload struct {
	Domain       string `json:"domain"`
	DocumentRoot string `json:"document_root"`
	SystemUser   string `json:"system_user"`
}

func (r *Registry) handleWebsiteStatus(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload websiteStatusPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Domain == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "domain is required", nil)
	}

	status, err := r.deps.Sites.StatusOf(ctx, payload.Domain, payload.DocumentRoot, payload.SystemUser)
	if err != nil {
		return nil, websiteError(err)
	}
	return structToMap(status)
}

// websiteLogsPayload selects a site's log to read.
type websiteLogsPayload struct {
	DocumentRoot string `json:"document_root"`
	Kind         string `json:"kind"`
	Lines        int    `json:"lines"`
}

func (r *Registry) handleWebsiteLogs(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload websiteLogsPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.DocumentRoot == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "document_root is required", nil)
	}
	if payload.Lines < 0 {
		return nil, Fail(protocol.CodeInvalidPayload, "lines must not be negative", nil)
	}

	kind := sites.LogKind(payload.Kind)
	switch kind {
	case "":
		kind = sites.LogAccess
	case sites.LogAccess, sites.LogError:
	default:
		return nil, Fail(protocol.CodeInvalidPayload, `kind must be "access" or "error"`, nil)
	}

	lines, err := r.deps.Sites.ReadLog(payload.DocumentRoot, kind, payload.Lines)
	if err != nil {
		return nil, websiteError(err)
	}

	return map[string]any{
		"kind":  string(kind),
		"lines": lines,
		"count": len(lines),
	}, nil
}

func (r *Registry) handleNginxValidate(ctx context.Context, _ protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	if err := r.deps.Nginx.Validate(ctx); err != nil {
		if errors.Is(err, nginx.ErrInvalidConfig) {
			// The configuration is wrong, which is an answer rather than a
			// failure of the operation: the caller asked whether it is valid.
			return map[string]any{"valid": false, "detail": err.Error()}, nil
		}
		return nil, websiteError(err)
	}
	return map[string]any{"valid": true}, nil
}

func (r *Registry) handleNginxReload(ctx context.Context, _ protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	if err := r.deps.Nginx.Reload(ctx); err != nil {
		return nil, websiteError(err)
	}
	return map[string]any{"reloaded": true}, nil
}

// reporterFunc adapts a job reporter to the sites package's callback.
//
// A nil reporter means synchronous execution, where there is nobody to report
// progress to.
func reporterFunc(reporter *jobs.Reporter) func(int, string) {
	if reporter == nil {
		return nil
	}
	return reporter.Report
}

// websiteError maps a provisioning failure to a structured response.
func websiteError(err error) error {
	switch {
	case errors.Is(err, sites.ErrUnsupported), errors.Is(err, nginx.ErrUnavailable),
		errors.Is(err, sites.ErrNoUserTool):
		return Fail(protocol.CodeUnsupported, "This host cannot manage websites", err)
	case errors.Is(err, nginx.ErrInvalidConfig):
		return Fail(protocol.CodeInvalidRequest,
			"The generated web server configuration was rejected", err)
	case errors.Is(err, sites.ErrOutsideRoot):
		return Fail(protocol.CodeInvalidPayload,
			"The document root is outside the permitted directory", err)
	case errors.Is(err, sites.ErrOccupied):
		// Worth its own message rather than falling through to the default,
		// because the operator has to do something specific about it and the
		// wrapped error names the directory and its owner.
		return Fail(protocol.CodeInvalidRequest,
			"That directory already holds files belonging to another account. "+
				"A deleted website keeps its files, so recreating one on the same "+
				"document root needs the old files removed or moved first", err)
	case errors.Is(err, validate.ErrInvalidDomain):
		return Fail(protocol.CodeInvalidPayload, "That is not a valid domain name", err)
	case errors.Is(err, validate.ErrInvalidSystemUser):
		return Fail(protocol.CodeInvalidPayload, "That is not a valid system user name", err)
	case errors.Is(err, validate.ErrInvalidPath):
		return Fail(protocol.CodeInvalidPayload, "That is not a valid path", err)
	default:
		return err
	}
}
