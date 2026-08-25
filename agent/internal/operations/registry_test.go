package operations

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

// procFixture writes a minimal but realistic /proc tree.
//
// Collectors are tested against a fixture rather than the build machine so the
// assertions can be exact: a test that only checks "some number came back"
// would pass against a parser that reads the wrong column.
func procFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	write := func(name, content string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	write("stat", "cpu  1000 100 500 8000 200 0 50 0 0 0\n"+
		"cpu0 500 50 250 4000 100 0 25 0 0 0\n"+
		"cpu1 500 50 250 4000 100 0 25 0 0 0\n"+
		"intr 12345\n")
	write("meminfo", "MemTotal:       16384000 kB\n"+
		"MemFree:         2048000 kB\n"+
		"MemAvailable:    8192000 kB\n"+
		"Buffers:          512000 kB\n"+
		"Cached:          4096000 kB\n"+
		"SwapTotal:       2048000 kB\n"+
		"SwapFree:        1024000 kB\n")
	write("loadavg", "1.50 0.75 0.30 2/512 9999\n")
	write("uptime", "86400.00 340000.00\n")
	write("net/dev", "Inter-|   Receive                    |  Transmit\n"+
		" face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed\n"+
		"    lo:    1000      10    0    0    0     0          0         0     1000      10    0    0    0     0       0          0\n"+
		"  eth0: 5000000   40000    1    2    0     0          0         0  2500000   20000    3    4    0     0       0          0\n")
	write("mounts", "/dev/sda1 / ext4 rw,relatime 0 0\n"+
		"proc /proc proc rw,nosuid 0 0\n")
	write("sys/kernel/osrelease", "6.1.0-test\n")

	// One process entry.
	write("1234/status", "Name:\tnginx\nState:\tS (sleeping)\nPPid:\t1\n"+
		"Uid:\t0\t0\t0\t0\nVmRSS:\t   20480 kB\nThreads:\t4\n")
	write("1234/stat", "1234 (nginx) S 1 1234 1234 0 -1 4194304 100 0 0 0 250 150 0 0 20 0 4 0 100 0 0 0 0\n")
	write("1234/cmdline", "nginx\x00-g\x00daemon off;\x00")

	return root
}

// fixture bundles a registry with its collaborators.
type fixture struct {
	registry *Registry
	jobs     *jobs.Runner
	logBuf   *bytes.Buffer
	procRoot string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Level: "error", Output: &buf})

	procRoot := procFixture(t)

	// systemctl deliberately points at a path that does not exist, so service
	// operations exercise the "unavailable" path rather than the build
	// machine's init system.
	runner, err := command.NewRunner(command.Spec{
		Name: services.CommandName,
		Path: filepath.Join(t.TempDir(), "systemctl"),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	jobRunner := jobs.NewRunner(jobs.Options{
		MaxConcurrent: 4,
		MaxJobs:       16,
		Timeout:       5 * time.Second,
		Retention:     time.Minute,
		Log:           log,
	})
	t.Cleanup(func() {
		if err := jobRunner.Shutdown(5 * time.Second); err != nil {
			t.Errorf("job runner shutdown: %v", err)
		}
	})

	registry := NewRegistry(Dependencies{
		Collector: collectors.New(collectors.Options{ProcRoot: procRoot}),
		Services:  services.NewProvider(runner),
		Jobs:      jobRunner,
		Log:       log,
	})

	return &fixture{registry: registry, jobs: jobRunner, logBuf: &buf, procRoot: procRoot}
}

func request(op protocol.OperationType, payload map[string]any) protocol.Request {
	return protocol.Request{Operation: op, RequestID: "req_1", Payload: payload}
}

func (f *fixture) dispatch(op protocol.OperationType, payload map[string]any) protocol.Response {
	return f.registry.Dispatch(context.Background(), request(op, payload))
}

// wireData re-encodes a response the way the socket transport does.
//
// Handlers that build a map by hand return native Go types, while those going
// through structToMap return JSON types. Asserting against the encoded form
// tests what a caller actually receives, and proves the payload survives
// serialisation at all.
func wireData(t *testing.T, resp protocol.Response) map[string]any {
	t.Helper()

	encoded, err := json.Marshal(resp.Data)
	if err != nil {
		t.Fatalf("response data is not serialisable: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode response data: %v", err)
	}
	return out
}

// ------------------------------------------------------------- allowlist

func TestDispatchPing(t *testing.T) {
	resp := newFixture(t).dispatch(protocol.OperationPing, nil)

	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected success, got %+v", resp)
	}
	if resp.RequestID != "req_1" {
		t.Fatalf("request id must be echoed, got %q", resp.RequestID)
	}
	if resp.Data["pong"] != true {
		t.Fatalf("unexpected ping payload: %+v", resp.Data)
	}
}

