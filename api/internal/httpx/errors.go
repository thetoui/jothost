package httpx

import (
	"errors"
	"net/http"
)

// Stable error codes returned to clients. Codes are part of the API contract
// and must not change once released.
const (
	CodeBadRequest       = "BAD_REQUEST"
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeForbidden        = "FORBIDDEN"
	CodeNotFound         = "RESOURCE_NOT_FOUND"
	CodeMethodNotAllowed = "METHOD_NOT_ALLOWED"
	CodeConflict         = "CONFLICT"
	CodeRateLimited      = "RATE_LIMITED"
	CodeInternal         = "INTERNAL_ERROR"
	CodeUnavailable      = "SERVICE_UNAVAILABLE"
)

// APIError is an error carrying a client-safe code, message, and HTTP status.
// Internal context is kept in the wrapped error and only ever logged.
type APIError struct {
	Code    string
	Message string
	Status  int
	// Internal is the underlying cause. It is logged, never serialized.
	Internal error
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Internal != nil {
		return e.Code + ": " + e.Message + ": " + e.Internal.Error()
	}
	return e.Code + ": " + e.Message
}

// Unwrap exposes the internal cause to errors.Is and errors.As.
func (e *APIError) Unwrap() error { return e.Internal }

// WithInternal attaches the underlying cause without changing the client-facing
// code or message.
func (e *APIError) WithInternal(err error) *APIError {
	clone := *e
	clone.Internal = err
	return &clone
}

// New builds an APIError.
func New(status int, code, message string) *APIError {
	return &APIError{Code: code, Message: message, Status: status}
}

// Constructors for the common failure modes.
func BadRequest(message string) *APIError {
	return New(http.StatusBadRequest, CodeBadRequest, message)
}

func ValidationFailed(message string) *APIError {
	return New(http.StatusUnprocessableEntity, CodeValidationFailed, message)
}

func Unauthorized(message string) *APIError {
	return New(http.StatusUnauthorized, CodeUnauthorized, message)
}

func Forbidden(message string) *APIError {
	return New(http.StatusForbidden, CodeForbidden, message)
}

func NotFound(message string) *APIError {
	return New(http.StatusNotFound, CodeNotFound, message)
}

func Conflict(message string) *APIError {
	return New(http.StatusConflict, CodeConflict, message)
}

func Unavailable(message string) *APIError {
	return New(http.StatusServiceUnavailable, CodeUnavailable, message)
}

// Internal returns the generic internal error. The cause is preserved for
// logging but is never rendered to the client.
func Internal(cause error) *APIError {
	return (&APIError{
		Code:    CodeInternal,
		Message: "An internal error occurred",
		Status:  http.StatusInternalServerError,
	}).WithInternal(cause)
}

// AsAPIError converts any error into an APIError. Errors that are not already
// APIErrors collapse to a generic internal error, which is what prevents
// accidental disclosure of internal messages.
func AsAPIError(err error) *APIError {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return Internal(err)
}
