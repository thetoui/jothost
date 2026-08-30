// Package services lists and controls the daemons on the managed host.
//
// The panel holds no state of its own here: a service's state is on the host,
// and the Agent is the only thing that may read or change it (CLAUDE.md
// section 7). This package is the boundary that decides who may ask, records
// that they did, and turns the Agent's answers into the panel's shapes.
package services

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
)

// Audit action names. One per verb rather than a single "service.action" with
// the verb in the metadata: an audit trail is read by searching it, and
// "who stopped something" is the question that gets asked.
const (
	ActionStart        = "service.start"
	ActionStop         = "service.stop"
	ActionRestart      = "service.restart"
	ActionEnable       = "service.enable"
	ActionDisable      = "service.disable"
	ResourceTypeServer = "server"
)

// Errors returned by the service.
var (
	// ErrUnknownAction means the verb is not one of the five.
	ErrUnknownAction = errors.New("unknown service action")
	// ErrUnavailable means the host has no service manager, so nothing can be
	// started or stopped through the panel.
	ErrUnavailable = errors.New("this host has no service manager")
)

// Service reads and changes the host's services.
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

// The verbs a service may be given. They match the Agent's own constants; a
// verb that is not one of these never reaches it.
const (
	VerbStart   = "start"
	VerbStop    = "stop"
	VerbRestart = "restart"
	VerbEnable  = "enable"
	VerbDisable = "disable"
)

// auditActions maps a verb to the action name recorded for it.
var auditActions = map[string]string{
	VerbStart:   ActionStart,
	VerbStop:    ActionStop,
	VerbRestart: ActionRestart,
	VerbEnable:  ActionEnable,
	VerbDisable: ActionDisable,
}

// List reports the services this host has.
func (s *Service) List(ctx context.Context, requestID string) (agentclient.ServiceDetectResult, error) {
	return s.agent.ServiceDetect(ctx, requestID)
}

// Act runs one verb against one service.
//
// The result is the service's state *after* the action, read from the host
// rather than assumed: a start that returns zero and leaves the service down
// is exactly what an operator needs to see, and "the command ran" is not an
// answer about the service.
func (s *Service) Act(ctx context.Context, requestID, key, verb string, actor Actor) (agentclient.ServiceActionResult, error) {
	action, known := auditActions[verb]
	if !known {
		return agentclient.ServiceActionResult{},
			fmt.Errorf("%w: %q", ErrUnknownAction, verb)
	}

	result, err := s.agent.ServiceAction(ctx, requestID, key, verb)
	if err != nil {
		// A refused action is recorded too. "Who tried to stop SSH" is a
		// question worth being able to answer, and an audit trail that holds
		// only what succeeded cannot.
		s.record(ctx, actor, action, audit.StatusFailure, map[string]any{
			"service": key,
			"reason":  agentclient.Message(err),
		})
		return agentclient.ServiceActionResult{}, err
	}

	s.record(ctx, actor, action, audit.StatusSuccess, map[string]any{
		"service": key,
		"unit":    result.Unit,
		"running": result.Running,
	})
	return result, nil
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
