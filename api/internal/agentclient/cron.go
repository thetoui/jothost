package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The cron operations.
//
// Everything here names a system *account* and whole job records. There is no
// operation that appends to a crontab: the panel hands over the complete set
// for an account and the Agent rewrites its block from it, so the file cannot
// come to say something the panel's records do not.

// CronJob is one job as the Agent needs it.
type CronJob struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Command  string `json:"command"`
	Enabled  bool   `json:"enabled"`
}

// CronStatus describes the host's scheduling arrangement.
type CronStatus struct {
	Available bool   `json:"available"`
	SpoolDir  string `json:"spool_dir"`
	LogDir    string `json:"log_dir"`
}

// CronApplyResult reports what was written.
type CronApplyResult struct {
	Account string `json:"account"`
	Path    string `json:"path"`
	Jobs    int    `json:"jobs"`
	Skipped int    `json:"skipped"`
}

// CronRunResult is what one manual run did.
type CronRunResult struct {
	JobID      string `json:"job_id"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	Truncated  bool   `json:"truncated"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	// Status is "success" or "failed".
	Status string `json:"status"`
}

// CronStatusOf asks whether this host can schedule anything.
func (c *Client) CronStatusOf(ctx context.Context, requestID string) (CronStatus, error) {
	var result CronStatus
	err := c.call(ctx, requestID, protocol.OperationCronStatus, map[string]any{}, &result)
	return result, err
}

// CronApply writes one account's complete set of jobs.
func (c *Client) CronApply(ctx context.Context, requestID, account string, jobs []CronJob) error {
	if jobs == nil {
		jobs = []CronJob{}
	}
	var result CronApplyResult
	return c.call(ctx, requestID, protocol.OperationCronApply, map[string]any{
		"account": account,
		"jobs":    jobs,
	}, &result)
}

// CronRemove drops the panel's block from an account's crontab.
func (c *Client) CronRemove(ctx context.Context, requestID, account string) error {
	var result CronApplyResult
	return c.call(ctx, requestID, protocol.OperationCronRemove, map[string]any{
		"account": account,
	}, &result)
}

// CronRun executes one job now, as the account that owns it.
func (c *Client) CronRun(ctx context.Context, requestID, account string, job CronJob) (CronRunResult, error) {
	var result CronRunResult
	err := c.call(ctx, requestID, protocol.OperationCronRun, map[string]any{
		"account": account,
		"job":     job,
	}, &result)
	return result, err
}

// CronLogRemove discards a deleted job's collected output.
func (c *Client) CronLogRemove(ctx context.Context, requestID, jobID string) error {
	var result struct {
		Removed bool `json:"removed"`
	}
	return c.call(ctx, requestID, protocol.OperationCronLogRemove, map[string]any{
		"job_id": jobID,
	}, &result)
}
