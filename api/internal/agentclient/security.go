package agentclient

import (
	"context"

	"github.com/jothost/panel/shared/protocol"
)

// The two host probes the Security Center needs and nothing else provides.
//
// Neither takes a parameter, and neither changes anything. That is what makes
// them safe to run on a schedule, and it is why there is no request type here
// to get wrong.

// SecuritySocket is one listening socket.
type SecuritySocket struct {
	Protocol string `json:"protocol"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	// Public reports whether the socket is bound to something other than
	// loopback — the field the whole scanner exists for.
	Public  bool   `json:"public"`
	UID     int    `json:"uid"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

// SecurityPortReport is what the host is listening on.
type SecurityPortReport struct {
	Sockets []SecuritySocket `json:"sockets"`
	Public  int              `json:"public"`
	// ProcessesResolved reports whether socket-to-process mapping worked. False
	// means the sockets are accurate and their owners unknown, which is not the
	// same as nothing owning them.
	ProcessesResolved bool   `json:"processes_resolved"`
	Reason            string `json:"reason,omitempty"`
}

// SecurityIssue is one thing found wrong with a file.
type SecurityIssue struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Mode   string `json:"mode"`
	Detail string `json:"detail,omitempty"`
}

// SecurityPermissionReport is what is writable or exposed on the host.
type SecurityPermissionReport struct {
	Issues []SecurityIssue `json:"issues"`
	// Counts are full totals; Issues is capped for display.
	Counts  map[string]int `json:"counts"`
	Scanned int            `json:"scanned"`
	// Complete reports whether the whole tree was walked. False makes the
	// counts lower bounds — "we found nothing else" and "we stopped looking"
	// are not the same answer.
	Complete bool     `json:"complete"`
	Roots    []string `json:"roots"`
	Reason   string   `json:"reason,omitempty"`
}

// SecurityPorts reports what the host is listening on.
func (c *Client) SecurityPorts(ctx context.Context, requestID string) (
	SecurityPortReport, error,
) {
	var report SecurityPortReport
	err := c.call(ctx, requestID, protocol.OperationSecurityPorts, map[string]any{}, &report)
	return report, err
}

// SecurityPermissions reports what is writable or exposed under the panel's
// directories.
func (c *Client) SecurityPermissions(ctx context.Context, requestID string) (
	SecurityPermissionReport, error,
) {
	var report SecurityPermissionReport
	err := c.call(ctx, requestID, protocol.OperationSecurityPermissions,
		map[string]any{}, &report)
	return report, err
}
