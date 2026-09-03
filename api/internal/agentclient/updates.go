package agentclient

import (
	"context"
	"time"

	"github.com/jothost/panel/shared/protocol"
)

// The system update operations.
//
// A check refreshes the host's package index, so it reaches the network and is
// not free. The panel caches what comes back rather than asking on every page
// load — and the field that matters most in the answer is Checked, not the
// package list: an empty list from a check that failed is "not known", and
// showing it as "up to date" is the failure this phase is built around.

// UpdatePackage is one update the host has waiting.
type UpdatePackage struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	// Security is only meaningful when UpdateReport.SecurityKnown is true.
	Security bool   `json:"security"`
	Origin   string `json:"origin"`
}

// UpdateHeld is a package with something newer available that the host will not
// upgrade — a pin somebody set, reported separately so it is not mistaken for
// outstanding work.
type UpdateHeld struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	Reason    string `json:"reason"`
}

// UpdateReport is what the host says about its updates.
type UpdateReport struct {
	Available bool   `json:"available"`
	Manager   string `json:"manager"`
	// Checked reports whether the package index was actually refreshed. When it
	// is false the lists below are not "nothing to do" — they are "not known".
	Checked bool   `json:"checked"`
	Reason  string `json:"reason"`
	// SecurityKnown reports whether this host can distinguish security updates.
	SecurityKnown bool `json:"security_known"`

	Packages []UpdatePackage `json:"packages"`
	Held     []UpdateHeld    `json:"held"`

	Unavailable int `json:"unavailable_repositories"`
	Stale       int `json:"stale_repositories"`

	CheckedAt      time.Time `json:"checked_at"`
	RebootRequired bool      `json:"reboot_required"`
}

// UpdateChange is one package an apply moved.
type UpdateChange struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// UpdateResult reports what an apply or a revert did.
type UpdateResult struct {
	// Changed is what actually moved, read back from the host. It is routinely
	// longer than what was requested, because a package manager resolves
	// dependencies.
	Changed        []UpdateChange `json:"changed"`
	Requested      []string       `json:"requested"`
	Output         string         `json:"output"`
	RebootRequired bool           `json:"reboot_required"`
}

// UpdatesCheck asks the host what it has waiting.
func (c *Client) UpdatesCheck(ctx context.Context, requestID string) (UpdateReport, error) {
	var report UpdateReport
	err := c.call(ctx, requestID, protocol.OperationUpdatesCheck, map[string]any{}, &report)
	return report, err
}

// UpdatesApply installs updates. An empty list means everything.
func (c *Client) UpdatesApply(ctx context.Context, requestID string,
	packages []string,
) (UpdateResult, error) {
	var result UpdateResult
	err := c.call(ctx, requestID, protocol.OperationUpdatesApply,
		map[string]any{"packages": packages}, &result)
	return result, err
}

// UpdatesRevert puts one package back to an earlier version.
//
// It fails when the host can no longer install that version, which is the
// ordinary case — see the Agent's updates package for why this is not a
// rollback.
func (c *Client) UpdatesRevert(ctx context.Context, requestID, name, version string) (
	UpdateResult, error,
) {
	var result UpdateResult
	err := c.call(ctx, requestID, protocol.OperationUpdatesRevert,
		map[string]any{"package": name, "version": version}, &result)
	return result, err
}
