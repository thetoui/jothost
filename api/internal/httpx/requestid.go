package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// RequestIDHeader is the header carrying the correlation ID in and out of the
// API (ARCHITECTURE.md section 12).
const RequestIDHeader = "X-Request-ID"

// requestIDPrefix matches the format used in API_SPEC.md examples.
const requestIDPrefix = "req_"

type requestIDContextKey struct{}

// ContextWithRequestID stores id on ctx.
func ContextWithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDContextKey{}, id)
}

// RequestIDFromContext returns the request ID stored on ctx, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDContextKey{}).(string); ok {
		return id
	}
	return ""
}

// NewRequestID generates an unpredictable request identifier. Randomness comes
// from crypto/rand so IDs cannot be guessed and correlated by a third party.
func NewRequestID() string {
	buf := make([]byte, 12)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is not recoverable in a meaningful way here;
		// fall back to a constant marker so the request still carries a value
		// and the anomaly is visible in logs.
		return requestIDPrefix + "unavailable"
	}
	return requestIDPrefix + hex.EncodeToString(buf)
}
