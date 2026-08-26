package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/protocol"
)

func newRepo(t *testing.T) *jobs.Repository {
	t.Helper()
	deps := testsupport.Require(t)
	return jobs.NewRepository(deps.Pool)
}

func TestCreateStartsPending(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{
		Type:    jobs.TypeWebsiteCreate,
		Payload: map[string]any{"domain": "example.test"},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if job.Status != jobs.StatePending {
		t.Fatalf("new job status = %q, want PENDING", job.Status)
	}
	if job.Progress != 0 {
		t.Fatalf("new job progress = %d, want 0", job.Progress)
	}
	if got := job.Payload["domain"]; got != "example.test" {
		t.Fatalf("payload round-trip = %v, want example.test", got)
	}
}

// A job must be handed to exactly one worker. This is the property SKIP LOCKED
// exists for, and getting it wrong means two workers provisioning one website.
func TestClaimHandsAJobToOnlyOneCaller(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	created, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	const workers = 8
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		claimed []string
	)

	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			job, err := repo.Claim(ctx)
			if err != nil {
				return
			}
			mu.Lock()
			claimed = append(claimed, job.ID)
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(claimed) != 1 {
		t.Fatalf("job was claimed %d times, want exactly 1", len(claimed))
	}
	if claimed[0] != created.ID {
		t.Fatalf("claimed job %s, want %s", claimed[0], created.ID)
	}

	job, err := repo.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	if job.Status != jobs.StateRunning {
		t.Fatalf("claimed job status = %q, want RUNNING", job.Status)
	}
	if job.StartedAt == nil {
		t.Fatal("claimed job has no started_at")
	}
}

func TestClaimReportsNotFoundWhenQueueIsEmpty(t *testing.T) {
	repo := newRepo(t)

	if _, err := repo.Claim(context.Background()); !errors.Is(err, jobs.ErrNotFound) {
		t.Fatalf("claim on empty queue = %v, want ErrNotFound", err)
	}
}

func TestClaimTakesTheOldestJobFirst(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	first, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create first job: %v", err)
	}
	// Postgres' now() is transaction-scoped, so two rows created in the same
	// instant could tie; a brief gap makes the ordering unambiguous.
	time.Sleep(5 * time.Millisecond)
	if _, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteDelete}); err != nil {
		t.Fatalf("create second job: %v", err)
	}

	claimed, err := repo.Claim(ctx)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != first.ID {
		t.Fatalf("claimed %s, want the older job %s", claimed.ID, first.ID)
	}
}

func TestProgressIsIgnoredOnceAJobHasFinished(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := repo.Complete(ctx, job.ID, map[string]any{"reloaded": true}); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// A late progress report from a straggling poll must not drag a finished
	// job back into RUNNING.
	if err := repo.Progress(ctx, job.ID, 40, "still working"); err != nil {
		t.Fatalf("progress: %v", err)
	}

	after, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StateSuccess {
		t.Fatalf("status = %q, want SUCCESS", after.Status)
	}
	if after.Progress != 100 {
		t.Fatalf("progress = %d, want 100", after.Progress)
	}
	if got := after.Result["reloaded"]; got != true {
		t.Fatalf("result = %v, want reloaded true", after.Result)
	}
}

func TestProgressIsClampedToTheValidRange(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The column has a CHECK constraint; an unclamped value would fail the
	// write rather than being stored wrong.
	if err := repo.Progress(ctx, job.ID, 500, "overshot"); err != nil {
		t.Fatalf("progress above range: %v", err)
	}
	after, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Progress != 100 {
		t.Fatalf("progress = %d, want it clamped to 100", after.Progress)
	}
}

func TestCancelOnlyAppliesToPendingJobs(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	if err := repo.Cancel(ctx, job.ID); err != nil {
		t.Fatalf("cancel pending job: %v", err)
	}

	after, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StateCancelled {
		t.Fatalf("status = %q, want CANCELLED", after.Status)
	}

	// A second cancel must not silently succeed: the job is already terminal.
	if err := repo.Cancel(ctx, job.ID); !errors.Is(err, jobs.ErrTerminal) {
		t.Fatalf("cancel of a finished job = %v, want ErrTerminal", err)
	}
}

