package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/shared/validate"
)

// Notifier is told when an alert starts or clears.
//
// An interface declared here rather than an import of the notifications
// package, so the monitor does not depend on it: a panel with no channels
// configured must behave exactly as it did before Phase 20 existed, and a nil
// notifier is how that is expressed.
type Notifier interface {
	AlertOpened(ctx context.Context, alertID, severity, message, target string)
	AlertResolved(ctx context.Context, alertID, severity, message, target string,
		openFor time.Duration)
}

// Monitor takes readings on a cadence and drives the alert engine.
//
// It runs as one goroutine tied to the API's lifecycle, beside the metric
// sampler — and deliberately not *inside* it. The sampler's job is to record
// what the host said; this one's is to decide what that means. Keeping them
// apart is what lets an operator change a threshold without touching the thing
// that collects the data, and what stops a slow evaluation delaying a sample.
type Monitor struct {
	repo     *Repository
	agent    *agentclient.Client
	log      *slog.Logger
	metrics  MetricSource
	notifier Notifier

	serverID string
	interval time.Duration
	// retention bounds how long resolved alerts and closed service stretches
	// are kept.
	retention  time.Duration
	pruneEvery time.Duration
	now        func() time.Time
}

// MetricSource is how the monitor learns how long a condition has held.
//
// An interface over the metric history rather than the history package itself:
// the monitor needs one question answered — "how far back does this breach go"
// — and depending on the whole repository would make the two impossible to
// change independently.
type MetricSource interface {
	// BreachedSince returns when the run of consecutive samples that all breach
	// the threshold began, and whether any sample was found at all.
	//
	// It is asked of the stored samples rather than kept in memory so that a
	// panel restarted mid-incident does not forget that a disk has been full
	// for an hour — which is exactly when a restart is most likely.
	BreachedSince(ctx context.Context, serverID, metric string,
		threshold float64, above bool, now time.Time) (time.Time, bool, error)
}

// MonitorOptions configure a Monitor.
type MonitorOptions struct {
	Repo    *Repository
	Agent   *agentclient.Client
	Metrics MetricSource
	Log     *slog.Logger
	// Notifier is told when an alert starts or clears. Nil is normal: a panel
	// with no channels configured behaves exactly as it did before Phase 20.
	Notifier Notifier

	ServerID string
	// Interval is how often readings are taken. It should be no finer than the
	// metric sampler's, because a second evaluation of the same sample can only
	// reach the same conclusion.
	Interval time.Duration
	// Retention is how long resolved alerts and closed service stretches are
	// kept.
	Retention time.Duration
	Now       func() time.Time
}

// NewMonitor builds a Monitor.
func NewMonitor(opts MonitorOptions) *Monitor {
	if opts.Interval <= 0 {
		opts.Interval = time.Minute
	}
	if opts.Retention <= 0 {
		opts.Retention = 90 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	return &Monitor{
		repo:       opts.Repo,
		agent:      opts.Agent,
		log:        log,
		metrics:    opts.Metrics,
		notifier:   opts.Notifier,
		serverID:   opts.ServerID,
		interval:   opts.Interval,
		retention:  opts.Retention,
		pruneEvery: 6 * time.Hour,
		now:        opts.Now,
	}
}

// Run evaluates until ctx is cancelled.
//
// A failed evaluation is logged and skipped rather than ending the loop. An
// Agent restart or one timed-out probe must leave a gap in the watching, not
// stop it until somebody restarts the panel — which nobody would notice,
// because a monitor that has stopped looks exactly like a host with no problems.
func (m *Monitor) Run(ctx context.Context) {
	m.log.Info("monitor started", "interval", m.interval.String())

	if written, err := m.repo.EnsureDefaults(ctx, m.serverID); err != nil {
		m.log.Error("could not write the default alert rules", "error", err.Error())
	} else if written > 0 {
		m.log.Info("wrote the default alert rules", "rules", written)
	}

	// The first evaluation happens immediately, so a panel that has just
	// started is watching rather than waiting out an interval.
	m.Once(ctx)

	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(m.pruneEvery)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.log.Info("monitor stopped")
			return
		case <-ticker.C:
			m.Once(ctx)
		case <-pruneTicker.C:
			m.prune(ctx)
		}
	}
}

