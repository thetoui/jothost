// Package protocol defines the wire contract between the API and the Host
// Agent. Operations are typed and allowlisted (CLAUDE.md sections 6 and 16);
// arbitrary operation strings are rejected before any handler runs.
package protocol

import (
	"errors"
	"fmt"
	"time"
)

// OperationType identifies a privileged operation the Agent may perform.
type OperationType string

// Operations known to the Agent. Phase 0 ships only the non-privileged
// liveness operation; later phases append their own constants and register
// them in allowedOperations.
const (
	OperationPing OperationType = "agent.ping"
)

// allowedOperations is the allowlist consulted by Validate. An operation that
// is not present here can never reach a handler.
var allowedOperations = map[OperationType]struct{}{
	OperationPing: {},
}

// ErrUnknownOperation is returned for any operation outside the allowlist.
var ErrUnknownOperation = errors.New("unknown operation")

// IsAllowed reports whether op is on the operation allowlist.
func IsAllowed(op OperationType) bool {
	_, ok := allowedOperations[op]
	return ok
}

// Request is a single typed operation sent from the API to the Agent.
type Request struct {
	// Operation must be a member of the allowlist.
	Operation OperationType `json:"operation"`
	// RequestID correlates agent logs with API logs.
	RequestID string `json:"request_id"`
	// Payload carries operation-specific arguments. Each handler decodes it
	// into its own typed struct; it is never interpreted as a shell string.
	Payload map[string]any `json:"payload,omitempty"`
}

// Validate enforces the allowlist and the required envelope fields.
func (r Request) Validate() error {
	if r.Operation == "" {
		return errors.New("operation is required")
	}
	if !IsAllowed(r.Operation) {
		return fmt.Errorf("%w: %q", ErrUnknownOperation, r.Operation)
	}
	if r.RequestID == "" {
		return errors.New("request_id is required")
	}
	return nil
}

// Status is the outcome of an operation.
type Status string

// Operation outcomes.
const (
	StatusSuccess Status = "SUCCESS"
	StatusFailed  Status = "FAILED"
)

// Error is a structured agent-side failure. It never carries shell commands,
// stack traces, or secrets (ARCHITECTURE.md section 13).
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response is the Agent's reply to a Request.
type Response struct {
	Status    Status         `json:"status"`
	RequestID string         `json:"request_id"`
	Data      map[string]any `json:"data,omitempty"`
	Error     *Error         `json:"error,omitempty"`
	Duration  time.Duration  `json:"-"`
}

// Error codes returned by the Agent transport layer.
const (
	CodeUnknownOperation = "UNKNOWN_OPERATION"
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeInternal         = "INTERNAL_ERROR"
	CodeTimeout          = "OPERATION_TIMEOUT"
)

// NewError builds a failed Response carrying a structured error.
func NewError(requestID, code, message string) Response {
	return Response{
		Status:    StatusFailed,
		RequestID: requestID,
		Error:     &Error{Code: code, Message: message},
	}
}

// NewSuccess builds a successful Response.
func NewSuccess(requestID string, data map[string]any) Response {
	return Response{Status: StatusSuccess, RequestID: requestID, Data: data}
}
