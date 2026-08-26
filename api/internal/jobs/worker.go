package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

// Dispatcher submits work to the Agent and reports on it.
//
// It is an interface so the worker can be tested without a live Agent socket;
// *agentclient.Client satisfies it.
type Dispatcher interface {
	SubmitAsync(ctx context.Context, requestID string, op protocol.OperationType, payload map[string]any) (string, error)
	JobStatus(ctx context.Context, requestID, jobID string) (agentclient.Job, error)
}

// Observer is told when a job reaches a terminal state.
//
// This is how a resource row catches up with the work done to it: the websites
// service uses it to move a site from "creating" to "active" or "failed". It
// runs after the job row is already updated, so a failing observer leaves an
// accurate job and a stale resource rather than the reverse.
type Observer interface {
	JobFinished(ctx context.Context, job Job, state State, result map[string]any, failure string)
}

// Options configure a Worker.
type Options struct {
	Repository *Repository
	Dispatcher Dispatcher
	Observer   Observer
	Log        *slog.Logger

	// PollInterval is how often an idle worker looks for new work.
	PollInterval time.Duration
	// AgentPollInterval is how often a dispatched job's progress is read back.
	AgentPollInterval time.Duration
	// JobTimeout bounds one job end to end.
	JobTimeout time.Duration
	// StaleAfter is how long a RUNNING job may sit untouched before startup
	// treats its worker as dead and requeues it.
	StaleAfter time.Duration
}

// Defaults for a Worker.
const (
	DefaultPollInterval      = 1 * time.Second
	DefaultAgentPollInterval = 500 * time.Millisecond
	DefaultJobTimeout        = 10 * time.Minute
	DefaultStaleAfter        = 15 * time.Minute
)

// Worker claims queued jobs and runs them against the Agent.
//
// One worker runs per API process and executes one job at a time.
// Infrastructure operations touch shared global state — nginx's configuration
// directory, /etc/passwd, the site root — so running two at once invites a
// half-written config being reloaded by the other. Throughput is not the
// constraint here; a control panel queues a handful of jobs, not thousands.
type Worker struct {
	repo       *Repository
	dispatcher Dispatcher
	observer   Observer
	log        *slog.Logger

	pollInterval      time.Duration
	agentPollInterval time.Duration
	jobTimeout        time.Duration
	staleAfter        time.Duration

	stopped chan struct{}
	once    sync.Once
}

// NewWorker builds a Worker, applying defaults for unset options.
func NewWorker(opts Options) *Worker {
	if opts.PollInterval <= 0 {
		opts.PollInterval = DefaultPollInterval
	}
	if opts.AgentPollInterval <= 0 {
		opts.AgentPollInterval = DefaultAgentPollInterval
	}
	if opts.JobTimeout <= 0 {
		opts.JobTimeout = DefaultJobTimeout
	}
	if opts.StaleAfter <= 0 {
		opts.StaleAfter = DefaultStaleAfter
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}

	return &Worker{
		repo:              opts.Repository,
		dispatcher:        opts.Dispatcher,
		observer:          opts.Observer,
		log:               opts.Log,
		pollInterval:      opts.PollInterval,
		agentPollInterval: opts.AgentPollInterval,
		jobTimeout:        opts.JobTimeout,
		staleAfter:        opts.StaleAfter,
		stopped:           make(chan struct{}),
	}
}

// Run processes jobs until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	defer w.once.Do(func() { close(w.stopped) })

	// A previous process may have died mid-job. Those rows say RUNNING but
	// nothing is running them, so they are returned to the queue before this
	// worker starts claiming new work.
	requeued, err := w.repo.RequeueStale(ctx, w.staleAfter)
	if err != nil {
		w.log.Error("failed to requeue interrupted jobs", logger.KeyError, err.Error())
	} else if requeued > 0 {
		w.log.Warn("requeued jobs interrupted by a restart", "count", requeued)
	}

	w.log.Info("job worker started", "poll_interval", w.pollInterval.String())

	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	for {
		// Drain the queue before waiting again, so a burst of queued jobs is
		// not paced one per tick.
		for {
			claimed, err := w.claimOne(ctx)
			if err != nil || !claimed {
				break
			}
		}

		select {
		case <-ctx.Done():
			w.log.Info("job worker stopped")
			return
		case <-ticker.C:
		}
	}
}

// Wait blocks until Run has returned.
func (w *Worker) Wait(timeout time.Duration) error {
	select {
	case <-w.stopped:
		return nil
	case <-time.After(timeout):
		return errors.New("job worker did not stop within the timeout")
	}
}

// claimOne takes at most one job and runs it. It reports whether it ran one.
func (w *Worker) claimOne(ctx context.Context) (bool, error) {
	job, err := w.repo.Claim(ctx)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		w.log.Error("failed to claim a job", logger.KeyError, err.Error())
		return false, err
	}

	w.execute(ctx, job)
	return true, nil
}

