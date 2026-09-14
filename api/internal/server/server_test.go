package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/logger"
)

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
		Auth: config.AuthConfig{
			EncryptionKey:    testsupport.TestEncryptionKey,
			AccessTokenTTL:   15 * time.Minute,
			RefreshTokenTTL:  time.Hour,
			MFAChallengeTTL:  5 * time.Minute,
			TOTPIssuer:       "JotHost Test",
			LoginRateLimit:   5,
			LoginIPRateLimit: 20,
			LoginRateWindow:  time.Minute,
		},
	}
}

// testServer builds a Server against the live test dependencies.
func testServer(t *testing.T) *Server {
	t.Helper()

	deps := testsupport.Require(t)

	var buf bytes.Buffer
	srv, err := New(Options{
		Config: baseConfig(),
		Log:    logger.New(logger.Options{Service: "api", Output: &buf}),
		Pool:   deps.Pool,
		Redis:  deps.Redis,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return srv
}

func do(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Router.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec
}

func TestHealthEndpoint(t *testing.T) {
	s := testServer(t)

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
	// /healthz must not depend on the Agent, which is unreachable here.
	rec := do(t, testServer(t), http.MethodGet, "/healthz")
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness must not probe dependencies, got %d", rec.Code)
	}
}

func TestVersionEndpoint(t *testing.T) {
	rec := do(t, testServer(t), http.MethodGet, "/api/v1/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"version"`) {
		t.Fatalf("version payload missing: %s", rec.Body.String())
	}
}

func TestReadinessReportsPerDependencyState(t *testing.T) {
	// Postgres and Redis are live; the Agent socket is not, so readiness must
	// fail overall while still reporting the healthy dependencies as up.
	rec := do(t, testServer(t), http.MethodGet, "/readyz")

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while the agent is down, got %d (%s)", rec.Code, rec.Body.String())
	}

	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Ready  bool `json:"ready"`
			Checks map[string]struct {
				Status string `json:"status"`
			} `json:"checks"`
		} `json:"data"`
		Error *httpx.ErrorDetail `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("invalid envelope: %v", err)
	}

	if env.Data.Checks["postgres"].Status != "up" {
		t.Fatalf("postgres must report up, got %q", env.Data.Checks["postgres"].Status)
	}
	if env.Data.Checks["redis"].Status != "up" {
		t.Fatalf("redis must report up, got %q", env.Data.Checks["redis"].Status)
	}
	if env.Data.Checks["agent"].Status != "down" {
		t.Fatalf("agent must report down, got %q", env.Data.Checks["agent"].Status)
	}
	if env.Error == nil || env.Error.Code != httpx.CodeUnavailable {
		t.Fatalf("unexpected readiness error: %+v", env.Error)
	}
	// Credentials embedded in DATABASE_URL must never surface in the payload.
	if strings.Contains(rec.Body.String(), "pw@") {
		t.Fatalf("credentials leaked into readiness output: %s", rec.Body.String())
	}
}

func TestUnknownRouteReturnsEnvelope(t *testing.T) {
	rec := do(t, testServer(t), http.MethodGet, "/api/v1/does-not-exist")

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
	rec := do(t, testServer(t), http.MethodGet, "/healthz")
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatal("security headers must apply to every response")
	}
}

func TestProtectedRoutesRejectAnonymousCallers(t *testing.T) {
	s := testServer(t)

	// Every authenticated route must answer 401 without a token. A route added
	// later without RequireAuth should make this fail.
	protected := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/v1/auth/me"},
		{http.MethodPost, "/api/v1/auth/logout"},
		{http.MethodPost, "/api/v1/auth/2fa/setup"},
		{http.MethodPost, "/api/v1/auth/2fa/enable"},
		{http.MethodPost, "/api/v1/auth/2fa/disable"},
		{http.MethodPost, "/api/v1/auth/2fa/recovery-codes"},
	}

	for _, route := range protected {
		rec := do(t, s, route.method, route.path)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s: expected 401 without a token, got %d", route.method, route.path, rec.Code)
		}

		var env httpx.Envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s %s: invalid envelope: %v", route.method, route.path, err)
		}
		if env.Error == nil || env.Error.Code != httpx.CodeUnauthorized {
			t.Fatalf("%s %s: unexpected error %+v", route.method, route.path, env.Error)
		}
	}
}

func TestRunShutsDownGracefully(t *testing.T) {
	s := testServer(t)

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
