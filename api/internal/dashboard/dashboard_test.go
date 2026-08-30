package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

// agentReply decides what a fake Agent answers for one operation.
type agentReply func(req protocol.Request) protocol.Response

// fakeAgent starts a Unix socket Agent that answers with reply.
func fakeAgent(t *testing.T, reply agentReply) *agentclient.Client {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix socket transport is Linux-only")
	}

	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()

				buf := make([]byte, 8192)
				n, err := c.Read(buf)
				if err != nil && n == 0 {
					return
				}

				var req protocol.Request
				if err := json.Unmarshal(bytes.TrimSpace(buf[:n]), &req); err != nil {
					return
				}

				out, err := json.Marshal(reply(req))
				if err != nil {
					return
				}
				_, _ = c.Write(append(out, '\n'))
			}(conn)
		}
	}()

	return agentclient.New(agentclient.Options{SocketPath: socketPath, Timeout: 3 * time.Second})
}

// healthyAgent answers every Phase 2 operation with plausible values.
func healthyAgent(t *testing.T) *agentclient.Client {
	t.Helper()

	return fakeAgent(t, func(req protocol.Request) protocol.Response {
		var data map[string]any

		switch req.Operation {
		case protocol.OperationSystemInfo:
			data = map[string]any{
				"hostname": "web01", "os_name": "Debian GNU/Linux", "os_version": "12",
				"kernel_version": "6.1.0", "architecture": "amd64",
				"uptime_seconds": 86400, "cores": 4,
			}
		case protocol.OperationMetricsCPU:
			data = map[string]any{"usage_percent": 12.5, "cores": 4}
		case protocol.OperationMetricsMemory:
			data = map[string]any{
				"total_bytes": 16000000000, "used_percent": 40.0,
				"swap_total_bytes": 4000000000, "swap_used_percent": 10.0,
			}
		case protocol.OperationMetricsDisk:
			data = map[string]any{
				"filesystems": []any{
					map[string]any{"mount_point": "/", "used_percent": 45.0, "total_bytes": 100},
				},
				"total_bytes": 100, "used_bytes": 45,
			}
		case protocol.OperationMetricsNetwork:
			data = map[string]any{"interfaces": []any{}, "total_rx_bytes": 1000, "total_tx_bytes": 500}
		case protocol.OperationMetricsLoad:
			data = map[string]any{"load_1": 0.5, "cores": 4, "load_per_core": 0.125}
		case protocol.OperationServiceDetect:
			// What the Agent's detection returns: services the host actually
			// has, with the state it actually has them in.
			data = map[string]any{
				"services": []any{
					map[string]any{
						"key": "nginx", "label": "nginx", "role": "web",
						"installed": true, "running": true, "essential": true,
						"active_state": "active", "controllable": true,
					},
				},
				"count":        1,
				"controllable": true,
			}
		default:
			data = map[string]any{}
		}

		return protocol.NewSuccess(req.RequestID, data)
	})
}

// fixture bundles a dashboard service with a registered server.
type fixture struct {
	service  *Service
	serverID string
	ctx      context.Context
	logBuf   *bytes.Buffer
}

func newFixture(t *testing.T, agent *agentclient.Client, opts ...func(*Options)) *fixture {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	serverRepo := servers.NewRepository(deps.Pool)
	server, err := serverRepo.Register(ctx, servers.RegisterParams{Hostname: "web01"})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}

	var buf bytes.Buffer
	options := Options{
		Agent:        agent,
		Servers:      serverRepo,
		Log:          logger.New(logger.Options{Service: "api", Level: "error", Output: &buf}),
		Dependencies: []string{"postgres"},
		CheckDependency: func(context.Context, string) bool {
			return true
		},
		Now: func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	for _, opt := range opts {
		opt(&options)
	}

	return &fixture{
		service:  NewService(options),
		serverID: server.ID,
		ctx:      ctx,
		logBuf:   &buf,
	}
}

func (f *fixture) snapshot(t *testing.T) Snapshot {
	t.Helper()

	snapshot, err := f.service.Snapshot(f.ctx, f.serverID, "req_1")
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	return snapshot
}

// ------------------------------------------------------------- happy path

