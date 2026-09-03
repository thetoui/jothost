// Package operations holds the Agent's typed operation handlers. Dispatch is
// allowlist-driven: an operation string that is not registered here can never
// reach code that touches the host (CLAUDE.md sections 6 and 16).
package operations

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/jothost/panel/agent/internal/apache"
	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/cron"
	"github.com/jothost/panel/agent/internal/database"
	"github.com/jothost/panel/agent/internal/dns"
	"github.com/jothost/panel/agent/internal/fail2ban"
	"github.com/jothost/panel/agent/internal/files"
	"github.com/jothost/panel/agent/internal/firewall"
	"github.com/jothost/panel/agent/internal/ftp"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/logs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/nodejs"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/pma"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/ssh"
	"github.com/jothost/panel/agent/internal/ssl"
	"github.com/jothost/panel/agent/internal/updates"
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
	// Apache is the hybrid backend. Nil, or present but not installed, on a
	// host that serves everything from nginx — which is the default.
	Apache *apache.Provider
	// Firewall manages the host's packet filter. Nil on a host where ufw is
	// not installed, which handlers report as unsupported.
	Firewall *firewall.Provider
	Jobs     *jobs.Runner
	Log      *slog.Logger

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

	// SSL issues and renews certificates. Nil on a host where certificate
	// management is not wired up, which handlers report as unsupported.
	SSL *ssl.Manager

	// Databases manages the database servers on the host. Nil, or holding no
	// reachable engine, means database management is reported as unsupported
	// rather than failing one operation at a time.
	Databases *database.Manager

	// PHPMyAdmin installs and serves the database console. Nil where the host
	// cannot run it, which handlers report as unsupported.
	PHPMyAdmin *pma.Manager

	// Node runs Node.js applications. Nil where the host has no runtime,
	// which handlers report as unsupported.
	Node *nodejs.Manager

	// Files serves the file manager. Nil, or configured with no roots, means
	// file management is unavailable rather than unrestricted.
	Files *files.Manager

	// Fail2Ban manages the host's intrusion prevention. Nil, or a host without
	// it, means the panel offers to install it rather than pretending the
	// settings are there.
	Fail2Ban *fail2ban.Provider
	FTP      *ftp.Provider

	// Updates reports and applies the host's package updates. Nil, or a host
	// with no package manager the panel drives, means the panel reports that
	// rather than showing an empty list that reads as "up to date".
	Updates *updates.Provider

	// DNS runs the host's authoritative name server. Nil, or a host without
	// BIND, means the panel offers to install one rather than accepting zones
	// nothing would answer for.
	DNS *dns.Provider

	// SSH reads and changes the host's SSH server configuration. Nil, or a host
	// with no sshd, means the panel reports that rather than offering settings
	// nothing would read.
	SSH *ssh.Provider

	// Cron writes the host's crontabs. Nil, or a host with no spool directory,
	// means nothing can be scheduled — which the panel reports rather than
	// accepting jobs that would never run.
	Cron *cron.Provider

	// Logs reads the host's log files. Nil, or configured with no roots, means
	// log reading is unavailable rather than unrestricted — a path check that
	// fails open would turn a log viewer into a file reader.
	Logs *logs.Provider
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

	r.mustRegister(protocol.OperationServiceDetect, r.handleServiceDetect)
	r.mustRegister(protocol.OperationServiceAction, r.handleServiceAction)
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
	r.mustRegister(protocol.OperationFirewallStatus, r.handleFirewallStatus)
	r.mustRegister(protocol.OperationFirewallChange, r.handleFirewallChange)
	r.mustRegister(protocol.OperationFirewallConfirm, r.handleFirewallConfirm)
	r.mustRegister(protocol.OperationFirewallRollback, r.handleFirewallRollback)
	r.mustRegister(protocol.OperationApacheStatus, r.handleApacheStatus)
	r.mustRegister(protocol.OperationApacheInstall, r.handleApacheInstall)
	r.mustRegister(protocol.OperationWebsitePHPSet, r.handleWebsitePHPSet)
	r.mustRegister(protocol.OperationWebsitePHPUnset, r.handleWebsitePHPUnset)

	r.mustRegister(protocol.OperationSSLIssue, r.handleSSLIssue)
	r.mustRegister(protocol.OperationSSLRenew, r.handleSSLRenew)
	r.mustRegister(protocol.OperationSSLRevoke, r.handleSSLRevoke)
	r.mustRegister(protocol.OperationSSLStatus, r.handleSSLStatus)
	r.mustRegister(protocol.OperationSSLCapabilities, r.handleSSLCapabilities)

	r.mustRegister(protocol.OperationDatabaseEngines, r.handleDatabaseEngines)
	r.mustRegister(protocol.OperationDatabaseList, r.handleDatabaseList)
	r.mustRegister(protocol.OperationDatabaseCreate, r.handleDatabaseCreate)
	r.mustRegister(protocol.OperationDatabaseDelete, r.handleDatabaseDelete)
	r.mustRegister(protocol.OperationDatabaseSize, r.handleDatabaseSize)
	r.mustRegister(protocol.OperationDatabaseUserList, r.handleDatabaseUserList)
	r.mustRegister(protocol.OperationDatabaseUserCreate, r.handleDatabaseUserCreate)
	r.mustRegister(protocol.OperationDatabaseUserDelete, r.handleDatabaseUserDelete)
	r.mustRegister(protocol.OperationDatabaseUserPassword, r.handleDatabaseUserPassword)
	r.mustRegister(protocol.OperationDatabaseUserGrant, r.handleDatabaseUserGrant)

	r.mustRegister(protocol.OperationPHPMyAdminStatus, r.handlePHPMyAdminStatus)
	r.mustRegister(protocol.OperationPHPMyAdminInstall, r.handlePHPMyAdminInstall)
	r.mustRegister(protocol.OperationPHPMyAdminUninstall, r.handlePHPMyAdminUninstall)

	r.mustRegister(protocol.OperationNodeVersions, r.handleNodeVersions)
	r.mustRegister(protocol.OperationNodeInstall, r.handleNodeInstall)
	r.mustRegister(protocol.OperationNodeUninstall, r.handleNodeUninstall)
	r.mustRegister(protocol.OperationNodeAppDeploy, r.handleNodeAppDeploy)
	r.mustRegister(protocol.OperationNodeAppRemove, r.handleNodeAppRemove)
	r.mustRegister(protocol.OperationNodeAppStart, r.handleNodeAppStart)
	r.mustRegister(protocol.OperationNodeAppStop, r.handleNodeAppStop)
	r.mustRegister(protocol.OperationNodeAppRestart, r.handleNodeAppRestart)
	r.mustRegister(protocol.OperationNodeAppStatus, r.handleNodeAppStatus)
	r.mustRegister(protocol.OperationNodeAppLogs, r.handleNodeAppLogs)
	r.mustRegister(protocol.OperationNodeAppInstall, r.handleNodeAppInstall)

	r.mustRegister(protocol.OperationFileList, r.handleFileList)
	r.mustRegister(protocol.OperationFileStat, r.handleFileStat)
	r.mustRegister(protocol.OperationFileRead, r.handleFileRead)
	r.mustRegister(protocol.OperationFileWrite, r.handleFileWrite)
	r.mustRegister(protocol.OperationFileMkdir, r.handleFileMkdir)
	r.mustRegister(protocol.OperationFileCreate, r.handleFileCreate)
	r.mustRegister(protocol.OperationFileDelete, r.handleFileDelete)
	r.mustRegister(protocol.OperationFileCopy, r.handleFileCopy)
	r.mustRegister(protocol.OperationFileMove, r.handleFileMove)
	r.mustRegister(protocol.OperationFileChmod, r.handleFileChmod)
	r.mustRegister(protocol.OperationFileArchive, r.handleFileArchive)
	r.mustRegister(protocol.OperationFileExtract, r.handleFileExtract)
	r.mustRegister(protocol.OperationFileSearch, r.handleFileSearch)

	r.mustRegister(protocol.OperationFail2banStatus, r.handleFail2banStatus)
	r.mustRegister(protocol.OperationFail2banInstall, r.handleFail2banInstall)
	r.mustRegister(protocol.OperationFail2banConfigure, r.handleFail2banConfigure)
	r.mustRegister(protocol.OperationFail2banIgnore, r.handleFail2banIgnore)
	r.mustRegister(protocol.OperationFail2banBanned, r.handleFail2banBanned)
	r.mustRegister(protocol.OperationFail2banUnban, r.handleFail2banUnban)
	r.mustRegister(protocol.OperationFail2banBan, r.handleFail2banBan)

	r.mustRegister(protocol.OperationFTPStatus, r.handleFTPStatus)
	r.mustRegister(protocol.OperationFTPInstall, r.handleFTPInstall)
	r.mustRegister(protocol.OperationFTPReconcile, r.handleFTPReconcile)
	r.mustRegister(protocol.OperationFTPSessions, r.handleFTPSessions)
	r.mustRegister(protocol.OperationFTPDisconnect, r.handleFTPDisconnect)

	r.mustRegister(protocol.OperationUpdatesCheck, r.handleUpdatesCheck)
	r.mustRegister(protocol.OperationUpdatesApply, r.handleUpdatesApply)
	r.mustRegister(protocol.OperationUpdatesRevert, r.handleUpdatesRevert)

	r.mustRegister(protocol.OperationDNSStatus, r.handleDNSStatus)
	r.mustRegister(protocol.OperationDNSInstall, r.handleDNSInstall)
	r.mustRegister(protocol.OperationDNSReconcile, r.handleDNSReconcile)
	r.mustRegister(protocol.OperationDNSZoneStatus, r.handleDNSZoneStatus)
	r.mustRegister(protocol.OperationDNSSigning, r.handleDNSSigning)

	r.mustRegister(protocol.OperationSSHStatus, r.handleSSHStatus)
	r.mustRegister(protocol.OperationSSHConfigure, r.handleSSHConfigure)
	r.mustRegister(protocol.OperationSSHKeyList, r.handleSSHKeyList)
	r.mustRegister(protocol.OperationSSHKeyAdd, r.handleSSHKeyAdd)
	r.mustRegister(protocol.OperationSSHKeyRemove, r.handleSSHKeyRemove)

	r.mustRegister(protocol.OperationCronStatus, r.handleCronStatus)
	r.mustRegister(protocol.OperationCronApply, r.handleCronApply)
	r.mustRegister(protocol.OperationCronRemove, r.handleCronRemove)
	r.mustRegister(protocol.OperationCronRun, r.handleCronRun)
	r.mustRegister(protocol.OperationCronLogRemove, r.handleCronLogRemove)

	r.mustRegister(protocol.OperationLogList, r.handleLogList)
	r.mustRegister(protocol.OperationLogTail, r.handleLogTail)
	r.mustRegister(protocol.OperationLogRead, r.handleLogRead)

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

	data, err := r.invoke(ctx, req, handler, nil)
	if err != nil {
		return r.errorResponse(req, err)
	}
	return protocol.NewSuccess(req.RequestID, data)
}

