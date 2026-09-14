package agentclient

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// How long a call to the Agent is allowed to take.
//
// Every call is bounded - CLAUDE.md section 6 - but the bound used to be the
// configured default and nothing else, applied on top of whatever the caller
// had already decided. A handler that deliberately allowed twenty minutes to
// install a virus scanner was cut off after thirty seconds and reported a
// failure while the install carried on and succeeded.

// slowAgent answers on a Unix socket after a delay.
func slowAgent(t *testing.T, delay time.Duration) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix domain sockets in a temp dir are not available here")
	}

	socket := filepath.Join(t.TempDir(), "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				time.Sleep(delay)
				encoded, _ := json.Marshal(protocol.Response{
					Status:    protocol.StatusSuccess,
					RequestID: "req_test",
				})
				_, _ = conn.Write(append(encoded, '\n'))
			}()
		}
	}()
	return socket
}

func request() protocol.Request {
	return protocol.Request{
		Operation: protocol.OperationSystemInfo,
		RequestID: "req_0123456789abcdef01234567",
	}
}

func TestACallerDeadlineLongerThanTheDefaultIsHonoured(t *testing.T) {
	// The case that was broken. The agent takes longer than the client's own
	// default; a caller that asked for more time must get it.
	client := New(Options{
		SocketPath: slowAgent(t, 250*time.Millisecond),
		Timeout:    50 * time.Millisecond,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, err := client.Do(ctx, request()); err != nil {
		t.Fatalf("a caller's longer deadline was not honoured: %v", err)
	}
}

func TestACallerDeadlineShorterThanTheDefaultStillApplies(t *testing.T) {
	// The other direction, so the change above is "respect the caller" and not
	// "ignore every deadline".
	client := New(Options{
		SocketPath: slowAgent(t, 2*time.Second),
		Timeout:    time.Minute,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	if _, err := client.Do(ctx, request()); err == nil {
		t.Fatal("a call outlived the caller's own deadline")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the call took %s, so the caller's deadline was not applied", elapsed)
	}
}

func TestACallWithNoDeadlineIsStillBounded(t *testing.T) {
	// A caller that sets none gets the configured default. Nothing may wait on
	// the Agent forever: the Agent runs as root and a hung call holds an API
	// request open behind it.
	client := New(Options{
		SocketPath: slowAgent(t, 2*time.Second),
		Timeout:    100 * time.Millisecond,
	})

	started := time.Now()
	if _, err := client.Do(context.Background(), request()); err == nil {
		t.Fatal("a call with no deadline was not bounded by the default")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("the call took %s, so the default did not apply", elapsed)
	}
}
