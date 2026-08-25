package jobs

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

func newRunner(t *testing.T, opts Options) *Runner {
	t.Helper()

	if opts.Log == nil {
		var buf bytes.Buffer
		opts.Log = logger.New(logger.Options{Service: "agent", Level: "error", Output: &buf})
	}

	runner := NewRunner(opts)
	t.Cleanup(func() {
		if err := runner.Shutdown(5 * time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})
	return runner
}

// awaitTerminal polls until a job finishes.
func awaitTerminal(t *testing.T, runner *Runner, id string) Job {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		job, err := runner.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if job.State.Terminal() {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish, state %s", id, job.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestSubmitRunsToCompletion(t *testing.T) {
	runner := newRunner(t, Options{})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	job := awaitTerminal(t, runner, id)
	if job.State != protocol.JobSuccess {
		t.Fatalf("state = %s, want SUCCESS", job.State)
	}
	if job.Result["ok"] != true {
		t.Fatalf("unexpected result: %+v", job.Result)
	}
	if job.Progress != 100 {
		t.Fatalf("a completed job must report 100%%, got %d", job.Progress)
	}
	if job.StartedAt == nil || job.CompletedAt == nil {
		t.Fatalf("timestamps must be recorded: %+v", job)
	}
}

func TestFailedJobReportsAGenericError(t *testing.T) {
	runner := newRunner(t, Options{})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			return nil, errors.New("connection to /var/secret/socket failed")
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	job := awaitTerminal(t, runner, id)
	if job.State != protocol.JobFailed {
		t.Fatalf("state = %s, want FAILED", job.State)
	}
	// The cause belongs in the log, not in a caller-visible message.
	if strings.Contains(job.Error.Message, "/var/secret") {
		t.Fatalf("internal detail leaked: %q", job.Error.Message)
	}
	if job.Error.Code != protocol.CodeInternal {
		t.Fatalf("error code = %q", job.Error.Code)
	}
}

func TestPanicInAJobDoesNotCrashTheAgent(t *testing.T) {
	runner := newRunner(t, Options{})

	// A panic in one operation must not take down a root daemon that other
	// operations depend on.
	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			panic("handler exploded")
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	job := awaitTerminal(t, runner, id)
	if job.State != protocol.JobFailed {
		t.Fatalf("a panicking job must be recorded as failed, got %s", job.State)
	}

	// The runner must still work afterwards.
	next, err := runner.Submit(protocol.OperationPing, "req_2",
		func(context.Context, *Reporter) (map[string]any, error) {
			return map[string]any{"ok": true}, nil
		})
	if err != nil {
		t.Fatalf("the runner must survive a panic: %v", err)
	}
	if job := awaitTerminal(t, runner, next); job.State != protocol.JobSuccess {
		t.Fatalf("the next job must succeed, got %s", job.State)
	}
}

func TestJobTimeout(t *testing.T) {
	runner := newRunner(t, Options{Timeout: 150 * time.Millisecond})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(ctx context.Context, _ *Reporter) (map[string]any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	job := awaitTerminal(t, runner, id)
	if job.State != protocol.JobFailed {
		t.Fatalf("state = %s, want FAILED", job.State)
	}
	if job.Error.Code != protocol.CodeTimeout {
		t.Fatalf("error code = %q, want %q", job.Error.Code, protocol.CodeTimeout)
	}
}

func TestCancelStopsARunningJob(t *testing.T) {
	runner := newRunner(t, Options{Timeout: 30 * time.Second})

	started := make(chan struct{})
	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(ctx context.Context, _ *Reporter) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-started
	if err := runner.Cancel(id); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	job := awaitTerminal(t, runner, id)
	if job.State != protocol.JobCancelled {
		t.Fatalf("state = %s, want CANCELLED", job.State)
	}
}

func TestCancelRejectsUnknownAndFinishedJobs(t *testing.T) {
	runner := newRunner(t, Options{})

	if err := runner.Cancel("job_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			return nil, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	awaitTerminal(t, runner, id)

	if err := runner.Cancel(id); !errors.Is(err, ErrTerminal) {
		t.Fatalf("cancelling a finished job must fail, got %v", err)
	}
}

func TestProgressReporting(t *testing.T) {
	runner := newRunner(t, Options{Timeout: 5 * time.Second})

	reported := make(chan struct{})
	release := make(chan struct{})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(_ context.Context, reporter *Reporter) (map[string]any, error) {
			reporter.Report(40, "configuring nginx")
			close(reported)
			<-release
			return map[string]any{"done": true}, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-reported
	job, err := runner.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Progress != 40 || job.Message != "configuring nginx" {
		t.Fatalf("progress not recorded: %+v", job)
	}

	close(release)
	awaitTerminal(t, runner, id)
}

func TestProgressIsClamped(t *testing.T) {
	runner := newRunner(t, Options{Timeout: 5 * time.Second})

	reported := make(chan struct{})
	release := make(chan struct{})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(_ context.Context, reporter *Reporter) (map[string]any, error) {
			reporter.Report(500, "over")
			close(reported)
			<-release
			return nil, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-reported
	job, err := runner.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if job.Progress != 100 {
		t.Fatalf("progress must be clamped to 100, got %d", job.Progress)
	}

	close(release)
	awaitTerminal(t, runner, id)
}

func TestNilReporterIsSafe(t *testing.T) {
	// Synchronous dispatch passes a nil reporter; a handler that reports
	// progress anyway must not crash the daemon.
	var reporter *Reporter
	reporter.Report(50, "sync")

	if reporter.JobID() != "" {
		t.Fatal("a nil reporter must have no job id")
	}
}

func TestConcurrencyLimit(t *testing.T) {
	runner := newRunner(t, Options{MaxConcurrent: 2, Timeout: 5 * time.Second})

	release := make(chan struct{})
	blocker := func(context.Context, *Reporter) (map[string]any, error) {
		<-release
		return nil, nil
	}

	for i := 0; i < 2; i++ {
		if _, err := runner.Submit(protocol.OperationPing, "req_1", blocker); err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
	}

	// A third submission must be refused rather than queued without bound:
	// unbounded queueing is how a caller exhausts a root daemon.
	if _, err := runner.Submit(protocol.OperationPing, "req_1", blocker); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("expected ErrQueueFull, got %v", err)
	}

	close(release)
}

func TestRetentionEvictsFinishedJobs(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	runner := newRunner(t, Options{
		Retention: time.Minute,
		Now:       func() time.Time { return clock },
	})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			return nil, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	awaitTerminal(t, runner, id)

	if len(runner.List()) != 1 {
		t.Fatal("the finished job must still be queryable within its retention window")
	}

	// Past the window, the record is dropped so memory does not grow without
	// bound on a busy Agent.
	clock = clock.Add(2 * time.Minute)
	if jobs := runner.List(); len(jobs) != 0 {
		t.Fatalf("expected the job to be evicted, got %+v", jobs)
	}
}

func TestJobIDsAreUnguessable(t *testing.T) {
	runner := newRunner(t, Options{MaxConcurrent: 64, MaxJobs: 128})

	seen := make(map[string]struct{}, 50)
	for i := 0; i < 50; i++ {
		id, err := runner.Submit(protocol.OperationPing, "req_1",
			func(context.Context, *Reporter) (map[string]any, error) {
				return nil, nil
			})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		if !strings.HasPrefix(id, "job_") {
			t.Fatalf("unexpected job id format: %q", id)
		}
		// Sequential IDs would let one caller enumerate another's jobs.
		if _, duplicate := seen[id]; duplicate {
			t.Fatalf("duplicate job id: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestGetReturnsACopy(t *testing.T) {
	runner := newRunner(t, Options{})

	id, err := runner.Submit(protocol.OperationPing, "req_1",
		func(context.Context, *Reporter) (map[string]any, error) {
			return map[string]any{"value": "original"}, nil
		})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	awaitTerminal(t, runner, id)

	job, err := runner.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	job.Result["value"] = "mutated"

	// A caller mutating its copy must not corrupt the runner's record.
	again, err := runner.Get(id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if again.Result["value"] != "original" {
		t.Fatal("Get must return a defensive copy")
	}
}

func TestListIsOrderedNewestFirst(t *testing.T) {
	clock := time.Unix(1700000000, 0)
	runner := newRunner(t, Options{
		MaxConcurrent: 8,
		Retention:     time.Hour,
		Now:           func() time.Time { return clock },
	})

	var ids []string
	for i := 0; i < 3; i++ {
		id, err := runner.Submit(protocol.OperationPing, "req_1",
			func(context.Context, *Reporter) (map[string]any, error) {
				return nil, nil
			})
		if err != nil {
			t.Fatalf("Submit: %v", err)
		}
		ids = append(ids, id)
		awaitTerminal(t, runner, id)
		clock = clock.Add(time.Second)
	}

	list := runner.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 jobs, got %d", len(list))
	}
	if list[0].ID != ids[2] {
		t.Fatalf("expected the newest job first, got %s", list[0].ID)
	}
}

func TestShutdownCancelsRunningJobs(t *testing.T) {
	var buf bytes.Buffer
	runner := NewRunner(Options{
		Timeout: 30 * time.Second,
		Log:     logger.New(logger.Options{Service: "agent", Level: "error", Output: &buf}),
	})

	started := make(chan struct{})
	if _, err := runner.Submit(protocol.OperationPing, "req_1",
		func(ctx context.Context, _ *Reporter) (map[string]any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}); err != nil {
		t.Fatalf("Submit: %v", err)
	}

	<-started

	// Shutdown must not wait for a 30-second job to finish on its own.
	start := time.Now()
	if err := runner.Shutdown(5 * time.Second); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("shutdown did not cancel running jobs: took %v", elapsed)
	}
}

func TestConcurrentAccessIsSafe(t *testing.T) {
	runner := newRunner(t, Options{MaxConcurrent: 16, MaxJobs: 256})

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			id, err := runner.Submit(protocol.OperationPing, "req_1",
				func(_ context.Context, reporter *Reporter) (map[string]any, error) {
					reporter.Report(50, "working")
					return map[string]any{"ok": true}, nil
				})
			if err != nil {
				return
			}
			_, _ = runner.Get(id)
			_ = runner.List()
		}()
	}
	wg.Wait()
}

func TestJobStateTerminal(t *testing.T) {
	terminal := []protocol.JobState{protocol.JobSuccess, protocol.JobFailed, protocol.JobCancelled}
	for _, state := range terminal {
		if !state.Terminal() {
			t.Fatalf("%s must be terminal", state)
		}
	}
	for _, state := range []protocol.JobState{protocol.JobPending, protocol.JobRunning} {
		if state.Terminal() {
			t.Fatalf("%s must not be terminal", state)
		}
	}
}
