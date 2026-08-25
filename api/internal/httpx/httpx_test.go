package httpx

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newRequest() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	return r.WithContext(ContextWithRequestID(r.Context(), "req_test"))
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) Envelope {
	t.Helper()
	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	return env
}

func TestOKEnvelope(t *testing.T) {
	rec := httptest.NewRecorder()
	OK(rec, newRequest(), map[string]string{"status": "ok"})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("nosniff header must be set")
	}

	env := decodeEnvelope(t, rec)
	if !env.Success {
		t.Fatal("success envelope must set success=true")
	}
	if env.RequestID != "req_test" {
		t.Fatalf("request_id must be echoed, got %q", env.RequestID)
	}
	if env.Error != nil {
		t.Fatal("success envelope must not carry an error")
	}
}

func TestErrorEnvelopeUsesAPIError(t *testing.T) {
	rec := httptest.NewRecorder()
	Error(rec, newRequest(), NotFound("Website not found"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	env := decodeEnvelope(t, rec)
	if env.Success {
		t.Fatal("error envelope must set success=false")
	}
	if env.Error.Code != CodeNotFound || env.Error.Message != "Website not found" {
		t.Fatalf("unexpected error detail: %+v", env.Error)
	}
	if env.RequestID != "req_test" {
		t.Fatalf("request_id must be present on errors, got %q", env.RequestID)
	}
}

func TestErrorHidesInternalDetail(t *testing.T) {
	// An arbitrary error must never reach the client verbatim: it could carry
	// a DSN, a shell command, or a filesystem path.
	cause := errors.New("dial postgres://jothost:hunter2@postgres:5432 failed")

	rec := httptest.NewRecorder()
	Error(rec, newRequest(), cause)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "hunter2") || strings.Contains(body, "postgres://") {
		t.Fatalf("internal detail leaked to client: %s", body)
	}
	env := decodeEnvelope(t, rec)
	if env.Error.Code != CodeInternal {
		t.Fatalf("expected INTERNAL_ERROR, got %q", env.Error.Code)
	}
}

func TestAsAPIErrorUnwrapsWrappedAPIError(t *testing.T) {
	base := Conflict("Domain already exists")
	wrapped := base.WithInternal(errors.New("unique violation on domains.name"))

	got := AsAPIError(wrapped)
	if got.Code != CodeConflict || got.Status != http.StatusConflict {
		t.Fatalf("wrapped APIError must survive conversion: %+v", got)
	}
	if !errors.Is(wrapped, wrapped.Internal) {
		t.Fatal("internal cause must remain reachable via errors.Is for logging")
	}
}

func TestNewRequestIDIsUniqueAndPrefixed(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := NewRequestID()
		if !strings.HasPrefix(id, "req_") {
			t.Fatalf("request id %q must use the req_ prefix", id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate request id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestRequestIDFromContextMissing(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if got := RequestIDFromContext(r.Context()); got != "" {
		t.Fatalf("expected empty request id, got %q", got)
	}
}
