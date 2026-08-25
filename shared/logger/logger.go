// Package logger provides the structured JSON logger used by every JotHost
// service. Fields follow ARCHITECTURE.md section 12.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Field keys defined by ARCHITECTURE.md section 12.
const (
	KeyService   = "service"
	KeyRequestID = "request_id"
	KeyUserID    = "user_id"
	KeyAction    = "action"
	KeyResource  = "resource"
	KeyError     = "error"
	KeyDuration  = "duration_ms"
)

// redactedKeys are never emitted in log output (CLAUDE.md section 14).
var redactedKeys = map[string]struct{}{
	"password":          {},
	"password_hash":     {},
	"api_token":         {},
	"token":             {},
	"access_token":      {},
	"refresh_token":     {},
	"jwt":               {},
	"authorization":     {},
	"private_key":       {},
	"secret":            {},
	"jwt_secret":        {},
	"encryption_key":    {},
	"database_password": {},
	"session_token":     {},
}

const redactedValue = "[REDACTED]"

// Options configures a logger instance.
type Options struct {
	// Service is the emitting service name, e.g. "api" or "agent".
	Service string
	// Level is one of debug, info, warn, error. Invalid values fall back to info.
	Level string
	// Output is where log lines are written. Defaults to os.Stderr when nil.
	Output io.Writer
}

// New builds a JSON logger that always carries the service field and redacts
// sensitive attribute values.
func New(opts Options) *slog.Logger {
	out := opts.Output
	if out == nil {
		out = os.Stderr
	}
	handler := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       ParseLevel(opts.Level),
		ReplaceAttr: redact,
	})
	return slog.New(handler).With(KeyService, opts.Service)
}

// ParseLevel maps a configuration string to a slog level. Unknown values return
// slog.LevelInfo so that a typo can never silence logging entirely.
func ParseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// redact replaces the value of any attribute whose key is known to carry a
// secret. It is applied at every nesting depth by slog.
func redact(_ []string, a slog.Attr) slog.Attr {
	if _, sensitive := redactedKeys[strings.ToLower(a.Key)]; sensitive {
		return slog.String(a.Key, redactedValue)
	}
	return a
}

type contextKey struct{}

// WithContext stores a logger in ctx so downstream layers inherit its fields.
func WithContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, contextKey{}, l)
}

// FromContext returns the logger stored by WithContext, or fallback when ctx
// carries none. fallback must not be nil.
func FromContext(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(contextKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return fallback
}
