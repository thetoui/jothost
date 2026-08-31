package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// Typed wrappers over the agent protocol.
//
// Callers use these rather than building a protocol.Request by hand: the
// operation name, the payload shape, and the result type are decided in one
// place, so a caller cannot pass a payload the Agent will reject at runtime.

// ErrOperationFailed is returned when the Agent refuses or fails an operation.
// The structured code is preserved so callers can branch on it.
type ErrOperationFailed struct {
	Code    string
	Message string
}

func (e *ErrOperationFailed) Error() string {
	return "agent operation failed: " + e.Code + ": " + e.Message
}

// IsUnsupported reports whether the Agent said the host cannot do this.
//
// Callers use it to hide a feature rather than show an error: a host without
// systemd is a configuration, not a fault.
func IsUnsupported(err error) bool {
	var failed *ErrOperationFailed
	return errors.As(err, &failed) && failed.Code == protocol.CodeUnsupported
}

// IsBusy reports whether the Agent refused because it is already running as
// many jobs as it allows.
//
// It is not a failure of the work: the same request a moment later succeeds.
// Callers put the job back in the queue rather than marking it failed, which
// is the difference between a burst of work being paced and half of it being
// reported broken.
func IsBusy(err error) bool {
	var failed *ErrOperationFailed
	return errors.As(err, &failed) && failed.Code == protocol.CodeBusy
}

// call runs an operation and decodes its result into dst.
func (c *Client) call(ctx context.Context, requestID string, op protocol.OperationType, payload map[string]any, dst any) error {
	resp, err := c.Do(ctx, protocol.Request{
		Operation: op,
		RequestID: requestID,
		Payload:   payload,
	})
	if err != nil {
		return err
	}

	if resp.Status != protocol.StatusSuccess {
		code, message := protocol.CodeInternal, "Operation failed"
		if resp.Error != nil {
			code, message = resp.Error.Code, resp.Error.Message
		}
		return &ErrOperationFailed{Code: code, Message: message}
	}

	if dst == nil {
		return nil
	}

	encoded, err := json.Marshal(resp.Data)
	if err != nil {
		return fmt.Errorf("encode agent result: %w", err)
	}
	if err := json.Unmarshal(encoded, dst); err != nil {
		return fmt.Errorf("decode agent result: %w", err)
	}
	return nil
}

// AgentInfo describes the Agent and what it can do on this host.
type AgentInfo struct {
	Version       string          `json:"version"`
	Commit        string          `json:"commit"`
	BuildDate     string          `json:"build_date"`
	UptimeSeconds int64           `json:"uptime_seconds"`
	Operations    []string        `json:"operations"`
	Capabilities  map[string]bool `json:"capabilities"`
}

// Info reports the Agent's version and capabilities.
func (c *Client) Info(ctx context.Context, requestID string) (AgentInfo, error) {
	var info AgentInfo
	err := c.call(ctx, requestID, protocol.OperationInfo, nil, &info)
	return info, err
}

// SystemInfo describes the host.
type SystemInfo struct {
	Hostname      string `json:"hostname"`
	OSName        string `json:"os_name"`
	OSVersion     string `json:"os_version"`
	KernelVersion string `json:"kernel_version"`
	Architecture  string `json:"architecture"`
	UptimeSeconds uint64 `json:"uptime_seconds"`
	BootTime      string `json:"boot_time"`
	Cores         int    `json:"cores"`
}

// System reports the host's identity and uptime.
func (c *Client) System(ctx context.Context, requestID string) (SystemInfo, error) {
	var info SystemInfo
	err := c.call(ctx, requestID, protocol.OperationSystemInfo, nil, &info)
	return info, err
}

// CPUStats is a CPU usage sample.
type CPUStats struct {
	UsagePercent  float64 `json:"usage_percent"`
	UserPercent   float64 `json:"user_percent"`
	SystemPercent float64 `json:"system_percent"`
	IOWaitPercent float64 `json:"iowait_percent"`
	IdlePercent   float64 `json:"idle_percent"`
	Cores         int     `json:"cores"`
	SampleWindow  string  `json:"sample_window"`
}

// CPU reports CPU usage since the Agent's previous sample.
func (c *Client) CPU(ctx context.Context, requestID string) (CPUStats, error) {
	var stats CPUStats
	err := c.call(ctx, requestID, protocol.OperationMetricsCPU, nil, &stats)
	return stats, err
}

// MemoryStats is a memory usage sample.
type MemoryStats struct {
	TotalBytes      uint64  `json:"total_bytes"`
	AvailableBytes  uint64  `json:"available_bytes"`
	UsedBytes       uint64  `json:"used_bytes"`
	FreeBytes       uint64  `json:"free_bytes"`
	BuffersBytes    uint64  `json:"buffers_bytes"`
	CachedBytes     uint64  `json:"cached_bytes"`
	SwapTotalBytes  uint64  `json:"swap_total_bytes"`
	SwapUsedBytes   uint64  `json:"swap_used_bytes"`
	SwapFreeBytes   uint64  `json:"swap_free_bytes"`
	UsedPercent     float64 `json:"used_percent"`
	SwapUsedPercent float64 `json:"swap_used_percent"`
}

