// Package audit records sensitive actions (CLAUDE.md section 15).
//
// Audit rows are append-only: migration 0001 installs triggers that reject
// UPDATE and DELETE on audit_logs, so history cannot be rewritten through the
// API or by anything else holding a database connection.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/shared/logger"
)

// Action names for the events Phase 1 records. Later phases append their own
// (website.create, ssl.issue, and so on) rather than passing raw strings.
const (
	ActionLoginSucceeded  = "auth.login"
	ActionLoginFailed     = "auth.login_failed"
	ActionLoginRateLimit  = "auth.login_rate_limited"
	ActionLogout          = "auth.logout"
	ActionTokenRefreshed  = "auth.refresh"
	ActionRefreshReuse    = "auth.refresh_reuse_detected"
	ActionTwoFactorSetup  = "user.2fa_setup"
	ActionTwoFactorEnable = "user.2fa_enabled"
	ActionTwoFactorFailed = "user.2fa_failed"
	ActionTwoFactorDisabl = "user.2fa_disabled"
	ActionUserCreated     = "user.create"
)

// Outcome values.
const (
	StatusSuccess = "SUCCESS"
	StatusFailure = "FAILURE"
)

// maxUserAgentLength bounds a client-controlled string before storage.
const maxUserAgentLength = 512

// Event is one auditable action.
type Event struct {
	// UserID is empty for actions taken by an unidentified caller, such as a
	// failed login against a username that does not exist.
	UserID       string
	Action       string
	ResourceType string
	ResourceID   string
	IPAddress    string
	UserAgent    string
	Status       string
	// Metadata carries action-specific context. It must never contain
	// passwords, tokens, or secrets.
	Metadata map[string]any
}

// Recorder writes audit events.
type Recorder struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// NewRecorder builds a Recorder.
func NewRecorder(pool *pgxpool.Pool, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, log: log}
}

// Record persists an event.
//
// A failure to write an audit row is logged at error level and returned. It is
// never swallowed: losing an audit trail silently is worse than a failed
// request, and callers decide whether the action itself should still proceed.
func (r *Recorder) Record(ctx context.Context, event Event) error {
	var metadata []byte
	if len(event.Metadata) > 0 {
		encoded, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("encode audit metadata: %w", err)
		}
		metadata = encoded
	}

	status := event.Status
	if status == "" {
		status = StatusSuccess
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO audit_logs
			(user_id, action, resource_type, resource_id, ip_address, user_agent, status, metadata)
		VALUES
			(nullif($1, '')::uuid, $2, nullif($3, ''), nullif($4, '')::uuid, $5::inet, $6, $7, $8)`,
		event.UserID,
		event.Action,
		event.ResourceType,
		event.ResourceID,
		normalizeIP(event.IPAddress),
		truncate(event.UserAgent, maxUserAgentLength),
		status,
		metadata)
	if err != nil {
		r.log.Error("failed to write audit log",
			logger.KeyAction, event.Action,
			logger.KeyError, err.Error(),
		)
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

// RecordAsync writes an event without blocking the caller's response, used on
// paths where the action has already completed and cannot be undone.
//
// The event still reaches the same table and a write failure is still logged;
// only the caller's latency is decoupled.
func (r *Recorder) RecordAsync(ctx context.Context, event Event) {
	// The request context is cancelled as soon as the response is written, so
	// the write runs on a detached context with its own deadline.
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)

	go func() {
		defer cancel()
		if err := r.Record(detached, event); err != nil {
			r.log.Error("async audit write failed",
				logger.KeyAction, event.Action,
				logger.KeyError, err.Error(),
			)
		}
	}()
}

func normalizeIP(raw string) *string {
	if raw == "" {
		return nil
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		raw = host
	}
	if net.ParseIP(raw) == nil {
		return nil
	}
	return &raw
}

func truncate(value string, max int) *string {
	if value == "" {
		return nil
	}
	if len(value) > max {
		value = value[:max]
	}
	return &value
}
