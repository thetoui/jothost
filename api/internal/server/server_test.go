package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
)

func testServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	var buf bytes.Buffer
	return New(cfg, logger.New(logger.Options{Service: "api", Output: &buf}))
}

func baseConfig() config.Config {
	return config.Config{
		Environment:     config.EnvTest,
		HTTPAddr:        "127.0.0.1:0",
		LogLevel:        "error",
		ShutdownTimeout: time.Second,
		ReadTimeout:     time.Second,
		WriteTimeout:    time.Second,
		IdleTimeout:     time.Second,
		DatabaseURL:     "postgres://user:pw@127.0.0.1:1/jothost",
		RedisURL:        "redis://127.0.0.1:1/0",
		AgentSocket:     "/nonexistent/agent.sock",
		AgentTimeout:    time.Second,
	}
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	s := testServer(t, baseConfig())

	for _, path := range []string{"/healthz", "/api/v1/health"} {
		rec := do(t, s, http.MethodGet, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: expected 200, got %d", path, rec.Code)
		}

		var env httpx.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: invalid envelope: %v", path, err)
		}
		if !env.Success {
			t.Fatalf("%s: expected success=true", path)
		}
		if env.RequestID == "" {
			t.Fatalf("%s: response must carry request_id", path)
		}
		if rec.Header().Get(httpx.RequestIDHeader) == "" {
			t.Fatalf("%s: response must echo the request id header", path)
		}
	}
}

func TestHealthEndpointIsLivenessOnly(t *testing.T) {
	// /healthz must not depend on Postgres, Redis, or the Agent: all three are
	// unreachable in baseConfig and it must still return 200.
	rec := do(t, testServer(t, baseConfig()), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must not probe dependencies, got %d", rec.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	rec := do(t, testServer(t, baseConfig()), http.MethodGet, "/api/v1/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "\"version\"") {
		t.Fatalf("version payload missing: %s", rec.Body.String())
	}
}

func TestReadinessFailsWhenDependenciesDown(t *testing.T) {
	rec := do(t, testServer(t, baseConfig()), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when dependencies are down, got %d", rec.Code)
	}

	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}
	if env.Success {
		t.Fatal("readiness must report success=false when a dependency is down")
	}
	if env.Error == nil || env.Error.Code != httpx.CodeUnavailable {
		t.Fatalf("unexpected readiness error: %+v", env.Error)
	}
	// Credentials embedded in DATABASE_URL must never surface in the payload.
	if strings.Contains(rec.Body.String(), "pw@") {
		t.Fatalf("credentials leaked into readiness output: %s", rec.Body.String())
	}
}

func TestReadinessSucceedsWhenDependenciesUp(t *testing.T) {
	pg := listenTCP(t)
	redis := listenTCP(t)
	socket := startFakeAgent(t)

	cfg := baseConfig()
	cfg.DatabaseURL = "postgres://user:pw@" + pg + "/jothost"
	cfg.RedisURL = "redis://" + redis + "/0"
	cfg.AgentSocket = socket

	rec := do(t, testServer(t, cfg), http.MethodGet, "/readyz")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when all dependencies are up, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestUnknownRouteReturnsEnvelope(t *testing.T) {
	rec := do(t, testServer(t, baseConfig()), http.MethodGet, "/api/v1/does-not-exist")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}

	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("404 must return the standard envelope, got %s", rec.Body.String())
	}
	if env.Error == nil || env.Error.Code != httpx.CodeNotFound {
		t.Fatalf("unexpected 404 body: %+v", env.Error)
	}
}

func TestSecurityHeadersApplied(t *testing.T) {
	rec := do(t, testServer(t, baseConfig()), http.MethodGet, "/healthz")
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers must apply to every response")
	}
}

func TestRunShutsDownGracefully(t *testing.T) {
	cfg := baseConfig()
	cfg.HTTPAddr = "127.0.0.1:0"

	s := testServer(t, cfg)
	// Bind an ephemeral port explicitly so the test never races on a fixed one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s.http.Addr = ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	// Give the listener a moment, then request shutdown.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("graceful shutdown returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not shut down within 5s")
	}
}

func TestAddressFromURL(t *testing.T) {
	cases := []struct {
		raw, defaultPort, want string
		wantErr                bool
	}{
		{"postgres://u:p@postgres:5432/db", "5432", "postgres:5432", false},
		{"redis://redis/0", "6379", "redis:6379", false},
		{"postgres://", "5432", "", true},
		{"://bad", "5432", "", true},
	}
	for _, tc := range cases {
		got, err := addressFromURL(tc.raw, tc.defaultPort)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("addressFromURL(%q) expected error", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("addressFromURL(%q) returned error: %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("addressFromURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// listenTCP starts a throwaway TCP listener and returns its host:port.
func listenTCP(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	return ln.Addr().String()
}

// startFakeAgent serves protocol.OperationPing over a Unix socket.
func startFakeAgent(t *testing.T) string {
	t.Helper()
	if _, err := os.Stat("/proc"); err != nil {
		t.Skip("unix socket test requires a Linux environment")
	}

	dir := t.TempDir()
	socket := filepath.Join(dir, "agent.sock")

	ln, err := net.Listen("unix", socket)
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
				buf := make([]byte, 4096)
				n, err := c.Read(buf)
				if err != nil || n == 0 {
					return
				}
				var req protocol.Request
				if err := json.Unmarshal(bytes.TrimSpace(buf[:n]), &req); err != nil {
					return
				}
				resp := protocol.NewSuccess(req.RequestID, map[string]any{"pong": true})
				out, err := json.Marshal(resp)
				if err != nil {
					return
				}
				_, _ = c.Write(append(out, '\n'))
			}(conn)
		}
	}()
	return socket
}
