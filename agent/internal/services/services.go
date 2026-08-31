// Package services inspects and controls the host's daemons.
//
// It speaks to two init systems, because a hosting panel meets two. systemd is
// what nearly every production host runs and what the specs name. OpenRC is
// what Alpine and Gentoo run — and on Alpine systemd is not merely absent but
// unavailable, so a panel that spoke only systemd could report what was running
// there and change none of it.
//
// Both are driven as allowlisted, parameterised commands (CLAUDE.md section 6):
// never through a shell, and never with a caller-supplied string interpolated
// into a command line. Which one a host uses is decided here, once, from what is
// installed; nothing above this package chooses, and callers cannot tell the
// two apart.
package services

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
)

// Errors returned by the provider.
var (
	// ErrUnavailable means no supported init system was found. It is reported
	// explicitly rather than as an internal error, so a container without
	// systemd degrades visibly instead of looking broken.
	ErrUnavailable = errors.New("no supported service manager is available")
	ErrInvalidName = errors.New("invalid service name")
	ErrNotFound    = errors.New("service not found")
)

// CommandName is the allowlist key for systemctl.
const CommandName = "systemctl"

// unitPattern bounds a caller-supplied unit name.
//
// systemctl arguments are passed as argv, so shell metacharacters are already
// inert. This exists for a different reason: without it, a caller could pass
// an option-looking value such as "--version" or a path, turning a status
// query into a different command entirely.
var unitPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.@-]{0,127}$`)

// maxUnits bounds a listing so a host with thousands of units cannot produce
// an unbounded response.
const maxUnits = 500

// Status is a normalised service state.
type Status struct {
	Name string `json:"name"`
	// Description is systemd's human-readable summary.
	Description string `json:"description"`
	// ActiveState is systemd's own value: active, inactive, failed, activating.
	ActiveState string `json:"active_state"`
	// SubState is the finer-grained state: running, dead, exited.
	SubState string `json:"sub_state"`
	// LoadState reports whether the unit file was found at all.
	LoadState string `json:"load_state"`
	// Enabled reports whether the unit starts at boot. It is nil when systemd
	// cannot answer, which is different from "not enabled".
	Enabled *bool `json:"enabled"`
	// Running is the convenience flag callers actually branch on.
	Running bool `json:"running"`
	// MainPID is 0 when the service is not running.
	MainPID int `json:"main_pid"`
}

// The init systems this package can drive.
const (
	ManagerSystemd = "systemd"
	ManagerOpenRC  = "openrc"
	// ManagerNone is a host with neither: state can still be reported from the
	// process table, and nothing can be started or stopped.
	ManagerNone = ""
)

// Provider inspects and controls services.
type Provider struct {
	runner *command.Runner
	// manager is the init system resolved at construction. It is fixed for the
	// life of the Agent: an init system is not something a host changes while
	// running, and re-deciding per request would mean two requests a second
	// apart could act on different ones.
	manager string
	// openrcInitDir and openrcRunlevelDir are where OpenRC keeps its init
	// scripts and its boot configuration. Fields so a test can point them at a
	// temporary tree.
	openrcInitDir     string
	openrcRunlevelDir string
}

// NewProvider builds a Provider over an allowlisted command runner.
//
// systemd is preferred where both are present, which happens on a host that has
// OpenRC installed as a leftover: systemd is the one actually supervising
// anything there, and driving the other would report success while changing
// nothing.
func NewProvider(runner *command.Runner) *Provider {
	provider := &Provider{runner: runner}

	switch {
	case runner != nil && runner.Available(CommandName):
		provider.manager = ManagerSystemd
	case provider.openrcAvailable():
		provider.manager = ManagerOpenRC
	}
	return provider
}

// Manager reports which init system this host is driven through, or "" for a
// host with neither.
func (p *Provider) Manager() string {
	if p == nil {
		return ManagerNone
	}
	return p.manager
}

// Available reports whether the service manager can be used.
func (p *Provider) Available() bool {
	return p != nil && p.manager != ManagerNone
}

// ValidateName checks a caller-supplied unit name.
func ValidateName(name string) error {
	if !unitPattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	// A name that starts with a dash would be parsed by systemctl as an
	// option, which the pattern above already prevents; this makes the intent
	// explicit for anyone relaxing the pattern later.
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: must not start with a dash", ErrInvalidName)
	}
	return nil
}

// showProperties are the systemd properties a Status is built from. Asking for
// an explicit set keeps the output stable and machine-parseable.
var showProperties = []string{
	"Id", "Description", "LoadState", "ActiveState", "SubState",
	"UnitFileState", "MainPID",
}

// Status reports one service's state.
func (p *Provider) Status(ctx context.Context, name string) (Status, error) {
	if err := ValidateName(name); err != nil {
		return Status{}, err
	}
	if !p.Available() {
		return Status{}, ErrUnavailable
	}
	if p.manager == ManagerOpenRC {
		return p.openrcStatus(ctx, name)
	}

	// Arguments are separate argv entries: the unit name can never be read as
	// part of the option string.
	result, err := p.runner.Run(ctx, CommandName,
		"show", name,
		"--no-pager",
		"--property="+strings.Join(showProperties, ","))
	if err != nil {
		return Status{}, fmt.Errorf("query service: %w", err)
	}

	properties := parseProperties(result.Stdout)
	if properties["Id"] == "" && properties["LoadState"] == "" {
		return Status{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	// systemd answers for a unit it has never heard of with LoadState=not-found
	// rather than a non-zero exit, so absence is detected here.
	if properties["LoadState"] == "not-found" {
		return Status{}, fmt.Errorf("%w: %s", ErrNotFound, name)
	}

	return statusFromProperties(name, properties), nil
}

// List reports the state of the named services.
//
// The caller supplies the names: Phase 2 has no opinion about which services
// matter, and enumerating every unit on the host would be a large, mostly
// irrelevant response. The dashboard's service list arrives in Phase 3.
func (p *Provider) List(ctx context.Context, names []string) ([]Status, error) {
	if !p.Available() {
		return nil, ErrUnavailable
	}
	if len(names) > maxUnits {
		return nil, fmt.Errorf("at most %d services may be queried at once", maxUnits)
	}

	seen := make(map[string]struct{}, len(names))
	statuses := make([]Status, 0, len(names))

	for _, name := range names {
		if err := ValidateName(name); err != nil {
			return nil, err
		}
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}

		status, err := p.Status(ctx, name)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// An absent unit is reported as absent rather than failing the
				// whole listing: a panel asking about php8.4-fpm on a host
				// without it wants that answer, not an error.
				statuses = append(statuses, Status{
					Name:        name,
					LoadState:   "not-found",
					ActiveState: "inactive",
				})
				continue
			}
			return nil, err
		}
		statuses = append(statuses, status)
	}

	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Name < statuses[j].Name })
	return statuses, nil
}

// parseProperties reads systemctl's KEY=VALUE output.
func parseProperties(output string) map[string]string {
	properties := make(map[string]string, len(showProperties))

	for _, line := range strings.Split(output, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key == "" {
			continue
		}
		properties[key] = value
	}
	return properties
}

// statusFromProperties converts systemd properties into a Status.
func statusFromProperties(name string, properties map[string]string) Status {
	status := Status{
		Name:        name,
		Description: properties["Description"],
		LoadState:   properties["LoadState"],
		ActiveState: properties["ActiveState"],
		SubState:    properties["SubState"],
	}
	if id := properties["Id"]; id != "" {
		status.Name = id
	}

	status.Running = status.ActiveState == "active" && status.SubState == "running"

	// UnitFileState is empty for a transient unit, which is not the same as
	// "disabled"; a nil Enabled says "unknown" rather than guessing.
	switch properties["UnitFileState"] {
	case "enabled", "enabled-runtime", "static", "indirect":
		enabled := true
		status.Enabled = &enabled
	case "disabled", "masked", "masked-runtime":
		enabled := false
		status.Enabled = &enabled
	}

	if pid := properties["MainPID"]; pid != "" && pid != "0" {
		var parsed int
		if _, err := fmt.Sscanf(pid, "%d", &parsed); err == nil {
			status.MainPID = parsed
		}
	}

	return status
}
