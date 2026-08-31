// Package ssh serves the host's SSH server settings to the panel.
//
// The panel holds no state of its own here: the configuration is files on the
// host, and the Agent is the only thing that may read or change them. This
// package is the boundary that decides who may ask, records that they did, and
// turns the Agent's answers into the panel's shapes.
//
// It records more than most. Every change here is a change to how the machine
// is administered — who may log in, with what, on which port — and those are
// the entries somebody reads after an incident. CLAUDE.md section 15 names
// ssh.change specifically.
package ssh

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit actions.
const (
	ActionChange       = "ssh.change"
	ActionKeyAdd       = "ssh.key.add"
	ActionKeyRemove    = "ssh.key.remove"
	ResourceTypeServer = "server"
)

// Errors returned by the service.
var (
	// ErrUnavailable means this host has no SSH server.
	ErrUnavailable = errors.New("this host has no SSH server")
	// ErrNothingToDo means the request asked for no change.
	ErrNothingToDo = errors.New("no setting was given to change")
	// ErrInvalidValue means a setting is not one the panel accepts.
	ErrInvalidValue = errors.New("invalid SSH setting")
)

// Service reads and changes the host's SSH configuration.
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

// Status reports the configuration, the accounts and the recommendations.
func (s *Service) Status(ctx context.Context, requestID string) (agentclient.SSHStatus, error) {
	return s.agent.SSHStatusOf(ctx, requestID)
}

// Configure changes the server's settings.
func (s *Service) Configure(ctx context.Context, requestID string,
	change agentclient.SSHChange, actor Actor,
) (agentclient.SSHApplyResult, error) {
	metadata := changeMetadata(change)
	if len(metadata) == 0 {
		return agentclient.SSHApplyResult{}, ErrNothingToDo
	}

	result, err := s.agent.SSHConfigure(ctx, requestID, change)
	if err != nil {
		// A refused change is recorded too. "Who tried to turn off password
		// authentication" is a question worth being able to answer, and a trail
		// that holds only what succeeded cannot.
		metadata["reason"] = agentclient.Message(err)
		s.record(ctx, actor, ActionChange, audit.StatusFailure, metadata)
		return agentclient.SSHApplyResult{}, err
	}

	metadata["reloaded"] = result.Reloaded
	s.record(ctx, actor, ActionChange, audit.StatusSuccess, metadata)
	return result, nil
}

// Keys lists one account's authorised keys.
func (s *Service) Keys(ctx context.Context, requestID, account string) (agentclient.SSHKeyList, error) {
	return s.agent.SSHKeys(ctx, requestID, account)
}

// AddKey authorises a key.
func (s *Service) AddKey(ctx context.Context, requestID, account, key string,
	actor Actor,
) (agentclient.SSHKey, error) {
	added, err := s.agent.SSHAddKey(ctx, requestID, account, key)
	if err != nil {
		s.record(ctx, actor, ActionKeyAdd, audit.StatusFailure, map[string]any{
			"account": account, "reason": agentclient.Message(err),
		})
		return agentclient.SSHKey{}, err
	}

	// The fingerprint and the comment, never the key itself. A public key is
	// not a secret, but an audit trail is read by people looking for what
	// changed, and a fingerprint identifies a key more usefully than 400
	// characters of base64.
	s.record(ctx, actor, ActionKeyAdd, audit.StatusSuccess, map[string]any{
		"account": account, "fingerprint": added.Fingerprint,
		"type": added.Type, "comment": added.Comment,
	})
	return added, nil
}

// RemoveKey withdraws a key.
func (s *Service) RemoveKey(ctx context.Context, requestID, account, fingerprint string,
	actor Actor,
) (agentclient.SSHKey, error) {
	removed, err := s.agent.SSHRemoveKey(ctx, requestID, account, fingerprint)
	if err != nil {
		s.record(ctx, actor, ActionKeyRemove, audit.StatusFailure, map[string]any{
			"account": account, "fingerprint": fingerprint,
			"reason": agentclient.Message(err),
		})
		return agentclient.SSHKey{}, err
	}

	s.record(ctx, actor, ActionKeyRemove, audit.StatusSuccess, map[string]any{
		"account": account, "fingerprint": removed.Fingerprint,
		"comment": removed.Comment,
	})
	return removed, nil
}

// changeMetadata renders a change for the audit trail.
//
// Only what was asked for: a record listing every setting would make it
// impossible to see which one the request actually changed.
func changeMetadata(change agentclient.SSHChange) map[string]any {
	metadata := map[string]any{}
	if change.Port != nil {
		metadata["port"] = *change.Port
	}
	if change.RootLogin != nil {
		metadata["root_login"] = *change.RootLogin
	}
	if change.PasswordAuthentication != nil {
		metadata["password_authentication"] = *change.PasswordAuthentication
	}
	if change.PubkeyAuthentication != nil {
		metadata["pubkey_authentication"] = *change.PubkeyAuthentication
	}
	if change.PermitEmptyPasswords != nil {
		metadata["permit_empty_passwords"] = *change.PermitEmptyPasswords
	}
	if change.X11Forwarding != nil {
		metadata["x11_forwarding"] = *change.X11Forwarding
	}
	if change.MaxAuthTries != nil {
		metadata["max_auth_tries"] = *change.MaxAuthTries
	}
	return metadata
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
