// Package httpx implements the standard JotHost API response envelope defined
// in API_SPEC.md section 1 and the error contract in ARCHITECTURE.md
// section 13.
package httpx

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/jothost/panel/shared/logger"
)

// Envelope is the single response shape used by every endpoint.
type Envelope struct {
	Success   bool         `json:"success"`
	Data      any          `json:"data,omitempty"`
	Error     *ErrorDetail `json:"error,omitempty"`
	RequestID string       `json:"request_id"`
}

// ErrorDetail is the client-facing error body. It carries a stable machine
// code and a message safe to display; internal details never appear here.
type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteJSON writes an envelope with the given status code. Encoding failures
// are logged rather than ignored (CLAUDE.md section 13); the status line has
// already been sent at that point, so the connection is simply closed.
func WriteJSON(w http.ResponseWriter, r *http.Request, status int, env Envelope) {
	env.RequestID = RequestIDFromContext(r.Context())

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(env); err != nil {
		logger.FromContext(r.Context(), slog.Default()).Error(
			"failed to encode response",
			logger.KeyError, err.Error(),
			logger.KeyRequestID, env.RequestID,
		)
	}
}

// OK writes a 200 success envelope.
func OK(w http.ResponseWriter, r *http.Request, data any) {
	WriteJSON(w, r, http.StatusOK, Envelope{Success: true, Data: data})
}

// Created writes a 201 success envelope.
func Created(w http.ResponseWriter, r *http.Request, data any) {
	WriteJSON(w, r, http.StatusCreated, Envelope{Success: true, Data: data})
}

// Error writes a failure envelope derived from err. Unknown errors are
// reported as a generic internal error so that internal detail never leaks.
//
// The internal cause is logged rather than discarded. Without that, a 500 tells
// the client nothing (by design) and the operator nothing (by accident), which
// leaves a fault with no way in at all.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	apiErr := AsAPIError(err)

	if apiErr.Status >= http.StatusInternalServerError && apiErr.Internal != nil {
		slog.Default().Error("request failed",
			"request_id", RequestIDFromContext(r.Context()),
			"method", r.Method,
			"path", r.URL.Path,
			"code", apiErr.Code,
			// The cause, not the response: this is the half the client is
			// deliberately not shown.
			"error", apiErr.Internal.Error(),
		)
	}

	WriteJSON(w, r, apiErr.Status, Envelope{
		Success: false,
		Error:   &ErrorDetail{Code: apiErr.Code, Message: apiErr.Message},
	})
}