// Memory reports memory usage.
func (c *Client) Memory(ctx context.Context, requestID string) (MemoryStats, error) {
	var stats MemoryStats
	err := c.call(ctx, requestID, protocol.OperationMetricsMemory, nil, &stats)
	return stats, err
}

// LoadStats is the system load average.
type LoadStats struct {
	Load1        float64 `json:"load_1"`
	Load5        float64 `json:"load_5"`
	Load15       float64 `json:"load_15"`
	RunningProcs int     `json:"running_processes"`
	TotalProcs   int     `json:"total_processes"`
	Cores        int     `json:"cores"`
	LoadPerCore  float64 `json:"load_per_core"`
}

// Load reports the load average.
func (c *Client) Load(ctx context.Context, requestID string) (LoadStats, error) {
	var stats LoadStats
	err := c.call(ctx, requestID, protocol.OperationMetricsLoad, nil, &stats)
	return stats, err
}

// Filesystem is one mounted filesystem's usage.
type Filesystem struct {
	Device            string  `json:"device"`
	MountPoint        string  `json:"mount_point"`
	Type              string  `json:"type"`
	TotalBytes        uint64  `json:"total_bytes"`
	UsedBytes         uint64  `json:"used_bytes"`
	FreeBytes         uint64  `json:"free_bytes"`
	AvailableBytes    uint64  `json:"available_bytes"`
	UsedPercent       float64 `json:"used_percent"`
	InodesTotal       uint64  `json:"inodes_total"`
	InodesUsed        uint64  `json:"inodes_used"`
	InodesFree        uint64  `json:"inodes_free"`
	InodesUsedPercent float64 `json:"inodes_used_percent"`
	ReadOnly          bool    `json:"read_only"`
}

// DiskStats is a disk usage sample.
type DiskStats struct {
	Filesystems []Filesystem `json:"filesystems"`
	TotalBytes  uint64       `json:"total_bytes"`
	UsedBytes   uint64       `json:"used_bytes"`
}

// Disk reports filesystem usage. An empty mountPoint reports every real
// filesystem.
func (c *Client) Disk(ctx context.Context, requestID, mountPoint string) (DiskStats, error) {
	var payload map[string]any
	if mountPoint != "" {
		payload = map[string]any{"mount_point": mountPoint}
	}

	var stats DiskStats
	err := c.call(ctx, requestID, protocol.OperationMetricsDisk, payload, &stats)
	return stats, err
}

// NetworkInterface is one interface's counters and rates.
type NetworkInterface struct {
	Name             string  `json:"name"`
	RxBytes          uint64  `json:"rx_bytes"`
	RxPackets        uint64  `json:"rx_packets"`
	RxErrors         uint64  `json:"rx_errors"`
	RxDropped        uint64  `json:"rx_dropped"`
	TxBytes          uint64  `json:"tx_bytes"`
	TxPackets        uint64  `json:"tx_packets"`
	TxErrors         uint64  `json:"tx_errors"`
	TxDropped        uint64  `json:"tx_dropped"`
	RxBytesPerSecond float64 `json:"rx_bytes_per_second"`
	TxBytesPerSecond float64 `json:"tx_bytes_per_second"`
}

// NetworkStats is a network sample.
type NetworkStats struct {
	Interfaces   []NetworkInterface `json:"interfaces"`
	TotalRxBytes uint64             `json:"total_rx_bytes"`
	TotalTxBytes uint64             `json:"total_tx_bytes"`
	SampleWindow string             `json:"sample_window"`
}

// Network reports interface counters and rates.
func (c *Client) Network(ctx context.Context, requestID string) (NetworkStats, error) {
	var stats NetworkStats
	err := c.call(ctx, requestID, protocol.OperationMetricsNetwork, nil, &stats)
	return stats, err
}

// Process is one running process.
type Process struct {
	PID            int     `json:"pid"`
	PPID           int     `json:"ppid"`
	Name           string  `json:"name"`
	State          string  `json:"state"`
	User           string  `json:"user"`
	UID            int     `json:"uid"`
	MemoryRSSBytes uint64  `json:"memory_rss_bytes"`
	MemoryPercent  float64 `json:"memory_percent"`
	CPUTimeSeconds float64 `json:"cpu_time_seconds"`
	Threads        int     `json:"threads"`
	// Command is host data returned verbatim. Treat it as untrusted when
	// rendering it anywhere.
	Command string `json:"command"`
}

// ProcessListResult is a process listing.
type ProcessListResult struct {
	Processes []Process `json:"processes"`
	Count     int       `json:"count"`
}

// ProcessList reports the running processes. sortBy is "memory" or "cpu".
func (c *Client) ProcessList(ctx context.Context, requestID string, limit int, sortBy string) (ProcessListResult, error) {
	payload := map[string]any{}
	if limit > 0 {
		payload["limit"] = limit
	}
	if sortBy != "" {
		payload["sort_by"] = sortBy
	}

	var result ProcessListResult
	err := c.call(ctx, requestID, protocol.OperationProcessList, payload, &result)
	return result, err
}

