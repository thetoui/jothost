package operations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/pma"
	"github.com/jothost/panel/shared/protocol"
)

// phpMyAdminRequest is the payload the install operation takes.
type phpMyAdminRequest struct {
	// ServerName is the host phpMyAdmin will answer on. There is no default:
	// serving a database console on a name nobody chose is how one ends up
	// reachable by accident.
	ServerName string `json:"server_name"`
}

// handlePHPMyAdminStatus reports whether phpMyAdmin is installed and served.
func (r *Registry) handlePHPMyAdminStatus(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.PHPMyAdmin == nil {
		return map[string]any{
			"installed":   false,
			"served":      false,
			"can_install": false,
			"detail":      "phpMyAdmin management is not configured on this host",
		}, nil
	}

	status := r.deps.PHPMyAdmin.Status(ctx)
	return map[string]any{
		"installed":   status.Installed,
		"served":      status.Served,
		"webroot":     status.Webroot,
		"server_name": status.ServerName,
		"url":         status.URL,
		"php_version": status.PHPVersion,
		"can_install": status.CanInstall,
		"detail":      status.Detail,
	}, nil
}

// handlePHPMyAdminInstall installs phpMyAdmin and publishes it.
//
// This is the long one: a package install, an account, a pool, and a vhost.
// The API submits it as a background job for that reason.
func (r *Registry) handlePHPMyAdminInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.PHPMyAdmin == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"phpMyAdmin management is not configured on this host", nil)
	}

	request, err := decodePHPMyAdmin(req)
	if err != nil {
		return nil, err
	}

	status, err := r.deps.PHPMyAdmin.Install(ctx, request.ServerName, reporterFunc(reporter))
	if err != nil {
		return nil, phpMyAdminError("phpMyAdmin could not be installed", err)
	}

	return map[string]any{
		"installed":   status.Installed,
		"served":      status.Served,
		"server_name": status.ServerName,
		"url":         status.URL,
		"php_version": status.PHPVersion,
	}, nil
}

// handlePHPMyAdminUninstall removes phpMyAdmin from the host.
func (r *Registry) handlePHPMyAdminUninstall(ctx context.Context, _ protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.PHPMyAdmin == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"phpMyAdmin management is not configured on this host", nil)
	}

	if err := r.deps.PHPMyAdmin.Uninstall(ctx, reporterFunc(reporter)); err != nil {
		return nil, phpMyAdminError("phpMyAdmin could not be removed", err)
	}
	return map[string]any{"removed": true}, nil
}

func decodePHPMyAdmin(req protocol.Request) (phpMyAdminRequest, error) {
	var decoded phpMyAdminRequest
	if req.Payload == nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "A payload is required", nil)
	}

	encoded, err := json.Marshal(req.Payload)
	if err != nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "The payload could not be read", err)
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return decoded, Fail(protocol.CodeInvalidPayload, "The payload is not valid", err)
	}
	return decoded, nil
}

// phpMyAdminError converts a manager failure into a structured refusal.
func phpMyAdminError(summary string, err error) error {
	switch {
	case errors.Is(err, pma.ErrUnsupported), errors.Is(err, pma.ErrNoPHP):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, pma.ErrInvalidServerName):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	case errors.Is(err, pma.ErrNotInstalled):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, summary, err)
	}
}
