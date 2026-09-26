package backup

import (
	"context"
	"log/slog"
	"time"

	"github.com/jothost/panel/shared/logger"
)

// Scheduler takes the backups that are due.
//
// # Why this is not a cron job
//
// Phase 10 gives the panel a cron page, and writing a crontab entry would have
// been less code. It would also have been wrong twice over, for the same two
// reasons Phase 21's update scheduler is not one either. Those jobs run as a
// website's own unprivileged account, and reading every file of every site
// needs root — which only the Agent has. And a schedule written into a crontab
// is a schedule the panel can no longer describe: "when does this next run"
// becomes a question about a file somebody may have edited.
//
// So the panel keeps the schedule, this loop decides when it is due, and the
// Agent does the privileged part.
//
// # Why a missed window is not skipped
//
// A schedule is due when its time of day has passed and it has not run since.
// A panel that was off at three in the morning takes the backup when it comes
// back, rather than waiting until tomorrow — because the alternative is a day
// with no backup and nothing saying so.
//
// The cost is that a panel restarted at noon takes last night's backup at noon.
// That is the right trade: a late backup is a backup, and a skipped one is not.
type Scheduler struct {
	service *Service
	repo    *Repository
	log     *slog.Logger

	serverID string
	tick     time.Duration
	now      func() time.Time

	reverifyEvery time.Duration
	reverifyBatch int
	lastReverify  time.Time
}

// reverifySweep is how often the loop looks for backups due another check.
// Hourly: the interval between checks of one backup is days, so looking more
// often finds nothing new, and looking less often lets a batch limit fall
// behind on a host with many backups.
const reverifySweep = time.Hour

// defaultReverifyBatch bounds the backups checked in one sweep. Each check
// reads a whole archive back from its destination — a download, and on S3 a
// billed one — so a host with hundreds of backups spreads them over the week
// rather than pulling all of them at the same minute.
const defaultReverifyBatch = 5

// SchedulerOptions configure a Scheduler.
type SchedulerOptions struct {
	Service  *Service
	Repo     *Repository
	Log      *slog.Logger
	ServerID string
	// Tick is how often the loop asks whether anything is due. It is not how
	// often anything happens: the schedules are.
	Tick time.Duration
	Now  func() time.Time
	// ReverifyEvery is how long a verified backup goes before it is read back
	// and checked again. Zero turns re-verification off.
	ReverifyEvery time.Duration
	// ReverifyBatch bounds the backups checked in one sweep.
	ReverifyBatch int
}

// NewScheduler builds a Scheduler.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	if opts.Tick <= 0 {
		opts.Tick = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.ReverifyBatch <= 0 {
		opts.ReverifyBatch = defaultReverifyBatch
	}
	return &Scheduler{
		service:       opts.Service,
		repo:          opts.Repo,
		log:           log,
		serverID:      opts.ServerID,
		tick:          opts.Tick,
		now:           opts.Now,
		reverifyEvery: opts.ReverifyEvery,
		reverifyBatch: opts.ReverifyBatch,
	}
}

// Run works until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("backup scheduler started", "tick", s.tick.String(),
		"reverify_every", s.reverifyEvery.String())

	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("backup scheduler stopped")
			return
		case <-ticker.C:
		}
		s.runDue(ctx)
		if s.reverifyEvery > 0 && s.now().Sub(s.lastReverify) >= reverifySweep {
			s.lastReverify = s.now()
			s.reverifyDue(ctx)
		}
	}
}

// reverifyDue reads back the backups whose last check is older than
// reverifyEvery, a batch at a time.
//
// Taking a backup proves it could be written. Only reading it back later
// proves it is still there: bit rot, a bucket somebody emptied, a lifecycle
// rule on the provider's side all leave the panel saying "verified" from the
// day it was written, until a restore that fails says otherwise.
func (s *Scheduler) reverifyDue(ctx context.Context) {
	cutoff := s.now().UTC().Add(-s.reverifyEvery)
	due, err := s.repo.DueForReverification(ctx, s.serverID, cutoff, s.reverifyBatch)
	if err != nil {
		s.log.Error("could not read the backups due another check", logger.KeyError, err.Error())
		return
	}
	for _, item := range due {
		if ctx.Err() != nil {
			return
		}
		if err := s.service.reverify(ctx, item); err != nil {
			// Could not check, which is not the same as failed: tried again on
			// a later sweep, since its verified_at is unchanged.
			s.log.Warn("a backup could not be checked again; will retry",
				"backup_id", item.ID, "subject", item.Subject, logger.KeyError, err.Error())
			continue
		}
		s.log.Info("backup checked again", "backup_id", item.ID, "subject", item.Subject)
	}
}

// runDue takes every backup whose time has come.
func (s *Scheduler) runDue(ctx context.Context) {
	due, err := s.repo.DueSchedules(ctx, s.serverID, s.now().UTC())
	if err != nil {
		s.log.Error("could not read the backup schedules", logger.KeyError, err.Error())
		return
	}

	for _, schedule := range due {
		// Marked as run *before* the work is queued, not after. The loop ticks
		// every minute and queueing takes longer than that on a busy host, so
		// waiting until the job finished would start the same backup sixty
		// times in an hour.
		if err := s.repo.RecordScheduleRun(ctx, schedule.ID, "running", ""); err != nil {
			s.log.Error("could not mark a schedule as run",
				"schedule", schedule.Name, logger.KeyError, err.Error())
			continue
		}

		item, err := s.service.runSchedule(ctx, schedule, Actor{})
		if err != nil {
			s.log.Error("a scheduled backup could not be started",
				"schedule", schedule.Name, logger.KeyError, err.Error())
			continue
		}
		s.log.Info("scheduled backup queued",
			"schedule", schedule.Name, "backup_id", item.ID, "subject", item.Subject)
	}
}
