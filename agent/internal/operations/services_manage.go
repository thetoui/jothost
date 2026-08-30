package operations

import (
	"context"
	"fmt"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/shared/protocol"
)

// The service manager's request boundary.
//
// A request names a *key* from the Agent's catalogue, never a unit. The unit it
// becomes is chosen here, from a table in this repository, so no value a client
// sends can ever reach systemctl as the thing being acted on. That is the same
// rule the command allowlist follows, applied one level up.

// serviceDetectPayload has no fields: what the host has is not something the
// caller gets to influence. It exists so a payload carrying anything is
// refused rather than ignored.
type serviceDetectPayload struct{}

// handleServiceDetect reports the services this host actually has.
func (r *Registry) handleServiceDetect(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload serviceDetectPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	detected := r.deps.Services.Detect(ctx, r.phpFPMDefinitions(ctx), r.deps.Collector)

	entries := make([]map[string]any, 0, len(detected))
	for _, entry := range detected {
		converted, err := structToMap(entry)
		if err != nil {
			return nil, err
		}
		entries = append(entries, converted)
	}

	return map[string]any{
		"services": entries,
		"count":    len(entries),
		// Whether this host can start and stop anything at all, which is a
		// property of the host rather than of any one service.
		"controllable": r.deps.Services.Available(),
	}, nil
}

// serviceActionPayload names what to do and to which service.
type serviceActionPayload struct {
	// Service is a catalogue key, not a unit name.
	Service string `json:"service"`
	// Action is one of the five verbs below. It is matched against constants,
	// never passed through.
	Action string `json:"action"`
}

// handleServiceAction starts, stops, restarts, enables, or disables a service.
func (r *Registry) handleServiceAction(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload serviceActionPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Service == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "service is required", nil)
	}

	definition, unit, err := r.deps.Services.UnitFor(ctx, payload.Service, r.phpFPMDefinitions(ctx))
	if err != nil {
		return nil, serviceError(err)
	}

	// Whether this verb may be applied is the service's own rule, checked
	// where the flag it reads lives. A protected service can be restarted but
	// not taken away: stopping sshd on a remote host locks the operator out of
	// the machine they are administering.
	if err := definition.Allows(payload.Action); err != nil {
		return nil, serviceError(err)
	}

	if err := r.runServiceAction(ctx, payload.Action, unit); err != nil {
		return nil, serviceError(err)
	}

	// The state afterwards, read rather than assumed: an action that returned
	// zero and left the service down is exactly what an operator needs to see,
	// and "we ran the command" is not an answer about the service.
	status, err := r.deps.Services.Status(ctx, unit)
	if err != nil {
		return nil, serviceError(err)
	}

	return map[string]any{
		"service": definition.Key,
		"unit":    unit,
		"action":  payload.Action,
		"running": status.Running,
		"enabled": status.Enabled,
		"state":   status.ActiveState,
	}, nil
}

// runServiceAction dispatches to the provider.
//
// The verb reaching the provider is one of this file's constants, never the
// caller's string.
func (r *Registry) runServiceAction(ctx context.Context, action, unit string) error {
	switch action {
	case services.ActionStart:
		return r.deps.Services.Start(ctx, unit)
	case services.ActionStop:
		return r.deps.Services.Stop(ctx, unit)
	case services.ActionRestart:
		return r.deps.Services.Restart(ctx, unit)
	case services.ActionEnable:
		return r.deps.Services.Enable(ctx, unit)
	case services.ActionDisable:
		return r.deps.Services.Disable(ctx, unit)
	default:
		// Unreachable: the caller validated the action against the same
		// constants. Kept because a new verb added above without a case here
		// would otherwise silently do nothing and report success.
		return fmt.Errorf("unhandled service action %q", action)
	}
}

// phpFPMDefinitions contributes one catalogue entry per installed PHP version.
//
// The static catalogue cannot hold these: which versions a host has is
// discovered at runtime, and a table naming php-fpm8.4 on a host that runs 8.2
// would be wrong everywhere it mattered.
func (r *Registry) phpFPMDefinitions(ctx context.Context) []services.Definition {
	if r.deps.PHP == nil || !r.deps.PHP.Available() {
		return nil
	}

	versions := r.deps.PHP.Detect(ctx)
	names := make([]string, 0, len(versions))
	for _, version := range versions {
		names = append(names, version.Version)
	}
	return services.PHPFPMDefinitions(names)
}
