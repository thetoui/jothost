package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/logger"
)

func testLogger(buf *bytes.Buffer) *bytes.Buffer { return buf }

func TestRequestIDGeneratesWhenAbsent(t *testing.T) {
	var seen string
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = httpx.RequestIDFromContext(r.Context())
	}), RequestID())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	if !strings.HasPrefix(seen, "req_") {
		t.Fatalf("expected generated request id, got %q", seen)
	}
	if got := rec.Header().Get(httpx.RequestIDHeader); got != seen {
		t.Fatalf("response header %q must match context id %q", got, seen)
	}
}

func TestRequestIDHonoursSafeClientValue(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := httpx.RequestIDFromContext(r.Context()); got != "trace-abc-123" {
			t.Fatalf("expected client id to be honoured, got %q", got)
		}
	}), RequestID())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(httpx.RequestIDHeader, "trace-abc-123")
	h.ServeHTTP(httptest.NewRecorder(), req)
}

func TestRequestIDRejectsUnsafeClientValue(t *testing.T) {
	// Newlines and control characters would let a client forge log lines.
	unsafe := []string{
		"short",
		"bad value with spaces",
		"inject\nlevel=error msg=\"fake\"",
		strings.Repeat("a", 128),
		"<script>alert(1)</script>",
	}

	for _, candidate := range unsafe {
		var seen string
		h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seen = httpx.RequestIDFromContext(r.Context())
		}), RequestID())

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set(httpx.RequestIDHeader, candidate)
		h.ServeHTTP(httptest.NewRecorder(), req)

		if seen == candidate {
			t.Fatalf("unsafe request id %q must be replaced", candidate)
		}
		if !strings.HasPrefix(seen, "req_") {
			t.Fatalf("replacement id must be generated, got %q", seen)
		}
	}
}

func TestLoggerEmitsStructuredLine(t *testing.T) {
	var buf bytes.Buffer
	base := logger.New(logger.Options{Service: "api", Output: testLogger(&buf)})

	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("nope"))
	}), RequestID(), Logger(base))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/health?token=secret", nil))

	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v (%s)", err, buf.String())
	}
	if line["status"].(float64) != float64(http.StatusTeapot) {
		t.Fatalf("expected recorded status 418, got %v", line["status"])
	}
	if line["path"] != "/api/v1/health" {
		t.Fatalf("expected path without query string, got %v", line["path"])
	}
	if strings.Contains(buf.String(), "token=secret") {
		t.Fatalf("query string must not be logged: %s", buf.String())
	}
	if _, ok := line[logger.KeyRequestID]; !ok {
		t.Fatal("access log must carry request_id")
	}
	if _, ok := line[logger.KeyDuration]; !ok {
		t.Fatal("access log must carry duration_ms")
	}
}

func TestRecoverReturnsGenericError(t *testing.T) {
	var buf bytes.Buffer
	base := logger.New(logger.Options{Service: "api", Output: &buf})

	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("database password hunter2 in panic value")
	}), RequestID(), Logger(base), Recover(base))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 after panic, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hunter2") {
		t.Fatalf("panic detail must not reach the client: %s", body)
	}

	var env httpx.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("panic response is not a valid envelope: %v", err)
	}
	if env.Success || env.Error.Code != httpx.CodeInternal {
		t.Fatalf("unexpected panic envelope: %+v", env)
	}
	if env.RequestID == "" {
		t.Fatal("panic response must still carry a request_id")
	}
	if !strings.Contains(buf.String(), "panic recovered") {
		t.Fatal("panic must be logged")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), SecurityHeaders())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	want := map[string]string{
		"X-Content-Type-Options":     "nosniff",
		"X-Frame-Options":            "DENY",
		"Referrer-Policy":            "no-referrer",
		"Cross-Origin-Opener-Policy": "same-origin",
	}
	for header, value := range want {
		if got := rec.Header().Get(header); got != value {
			t.Fatalf("header %s = %q, want %q", header, got, value)
		}
	}
}

func TestChainOrdersOutermostFirst(t *testing.T) {
	var order []string
	mark := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				order = append(order, name)
				next.ServeHTTP(w, r)
			})
		}
	}

	h := Chain(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		order = append(order, "handler")
	}), mark("first"), mark("second"))

	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	got := strings.Join(order, ",")
	if got != "first,second,handler" {
		t.Fatalf("unexpected middleware order: %s", got)
	}
}
