package tenancy

import (
	"context"
	"time"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/logger"
)

// The usage sampler.
//
// Disk and bandwidth are facts about a host, not rows in this database, so
// somebody has to go and look. That happens on a timer rather than when a page
// is opened, for two reasons: measuring is expensive — it walks a customer's
// whole site — and a figure that only exists while somebody is watching cannot
// be used to notice that a customer went over their quota last Tuesday.
//
// The sampler lives in the panel rather than the Agent, alongside the metric
// sampler Phase 19 built and the update scheduler Phase 21 built. The
// privileged half stays in the Agent; the schedule stays somewhere describable.

// DefaultSampleInterval is how often usage is measured.
//
// Fifteen minutes. Disk usage does not change fast enough to be worth walking
// a site more often, and a shorter interval on a host with a hundred sites is
// a panel that spends its life running du.
const DefaultSampleInterval = 15 * time.Minute

// SampleAll measures every subscription once.
//
// One subscription failing does not stop the rest: a site whose directory
// somebody moved would otherwise mean nobody's usage is ever updated again.
// The failure is recorded against that subscription, where somebody will see
// it, rather than only in a log.
func (s *Service) SampleAll(ctx context.Context) error {
	if s.agent == nil {
		return nil
	}
	roots, err := s.repo.ListAllForMeasurement(ctx)
	if err != nil {
		return err
	}

	period := periodStart(s.now())
	for subscriptionID, documentRoots := range roots {
		s.sampleOne(ctx, subscriptionID, documentRoots, period)
	}
	return nil
}

// sampleOne measures one subscription.
func (s *Service) sampleOne(ctx context.Context, subscriptionID string,
	documentRoots []string, period time.Time,
) {
	// A subscription with no websites is measured as using nothing, not as
	// unmeasurable: the panel knows it owns nothing, so zero is the truth here
	// and is the one place in this package where it is.
	if len(documentRoots) == 0 {
		zero := int64(0)
		if err := s.repo.RecordUsage(ctx, RecordUsageParams{
			SubscriptionID: subscriptionID,
			PeriodStart:    period,
			DiskBytes:      &zero,
			RawBandwidth:   &zero,
		}); err != nil {
			s.log.Warn("could not record an empty subscription's usage",
				"subscription_id", subscriptionID, logger.KeyError, err.Error())
		}
		return
	}

	usage, err := s.agent.TenantUsageOf(ctx, httpx.RequestIDFromContext(ctx), documentRoots)
	if err != nil {
		// Nothing measured. The row keeps its previous figures and gains an
		// error, so the page says "measured at 10:15, and the last attempt
		// failed" rather than silently showing a stale number as current.
		if recErr := s.repo.RecordUsage(ctx, RecordUsageParams{
			SubscriptionID: subscriptionID,
			PeriodStart:    period,
			Error:          err.Error(),
		}); recErr != nil {
			s.log.Warn("could not record a failed measurement",
				"subscription_id", subscriptionID, logger.KeyError, recErr.Error())
		}
		return
	}

	message := ""
	if usage.Partial {
		message = "some of this subscription's websites could not be measured, " +
			"so these figures are a floor"
	}

	if err := s.repo.RecordUsage(ctx, RecordUsageParams{
		SubscriptionID: subscriptionID,
		PeriodStart:    period,
		DiskBytes:      usage.DiskBytes,
		RawBandwidth:   usage.BandwidthBytes,
		Error:          message,
	}); err != nil {
		s.log.Warn("could not record a measurement",
			"subscription_id", subscriptionID, logger.KeyError, err.Error())
	}
}

// MeasureOne measures a single subscription now, for an operator who pressed
// the button rather than waiting for the timer.
func (s *Service) MeasureOne(ctx context.Context, actor Actor, subscriptionID string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}

	websites, err := s.repo.ListWebsites(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	roots := make([]string, 0, len(websites))
	for _, website := range websites {
		roots = append(roots, website.DocumentRoot)
	}
	s.sampleOne(ctx, subscriptionID, roots, periodStart(s.now()))

	return s.Subscription(ctx, actor, subscriptionID)
}

// periodStart is the first day of the month a measurement belongs to.
//
// A calendar month rather than a rolling window, because that is the period a
// bandwidth allowance is sold in and the one a customer can check against a
// calendar. A rolling thirty days would be defensible and impossible to
// explain on the telephone.
func periodStart(now time.Time) time.Time {
	utc := now.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// StartSampler runs SampleAll on a timer until ctx is cancelled.
func (s *Service) StartSampler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultSampleInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// One measurement at startup, so a panel that has just come up shows real
	// figures rather than nothing for the first quarter of an hour.
	if err := s.SampleAll(ctx); err != nil {
		s.log.Warn("the first usage measurement failed", logger.KeyError, err.Error())
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.SampleAll(ctx); err != nil {
				s.log.Warn("a usage measurement failed", logger.KeyError, err.Error())
			}
		}
	}
}
