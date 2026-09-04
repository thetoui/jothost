package notifications

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jothost/panel/shared/logger"
)

// Dispatcher delivers what the rest of the panel raised.
//
// # Why a loop and not a call
//
// Because the alternative is the monitor waiting on somebody's SMTP server
// while alerts queue behind it. The phases raise events; this delivers them; a
// slow relay costs a notification its promptness and costs the panel nothing
// else.
//
// # The retry policy, and why it gives up
//
// Backoff, and then a limit. A delivery retried forever is a queue that never
// drains, and behind it every later notification waits — so a channel with a
// wrong password would delay every alert on the machine indefinitely while
// producing nothing.
//
// A permanent failure is not retried at all. A wrong bot token, a chat the bot
// was removed from, a mail server refusing the sender: retrying four times
// reaches the same answer four times. The sender says which kind of failure it
// was, and that distinction is what keeps the queue moving.
//
// Either way the row stays, with the reason. A notification that was never
// delivered is the single most important thing this table records: it is the
// only way an operator learns that the silence they have been enjoying was not
// good news.
type Dispatcher struct {
	service *Service
	repo    *Repository
	log     *slog.Logger

	interval    time.Duration
	batch       int
	maxAttempts int
	retention   time.Duration
	now         func() time.Time
}

// Defaults for a Dispatcher.
const (
	// DefaultInterval is how often the queue is drained. Fifteen seconds is
	// well under the minute the monitor takes to notice anything, so a
	// notification is never waiting on this loop rather than on the event.
	DefaultInterval = 15 * time.Second
	// DefaultBatch bounds one round, so a backlog is worked through steadily
	// rather than in one burst that holds a database connection for minutes.
	DefaultBatch = 20
	// DefaultMaxAttempts is where a transient failure becomes a permanent one.
	// Four attempts with the backoff below spans about a quarter of an hour,
	// which covers a relay being restarted and does not cover a relay that is
	// gone.
	DefaultMaxAttempts = 4
	// DefaultRetention is how long the event history is kept.
	DefaultRetention = 30 * 24 * time.Hour
)

// DispatcherOptions configure a Dispatcher.
type DispatcherOptions struct {
	Service     *Service
	Repo        *Repository
	Log         *slog.Logger
	Interval    time.Duration
	Batch       int
	MaxAttempts int
	Retention   time.Duration
	Now         func() time.Time
}

// NewDispatcher builds a Dispatcher.
func NewDispatcher(opts DispatcherOptions) *Dispatcher {
	if opts.Interval <= 0 {
		opts.Interval = DefaultInterval
	}
	if opts.Batch <= 0 {
		opts.Batch = DefaultBatch
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.Retention <= 0 {
		opts.Retention = DefaultRetention
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		service:     opts.Service,
		repo:        opts.Repo,
		log:         log,
		interval:    opts.Interval,
		batch:       opts.Batch,
		maxAttempts: opts.MaxAttempts,
		retention:   opts.Retention,
		now:         opts.Now,
	}
}

// Run works until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("notification dispatcher started", "interval", d.interval.String())

	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	// Hourly, and only the prune. The events table grows by one row per thing
	// that happened, which is slow, and a delivery record nobody can read back
	// is worth less than the disk it costs.
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()

	for {
		select {
		case <-ctx.Done():
			d.log.Info("notification dispatcher stopped")
			return
		case <-ticker.C:
			d.Once(ctx)
		case <-prune.C:
			d.prune(ctx)
		}
	}
}

// Once drains one batch. Exported so a test can drive the loop without waiting.
func (d *Dispatcher) Once(ctx context.Context) {
	due, err := d.repo.ClaimDue(ctx, d.now().UTC(), d.batch)
	if err != nil {
		d.log.Error("could not read the notification queue", logger.KeyError, err.Error())
		return
	}

	for _, item := range due {
		d.deliver(ctx, item)
	}
}

// deliver attempts one delivery and records what happened.
func (d *Dispatcher) deliver(ctx context.Context, item Due) {
	message := Message{
		Severity: item.Event.Severity,
		Title:    item.Event.Title,
		Body:     item.Event.Body,
		Link:     item.Event.Link,
	}

	// Bounded independently of the loop: a channel that accepts a connection
	// and then never answers must not hold the batch.
	sendCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	sendErr := d.service.send(sendCtx, item.Channel, message)
	now := d.now().UTC()

	if sendErr == nil {
		if err := d.repo.MarkSent(ctx, item.Delivery.ID, now); err != nil {
			d.log.Error("could not record a delivered notification",
				logger.KeyError, err.Error())
		}
		if err := d.repo.RecordChannelResult(ctx, item.Channel.ID, true, "", now); err != nil {
			d.log.Error("could not record a channel success", logger.KeyError, err.Error())
		}
		d.log.Info("notification delivered",
			"channel", item.Channel.Name, "kind", item.Event.Kind,
			"attempts", item.Delivery.Attempts)
		return
	}

	reason := sendErr.Error()
	if err := d.repo.RecordChannelResult(ctx, item.Channel.ID, false, reason, now); err != nil {
		d.log.Error("could not record a channel failure", logger.KeyError, err.Error())
	}

	// A permanent failure is not retried. Retrying a wrong password four times
	// reaches the same answer four times, and delays everything behind it.
	permanent := errors.Is(sendErr, ErrPermanent)
	exhausted := item.Delivery.Attempts >= d.maxAttempts

	if permanent || exhausted {
		if err := d.repo.MarkFailed(ctx, item.Delivery.ID, reason); err != nil {
			d.log.Error("could not record a failed notification",
				logger.KeyError, err.Error())
		}
		// Warn, not Error: the notification failing is news about the channel,
		// and the panel's own operation is fine. It is logged at all because
		// the log is the one place this is visible without opening the page.
		d.log.Warn("notification not delivered",
			"channel", item.Channel.Name, "kind", item.Event.Kind,
			"attempts", item.Delivery.Attempts,
			"permanent", permanent, "reason", reason)
		return
	}

	next := now.Add(backoff(item.Delivery.Attempts))
	if err := d.repo.Reschedule(ctx, item.Delivery.ID, reason, next); err != nil {
		d.log.Error("could not reschedule a notification", logger.KeyError, err.Error())
	}
	d.log.Warn("notification delivery failed, will retry",
		"channel", item.Channel.Name, "attempt", item.Delivery.Attempts,
		"next_attempt", next.Format(time.RFC3339), "reason", reason)
}

// backoff returns how long to wait before attempt n+1.
//
// A minute, then four, then sixteen. Exponential rather than fixed because the
// failures worth retrying are outages, and an outage that has already lasted
// five minutes is more likely to last another five than to end in the next
// thirty seconds — so a fixed interval spends its attempts in the window where
// they are least likely to work.
func backoff(attempts int) time.Duration {
	switch {
	case attempts <= 1:
		return time.Minute
	case attempts == 2:
		return 4 * time.Minute
	default:
		return 16 * time.Minute
	}
}

// prune removes events past the retention window.
func (d *Dispatcher) prune(ctx context.Context) {
	cutoff := d.now().UTC().Add(-d.retention)
	removed, err := d.repo.PruneEvents(ctx, cutoff)
	if err != nil {
		d.log.Error("could not prune notification history", logger.KeyError, err.Error())
		return
	}
	if removed > 0 {
		d.log.Info("pruned notification history", "removed", removed)
	}
}
