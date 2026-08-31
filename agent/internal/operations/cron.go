package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/cron"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The cron operations' request boundary.
//
// Every one of them names an *account* and, where relevant, whole job records.
// The account is checked against the system user rules before it becomes a
// filename, and each job is validated again here before it becomes a line in a
// file that a daemon executes — the Agent does not assume its caller validated
// anything, because it is the last place a value can be stopped.

// cronPayload carries what the cron operations need.
type cronPayload struct {
	// Account is the website's system user. The crontab written is that
	// account's, and the job runs as it.
	Account string `json:"account"`
	// Jobs is the complete set for this account. Complete rather than
	// incremental: the Agent rewrites the panel's block from it, so a job the
	// panel deleted disappears by not being here.
	Jobs []cron.Job `json:"jobs"`
	// Job is the single job a run acts on.
	Job cron.Job `json:"job"`
	// JobID names a job whose log is being discarded.
	JobID string `json:"job_id"`
}

// cronProvider returns the provider, or the error a caller should see when this
// host cannot schedule anything.
func (r *Registry) cronProvider() (*cron.Provider, error) {
	if r.deps.Cron == nil || !r.deps.Cron.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"This host has no cron daemon, so nothing can be scheduled on it", nil)
	}
	return r.deps.Cron, nil
}

// handleCronStatus reports whether this host can schedule jobs.
func (r *Registry) handleCronStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Cron == nil {
		return map[string]any{"available": false}, nil
	}
	return r.deps.Cron.Status(ctx), nil
}

// handleCronApply rewrites one account's scheduled jobs.
func (r *Registry) handleCronApply(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.cronProvider()
	if err != nil {
		return nil, err
	}
	var payload cronPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.Apply(ctx, payload.Account, payload.Jobs)
	if err != nil {
		return nil, cronError(err)
	}
	return result, nil
}

// handleCronRemove drops the panel's block from an account's crontab.
func (r *Registry) handleCronRemove(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.cronProvider()
	if err != nil {
		return nil, err
	}
	var payload cronPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.Remove(ctx, payload.Account)
	if err != nil {
		return nil, cronError(err)
	}
	return result, nil
}

// handleCronRun runs one job now, as the account that owns it.
func (r *Registry) handleCronRun(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.cronProvider()
	if err != nil {
		return nil, err
	}
	var payload cronPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.Run(ctx, payload.Account, payload.Job)
	if err != nil {
		return nil, cronError(err)
	}
	return structToMap(result)
}

// handleCronLogRemove discards a deleted job's collected output.
func (r *Registry) handleCronLogRemove(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.cronProvider()
	if err != nil {
		return nil, err
	}
	var payload cronPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.RemoveLog(payload.JobID); err != nil {
		return nil, cronError(err)
	}
	return map[string]any{"job_id": payload.JobID, "removed": true}, nil
}

// cronError turns a package error into the code the caller should see.
func cronError(err error) error {
	switch {
	case errors.Is(err, cron.ErrUnavailable):
		return Fail(protocol.CodeUnsupported,
			"This host has no cron daemon, so nothing can be scheduled on it", err)
	case errors.Is(err, cron.ErrNoAccount):
		return Fail(protocol.CodeNotFound,
			"The account these jobs would run as does not exist on this host", err)
	case errors.Is(err, cron.ErrRootRefused):
		// Not a validation failure: a request that would have run something as
		// root is a fault in the panel, and it is refused loudly.
		return Fail(protocol.CodeInvalidRequest,
			"A scheduled job may not run as root", err)
	case errors.Is(err, validate.ErrInvalidSchedule),
		errors.Is(err, validate.ErrInvalidCommand),
		errors.Is(err, validate.ErrInvalidJobName),
		errors.Is(err, validate.ErrInvalidJobType),
		errors.Is(err, validate.ErrInvalidUUID),
		errors.Is(err, validate.ErrInvalidSystemUser):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return err
	}
}
