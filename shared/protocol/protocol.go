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

// Operations known to the Agent.
//
// Adding a constant is not enough to make an operation callable: it must also
// appear in allowedOperations below, and a handler must be registered for it
// in the Agent. The three lists are cross-checked at startup and in tests.
const (
	// Agent introspection. Neither touches the host.
	OperationPing OperationType = "agent.ping"
	OperationInfo OperationType = "agent.info"

	// Read-only host inspection.
	OperationSystemInfo     OperationType = "system.info"
	OperationMetricsCPU     OperationType = "metrics.cpu"
	OperationMetricsMemory  OperationType = "metrics.memory"
	OperationMetricsDisk    OperationType = "metrics.disk"
	OperationMetricsNetwork OperationType = "metrics.network"
	OperationMetricsLoad    OperationType = "metrics.load"
	OperationProcessList    OperationType = "process.list"
	// service.detect reports what this host actually has, and service.action
	// starts, stops, restarts, enables or disables one of them. The action
	// names a catalogue key rather than a unit: see operations/services_manage.go.
	OperationServiceDetect OperationType = "service.detect"
	OperationServiceAction OperationType = "service.action"

	OperationServiceList   OperationType = "service.list"
	OperationServiceStatus OperationType = "service.status"

	// Website provisioning. These change the host, unlike everything above.
	OperationWebsiteCreate OperationType = "website.create"
	OperationWebsiteDelete OperationType = "website.delete"
	OperationWebsiteUpdate OperationType = "website.update"
	OperationWebsiteStatus OperationType = "website.status"
	OperationWebsiteLogs   OperationType = "website.logs"
	OperationNginxValidate OperationType = "nginx.validate"
	OperationNginxReload   OperationType = "nginx.reload"

	OperationPHPVersions   OperationType = "php.versions"
	OperationPHPInstall    OperationType = "php.install"
	OperationPHPUninstall  OperationType = "php.uninstall"
	OperationPHPPoolCreate OperationType = "php.pool.create"
	OperationPHPPoolDelete OperationType = "php.pool.delete"
	OperationPHPPoolStatus OperationType = "php.pool.status"
	OperationPHPExtensions OperationType = "php.extensions"

	// A website's PHP is set and unset as one operation because the pool and
	// the vhost must change together: a pool nothing passes to is never
	// reached, and a fastcgi_pass to a missing pool returns 502.
	OperationWebsitePHPSet   OperationType = "website.php.set"
	OperationWebsitePHPUnset OperationType = "website.php.unset"

	OperationSSLIssue        OperationType = "ssl.issue"
	OperationSSLRenew        OperationType = "ssl.renew"
	OperationSSLRevoke       OperationType = "ssl.revoke"
	OperationSSLStatus       OperationType = "ssl.status"
	OperationSSLCapabilities OperationType = "ssl.capabilities"

	// Database management. The Agent drives the local database servers; the
	// API never holds a connection to a managed engine.
	OperationDatabaseEngines      OperationType = "database.engines"
	OperationDatabaseList         OperationType = "database.list"
	OperationDatabaseCreate       OperationType = "database.create"
	OperationDatabaseDelete       OperationType = "database.delete"
	OperationDatabaseSize         OperationType = "database.size"
	OperationDatabaseUserList     OperationType = "database.user.list"
	OperationDatabaseUserCreate   OperationType = "database.user.create"
	OperationDatabaseUserDelete   OperationType = "database.user.delete"
	OperationDatabaseUserPassword OperationType = "database.user.password"
	OperationDatabaseUserGrant    OperationType = "database.user.grant"

	// phpMyAdmin is installed from the host's package manager and served on
	// one name the operator chooses. It is never enabled by default.
	OperationPHPMyAdminStatus    OperationType = "phpmyadmin.status"
	OperationPHPMyAdminInstall   OperationType = "phpmyadmin.install"
	OperationPHPMyAdminUninstall OperationType = "phpmyadmin.uninstall"

	// Node.js. An application is somebody else's code, so every one of these
	// runs it as the website's own account and never as the Agent's.
	// The firewall. Every change is applied provisionally and undone unless it
	// is confirmed inside its window, which is why there are four operations
	// here rather than one per verb (CLAUDE.md section 19).
	OperationFirewallStatus   OperationType = "firewall.status"
	OperationFirewallChange   OperationType = "firewall.change"
	OperationFirewallConfirm  OperationType = "firewall.confirm"
	OperationFirewallRollback OperationType = "firewall.rollback"

	// Apache is the backend of the hybrid arrangement. Its per-site
	// configuration is written by the website operations, which already carry
	// everything a vhost needs; these two are about the server itself.
	OperationApacheStatus  OperationType = "apache.status"
	OperationApacheInstall OperationType = "apache.install"

	OperationNodeVersions   OperationType = "node.versions"
	OperationNodeInstall    OperationType = "node.install"
	OperationNodeUninstall  OperationType = "node.uninstall"
	OperationNodeAppDeploy  OperationType = "node.app.deploy"
	OperationNodeAppRemove  OperationType = "node.app.remove"
	OperationNodeAppStart   OperationType = "node.app.start"
	OperationNodeAppStop    OperationType = "node.app.stop"
	OperationNodeAppRestart OperationType = "node.app.restart"
	OperationNodeAppStatus  OperationType = "node.app.status"
	OperationNodeAppLogs    OperationType = "node.app.logs"
	OperationNodeAppInstall OperationType = "node.app.dependencies"

	// File manager. Every one of these carries a caller-supplied path, so
	// every handler resolves it through pathsec before touching the disk.
	OperationFileList    OperationType = "file.list"
	OperationFileStat    OperationType = "file.stat"
	OperationFileRead    OperationType = "file.read"
	OperationFileWrite   OperationType = "file.write"
	OperationFileMkdir   OperationType = "file.mkdir"
	OperationFileCreate  OperationType = "file.create"
	OperationFileDelete  OperationType = "file.delete"
	OperationFileCopy    OperationType = "file.copy"
	OperationFileMove    OperationType = "file.move"
	OperationFileChmod   OperationType = "file.chmod"
	OperationFileArchive OperationType = "file.archive"
	OperationFileExtract OperationType = "file.extract"
	OperationFileSearch  OperationType = "file.search"

	// Logs. Every one of these names a *catalogue key*, never a path: see
	// agent/internal/logs. A log reader that took a path would be a file
	// reader with no restrictions at all.
	OperationLogList OperationType = "log.list"
	OperationLogTail OperationType = "log.tail"
	OperationLogRead OperationType = "log.read"

	// Asynchronous execution control.
	OperationJobStatus OperationType = "job.status"
	OperationJobCancel OperationType = "job.cancel"
	OperationJobList   OperationType = "job.list"
)