func TestDispatchRejectsUnknownOperations(t *testing.T) {
	f := newFixture(t)

	// Command-injection shaped operation strings must be refused by the
	// allowlist before any handler lookup.
	for _, op := range []protocol.OperationType{
		"",
		"shell.exec",
		"agent.ping; rm -rf /",
		"$(cat /etc/shadow)",
		"../../bin/sh",
		"metrics.cpu\nmetrics.cpu",
		"METRICS.CPU",
		"website.create", // a real future operation, not yet registered
	} {
		resp := f.dispatch(op, nil)
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("operation %q must fail, got %+v", op, resp)
		}
		if resp.Error == nil {
			t.Fatalf("operation %q: expected an error", op)
		}
		if op != "" && resp.Error.Code != protocol.CodeUnknownOperation {
			t.Fatalf("operation %q: expected UNKNOWN_OPERATION, got %+v", op, resp.Error)
		}
	}
}

func TestRegisteredOperationsMatchTheAllowlist(t *testing.T) {
	f := newFixture(t)

	registered := make(map[protocol.OperationType]struct{})
	for _, op := range f.registry.Operations() {
		if !protocol.IsAllowed(op) {
			t.Fatalf("registered operation %q is not on the allowlist", op)
		}
		registered[op] = struct{}{}
	}

	// The reverse direction matters too: an allowlisted operation with no
	// handler would be callable and answer with a confusing internal error.
	for _, op := range protocol.AllowedOperations() {
		if _, ok := registered[op]; !ok {
			t.Fatalf("allowlisted operation %q has no handler", op)
		}
	}
}

func TestRegisterRejectsNonAllowlistedOperation(t *testing.T) {
	f := newFixture(t)

	defer func() {
		if recover() == nil {
			t.Fatal("registering a non-allowlisted operation must panic at startup")
		}
	}()
	f.registry.mustRegister("not.allowlisted", f.registry.handlePing)
}

func TestErrorResponsesCarryNoInternalDetail(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch("cat /etc/shadow", nil)
	if resp.Error == nil {
		t.Fatal("expected an error response")
	}
	if resp.Error.Message != "Operation is not permitted" {
		t.Fatalf("error message must be generic, got %q", resp.Error.Message)
	}
}

func TestDispatchRejectsMissingRequestID(t *testing.T) {
	f := newFixture(t)

	resp := f.registry.Dispatch(context.Background(), protocol.Request{Operation: protocol.OperationPing})
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("missing request_id must fail, got %+v", resp)
	}
}

// --------------------------------------------------------------- metrics

func TestMetricsOperations(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		op   protocol.OperationType
		want string
	}{
		{protocol.OperationMetricsCPU, "cores"},
		{protocol.OperationMetricsMemory, "total_bytes"},
		{protocol.OperationMetricsLoad, "load_1"},
		{protocol.OperationMetricsNetwork, "interfaces"},
		{protocol.OperationSystemInfo, "kernel_version"},
	}

	for _, tc := range cases {
		resp := f.dispatch(tc.op, nil)
		if resp.Status != protocol.StatusSuccess {
			t.Fatalf("%s: expected success, got %+v", tc.op, resp.Error)
		}
		if _, ok := resp.Data[tc.want]; !ok {
			t.Fatalf("%s: response is missing %q: %+v", tc.op, tc.want, resp.Data)
		}
	}
}

