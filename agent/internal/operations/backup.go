package operations

import (
	"context"
	"errors"

	"github.com/jothost/panel/agent/internal/backup"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The backup operations' request boundary.
//
// These payloads are the only ones in the Agent that carry a secret: an S3
// secret key, or the private key for an SFTP destination. They arrive over the
// Unix socket for the length of one operation and are never logged, never
// written to the audit trail, and never stored — the panel keeps them
// encrypted and hands them over each time.
//
// That is also why the panel's job queue does not hold them. A job row carries
// the backup's id; the credentials are resolved and decrypted at dispatch, so a
// dump of the jobs table is not a dump of somebody's storage credentials.
//
// Everything else in a payload is checked before it can become a path: keys by
// validate.BackupKey, document roots against the Agent's own site root,
// destinations by the store that will use them.

// backupProvider returns the provider, or the error a caller should see when
// this host cannot take backups.
func (r *Registry) backupProvider() (*backup.Provider, error) {
	if r.deps.Backup == nil || !r.deps.Backup.Available() {
		return nil, Fail(protocol.CodeUnsupported, backup.ErrUnavailable.Error(), nil)
	}
	return r.deps.Backup, nil
}

// handleBackupCapabilities reports what this host can actually do.
//
// It is answered even when backups are unavailable, because "this host cannot
// take backups, and here is why" is the single most useful thing the panel can
// show on a page whose whole purpose is to promise that data is safe.
func (r *Registry) handleBackupCapabilities(_ context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.Backup == nil {
		return structToMap(backup.Capabilities{Reason: backup.ErrUnavailable.Error()})
	}
	return structToMap(r.deps.Backup.Capabilities())
}

// handleBackupCreate takes a backup and reads it back.
func (r *Registry) handleBackupCreate(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.backupProvider()
	if err != nil {
		return nil, err
	}
	var payload backup.Request
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.BackupType(payload.Type); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	if err := validate.BackupKey(payload.Key); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.Create(ctx, payload, backup.Report(reporterFunc(reporter)))
	if err != nil {
		return nil, backupError(err)
	}
	return structToMap(result)
}

// handleBackupVerify reads a stored backup back and checks it.
func (r *Registry) handleBackupVerify(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.backupProvider()
	if err != nil {
		return nil, err
	}
	var payload backup.VerifyRequest
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.BackupKey(payload.Key); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.Verify(ctx, payload)
	if err != nil {
		return nil, backupError(err)
	}
	return structToMap(result)
}

// handleBackupRestore puts a backup back.
//
// The confirmation CLAUDE.md section 18 requires is the API's: a restore is a
// destructive act, and the person doing it has to say so in the request that
// starts it. By the time it reaches here the decision has been made, and the
// Agent's job is to do it in a way that can be undone if it fails halfway.
func (r *Registry) handleBackupRestore(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.backupProvider()
	if err != nil {
		return nil, err
	}
	var payload backup.RestoreRequest
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.BackupKey(payload.Key); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	result, err := provider.Restore(ctx, payload, backup.Report(reporterFunc(reporter)))
	if err != nil {
		return nil, backupError(err)
	}
	return structToMap(result)
}

// handleBackupDelete removes one archive from its destination.
func (r *Registry) handleBackupDelete(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.backupProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Key         string             `json:"key"`
		Destination backup.Destination `json:"destination"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.BackupKey(payload.Key); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	if err := provider.Delete(ctx, payload.Key, payload.Destination); err != nil {
		return nil, backupError(err)
	}
	return map[string]any{"deleted": true, "key": payload.Key}, nil
}

// handleBackupCheckTarget writes a small object to a destination and reads it
// back, so a destination that cannot work says so while somebody is watching.
func (r *Registry) handleBackupCheckTarget(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.backupProvider()
	if err != nil {
		return nil, err
	}
	var payload struct {
		Destination backup.Destination `json:"destination"`
	}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if err := provider.CheckDestination(ctx, payload.Destination); err != nil {
		// A destination that does not work is a fact about the destination, not
		// a failed operation: the check did its job. It is returned as an error
		// anyway so the panel records the reason against the destination rather
		// than having to interpret a success that means "no".
		return nil, backupError(err)
	}
	return map[string]any{"ok": true}, nil
}

// backupError maps this package's errors onto protocol codes.
func backupError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, backup.ErrUnavailable),
		errors.Is(err, backup.ErrUnsupportedDestination),
		errors.Is(err, backup.ErrPanelUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, backup.ErrNotFound):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, backup.ErrNothingToBackUp),
		errors.Is(err, backup.ErrDestinationFailed),
		errors.Is(err, backup.ErrCorrupt),
		errors.Is(err, backup.ErrArchiveMalformed),
		errors.Is(err, backup.ErrRestoreFailed),
		errors.Is(err, backup.ErrTooLarge),
		errors.Is(err, backup.ErrPanelRestoreOnHost):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, backup.ErrPanelRequest),
		errors.Is(err, backup.ErrSealingKey),
		errors.Is(err, validate.ErrInvalidBackupKey),
		errors.Is(err, validate.ErrInvalidBackupType),
		errors.Is(err, validate.ErrInvalidDestination),
		errors.Is(err, validate.ErrInvalidChecksum):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
