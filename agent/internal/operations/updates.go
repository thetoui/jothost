package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/updates"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The update operations' request boundary.
//
// A request carries package names and, for a revert, one version. Both are
// checked here with validate.PackageName and validate.PackageVersion before
// they can become arguments to a program running as root — and the rule that
// matters most is the one about a leading dash: "--allow-untrusted" is not a
// package, it is a flag, and passing it through would let a request change how
// the package manager behaves rather than what it operates on.
//
// Nothing else arrives from outside. The package manager is chosen by detecting
// what the host has, the repositories are the host's own, and no path, option or
// repository URL is ever taken from a request.

// updatesPayload carries what these operations need.
type updatesPayload struct {
	// Packages names what to apply. Empty means everything the package manager
	// offers, which is what "update all" means.
	Packages []string `json:"packages"`
	// Package and Version name a single package to put back.
	Package string `json:"package"`
	Version string `json:"version"`
}

// updatesProvider returns the provider, or the error a caller should see when
// this host has no package manager the panel drives.
func (r *Registry) updatesProvider() (*updates.Provider, error) {
	if r.deps.Updates == nil || !r.deps.Updates.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			updates.ErrUnavailable.Error(), nil)
	}
	return r.deps.Updates, nil
}

// handleUpdatesCheck reports what this host has waiting.
//
// It refreshes the package index first, which is why it is not free and why the
// panel caches the answer rather than asking on every page load: "check for
// updates" means going and looking, and a check against a stale index is the
// thing this phase exists to avoid.
func (r *Registry) handleUpdatesCheck(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Updates == nil {
		return structToMap(updates.Report{
			Packages: []updates.Package{}, Held: []updates.Held{},
			Reason: updates.ErrUnavailable.Error(),
		})
	}

	report, err := r.deps.Updates.Check(ctx)
	if err != nil {
		return nil, updatesError(err)
	}
	return structToMap(report)
}

// handleUpdatesApply installs updates.
func (r *Registry) handleUpdatesApply(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.updatesProvider()
	if err != nil {
		return nil, err
	}
	var payload updatesPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	for _, name := range payload.Packages {
		if err := validate.PackageName(name); err != nil {
			return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
	}

	result, err := provider.Apply(ctx, payload.Packages, reporterFunc(reporter))
	if err != nil {
		return nil, updatesError(err)
	}
	return structToMap(result)
}

// handleUpdatesRevert puts one package back to an earlier version.
//
// It is not a rollback and does not pretend to be: the provider asks the
// package manager whether that exact version can still be installed and refuses
// when it cannot, which on most hosts most of the time is the answer.
func (r *Registry) handleUpdatesRevert(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.updatesProvider()
	if err != nil {
		return nil, err
	}
	var payload updatesPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.PackageName(payload.Package); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	if err := validate.PackageVersion(payload.Version); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.Revert(ctx, payload.Package, payload.Version, reporterFunc(reporter))
	if err != nil {
		return nil, updatesError(err)
	}
	return structToMap(result)
}

// updatesError maps this package's errors onto protocol codes.
func updatesError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, updates.ErrUnavailable),
		errors.Is(err, updates.ErrSecurityUnsupported):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, updates.ErrVersionUnavailable):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, updates.ErrRefreshFailed),
		errors.Is(err, updates.ErrUpgradeFailed):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, validate.ErrInvalidPackage),
		errors.Is(err, validate.ErrInvalidPackageVersion):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
