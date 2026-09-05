package tenancy

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/shared/validate"
)

// sliceTemplate renders a systemd slice for one subscription.
//
// A slice is the right shape for this: it is a cgroup with a name, it nests,
// and systemd owns the cgroup tree on every host that runs it. Writing to
// /sys/fs/cgroup directly would work exactly until systemd next reconciled the
// tree and silently undid it — two writers, one hierarchy, and the kernel
// takes no side.
//
// Every value substituted here is an integer this package formatted itself,
// and the slice name has been through validate.SliceName. That matters more
// than it looks: this file is read by pid 1 as root, and a value that could
// carry a newline could close one directive and start another.
var sliceTemplate = template.Must(template.New("slice").Parse(
	`# Managed by JotHost Panel. Manual edits are overwritten.
[Unit]
Description=JotHost subscription {{ .Description }}
Documentation=https://github.com/thetoui/jothost
Before=slices.target

[Slice]
{{- if .CPUQuota }}
# A percentage of one core. Above 100% means more than one core, which is a
# meaningful setting rather than a mistake on a multi-core host.
CPUQuota={{ .CPUQuota }}
{{- end }}
{{- if .MemoryMax }}
# A ceiling, not a reservation. Processes in this slice are killed when they
# exceed it, which is why the panel refuses a cap small enough to kill
# everything that starts.
MemoryMax={{ .MemoryMax }}
{{- end }}
{{- if .IOWeight }}
# A share of the disk under contention, not a cap. A subscription alone on an
# idle host still gets all of it.
IOWeight={{ .IOWeight }}
{{- end }}
`))

// sliceView is what the template renders.
type sliceView struct {
	Description string
	CPUQuota    string
	MemoryMax   string
	IOWeight    string
}

// RenderSlice produces the unit for a subscription's slice.
//
// Exported so it can be tested for what it is: a file systemd parses. The test
// asserts the directives, not that a function was called.
func RenderSlice(sliceName, description string, limits Limits) (string, error) {
	if err := validate.SliceName(sliceName); err != nil {
		return "", err
	}
	if err := validate.Isolation(limits.CPUPercent, limits.MemoryMB, limits.IOWeight); err != nil {
		return "", err
	}
	if err := validate.PlanName(description); err != nil {
		return "", err
	}

	view := sliceView{Description: description}
	if limits.CPUPercent != nil {
		view.CPUQuota = fmt.Sprintf("%d%%", *limits.CPUPercent)
	}
	if limits.MemoryMB != nil {
		view.MemoryMax = fmt.Sprintf("%dM", *limits.MemoryMB)
	}
	if limits.IOWeight != nil {
		view.IOWeight = fmt.Sprintf("%d", *limits.IOWeight)
	}

	var out bytes.Buffer
	if err := sliceTemplate.Execute(&out, view); err != nil {
		return "", fmt.Errorf("render the slice for %s: %w", sliceName, err)
	}
	return out.String(), nil
}

// ApplyIsolation writes a subscription's slice and asks systemd to read it.
//
// The result distinguishes what was written from what is enforced, and the
// distinction is the point of the operation. On a host with systemd the unit
// is installed and reloaded, and the state is 'applied'. On a host without
// systemd the unit is still written — a host that gains systemd later should
// find the limits already there — and the state is 'declared', which says in
// one word that nothing is capped.
//
// Placed is false in both cases and is reported rather than omitted. A slice
// with no processes in it limits nothing, and the panel says which services
// would have to join it; see docs/PHASE22.md.
func (p *Provider) ApplyIsolation(ctx context.Context, sliceName, description string,
	limits Limits,
) (IsolationResult, error) {
	if err := validate.SliceName(sliceName); err != nil {
		return IsolationResult{}, err
	}

	unitPath := filepath.Join(p.unitDir, sliceName)
	result := IsolationResult{Slice: sliceName, UnitPath: unitPath}

	// No limits means no slice. Leaving an empty one behind would be a cgroup
	// that exists to cap nothing, and the next operator to read the unit list
	// would have to work out that it does nothing.
	if limits.Empty() {
		if err := p.removeUnit(ctx, unitPath); err != nil {
			return result, err
		}
		result.State = StateNone
		result.Detail = "this subscription has no resource limits, so it has no slice"
		return result, nil
	}

	unit, err := RenderSlice(sliceName, description, limits)
	if err != nil {
		return result, err
	}

	if err := os.MkdirAll(p.unitDir, 0o755); err != nil {
		return result, fmt.Errorf("create %s: %w", p.unitDir, err)
	}
	// 0644 and root-owned: systemd reads it as pid 1, and every other unit in
	// this directory is world-readable. There is nothing secret in a CPU cap.
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return result, fmt.Errorf("write %s: %w", unitPath, err)
	}

	if !p.hasSystemd() {
		result.State = StateDeclared
		result.Detail = ErrNoSystemd.Error() + "; the limits are recorded and will apply " +
			"if this host gains systemd"
		return result, nil
	}

	if err := p.services.DaemonReload(ctx); err != nil {
		result.State = StateFailed
		result.Detail = fmt.Sprintf("systemd would not re-read its units: %v", err)
		return result, nil
	}

	result.State = StateApplied
	result.Detail = "the slice is installed and systemd has read it"
	return result, nil
}

// RemoveIsolation deletes a subscription's slice.
func (p *Provider) RemoveIsolation(ctx context.Context, sliceName string) (IsolationResult, error) {
	if err := validate.SliceName(sliceName); err != nil {
		return IsolationResult{}, err
	}
	unitPath := filepath.Join(p.unitDir, sliceName)
	if err := p.removeUnit(ctx, unitPath); err != nil {
		return IsolationResult{}, err
	}
	return IsolationResult{
		Slice:    sliceName,
		UnitPath: unitPath,
		State:    StateNone,
		Detail:   "the slice has been removed",
	}, nil
}

// removeUnit deletes a slice unit if it is there and tells systemd.
//
// A unit that was never written is not an error: this is called on every
// change of limits, including the first, and a panel that failed the first
// edit of a subscription because there was nothing to delete would be a panel
// nobody could configure.
func (p *Provider) removeUnit(ctx context.Context, unitPath string) error {
	if err := os.Remove(unitPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove %s: %w", unitPath, err)
	}
	if p.hasSystemd() {
		if err := p.services.DaemonReload(ctx); err != nil {
			// The file is gone, which is what was asked for. systemd will
			// notice on its next reload, so this is a warning rather than a
			// failure — but it is not swallowed.
			p.log.Warn("removed a subscription slice but systemd would not reload",
				"unit", unitPath, "error", err.Error())
		}
	}
	return nil
}

// IsolationStatus reports what this host can do about resource limits.
func (p *Provider) IsolationStatus() (bool, string) {
	if !p.hasSystemd() {
		return false, ErrNoSystemd.Error()
	}
	return true, ""
}

// hasSystemd reports whether this host is driven by systemd specifically.
//
// Specifically systemd, not "a service manager". Everything above writes a
// systemd unit, and a host running OpenRC would read none of it — which is the
// difference between 'applied' and 'declared'.
func (p *Provider) hasSystemd() bool {
	return p.services != nil && p.services.Manager() == services.ManagerSystemd
}
