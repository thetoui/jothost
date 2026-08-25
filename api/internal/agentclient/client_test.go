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

	if err := New(socketPath, 2*time.Second).Ping(context.Background(), "req_1"); err != nil {
		t.Fatalf("Ping returned error: %v", err)
	}
}

func TestDoEchoesRequestID(t *testing.T) {
	socketPath := fakeAgent(t, successReply)

	resp, err := New(socketPath, 2*time.Second).Do(context.Background(), protocol.Request{
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
	client := New("/nonexistent/agent.sock", time.Second)

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
	client := New("/nonexistent/agent.sock", time.Second)

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

	err := New(socketPath, 2*time.Second).Ping(context.Background(), "req_1")
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
	_, err := New(socketPath, 300*time.Millisecond).Do(context.Background(), protocol.Request{
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

	_, err := New(socketPath, 3*time.Second).Do(context.Background(), protocol.Request{
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

	_, err := New(socketPath, 2*time.Second).Do(context.Background(), protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_1",
	})
	if err == nil {
		t.Fatal("malformed agent response must be rejected")
	}
}