func TestCancelRefusesARunningJob(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// The Agent is changing the host by now. Reporting it cancelled would be a
	// claim the panel cannot make good on.
	if err := repo.Cancel(ctx, job.ID); !errors.Is(err, jobs.ErrTerminal) {
		t.Fatalf("cancel of a running job = %v, want ErrTerminal", err)
	}
}

func TestRequeueStaleRecoversJobsFromACrashedWorker(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A zero threshold treats every running job as abandoned, which is what a
	// process that just died looks like.
	requeued, err := repo.RequeueStale(ctx, 0)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued %d jobs, want 1", requeued)
	}

	after, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StatePending {
		t.Fatalf("status = %q, want PENDING", after.Status)
	}
	if after.StartedAt != nil {
		t.Fatal("requeued job kept its started_at")
	}
}

func TestRequeueStaleLeavesRecentJobsAlone(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	job, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	if _, err := repo.Claim(ctx); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// A job that started seconds ago is running, not abandoned. Requeuing it
	// would run the same infrastructure operation twice.
	requeued, err := repo.RequeueStale(ctx, time.Hour)
	if err != nil {
		t.Fatalf("requeue: %v", err)
	}
	if requeued != 0 {
		t.Fatalf("requeued %d jobs, want 0", requeued)
	}

	after, err := repo.Get(ctx, job.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.Status != jobs.StateRunning {
		t.Fatalf("status = %q, want RUNNING", after.Status)
	}
}

func TestListFiltersByResource(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	// A UUID that no website row uses; the column has no foreign key, so this
	// exercises the filter without needing a real site.
	const resourceID = "11111111-2222-3333-4444-555555555555"

	if _, err := repo.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypeWebsiteCreate,
		ResourceType: "website",
		ResourceID:   resourceID,
	}); err != nil {
		t.Fatalf("create matching job: %v", err)
	}
	if _, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteDelete}); err != nil {
		t.Fatalf("create unrelated job: %v", err)
	}

	found, err := repo.List(ctx, jobs.ListParams{
		ResourceType: "website",
		ResourceID:   resourceID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("listed %d jobs, want 1", len(found))
	}
	if found[0].ResourceID == nil || *found[0].ResourceID != resourceID {
		t.Fatalf("listed job resource = %v, want %s", found[0].ResourceID, resourceID)
	}
}

// ---------------------------------------------------------------- the worker

// fakeDispatcher stands in for the Agent so worker behaviour can be tested
// without a socket or a host.
type fakeDispatcher struct {
	mu sync.Mutex

	submitErr error
	// states is returned by successive JobStatus calls, so a test can drive a
	// job through progress updates into a terminal state.
	states []agentclient.Job
	calls  int

	submitted []protocol.OperationType
	payloads  []map[string]any
}

func (f *fakeDispatcher) SubmitAsync(_ context.Context, _ string,
	op protocol.OperationType, payload map[string]any,
) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.submitErr != nil {
		return "", f.submitErr
	}
	f.submitted = append(f.submitted, op)
	f.payloads = append(f.payloads, payload)
	return "agent-job-1", nil
}

func (f *fakeDispatcher) JobStatus(_ context.Context, _, _ string) (agentclient.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.calls >= len(f.states) {
		// Hold on the last state rather than running off the end.
		return f.states[len(f.states)-1], nil
	}
	state := f.states[f.calls]
	f.calls++
	return state, nil
}

// recordingObserver captures reconciliation calls.
type recordingObserver struct {
	mu       sync.Mutex
	finished []jobs.State
	failures []string
}

func (o *recordingObserver) JobFinished(_ context.Context, _ jobs.Job, state jobs.State,
	_ map[string]any, failure string,
) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finished = append(o.finished, state)
	o.failures = append(o.failures, failure)
}

func (o *recordingObserver) snapshot() ([]jobs.State, []string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]jobs.State(nil), o.finished...), append([]string(nil), o.failures...)
}

