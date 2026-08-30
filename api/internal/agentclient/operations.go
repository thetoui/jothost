package agentclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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
