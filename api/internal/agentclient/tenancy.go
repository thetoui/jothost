package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The tenancy operations.
//
// Two things cross this boundary and no third thing: paths the panel wrote,
// and numbers it stored. No unit name of a caller's choosing, no directive, no
// cgroup path — the slice is rendered from a template on the far side, from a
// name the panel derives from a subscription's id.

// TenantSiteUsage is what one website was measured to be using.
//
// The measurements are pointers because nil means *not measured*, which is not
// zero. A site whose disk could not be read is not a site using no disk.
type TenantSiteUsage struct {
	DocumentRoot   string `json:"document_root"`
	DiskBytes      *int64 `json:"disk_bytes"`
	BandwidthBytes *int64 `json:"bandwidth_bytes"`
	LogTruncated   bool   `json:"log_truncated"`
	Error          string `json:"error"`
}

// TenantUsage is a measurement across a subscription's websites.
type TenantUsage struct {
	Sites          []TenantSiteUsage `json:"sites"`
	DiskBytes      *int64            `json:"disk_bytes"`
	BandwidthBytes *int64            `json:"bandwidth_bytes"`
	Partial        bool              `json:"partial"`
	Warnings       []string          `json:"warnings"`
}

// TenantStatus is what a host can do about resource limits.
type TenantStatus struct {
	IsolationAvailable bool   `json:"isolation_available"`
	IsolationDetail    string `json:"isolation_detail"`
	// Placement says whether anything actually runs inside a subscription's
	// slice. It is reported separately from availability because a slice with
	// no processes in it is a limit on nothing, and the panel must not imply
	// one fact from the other.
	Placement bool `json:"placement"`
}

// TenantIsolation is what happened when limits were applied.
type TenantIsolation struct {
	Slice    string `json:"slice"`
	State    string `json:"state"`
	Detail   string `json:"detail"`
	UnitPath string `json:"unit_path"`
	Placed   bool   `json:"placed"`
}

// TenantStatusOf asks what this host can enforce.
func (c *Client) TenantStatusOf(ctx context.Context, requestID string) (TenantStatus, error) {
	var result TenantStatus
	err := c.call(ctx, requestID, protocol.OperationTenantStatus, map[string]any{}, &result)
	return result, err
}

// TenantUsageOf measures a set of websites.
func (c *Client) TenantUsageOf(ctx context.Context, requestID string,
	documentRoots []string,
) (TenantUsage, error) {
	if documentRoots == nil {
		documentRoots = []string{}
	}
	var result TenantUsage
	err := c.call(ctx, requestID, protocol.OperationTenantUsage, map[string]any{
		"document_roots": documentRoots,
	}, &result)
	return result, err
}

// TenantIsolationApply writes a subscription's resource limits.
func (c *Client) TenantIsolationApply(ctx context.Context, requestID, slice, description string,
	cpuPercent, memoryMB, ioWeight *int,
) (TenantIsolation, error) {
	var result TenantIsolation
	err := c.call(ctx, requestID, protocol.OperationTenantIsolationApply, map[string]any{
		"slice":       slice,
		"description": description,
		"cpu_percent": cpuPercent,
		"memory_mb":   memoryMB,
		"io_weight":   ioWeight,
	}, &result)
	return result, err
}

// TenantIsolationRemove deletes a subscription's slice.
func (c *Client) TenantIsolationRemove(ctx context.Context, requestID, slice string) (TenantIsolation, error) {
	var result TenantIsolation
	err := c.call(ctx, requestID, protocol.OperationTenantIsolationRemove, map[string]any{
		"slice": slice,
	}, &result)
	return result, err
}
