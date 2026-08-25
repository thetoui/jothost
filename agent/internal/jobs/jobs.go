// Package jobs runs Agent operations asynchronously.
//
// Some infrastructure operations take minutes — issuing a certificate,
// restoring a backup — and holding a socket connection open for the duration
// makes every timeout and retry harder. A caller submits the operation, gets a
// job ID immediately, and polls for progress.
//
// State lives in memory. The API owns durable job records (DATABASE.md
// section 24); duplicating that here would give two sources of truth for the
// same job, and the Agent's copy would be the one that is wrong after a
// restart. Jobs are therefore explicitly ephemeral: an Agent restart loses
// them, which the API must treat as a failed job.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// Errors returned by the runner.
var (
	ErrNotFound  = errors.New("job not found")
	ErrQueueFull = errors.New("too many jobs are running")
	ErrTerminal  = errors.New("job has already finished")
)

// Job is one asynchronous operation.
type Job struct {
	ID        string                 `json:"id"`
	Operation protocol.OperationType `json:"operation"`
	RequestID string                 `json:"request_id"`
	State     protocol.JobState      `json:"state"`
	// Progress is 0-100. Handlers that cannot estimate leave it at 0 until
	// they finish, which is more honest than a fabricated curve.
	Progress int    `json:"progress"`
	Message  string `json:"message,omitempty"`

	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	Result map[string]any  `json:"result,omitempty"`
	Error  *protocol.Error `json:"error,omitempty"`

	// cancel stops the running operation. It is nil once the job is terminal.
	cancel context.CancelFunc
}

// clone returns a copy safe to hand outside the runner's lock.
func (j *Job) clone() Job {
	copied := *j
	copied.cancel = nil
	if j.Result != nil {
		copied.Result = make(map[string]any, len(j.Result))
		for k, v := range j.Result {
			copied.Result[k] = v
		}
	}
	if j.Error != nil {
		errCopy := *j.Error
		copied.Error = &errCopy
	}
	return copied
}

// Executor runs one operation to completion.
type Executor func(ctx context.Context, reporter *Reporter) (map[string]any, error)

// Options configures a Runner.
type Options struct {
	// MaxConcurrent bounds simultaneously running jobs.
	MaxConcurrent int
	// Retention is how long a finished job stays queryable. A caller that
	// polls needs the result to still be there when it asks.
	Retention time.Duration
	// MaxJobs bounds total retained jobs, so a caller that submits
	// continuously cannot grow the Agent's memory without limit.
	MaxJobs int
	// Timeout bounds a single job.
	Timeout time.Duration
	Log     *slog.Logger
	// Now defaults to time.Now.
	Now func() time.Time
}

// Runner owns the job table and the workers running them.
type Runner struct {
	opts Options
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	jobs    map[string]*Job
	running int

	// wg tracks in-flight jobs so shutdown can drain them.
	wg sync.WaitGroup
}

// NewRunner builds a Runner.
func NewRunner(opts Options) *Runner {
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 4
	}
	if opts.Retention <= 0 {
		opts.Retention = 15 * time.Minute
	}
	if opts.MaxJobs <= 0 {
		opts.MaxJobs = 256
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 30 * time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Runner{
		opts: opts,
		log:  opts.Log,
		now:  opts.Now,
		jobs: make(map[string]*Job),
	}
}

// Submit queues an operation and returns its job ID.
//
// The parent context is deliberately not used for the job's lifetime: it
// belongs to the socket connection that submitted the request, and that
// connection closes as soon as the job ID is returned.
func (r *Runner) Submit(op protocol.OperationType, requestID string, exec Executor) (string, error) {
	id, err := newJobID()
	if err != nil {
		return "", err
	}

	r.mu.Lock()
	r.evictLocked()

	if r.running >= r.opts.MaxConcurrent {
		r.mu.Unlock()
		return "", fmt.Errorf("%w: %d already running", ErrQueueFull, r.running)
	}
	if len(r.jobs) >= r.opts.MaxJobs {
		r.mu.Unlock()
		return "", fmt.Errorf("%w: %d jobs retained", ErrQueueFull, len(r.jobs))
	}

	ctx, cancel := context.WithTimeout(context.Background(), r.opts.Timeout)
	job := &Job{
		ID:        id,
		Operation: op,
		RequestID: requestID,
		State:     protocol.JobPending,
		CreatedAt: r.now(),
		cancel:    cancel,
	}
	r.jobs[id] = job
	r.running++
	r.mu.Unlock()

	r.wg.Add(1)
	go r.run(ctx, cancel, job, exec)

	return id, nil
}

