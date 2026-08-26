package socket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/audit"
	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

// testToken is a fixed shared secret for tests. It is not a real credential.
const testToken = "0123456789abcdef0123456789abcdef"

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("agent socket transport is Linux-only")
	}
}

// harness bundles a running server with its audit trail.
type harness struct {
	socketPath   string
	auditPath    string
	logBuf       *bytes.Buffer
	server       *Server
	auditRecords func(t *testing.T) []audit.Record
}

type serverOptions struct {
	token       string
	allowedUIDs []int
	// noAudit disables the audit file, for tests that only need the transport.
	noAudit bool
	// operationTimeout overrides the per-operation deadline. It is set before
	// the server starts: mutating cfg on a running server would race with the
	// accept loop.
	operationTimeout time.Duration
}

func startServer(t *testing.T, opts serverOptions) *harness {
	t.Helper()
	requireUnix(t)

	dir := t.TempDir()
	socketPath := filepath.Join(dir, "agent.sock")
	auditPath := filepath.Join(dir, "audit.log")
	if opts.noAudit {
		auditPath = ""
	}

	operationTimeout := opts.operationTimeout
	if operationTimeout <= 0 {
		operationTimeout = 2 * time.Second
	}

	cfg := config.Config{
		SocketPath:       socketPath,
		SocketMode:       0o660,
		LogLevel:         "error",
		OperationTimeout: operationTimeout,
		ShutdownTimeout:  2 * time.Second,
		MaxConcurrent:    4,
	}

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})

	auditWriter, err := audit.NewWriter(audit.Options{Path: auditPath, Log: log})
	if err != nil {
		t.Fatalf("audit writer: %v", err)
	}
	t.Cleanup(func() {
		if err := auditWriter.Close(); err != nil {
			t.Errorf("close audit: %v", err)
		}
	})

	// systemctl points at a path that does not exist, so service operations
	// exercise the degraded path rather than the build machine's init system.
	runner, err := command.NewRunner(command.Spec{
		Name: services.CommandName,
		Path: filepath.Join(dir, "systemctl"),
	})
	if err != nil {
		t.Fatalf("command runner: %v", err)
	}

	jobRunner := jobs.NewRunner(jobs.Options{
		MaxConcurrent: 4,
		Timeout:       5 * time.Second,
		Retention:     time.Minute,
		Log:           log,
	})
	t.Cleanup(func() {
		if err := jobRunner.Shutdown(5 * time.Second); err != nil {
			t.Errorf("job shutdown: %v", err)
		}
	})

	registry := operations.NewRegistry(operations.Dependencies{
		Collector: collectors.New(collectors.Options{ProcRoot: procFixture(t)}),
		Services:  services.NewProvider(runner),
		Jobs:      jobRunner,
		Log:       log,
	})

	srv := New(Options{
		Config:   cfg,
		Log:      log,
		Registry: registry,
		Auth: NewAuthenticator(AuthOptions{
			AllowedUIDs: opts.allowedUIDs,
			Token:       opts.token,
		}),
		Audit: auditWriter,
	})

	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("agent did not shut down within 5s")
		}
	})

	return &harness{
		socketPath: socketPath,
		auditPath:  auditPath,
		logBuf:     &buf,
		server:     srv,
		auditRecords: func(t *testing.T) []audit.Record {
			t.Helper()
			return readAudit(t, auditPath)
		},
	}
}

// procFixture writes a minimal /proc tree so metric operations work.
func procFixture(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"stat":    "cpu 100 0 0 900 0 0 0 0\ncpu0 100 0 0 900 0 0 0 0\n",
		"meminfo": "MemTotal: 1000 kB\nMemAvailable: 500 kB\n",
		"loadavg": "0.10 0.20 0.30 1/10 100\n",
		"uptime":  "1000.0 900.0\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

// readAudit parses the JSON-lines audit file.
func readAudit(t *testing.T, path string) []audit.Record {
	t.Helper()

	if path == "" {
		return nil
	}

	data, err := os.ReadFile(path) //nolint:gosec // test-controlled path
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read audit log: %v", err)
	}

	var records []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var record audit.Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("audit line is not valid JSON: %v (%s)", err, line)
		}
		records = append(records, record)
	}
	return records
}