// execute runs one claimed job to a terminal state.
func (w *Worker) execute(ctx context.Context, job Job) {
	log := w.log.With("job_id", job.ID, "type", job.Type)
	log.Info("job started")

	// The timeout is the worker's own bound. The Agent enforces its own too;
	// this one exists so an Agent that stops answering cannot pin the worker.
	runCtx, cancel := context.WithTimeout(ctx, w.jobTimeout)
	defer cancel()

	state, result, failure := w.dispatch(runCtx, job)

	// The finishing write uses ctx, not runCtx: when the job failed *because*
	// runCtx expired, writing the outcome through it would fail too and the row
	// would be left RUNNING with no explanation.
	finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer finishCancel()

	switch state {
	case StateSuccess:
		if err := w.repo.Complete(finishCtx, job.ID, result); err != nil {
			log.Error("failed to record job success", logger.KeyError, err.Error())
		}
		log.Info("job succeeded")
	default:
		if err := w.repo.Fail(finishCtx, job.ID, failure); err != nil {
			log.Error("failed to record job failure", logger.KeyError, err.Error())
		}
		// The reason is a user-facing message from the Agent's structured
		// error, not an internal string, so it is safe to log and to display.
		log.Warn("job failed", "reason", failure)
	}

	if w.observer != nil {
		w.observer.JobFinished(finishCtx, job, state, result, failure)
	}
}

// dispatch submits the job to the Agent and follows it to completion.
func (w *Worker) dispatch(ctx context.Context, job Job) (State, map[string]any, string) {
	operation := protocol.OperationType(job.Type)

	// The request id ties the panel's job, the Agent's job, and both audit
	// trails together, so one identifier follows the work across the boundary.
	requestID := "job_" + job.ID

	agentJobID, err := w.dispatcher.SubmitAsync(ctx, requestID, operation, job.Payload)
	if err != nil {
		return StateFailed, nil, describeFailure(err)
	}

	if err := w.repo.Progress(ctx, job.ID, 5, "Dispatched to the host agent"); err != nil {
		// Progress is cosmetic; losing it must not fail the job.
		w.log.Warn("failed to record job progress", logger.KeyError, err.Error())
	}

	return w.follow(ctx, job, requestID, agentJobID)
}

// follow polls the Agent until its job finishes, mirroring progress.
func (w *Worker) follow(ctx context.Context, job Job, requestID, agentJobID string) (State, map[string]any, string) {
	ticker := time.NewTicker(w.agentPollInterval)
	defer ticker.Stop()

	lastProgress := -1

	for {
		select {
		case <-ctx.Done():
			// The Agent is still working; the panel has just stopped watching.
			// Saying so is more honest than reporting a failure it did not have.
			return StateFailed, nil, "The operation did not finish in time. It may still be running on the host."
		case <-ticker.C:
		}

		agentJob, err := w.dispatcher.JobStatus(ctx, requestID, agentJobID)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			// A transient socket error should not discard work that is very
			// likely still running, so polling continues until the timeout.
			w.log.Warn("failed to read agent job status",
				"job_id", job.ID, logger.KeyError, err.Error())
			continue
		}

		if agentJob.Progress != lastProgress || agentJob.Message != "" {
			lastProgress = agentJob.Progress
			// Agent progress is squeezed into 10-95 so the panel's own
			// dispatch and completion steps have room at either end.
			scaled := 10 + (agentJob.Progress * 85 / 100)
			if err := w.repo.Progress(ctx, job.ID, scaled, agentJob.Message); err != nil {
				w.log.Warn("failed to mirror job progress", logger.KeyError, err.Error())
			}
		}

		switch agentJob.State {
		case protocol.JobSuccess:
			return StateSuccess, agentJob.Result, ""
		case protocol.JobFailed:
			return StateFailed, agentJob.Result, describeAgentError(agentJob.Error)
		case protocol.JobCancelled:
			return StateCancelled, nil, "The operation was cancelled on the host."
		}
	}
}

// describeFailure turns an error into something safe to show a user.
//
// Internal error text can carry paths and configuration detail, so only the
// Agent's structured errors are passed through; anything else is reported
// generically and the detail stays in the logs.
func describeFailure(err error) string {
	var failed *agentclient.ErrOperationFailed
	if errors.As(err, &failed) {
		return failed.Message
	}
	if errors.Is(err, agentclient.ErrUnavailable) {
		return "The host agent is unavailable."
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The operation timed out."
	}
	return "The operation could not be completed."
}

// describeAgentError renders a structured Agent error for display.
func describeAgentError(agentErr *protocol.Error) string {
	if agentErr == nil {
		return "The operation failed on the host."
	}
	message := strings.TrimSpace(agentErr.Message)
	if message == "" {
		return fmt.Sprintf("The operation failed on the host (%s).", agentErr.Code)
	}
	return message
}