// invoke runs a handler and turns a panic into an error.
//
// Without this, a nil dereference anywhere in any handler takes down the whole
// Agent — and the Agent is the only thing that can manage every site on the
// host, so one bad request would leave an operator unable to touch any of
// them until someone noticed and restarted it. A single failed operation is a
// far better outcome than a dead daemon.
//
// The recovered value is logged with its stack and never returned: a panic
// message can carry internal state, and the caller gets the same generic
// internal error it would get for any other unexpected failure.
func (r *Registry) invoke(ctx context.Context, req protocol.Request, handler Handler,
	reporter *jobs.Reporter,
) (data map[string]any, err error) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		r.log.Error("operation handler panicked",
			"operation", string(req.Operation),
			"request_id", req.RequestID,
			"panic", fmt.Sprint(recovered),
			"stack", string(debug.Stack()),
		)
		err = Fail(protocol.CodeInternal, "The operation failed unexpectedly", nil)
	}()

	return handler(ctx, req, reporter)
}

// dispatchAsync submits an operation to the job runner.
func (r *Registry) dispatchAsync(req protocol.Request, handler Handler) protocol.Response {
	jobID, err := r.deps.Jobs.Submit(req.Operation, req.RequestID,
		func(ctx context.Context, reporter *jobs.Reporter) (map[string]any, error) {
			return r.invoke(ctx, req, handler, reporter)
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
