package metrics

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/shared/logger"
)

// Sampler polls the Agent on a cadence and records what it reports.
//
// It runs as a single goroutine tied to the API's lifecycle. The Agent holds
// no history, so without this the dashboard graph would have nothing to plot.
type Sampler struct {
	agent   *agentclient.Client
	metrics *Repository
	servers *servers.Repository
	log     *slog.Logger

	serverID  string
	interval  time.Duration
	retention time.Duration
	// pruneEvery bounds how often retention runs; pruning on every sample
	// would be a delete scan per interval for no benefit.
	pruneEvery time.Duration
	now        func() time.Time
}

// SamplerOptions configures a Sampler.
type SamplerOptions struct {
	Agent    *agentclient.Client
	Metrics  *Repository
	Servers  *servers.Repository
	Log      *slog.Logger
	ServerID string
	// Interval is how often a sample is taken.
	Interval time.Duration
	// Retention is how long samples are kept.
	Retention time.Duration
	// Now defaults to time.Now.
	Now func() time.Time
}

// NewSampler builds a Sampler.
func NewSampler(opts SamplerOptions) *Sampler {
	if opts.Interval <= 0 {
		opts.Interval = 30 * time.Second
	}
	if opts.Retention <= 0 {
		opts.Retention = 30 * 24 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Sampler{
		agent:      opts.Agent,
		metrics:    opts.Metrics,
		servers:    opts.Servers,
		log:        opts.Log,
		serverID:   opts.ServerID,
		interval:   opts.Interval,
		retention:  opts.Retention,
		pruneEvery: time.Hour,
		now:        opts.Now,
	}
}

// Run samples until ctx is cancelled.
//
// A failed sample is logged and skipped rather than ending the loop: an Agent
// restart or a transient timeout must leave a gap in the graph, not stop
// collection until the API is restarted.
func (s *Sampler) Run(ctx context.Context) {
	s.log.Info("metric sampler started",
		"interval", s.interval.String(),
		"retention", s.retention.String(),
	)

	// The first sample is taken immediately so a freshly started panel has a
	// data point rather than an empty graph for one interval.
	s.sampleOnce(ctx)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	pruneTicker := time.NewTicker(s.pruneEvery)
	defer pruneTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.log.Info("metric sampler stopped")
			return
		case <-ticker.C:
			s.sampleOnce(ctx)
		case <-pruneTicker.C:
			s.prune(ctx)
		}
	}
}

// sampleOnce collects and stores one reading.
func (s *Sampler) sampleOnce(ctx context.Context) {
	// The sample is bounded well inside the interval so a slow Agent cannot
	// make samples overlap or drift.
	ctx, cancel := context.WithTimeout(ctx, s.interval)
	defer cancel()

	requestID := httpx.NewRequestID()
	sample := Sample{Timestamp: s.now()}
	reachable := false

	if stats, err := s.agent.CPU(ctx, requestID); err == nil {
		reachable = true
		// The Agent's first CPU reading after a restart has no earlier sample
		// to compare against and reports a zero window. Storing that 0% would
		// be a fabricated measurement, and it plots as a dip to the floor;
		// leaving it absent is the honest record.
		if stats.SampleWindow != "" && stats.SampleWindow != "0s" {
			value := stats.UsagePercent
			sample.CPUPercent = &value
		}
	} else {
		s.logSampleFailure("cpu", err)
	}

	if stats, err := s.agent.Memory(ctx, requestID); err == nil {
		reachable = true
		value := stats.UsedPercent
		sample.MemoryPercent = &value
	} else {
		s.logSampleFailure("memory", err)
	}

	if stats, err := s.agent.Load(ctx, requestID); err == nil {
		reachable = true
		one, five, fifteen := stats.Load1, stats.Load5, stats.Load15
		sample.Load1, sample.Load5, sample.Load15 = &one, &five, &fifteen
	} else {
		s.logSampleFailure("load", err)
	}

	if stats, err := s.agent.Disk(ctx, requestID, ""); err == nil {
		reachable = true
		// One percentage across every real filesystem is what a dashboard
		// gauge needs; the per-filesystem breakdown stays in the live view.
		if stats.TotalBytes > 0 {
			value := round2(float64(stats.UsedBytes) / float64(stats.TotalBytes) * 100)
			sample.DiskPercent = &value
		}
	} else {
		s.logSampleFailure("disk", err)
	}

	if stats, err := s.agent.Network(ctx, requestID); err == nil {
		reachable = true
		rx, tx := int64(stats.TotalRxBytes), int64(stats.TotalTxBytes)
		sample.NetworkRx, sample.NetworkTx = &rx, &tx
	} else {
		s.logSampleFailure("network", err)
	}

	s.recordReachability(ctx, reachable)

	if sample.Empty() {
		// Nothing was collected: the Agent is down. A row of NULLs would be
		// indistinguishable from a host reporting zeros.
		return
	}

	// The insert uses a detached context so a cancelled sample still persists
	// what it managed to collect.
	insertCtx, cancelInsert := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelInsert()

	if err := s.metrics.Insert(insertCtx, s.serverID, sample); err != nil {
		s.log.Error("failed to store metric sample", logger.KeyError, err.Error())
	}
}

// recordReachability keeps the server's status in step with what was observed.
func (s *Sampler) recordReachability(ctx context.Context, reachable bool) {
	status := servers.StatusOffline
	if reachable {
		status = servers.StatusOnline
	}

	statusCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if err := s.servers.SetStatus(statusCtx, s.serverID, status); err != nil {
		s.log.Warn("failed to record server status", logger.KeyError, err.Error())
	}
}

// prune enforces the retention window.
func (s *Sampler) prune(ctx context.Context) {
	pruneCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	removed, err := s.metrics.Prune(pruneCtx, s.now().Add(-s.retention))
	if err != nil {
		s.log.Error("failed to prune metric samples", logger.KeyError, err.Error())
		return
	}
	if removed > 0 {
		s.log.Info("pruned expired metric samples", "removed", removed)
	}
}

// logSampleFailure reports a metric the Agent could not provide.
//
// An unsupported metric is expected on some hosts and is logged at debug;
// anything else is a real failure worth seeing.
func (s *Sampler) logSampleFailure(metric string, err error) {
	if agentclient.IsUnsupported(err) {
		s.log.Debug("metric unsupported on this host", "metric", metric)
		return
	}
	if errors.Is(err, agentclient.ErrUnavailable) {
		s.log.Debug("agent unreachable while sampling", "metric", metric)
		return
	}
	s.log.Warn("failed to sample metric", "metric", metric, logger.KeyError, err.Error())
}

// round2 rounds to two decimal places, matching the column scale.
func round2(value float64) float64 {
	return float64(int64(value*100+0.5)) / 100
}
