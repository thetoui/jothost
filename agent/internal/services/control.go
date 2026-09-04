package services

import (
	"context"
	"fmt"
	"strings"
)

// Control operations: starting, stopping, restarting, and setting what starts
// at boot.
//
// Every verb here is dispatched to whichever init system this host runs, and
// the two are deliberately indistinguishable from outside: a caller asks for a
// service to start, not for systemctl to be run.
//
// Deliberately absent: masking, editing arbitrary units, and anything that
// touches a unit the panel did not write. Every operation takes a name that has
// already been through ValidateName, so no argument can be read as an option.

// Start starts a unit.
func (p *Provider) Start(ctx context.Context, name string) error {
	return p.control(ctx, "start", name)
}

// Stop stops a unit.
func (p *Provider) Stop(ctx context.Context, name string) error {
	return p.control(ctx, "stop", name)
}

// Restart restarts a unit, starting it if it was not running.
func (p *Provider) Restart(ctx context.Context, name string) error {
	return p.control(ctx, "restart", name)
}

// Reload asks a unit to re-read its configuration without stopping.
//
// Distinct from Restart because for some daemons the difference is visible to
// somebody: reloading Postfix keeps deliveries that are in progress, while
// restarting it drops them. Not every daemon supports it, and the ones that do
// not report a failure — so a caller that needs the change applied either way
// falls back to a restart rather than assuming the reload worked.
func (p *Provider) Reload(ctx context.Context, name string) error {
	return p.control(ctx, "reload", name)
}

// Enable makes a unit start at boot.
//
// A Node.js application that does not come back after a reboot is one an
// operator discovers is down from a customer, so this is part of creating one
// rather than a separate choice.
func (p *Provider) Enable(ctx context.Context, name string) error {
	if p.manager == ManagerOpenRC {
		return p.openrcSetBoot(ctx, name, true)
	}
	return p.control(ctx, "enable", name)
}

// Disable stops a unit starting at boot.
func (p *Provider) Disable(ctx context.Context, name string) error {
	if p.manager == ManagerOpenRC {
		return p.openrcSetBoot(ctx, name, false)
	}
	return p.control(ctx, "disable", name)
}

// DaemonReload makes systemd read unit files that have changed on disk.
//
// Without it a unit the panel just wrote does not exist as far as systemd is
// concerned, and starting it fails with "unit not found" — which reads like
// the panel wrote nothing.
func (p *Provider) DaemonReload(ctx context.Context) error {
	if !p.Available() {
		return ErrUnavailable
	}
	if p.manager == ManagerOpenRC {
		// OpenRC reads its init scripts on every invocation, so there is
		// nothing to reload. Returning nil rather than an error keeps callers
		// from having to know which init system they are on.
		return nil
	}

	result, err := p.runner.Run(ctx, CommandName, "daemon-reload")
	if err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("daemon-reload: %s", firstLine(result.Stderr))
	}
	return nil
}

// control runs one systemctl verb against one unit.
//
// The verb is a constant from this file, never a caller's string: the whole
// point of the allowlist is that the set of things the Agent can do to a unit
// is fixed and reviewable.
func (p *Provider) control(ctx context.Context, verb, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if !p.Available() {
		return ErrUnavailable
	}
	if p.manager == ManagerOpenRC {
		return p.openrcControl(ctx, verb, name)
	}

	result, err := p.runner.Run(ctx, CommandName, verb, name)
	if err != nil {
		return fmt.Errorf("%s %s: %w", verb, name, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%s %s: %s", verb, name, firstLine(result.Stderr))
	}
	return nil
}

// firstLine trims a systemctl failure to the part worth showing.
func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "systemd refused the operation"
	}
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return value
}