// runWorkerOnce runs the worker until the job reaches a terminal state.
func runWorkerOnce(t *testing.T, repo *jobs.Repository, dispatcher jobs.Dispatcher,
	observer jobs.Observer, jobID string,
) jobs.Job {
	t.Helper()

	worker := jobs.NewWorker(jobs.Options{
		Repository:        repo,
		Dispatcher:        dispatcher,
		Observer:          observer,
		PollInterval:      10 * time.Millisecond,
		AgentPollInterval: 5 * time.Millisecond,
		JobTimeout:        3 * time.Second,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("job did not reach a terminal state")
		case <-time.After(10 * time.Millisecond):
		}

		job, err := repo.Get(context.Background(), jobID)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status.Terminal() {
			cancel()
			if err := worker.Wait(5 * time.Second); err != nil {
				t.Fatalf("worker did not stop: %v", err)
			}
			return job
		}
	}
}

func TestWorkerRunsAJobToSuccess(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	queued, err := repo.Create(ctx, jobs.CreateParams{
		Type:    jobs.TypeWebsiteCreate,
		Payload: map[string]any{"domain": "example.test"},
	})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	dispatcher := &fakeDispatcher{states: []agentclient.Job{
		{State: protocol.JobRunning, Progress: 50, Message: "writing vhost"},
		{
			State:    protocol.JobSuccess,
			Progress: 100,
			Result:   map[string]any{"reloaded": true},
		},
	}}
	observer := &recordingObserver{}

	job := runWorkerOnce(t, repo, dispatcher, observer, queued.ID)

	if job.Status != jobs.StateSuccess {
		t.Fatalf("status = %q, want SUCCESS (error: %v)", job.Status, job.Error)
	}
	if job.Progress != 100 {
		t.Fatalf("progress = %d, want 100", job.Progress)
	}
	if got := job.Result["reloaded"]; got != true {
		t.Fatalf("result = %v, want reloaded true", job.Result)
	}

	if len(dispatcher.submitted) != 1 ||
		dispatcher.submitted[0] != protocol.OperationType(jobs.TypeWebsiteCreate) {
		t.Fatalf("submitted %v, want one website.create", dispatcher.submitted)
	}
	if got := dispatcher.payloads[0]["domain"]; got != "example.test" {
		t.Fatalf("dispatched payload = %v, want the queued payload", dispatcher.payloads[0])
	}

	states, _ := observer.snapshot()
	if len(states) != 1 || states[0] != jobs.StateSuccess {
		t.Fatalf("observer saw %v, want one SUCCESS", states)
	}
}

func TestWorkerRecordsAnAgentFailure(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	queued, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	dispatcher := &fakeDispatcher{states: []agentclient.Job{{
		State: protocol.JobFailed,
		Error: &protocol.Error{
			Code:    protocol.CodeInternal,
			Message: "nginx configuration test failed",
		},
	}}}
	observer := &recordingObserver{}

	job := runWorkerOnce(t, repo, dispatcher, observer, queued.ID)

	if job.Status != jobs.StateFailed {
		t.Fatalf("status = %q, want FAILED", job.Status)
	}
	if job.Error == nil || *job.Error != "nginx configuration test failed" {
		t.Fatalf("error = %v, want the agent's message", job.Error)
	}

	states, failures := observer.snapshot()
	if len(states) != 1 || states[0] != jobs.StateFailed {
		t.Fatalf("observer saw %v, want one FAILED", states)
	}
	if failures[0] != "nginx configuration test failed" {
		t.Fatalf("observer failure = %q, want the agent's message", failures[0])
	}
}

// An unreachable Agent must fail the job with a message a user can act on,
// not leak the underlying socket error.
func TestWorkerReportsAnUnavailableAgentSafely(t *testing.T) {
	repo := newRepo(t)
	ctx := context.Background()

	queued, err := repo.Create(ctx, jobs.CreateParams{Type: jobs.TypeWebsiteCreate})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}

	dispatcher := &fakeDispatcher{submitErr: agentclient.ErrUnavailable}
	job := runWorkerOnce(t, repo, dispatcher, &recordingObserver{}, queued.ID)

	if job.Status != jobs.StateFailed {
		t.Fatalf("status = %q, want FAILED", job.Status)
	}
	if job.Error == nil || *job.Error != "The host agent is unavailable." {
		t.Fatalf("error = %v, want the agent-unavailable message", job.Error)
	}
}
