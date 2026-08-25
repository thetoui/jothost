package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// fakeAgent starts a Unix socket server that replies with reply for every
// request. A nil reply closes the connection without answering.
func fakeAgent(t *testing.T, reply func(protocol.Request) []byte) string {
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
				_ = json.Unmarshal([]byte(strings.TrimSpace(string(buf[:n]))), &req)

				out := reply(req)
				if out == nil {
					return
				}
				_, _ = c.Write(append(out, '\n'))
			}(conn)
		}
	}()

	return socketPath
}

func successReply(req protocol.Request) []byte {
	out, _ := json.Marshal(protocol.NewSuccess(req.RequestID, map[string]any{"pong": true}))
	return out
}

func TestPingSucceeds(t *testing.T) {
	socketPath := fakeAgent(t, successReply)

	if err := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second}).Ping(context.Background(), "req_1"); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}

func TestDoEchoesRequestID(t *testing.T) {
	socketPath := fakeAgent(t, successReply)

	resp, err := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second}).Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_echo",
	})
	if err != nil {
		t.Fatalf("Do returned error: %v", err)
	}
	if resp.RequestID != "req_echo" {
		t.Fatalf("expected request id to round-trip, got %q", resp.RequestID)
	}
}

func TestDoValidatesBeforeDialing(t *testing.T) {
	// The client must refuse a non-allowlisted operation without opening a
	// connection to the privileged process at all.
	client := New(Options{SocketPath: "/nonexistent/agent.sock", Timeout: time.Second})

	_, err := client.Do(context.Background(), protocol.Request{
		Operation: "shell.exec",
		RequestID: "req_1",
	})
	if err == nil {
		t.Fatal("non-allowlisted operation must be rejected client-side")
	}
	if !errors.Is(err, protocol.ErrUnknownOperation) {
		t.Fatalf("expected ErrUnknownOperation, got %v", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatal("client must not attempt to dial an invalid operation")
	}
}

func TestDoReportsUnavailableSocket(t *testing.T) {
	client := New(Options{SocketPath: "/nonexistent/agent.sock", Timeout: time.Second})

	_, err := client.Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestPingFailsOnAgentError(t *testing.T) {
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		out, _ := json.Marshal(protocol.NewError(req.RequestID, protocol.CodeInternal, "Operation failed"))
		return out
	})

	err := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second}).Ping(context.Background(), "req_1")
	if err == nil {
		t.Fatal("a FAILED response must surface as an error")
	}
}

func TestDoTimesOutOnSilentAgent(t *testing.T) {
	// An agent that accepts the connection but never replies must not hang
	// the API: the configured timeout has to bound the exchange.
	socketPath := fakeAgent(t, func(protocol.Request) []byte {
		time.Sleep(3 * time.Second)
		return nil
	})

	start := time.Now()
	_, err := New(Options{SocketPath: socketPath, Timeout: 300 * time.Millisecond}).Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("timeout was not enforced: took %v", elapsed)
	}
}

func TestDoRejectsOversizedResponse(t *testing.T) {
	// A compromised or buggy agent must not be able to exhaust API memory.
	socketPath := fakeAgent(t, func(protocol.Request) []byte {
		return []byte(strings.Repeat("A", (1<<20)+1024))
	})

	_, err := New(Options{SocketPath: socketPath, Timeout: 3 * time.Second}).Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if err == nil {
		t.Fatal("oversized agent response must be rejected")
	}
}

func TestDoRejectsMalformedResponse(t *testing.T) {
	socketPath := fakeAgent(t, func(protocol.Request) []byte {
		return []byte("{not json")
	})

	_, err := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second}).Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if err == nil {
		t.Fatal("malformed agent response must be rejected")
	}
}