// ServiceStatus is a service's state.
type ServiceStatus struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ActiveState string `json:"active_state"`
	SubState    string `json:"sub_state"`
	LoadState   string `json:"load_state"`
	// Enabled is nil when the service manager cannot say, which is different
	// from "not enabled".
	Enabled *bool `json:"enabled"`
	Running bool  `json:"running"`
	MainPID int   `json:"main_pid"`
}

// ServiceListResult is a service listing.
type ServiceListResult struct {
	Services []ServiceStatus `json:"services"`
	Count    int             `json:"count"`
}

// ServiceList reports the state of the named services.
func (c *Client) ServiceList(ctx context.Context, requestID string, names []string) (ServiceListResult, error) {
	var result ServiceListResult
	err := c.call(ctx, requestID, protocol.OperationServiceList,
		map[string]any{"names": names}, &result)
	return result, err
}

// ServiceStatusOf reports one service's state.
func (c *Client) ServiceStatusOf(ctx context.Context, requestID, name string) (ServiceStatus, error) {
	var status ServiceStatus
	err := c.call(ctx, requestID, protocol.OperationServiceStatus,
		map[string]any{"name": name}, &status)
	return status, err
}

// Job is an asynchronous operation's state.
type Job struct {
	ID          string                 `json:"id"`
	Operation   protocol.OperationType `json:"operation"`
	RequestID   string                 `json:"request_id"`
	State       protocol.JobState      `json:"state"`
	Progress    int                    `json:"progress"`
	Message     string                 `json:"message,omitempty"`
	CreatedAt   string                 `json:"created_at"`
	StartedAt   *string                `json:"started_at,omitempty"`
	CompletedAt *string                `json:"completed_at,omitempty"`
	Result      map[string]any         `json:"result,omitempty"`
	Error       *protocol.Error        `json:"error,omitempty"`
}

// SubmitAsync queues an operation and returns its job ID.
func (c *Client) SubmitAsync(ctx context.Context, requestID string, op protocol.OperationType, payload map[string]any) (string, error) {
	resp, err := c.Do(ctx, protocol.Request{
		Operation: op,
		RequestID: requestID,
		Mode:      protocol.ModeAsync,
		Payload:   payload,
	})
	if err != nil {
		return "", err
	}

	if resp.Status != protocol.StatusAccepted {
		code, message := protocol.CodeInternal, "Operation failed"
		if resp.Error != nil {
			code, message = resp.Error.Code, resp.Error.Message
		}
		return "", &ErrOperationFailed{Code: code, Message: message}
	}

	jobID, ok := resp.Data["job_id"].(string)
	if !ok || jobID == "" {
		return "", errors.New("agent accepted the operation but returned no job id")
	}
	return jobID, nil
}

// JobStatus reports an asynchronous operation's progress.
func (c *Client) JobStatus(ctx context.Context, requestID, jobID string) (Job, error) {
	var job Job
	err := c.call(ctx, requestID, protocol.OperationJobStatus,
		map[string]any{"job_id": jobID}, &job)
	return job, err
}

// CancelJob stops a running operation.
func (c *Client) CancelJob(ctx context.Context, requestID, jobID string) error {
	return c.call(ctx, requestID, protocol.OperationJobCancel,
		map[string]any{"job_id": jobID}, nil)
}

// JobListResult is the Agent's job table.
type JobListResult struct {
	Jobs  []Job `json:"jobs"`
	Count int   `json:"count"`
}

// JobList reports the Agent's retained jobs.
func (c *Client) JobList(ctx context.Context, requestID string) (JobListResult, error) {
	var result JobListResult
	err := c.call(ctx, requestID, protocol.OperationJobList, nil, &result)
	return result, err
}

// PHPVersion is one PHP version the host has.
type PHPVersion struct {
	Version    string `json:"version"`
	Full       string `json:"full_version"`
	BinaryPath string `json:"binary_path"`
	FPMService string `json:"fpm_service"`
	PoolDir    string `json:"pool_dir"`
	ConfigPath string `json:"config_path"`
	Installed  bool   `json:"installed"`
}

// PHPVersionsResult is the Agent's PHP inventory.
type PHPVersionsResult struct {
	Versions []PHPVersion `json:"versions"`
	Count    int          `json:"count"`
	// CanInstall reports whether the host has a package manager. The panel
	// hides the install control when it does not, rather than offering a
	// button that always fails.
	CanInstall     bool   `json:"can_install"`
	PackageManager string `json:"package_manager"`
}

// PHPVersions reports the PHP versions installed on the host.
func (c *Client) PHPVersions(ctx context.Context, requestID string) (PHPVersionsResult, error) {
	var result PHPVersionsResult
	err := c.call(ctx, requestID, protocol.OperationPHPVersions, nil, &result)
	return result, err
}

