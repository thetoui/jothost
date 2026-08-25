package socket

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

func requireUnix(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("agent socket transport is Linux-only")
	}
}

func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	requireUnix(t)

	socketPath := filepath.Join(t.TempDir(), "agent.sock")
	cfg := config.Config{
		SocketPath:       socketPath,
		SocketMode:       0o660,
		LogLevel:         "error",
		OperationTimeout: 2 * time.Second,
		ShutdownTimeout:  2 * time.Second,
		MaxConcurrent:    4,
	}

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(cfg, log, operations.NewRegistry(log))

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

	return srv, socketPath
}

// send writes a raw payload to the socket and returns the raw reply.
func send(t *testing.T, socketPath string, payload []byte) []byte {
	t.Helper()

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial agent: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
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

func TestPingOverSocket(t *testing.T) {
	_, socketPath := startServer(t)

	resp := sendRequest(t, socketPath, protocol.Request{
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
	_, socketPath := startServer(t)

	info, err := os.Stat(socketPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o660 {
		t.Fatalf("socket mode = %#o, want 0660 (world access must be denied)", perm)
	}
}

func TestSocketDirectoryIsNotWorldAccessible(t *testing.T) {
	_, socketPath := startServer(t)

	info, err := os.Stat(filepath.Dir(socketPath))
	if err != nil {
		t.Fatalf("stat socket directory: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o007 != 0 {
		t.Fatalf("socket directory mode = %#o, must grant no world access", perm)
	}
}

func TestMissingSocketGroupIsNotFatal(t *testing.T) {
	requireUnix(t)

	// An install where the shared group does not exist must still start: the
	// socket simply keeps the Agent's own group, which is more restrictive.
	socketPath := filepath.Join(t.TempDir(), "agent.sock")

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(config.Config{
		SocketPath:       socketPath,
		SocketMode:       0o660,
		SocketGroup:      "definitely-not-a-real-group",
		OperationTimeout: time.Second,
		ShutdownTimeout:  time.Second,
		MaxConcurrent:    1,
	}, log, operations.NewRegistry(log))

	if err := srv.Listen(); err != nil {
		t.Fatalf("a missing socket group must not prevent startup: %v", err)
	}
	defer func() { _ = srv.listener.Close() }()

	if !strings.Contains(buf.String(), "socket group not found") {
		t.Fatalf("a missing socket group must be logged: %s", buf.String())
	}
}

func TestAgentDoesNotListenOnTCP(t *testing.T) {
	srv, _ := startServer(t)

	if network := srv.listener.Addr().Network(); network != "unix" {
		t.Fatalf("agent listener network = %q, want unix", network)
	}
}

func TestUnknownOperationIsRejected(t *testing.T) {
	_, socketPath := startServer(t)

	resp := sendRequest(t, socketPath, protocol.Request{
		Operation: "website.create",
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
	_, socketPath := startServer(t)

	for _, op := range []string{
		"agent.ping; rm -rf /",
		"$(reboot)",
		"agent.ping && id",
		"agent.ping\nagent.ping",
		"../../../bin/sh",
	} {
		resp := sendRequest(t, socketPath, protocol.Request{
			Operation: protocol.OperationType(op),
			RequestID: "req_1",
		})
		if resp.Status != protocol.StatusFailed {
			t.Fatalf("operation %q must be rejected, got %+v", op, resp)
		}
	}
}

func TestMalformedRequestIsRejected(t *testing.T) {
	_, socketPath := startServer(t)

	var resp protocol.Response
	if err := json.Unmarshal(send(t, socketPath, []byte("{not json")), &resp); err != nil {
		t.Fatalf("agent must reply with a structured error, got: %v", err)
	}
	if resp.Status != protocol.StatusFailed || resp.Error.Code != protocol.CodeInvalidRequest {
		t.Fatalf("expected INVALID_REQUEST, got %+v", resp)
	}
}

func TestOversizedRequestIsRejected(t *testing.T) {
	_, socketPath := startServer(t)

	// 2 MiB of payload exceeds the 1 MiB read cap.
	huge := `{"operation":"agent.ping","request_id":"req_1","payload":{"x":"` +
		strings.Repeat("A", 2<<20) + `"}}`

	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}

	// A short write is acceptable: the server stops reading at the cap.
	_, _ = conn.Write([]byte(huge + "\n"))

	line, _ := bufio.NewReader(conn).ReadBytes('\n')
	if len(line) == 0 {
		// A closed connection without a reply is an acceptable rejection.
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

func TestListenRefusesToDeleteNonSocketFile(t *testing.T) {
	requireUnix(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "important.conf")
	if err := os.WriteFile(path, []byte("do not delete"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(config.Config{
		SocketPath:       path,
		SocketMode:       0o660,
		OperationTimeout: time.Second,
		ShutdownTimeout:  time.Second,
		MaxConcurrent:    1,
	}, log, operations.NewRegistry(log))

	if err := srv.Listen(); err == nil {
		t.Fatal("Listen must refuse to remove a non-socket file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("existing file must be preserved: %v", err)
	}
}

func TestListenReplacesStaleSocket(t *testing.T) {
	requireUnix(t)

	socketPath := filepath.Join(t.TempDir(), "agent.sock")

	// Simulate a socket left behind by an unclean shutdown. Closing a Go unix
	// listener unlinks the socket, so it is recreated explicitly afterwards.
	stale, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	if err := stale.Close(); err != nil {
		t.Fatalf("close stale listener: %v", err)
	}
	stale2, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("recreate stale socket: %v", err)
	}
	defer func() { _ = stale2.Close() }()

	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(config.Config{
		SocketPath:       socketPath,
		SocketMode:       0o660,
		OperationTimeout: time.Second,
		ShutdownTimeout:  time.Second,
		MaxConcurrent:    1,
	}, log, operations.NewRegistry(log))

	if err := srv.Listen(); err != nil {
		t.Fatalf("Listen must replace a stale socket: %v", err)
	}
	if err := srv.listener.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestServeBeforeListenFails(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(logger.Options{Service: "agent", Output: &buf})
	srv := New(config.Config{MaxConcurrent: 1}, log, operations.NewRegistry(log))

	if err := srv.Serve(context.Background()); err == nil {
		t.Fatal("Serve must fail when Listen has not run")
	}
}

func TestConcurrentRequests(t *testing.T) {
	_, socketPath := startServer(t)

	const clients = 16
	results := make(chan protocol.Status, clients)

	for i := 0; i < clients; i++ {
		go func() {
			conn, err := net.DialTimeout("unix", socketPath, 3*time.Second)
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
