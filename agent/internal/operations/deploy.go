package operations

import (
	"context"
	"errors"
	"fmt"

	"github.com/jothost/panel/agent/internal/deploy"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The deployment operations' request boundary.
//
// A request carries what the panel recorded: a remote that has been through
// validate.GitRemote, a branch through validate.GitBranch, typed actions from a
// closed set, and the website's own account and document root — both produced
// by a website operation rather than sent by a caller.
//
// It carries no command line. Every action but one becomes an argv the deploy
// package builds; the exception is the deployment script, which is text the
// panel stored, written to a file owned by the site account and run as
// `sh <file>` — never as `sh -c <text>`, because the process table on a Linux
// host is world-readable and a deployment script is exactly the kind of text
// that holds a token.
//
// The document root is the one path that arrives from outside. It is checked to
// be absolute here, and git is run inside it as the site account — which is
// itself a check, because git refuses to operate on a repository owned by
// somebody else.

// deployPayload carries a deployment.
type deployPayload struct {
	Account      string `json:"account"`
	DocumentRoot string `json:"document_root"`
	Remote       string `json:"remote"`
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`

	Actions []struct {
		Kind string `json:"kind"`
	} `json:"actions"`

	Script               string `json:"script"`
	ScriptTimeoutSeconds int    `json:"script_timeout_seconds"`
	UseDeployKey         bool   `json:"use_deploy_key"`
	RollbackOnFailure    bool   `json:"rollback_on_failure"`
}

// request converts the payload into the provider's own type.
func (p deployPayload) request() (deploy.Request, error) {
	if p.DocumentRoot == "" || p.DocumentRoot[0] != '/' {
		return deploy.Request{}, Fail(protocol.CodeInvalidPayload,
			"a deployment needs the website's document root, as an absolute path", nil)
	}

	actions := make([]deploy.Action, 0, len(p.Actions))
	for _, action := range p.Actions {
		if err := validate.DeployAction(action.Kind); err != nil {
			return deploy.Request{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		actions = append(actions, deploy.Action{Kind: action.Kind})
	}

	timeout := p.ScriptTimeoutSeconds
	if timeout == 0 {
		timeout = 600
	}

	return deploy.Request{
		Account:              p.Account,
		DocumentRoot:         p.DocumentRoot,
		Remote:               p.Remote,
		Branch:               p.Branch,
		Commit:               p.Commit,
		Actions:              actions,
		Script:               p.Script,
		ScriptTimeoutSeconds: timeout,
		UseDeployKey:         p.UseDeployKey,
		RollbackOnFailure:    p.RollbackOnFailure,
	}, nil
}

// deployProvider returns the provider, or the error a caller should see when
// this host cannot deploy.
func (r *Registry) deployProvider() (*deploy.Provider, error) {
	if r.deps.Deploy == nil || !r.deps.Deploy.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"git is not installed on this host, so nothing can be deployed", nil)
	}
	return r.deps.Deploy, nil
}

// handleDeployStatus reports what this host has for one website.
func (r *Registry) handleDeployStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct {
		Account      string `json:"account"`
		DocumentRoot string `json:"document_root"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Deploy == nil {
		return structToMap(deploy.Status{Reason: deploy.ErrUnavailable.Error()})
	}
	return structToMap(r.deps.Deploy.Status(ctx, payload.Account, payload.DocumentRoot))
}

// handleDeployKeyGenerate creates a deploy key for one website.
//
// The private half is written to this host and is not in the reply. The reply
// carries the public half, which is what somebody pastes into a forge — see
// docs/PHASE27.md on why the two halves live in different places.
func (r *Registry) handleDeployKeyGenerate(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.deployProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Account string `json:"account"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	key, err := provider.GenerateKey(ctx, payload.Account)
	if err != nil {
		return nil, deployError(err)
	}
	return structToMap(key)
}

// handleDeployKeyRemove deletes a website's deploy key.
func (r *Registry) handleDeployKeyRemove(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.deployProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Account string `json:"account"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := provider.RemoveKey(payload.Account); err != nil {
		return nil, deployError(err)
	}
	return map[string]any{"removed": true}, nil
}

// handleDeployRun deploys a website.
//
// Asynchronous by nature: a clone, a dependency install and a build together
// take minutes, which is well past what a request should hold open. The job's
// progress is what the panel follows while it runs.
func (r *Registry) handleDeployRun(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.deployProvider()
	if err != nil {
		return nil, err
	}
	var payload deployPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	request, err := payload.request()
	if err != nil {
		return nil, err
	}

	result, runErr := provider.Deploy(ctx, request, reporterFunc(reporter))
	data, err := structToMap(result)
	if err != nil {
		return nil, err
	}
	if runErr == nil {
		return data, nil
	}

	// A build that failed is an *outcome*, not a failure of this operation.
	//
	// That is the same distinction cron draws about a job that exits non-zero,
	// and here it decides whether anybody ever sees the log: an operation that
	// returns an error returns no data with it, so the panel would record the
	// deployment as failed and have nothing to show — no log, no failed step,
	// no record that the source was rolled back. Every one of those is in the
	// result, and the result is the only thing that says why.
	//
	// Everything else — a repository that could not be reached, an account
	// that resolves to root, a step naming a tool this host does not have — is
	// a failure of the operation and is returned as one.
	if errors.Is(runErr, deploy.ErrActionFailed) {
		data["error"] = runErr.Error()
		return data, nil
	}
	data["error"] = runErr.Error()
	return data, deployError(runErr)
}

// handleDeployUnlink disconnects a website from its repository.
//
// The files stay. Unlinking should stop a site being deployed, not take it
// offline, and a panel that deleted a customer's document root as a side effect
// would be doing something nobody asked for.
func (r *Registry) handleDeployUnlink(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.deployProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Account      string `json:"account"`
		DocumentRoot string `json:"document_root"`
		RemoveKey    bool   `json:"remove_key"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.Unlink(payload.Account, payload.DocumentRoot, payload.RemoveKey); err != nil {
		return nil, deployError(err)
	}
	return map[string]any{"unlinked": true}, nil
}

// deployError maps this package's errors onto protocol codes.
//
// ErrRootRefused is not collapsed into a generic failure. Every other error
// here is something that did not work; that one is the panel refusing to run a
// customer's build as root, and an operator seeing it needs to know that is
// what happened rather than that a deployment failed.
func deployError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, deploy.ErrUnavailable), errors.Is(err, deploy.ErrActionUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, deploy.ErrRootRefused):
		return Fail(protocol.CodeInvalidRequest,
			"this website's account resolves to root, and a deployment will not run as root: "+
				err.Error(), err)
	case errors.Is(err, deploy.ErrNoAccount), errors.Is(err, deploy.ErrNotCloned):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, deploy.ErrRemoteFailed), errors.Is(err, deploy.ErrDirty):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, deploy.ErrActionFailed):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, validate.ErrInvalidRemote),
		errors.Is(err, validate.ErrInvalidBranch),
		errors.Is(err, validate.ErrInvalidAction),
		errors.Is(err, validate.ErrInvalidScript):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, fmt.Sprintf("%v", err), err)
	}
}