// send writes a raw payload and returns the raw reply.
func send(t *testing.T, socketPath string, payload []byte) []byte {
	t.Helper()

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		t.Fatalf("write: %v", err)
	}

	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		t.Fatalf("read reply: %v", err)
	}
	return line
}

func sendRequest(t *testing.T, socketPath string, req protocol.Request) protocol.Response {
	t.Helper()

	payload, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var resp protocol.Response
	if err := json.Unmarshal(send(t, socketPath, payload), &resp); err != nil {
		t.Fatalf("reply is not valid JSON: %v", err)
	}
	return resp
}

// ----------------------------------------------------------- transport

func TestPingOverSocket(t *testing.T) {
	h := startServer(t, serverOptions{})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_ping",
	})

	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("expected SUCCESS, got %+v", resp)
	}
	if resp.RequestID != "req_ping" {
		t.Fatalf("request id must be echoed, got %q", resp.RequestID)
	}
}

func TestSocketPermissionsRestrictAccess(t *testing.T) {
	h := startServer(t, serverOptions{})

	info, err := os.Stat(h.socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode = %#o, want 0660 (world access must be denied)", perm)
	}
}

func TestSocketDirectoryIsNotWorldAccessible(t *testing.T) {
	h := startServer(t, serverOptions{})

	info, err := os.Stat(filepath.Dir(h.socketPath))
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Fatalf("socket directory mode = %#o, must grant no world access", perm)
	}
}

func TestAgentDoesNotListenOnTCP(t *testing.T) {
	h := startServer(t, serverOptions{})

	if network := h.server.listener.Addr().Network(); network != "unix" {
		t.Fatalf("agent listener network = %q, want unix", network)
	}
}

func TestServeBeforeListenFails(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})

	srv := New(Options{
		Config: config.Config{MaxConcurrent: 1},
		Log:    log,
		Auth:   NewAuthenticator(AuthOptions{}),
	})
	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve must fail when Listen has not run")
	}
}

func TestListenRefusesToDeleteNonSocketFile(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "important.conf")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(Options{
		Config: config.Config{
			SocketPath:       path,
			SocketMode:       0o660,
			OperationTimeout: time.Second,
			ShutdownTimeout:  time.Second,
			MaxConcurrent:    1,
		},
		Log:  log,
		Auth: NewAuthenticator(AuthOptions{}),
	})

	if err := srv.Listen(); err == nil {
		t.Fatal("Listen must refuse to remove a non-socket file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("existing file must be preserved: %v", err)
	}
}

// -------------------------------------------------------- authentication

func TestTokenIsRequiredWhenConfigured(t *testing.T) {
	h := startServer(t, serverOptions{token: testToken})

	cases := []struct {
		name  string
		token string
	}{
		{"missing", ""},
		{"wrong", "wrong-token-of-the-right-length00"},
		{"prefix", testToken[:16]},
		{"suffix", testToken + "extra"},
		{"empty-ish", " "},
	}

	for _, tc := range cases {
		resp := sendRequest(t, h.socketPath, protocol.Request{
			Operation: protocol.OperationPing,
			RequestID: "req_1",
			Token:     tc.token,
		})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("%s token must be refused, got %+v", tc.name, resp)
		}
		if resp.Error.Code != protocol.CodeUnauthorized {
			t.Fatalf("%s token: expected UNAUTHORIZED, got %q", tc.name, resp.Error.Code)
		}
	}

	// The correct token still works.
	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
		Token:     testToken,
	})
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("the correct token must be accepted, got %+v", resp)
	}
}

func TestUnauthenticatedCallerNeverReachesAHandler(t *testing.T) {
	h := startServer(t, serverOptions{token: testToken})

	// Even a read-only operation must be refused before dispatch: an
	// unauthenticated caller must learn nothing about the host.
	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationSystemInfo,
		RequestID: "req_1",
	})
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeUnauthorized {
		t.Fatalf("expected UNAUTHORIZED, got %+v", resp)
	}
	if len(resp.Data) != 0 {
		t.Fatalf("a refused caller must receive no data, got %+v", resp.Data)
	}
}

func TestRejectionMessageDoesNotRevealWhichCheckFailed(t *testing.T) {
	h := startServer(t, serverOptions{token: testToken})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
		Token:     "wrong-token-of-the-right-length00",
	})

	// Telling a caller whether it failed on UID or on token would help it work
	// out which to attack.
	if resp.Error.Message != "Not authorised" {
		t.Fatalf("rejection message must be uniform, got %q", resp.Error.Message)
	}
}