// Once takes one round of readings and applies every rule to them.
func (m *Monitor) Once(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, m.interval)
	defer cancel()

	rules, err := m.repo.ListRules(ctx, m.serverID)
	if err != nil {
		m.log.Error("could not read the alert rules", "error", err.Error())
		return
	}

	now := m.now().UTC()
	readings := m.takeReadings(ctx, now)

	for _, rule := range rules {
		if !rule.Enabled {
			// A disabled rule stops watching, and anything it had open is
			// resolved: leaving an alert open under a rule nobody is applying
			// would be a warning that can never clear.
			m.resolve(ctx, rule, rule.Target, now, "the rule was disabled")
			continue
		}
		m.applyRule(ctx, rule, readings, now)
	}
}

// applyRule judges every reading a rule is interested in.
func (m *Monitor) applyRule(ctx context.Context, rule Rule, readings []Reading, now time.Time) {
	matched := false

	for _, reading := range readings {
		if reading.Metric != rule.Metric {
			continue
		}
		// An empty target watches every instance, which is what somebody
		// setting a general disk threshold means.
		if rule.Target != "" && rule.Target != reading.Target {
			continue
		}
		matched = true

		decision := Evaluate(rule, m.withDuration(ctx, rule, reading, now), now)
		switch {
		case decision.Sustained:
			m.open(ctx, rule, decision.Reading, now)
		case decision.Breached:
			// Breached and not yet sustained. Nothing happens, which is the
			// entire point of the duration: this is where a blip dies.
		case decision.Reading.Available:
			m.resolve(ctx, rule, decision.Reading.Target, now, "")
		}
	}

	// A rule whose target no longer exists — a filesystem unmounted, a service
	// removed — has nothing to judge. Its alert is resolved rather than left
	// open forever about something that is no longer there.
	if !matched && rule.Target != "" {
		m.resolve(ctx, rule, rule.Target, now, "the thing it watched is no longer reported")
	}
}

// withDuration fills in how long a metric reading has been breaching.
//
// Service readings already know, because a service state is a stretch of time
// with a start. Metric readings do not, so the stored samples are asked — which
// is what makes the duration survive a restart.
func (m *Monitor) withDuration(ctx context.Context, rule Rule, reading Reading,
	now time.Time,
) Reading {
	if !reading.Available || rule.ForSeconds <= 0 || !reading.Since.IsZero() {
		return reading
	}
	if m.metrics == nil || !isStoredMetric(rule.Metric) {
		return reading
	}

	since, found, err := m.metrics.BreachedSince(ctx, m.serverID, rule.Metric,
		rule.Threshold, rule.Comparison != validate.ComparisonBelow, now)
	if err != nil {
		m.log.Warn("could not read how long a threshold has been breached",
			"metric", rule.Metric, "error", err.Error())
		return reading
	}
	if found {
		reading.Since = since
	}
	return reading
}

// isStoredMetric reports whether the sample history carries this metric.
//
// Disk is stored as a single whole-host figure, so a rule on one mount point
// cannot get its duration from the samples — it is judged on the live reading
// with whatever duration the caller supplied, which for a per-filesystem rule
// means the first sustained evaluation opens it. Said here rather than
// discovered later.
func isStoredMetric(metric string) bool {
	switch metric {
	case validate.MetricCPU, validate.MetricMemory, validate.MetricLoad:
		return true
	default:
		return false
	}
}

