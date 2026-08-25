package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
)

func decode(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("log output is not valid JSON: %v (%s)", err, buf.String())
	}
	return out
}

func TestNewEmitsJSONWithService(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Service: "api", Level: "info", Output: &buf})
	l.Info("started")

	got := decode(t, &buf)
	if got[KeyService] != "api" {
		t.Fatalf("expected service=api, got %v", got[KeyService])
	}
	if got["msg"] != "started" {
		t.Fatalf("expected msg=started, got %v", got["msg"])
	}
}

func TestSensitiveFieldsAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Service: "api", Level: "info", Output: &buf})
	l.Info("login",
		"password", "hunter2",
		"jwt", "eyJhbGciOi",
		"database_password", "s3cret",
		"username", "admin",
	)

	raw := buf.Bytes()
	for _, secret := range []string{"hunter2", "eyJhbGciOi", "s3cret"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret %q leaked into logs: %s", secret, raw)
		}
	}
	got := decode(t, &buf)
	if got["password"] != redactedValue {
		t.Fatalf("expected password to be redacted, got %v", got["password"])
	}
	if got["username"] != "admin" {
		t.Fatalf("non-sensitive fields must survive, got %v", got["username"])
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		" warn ":  slog.LevelWarn,
		"error":   slog.LevelError,
		"garbage": slog.LevelInfo,
		"":        slog.LevelInfo,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Fatalf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestContextRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	fallback := New(Options{Service: "api", Output: &buf})
	if got := FromContext(context.Background(), fallback); got != fallback {
		t.Fatal("empty context must return the fallback logger")
	}

	scoped := fallback.With(KeyRequestID, "req_1")
	ctx := WithContext(context.Background(), scoped)
	if got := FromContext(ctx, fallback); got != scoped {
		t.Fatal("context logger must be returned when present")
	}
}