// run executes a job and records its outcome.
func (r *Runner) run(ctx context.Context, cancel context.CancelFunc, job *Job, exec Executor) {
	defer r.wg.Done()
	defer cancel()

	started := r.now()
	r.mu.Lock()
	job.State = protocol.JobRunning
	job.StartedAt = &started
	r.mu.Unlock()

	reporter := &Reporter{runner: r, jobID: job.ID}

	result, err := r.safeExec(ctx, exec, reporter)

	completed := r.now()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.running--
	job.CompletedAt = &completed
	job.cancel = nil

	switch {
	case errors.Is(ctx.Err(), context.Canceled):
		job.State = protocol.JobCancelled
		job.Error = &protocol.Error{Code: protocol.CodeInvalidRequest, Message: "Operation cancelled"}
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		job.State = protocol.JobFailed
		job.Error = &protocol.Error{Code: protocol.CodeTimeout, Message: "Operation timed out"}
	case err != nil:
		job.State = protocol.JobFailed
		// The cause is logged; the caller receives a structured code only.
		job.Error = &protocol.Error{Code: protocol.CodeInternal, Message: "Operation failed"}
		r.log.Error("job failed",
			"job_id", job.ID,
			"operation", string(job.Operation),
			"request_id", job.RequestID,
			"error", err.Error(),
		)
	default:
		job.State = protocol.JobSuccess
		job.Progress = 100
		job.Result = result
	}
}

// safeExec runs the executor, converting a panic into an error.
//
// A panic in one operation must not take down a root daemon that other
// operations depend on.
func (r *Runner) safeExec(ctx context.Context, exec Executor, reporter *Reporter) (result map[string]any, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("operation panicked: %v", recovered)
		}
	}()
	return exec(ctx, reporter)
}

// Get returns a job by ID.
func (r *Runner) Get(id string) (Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[id]
	if !ok {
		return Job{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return job.clone(), nil
}

// List returns every retained job, newest first.
func (r *Runner) List() []Job {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.evictLocked()

	out := make([]Job, 0, len(r.jobs))
	for _, job := range r.jobs {
		out = append(out, job.clone())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// Cancel stops a running job.
func (r *Runner) Cancel(id string) error {
	r.mu.Lock()

	job, ok := r.jobs[id]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if job.State.Terminal() {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s is %s", ErrTerminal, id, job.State)
	}

	cancel := job.cancel
	r.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return nil
}

// setProgress records progress for a running job.
func (r *Runner) setProgress(id string, percent int, message string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	job, ok := r.jobs[id]
	if !ok || job.State.Terminal() {
		return
	}
	job.Progress = percent
	job.Message = message
}

// evictLocked drops finished jobs past their retention window. The caller must
// hold r.mu.
func (r *Runner) evictLocked() {
	cutoff := r.now().Add(-r.opts.Retention)

	for id, job := range r.jobs {
		if !job.State.Terminal() || job.CompletedAt == nil {
			continue
		}
		if job.CompletedAt.Before(cutoff) {
			delete(r.jobs, id)
		}
	}
}

// Shutdown cancels running jobs and waits for them to stop.
func (r *Runner) Shutdown(timeout time.Duration) error {
	r.mu.Lock()
	for _, job := range r.jobs {
		if job.cancel != nil {
			job.cancel()
		}
	}
	r.mu.Unlock()

	drained := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(drained)
	}()

	select {
	case <-drained:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("job runner did not drain within %s", timeout)
	}
}

// Reporter lets a running operation publish progress.
type Reporter struct {
	runner *Runner
	jobID  string
}

// Report records a progress percentage and message.
func (r *Reporter) Report(percent int, message string) {
	if r == nil || r.runner == nil {
		// Synchronous execution passes a nil reporter; a handler that reports
		// progress anyway must not crash.
		return
	}
	r.runner.setProgress(r.jobID, percent, message)
}

// JobID returns the job this reporter belongs to, or "" when synchronous.
func (r *Reporter) JobID() string {
	if r == nil {
		return ""
	}
	return r.jobID
}

// newJobID returns an unguessable job identifier.
//
// Sequential IDs would let one caller enumerate another's jobs; random ones
// make a job ID a capability rather than a coordinate.
func newJobID() (string, error) {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return "job_" + hex.EncodeToString(buf), nil
}