// open records an alert, or refreshes the one already open.
func (m *Monitor) open(ctx context.Context, rule Rule, reading Reading, now time.Time) {
	ruleID := rule.ID
	value := reading.Value
	threshold := rule.Threshold

	alert := Alert{
		ServerID:  m.serverID,
		RuleID:    &ruleID,
		Metric:    rule.Metric,
		Target:    reading.Target,
		Severity:  rule.Severity,
		Threshold: &threshold,
		Message:   Message(rule, reading, now),
		Value:     &value,
		OpenedAt:  now,
	}

	opened, err := m.repo.OpenAlert(ctx, alert)
	if err != nil {
		m.log.Error("could not open an alert", "metric", rule.Metric, "error", err.Error())
		return
	}
	// Logged only when it is new, which is what the timestamps distinguish. A
	// line every minute for the same full disk is a log nobody reads.
	if opened.OpenedAt.Equal(opened.LastSeenAt) {
		m.log.Warn("alert opened",
			"severity", opened.Severity, "metric", opened.Metric,
			"target", opened.Target, "message", opened.Message)
	}

	// Raised on every evaluation, not only the first. The notification's own
	// dedupe key is the alert's id, so a disk that has been full for a week
	// produces one message — and doing it this way means a panel that could not
	// reach its database on the minute the alert opened still notifies when it
	// comes back, rather than having missed its one chance.
	if m.notifier != nil {
		m.notifier.AlertOpened(ctx, opened.ID, opened.Severity,
			opened.Message, opened.Target)
	}
}

// resolve closes an open alert for a condition that has cleared.
func (m *Monitor) resolve(ctx context.Context, rule Rule, target string,
	now time.Time, reason string,
) {
	resolved, found, err := m.repo.ResolveAlert(ctx, m.serverID, rule.ID, target, now)
	if err != nil {
		m.log.Error("could not resolve an alert", "metric", rule.Metric, "error", err.Error())
		return
	}
	if !found {
		return
	}

	fields := []any{
		"severity", resolved.Severity, "metric", resolved.Metric,
		"target", resolved.Target,
		"open_for", formatDuration(now.Sub(resolved.OpenedAt)),
	}
	if reason != "" {
		fields = append(fields, "reason", reason)
	}
	m.log.Info("alert resolved", fields...)

	if m.notifier != nil {
		m.notifier.AlertResolved(ctx, resolved.ID, resolved.Severity,
			resolved.Message, resolved.Target, now.Sub(resolved.OpenedAt))
	}
}

// takeReadings asks the host what it is doing.
//
// One round of probes for every rule, rather than a probe per rule: a host with
// twenty disk rules should not be asked about its disks twenty times, and two
// rules judging different readings of the same thing could disagree about
// whether it breached.
func (m *Monitor) takeReadings(ctx context.Context, now time.Time) []Reading {
	requestID := httpx.NewRequestID()
	readings := make([]Reading, 0, 16)

	if stats, err := m.agent.CPU(ctx, requestID); err == nil {
		readings = append(readings, Reading{
			Metric: validate.MetricCPU, Value: stats.UsagePercent, Available: true,
		})
	} else {
		readings = append(readings, Reading{Metric: validate.MetricCPU})
		m.logProbe("cpu", err)
	}

	if stats, err := m.agent.Memory(ctx, requestID); err == nil {
		readings = append(readings,
			Reading{Metric: validate.MetricMemory, Value: stats.UsedPercent, Available: true})
		// Swap is only meaningful where there is any. On a host with none, a
		// rule about it would otherwise be permanently satisfied at zero.
		if stats.SwapTotalBytes > 0 {
			readings = append(readings, Reading{
				Metric: validate.MetricSwap, Value: stats.SwapUsedPercent, Available: true,
			})
		}
	} else {
		readings = append(readings, Reading{Metric: validate.MetricMemory})
		m.logProbe("memory", err)
	}

	if stats, err := m.agent.Load(ctx, requestID); err == nil {
		readings = append(readings, Reading{
			Metric: validate.MetricLoad, Value: stats.LoadPerCore, Available: true,
		})
	} else {
		readings = append(readings, Reading{Metric: validate.MetricLoad})
		m.logProbe("load", err)
	}

	if stats, err := m.agent.Disk(ctx, requestID, ""); err == nil {
		// Per filesystem, never on an aggregate: a full /var matters even when
		// a large idle /home keeps the overall figure comfortable.
		for _, fs := range stats.Filesystems {
			readings = append(readings, Reading{
				Metric:    validate.MetricDisk,
				Target:    fs.MountPoint,
				Value:     fs.UsedPercent,
				Available: true,
				Detail:    fmt.Sprintf("%s free", humanBytes(fs.FreeBytes)),
			})
		}
	} else {
		readings = append(readings, Reading{Metric: validate.MetricDisk})
		m.logProbe("disk", err)
	}

	readings = append(readings, m.serviceReadings(ctx, requestID, now)...)
	return readings
}