// allowedOperations is the allowlist consulted by Validate. An operation that
// is not present here can never reach a handler.
var allowedOperations = map[OperationType]struct{}{
	OperationPing:                 {},
	OperationInfo:                 {},
	OperationSystemInfo:           {},
	OperationMetricsCPU:           {},
	OperationMetricsMemory:        {},
	OperationMetricsDisk:          {},
	OperationMetricsNetwork:       {},
	OperationMetricsLoad:          {},
	OperationProcessList:          {},
	OperationServiceDetect:        {},
	OperationServiceAction:        {},
	OperationServiceList:          {},
	OperationServiceStatus:        {},
	OperationWebsiteCreate:        {},
	OperationWebsiteDelete:        {},
	OperationWebsiteUpdate:        {},
	OperationWebsiteStatus:        {},
	OperationWebsiteLogs:          {},
	OperationNginxValidate:        {},
	OperationNginxReload:          {},
	OperationPHPVersions:          {},
	OperationPHPInstall:           {},
	OperationPHPUninstall:         {},
	OperationPHPPoolCreate:        {},
	OperationPHPPoolDelete:        {},
	OperationPHPPoolStatus:        {},
	OperationPHPExtensions:        {},
	OperationFirewallStatus:       {},
	OperationFirewallChange:       {},
	OperationFirewallConfirm:      {},
	OperationFirewallRollback:     {},
	OperationApacheStatus:         {},
	OperationApacheInstall:        {},
	OperationWebsitePHPSet:        {},
	OperationWebsitePHPUnset:      {},
	OperationSSLIssue:             {},
	OperationSSLRenew:             {},
	OperationSSLRevoke:            {},
	OperationSSLStatus:            {},
	OperationSSLCapabilities:      {},
	OperationDatabaseEngines:      {},
	OperationDatabaseList:         {},
	OperationDatabaseCreate:       {},
	OperationDatabaseDelete:       {},
	OperationDatabaseSize:         {},
	OperationDatabaseUserList:     {},
	OperationDatabaseUserCreate:   {},
	OperationDatabaseUserDelete:   {},
	OperationDatabaseUserPassword: {},
	OperationDatabaseUserGrant:    {},
	OperationPHPMyAdminStatus:     {},
	OperationPHPMyAdminInstall:    {},
	OperationPHPMyAdminUninstall:  {},
	OperationNodeVersions:         {},
	OperationNodeInstall:          {},
	OperationNodeUninstall:        {},
	OperationNodeAppDeploy:        {},
	OperationNodeAppRemove:        {},
	OperationNodeAppStart:         {},
	OperationNodeAppStop:          {},
	OperationNodeAppRestart:       {},
	OperationNodeAppStatus:        {},
	OperationNodeAppLogs:          {},
	OperationNodeAppInstall:       {},
	OperationFileList:             {},
	OperationFileStat:             {},
	OperationFileRead:             {},
	OperationFileWrite:            {},
	OperationFileMkdir:            {},
	OperationFileCreate:           {},
	OperationFileDelete:           {},
	OperationFileCopy:             {},
	OperationFileMove:             {},
	OperationFileChmod:            {},
	OperationFileArchive:          {},
	OperationFileExtract:          {},
	OperationFileSearch:           {},
	OperationLogList:              {},
	OperationLogTail:              {},
	OperationLogRead:              {},
	OperationJobStatus:            {},
	OperationJobCancel:            {},
	OperationJobList:              {},
}