func TestTokenIsNotEchoedOrLogged(t *testing.T) {
	h := startServer(t, serverOptions{token: testToken})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
		Token:     testToken,
	})

	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if bytes.Contains(encoded, []byte(testToken)) {
		t.Fatalf("the token must not be echoed: %s", encoded)
	}
	if strings.Contains(h.logBuf.String(), testToken) {
		t.Fatalf("the token must not be logged: %s", h.logBuf.String())
	}
	for _, record := range h.auditRecords(t) {
		encoded, err := json.Marshal(record)
		if err != nil {
			t.Fatalf("marshal audit record: %v", err)
		}
		if bytes.Contains(encoded, []byte(testToken)) {
			t.Fatalf("the token must not be audited: %s", encoded)
		}
	}
}

func TestPeerCredentialsAreReadFromTheKernel(t *testing.T) {
	h := startServer(t, serverOptions{})

	sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})

	records := h.auditRecords(t)
	if len(records) == 0 {
		t.Fatal("expected an audit record")
	}

	// The identity comes from the kernel, not from the request body, which is
	// what makes it unforgeable.
	last := records[len(records)-1]
	if last.PeerPID != os.Getpid() {
		t.Fatalf("peer pid = %d, want this process (%d)", last.PeerPID, os.Getpid())
	}
	if last.PeerUID != os.Getuid() {
		t.Fatalf("peer uid = %d, want %d", last.PeerUID, os.Getuid())
	}
}

func TestPeerAllowlistRefusesOtherUIDs(t *testing.T) {
	requireUnix(t)

	// An allowlist naming a UID that is neither root nor the test process must
	// refuse this caller.
	if os.Getuid() == 0 {
		t.Skip("root is always permitted; this test needs an unprivileged UID")
	}

	h := startServer(t, serverOptions{allowedUIDs: []int{os.Getuid() + 1000}})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeUnauthorized {
		t.Fatalf("a non-allowlisted uid must be refused, got %+v", resp)
	}
}

func TestPeerAllowlistAcceptsListedUID(t *testing.T) {
	h := startServer(t, serverOptions{allowedUIDs: []int{os.Getuid()}})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("an allowlisted uid must be accepted, got %+v", resp)
	}
}

func TestAuthenticatorUnitBehaviour(t *testing.T) {
	auth := NewAuthenticator(AuthOptions{AllowedUIDs: []int{1000}, Token: testToken})

	if err := auth.AuthorizePeer(PeerCredentials{UID: 1000}); err != nil {
		t.Fatalf("an allowlisted uid must pass: %v", err)
	}
	// The Agent runs as root and its own health check connects as root.
	if err := auth.AuthorizePeer(PeerCredentials{UID: 0}); err != nil {
		t.Fatalf("root must always pass: %v", err)
	}
	if err := auth.AuthorizePeer(PeerCredentials{UID: 1001}); !errors.Is(err, ErrPeerDenied) {
		t.Fatalf("expected ErrPeerDenied, got %v", err)
	}

	if err := auth.AuthorizeToken(testToken); err != nil {
		t.Fatalf("the correct token must pass: %v", err)
	}
	if err := auth.AuthorizeToken(""); !errors.Is(err, ErrTokenMissing) {
		t.Fatalf("expected ErrTokenMissing, got %v", err)
	}
	if err := auth.AuthorizeToken("nope"); !errors.Is(err, ErrTokenMismatch) {
		t.Fatalf("expected ErrTokenMismatch, got %v", err)
	}

	// With neither configured, both checks pass: socket permissions are then
	// the only boundary, which the daemon warns about at startup.
	open := NewAuthenticator(AuthOptions{})
	if open.RequiresToken() || open.RestrictsPeers() {
		t.Fatal("an unconfigured authenticator must report no checks")
	}
	if err := open.AuthorizePeer(PeerCredentials{UID: 12345}); err != nil {
		t.Fatalf("an unconfigured peer check must pass: %v", err)
	}
	if err := open.AuthorizeToken(""); err != nil {
		t.Fatalf("an unconfigured token check must pass: %v", err)
	}
}

// -------------------------------------------------------------- security

