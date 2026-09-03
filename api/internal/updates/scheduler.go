package updates

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/validate"
)

// Scheduler checks for updates on a cadence and applies them in their window.
//
// # Why this is not a cron job
//
// Phase 10 gives the panel a cron page, and it would have been less code to
// write a crontab entry. It would also have been wrong twice over. Those jobs
// run as a website's own unprivileged account, and upgrading the machine needs
// root — which only the Agent has. And a schedule written into a crontab is a
// schedule the panel can no longer describe: "when does this next run" becomes
// a question about a file somebody may have edited.
//
// So the panel keeps the schedule, this loop decides when it is due, and the
// Agent does the privileged part. The same arrangement as the metric sampler,
// for the same reasons.
//
// # Why the window is a window
//
// Applying updates restarts daemons. An operator picks a quiet hour, and this
// loop only acts inside it — which means it must run often enough to notice the
// window and must not act twice within one.
type Scheduler struct {
	service  *Service
	repo     *Repository
	log      *slog.Logger
	serverID string

	// tick is how often the loop wakes to ask whether anything is due. It is
	// not how often anything happens: the check interval and the window are.
	tick time.Duration
	now  func() time.Time
}

// SchedulerOptions configure a Scheduler.
type SchedulerOptions struct {
	Service  *Service
	Repo     *Repository
	Log      *slog.Logger
	ServerID string
	// Tick defaults to a minute, which is fine enough to hit a window given to
	// the minute and coarse enough to cost nothing.
	Tick time.Duration
	Now  func() time.Time
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
	return &Scheduler{
		service:  opts.Service,
		repo:     opts.Repo,
		log:      log,
		serverID: opts.ServerID,
		tick:     opts.Tick,
		now:      opts.Now,
	}
}

// Run works until ctx is cancelled.
//
// A failed tick is logged and skipped rather than ending the loop: an Agent
// restart or an unreachable mirror must leave a gap, not stop the panel
// checking until it is restarted.
func (s *Scheduler) Run(ctx context.Context) {
	s.log.Info("update scheduler started", "tick", s.tick.String())

	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("update scheduler stopped")
			return
		case <-ticker.C:
			s.once(ctx)
		}
	}
}

// once does whatever is due.
func (s *Scheduler) once(ctx context.Context) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		s.log.Warn("could not read the update settings", "error", err.Error())
		return
	}

	if s.checkDue(settings) {
		s.check(ctx)
		// Re-read: the check has just moved last_checked_at, and the apply
		// below decides on the list that check produced.
		if settings, err = s.repo.Settings(ctx, s.serverID); err != nil {
			return
		}
	}

	if settings.Policy == validate.UpdatesOff {
		return
	}
	if !s.windowDue(settings) {
		return
	}
	s.apply(ctx, settings)
}

// checkDue reports whether it is time to look.
func (s *Scheduler) checkDue(settings Settings) bool {
	if settings.LastCheckedAt == nil {
		// Never checked. Looking once at startup is what makes a freshly
		// installed panel show something other than "unknown" on its first day.
		return true
	}
	interval := time.Duration(settings.CheckIntervalHours) * time.Hour
	return s.now().UTC().Sub(settings.LastCheckedAt.UTC()) >= interval
}

// windowDue reports whether now is inside the configured window and nothing has
// run in it yet.
//
// "Nothing yet" is the half that matters. The loop wakes every minute, and a
// window is a minute wide; without this an operator who set 03:00 would get one
// upgrade, and an operator whose upgrade took ninety seconds would get two.
func (s *Scheduler) windowDue(settings Settings) bool {
	now := s.now()
	if settings.DayOfWeek != validate.EveryDay && int(now.Weekday()) != settings.DayOfWeek {
		return false
	}
	if now.Hour() != settings.Hour || now.Minute() != settings.Minute {
		return false
	}
	if settings.LastRunAt == nil {
		return true
	}
	// Anything within the last hour counts as "this window", which covers a run
	// that took longer than a minute and a clock that moved slightly.
	return now.UTC().Sub(settings.LastRunAt.UTC()) > time.Hour
}

// check asks the host what it has waiting.
func (s *Scheduler) check(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	check, err := s.service.CheckNow(ctx, httpx.NewRequestID())
	if err != nil {
		s.log.Warn("the scheduled update check failed", "error", err.Error())
		// Recorded anyway, so a mirror that has been down for a week is not
		// retried every minute and is visible as what it is.
		if markErr := s.repo.MarkChecked(ctx, s.serverID, s.now().UTC()); markErr != nil {
			s.log.Warn("could not record the failed check", "error", markErr.Error())
		}
		return
	}
	if !check.Succeeded {
		s.log.Warn("the scheduled update check could not read the repositories",
			"reason", check.Reason)
		return
	}
	if check.PackageCount > 0 {
		s.log.Info("updates are available",
			"packages", check.PackageCount, "security", check.SecurityCount)
	}
}

// apply installs what the policy allows.
func (s *Scheduler) apply(ctx context.Context, settings Settings) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()

	request := ApplyRequest{Trigger: TriggerScheduled}
	if settings.Policy == validate.UpdatesSecurity {
		request.SecurityOnly = true
	}

	// No actor: nobody asked, and inventing a user id in the audit trail would
	// be worse than a record that plainly says the schedule did it.
	run, err := s.service.Apply(ctx, Actor{}, httpx.NewRequestID(), request)
	switch {
	case err == nil:
		s.log.Info("scheduled updates applied",
			"run", run.ID, "changed", len(run.Changes), "policy", settings.Policy)
	case isExpected(err):
		// Nothing outstanding, nothing this host can identify as a security
		// fix, or everything excluded. All ordinary, none worth an error.
		s.log.Debug("scheduled updates had nothing to do", "reason", err.Error())
		if markErr := s.repo.MarkRun(ctx, s.serverID, s.now().UTC()); markErr != nil {
			s.log.Warn("could not record the empty run", "error", markErr.Error())
		}
	default:
		s.log.Error("scheduled updates failed", "error", err.Error())
	}
}

// isExpected reports whether an apply failure is an ordinary "nothing to do".
//
// All four are states a healthy host reaches on most nights, and logging them
// as errors would make a log nobody reads — which is how a real failure goes
// unnoticed.
func isExpected(err error) bool {
	for _, expected := range []error{
		ErrNothingToDo, ErrSecurityUnknown, ErrExcluded, ErrUnavailable,
	} {
		if errors.Is(err, expected) {
			return true
		}
	}
	return false
}
