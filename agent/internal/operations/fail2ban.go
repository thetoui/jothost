package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/fail2ban"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The intrusion-prevention operations' request boundary.
//
// A request names a **jail from the Agent's catalogue** and, where relevant, an
// address that is parsed before it becomes an argument. It never names a filter,
// a log path or an action: those decide who gets banned, and a regular
// expression arriving over the wire would be a way to lock anybody out of the
// machine — or, more likely, to ban nobody while the panel reported protection.

// fail2banPayload carries what these operations need.
type fail2banPayload struct {
	// Jail is a catalogue name.
	Jail string `json:"jail"`
	// Enabled turns a jail on or off. Nil leaves it alone.
	Enabled *bool `json:"enabled"`
	// The policy. Nil fields are left as they are, so changing the ban time
	// does not silently reset the threshold.
	MaxRetry *int `json:"max_retry"`
	FindTime *int `json:"find_time"`
	BanTime  *int `json:"ban_time"`
	// Address is what a ban or an unban acts on.
	Address string `json:"address"`
	// Ignored is the list of addresses no jail may ban.
	Ignored []string `json:"ignored"`
}

// fail2banProvider returns the provider, or the error a caller should see when
// this host has no fail2ban.
func (r *Registry) fail2banProvider() (*fail2ban.Provider, error) {
	if r.deps.Fail2Ban == nil || !r.deps.Fail2Ban.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"fail2ban is not installed on this host", nil)
	}
	return r.deps.Fail2Ban, nil
}

// handleFail2banStatus reports the jails, the bans and the policy.
func (r *Registry) handleFail2banStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Fail2Ban == nil {
		return structToMap(fail2ban.Status{
			Jails: []fail2ban.Jail{}, Ignored: []string{},
			Reason: "fail2ban is not installed on this host",
		})
	}
	return structToMap(r.deps.Fail2Ban.Status(ctx, r.canInstallPackages()))
}

// handleFail2banInstall puts fail2ban on the host.
//
// It reuses the PHP installer for the same reason the Apache one does: that is
// the package-manager wrapper the Agent already has, and it resolves apk, apt or
// dnf once at startup and never takes a package name from a request.
func (r *Registry) handleFail2banInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.Fail2Ban != nil && r.deps.Fail2Ban.Available() {
		// Already there. Reporting that is more useful than installing it
		// again, and idempotence is what makes a retry safe.
		return structToMap(r.deps.Fail2Ban.Status(ctx, false))
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no package manager the panel can install with", nil)
	}

	if err := r.deps.PHPInstaller.InstallPackage(ctx, "fail2ban", reporterFunc(reporter)); err != nil {
		return nil, err
	}

	if r.deps.Fail2Ban == nil {
		return map[string]any{"available": true}, nil
	}
	return structToMap(r.deps.Fail2Ban.Status(ctx, false))
}

// handleFail2banConfigure changes one jail.
func (r *Registry) handleFail2banConfigure(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.fail2banProvider()
	if err != nil {
		return nil, err
	}
	var payload fail2banPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	result, err := provider.Apply(ctx, fail2ban.Change{
		Jail:     payload.Jail,
		Enabled:  payload.Enabled,
		MaxRetry: payload.MaxRetry,
		FindTime: payload.FindTime,
		BanTime:  payload.BanTime,
	})
	if err != nil {
		return nil, fail2banError(err)
	}
	return structToMap(result)
}

// handleFail2banIgnore changes the addresses no jail may ban.
func (r *Registry) handleFail2banIgnore(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.fail2banProvider()
	if err != nil {
		return nil, err
	}
	var payload fail2banPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	ignored, err := provider.SetIgnored(ctx, payload.Ignored)
	if err != nil {
		return nil, fail2banError(err)
	}
	return map[string]any{"ignored": ignored, "count": len(ignored)}, nil
}

// handleFail2banBanned lists what this host is currently blocking.
func (r *Registry) handleFail2banBanned(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.fail2banProvider()
	if err != nil {
		return nil, err
	}
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	banned, err := provider.BannedAddresses(ctx)
	if err != nil {
		return nil, fail2banError(err)
	}
	return map[string]any{"banned": banned, "count": len(banned)}, nil
}

// handleFail2banUnban releases an address.
func (r *Registry) handleFail2banUnban(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.fail2banProvider()
	if err != nil {
		return nil, err
	}
	var payload fail2banPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.Unban(ctx, payload.Jail, payload.Address); err != nil {
		return nil, fail2banError(err)
	}
	return map[string]any{"jail": payload.Jail, "address": payload.Address, "banned": false}, nil
}

// handleFail2banBan bans an address by hand.
func (r *Registry) handleFail2banBan(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.fail2banProvider()
	if err != nil {
		return nil, err
	}
	var payload fail2banPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.Ban(ctx, payload.Jail, payload.Address); err != nil {
		return nil, fail2banError(err)
	}
	return map[string]any{"jail": payload.Jail, "address": payload.Address, "banned": true}, nil
}

// canInstallPackages reports whether the panel could install fail2ban here.
func (r *Registry) canInstallPackages() bool {
	return r.deps.PHPInstaller != nil && r.deps.PHPInstaller.Available()
}

// fail2banError turns a package error into the code the caller should see.
func fail2banError(err error) error {
	switch {
	case errors.Is(err, fail2ban.ErrUnavailable):
		return Fail(protocol.CodeUnsupported, "fail2ban is not installed on this host", err)
	case errors.Is(err, fail2ban.ErrNotRunning):
		return Fail(protocol.CodeInvalidRequest,
			"fail2ban is installed but not running, so nothing is being banned", err)
	case errors.Is(err, fail2ban.ErrUnknownJail):
		return Fail(protocol.CodeNotFound, cleanMessage(err), err)
	case errors.Is(err, fail2ban.ErrNotBanned):
		return Fail(protocol.CodeNotFound, cleanMessage(err), err)
	case errors.Is(err, fail2ban.ErrNotApplied):
		return Fail(protocol.CodeInvalidRequest, cleanMessage(err), err)
	case errors.Is(err, fail2ban.ErrInvalidConfig),
		errors.Is(err, validate.ErrInvalidBanIP),
		errors.Is(err, validate.ErrInvalidIgnoreIP),
		errors.Is(err, validate.ErrInvalidDuration),
		errors.Is(err, validate.ErrInvalidRetries),
		errors.Is(err, validate.ErrInvalidJail):
		return Fail(protocol.CodeInvalidPayload, cleanMessage(err), err)
	default:
		return err
	}
}
