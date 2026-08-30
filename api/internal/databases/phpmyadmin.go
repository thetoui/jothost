package databases

import (
	"context"
	"errors"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/validate"
)

// Audit action names for the database console.
const (
	ActionPHPMyAdminInstall   = "phpmyadmin.install"
	ActionPHPMyAdminUninstall = "phpmyadmin.uninstall"

	ResourceTypeServer = "server"
)

// ErrConsoleUnavailable means the host cannot run phpMyAdmin.
var ErrConsoleUnavailable = errors.New("phpMyAdmin cannot be installed on this server")

// Console is the phpMyAdmin subset of the agent client.
type Console interface {
	PHPMyAdmin(ctx context.Context, requestID string) (agentclient.PHPMyAdminStatus, error)
}

// ConsoleStatus reports whether phpMyAdmin is installed and where it is served.
func (s *Service) ConsoleStatus(ctx context.Context, requestID string) (agentclient.PHPMyAdminStatus, error) {
	console, ok := s.agent.(Console)
	if !ok {
		return agentclient.PHPMyAdminStatus{}, ErrConsoleUnavailable
	}
	return console.PHPMyAdmin(ctx, requestID)
}

// InstallConsole queues installation of phpMyAdmin.
//
// This is the one operation in this package that goes through the job queue.
// The rest are a single statement each; this is a package download, a system
// account, an FPM pool, and a vhost — minutes, not milliseconds, and nothing in
// the response depends on it having finished.
func (s *Service) InstallConsole(ctx context.Context, serverName string, actor Actor) (jobs.Job, error) {
	if s.serverID == "" {
		return jobs.Job{}, ErrNoServer
	}

	name := validate.NormalizeDomain(serverName)
	if err := validate.Domain(name); err != nil {
		return jobs.Job{}, err
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypePHPMyAdminInstall,
		Payload:      map[string]any{"server_name": name},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeServer,
		ResourceID:   s.serverID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionPHPMyAdminInstall, "", map[string]any{
		"server_name": name, "job_id": job.ID,
	})
	return job, nil
}

// UninstallConsole queues removal of phpMyAdmin.
func (s *Service) UninstallConsole(ctx context.Context, actor Actor) (jobs.Job, error) {
	if s.serverID == "" {
		return jobs.Job{}, ErrNoServer
	}

	job, err := s.jobs.Create(ctx, jobs.CreateParams{
		Type:         jobs.TypePHPMyAdminUninstall,
		Payload:      map[string]any{},
		CreatedBy:    actor.UserID,
		ResourceType: ResourceTypeServer,
		ResourceID:   s.serverID,
	})
	if err != nil {
		return jobs.Job{}, err
	}

	s.record(ctx, actor, ActionPHPMyAdminUninstall, "", nil)
	return job, nil
}
