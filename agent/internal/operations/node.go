package operations

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nodejs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// isValidationError reports whether a failure came from shared/validate.
//
// Those messages name the field and the rule, so they are worth showing; a
// generic internal error would tell the caller nothing about a value they
// supplied and can fix.
func isValidationError(err error) bool {
	for _, sentinel := range []error{
		validate.ErrInvalidNodeVersion, validate.ErrInvalidAppName,
		validate.ErrInvalidPort, validate.ErrInvalidEnvKey,
		validate.ErrInvalidStartupFile, validate.ErrInvalidPath,
		validate.ErrInvalidSystemUser, validate.ErrInvalidDomain,
	} {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}

// nodeRequest is the shape every Node.js operation decodes into.
//
// One struct across all of them keeps the field names identical, and unknown
// fields are refused rather than ignored: a misspelled "startup_file" that was
// silently dropped would start the wrong entry point.
type nodeRequest struct {
	// Package names a release line to install, from the Agent's own table.
	Package string `json:"package"`

	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Root        string            `json:"root"`
	Startup     string            `json:"startup_file"`
	Port        int               `json:"port"`
	User        string            `json:"user"`
	Group       string            `json:"group"`
	Environment map[string]string `json:"environment"`

	// Lines bounds a log request.
	Lines int `json:"lines"`
}

// app builds the application description from a request.
func (r nodeRequest) app() nodejs.App {
	return nodejs.App{
		Name:        r.Name,
		Version:     r.Version,
		Root:        r.Root,
		Startup:     r.Startup,
		Port:        r.Port,
		User:        r.User,
		Group:       r.Group,
		Environment: r.Environment,
	}
}

func decodeNode(req protocol.Request) (nodeRequest, error) {
	var decoded nodeRequest
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

// nodeManager returns the manager, or explains why there is none.
func (r *Registry) nodeManager() (*nodejs.Manager, error) {
	if r.deps.Node == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Node.js management is not configured on this host", nil)
	}
	return r.deps.Node, nil
}

// handleNodeVersions reports the runtimes installed and what could be added.
func (r *Registry) handleNodeVersions(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Node == nil {
		return map[string]any{
			"versions": []any{}, "count": 0, "available": false,
			"offers": []any{}, "can_install": false,
		}, nil
	}
	return r.deps.Node.Versions(ctx), nil
}

// handleNodeInstall adds a Node.js release line to the host.
func (r *Registry) handleNodeInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	version, err := manager.Install(ctx, request.Package, reporterFunc(reporter))
	if err != nil {
		return nil, nodeError("Node.js could not be installed", err)
	}

	return map[string]any{
		"version":      version.Version,
		"full_version": version.Full,
		"binary_path":  version.BinaryPath,
		"npm_version":  version.NPMVersion,
		"installed":    true,
	}, nil
}

// handleNodeUninstall removes a Node.js release line.
func (r *Registry) handleNodeUninstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	if err := manager.Uninstall(ctx, request.Package, reporterFunc(reporter)); err != nil {
		return nil, nodeError("Node.js could not be removed", err)
	}
	return map[string]any{"package": request.Package, "removed": true}, nil
}

// handleNodeAppDeploy makes an application ready to run.
func (r *Registry) handleNodeAppDeploy(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	app := request.app()
	report(reporterFunc(reporter), 30, "Preparing "+app.Name)
	if err := manager.Deploy(ctx, app); err != nil {
		return nil, nodeError("The application could not be prepared", err)
	}

	report(reporterFunc(reporter), 100, app.Name+" is ready to start")
	return map[string]any{
		"name":       app.Name,
		"unit":       app.UnitName(),
		"managed_by": manager.Runtime(),
		"deployed":   true,
	}, nil
}

// handleNodeAppRemove takes an application's runtime state off the host.
func (r *Registry) handleNodeAppRemove(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	app := request.app()
	report(reporterFunc(reporter), 40, "Stopping "+app.Name)
	if err := manager.Remove(ctx, app); err != nil {
		return nil, nodeError("The application could not be removed", err)
	}

	report(reporterFunc(reporter), 100, app.Name+" is gone")
	return map[string]any{"name": app.Name, "removed": true}, nil
}