// ErrUnknownOperation is returned for any operation outside the allowlist.
var ErrUnknownOperation = errors.New("unknown operation")

// IsAllowed reports whether op is on the operation allowlist.
func IsAllowed(op OperationType) bool {
	_, ok := allowedOperations[op]
	return ok
}

// AllowedOperations returns the allowlist, for diagnostics and tests.
func AllowedOperations() []OperationType {
	ops := make([]OperationType, 0, len(allowedOperations))
	for op := range allowedOperations {
		ops = append(ops, op)
	}
	return ops
}

// ExecutionMode selects synchronous or asynchronous execution.
type ExecutionMode string

// Execution modes. The zero value is treated as ModeSync so an omitted field
// cannot accidentally detach an operation from its caller.
const (
	ModeSync  ExecutionMode = "sync"
	ModeAsync ExecutionMode = "async"
)

// jobControlOperations may never be submitted asynchronously: dispatching them
// through the job runner would let a caller queue a job that cancels jobs,
// which is both useless and a way to obscure intent in the audit trail.
var jobControlOperations = map[OperationType]struct{}{
	OperationJobStatus: {},
	OperationJobCancel: {},
	OperationJobList:   {},
}

// IsJobControl reports whether op manages the job runner itself.
func IsJobControl(op OperationType) bool {
	_, ok := jobControlOperations[op]
	return ok
}

// maxRequestIDLength bounds a caller-supplied correlation ID before it reaches
// logs or an audit record.
const maxRequestIDLength = 64

// Request is a single typed operation sent from the API to the Agent.
type Request struct {
	// Operation must be a member of the allowlist.
	Operation OperationType `json:"operation"`
	// RequestID correlates agent logs with API logs.
	RequestID string `json:"request_id"`
	// Token authenticates the caller. It is a shared secret and must never be
	// logged (CLAUDE.md section 14).
	Token string `json:"token,omitempty"`
	// Mode selects synchronous or asynchronous execution.
	Mode ExecutionMode `json:"mode,omitempty"`
	// Payload carries operation-specific arguments. Each handler decodes it
	// into its own typed struct; it is never interpreted as a shell string.
	Payload map[string]any `json:"payload,omitempty"`
}

// Async reports whether the request asked for background execution.
func (r Request) Async() bool { return r.Mode == ModeAsync }

// Validate enforces the allowlist and the required envelope fields.
//
// It deliberately does not check the token: authentication is the transport's
// job and happens before validation, so a caller that fails it never reaches
// operation dispatch at all.
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
	if len(r.RequestID) > maxRequestIDLength {
		return fmt.Errorf("request_id must be at most %d characters", maxRequestIDLength)
	}
	switch r.Mode {
	case "", ModeSync, ModeAsync:
	default:
		return fmt.Errorf("mode must be %q or %q", ModeSync, ModeAsync)
	}
	if r.Async() && IsJobControl(r.Operation) {
		return fmt.Errorf("operation %q cannot be run asynchronously", r.Operation)
	}
	return nil
}

// Status is the outcome of an operation.
type Status string

// Operation outcomes.
const (
	StatusSuccess Status = "SUCCESS"
	StatusFailed  Status = "FAILED"
	// StatusAccepted is returned when an asynchronous request has been queued.
	StatusAccepted Status = "ACCEPTED"
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

// Error codes returned by the Agent transport and handlers.
const (
	CodeUnknownOperation = "UNKNOWN_OPERATION"
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeInvalidPayload   = "INVALID_PAYLOAD"
	CodeUnauthorized     = "UNAUTHORIZED"
	CodeInternal         = "INTERNAL_ERROR"
	CodeTimeout          = "OPERATION_TIMEOUT"
	CodeUnsupported      = "UNSUPPORTED"
	CodeNotFound         = "NOT_FOUND"
	CodeBusy             = "AGENT_BUSY"
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

// NewAccepted builds the reply to an accepted asynchronous request.
func NewAccepted(requestID, jobID string) Response {
	return Response{
		Status:    StatusAccepted,
		RequestID: requestID,
		Data:      map[string]any{"job_id": jobID},
	}
}

// JobState is the lifecycle state of an asynchronous operation. The values
// match the job states in PRD.md section 11.
type JobState string

// Job states.
const (
	JobPending   JobState = "PENDING"
	JobRunning   JobState = "RUNNING"
	JobSuccess   JobState = "SUCCESS"
	JobFailed    JobState = "FAILED"
	JobCancelled JobState = "CANCELLED"
)

// Terminal reports whether a job has finished and will not change again.
func (s JobState) Terminal() bool {
	switch s {
	case JobSuccess, JobFailed, JobCancelled:
		return true
	default:
		return false
	}
}
