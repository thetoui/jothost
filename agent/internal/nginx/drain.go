package nginx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultProcRoot is where process state is read from. It is a variable in
// Options rather than a constant here so tests can point at a fixture tree.
const DefaultProcRoot = "/proc"

// DrainTimeout bounds how long a caller will wait for nginx's previous workers
// to finish.
//
// A reload is graceful: the old workers keep serving until their connections
// end, and a single slow request would otherwise hold up the operation for as
// long as it runs. Ten seconds covers an ordinary drain; past that the caller
// proceeds anyway, because a job that never finishes is worse than a request
// that fails.
const DrainTimeout = 10 * time.Second

// drainPoll is how often the process table is re-read while waiting.
const drainPoll = 100 * time.Millisecond

// processName is the name nginx's master and workers both carry.
const processName = "nginx"

// WorkerPIDs returns the process ids of nginx's workers.
//
// The master is excluded deliberately: it keeps its pid across a reload, so a
// caller waiting for it to exit would wait forever. A worker is an nginx
// process whose parent is also an nginx process, which identifies the master
// without depending on a pid file whose location varies by distribution.
func (p *Provider) WorkerPIDs() ([]int, error) {
	entries, err := os.ReadDir(p.procRoot)
	if err != nil {
		return nil, fmt.Errorf("read process table: %w", err)
	}

	type process struct{ parent int }
	nginxProcesses := make(map[int]process)

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		name, parent, ok := p.processStatus(pid)
		if !ok || name != processName {
			continue
		}
		nginxProcesses[pid] = process{parent: parent}
	}

	workers := make([]int, 0, len(nginxProcesses))
	for pid, proc := range nginxProcesses {
		if _, parentIsNginx := nginxProcesses[proc.parent]; parentIsNginx {
			workers = append(workers, pid)
		}
	}
	sort.Ints(workers)
	return workers, nil
}

// WaitForWorkers blocks until none of the given workers is running any more,
// and returns the ones still alive when it gave up.
//
// An empty return means the drain finished. A non-empty one is not an error:
// the caller decides what to do about it, and the only sensible thing is to
// carry on and say so.
func (p *Provider) WaitForWorkers(ctx context.Context, pids []int, timeout time.Duration) []int {
	if len(pids) == 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)

	for {
		remaining := make([]int, 0, len(pids))
		for _, pid := range pids {
			if p.isRunningWorker(pid) {
				remaining = append(remaining, pid)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return remaining
		}

		select {
		case <-ctx.Done():
			return remaining
		case <-time.After(drainPoll):
		}
	}
}

// isRunningWorker reports whether a pid is still an nginx process.
//
// The name is checked as well as the pid's existence: pids are reused, and
// treating an unrelated new process as a draining worker would hold up the
// caller for the whole timeout.
func (p *Provider) isRunningWorker(pid int) bool {
	name, _, ok := p.processStatus(pid)
	return ok && name == processName
}

// processStatus reads a process's name and parent, reporting whether it could
// be read at all. A process that exits between the directory listing and this
// read is the normal case, not a failure.
func (p *Provider) processStatus(pid int) (name string, parent int, ok bool) {
	path := filepath.Join(p.procRoot, strconv.Itoa(pid), "status")
	content, err := os.ReadFile(path) //nolint:gosec // path is built from an integer pid
	if err != nil {
		return "", 0, false
	}

	for _, line := range strings.Split(string(content), "\n") {
		switch {
		case strings.HasPrefix(line, "Name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		case strings.HasPrefix(line, "PPid:"):
			value := strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return "", 0, false
			}
			parent = parsed
		}
	}
	if name == "" {
		return "", 0, false
	}
	return name, parent, true
}
