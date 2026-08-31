// Package fail2ban serves the host's intrusion prevention to the panel.
//
// The panel holds no state of its own: the jails are files on the host and the
// bans are firewall rules, and the Agent is the only thing that may read or
// change either. This package decides who may ask and records that they did.
//
// It records more than most. Every action here changes who can reach the
// machine — a ban, an unban, a threshold, an address that is never banned — and
// those are the entries somebody reads after an incident.
package fail2ban

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit actions.
const (
	ActionInstall      = "fail2ban.install"
	ActionConfigure    = "fail2ban.configure"
	ActionIgnore       = "fail2ban.ignore"
	ActionBan          = "fail2ban.ban"
	ActionUnban        = "fail2ban.unban"
	ResourceTypeServer = "server"
)

// Errors returned by the service.
var (
	// ErrUnavailable means fail2ban is not installed.
	ErrUnavailable = errors.New("fail2ban is not installed on this host")
	// ErrNothingToDo means a change asked for nothing.
	ErrNothingToDo = errors.New("no setting was given to change")
)

// Service manages the host's intrusion prevention.
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
	return &Service{agent: opts.Agent, audit: opts.Audit, log: log, serverID: opts.ServerID}
}

// Actor identifies who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Status reports the jails, the bans and the policy.
func (s *Service) Status(ctx context.Context, requestID string) (agentclient.Fail2BanStatus, error) {
	return s.agent.Fail2BanStatusOf(ctx, requestID)
}

// Install puts fail2ban on the host.
func (s *Service) Install(ctx context.Context, requestID string, actor Actor) (agentclient.Fail2BanStatus, error) {
	status, err := s.agent.Fail2BanInstall(ctx, requestID)
	if err != nil {
		s.record(ctx, actor, ActionInstall, audit.StatusFailure, map[string]any{
			"reason": agentclient.Message(err),
		})
		return agentclient.Fail2BanStatus{}, err
	}

	s.record(ctx, actor, ActionInstall, audit.StatusSuccess, map[string]any{
		"version": status.Version,
	})
	return status, nil
}

// Configure changes one jail.
func (s *Service) Configure(ctx context.Context, requestID string,
	change agentclient.Fail2BanChange, actor Actor,
) (agentclient.Fail2BanApplyResult, error) {
	metadata := map[string]any{"jail": change.Jail}
	if change.Enabled != nil {
		metadata["enabled"] = *change.Enabled
	}
	if change.MaxRetry != nil {
		metadata["max_retry"] = *change.MaxRetry
	}
	if change.FindTime != nil {
		metadata["find_time"] = *change.FindTime
	}
	if change.BanTime != nil {
		metadata["ban_time"] = *change.BanTime
	}
	if len(metadata) == 1 {
		return agentclient.Fail2BanApplyResult{}, ErrNothingToDo
	}

	result, err := s.agent.Fail2BanConfigure(ctx, requestID, change)
	if err != nil {
		metadata["reason"] = agentclient.Message(err)
		s.record(ctx, actor, ActionConfigure, audit.StatusFailure, metadata)
		return agentclient.Fail2BanApplyResult{}, err
	}

	s.record(ctx, actor, ActionConfigure, audit.StatusSuccess, metadata)
	return result, nil
}

// SetIgnored changes the addresses no jail may ban.
func (s *Service) SetIgnored(ctx context.Context, requestID string, addresses []string,
	actor Actor,
) ([]string, error) {
	ignored, err := s.agent.Fail2BanSetIgnored(ctx, requestID, addresses)
	if err != nil {
		s.record(ctx, actor, ActionIgnore, audit.StatusFailure, map[string]any{
			"reason": agentclient.Message(err),
		})
		return nil, err
	}

	// The whole list, because "who added that address to the ignore list" is
	// the question this record exists to answer — an address that is never
	// banned is a hole somebody opened deliberately.
	s.record(ctx, actor, ActionIgnore, audit.StatusSuccess, map[string]any{
		"ignored": ignored,
	})
	return ignored, nil
}

// Banned lists what the host is currently blocking.
func (s *Service) Banned(ctx context.Context, requestID string) (agentclient.Fail2BanBannedList, error) {
	return s.agent.Fail2BanBannedAddresses(ctx, requestID)
}

// Unban releases an address.
func (s *Service) Unban(ctx context.Context, requestID, jail, address string, actor Actor) error {
	if err := s.agent.Fail2BanUnban(ctx, requestID, jail, address); err != nil {
		s.record(ctx, actor, ActionUnban, audit.StatusFailure, map[string]any{
			"jail": jail, "address": address, "reason": agentclient.Message(err),
		})
		return err
	}

	s.record(ctx, actor, ActionUnban, audit.StatusSuccess, map[string]any{
		"jail": jail, "address": address,
	})
	return nil
}

// Ban blocks an address by hand.
func (s *Service) Ban(ctx context.Context, requestID, jail, address string, actor Actor) error {
	if err := s.agent.Fail2BanBan(ctx, requestID, jail, address); err != nil {
		s.record(ctx, actor, ActionBan, audit.StatusFailure, map[string]any{
			"jail": jail, "address": address, "reason": agentclient.Message(err),
		})
		return err
	}

	s.record(ctx, actor, ActionBan, audit.StatusSuccess, map[string]any{
		"jail": jail, "address": address,
	})
	return nil
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
