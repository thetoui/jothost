package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// websiteCreatePayload describes a website to provision.
type websiteCreatePayload struct {
	Domain       string   `json:"domain"`
	Aliases      []string `json:"aliases"`
	DocumentRoot string   `json:"document_root"`
	SystemUser   string   `json:"system_user"`
	MaxBodySize  string   `json:"max_body_size"`
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

	result, err := r.deps.Sites.Create(ctx, sites.CreateRequest{
		Domain:       payload.Domain,
		Aliases:      payload.Aliases,
		DocumentRoot: payload.DocumentRoot,
		SystemUser:   payload.SystemUser,
		MaxBodySize:  payload.MaxBodySize,
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

	// The site's FPM pools go first, while its account still exists.
	//
	// A pool left behind names a user that is about to be deleted, and FPM
	// validates its whole pool directory at once: from then on *every* site on
	// that PHP version fails to configure, because one orphaned file refers to
	// an account that is gone. Deleting one website would quietly break PHP for
	// all the others.
	poolsRemoved := r.removeSitePools(ctx, payload.SystemUser)

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
	return data, nil
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

	result, err := r.deps.Sites.Update(ctx, sites.UpdateRequest{
		Domain:       payload.Domain,
		Aliases:      payload.Aliases,
		DocumentRoot: payload.DocumentRoot,
		MaxBodySize:  payload.MaxBodySize,
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