func TestProcessListOperation(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationProcessList, nil)
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected success, got %+v", resp.Error)
	}

	data := wireData(t, resp)
	count, ok := data["count"].(float64)
	if !ok || count != 1 {
		t.Fatalf("expected exactly the one fixture process, got %+v", data["count"])
	}

	processes, ok := data["processes"].([]any)
	if !ok || len(processes) != 1 {
		t.Fatalf("expected one process entry, got %+v", data["processes"])
	}

	process, ok := processes[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected process shape: %+v", processes[0])
	}
	if process["name"] != "nginx" {
		t.Fatalf("expected the fixture process name, got %+v", process["name"])
	}
	if process["pid"].(float64) != 1234 {
		t.Fatalf("expected pid 1234, got %+v", process["pid"])
	}
	// The command must be reassembled from the null-separated cmdline.
	if process["command"] != "nginx -g daemon off;" {
		t.Fatalf("unexpected command %+v", process["command"])
	}
}

func TestProcessListValidatesPayload(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name    string
		payload map[string]any
	}{
		{"negative limit", map[string]any{"limit": -1}},
		{"unknown sort", map[string]any{"sort_by": "disk"}},
		{"unknown field", map[string]any{"order": "asc"}},
		{"wrong type", map[string]any{"limit": "many"}},
	}

	for _, tc := range cases {
		resp := f.dispatch(protocol.OperationProcessList, tc.payload)
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("%s: must be rejected, got %+v", tc.name, resp)
		}
		if resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("%s: expected INVALID_PAYLOAD, got %q", tc.name, resp.Error.Code)
		}
	}
}

func TestDiskOperationRejectsTraversal(t *testing.T) {
	f := newFixture(t)

	// The mount point is matched against the mount table, but a traversal or
	// null byte is refused outright rather than silently failing to match.
	for _, mountPoint := range []string{
		"../../etc",
		"/var/www/../../etc",
		"relative/path",
		"/tmp/\x00/etc",
	} {
		resp := f.dispatch(protocol.OperationMetricsDisk, map[string]any{"mount_point": mountPoint})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("mount_point %q must be rejected, got %+v", mountPoint, resp)
		}
		if resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("mount_point %q: expected INVALID_PAYLOAD, got %q", mountPoint, resp.Error.Code)
		}
	}
}

func TestDiskOperationRejectsUnmountedPath(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationMetricsDisk, map[string]any{"mount_point": "/not/a/mount"})
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("expected failure, got %+v", resp)
	}
	if resp.Error.Code != protocol.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %q", resp.Error.Code)
	}
}

// --------------------------------------------------------------- services

func TestServiceOperationsDegradeWithoutSystemd(t *testing.T) {
	f := newFixture(t)

	// Without systemd the Agent must say so explicitly rather than returning
	// an internal error that looks like a bug.
	resp := f.dispatch(protocol.OperationServiceStatus, map[string]any{"name": "nginx"})
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("expected failure, got %+v", resp)
	}
	if resp.Error.Code != protocol.CodeUnsupported {
		t.Fatalf("expected UNSUPPORTED, got %q", resp.Error.Code)
	}
}

func TestServiceOperationsValidateNames(t *testing.T) {
	f := newFixture(t)

	// Name validation runs before availability, so an injection-shaped name is
	// refused even on a host that has no systemd at all.
	for _, name := range []string{
		"nginx; rm -rf /",
		"--version",
		"-h",
		"../../etc/passwd",
		"nginx\nrm -rf /",
		"nginx$(id)",
		"",
	} {
		resp := f.dispatch(protocol.OperationServiceStatus, map[string]any{"name": name})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("service name %q must be rejected, got %+v", name, resp)
		}
		if resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("service name %q: expected INVALID_PAYLOAD, got %q", name, resp.Error.Code)
		}
	}
}

func TestServiceListRequiresNames(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationServiceList, map[string]any{"names": []any{}})
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeInvalidPayload {
		t.Fatalf("an empty name list must be rejected, got %+v", resp)
	}
}

// ------------------------------------------------------------------ jobs

