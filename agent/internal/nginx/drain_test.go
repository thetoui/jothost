package nginx_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/nginx"
)

// fakeProc builds a process table the way /proc presents one.
type fakeProc struct {
	root string
	t    *testing.T
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	return &fakeProc{root: t.TempDir(), t: t}
}

// tryAdd and tryRemove change the process table and report what went wrong.
//
// They return an error rather than failing the test because the tests below
// change the table from a goroutine while a wait is in progress — that is what
// a reload looks like — and t.Fatalf from a goroutine that outlives its test
// panics the whole binary, taking every other test in the run with it. The
// error goes back to the test body through a channel instead.
func (f *fakeProc) tryAdd(pid int, name string, parent int) error {
	dir := filepath.Join(f.root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	content := fmt.Sprintf("Name:\t%s\nState:\tS (sleeping)\nPPid:\t%d\n", name, parent)
	if err := os.WriteFile(filepath.Join(dir, "status"), []byte(content), 0o644); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

func (f *fakeProc) tryRemove(pid int) error {
	if err := os.RemoveAll(filepath.Join(f.root, fmt.Sprint(pid))); err != nil {
		return fmt.Errorf("remove %d: %w", pid, err)
	}
	return nil
}

// add and remove are the same thing for a test body to call directly.
//
// Only from the test's own goroutine: they end the test on failure, which is
// what makes them convenient here and what makes them unusable anywhere else.
func (f *fakeProc) add(pid int, name string, parent int) {
	f.t.Helper()
	if err := f.tryAdd(pid, name, parent); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) remove(pid int) {
	f.t.Helper()
	if err := f.tryRemove(pid); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fakeProc) provider() *nginx.Provider {
	return nginx.NewProvider(nginx.Options{ProcRoot: f.root})
}

// The master keeps its pid across a reload, so a caller waiting for it to exit
// would wait for the whole timeout on every single switch.
func TestWorkerPIDsExcludesTheMaster(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(1, "jothost-agent", 0)
	proc.add(10, "nginx", 1) // master: its parent is not nginx
	proc.add(11, "nginx", 10)
	proc.add(12, "nginx", 10)
	proc.add(99, "php-fpm83", 1)

	workers, err := proc.provider().WorkerPIDs()
	if err != nil {
		t.Fatalf("worker pids: %v", err)
	}

	if len(workers) != 2 || workers[0] != 11 || workers[1] != 12 {
		t.Fatalf("workers = %v, want [11 12]", workers)
	}
}

func TestWorkerPIDsIgnoresOtherProcesses(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(1, "jothost-agent", 0)
	proc.add(50, "php-fpm82", 1)
	proc.add(51, "php-fpm82", 50)

	workers, err := proc.provider().WorkerPIDs()
	if err != nil {
		t.Fatalf("worker pids: %v", err)
	}
	if len(workers) != 0 {
		t.Fatalf("workers = %v, want none on a host with no nginx", workers)
	}
}

func TestWaitForWorkersReturnsWhenTheyExit(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(10, "nginx", 1)
	proc.add(11, "nginx", 10)
	proc.add(12, "nginx", 10)

	provider := proc.provider()

	// The old workers finish while the wait is in progress, which is what a
	// graceful reload looks like.
	//
	// The test waits for this goroutine before it returns, so its writes cannot
	// land in a temporary directory the framework has already removed.
	done := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		if err := proc.tryRemove(11); err != nil {
			done <- err
			return
		}
		done <- proc.tryRemove(12)
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Errorf("changing the process table: %v", err)
		}
	}()

	start := time.Now()
	remaining := provider.WaitForWorkers(context.Background(), []int{11, 12}, 5*time.Second)
	elapsed := time.Since(start)

	if len(remaining) != 0 {
		t.Fatalf("remaining = %v, want none once the workers have gone", remaining)
	}
	// It must return when they exit, not sit out the full timeout.
	if elapsed > 2*time.Second {
		t.Fatalf("waited %s after the workers exited", elapsed)
	}
}

// A single long-running request must not hold a switch open indefinitely.
func TestWaitForWorkersGivesUpAtTheTimeout(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(10, "nginx", 1)
	proc.add(11, "nginx", 10)

	remaining := proc.provider().WaitForWorkers(context.Background(), []int{11}, 200*time.Millisecond)

	if len(remaining) != 1 || remaining[0] != 11 {
		t.Fatalf("remaining = %v, want [11] reported as still draining", remaining)
	}
}

// Pids are reused. Treating an unrelated new process as a draining worker
// would stall every switch for the length of the timeout.
func TestWaitForWorkersIgnoresAReusedPID(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(10, "nginx", 1)
	proc.add(11, "nginx", 10)

	provider := proc.provider()

	done := make(chan error, 1)
	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := proc.tryRemove(11); err != nil {
			done <- err
			return
		}
		done <- proc.tryAdd(11, "php-fpm84", 1)
	}()
	defer func() {
		if err := <-done; err != nil {
			t.Errorf("changing the process table: %v", err)
		}
	}()

	remaining := provider.WaitForWorkers(context.Background(), []int{11}, 5*time.Second)
	if len(remaining) != 0 {
		t.Fatalf("remaining = %v, want none once the pid is no longer nginx", remaining)
	}
}

func TestWaitForWorkersStopsWhenTheContextEnds(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(10, "nginx", 1)
	proc.add(11, "nginx", 10)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	remaining := proc.provider().WaitForWorkers(ctx, []int{11}, time.Minute)
	if time.Since(start) > 5*time.Second {
		t.Fatal("a cancelled context must end the wait")
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining = %v, want the worker reported as still running", remaining)
	}
}

func TestWaitForWorkersWithNothingToWaitFor(t *testing.T) {
	proc := newFakeProc(t)
	if remaining := proc.provider().WaitForWorkers(context.Background(), nil, time.Minute); remaining != nil {
		t.Fatalf("remaining = %v, want nil", remaining)
	}
}

// A host whose process table cannot be read must report that rather than
// looking like a host with no workers, so the caller can log it.
func TestWorkerPIDsReportsAnUnreadableProcRoot(t *testing.T) {
	provider := nginx.NewProvider(nginx.Options{ProcRoot: filepath.Join(t.TempDir(), "missing")})

	if _, err := provider.WorkerPIDs(); err == nil {
		t.Fatal("an unreadable process table must be reported")
	}
}
