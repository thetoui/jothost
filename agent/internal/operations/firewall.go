package operations

import (
	"context"
	"errors"
	"time"

	"github.com/jothost/panel/agent/internal/firewall"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The firewall's request boundary.
//
// Every change goes through one operation, and that operation applies it
// provisionally: the reply carries a change id and a deadline, and the caller
// has until then to confirm. Nothing here can make a permanent change in one
// step, which is the point — a request that locks the host out cannot be
// followed by the request that would have fixed it.

// handleFirewallStatus reports the firewall as it stands.
func (r *Registry) handleFirewallStatus(ctx context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	if r.deps.Firewall == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Firewall management is not available on this host", nil)
	}

	status, err := r.deps.Firewall.Status(ctx)
	if err != nil {
		return nil, firewallError(err)
	}

	result, err := structToMap(status)
	if err != nil {
		return nil, err
	}
	result["guarded_ports"] = r.deps.Firewall.GuardedPorts()

	// A change waiting to be confirmed is the most important thing on this
	// response: it has a deadline, and something has to confirm it.
	if pending := r.deps.Firewall.PendingChange(); pending != nil {
		converted, err := structToMap(*pending)
		if err != nil {
			return nil, err
		}
		result["pending"] = converted
	}
	return result, nil
}

// firewallChangePayload is one change to make.
type firewallChangePayload struct {
	Kind string `json:"kind"`
	// Rule is required for the rule kinds. Its fields are validated in the
	// firewall package before any of them reaches a command line.
	Rule firewall.Rule `json:"rule"`
	// Policy and Direction are for a default-policy change.
	Policy    string `json:"policy"`
	Direction string `json:"direction"`
	// WindowSeconds is how long the change stays provisional. Zero takes the
	// default; anything past the maximum is clamped rather than refused,
	// because a caller asking for an hour wants a long window, not an error.
	WindowSeconds int `json:"window_seconds"`
}

// handleFirewallChange applies a change provisionally.
func (r *Registry) handleFirewallChange(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload firewallChangePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.Firewall == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Firewall management is not available on this host", nil)
	}

	change := firewall.Change{
		Kind:      payload.Kind,
		Rule:      payload.Rule,
		Policy:    payload.Policy,
		Direction: payload.Direction,
	}

	pending, err := r.deps.Firewall.Apply(ctx, change,
		time.Duration(payload.WindowSeconds)*time.Second, req.RequestID)
	if err != nil {
		return nil, firewallError(err)
	}

	return structToMap(pending)
}

// firewallConfirmPayload names the change to commit or undo.
type firewallConfirmPayload struct {
	ChangeID string `json:"change_id"`
}

// handleFirewallConfirm commits a provisional change.
//
// The request that reaches here has crossed the network the change governs,
// which is the only proof available that the change did not cut the host off.
func (r *Registry) handleFirewallConfirm(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload firewallConfirmPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.Firewall == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Firewall management is not available on this host", nil)
	}

	confirmed, err := r.deps.Firewall.Confirm(payload.ChangeID)
	if err != nil {
		return nil, firewallError(err)
	}
	return structToMap(confirmed)
}

// handleFirewallRollback undoes a provisional change without waiting.
func (r *Registry) handleFirewallRollback(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload firewallConfirmPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.Firewall == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"Firewall management is not available on this host", nil)
	}

	rolled, err := r.deps.Firewall.Rollback(ctx, payload.ChangeID)
	if err != nil {
		return nil, firewallError(err)
	}
	return structToMap(rolled)
}

// firewallError maps a firewall failure to a structured response.
func firewallError(err error) error {
	switch {
	case errors.Is(err, firewall.ErrUnavailable):
		return Fail(protocol.CodeUnsupported,
			"No firewall is available on this host", err)
	case errors.Is(err, firewall.ErrWouldLockOut):
		// The Agent's own words: they name the port and say what to do about
		// it, which a code cannot.
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, firewall.ErrChangeInFlight):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, firewall.ErrNoPendingChange):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, firewall.ErrRefused):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, validate.ErrInvalidFirewallRule),
		errors.Is(err, validate.ErrInvalidPortSpec),
		errors.Is(err, validate.ErrInvalidSource):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return err
	}
}