func TestTokenIsAttachedToEveryRequest(t *testing.T) {
	// The client attaches the secret so no call site can forget it and no
	// caller needs to hold it.
	received := make(chan string, 1)
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		received <- req.Token
		return successReply(req)
	})

	client := New(Options{
		SocketPath: socketPath,
		Timeout:    2 * time.Second,
		Token:      "0123456789abcdef0123456789abcdef",
	})
	if err := client.Ping(context.Background(), "req_1"); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	select {
	case token := <-received:
		if token != "0123456789abcdef0123456789abcdef" {
			t.Fatalf("agent received token %q", token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the agent never received a request")
	}
}

func TestTokenOverridesACallerSuppliedValue(t *testing.T) {
	// A caller must not be able to substitute its own token: the client's
	// configured secret is authoritative.
	received := make(chan string, 1)
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		received <- req.Token
		return successReply(req)
	})

	client := New(Options{
		SocketPath: socketPath,
		Timeout:    2 * time.Second,
		Token:      "configured-token-0123456789abcdef",
	})
	if _, err := client.Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
		Token:     "caller-supplied-token",
	}); err != nil {
		t.Fatalf("Do: %v", err)
	}

	if token := <-received; token != "configured-token-0123456789abcdef" {
		t.Fatalf("the configured token must win, got %q", token)
	}
}

func TestTypedWrappersDecodeResults(t *testing.T) {
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		var data map[string]any

		switch req.Operation {
		case protocol.OperationMetricsMemory:
			data = map[string]any{"total_bytes": 16384, "used_percent": 25.5}
		case protocol.OperationMetricsCPU:
			data = map[string]any{"usage_percent": 12.5, "cores": 4}
		case protocol.OperationInfo:
			data = map[string]any{
				"version":      "0.1.0-dev",
				"capabilities": map[string]any{"metrics": true, "services": false},
			}
		default:
			data = map[string]any{}
		}

		out, _ := json.Marshal(protocol.NewSuccess(req.RequestID, data))
		return out
	})

	client := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second})
	ctx := context.Background()

	memory, err := client.Memory(ctx, "req_1")
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	if memory.TotalBytes != 16384 || memory.UsedPercent != 25.5 {
		t.Fatalf("unexpected memory result: %+v", memory)
	}

	cpu, err := client.CPU(ctx, "req_2")
	if err != nil {
		t.Fatalf("CPU: %v", err)
	}
	if cpu.UsagePercent != 12.5 || cpu.Cores != 4 {
		t.Fatalf("unexpected cpu result: %+v", cpu)
	}

	info, err := client.Info(ctx, "req_3")
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	if !info.Capabilities["metrics"] || info.Capabilities["services"] {
		t.Fatalf("unexpected capabilities: %+v", info.Capabilities)
	}
}

func TestOperationFailureCarriesTheCode(t *testing.T) {
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		out, _ := json.Marshal(protocol.NewError(req.RequestID,
			protocol.CodeUnsupported, "This metric is not available on this host"))
		return out
	})

	client := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second})

	_, err := client.CPU(context.Background(), "req_1")
	if err == nil {
		t.Fatal("a FAILED response must surface as an error")
	}

	var failed *ErrOperationFailed
	if !errors.As(err, &failed) {
		t.Fatalf("expected ErrOperationFailed, got %T", err)
	}
	if failed.Code != protocol.CodeUnsupported {
		t.Fatalf("code = %q", failed.Code)
	}
	// Callers hide a feature rather than showing an error when the host simply
	// cannot do it.
	if !IsUnsupported(err) {
		t.Fatal("IsUnsupported must recognise an UNSUPPORTED failure")
	}
}

func TestSubmitAsyncReturnsAJobID(t *testing.T) {
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		if !req.Async() {
			out, _ := json.Marshal(protocol.NewError(req.RequestID, protocol.CodeInvalidRequest, "expected async"))
			return out
		}
		out, _ := json.Marshal(protocol.NewAccepted(req.RequestID, "job_abc123"))
		return out
	})

	client := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second})

	jobID, err := client.SubmitAsync(context.Background(), "req_1", protocol.OperationMetricsDisk, nil)
	if err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	if jobID != "job_abc123" {
		t.Fatalf("job id = %q", jobID)
	}
}

func TestSubmitAsyncRejectsAMissingJobID(t *testing.T) {
	socketPath := fakeAgent(t, func(req protocol.Request) []byte {
		// ACCEPTED without a job id leaves the caller unable to poll, so it is
		// an error rather than a silent success.
		out, _ := json.Marshal(protocol.Response{
			Status:    protocol.StatusAccepted,
			RequestID: req.RequestID,
		})
		return out
	})

	client := New(Options{SocketPath: socketPath, Timeout: 2 * time.Second})

	if _, err := client.SubmitAsync(context.Background(), "req_1", protocol.OperationMetricsDisk, nil); err == nil {
		t.Fatal("an accepted response without a job id must be rejected")
	}
}
