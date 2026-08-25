// Package middleware provides the HTTP middleware chain shared by every API
// route: request IDs, structured access logging, panic recovery, and baseline
// security headers.
package middleware

import (
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/logger"
)

// Middleware wraps an http.Handler.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so that the first argument is the outermost layer.
func Chain(h http.Handler, middlewares ...Middleware) http.Handler {
	for i := len(middlewares) - 1; i >= 0; i-- {
		h = middlewares[i](h)
	}
	return h
}

// safeRequestID bounds what an inbound X-Request-ID may contain. Client input
// ends up in logs, so it is restricted to an opaque token; anything else is
// replaced with a freshly generated ID to prevent log injection.
var safeRequestID = regexp.MustCompile(`^[A-Za-z0-9_.-]{8,64}$`)

// RequestID installs a correlation ID on the context and echoes it back on the
// response. A well-formed client-supplied ID is honoured so callers can trace
// a request end to end.
func RequestID() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(httpx.RequestIDHeader)
			if !safeRequestID.MatchString(id) {
				id = httpx.NewRequestID()
			}

			ctx := httpx.ContextWithRequestID(r.Context(), id)
			w.Header().Set(httpx.RequestIDHeader, id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// statusRecorder captures the status code and response size for access logs.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Logger emits one structured line per request and puts a request-scoped
// logger on the context for downstream layers.
func Logger(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			requestID := httpx.RequestIDFromContext(r.Context())

			scoped := base.With(logger.KeyRequestID, requestID)
			ctx := logger.WithContext(r.Context(), scoped)

			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r.WithContext(ctx))

			// Only the path is logged; query strings may carry sensitive
			// values and are deliberately omitted.
			scoped.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"bytes", rec.bytes,
				logger.KeyDuration, time.Since(start).Milliseconds(),
				"remote_addr", r.RemoteAddr,
			)
		})
	}
}

// Recover converts a panic into a logged 500 response so a single bad request
// cannot take the API process down.
func Recover(base *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// The panic value and stack stay in the logs; the client
				// receives only the generic internal error envelope.
				logger.FromContext(r.Context(), base).Error("panic recovered",
					logger.KeyError, sprint(rec),
					"method", r.Method,
					"path", r.URL.Path,
				)
				httpx.Error(w, r, httpx.Internal(nil))
			}()

			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders applies baseline hardening headers to every response.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
			next.ServeHTTP(w, r)
		})
	}
}
