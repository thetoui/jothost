package operations

import (
	"context"
	"errors"
	"time"

	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/version"
)

// startedAt is the process start time, reported by agent.info.
var startedAt = time.Now()

// handlePing is a non-privileged liveness probe. It performs no host access.
func (r *Registry) handlePing(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	return map[string]any{
		"pong":    true,
		"version": version.Current().Version,
	}, nil
}

// handleAgentInfo reports what this Agent can do.
//
// Capabilities are advertised rather than assumed so the API can present an
// honest picture on a host without systemd, instead of showing a service panel
// whose every entry errors.
func (r *Registry) handleAgentInfo(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	ops := make([]string, 0, len(r.handlers))
	for _, op := range r.Operations() {
		ops = append(ops, string(op))
	}

	return map[string]any{
		"version":        version.Current().Version,
		"commit":         version.Current().Commit,
		"build_date":     version.Current().BuildDate,
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
		"operations":     ops,
		"capabilities": map[string]bool{
			"metrics":  r.deps.Collector.Available(),
			"services": r.deps.Services.Available(),
			"jobs":     true,
		},
	}, nil
}

// handleSystemInfo reports the host's identity.
func (r *Registry) handleSystemInfo(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	info, err := r.deps.Collector.System()
	if err != nil {
		return nil, metricError(err)
	}
	return structToMap(info)
}

func (r *Registry) handleMetricsCPU(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	stats, err := r.deps.Collector.CPU()
	if err != nil {
		return nil, metricError(err)
	}
	return structToMap(stats)
}

func (r *Registry) handleMetricsMemory(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	stats, err := r.deps.Collector.Memory()
	if err != nil {
		return nil, metricError(err)
	}
	return structToMap(stats)
}

func (r *Registry) handleMetricsLoad(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	stats, err := r.deps.Collector.Load()
	if err != nil {
		return nil, metricError(err)
	}
	return structToMap(stats)
}

func (r *Registry) handleMetricsNetwork(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	stats, err := r.deps.Collector.Network()
	if err != nil {
		return nil, metricError(err)
	}
	return structToMap(stats)
}

// diskPayload optionally narrows a disk report to one mount point.
type diskPayload struct {
	MountPoint string `json:"mount_point"`
}

func (r *Registry) handleMetricsDisk(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload diskPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	// The mount point is matched against the kernel's mount table rather than
	// used as a path, so it cannot be used to probe the filesystem. It is
	// still validated first: a value with a null byte or a traversal segment
	// is a caller bug worth refusing loudly.
	if payload.MountPoint != "" {
		if err := validateMountPoint(payload.MountPoint); err != nil {
			return nil, err
		}
	}

	stats, err := r.deps.Collector.Disk(payload.MountPoint)
	if err != nil {
		if errors.Is(err, collectors.ErrNotMounted) {
			return nil, Fail(protocol.CodeNotFound, "That path is not a mounted filesystem", err)
		}
		return nil, metricError(err)
	}
	return structToMap(stats)
}

// processListPayload filters a process listing.
type processListPayload struct {
	Limit  int    `json:"limit"`
	SortBy string `json:"sort_by"`
}

func (r *Registry) handleProcessList(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload processListPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if payload.Limit < 0 {
		return nil, Fail(protocol.CodeInvalidPayload, "limit must not be negative", nil)
	}
	switch payload.SortBy {
	case "", "memory", "cpu":
	default:
		return nil, Fail(protocol.CodeInvalidPayload, `sort_by must be "memory" or "cpu"`, nil)
	}

	processes, err := r.deps.Collector.ProcessList(collectors.ProcessListOptions{
		Limit:  payload.Limit,
		SortBy: payload.SortBy,
	})
	if err != nil {
		return nil, metricError(err)
	}

	return map[string]any{
		"processes": processes,
		"count":     len(processes),
	}, nil
}

// serviceListPayload names the services to report on.
type serviceListPayload struct {
	Names []string `json:"names"`
}

func (r *Registry) handleServiceList(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload serviceListPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if len(payload.Names) == 0 {
		return nil, Fail(protocol.CodeInvalidPayload, "names must list at least one service", nil)
	}

	statuses, err := r.deps.Services.List(ctx, payload.Names)
	if err != nil {
		return nil, serviceError(err)
	}

	return map[string]any{
		"services": statuses,
		"count":    len(statuses),
	}, nil
}

// serviceStatusPayload names one service.
type serviceStatusPayload struct {
	Name string `json:"name"`
}

func (r *Registry) handleServiceStatus(ctx context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload serviceStatusPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.Name == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "name is required", nil)
	}

	status, err := r.deps.Services.Status(ctx, payload.Name)
	if err != nil {
		return nil, serviceError(err)
	}
	return structToMap(status)
}

// jobPayload identifies a job.
type jobPayload struct {
	JobID string `json:"job_id"`
}

func (r *Registry) handleJobStatus(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload jobPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.JobID == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "job_id is required", nil)
	}

	job, err := r.deps.Jobs.Get(payload.JobID)
	if err != nil {
		return nil, Fail(protocol.CodeNotFound, "Job not found", err)
	}
	return structToMap(job)
}

func (r *Registry) handleJobCancel(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	var payload jobPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if payload.JobID == "" {
		return nil, Fail(protocol.CodeInvalidPayload, "job_id is required", nil)
	}

	if err := r.deps.Jobs.Cancel(payload.JobID); err != nil {
		switch {
		case errors.Is(err, jobs.ErrNotFound):
			return nil, Fail(protocol.CodeNotFound, "Job not found", err)
		case errors.Is(err, jobs.ErrTerminal):
			return nil, Fail(protocol.CodeInvalidRequest, "Job has already finished", err)
		default:
			return nil, err
		}
	}

	return map[string]any{"cancelled": true, "job_id": payload.JobID}, nil
}

func (r *Registry) handleJobList(_ context.Context, req protocol.Request, _ *jobs.Reporter) (map[string]any, error) {
	list := r.deps.Jobs.List()
	return map[string]any{
		"jobs":  list,
		"count": len(list),
	}, nil
}

// metricError maps a collector failure to a structured response.
func metricError(err error) error {
	if errors.Is(err, collectors.ErrUnsupported) {
		return Fail(protocol.CodeUnsupported, "This metric is not available on this host", err)
	}
	if errors.Is(err, collectors.ErrMalformed) {
		return Fail(protocol.CodeInternal, "The host reported an unreadable value", err)
	}
	return err
}

// serviceError maps a service-provider failure to a structured response.
func serviceError(err error) error {
	switch {
	case errors.Is(err, services.ErrUnavailable):
		return Fail(protocol.CodeUnsupported, "No service manager is available on this host", err)
	case errors.Is(err, services.ErrInvalidName):
		return Fail(protocol.CodeInvalidPayload, "That is not a valid service name", err)
	case errors.Is(err, services.ErrNotFound):
		return Fail(protocol.CodeNotFound, "Service not found", err)
	default:
		return err
	}
}