// serviceReadings records what each service is doing and turns it into a
// reading.
//
// The state history is written here rather than in a loop of its own because
// the two need the same probe, and asking twice would let the alert and the
// history disagree about what the host said.
func (m *Monitor) serviceReadings(ctx context.Context, requestID string, now time.Time) []Reading {
	result, err := m.agent.ServiceDetect(ctx, requestID)
	if err != nil {
		m.logProbe("services", err)
		return nil
	}

	readings := make([]Reading, 0, len(result.Services))
	for _, service := range result.Services {
		status := serviceStatus(service)

		// Recorded whatever it says, including "not installed": the history is
		// about what the host reported, and a service that appears or vanishes
		// is exactly the kind of change worth being able to look back at.
		state, changed, err := m.repo.RecordServiceState(ctx, m.serverID, service.Key,
			service.Running, status, now)
		if err != nil {
			m.log.Warn("could not record a service state",
				"service", service.Key, "error", err.Error())
			continue
		}
		if changed {
			m.log.Info("service state changed",
				"service", service.Key, "status", status, "running", service.Running)
		}

		// Alerting is a different question from recording. "Not installed" is a
		// configuration rather than a fault, and a rule on it would fire
		// permanently on every host missing an optional unit.
		if !service.Installed {
			continue
		}

		value := 0.0
		if service.Running {
			value = 1
		}
		readings = append(readings, Reading{
			Metric: validate.MetricService,
			Target: service.Key,
			Value:  value,
			// The state's own start, so "down for two hours" is a subtraction
			// rather than a guess — and survives a restart of the panel.
			Since:     state.StartedAt,
			Available: true,
			Detail:    status,
		})
	}
	return readings
}

// serviceStatus renders what a service is doing.
//
// The same words the dashboard uses, and for the same reason it uses them:
// "stopped", "not installed" and "starting" are three different answers, and a
// history that rounded them together would say a service was down when it was
// coming up.
func serviceStatus(service agentclient.DetectedService) string {
	switch {
	case !service.Installed:
		return "not installed"
	case service.Running:
		return "running"
	case service.ActiveState == "failed":
		return "failed"
	case service.ActiveState == "activating":
		return "starting"
	default:
		return "stopped"
	}
}

// logProbe reports a reading that could not be taken.
//
// An unsupported metric is a property of the host and is logged at debug;
// anything else is a real failure worth seeing. Neither opens or resolves
// anything — see Evaluate.
func (m *Monitor) logProbe(metric string, err error) {
	if agentclient.IsUnsupported(err) {
		m.log.Debug("metric unsupported on this host", "metric", metric)
		return
	}
	m.log.Warn("could not read a metric", "metric", metric, "error", err.Error())
}

// prune removes history nobody will look at.
func (m *Monitor) prune(ctx context.Context) {
	pruneCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()

	cutoff := m.now().Add(-m.retention)

	if removed, err := m.repo.PruneAlerts(pruneCtx, cutoff); err != nil {
		m.log.Error("could not prune resolved alerts", "error", err.Error())
	} else if removed > 0 {
		m.log.Info("pruned resolved alerts", "removed", removed)
	}

	if removed, err := m.repo.PruneServiceStates(pruneCtx, cutoff); err != nil {
		m.log.Error("could not prune service history", "error", err.Error())
	} else if removed > 0 {
		m.log.Info("pruned service history", "removed", removed)
	}
}

// humanBytes renders a size the way somebody would say it.
func humanBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMGTP"[exp])
}