func TestSnapshotFillsEveryWidget(t *testing.T) {
	f := newFixture(t, healthyAgent(t))
	snapshot := f.snapshot(t)

	if snapshot.Server.Hostname != "web01" {
		t.Fatalf("unexpected server: %+v", snapshot.Server)
	}
	if snapshot.GeneratedAt == "" {
		t.Fatal("a snapshot must carry its collection time")
	}

	cases := []struct {
		name      string
		available bool
	}{
		{"system", snapshot.System.Available},
		{"cpu", snapshot.CPU.Available},
		{"memory", snapshot.Memory.Available},
		{"disk", snapshot.Disk.Available},
		{"network", snapshot.Network.Available},
		{"load", snapshot.Load.Available},
		{"services", snapshot.Services.Available},
	}
	for _, tc := range cases {
		if !tc.available {
			t.Fatalf("%s widget must be available", tc.name)
		}
	}

	if snapshot.CPU.Data.UsagePercent != 12.5 {
		t.Fatalf("cpu = %v", snapshot.CPU.Data.UsagePercent)
	}
	if snapshot.Memory.Data.UsedPercent != 40 {
		t.Fatalf("memory = %v", snapshot.Memory.Data.UsedPercent)
	}
}

func TestHealthySnapshotRaisesNoAlerts(t *testing.T) {
	f := newFixture(t, healthyAgent(t))
	snapshot := f.snapshot(t)

	if len(snapshot.Alerts) != 0 {
		t.Fatalf("a healthy host must raise no alerts, got %+v", snapshot.Alerts)
	}
	// A nil slice would serialise as null and break a client expecting a list.
	if snapshot.Alerts == nil {
		t.Fatal("Alerts must be a non-nil slice")
	}
}

func TestServicesCombineUnitsAndDependencies(t *testing.T) {
	f := newFixture(t, healthyAgent(t))
	snapshot := f.snapshot(t)

	states := *snapshot.Services.Data
	if len(states) != 2 {
		t.Fatalf("expected a dependency and a unit, got %+v", states)
	}

	kinds := map[string]string{}
	for _, state := range states {
		kinds[state.Name] = state.Kind
	}
	// The two kinds fail for different reasons, so they stay distinguishable.
	if kinds["postgres"] != KindDependency {
		t.Fatalf("postgres kind = %q", kinds["postgres"])
	}
	if kinds["nginx"] != KindSystemd {
		t.Fatalf("nginx kind = %q", kinds["nginx"])
	}
}

// ------------------------------------------------------------ degradation

func TestSnapshotSurvivesAnUnreachableAgent(t *testing.T) {
	// The dashboard is what an operator opens when the host is misbehaving, so
	// an unreachable Agent must still produce a page.
	agent := agentclient.New(agentclient.Options{
		SocketPath: "/nonexistent/agent.sock",
		Timeout:    time.Second,
	})
	f := newFixture(t, agent)

	snapshot := f.snapshot(t)

	if snapshot.System.Available {
		t.Fatal("the system widget cannot be available without an agent")
	}
	if snapshot.System.Error != "agent unreachable" {
		t.Fatalf("the widget must say why: %q", snapshot.System.Error)
	}
	// The server record comes from the database, so it is still present.
	if snapshot.Server.Hostname != "web01" {
		t.Fatal("the server record must still be reported")
	}

	// An unreachable agent is the alert that explains every empty panel.
	found := false
	for _, alert := range snapshot.Alerts {
		if alert.Category == "agent" && alert.Severity == SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unreachable agent must raise an alert: %+v", snapshot.Alerts)
	}
}

