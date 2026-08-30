// Package firewall is the panel's boundary onto the host's packet filter.
//
// It holds no state: the rules are on the host, and the provisional change and
// its rollback timer live in the Agent, which is the only place they can
// survive the API being unreachable. That matters more here than anywhere else
// in the panel — the failure this feature has to survive is precisely "the API
// can no longer be reached", because that is what a bad firewall rule causes.
package firewall

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit action names.
//
// Every firewall change is audited, and so is every refusal: "who tried to
// close SSH" is a question worth being able to answer (CLAUDE.md section 15
// lists firewall.change among the actions that must produce an event).
const (
	ActionChange       = "firewall.change"
	ActionConfirm      = "firewall.confirm"
	ActionRollback     = "firewall.rollback"
	ResourceTypeServer = "server"
)

// ErrUnavailable means the host has no firewall the panel can manage.
var ErrUnavailable = errors.New("this host has no firewall")

// Service reads and changes the host's firewall.
type Service struct {
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Log      *slog.Logger
	ServerID string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		agent:    opts.Agent,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Status reports the firewall as it stands, including any change waiting to be
// confirmed.
func (s *Service) Status(ctx context.Context, requestID string) (agentclient.FirewallStatus, error) {
	return s.agent.FirewallStatus(ctx, requestID)
}

// Change applies a change provisionally.
//
// The response carries a deadline. Nothing else may be changed until this one
// is confirmed or its window closes, and if it closes the Agent puts the rules
// back — including when the reason nobody confirmed is that the change cut the
// panel off.
func (s *Service) Change(ctx context.Context, requestID string,
	change agentclient.FirewallChange, actor Actor,
) (agentclient.FirewallPending, error) {
	pending, err := s.agent.FirewallChange(ctx, requestID, change)
	if err != nil {
		// A refused change is recorded too. The refusals here are the
		// interesting ones: they are attempts to close the port the host is
		// administered through.
		s.record(ctx, actor, ActionChange, audit.StatusFailure, map[string]any{
			"kind":   change.Kind,
			"rule":   change.Rule,
			"reason": agentclient.Message(err),
		})
		return agentclient.FirewallPending{}, err
	}

	s.record(ctx, actor, ActionChange, audit.StatusSuccess, map[string]any{
		"kind":      change.Kind,
		"rule":      change.Rule,
		"change_id": pending.ID,
		"deadline":  pending.Deadline,
	})
	return pending, nil
}

// Confirm commits a provisional change.
func (s *Service) Confirm(ctx context.Context, requestID, changeID string,
	actor Actor,
) (agentclient.FirewallPending, error) {
	confirmed, err := s.agent.FirewallConfirm(ctx, requestID, changeID)
	if err != nil {
		return agentclient.FirewallPending{}, err
	}

	s.record(ctx, actor, ActionConfirm, audit.StatusSuccess, map[string]any{
		"change_id": changeID,
		"kind":      confirmed.Kind,
	})
	return confirmed, nil
}

// Rollback undoes a provisional change without waiting for its window.
func (s *Service) Rollback(ctx context.Context, requestID, changeID string,
	actor Actor,
) (agentclient.FirewallPending, error) {
	rolled, err := s.agent.FirewallRollback(ctx, requestID, changeID)
	if err != nil {
		return agentclient.FirewallPending{}, err
	}

	s.record(ctx, actor, ActionRollback, audit.StatusSuccess, map[string]any{
		"change_id": changeID,
		"kind":      rolled.Kind,
	})
	return rolled, nil
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action, status string,
	metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: ResourceTypeServer,
		ResourceID:   s.serverID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       status,
		Metadata:     metadata,
	})
}
