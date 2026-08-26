package php

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/shared/logger"
)

// Syncer keeps php_versions matching what the host actually has.
//
// Detection is the Agent's answer, not an operator's assertion. Without this
// the panel would offer whatever was last written to the table, including
// versions someone removed from the host by hand — and a site pointed at one
// of those returns 502 with no obvious cause.
type Syncer struct {
	repo  *Repository
	agent *agentclient.Client
	log   *slog.Logger
}

// NewSyncer builds a Syncer.
func NewSyncer(repo *Repository, agent *agentclient.Client, log *slog.Logger) *Syncer {
	if log == nil {
		log = slog.Default()
	}
	return &Syncer{repo: repo, agent: agent, log: log}
}

// syncTimeout bounds one detection round trip.
const syncTimeout = 30 * time.Second

// Sync refreshes the version table from the Agent.
func (s *Syncer) Sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()

	detected, err := s.detect(ctx)
	if err != nil {
		return err
	}
	return s.repo.SyncVersions(ctx, detected)
}

func (s *Syncer) detect(ctx context.Context) ([]DetectedVersion, error) {
	response, err := s.agent.PHPVersions(ctx, "req_php_sync")
	if err != nil {
		return nil, err
	}

	detected := make([]DetectedVersion, 0, len(response.Versions))
	for _, version := range response.Versions {
		if !version.Installed {
			continue
		}
		detected = append(detected, DetectedVersion{
			Version:    version.Version,
			BinaryPath: version.BinaryPath,
			FPMService: version.FPMService,
			Full:       version.Full,
		})
	}
	return detected, nil
}

// Run refreshes the table at startup and then on an interval.
//
// The interval matters because PHP can be installed or removed outside the
// panel; a table refreshed only at startup drifts until the next restart.
func (s *Syncer) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSyncInterval
	}

	if err := s.Sync(ctx); err != nil {
		// A host without PHP is a valid configuration, so this is reported and
		// the panel keeps running with no versions to offer.
		s.logSyncFailure(err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.Sync(ctx); err != nil && ctx.Err() == nil {
				s.logSyncFailure(err)
			}
		}
	}
}

// DefaultSyncInterval is how often the version table is refreshed.
const DefaultSyncInterval = 5 * time.Minute

func (s *Syncer) logSyncFailure(err error) {
	if agentclient.IsUnsupported(err) || errors.Is(err, agentclient.ErrUnavailable) {
		s.log.Warn("php versions could not be detected",
			"detail", "the panel will offer no PHP until the agent reports some",
			logger.KeyError, err.Error())
		return
	}
	s.log.Error("php version sync failed", logger.KeyError, err.Error())
}

// JobFinished reconciles a PHP version with the outcome of its job.
//
// It satisfies jobs.Observer. Jobs for other resources are ignored, so this can
// be registered alongside the websites reconciler.
func (s *Syncer) JobFinished(ctx context.Context, job jobs.Job, state jobs.State,
	_ map[string]any, failure string,
) {
	switch job.Type {
	case jobs.TypePHPInstall, jobs.TypePHPUninstall:
	case jobs.TypeWebsitePHPSet, jobs.TypeWebsitePHPUnset:
		s.reconcileWebsite(ctx, job, state, failure)
		return
	default:
		return
	}

	version, _ := job.Payload["version"].(string)
	if version == "" {
		return
	}

	log := s.log.With("version", version, "job_id", job.ID, "type", job.Type)

	if state != jobs.StateSuccess {
		if err := s.repo.SetVersionStatus(ctx, version, StatusFailed); err != nil {
			log.Error("failed to mark php version as failed", logger.KeyError, err.Error())
		}
		log.Warn("php version job failed", "reason", failure)
		return
	}

	// Re-detect rather than assuming: the package manager reported success,
	// but what the panel offers must reflect what the host has, and only the
	// Agent can say what that is.
	if err := s.Sync(ctx); err != nil {
		log.Error("php version job succeeded but detection failed", logger.KeyError, err.Error())
		return
	}
	log.Info("php version reconciled")
}

// reconcileWebsite undoes a website PHP record when its job failed.
//
// The pool row and the website's php_version are written before the job runs,
// so the panel can show what the site is becoming. If the work then fails,
// leaving them would have the panel claim a site runs PHP that it does not.
func (s *Syncer) reconcileWebsite(ctx context.Context, job jobs.Job, state jobs.State, failure string) {
	websiteID, _ := job.Payload["website_id"].(string)
	if websiteID == "" {
		return
	}

	log := s.log.With("website_id", websiteID, "job_id", job.ID, "type", job.Type)

	if state == jobs.StateSuccess {
		if job.Type == jobs.TypeWebsitePHPUnset {
			// The pool is gone from the host; the record goes with it.
			if err := s.repo.DeletePool(ctx, websiteID); err != nil {
				log.Error("failed to remove the php pool record", logger.KeyError, err.Error())
			}
		}
		log.Info("website php reconciled")
		return
	}

	log.Warn("website php change failed", "reason", failure)

	switch job.Type {
	case jobs.TypeWebsitePHPSet:
		// Nothing on the host changed, or the change was rolled back, so the
		// site is still whatever it was. The optimistic record is removed.
		if err := s.repo.DeletePool(ctx, websiteID); err != nil {
			log.Error("failed to remove the php pool record", logger.KeyError, err.Error())
		}
		if err := s.repo.SetWebsiteVersion(ctx, websiteID, ""); err != nil {
			log.Error("failed to clear the website php version", logger.KeyError, err.Error())
		}
	case jobs.TypeWebsitePHPUnset:
		// The site still runs PHP, so the version it runs is put back.
		version, _ := job.Payload["version"].(string)
		if version == "" {
			return
		}
		if err := s.repo.SetWebsiteVersion(ctx, websiteID, version); err != nil {
			log.Error("failed to restore the website php version", logger.KeyError, err.Error())
		}
	}
}