func TestUnknownOperationIsRejected(t *testing.T) {
	h := startServer(t, serverOptions{})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: "website.reticulate",
		RequestID: "req_1",
	})
	if resp.Status != protocol.StatusFailed {
		t.Fatalf("unregistered operation must fail, got %+v", resp)
	}
	if resp.Error.Code != protocol.CodeUnknownOperation {
		t.Fatalf("expected UNKNOWN_OPERATION, got %q", resp.Error.Code)
	}
}

func TestCommandInjectionAttemptsAreRejected(t *testing.T) {
	h := startServer(t, serverOptions{})

	for _, op := range []string{
		"agent.ping; rm -rf /",
		"$(reboot)",
		"agent.ping && id",
		"agent.ping\nagent.ping",
		"../../../bin/sh",
		"metrics.cpu`id`",
	} {
		resp := sendRequest(t, h.socketPath, protocol.Request{
			Operation: protocol.OperationType(op),
			RequestID: "req_1",
		})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("operation %q must be rejected, got %+v", op, resp)
		}
	}
}

func TestServiceNameInjectionIsRejected(t *testing.T) {
	h := startServer(t, serverOptions{})

	// The payload reaches an operation that would otherwise build a systemctl
	// argument list, so this is the end-to-end injection check.
	for _, name := range []string{"nginx; rm -rf /", "--version", "$(id)", "a\nb"} {
		resp := sendRequest(t, h.socketPath, protocol.Request{
			Operation: protocol.OperationServiceStatus,
			RequestID: "req_1",
			Payload:   map[string]any{"name": name},
		})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("service name %q must be rejected, got %+v", name, resp)
		}
		if resp.Error.Code != protocol.CodeInvalidPayload {
			t.Fatalf("service name %q: expected INVALID_PAYLOAD, got %q", name, resp.Error.Code)
		}
	}
}

func TestMalformedRequestIsRejected(t *testing.T) {
	h := startServer(t, serverOptions{})

	var resp protocol.Response
	if err := json.Unmarshal(send(t, h.socketPath, []byte("{not json")), &resp); err != nil {
		t.Fatalf("agent must reply with a structured error, got: %v", err)
	}
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST, got %+v", resp)
	}
}

func TestOversizedRequestIsRejected(t *testing.T) {
	h := startServer(t, serverOptions{})

	// 2 MiB of payload exceeds the 1 MiB read cap.
	huge := `{"operation":"agent.ping","request_id":"req_1","payload":{"x":"` +
		strings.Repeat("A", 2<<20) + `"}}`

	conn, err := net.DialTimeout("unix", h.socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	_, _ = conn.Write([]byte(huge + "\n"))

	line, _ := bufio.NewReader(conn).ReadBytes('\n')
	if len(line) == 0 {
		return
	}
	var resp protocol.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return
	}
	if resp.Status == protocol.StatusSuccess {
		t.Fatal("oversized request must not succeed")
	}
}

func TestOperationTimeoutIsEnforced(t *testing.T) {
	requireUnix(t)

	// A handler that outlives the operation timeout must not hold the
	// connection open indefinitely.
	h := startServer(t, serverOptions{operationTimeout: 100 * time.Millisecond})

	start := time.Now()
	sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationProcessList,
		RequestID: "req_1",
	})
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the operation deadline was not enforced: took %v", elapsed)
	}
}

