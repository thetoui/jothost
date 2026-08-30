// Package webserver manages which web server arrangement a host runs.
//
// Two arrangements exist: nginx alone, which is what every phase before 4.5
// built, and nginx with Apache behind it. The choice is a property of the
// host — both servers are one process tree serving every site on the machine —
// so it is one setting, and changing it rewrites every site's configuration.
package webserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for web server work.
const (
	ActionModeSet       = "webserver.mode.set"
	ActionApacheInstall = "webserver.apache.install"
	ResourceTypeServer  = "server"
)

// Errors returned by the service.
var (
	// ErrApacheUnavailable means the host cannot run the hybrid arrangement
	// because Apache is not installed.
	ErrApacheUnavailable = errors.New("apache is not installed on this host")
	// ErrAlreadySet means the host already runs the requested arrangement.
	ErrAlreadySet = errors.New("the host already runs that arrangement")
)

// Service reads and changes the host's web server arrangement.
type Service struct {
	websites *websites.Repository
	jobs     *jobs.Repository
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Websites *websites.Repository
	Jobs     *jobs.Repository
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
		websites: opts.Websites,
		jobs:     opts.Jobs,
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

// Status is what the panel shows about the host's web server.
type Status struct {
	// Mode is the arrangement the panel has recorded.
	Mode string `json:"mode"`
	// Apache is what the host can actually do, read from the Agent rather than
	// from the record: the two disagreeing is exactly what an operator needs
	// to see.
	Apache ApacheStatus `json:"apache"`
	// Sites is how many websites a mode change would rewrite.
	Sites int `json:"sites"`
}

// ApacheStatus is the backend as the host reports it.
//
// It is the Agent's own result type rather than a copy: the two would have to
// be kept in step by hand, and a field added on one side and forgotten on the
// other is a fact the panel silently stops showing.
type ApacheStatus = agentclient.ApacheStatusResult

// Status reports the arrangement and what the host supports.
func (s *Service) Status(ctx context.Context, requestID string) (Status, error) {
	mode, err := s.websites.WebserverMode(ctx, s.serverID)
	if err != nil {
		return Status{}, err
	}

	sites, err := s.websites.ListForReconcile(ctx, s.serverID)
	if err != nil {
		return Status{}, err
	}

	status := Status{Mode: mode, Sites: len(sites)}

	apache, err := s.agent.ApacheStatus(ctx, requestID)
	if err != nil {
		// The panel still answers with what it knows. An Agent that cannot be
		// reached is a separate problem from the arrangement, and reporting
		// nothing at all would hide the mode as well.
		s.log.Warn("could not read the apache status from the agent",
			logger.KeyError, err.Error())
		return status, nil
	}
	status.Apache = apache
	return status, nil
}

// SetModeResult is a queued arrangement change.
type SetModeResult struct {
	Mode string `json:"mode"`
	// Jobs are the per-site rewrites, one for each website on the host.
	Jobs []jobs.Job `json:"jobs"`
}

// SetMode changes the host's arrangement and queues every site's rewrite.
//
// The record is changed first and the jobs second, both before anything
// touches the host: each job's payload is built from the record, so a job that
// runs — or is retried — after the switch writes the configuration the panel
// now describes. Queuing first would risk a site being rewritten against the
// old mode and left pointing at a backend nothing was told to create.
func (s *Service) SetMode(ctx context.Context, requestID, mode string, actor Actor) (SetModeResult, error) {
	if err := validate.WebserverMode(mode); err != nil {
		return SetModeResult{}, err
	}

	current, err := s.websites.WebserverMode(ctx, s.serverID)
	if err != nil {
		return SetModeResult{}, err
	}
	if current == mode {
		return SetModeResult{}, fmt.Errorf("%w: %s", ErrAlreadySet, mode)
	}

	if mode == validate.WebserverHybrid {
		// Refused here rather than discovered by every site's job failing in
		// turn, which is what "switch the mode and find out" would look like.
		apache, err := s.agent.ApacheStatus(ctx, requestID)
		if err != nil {
			return SetModeResult{}, err
		}
		if !apache.Available {
			return SetModeResult{}, ErrApacheUnavailable
		}
	}

	sites, err := s.websites.ListForReconcile(ctx, s.serverID)
	if err != nil {
		return SetModeResult{}, err
	}

	// Every site gets its backend port before the mode changes, so no site is
	// ever asked to serve through a port that has not been allocated. Ports
	// are kept afterwards, so switching back and forth does not renumber them.
	if mode == validate.WebserverHybrid {
		for index := range sites {
			port, err := s.websites.AssignBackendPort(ctx, sites[index])
			if err != nil {
				return SetModeResult{}, err
			}
			sites[index].ApachePort = &port
		}
	}

	if err := s.websites.SetWebserverMode(ctx, s.serverID, mode); err != nil {
		return SetModeResult{}, err
	}

	queued := make([]jobs.Job, 0, len(sites))
	for _, site := range sites {
		payload, err := s.websites.VhostPayload(ctx, site)
		if err != nil {
			return SetModeResult{}, err
		}

		job, err := s.jobs.Create(ctx, jobs.CreateParams{
			Type:         jobs.TypeWebsiteUpdate,
			Payload:      payload,
			CreatedBy:    actor.UserID,
			ResourceType: websites.ResourceTypeWebsite,
			ResourceID:   site.ID,
		})
		if err != nil {
			// The mode is already changed and some sites are already queued.
			// Reporting the failure with what was queued is more honest than
			// rolling back a mode that other jobs have started acting on.
			s.log.Error("a website could not be queued for the new arrangement",
				"website_id", site.ID, logger.KeyError, err.Error())
			return SetModeResult{Mode: mode, Jobs: queued}, err
		}
		queued = append(queued, job)
	}

	s.record(ctx, actor, ActionModeSet, map[string]any{
		"mode": mode, "previous": current, "websites": len(queued),
	})

	return SetModeResult{Mode: mode, Jobs: queued}, nil
}

// InstallApache puts Apache on the host.
//
// It does not change the arrangement: installing a package and rewriting every
// site on the machine are different decisions, and doing both because a user
// clicked one button is how an operator loses an afternoon.
func (s *Service) InstallApache(ctx context.Context, requestID string, actor Actor) (ApacheStatus, error) {
	status, err := s.agent.ApacheInstall(ctx, requestID)
	if err != nil {
		return ApacheStatus{}, err
	}

	s.record(ctx, actor, ActionApacheInstall, map[string]any{
		"version": status.Version,
	})
	return status, nil
}

// record writes an audit event, logging rather than failing the request.
func (s *Service) record(ctx context.Context, actor Actor, action string, metadata map[string]any) {
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
		Status:       audit.StatusSuccess,
		Metadata:     metadata,
	})
}