func TestAsyncDispatchReturnsAJobID(t *testing.T) {
	f := newFixture(t)

	req := request(protocol.OperationMetricsMemory, nil)
	req.Mode = protocol.ModeAsync

	resp := f.registry.Dispatch(context.Background(), req)
	if resp.Status != protocol.StatusAccepted {
		t.Fatalf("expected ACCEPTED, got %+v", resp)
	}

	jobID, ok := resp.Data["job_id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("expected a job id, got %+v", resp.Data)
	}

	// Poll until the job finishes, the way the API will.
	deadline := time.Now().Add(5 * time.Second)
	for {
		status := f.dispatch(protocol.OperationJobStatus, map[string]any{"job_id": jobID})
		if status.Status != protocol.StatusSuccess {
			t.Fatalf("job.status failed: %+v", status.Error)
		}

		statusData := wireData(t, status)
		state, _ := statusData["state"].(string)
		if protocol.JobState(state).Terminal() {
			if state != string(protocol.JobSuccess) {
				t.Fatalf("job finished as %s: %+v", state, status.Data)
			}
			result, ok := statusData["result"].(map[string]any)
			if !ok || result["total_bytes"] == nil {
				t.Fatalf("job result is missing the metric: %+v", statusData)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish within 5s, last state %q", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestJobControlOperationsCannotBeAsync(t *testing.T) {
	f := newFixture(t)

	// Queueing a job that cancels jobs is both useless and a way to obscure
	// intent in the audit trail.
	for _, op := range []protocol.OperationType{
		protocol.OperationJobStatus,
		protocol.OperationJobCancel,
		protocol.OperationJobList,
	} {
		req := request(op, map[string]any{"job_id": "job_x"})
		req.Mode = protocol.ModeAsync

		resp := f.registry.Dispatch(context.Background(), req)
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("%s must not be async, got %+v", op, resp)
		}
		if resp.Error.Code != protocol.CodeInvalidRequest {
			t.Fatalf("%s: expected INVALID_REQUEST, got %q", op, resp.Error.Code)
		}
	}
}

func TestJobStatusRejectsUnknownJob(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationJobStatus, map[string]any{"job_id": "job_missing"})
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeNotFound {
		t.Fatalf("expected NOT_FOUND, got %+v", resp)
	}
}

func TestJobOperationsValidatePayload(t *testing.T) {
	f := newFixture(t)

	for _, op := range []protocol.OperationType{protocol.OperationJobStatus, protocol.OperationJobCancel} {
		resp := f.dispatch(op, nil)
		if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("%s without job_id must be rejected, got %+v", op, resp)
		}
	}
}

func TestJobListIsEmptyInitially(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationJobList, nil)
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected success, got %+v", resp.Error)
	}
	if count, ok := wireData(t, resp)["count"].(float64); !ok || count != 0 {
		t.Fatalf("expected no jobs, got %+v", resp.Data["count"])
	}
}

// ------------------------------------------------------------ agent.info

func TestAgentInfoAdvertisesCapabilities(t *testing.T) {
	f := newFixture(t)

	resp := f.dispatch(protocol.OperationInfo, nil)
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected success, got %+v", resp.Error)
	}

	data := wireData(t, resp)

	capabilities, ok := data["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("expected capabilities, got %+v", data)
	}
	if capabilities["metrics"] != true {
		t.Fatalf("metrics must be advertised when /proc is readable: %+v", capabilities)
	}
	// systemctl is absent in this fixture, and the Agent must say so rather
	// than let the API discover it one failed call at a time.
	if capabilities["services"] != false {
		t.Fatalf("services must not be advertised without systemd: %+v", capabilities)
	}

	ops, ok := data["operations"].([]any)
	if !ok || len(ops) == 0 {
		t.Fatalf("expected the operation list, got %+v", data["operations"])
	}
}

func TestHandlerFailuresAreLoggedNotReturned(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Level: "debug", Output: &buf})

	// A collector pointed at a directory with no /proc files produces an
	// internal failure; the caller must not learn the filesystem detail.
	registry := NewRegistry(Dependencies{
		Collector: collectors.New(collectors.Options{ProcRoot: filepath.Join(t.TempDir(), "missing")}),
		Services:  services.NewProvider(nil),
		Jobs:      jobs.NewRunner(jobs.Options{Log: log}),
		Log:       log,
	})

	resp := registry.Dispatch(context.Background(), request(protocol.OperationMetricsCPU, nil))
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("expected failure, got %+v", resp)
	}
	if resp.Error.Code != protocol.CodeUnsupported {
		t.Fatalf("expected UNSUPPORTED, got %q", resp.Error.Code)
	}
	if bytes.Contains([]byte(resp.Error.Message), []byte("/proc")) {
		t.Fatalf("filesystem detail leaked to the caller: %q", resp.Error.Message)
	}
}
