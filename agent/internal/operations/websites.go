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
	return structToMap(result)
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
