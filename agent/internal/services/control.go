package services

import (
	"context"
	"fmt"
	"strings"
)

// Control operations: starting, stopping, and restarting a unit.
//
// Phase 12 is the Service Manager, and this is not it. What is here is the
// smallest dependency Phase 9 needs: a Node.js application managed by systemd
// has to be startable, and a panel that can write a unit but not start it has
// not managed anything (CLAUDE.md section 21).
//
// Deliberately absent, and left to Phase 12: masking, editing arbitrary units,
// and anything that touches a unit the panel did not write. Every operation
// here takes a unit name that has already been through ValidateName, so no
// argument can be read by systemctl as an option.

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

// Enable makes a unit start at boot.
//
// A Node.js application that does not come back after a reboot is one an
// operator discovers is down from a customer, so this is part of creating one
// rather than a separate choice.
func (p *Provider) Enable(ctx context.Context, name string) error {
	return p.control(ctx, "enable", name)
}

// Disable stops a unit starting at boot.
func (p *Provider) Disable(ctx context.Context, name string) error {
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