// PHPExtensionsResult lists a version's loaded extensions.
type PHPExtensionsResult struct {
	Version    string   `json:"version"`
	Extensions []string `json:"extensions"`
	Count      int      `json:"count"`
}

// PHPExtensions reports the extensions a version has loaded.
func (c *Client) PHPExtensions(ctx context.Context, requestID, version string) (PHPExtensionsResult, error) {
	var result PHPExtensionsResult
	err := c.call(ctx, requestID, protocol.OperationPHPExtensions,
		map[string]any{"version": version}, &result)
	return result, err
}

// SSLCapabilities reports which certificate providers a host can use.
type SSLCapabilities struct {
	// SelfSigned needs nothing but the Agent itself.
	SelfSigned bool `json:"selfsigned"`
	// LetsEncrypt needs certbot, public DNS, and a reachable challenge path.
	LetsEncrypt bool `json:"letsencrypt"`
}

// SSLProviders reports which certificate providers this host can use.
func (c *Client) SSLProviders(ctx context.Context, requestID string) (SSLCapabilities, error) {
	var result SSLCapabilities
	err := c.call(ctx, requestID, protocol.OperationSSLCapabilities, nil, &result)
	return result, err
}

// SSLCertificate is a certificate as the Agent found it on disk.
type SSLCertificate struct {
	Domain       string   `json:"domain"`
	Domains      []string `json:"domains"`
	Provider     string   `json:"provider"`
	CertPath     string   `json:"certificate_path"`
	KeyPath      string   `json:"private_key_path"`
	Issuer       string   `json:"issuer"`
	Fingerprint  string   `json:"fingerprint"`
	IssuedAt     string   `json:"issued_at"`
	ExpiresAt    string   `json:"expires_at"`
	SelfSigned   bool     `json:"self_signed"`
	Present      bool     `json:"present"`
	NeedsRenewal bool     `json:"needs_renewal"`
}

// SSLStatus reports the certificate currently on disk for a domain.
func (c *Client) SSLStatus(ctx context.Context, requestID, domain string) (SSLCertificate, error) {
	var result SSLCertificate
	err := c.call(ctx, requestID, protocol.OperationSSLStatus,
		map[string]any{"domain": domain}, &result)
	return result, err
}

// ---------------------------------------------------------------- databases

// DatabaseEngine describes one database server as the Agent found it.
type DatabaseEngine struct {
	Engine    string `json:"engine"`
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// SupportsHostPatterns reports whether accounts are a user/host pair, so
	// the panel shows the host control only where it means something.
	SupportsHostPatterns bool `json:"supports_host_patterns"`
}

// DatabaseEnginesResult is the answer to "what can this host run".
type DatabaseEnginesResult struct {
	Engines    []DatabaseEngine `json:"engines"`
	Available  bool             `json:"available"`
	Privileges []string         `json:"privileges"`
}

// DatabaseEngines reports which database servers this host runs.
func (c *Client) DatabaseEngines(ctx context.Context, requestID string) (DatabaseEnginesResult, error) {
	var result DatabaseEnginesResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseEngines, nil, &result)
	return result, err
}

// DatabaseRecord is one database as the server reports it.
type DatabaseRecord struct {
	Name      string `json:"name"`
	Engine    string `json:"engine"`
	Charset   string `json:"charset,omitempty"`
	Collation string `json:"collation,omitempty"`
	Owner     string `json:"owner,omitempty"`
	SizeBytes int64  `json:"size_bytes"`
}

// DatabaseListResult lists the databases on one engine.
type DatabaseListResult struct {
	Engine    string           `json:"engine"`
	Databases []DatabaseRecord `json:"databases"`
	Count     int              `json:"count"`
}

// DatabaseList reports the databases the host actually has.
//
// The panel uses it to reconcile: a database dropped from a shell is gone
// whether or not the panel's table still lists it.
func (c *Client) DatabaseList(ctx context.Context, requestID, engine string) (DatabaseListResult, error) {
	var result DatabaseListResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseList,
		map[string]any{"engine": engine}, &result)
	return result, err
}

// DatabaseCreateResult reports a created database as the server describes it.
type DatabaseCreateResult struct {
	Engine string `json:"engine"`
	Name   string `json:"name"`
	// Charset and Collation are what the server settled on, which is not
	// always what was asked for.
	Charset   string `json:"charset"`
	Collation string `json:"collation"`
	SizeBytes int64  `json:"size_bytes"`
	Created   bool   `json:"created"`
}

// DatabaseCreate creates a database on the host.
func (c *Client) DatabaseCreate(ctx context.Context, requestID, engine, name string) (DatabaseCreateResult, error) {
	var result DatabaseCreateResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseCreate,
		map[string]any{"engine": engine, "name": name}, &result)
	return result, err
}

// DatabaseDelete drops a database on the host.
func (c *Client) DatabaseDelete(ctx context.Context, requestID, engine, name string) error {
	return c.call(ctx, requestID, protocol.OperationDatabaseDelete,
		map[string]any{"engine": engine, "name": name}, nil)
}