func TestConcurrentRequests(t *testing.T) {
	h := startServer(t, serverOptions{})

	const clients = 16
	results := make(chan protocol.Status, clients)

	for i := 0; i < clients; i++ {
		go func() {
			conn, err := net.DialTimeout("unix", h.socketPath, 3*time.Second)
			if err != nil {
				results <- protocol.StatusFailed
				return
			}
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

			payload, err := json.Marshal(protocol.Request{
				Operation: protocol.OperationPing,
				RequestID: "req_concurrent",
			})
			if err != nil {
				results <- protocol.StatusFailed
				return
			}
			if _, err := conn.Write(append(payload, '\n')); err != nil {
				results <- protocol.StatusFailed
				return
			}
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil && len(line) == 0 {
				results <- protocol.StatusFailed
				return
			}
			var resp protocol.Response
			if err := json.Unmarshal(line, &resp); err != nil {
				results <- protocol.StatusFailed
				return
			}
			results <- resp.Status
		}()
	}

	for i := 0; i < clients; i++ {
		select {
		case status := <-results:
			if status != protocol.StatusSuccess {
				t.Fatalf("concurrent request %d failed with status %q", i, status)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent requests timed out")
		}
	}
}

// ----------------------------------------------------------------- audit

func TestEveryOperationIsAudited(t *testing.T) {
	h := startServer(t, serverOptions{})

	sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_success",
	})
	sendRequest(t, h.socketPath, protocol.Request{
		Operation: "not.an.operation",
		RequestID: "req_failure",
	})

	records := h.auditRecords(t)
	if len(records) < 2 {
		t.Fatalf("expected both operations to be audited, got %d records", len(records))
	}

	byRequest := make(map[string]audit.Record, len(records))
	for _, record := range records {
		byRequest[record.RequestID] = record
	}

	success, ok := byRequest["req_success"]
	if !ok || success.Status != audit.StatusSuccess {
		t.Fatalf("successful operation not audited correctly: %+v", success)
	}
	if success.Operation != string(protocol.OperationPing) {
		t.Fatalf("audit operation = %q", success.Operation)
	}
	if success.Timestamp == "" {
		t.Fatal("audit records must carry a timestamp")
	}

	// A refused operation matters more than a successful one during an
	// incident, so failures are audited too.
	failure, ok := byRequest["req_failure"]
	if !ok || failure.Status != audit.StatusFailure {
		t.Fatalf("failed operation not audited correctly: %+v", failure)
	}
	if failure.ErrorCode != protocol.CodeUnknownOperation {
		t.Fatalf("audit error code = %q", failure.ErrorCode)
	}
}

func TestDeniedCallersAreAudited(t *testing.T) {
	h := startServer(t, serverOptions{token: testToken})

	sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_denied",
		Token:     "wrong-token-of-the-right-length00",
	})

	records := h.auditRecords(t)
	if len(records) == 0 {
		t.Fatal("a denied caller must be audited")
	}

	last := records[len(records)-1]
	if last.Status != audit.StatusDenied {
		t.Fatalf("status = %q, want DENIED", last.Status)
	}
	if last.Detail["reason"] != "token_rejected" {
		t.Fatalf("the audit trail must record why: %+v", last.Detail)
	}
}

func TestAuditFallsBackWhenTheFileIsUnavailable(t *testing.T) {
	// A misconfigured audit path must not stop the daemon: refusing to start
	// would turn a logging problem into an outage.
	h := startServer(t, serverOptions{noAudit: true})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if resp.Status != protocol.StatusSuccess {
		t.Fatalf("the agent must keep serving without an audit file, got %+v", resp)
	}
	// Records still reach the structured log.
	if !strings.Contains(h.logBuf.String(), "agent_audit") {
		t.Fatalf("audit records must still be logged: %s", h.logBuf.String())
	}
}

func TestAsyncOperationOverSocket(t *testing.T) {
	h := startServer(t, serverOptions{})

	resp := sendRequest(t, h.socketPath, protocol.Request{
		Operation: protocol.OperationMetricsMemory,
		RequestID: "req_async",
		Mode:      protocol.ModeAsync,
	})
	if resp.Status != protocol.StatusAccepted {
		t.Fatalf("expected ACCEPTED, got %+v", resp)
	}

	jobID, ok := resp.Data["job_id"].(string)
	if !ok || jobID == "" {
		t.Fatalf("expected a job id, got %+v", resp.Data)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		status := sendRequest(t, h.socketPath, protocol.Request{
			Operation: protocol.OperationJobStatus,
			RequestID: "req_status",
			Payload:   map[string]any{"job_id": jobID},
		})
		if status.Status != protocol.StatusSuccess {
			t.Fatalf("job.status failed: %+v", status.Error)
		}

		state, _ := status.Data["state"].(string)
		if protocol.JobState(state).Terminal() {
			if state != string(protocol.JobSuccess) {
				t.Fatalf("job finished as %s", state)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not finish, last state %q", state)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The accepted submission is audited with its job id, so an async
	// operation is as traceable as a synchronous one.
	for _, record := range h.auditRecords(t) {
		if record.RequestID == "req_async" {
			if record.Detail["job_id"] != jobID {
				t.Fatalf("audit record must carry the job id: %+v", record.Detail)
			}
			if record.Detail["mode"] != string(protocol.ModeAsync) {
				t.Fatalf("audit record must record the mode: %+v", record.Detail)
			}
			return
		}
	}
	t.Fatal("the async submission was not audited")
}
