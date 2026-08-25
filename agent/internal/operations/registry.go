// Package operations holds the Agent's typed operation handlers. Dispatch is
// allowlist-driven: an operation string that is not registered here can never
// reach any code that touches the host (CLAUDE.md sections 6 and 16).
package operations

import (
	"context"
	"log/slog"

	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/version"
)

// Handler executes one typed operation. Payloads are decoded into typed
// structs by the handler; they are never passed to a shell.
type Handler func(ctx context.Context, req protocol.Request) (protocol.Response, error)

// Registry maps allowlisted operations to their handlers.
type Registry struct {
	log      *slog.Logger
	handlers map[protocol.OperationType]Handler
}

// NewRegistry builds the registry with the Phase 0 operation set.
func NewRegistry(log *slog.Logger) *Registry {
	r := &Registry{log: log, handlers: make(map[protocol.OperationType]Handler)}
	r.mustRegister(protocol.OperationPing, handlePing)
	return r
}

// mustRegister refuses to register an operation that is not on the shared
// allowlist, so the allowlist and the dispatch table cannot drift apart.
func (r *Registry) mustRegister(op protocol.OperationType, h Handler) {
	if !protocol.IsAllowed(op) {
		panic("operations: attempt to register non-allowlisted operation " + string(op))
	}
	if _, exists := r.handlers[op]; exists {
		panic("operations: duplicate handler for " + string(op))
	}
	r.handlers[op] = h
}

// Dispatch validates the request against the allowlist and runs its handler.
func (r *Registry) Dispatch(ctx context.Context, req protocol.Request) protocol.Response {
	if err := req.Validate(); err != nil {
		r.log.Warn("rejected agent request",
			"operation", string(req.Operation),
			"error", err.Error(),
		)
		return protocol.NewError(req.RequestID, protocol.CodeUnknownOperation, "Operation is not permitted")
	}

	handler, ok := r.handlers[req.Operation]
	if !ok {
		// Allowlisted but unimplemented: treat as unknown rather than falling
		// through to anything generic.
		r.log.Error("no handler registered for allowlisted operation", "operation", string(req.Operation))
		return protocol.NewError(req.RequestID, protocol.CodeUnknownOperation, "Operation is not permitted")
	}

	resp, err := handler(ctx, req)
	if err != nil {
		// The cause is logged; the API receives only a structured code.
		r.log.Error("operation failed",
			"operation", string(req.Operation),
			"request_id", req.RequestID,
			"error", err.Error(),
		)
		return protocol.NewError(req.RequestID, protocol.CodeInternal, "Operation failed")
	}
	return resp
}

// Operations returns the registered operation names, for diagnostics.
func (r *Registry) Operations() []protocol.OperationType {
	ops := make([]protocol.OperationType, 0, len(r.handlers))
	for op := range r.handlers {
		ops = append(ops, op)
	}
	return ops
}

// handlePing is a non-privileged liveness probe. It performs no host access.
func handlePing(_ context.Context, req protocol.Request) (protocol.Response, error) {
	return protocol.NewSuccess(req.RequestID, map[string]any{
		"pong":    true,
		"version": version.Current().Version,
	}), nil
}