// DatabaseSizeResult reports one database's size.
type DatabaseSizeResult struct {
	Engine    string `json:"engine"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
}

// DatabaseSize reports a database's size in bytes.
func (c *Client) DatabaseSize(ctx context.Context, requestID, engine, name string) (DatabaseSizeResult, error) {
	var result DatabaseSizeResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseSize,
		map[string]any{"engine": engine, "name": name}, &result)
	return result, err
}

// DatabaseUserResult reports an account, and its password on creation.
//
// The password field is populated exactly once, by the operation that actually
// set it. Created is false when the account was already on the server, and the
// password is then empty: an existing account keeps the password it had, and
// reporting a new one would hand the operator a credential the server never
// accepted. It is never logged and never stored in plaintext.
type DatabaseUserResult struct {
	Engine   string `json:"engine"`
	Username string `json:"username"`
	Host     string `json:"host,omitempty"`
	Password string `json:"password,omitempty"`
	Created  bool   `json:"created"`
	Changed  bool   `json:"changed"`
	Deleted  bool   `json:"deleted"`
}

// DatabaseUserRequest names an account and, optionally, its password.
type DatabaseUserRequest struct {
	Engine   string
	Username string
	Host     string
	// Password may be empty, in which case the Agent generates one. Letting
	// the host invent it means a password the operator never chose is never
	// weaker than the panel's generator, and an operator who wants their own
	// can still supply it.
	Password string
}

func (r DatabaseUserRequest) payload() map[string]any {
	payload := map[string]any{"engine": r.Engine, "username": r.Username}
	if r.Host != "" {
		payload["host"] = r.Host
	}
	if r.Password == "" {
		payload["generate"] = true
	} else {
		payload["password"] = r.Password
	}
	return payload
}

// DatabaseUserCreate adds an account to a database server.
func (c *Client) DatabaseUserCreate(ctx context.Context, requestID string,
	req DatabaseUserRequest,
) (DatabaseUserResult, error) {
	var result DatabaseUserResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseUserCreate, req.payload(), &result)
	return result, err
}

// DatabaseUserPassword changes an account's password.
func (c *Client) DatabaseUserPassword(ctx context.Context, requestID string,
	req DatabaseUserRequest,
) (DatabaseUserResult, error) {
	var result DatabaseUserResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseUserPassword, req.payload(), &result)
	return result, err
}

// DatabaseUserDelete removes an account from a database server.
func (c *Client) DatabaseUserDelete(ctx context.Context, requestID, engine, username, host string) error {
	payload := map[string]any{"engine": engine, "username": username}
	if host != "" {
		payload["host"] = host
	}
	return c.call(ctx, requestID, protocol.OperationDatabaseUserDelete, payload, nil)
}

// DatabaseUserList lists the accounts on one engine.
type DatabaseUserListResult struct {
	Engine string `json:"engine"`
	Users  []struct {
		Username string `json:"username"`
		Host     string `json:"host,omitempty"`
		Engine   string `json:"engine"`
	} `json:"users"`
	Count int `json:"count"`
}

// DatabaseUsers reports the accounts the host actually has.
func (c *Client) DatabaseUsers(ctx context.Context, requestID, engine string) (DatabaseUserListResult, error) {
	var result DatabaseUserListResult
	err := c.call(ctx, requestID, protocol.OperationDatabaseUserList,
		map[string]any{"engine": engine}, &result)
	return result, err
}

// DatabaseGrant sets an account's access to one database.
//
// An empty privilege revokes. The panel expresses "no access" through the same
// call as every other level, so there is one code path deciding who can reach
// a database rather than two that can disagree.
func (c *Client) DatabaseGrant(ctx context.Context, requestID, engine, username, host,
	database, privilege string,
) error {
	payload := map[string]any{
		"engine": engine, "username": username, "name": database, "privilege": privilege,
	}
	if host != "" {
		payload["host"] = host
	}
	return c.call(ctx, requestID, protocol.OperationDatabaseUserGrant, payload, nil)
}

// PHPMyAdminStatus describes phpMyAdmin on the managed host.
type PHPMyAdminStatus struct {
	Installed  bool   `json:"installed"`
	Served     bool   `json:"served"`
	Webroot    string `json:"webroot,omitempty"`
	ServerName string `json:"server_name,omitempty"`
	URL        string `json:"url,omitempty"`
	PHPVersion string `json:"php_version,omitempty"`
	// CanInstall reports whether the host has what installation needs, so the
	// panel can disable the control rather than offer one that fails halfway.
	CanInstall bool   `json:"can_install"`
	Detail     string `json:"detail,omitempty"`
}

// PHPMyAdmin reports whether the database console is installed and served.
func (c *Client) PHPMyAdmin(ctx context.Context, requestID string) (PHPMyAdminStatus, error) {
	var status PHPMyAdminStatus
	err := c.call(ctx, requestID, protocol.OperationPHPMyAdminStatus, nil, &status)
	return status, err
}

// ------------------------------------------------------------------ Node.js

// NodeVersion is one Node.js runtime on the host.
type NodeVersion struct {
	Version    string `json:"version"`
	Full       string `json:"full_version"`
	BinaryPath string `json:"binary_path"`
	NPMVersion string `json:"npm_version"`
}

// NodeOffer is a Node.js release line the host could install.
type NodeOffer struct {
	Version string `json:"version"`
	Package string `json:"package"`
	Label   string `json:"label"`
}

// NodeVersionsResult reports what the host runs and what it could run.
type NodeVersionsResult struct {
	Versions  []NodeVersion `json:"versions"`
	Count     int           `json:"count"`
	Available bool          `json:"available"`
	Offers    []NodeOffer   `json:"offers"`
	// CanInstall reports whether a package manager is present, so the panel
	// hides the install control rather than offering one that always fails.
	CanInstall     bool   `json:"can_install"`
	PackageManager string `json:"package_manager"`
	// ManagedBy is "systemd" or "agent": which mechanism runs applications on
	// this host. It changes what "logs" can return, so the panel says so.
	ManagedBy string `json:"managed_by"`
}

// NodeVersions reports the Node.js runtimes on the host.
// IsNotFound reports whether the Agent said the thing asked about is not there.
func IsNotFound(err error) bool {
	var failed *ErrOperationFailed
	return errors.As(err, &failed) && failed.Code == protocol.CodeNotFound
}

// IsInvalidRequest reports whether the Agent refused the request itself — the
// action is understood and not allowed, which is a conflict rather than a
// malformed message.
func IsInvalidRequest(err error) bool {
	var failed *ErrOperationFailed
	return errors.As(err, &failed) && failed.Code == protocol.CodeInvalidRequest
}

// IsInvalidPayload reports whether the Agent rejected the payload's shape or
// values.
func IsInvalidPayload(err error) bool {
	var failed *ErrOperationFailed
	return errors.As(err, &failed) && failed.Code == protocol.CodeInvalidPayload
}

// Message returns the Agent's own explanation for a failure.
//
// The Agent's structured errors carry a message written for a person; anything
// else carries Go's. This is what a caller records in an audit trail or shows
// a user, and it is deliberately not the full error string, which repeats the
// code and the wrapper.
func Message(err error) string {
	var failed *ErrOperationFailed
	if errors.As(err, &failed) {
		return failed.Message
	}
	if err == nil {
		return ""
	}
	return err.Error()
}

// ServiceDetectResult is the set of services the host actually has.
type ServiceDetectResult struct {
	Services []DetectedService `json:"services"`
	Count    int               `json:"count"`
	// Controllable reports whether this host can start and stop anything at
	// all. False means no service manager: the states below are still true,
	// and nothing can be changed through the panel.
	Controllable bool `json:"controllable"`
	// Manager names the init system: "systemd", "openrc", or empty for a host
	// with neither.
	Manager string `json:"manager"`
}

// DetectedService is one service as the host has it.
type DetectedService struct {
	Key       string   `json:"key"`
	Label     string   `json:"label"`
	Role      string   `json:"role"`
	Summary   string   `json:"summary"`
	Units     []string `json:"units"`
	Protected bool     `json:"protected"`
	Essential bool     `json:"essential"`
	// SelfManaged means another part of the panel owns this daemon's
	// lifecycle, so the init system is not where it is started and stopped.
	SelfManaged bool `json:"self_managed"`
	// SelfManagedBy names that owner, so the panel can explain the absence of
	// the controls rather than showing buttons that refuse.
	SelfManagedBy string `json:"self_managed_by"`

	Installed    bool   `json:"installed"`
	Running      bool   `json:"running"`
	PID          int    `json:"pid"`
	Unit         string `json:"unit"`
	Enabled      *bool  `json:"enabled"`
	ActiveState  string `json:"active_state"`
	SubState     string `json:"sub_state"`
	Controllable bool   `json:"controllable"`
}

// ServiceDetect asks the host which services it has.
func (c *Client) ServiceDetect(ctx context.Context, requestID string) (ServiceDetectResult, error) {
	var result ServiceDetectResult
	err := c.call(ctx, requestID, protocol.OperationServiceDetect,
		map[string]any{}, &result)
	return result, err
}

// ServiceActionResult is a service's state after being acted on.
type ServiceActionResult struct {
	Service string `json:"service"`
	Unit    string `json:"unit"`
	Action  string `json:"action"`
	Running bool   `json:"running"`
	Enabled *bool  `json:"enabled"`
	State   string `json:"state"`
}

// ServiceAction starts, stops, restarts, enables or disables a service.
//
// The service is named by its catalogue key, not by a unit: the Agent chooses
// the unit from its own table, so nothing sent from here can select what is
// acted on.
func (c *Client) ServiceAction(ctx context.Context, requestID, key, action string) (ServiceActionResult, error) {
	var result ServiceActionResult
	err := c.call(ctx, requestID, protocol.OperationServiceAction, map[string]any{
		"service": key,
		"action":  action,
	}, &result)
	return result, err
}

// FirewallRule is one rule as the panel and the Agent both understand it.
type FirewallRule struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Port      string `json:"port"`
	Source    string `json:"source"`
	Comment   string `json:"comment,omitempty"`
}

// FirewallPending is a change that has been applied and not yet confirmed.
type FirewallPending struct {
	ID     string       `json:"id"`
	Kind   string       `json:"kind"`
	Rule   FirewallRule `json:"rule"`
	Policy string       `json:"policy,omitempty"`
	// Deadline is when the Agent undoes the change if nothing confirms it.
	Deadline time.Time `json:"deadline"`
}

// FirewallStatus is the firewall as it stands.
type FirewallStatus struct {
	Available       bool           `json:"available"`
	Enabled         bool           `json:"enabled"`
	DefaultIncoming string         `json:"default_incoming"`
	DefaultOutgoing string         `json:"default_outgoing"`
	Rules           []FirewallRule `json:"rules"`
	Reason          string         `json:"reason,omitempty"`
	// GuardedPorts are the ports no change may close.
	GuardedPorts []int `json:"guarded_ports"`
	// Pending is a change waiting to be confirmed, which is the most important
	// thing on this response: it has a deadline.
	Pending *FirewallPending `json:"pending,omitempty"`
}

// FirewallChange is one change to make.
type FirewallChange struct {
	Kind          string       `json:"kind"`
	Rule          FirewallRule `json:"rule"`
	Policy        string       `json:"policy,omitempty"`
	Direction     string       `json:"direction,omitempty"`
	WindowSeconds int          `json:"window_seconds,omitempty"`
}

// FirewallStatus reads the host's firewall.
func (c *Client) FirewallStatus(ctx context.Context, requestID string) (FirewallStatus, error) {
	var result FirewallStatus
	err := c.call(ctx, requestID, protocol.OperationFirewallStatus, nil, &result)
	return result, err
}

// FirewallChange applies a change provisionally.
func (c *Client) FirewallChange(ctx context.Context, requestID string,
	change FirewallChange,
) (FirewallPending, error) {
	payload := map[string]any{
		"kind":           change.Kind,
		"rule":           change.Rule,
		"policy":         change.Policy,
		"direction":      change.Direction,
		"window_seconds": change.WindowSeconds,
	}

	var result FirewallPending
	err := c.call(ctx, requestID, protocol.OperationFirewallChange, payload, &result)
	return result, err
}

// FirewallConfirm commits a provisional change.
func (c *Client) FirewallConfirm(ctx context.Context, requestID, changeID string) (FirewallPending, error) {
	var result FirewallPending
	err := c.call(ctx, requestID, protocol.OperationFirewallConfirm,
		map[string]any{"change_id": changeID}, &result)
	return result, err
}

// FirewallRollback undoes a provisional change now.
func (c *Client) FirewallRollback(ctx context.Context, requestID, changeID string) (FirewallPending, error) {
	var result FirewallPending
	err := c.call(ctx, requestID, protocol.OperationFirewallRollback,
		map[string]any{"change_id": changeID}, &result)
	return result, err
}

// ApacheStatusResult is what the host reports about the hybrid backend.
type ApacheStatusResult struct {
	Available bool   `json:"available"`
	Running   bool   `json:"running"`
	Version   string `json:"version"`
	Sites     int    `json:"sites"`
	// CanInstall reports whether the Agent has a package manager it could
	// install Apache with, which is a different question from whether Apache
	// is there.
	CanInstall bool `json:"can_install"`
}

// ApacheStatus asks the host what the hybrid arrangement can do.
func (c *Client) ApacheStatus(ctx context.Context, requestID string) (ApacheStatusResult, error) {
	var result ApacheStatusResult
	err := c.call(ctx, requestID, protocol.OperationApacheStatus, nil, &result)
	return result, err
}

// ApacheInstall installs Apache and the modules the arrangement needs.
//
// It sends no package name: the Agent holds the only list, because a name from
// here would be an argument to a package manager running as root.
func (c *Client) ApacheInstall(ctx context.Context, requestID string) (ApacheStatusResult, error) {
	var result ApacheStatusResult
	err := c.call(ctx, requestID, protocol.OperationApacheInstall,
		map[string]any{}, &result)
	return result, err
}

func (c *Client) NodeVersions(ctx context.Context, requestID string) (NodeVersionsResult, error) {
	var result NodeVersionsResult
	err := c.call(ctx, requestID, protocol.OperationNodeVersions, nil, &result)
	return result, err
}

// NodeAppRequest describes an application to the Agent.
//
// Every field is validated again on the Agent, which is where it becomes a
// unit file and a process. This struct exists so the API cannot send a payload
// shaped differently from what the handler expects.
type NodeAppRequest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Root        string            `json:"root"`
	Startup     string            `json:"startup_file"`
	Port        int               `json:"port"`
	User        string            `json:"user"`
	Group       string            `json:"group"`
	Environment map[string]string `json:"environment,omitempty"`
}

// NodeStatus is what an application is doing on the host.
type NodeStatus struct {
	Name   string `json:"name"`
	State  string `json:"state"`
	PID    int    `json:"pid"`
	Port   int    `json:"port"`
	Uptime int64  `json:"uptime_seconds"`
	// ManagedBy is "systemd" or "agent".
	ManagedBy string `json:"managed_by"`
	// Listening separates "the process is up" from "it is answering", which
	// are not the same thing and should not be reported as one.
	Listening bool   `json:"listening"`
	Detail    string `json:"detail"`
	// Environment is what the process will actually see, read back from the
	// host rather than echoed from the request.
	Environment map[string]string `json:"environment,omitempty"`
}

// NodeLogs is the tail of an application's output.
type NodeLogs struct {
	Source     string   `json:"source"`
	Lines      []string `json:"lines"`
	ErrorLines []string `json:"error_lines"`
	OutPath    string   `json:"out_path"`
	ErrorPath  string   `json:"error_path"`
	Detail     string   `json:"detail"`
}

func nodePayload(req NodeAppRequest) map[string]any {
	payload := map[string]any{
		"name": req.Name, "version": req.Version, "root": req.Root,
		"startup_file": req.Startup, "port": req.Port,
		"user": req.User, "group": req.Group,
	}
	if len(req.Environment) > 0 {
		payload["environment"] = req.Environment
	}
	return payload
}

// NodeDeploy makes an application ready to run.
func (c *Client) NodeDeploy(ctx context.Context, requestID string, req NodeAppRequest) error {
	return c.call(ctx, requestID, protocol.OperationNodeAppDeploy, nodePayload(req), nil)
}

// NodeRemove takes an application's runtime state off the host.
func (c *Client) NodeRemove(ctx context.Context, requestID string, req NodeAppRequest) error {
	return c.call(ctx, requestID, protocol.OperationNodeAppRemove, nodePayload(req), nil)
}

// NodeStart, NodeStop, and NodeRestart drive the process.
func (c *Client) NodeStart(ctx context.Context, requestID string, req NodeAppRequest) (NodeStatus, error) {
	var status NodeStatus
	err := c.call(ctx, requestID, protocol.OperationNodeAppStart, nodePayload(req), &status)
	return status, err
}

func (c *Client) NodeStop(ctx context.Context, requestID string, req NodeAppRequest) (NodeStatus, error) {
	var status NodeStatus
	err := c.call(ctx, requestID, protocol.OperationNodeAppStop, nodePayload(req), &status)
	return status, err
}

func (c *Client) NodeRestart(ctx context.Context, requestID string, req NodeAppRequest) (NodeStatus, error) {
	var status NodeStatus
	err := c.call(ctx, requestID, protocol.OperationNodeAppRestart, nodePayload(req), &status)
	return status, err
}

// NodeStatusOf reports what an application is doing.
func (c *Client) NodeStatusOf(ctx context.Context, requestID string, req NodeAppRequest) (NodeStatus, error) {
	var status NodeStatus
	err := c.call(ctx, requestID, protocol.OperationNodeAppStatus, nodePayload(req), &status)
	return status, err
}

// NodeAppLogs returns the tail of an application's output.
func (c *Client) NodeAppLogs(ctx context.Context, requestID string, req NodeAppRequest, lines int) (NodeLogs, error) {
	payload := nodePayload(req)
	payload["lines"] = lines

	var logs NodeLogs
	err := c.call(ctx, requestID, protocol.OperationNodeAppLogs, payload, &logs)
	return logs, err
}

// NodeInstallDependencies runs npm install for an application.
func (c *Client) NodeInstallDependencies(ctx context.Context, requestID string, req NodeAppRequest) error {
	return c.call(ctx, requestID, protocol.OperationNodeAppInstall, nodePayload(req), nil)
}

// UpdateWebsite rewrites a website's vhost.
//
// The payload is passed through rather than typed, because the Node.js service
// uses it for exactly one thing — pointing a site at an application or back at
// its files — and a typed struct here would duplicate the website package's
// own without either being the definition.
func (c *Client) UpdateWebsite(ctx context.Context, requestID string, payload map[string]any) error {
	return c.call(ctx, requestID, protocol.OperationWebsiteUpdate, payload, nil)
}

// NodeInstall adds a Node.js release line to the host.
//
// The package name comes from the offers the Agent itself reported, never from
// a user's text: the Agent keeps the only table of package names, and this is
// the value it handed back.
func (c *Client) NodeInstall(ctx context.Context, requestID, pkg string) (NodeVersion, error) {
	var version NodeVersion
	err := c.call(ctx, requestID, protocol.OperationNodeInstall,
		map[string]any{"package": pkg}, &version)
	return version, err
}

// NodeUninstall removes a Node.js release line.
func (c *Client) NodeUninstall(ctx context.Context, requestID, pkg string) error {
	return c.call(ctx, requestID, protocol.OperationNodeUninstall,
		map[string]any{"package": pkg}, nil)
}
