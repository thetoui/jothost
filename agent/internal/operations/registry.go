// Package operations holds the Agent's typed operation handlers. Dispatch is
// allowlist-driven: an operation string that is not registered here can never
// reach code that touches the host (CLAUDE.md sections 6 and 16).
package operations

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/shared/protocol"
)

// Handler executes one typed operation.
//
// Payloads are decoded into typed structs by the handler; they are never
// passed to a shell. The reporter is nil for synchronous execution.
type Handler func(ctx context.Context, req protocol.Request, reporter *jobs.Reporter) (map[string]any, error)

// HandlerError is a failure a handler wants reported with a specific code and
// message, rather than collapsing to a generic internal error.
//
// It exists so a handler can say "that mount point is not mounted" without
// being able to accidentally leak an internal error string: the message is
// written deliberately at the point of failure.
type HandlerError struct {
	Code    string
	Message string
	// Cause is logged and never serialised.
	Cause error
}

func (e *HandlerError) Error() string {
	if e.Cause != nil {
		return e.Code + ": " + e.Message + ": " + e.Cause.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *HandlerError) Unwrap() error { return e.Cause }

// Fail builds a HandlerError.
func Fail(code, message string, cause error) error {
	return &HandlerError{Code: code, Message: message, Cause: cause}
}

// Dependencies are the collaborators handlers act through.
type Dependencies struct {
	Collector *collectors.Collector
	Services  *services.Provider
	Sites     *sites.Manager
	Nginx     *nginx.Provider
	Jobs      *jobs.Runner
	Log       *slog.Logger

	// PHP reports which versions the host has, PHPPools writes per-site FPM
	// pools, and PHPInstaller adds and removes versions. Any of them may be
	// nil on a host without PHP, which handlers report as unsupported rather
	// than failing obscurely.
	PHP          *php.Detector
	PHPPools     *php.Provider
	PHPInstaller *php.Installer
	// WebGroup is the group the web server runs as. A pool socket must be
	// group-owned by it or nginx cannot connect and every PHP request 502s.
	WebGroup string
}

// Registry maps allowlisted operations to their handlers.
type Registry struct {
	deps     Dependencies
	log      *slog.Logger
	handlers map[protocol.OperationType]Handler
}

// NewRegistry builds the registry with the Phase 2 operation set.
func NewRegistry(deps Dependencies) *Registry {
	r := &Registry{
		deps:     deps,
		log:      deps.Log,
		handlers: make(map[protocol.OperationType]Handler),
	}

	r.mustRegister(protocol.OperationPing, r.handlePing)
	r.mustRegister(protocol.OperationInfo, r.handleAgentInfo)

	r.mustRegister(protocol.OperationSystemInfo, r.handleSystemInfo)
	r.mustRegister(protocol.OperationMetricsCPU, r.handleMetricsCPU)
	r.mustRegister(protocol.OperationMetricsMemory, r.handleMetricsMemory)
	r.mustRegister(protocol.OperationMetricsDisk, r.handleMetricsDisk)
	r.mustRegister(protocol.OperationMetricsNetwork, r.handleMetricsNetwork)
	r.mustRegister(protocol.OperationMetricsLoad, r.handleMetricsLoad)
	r.mustRegister(protocol.OperationProcessList, r.handleProcessList)

	r.mustRegister(protocol.OperationServiceList, r.handleServiceList)
	r.mustRegister(protocol.OperationServiceStatus, r.handleServiceStatus)

	r.mustRegister(protocol.OperationWebsiteCreate, r.handleWebsiteCreate)
	r.mustRegister(protocol.OperationWebsiteDelete, r.handleWebsiteDelete)
	r.mustRegister(protocol.OperationWebsiteUpdate, r.handleWebsiteUpdate)
	r.mustRegister(protocol.OperationWebsiteStatus, r.handleWebsiteStatus)
	r.mustRegister(protocol.OperationWebsiteLogs, r.handleWebsiteLogs)
	r.mustRegister(protocol.OperationNginxValidate, r.handleNginxValidate)
	r.mustRegister(protocol.OperationNginxReload, r.handleNginxReload)

	r.mustRegister(protocol.OperationPHPVersions, r.handlePHPVersions)
	r.mustRegister(protocol.OperationPHPInstall, r.handlePHPInstall)
	r.mustRegister(protocol.OperationPHPUninstall, r.handlePHPUninstall)
	r.mustRegister(protocol.OperationPHPPoolCreate, r.handlePHPPoolCreate)
	r.mustRegister(protocol.OperationPHPPoolDelete, r.handlePHPPoolDelete)
	r.mustRegister(protocol.OperationPHPPoolStatus, r.handlePHPPoolStatus)
	r.mustRegister(protocol.OperationPHPExtensions, r.handlePHPExtensions)
	r.mustRegister(protocol.OperationWebsitePHPSet, r.handleWebsitePHPSet)
	r.mustRegister(protocol.OperationWebsitePHPUnset, r.handleWebsitePHPUnset)

	r.mustRegister(protocol.OperationJobStatus, r.handleJobStatus)
	r.mustRegister(protocol.OperationJobCancel, r.handleJobCancel)
	r.mustRegister(protocol.OperationJobList, r.handleJobList)

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

// Operations returns the registered operation names, for diagnostics.
func (r *Registry) Operations() []protocol.OperationType {
	ops := make([]protocol.OperationType, 0, len(r.handlers))
	for op := range r.handlers {
		ops = append(ops, op)
	}
	return ops
}

// Dispatch validates the request against the allowlist and runs its handler.
//
// Asynchronous requests are submitted to the job runner and return immediately
// with a job ID.
func (r *Registry) Dispatch(ctx context.Context, req protocol.Request) protocol.Response {
	if err := req.Validate(); err != nil {
		r.log.Warn("rejected agent request",
			"operation", string(req.Operation),
			"error", err.Error(),
		)
		if errors.Is(err, protocol.ErrUnknownOperation) {
			return protocol.NewError(req.RequestID, protocol.CodeUnknownOperation, "Operation is not permitted")
		}
		return protocol.NewError(req.RequestID, protocol.CodeInvalidRequest, "Request is not valid")
	}

	handler, ok := r.handlers[req.Operation]
	if !ok {
		// Allowlisted but unimplemented: treat as unknown rather than falling
		// through to anything generic.
		r.log.Error("no handler registered for allowlisted operation", "operation", string(req.Operation))
		return protocol.NewError(req.RequestID, protocol.CodeUnknownOperation, "Operation is not permitted")
	}

	if req.Async() {
		return r.dispatchAsync(req, handler)
	}

	data, err := handler(ctx, req, nil)
	if err != nil {
		return r.errorResponse(req, err)
	}
	return protocol.NewSuccess(req.RequestID, data)
}

// dispatchAsync submits an operation to the job runner.
func (r *Registry) dispatchAsync(req protocol.Request, handler Handler) protocol.Response {
	jobID, err := r.deps.Jobs.Submit(req.Operation, req.RequestID,
		func(ctx context.Context, reporter *jobs.Reporter) (map[string]any, error) {
			return handler(ctx, req, reporter)
		})
	if err != nil {
		if errors.Is(err, jobs.ErrQueueFull) {
			return protocol.NewError(req.RequestID, protocol.CodeBusy,
				"The agent is at its job limit. Try again shortly.")
		}
		r.log.Error("failed to submit job",
			"operation", string(req.Operation),
			"error", err.Error(),
		)
		return protocol.NewError(req.RequestID, protocol.CodeInternal, "Operation failed")
	}

	return protocol.NewAccepted(req.RequestID, jobID)
}

// errorResponse converts a handler failure into a structured response.
func (r *Registry) errorResponse(req protocol.Request, err error) protocol.Response {
	var handlerErr *HandlerError
	if errors.As(err, &handlerErr) {
		r.log.Warn("operation rejected",
			"operation", string(req.Operation),
			"request_id", req.RequestID,
			"code", handlerErr.Code,
			"error", err.Error(),
		)
		return protocol.NewError(req.RequestID, handlerErr.Code, handlerErr.Message)
	}

	// The cause is logged; the API receives only a structured code.
	r.log.Error("operation failed",
		"operation", string(req.Operation),
		"request_id", req.RequestID,
		"error", err.Error(),
	)
	return protocol.NewError(req.RequestID, protocol.CodeInternal, "Operation failed")
}