func TestOneFailingWidgetDoesNotAffectTheOthers(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationMetricsDisk {
			return protocol.NewError(req.RequestID, protocol.CodeInternal, "Operation failed")
		}
		if req.Operation == protocol.OperationMetricsMemory {
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"total_bytes": 100, "used_percent": 42.0,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	if snapshot.Disk.Available {
		t.Fatal("the failing widget must report unavailable")
	}
	if !snapshot.Memory.Available || snapshot.Memory.Data.UsedPercent != 42 {
		t.Fatalf("a healthy widget must still be filled: %+v", snapshot.Memory)
	}
}

func TestUnsupportedMetricIsDistinctFromAFailure(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationMetricsDisk {
			return protocol.NewError(req.RequestID, protocol.CodeUnsupported,
				"This metric is not available on this host")
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	// "The host cannot do this" is a different state from "this broke", and a
	// UI should render them differently.
	if !snapshot.Disk.Unsupported {
		t.Fatalf("expected the widget to report unsupported: %+v", snapshot.Disk)
	}
	if snapshot.Disk.Available {
		t.Fatal("an unsupported widget has no data")
	}
}

func TestServicesDegradeWithoutSystemd(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationServiceDetect {
			return protocol.NewError(req.RequestID, protocol.CodeUnsupported,
				"No service manager is available on this host")
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	// The dependencies the API checks itself are still worth showing, so the
	// widget reports what it has rather than nothing.
	if !snapshot.Services.Available {
		t.Fatalf("the widget must still carry its dependencies: %+v", snapshot.Services)
	}
	if !snapshot.Services.Unsupported {
		t.Fatal("the widget must record that units are unavailable")
	}

	states := *snapshot.Services.Data
	if len(states) != 1 || states[0].Name != "postgres" {
		t.Fatalf("expected only the dependency, got %+v", states)
	}
}

func TestWidgetErrorsCarryNoInternalDetail(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		return protocol.NewError(req.RequestID, protocol.CodeInternal,
			"dial /var/run/secret.sock: permission denied")
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	// The Agent's message may name paths; the dashboard reports a fixed
	// summary rather than passing it through.
	if snapshot.CPU.Error != "could not be collected" {
		t.Fatalf("widget error must be generic, got %q", snapshot.CPU.Error)
	}
}

// ----------------------------------------------------------------- alerts

func TestDiskAlertsArePerFilesystem(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationMetricsDisk {
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"filesystems": []any{
					// A nearly full /var beside a large idle /home: the
					// aggregate would look fine, which is why the check is
					// per filesystem.
					map[string]any{"mount_point": "/var", "used_percent": 95.0},
					map[string]any{"mount_point": "/home", "used_percent": 5.0},
				},
				"total_bytes": 1000, "used_bytes": 200,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	var diskAlerts []Alert
	for _, alert := range snapshot.Alerts {
		if alert.Category == "disk" {
			diskAlerts = append(diskAlerts, alert)
		}
	}

	if len(diskAlerts) != 1 {
		t.Fatalf("expected exactly the full filesystem to alert, got %+v", diskAlerts)
	}
	if diskAlerts[0].Severity != SeverityCritical {
		t.Fatalf("95%% must be critical, got %q", diskAlerts[0].Severity)
	}
	if diskAlerts[0].Message != "Disk /var is 95% full" {
		t.Fatalf("unexpected message: %q", diskAlerts[0].Message)
	}
}

func TestThresholdsSeparateWarningFromCritical(t *testing.T) {
	cases := []struct {
		used     float64
		severity string
		alerts   bool
	}{
		{used: 50, alerts: false},
		{used: 79.9, alerts: false},
		{used: 80, severity: SeverityWarning, alerts: true},
		{used: 89.9, severity: SeverityWarning, alerts: true},
		{used: 90, severity: SeverityCritical, alerts: true},
		{used: 99.9, severity: SeverityCritical, alerts: true},
	}

	for _, tc := range cases {
		used := tc.used
		agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
			if req.Operation == protocol.OperationMetricsDisk {
				return protocol.NewSuccess(req.RequestID, map[string]any{
					"filesystems": []any{
						map[string]any{"mount_point": "/", "used_percent": used},
					},
				})
			}
			return protocol.NewSuccess(req.RequestID, map[string]any{})
		})

		f := newFixture(t, agent)
		snapshot := f.snapshot(t)

		var found *Alert
		for i, alert := range snapshot.Alerts {
			if alert.Category == "disk" {
				found = &snapshot.Alerts[i]
			}
		}

		if !tc.alerts {
			if found != nil {
				t.Fatalf("%.1f%% must not alert, got %+v", used, *found)
			}
			continue
		}
		if found == nil {
			t.Fatalf("%.1f%% must alert", used)
		}
		if found.Severity != tc.severity {
			t.Fatalf("%.1f%%: severity = %q, want %q", used, found.Severity, tc.severity)
		}
	}
}

func TestMemoryAndSwapAlerts(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationMetricsMemory {
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"total_bytes": 1000, "used_percent": 97.0,
				"swap_total_bytes": 1000, "swap_used_percent": 60.0,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	var memory, swap bool
	for _, alert := range snapshot.Alerts {
		if alert.Category != "memory" {
			continue
		}
		if alert.Severity == SeverityCritical {
			memory = true
		}
		// Swap in use on a server usually means pressure, not healthy
		// overcommit, so it is surfaced separately.
		if alert.Message == "Swap is 60% used" {
			swap = true
		}
	}

	if !memory {
		t.Fatalf("97%% memory must raise a critical alert: %+v", snapshot.Alerts)
	}
	if !swap {
		t.Fatalf("swap in use must raise its own alert: %+v", snapshot.Alerts)
	}
}

func TestLoadAlertIsPerCore(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationMetricsLoad {
			// A load of 32 on a 64-core machine is half a core each: busy, not
			// a problem. Alerting on the raw figure would fire constantly on
			// large hosts.
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"load_1": 32.0, "cores": 64, "load_per_core": 0.5,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	for _, alert := range snapshot.Alerts {
		if alert.Category == "cpu" {
			t.Fatalf("a load of 0.5 per core must not alert: %+v", alert)
		}
	}
}

func TestStoppedServiceRaisesACriticalAlert(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationServiceDetect {
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"services": []any{
					map[string]any{"key": "nginx", "label": "nginx",
						"installed": true, "running": false,
						"active_state": "inactive", "essential": true},
				},
				"controllable": true,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	found := false
	for _, alert := range snapshot.Alerts {
		if alert.Category == "service" && alert.Message == "nginx is stopped" {
			found = true
			if alert.Severity != SeverityCritical {
				t.Fatalf("a stopped service must be critical, got %q", alert.Severity)
			}
		}
	}
	if !found {
		t.Fatalf("expected a service alert: %+v", snapshot.Alerts)
	}
}

func TestNotInstalledServiceDoesNotAlert(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationServiceDetect {
			// Detection leaves out what the host does not have, so an absent
			// service reaches the dashboard as nothing at all rather than as a
			// row saying "not installed".
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"services":     []any{},
				"controllable": true,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	// An optional unit that is simply absent is a configuration, not a fault;
	// alerting would fire permanently on every host without it.
	for _, alert := range snapshot.Alerts {
		if alert.Category == "service" {
			t.Fatalf("an absent unit must not alert: %+v", alert)
		}
	}
}

func TestUnreachableDependencyRaisesAnAlert(t *testing.T) {
	f := newFixture(t, healthyAgent(t), func(o *Options) {
		o.CheckDependency = func(context.Context, string) bool { return false }
	})

	snapshot := f.snapshot(t)

	found := false
	for _, alert := range snapshot.Alerts {
		if alert.Category == "service" && alert.Message == "postgres is unreachable" {
			found = true
		}
	}
	if !found {
		t.Fatalf("an unreachable dependency must alert: %+v", snapshot.Alerts)
	}
}

func TestSnapshotRejectsAnUnknownServer(t *testing.T) {
	f := newFixture(t, healthyAgent(t))

	_, err := f.service.Snapshot(f.ctx, "00000000-0000-0000-0000-000000000000", "req_1")
	if err == nil {
		t.Fatal("an unknown server must be reported")
	}
}

func TestTrimFloat(t *testing.T) {
	cases := map[float64]string{
		95:      "95",
		95.04:   "95",
		95.25:   "95.2",
		0.5:     "0.5",
		100:     "100",
		12.3456: "12.3",
	}
	for input, want := range cases {
		if got := trimFloat(input); got != want {
			t.Fatalf("trimFloat(%v) = %q, want %q", input, got, want)
		}
	}
}

// An alert that is always on is one nobody reads.
//
// Cron is legitimately stopped on a host with no scheduled work, and Apache is
// deliberately stopped whenever the host serves everything from nginx. Neither
// is a fault, and neither may raise a critical alert.
func TestAStoppedNonEssentialServiceDoesNotAlert(t *testing.T) {
	agent := fakeAgent(t, func(req protocol.Request) protocol.Response {
		if req.Operation == protocol.OperationServiceDetect {
			return protocol.NewSuccess(req.RequestID, map[string]any{
				"services": []any{
					map[string]any{"key": "cron", "label": "Cron", "role": "system",
						"installed": true, "running": false,
						"active_state": "inactive", "essential": false},
				},
				"controllable": true,
			})
		}
		return protocol.NewSuccess(req.RequestID, map[string]any{})
	})

	f := newFixture(t, agent)
	snapshot := f.snapshot(t)

	for _, alert := range snapshot.Alerts {
		if alert.Category == "service" {
			t.Fatalf("a stopped optional service must not alert: %+v", alert)
		}
	}
	// It is still listed: an operator wants to see that it is down, they just
	// do not want to be paged about it.
	if snapshot.Services.Data == nil || len(*snapshot.Services.Data) == 0 {
		t.Fatal("the service was left out of the listing entirely")
	}
}