// handleNodeAppStart runs an application.
func (r *Registry) handleNodeAppStart(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	return r.nodeLifecycle(ctx, req, reporter, "Starting", "started",
		func(manager *nodejs.Manager, app nodejs.App) (nodejs.Status, error) {
			return manager.Start(ctx, app)
		})
}

// handleNodeAppStop ends an application.
func (r *Registry) handleNodeAppStop(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	return r.nodeLifecycle(ctx, req, reporter, "Stopping", "stopped",
		func(manager *nodejs.Manager, app nodejs.App) (nodejs.Status, error) {
			return manager.Stop(ctx, app)
		})
}

// handleNodeAppRestart restarts an application.
func (r *Registry) handleNodeAppRestart(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	return r.nodeLifecycle(ctx, req, reporter, "Restarting", "restarted",
		func(manager *nodejs.Manager, app nodejs.App) (nodejs.Status, error) {
			return manager.Restart(ctx, app)
		})
}

// nodeLifecycle is the shared shape of start, stop, and restart.
func (r *Registry) nodeLifecycle(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter, verb, past string,
	action func(*nodejs.Manager, nodejs.App) (nodejs.Status, error),
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	app := request.app()
	report(reporterFunc(reporter), 40, verb+" "+app.Name)

	status, err := action(manager, app)
	if err != nil {
		return nil, nodeError("The application could not be "+past, err)
	}

	report(reporterFunc(reporter), 100, app.Name+" is "+string(status.State))
	return nodeStatus(status), nil
}

// handleNodeAppStatus reports what an application is doing.
func (r *Registry) handleNodeAppStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	app := request.app()
	status := manager.Status(ctx, app)

	// The environment is read back from the file rather than echoed from the
	// request, so the panel shows what the process will actually see — even if
	// somebody edited the file by hand.
	environment, err := nodejs.ReadEnvFile(app)
	if err != nil {
		environment = map[string]string{}
	}

	result := nodeStatus(status)
	result["environment"] = environment
	return result, nil
}

// handleNodeAppLogs returns the tail of an application's output.
func (r *Registry) handleNodeAppLogs(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	logs, err := manager.Logs(request.app(), request.Lines)
	if err != nil {
		return nil, nodeError("The logs could not be read", err)
	}
	return logs, nil
}

// handleNodeAppInstall installs an application's dependencies.
func (r *Registry) handleNodeAppInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	manager, err := r.nodeManager()
	if err != nil {
		return nil, err
	}
	request, err := decodeNode(req)
	if err != nil {
		return nil, err
	}

	app := request.app()
	if err := manager.InstallDependencies(ctx, app, reporterFunc(reporter)); err != nil {
		return nil, nodeError("The dependencies could not be installed", err)
	}
	return map[string]any{"name": app.Name, "installed": true}, nil
}

// nodeStatus renders a runtime status for the protocol.
func nodeStatus(status nodejs.Status) map[string]any {
	return map[string]any{
		"name":           status.Name,
		"state":          string(status.State),
		"pid":            status.PID,
		"port":           status.Port,
		"uptime_seconds": status.Uptime,
		"managed_by":     status.Managed,
		"listening":      status.Listening,
		"detail":         status.Detail,
	}
}

// nodeError converts a manager failure into a structured refusal.
func nodeError(summary string, err error) error {
	switch {
	case errors.Is(err, nodejs.ErrUnsupported),
		errors.Is(err, nodejs.ErrNoPackageManager),
		errors.Is(err, nodejs.ErrVersionNotInstalled),
		errors.Is(err, nodejs.ErrVersionUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, nodejs.ErrAppNotFound):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, nodejs.ErrStartupMissing), errors.Is(err, nodejs.ErrPortInUse):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		// A validation failure from shared/validate is the caller's mistake,
		// and its message names the field, so it is passed through.
		if isValidationError(err) {
			return Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		return Fail(protocol.CodeInternal, summary, err)
	}
}
