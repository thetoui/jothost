package operations

import (
	"context"
	"fmt"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/tenancy"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The tenancy operations' request boundary.
//
// A request carries two kinds of thing and no third kind.
//
// Paths: the document roots the panel recorded when it created each website.
// They are resolved on the far side through the same provisioner that created
// them, so a path outside the allowed root is refused by the code that owns
// that rule — this handler does not write the rule out again and get it
// subtly different.
//
// Numbers: a CPU percentage, a memory ceiling in megabytes, an IO weight. They
// are integers, they are bounds-checked here and again when the unit is
// rendered, and they become directives only through a template. No part of a
// request is ever a directive, a unit name of the caller's choosing, or a
// cgroup path.

// tenantUsagePayload asks what a set of websites is using.
type tenantUsagePayload struct {
	DocumentRoots []string `json:"document_roots"`
}

// tenantIsolationPayload carries the caps a subscription was sold.
type tenantIsolationPayload struct {
	Slice       string `json:"slice"`
	Description string `json:"description"`
	CPUPercent  *int   `json:"cpu_percent"`
	MemoryMB    *int   `json:"memory_mb"`
	IOWeight    *int   `json:"io_weight"`
}

// maxUsageSites bounds one measurement.
//
// A subscription with more sites than this is not a subscription; it is a
// request built to make the Agent walk the whole disk. The limit is generous
// enough that no real plan reaches it.
const maxUsageSites = 500

// tenancyProvider returns the provider, or the error a caller should see.
func (r *Registry) tenancyProvider() (*tenancy.Provider, error) {
	if r.deps.Tenancy == nil {
		return nil, Fail(protocol.CodeUnsupported,
			"this host cannot measure or limit subscriptions", nil)
	}
	return r.deps.Tenancy, nil
}

// handleTenantStatus reports what this host can do about tenancy.
//
// Asked before anything else so a page can say "this host cannot enforce
// resource limits" up front, rather than after somebody has configured a plan
// and is waiting to find out why nothing is capped.
func (r *Registry) handleTenantStatus(_ context.Context, _ protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.tenancyProvider()
	if err != nil {
		return map[string]any{
			"isolation_available": false,
			"isolation_detail":    "this host cannot apply resource limits",
			"placement":           false,
		}, nil
	}
	available, detail := provider.IsolationStatus()
	return map[string]any{
		"isolation_available": available,
		"isolation_detail":    detail,
		// Whether anything actually runs inside a subscription's slice.
		// Reported as its own fact because it is the difference between a
		// limit and a limit on nothing, and the panel must not imply one from
		// the other.
		"placement": false,
	}, nil
}

// handleTenantUsage measures a subscription's websites.
func (r *Registry) handleTenantUsage(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.tenancyProvider()
	if err != nil {
		return nil, err
	}

	var payload tenantUsagePayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if len(payload.DocumentRoots) > maxUsageSites {
		return nil, Fail(protocol.CodeInvalidPayload,
			fmt.Sprintf("a measurement covers at most %d websites", maxUsageSites), nil)
	}
	for _, root := range payload.DocumentRoots {
		if root == "" || root[0] != '/' {
			return nil, Fail(protocol.CodeInvalidPayload,
				"a document root must be an absolute path", nil)
		}
	}

	return structToMap(provider.Usage(ctx, payload.DocumentRoots))
}

// handleTenantIsolationApply writes a subscription's resource limits.
//
// The reply distinguishes what was written from what is enforced. That is the
// whole value of the operation on a host without systemd: the caller learns
// that nothing is capped instead of being told the change succeeded.
func (r *Registry) handleTenantIsolationApply(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.tenancyProvider()
	if err != nil {
		return nil, err
	}

	var payload tenantIsolationPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.SliceName(payload.Slice); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	if err := validate.Isolation(payload.CPUPercent, payload.MemoryMB, payload.IOWeight); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	if err := validate.PlanName(payload.Description); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.ApplyIsolation(ctx, payload.Slice, payload.Description, tenancy.Limits{
		CPUPercent: payload.CPUPercent,
		MemoryMB:   payload.MemoryMB,
		IOWeight:   payload.IOWeight,
	})
	if err != nil {
		return nil, Fail(protocol.CodeInternal, err.Error(), err)
	}
	return structToMap(result)
}

// handleTenantIsolationRemove deletes a subscription's slice.
func (r *Registry) handleTenantIsolationRemove(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.tenancyProvider()
	if err != nil {
		return nil, err
	}

	var payload struct {
		Slice string `json:"slice"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.SliceName(payload.Slice); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.RemoveIsolation(ctx, payload.Slice)
	if err != nil {
		return nil, Fail(protocol.CodeInternal, err.Error(), err)
	}
	return structToMap(result)
}
