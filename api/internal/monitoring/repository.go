// Package monitoring watches this host and records what it finds.
//
// # Two kinds of alert, and why both exist
//
// The dashboard already computes alerts from the reading in front of it. Those
// answer "what is wrong now" and cannot go stale, because they are derived from
// the same snapshot they are shown beside.
//
// This package answers a different question: "what has been wrong, since when,
// and is it still". Only that one can be notified about (Phase 20), looked back
// at, or acknowledged — and only that one needs rows.
//
// The two must never disagree about *thresholds*, so the dashboard reads its
// thresholds from the rules stored here rather than from constants of its own.
// One definition, two views.
//
// # Sustained breach
//
// A rule fires when its condition has held for its whole duration, not when one
// reading crossed a line. That is the difference between a panel somebody keeps
// and a panel somebody mutes: a backup job briefly filling memory is not an
// incident, and a rule that pages for it will be turned off within a week —
// after which nothing is watching at all.
package monitoring

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrRuleNotFound means no such alert rule.
	ErrRuleNotFound = errors.New("alert rule not found")
	// ErrAlertNotFound means no such alert.
	ErrAlertNotFound = errors.New("alert not found")
	// ErrDuplicateRule means a rule already watches that metric and target at
	// that severity. Two would both fire, and an operator would get two alerts
	// about one problem.
	ErrDuplicateRule = errors.New("a rule already watches that at that severity")
)

// Rule is one condition the panel watches.
type Rule struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Name     string `json:"name"`
	Metric   string `json:"metric"`
	// Target names which instance, for the metrics that have more than one: a
	// mount point for disk, a service key for service. Empty means any.
	Target     string  `json:"target"`
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	// ForSeconds is how long the breach must last. Zero fires on the first
	// reading, which is right for a service being down and wrong for almost
	// everything else.
	ForSeconds int    `json:"for_seconds"`
	Severity   string `json:"severity"`
	Enabled    bool   `json:"enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Alert is a condition that has been true for long enough to matter.
type Alert struct {
	ID       string  `json:"id"`
	ServerID string  `json:"server_id"`
	RuleID   *string `json:"rule_id,omitempty"`

	// Copied from the rule so an alert still describes itself after its rule is
	// edited or deleted. An alert whose threshold changed after the fact would
	// otherwise silently rewrite its own history.
	Metric    string   `json:"metric"`
	Target    string   `json:"target"`
	Severity  string   `json:"severity"`
	Threshold *float64 `json:"threshold,omitempty"`

	Status  string `json:"status"`
	Message string `json:"message"`

	// Value opened it, Worst is the worst seen since, LastValue is the most
	// recent. "It was briefly at 91%" and "it sat at 99% for an hour" are
	// different incidents and a resolved alert should be able to tell them
	// apart.
	Value     *float64 `json:"value,omitempty"`
	Worst     *float64 `json:"worst,omitempty"`
	LastValue *float64 `json:"last_value,omitempty"`

	OpenedAt   time.Time  `json:"opened_at"`
	LastSeenAt time.Time  `json:"last_seen_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`

	AcknowledgedAt *time.Time `json:"acknowledged_at,omitempty"`
	AcknowledgedBy *string    `json:"acknowledged_by,omitempty"`
}

// Alert statuses.
const (
	StatusOpen     = "open"
	StatusResolved = "resolved"
)

// ServiceState is one stretch of time a service spent in one state.
type ServiceState struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`
	Service  string `json:"service"`
	Running  bool   `json:"running"`
	Status   string `json:"status"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Duration returns how long the state lasted, measured to now when it is still
// current.
func (s ServiceState) Duration(now time.Time) time.Duration {
	if s.EndedAt != nil {
		return s.EndedAt.Sub(s.StartedAt)
	}
	return now.Sub(s.StartedAt)
}

// Repository reads and writes monitoring records.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const ruleColumns = `
	id, server_id, name, metric, target, comparison, threshold,
	for_seconds, severity, enabled, created_at, updated_at`

func scanRule(row pgx.Row) (Rule, error) {
	var rule Rule
	err := row.Scan(&rule.ID, &rule.ServerID, &rule.Name, &rule.Metric, &rule.Target,
		&rule.Comparison, &rule.Threshold, &rule.ForSeconds, &rule.Severity,
		&rule.Enabled, &rule.CreatedAt, &rule.UpdatedAt)
	if err != nil {
		return Rule{}, err
	}
	return rule, nil
}

// CreateRule records a rule.
func (r *Repository) CreateRule(ctx context.Context, rule Rule) (Rule, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO alert_rules
			(server_id, name, metric, target, comparison, threshold,
			 for_seconds, severity, enabled)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+ruleColumns,
		rule.ServerID, rule.Name, rule.Metric, rule.Target, rule.Comparison,
		rule.Threshold, rule.ForSeconds, rule.Severity, rule.Enabled)

	created, err := scanRule(row)
	if err != nil {
		return Rule{}, translateConstraint(err)
	}
	return created, nil
}

// UpdateRule changes a rule.
//
// The metric and target cannot change: a rule that watched something else would
// be a different rule, and the alerts it had already opened would then be
// attributed to a condition it never observed.
func (r *Repository) UpdateRule(ctx context.Context, id string, rule Rule) (Rule, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE alert_rules SET
			name        = $2,
			comparison  = $3,
			threshold   = $4,
			for_seconds = $5,
			severity    = $6,
			enabled     = $7,
			updated_at  = now()
		WHERE id = $1::uuid
		RETURNING `+ruleColumns,
		id, rule.Name, rule.Comparison, rule.Threshold, rule.ForSeconds,
		rule.Severity, rule.Enabled)

	updated, err := scanRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rule{}, ErrRuleNotFound
		}
		return Rule{}, translateConstraint(err)
	}
	return updated, nil
}

// GetRule returns one rule.
func (r *Repository) GetRule(ctx context.Context, id string) (Rule, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+ruleColumns+` FROM alert_rules WHERE id = $1::uuid`, id)
	rule, err := scanRule(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Rule{}, ErrRuleNotFound
		}
		return Rule{}, fmt.Errorf("select alert rule: %w", err)
	}
	return rule, nil
}

// ListRules returns a host's rules.
func (r *Repository) ListRules(ctx context.Context, serverID string) ([]Rule, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+ruleColumns+` FROM alert_rules WHERE server_id = $1::uuid
		 ORDER BY metric, target, severity`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()

	rules := make([]Rule, 0, 16)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// DeleteRule removes a rule.
//
// The alerts it opened stay: deleting a rule must not erase the history of what
// it caught, which is why alerts keep their own copy of what they were about.
func (r *Repository) DeleteRule(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM alert_rules WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete alert rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRuleNotFound
	}
	return nil
}

const alertColumns = `
	id, server_id, rule_id::text, metric, target, severity, threshold,
	status, message, value, worst, last_value,
	opened_at, last_seen_at, resolved_at, acknowledged_at, acknowledged_by::text`

func scanAlert(row pgx.Row) (Alert, error) {
	var alert Alert
	err := row.Scan(&alert.ID, &alert.ServerID, &alert.RuleID, &alert.Metric,
		&alert.Target, &alert.Severity, &alert.Threshold, &alert.Status,
		&alert.Message, &alert.Value, &alert.Worst, &alert.LastValue,
		&alert.OpenedAt, &alert.LastSeenAt, &alert.ResolvedAt,
		&alert.AcknowledgedAt, &alert.AcknowledgedBy)
	if err != nil {
		return Alert{}, err
	}
	return alert, nil
}

// OpenAlert opens an alert, or refreshes the one already open for the same
// condition.
//
// One row per thing being watched, which the unique index enforces. Without it
// a flapping disk would open a new alert every evaluation and somebody would
// wake to four hundred rows describing one filesystem.
//
// The worst value is kept with GREATEST rather than overwritten, because a
// resolved alert reading "peaked at 99%" is worth more than one reading
// "was at 81% when it cleared".
func (r *Repository) OpenAlert(ctx context.Context, alert Alert) (Alert, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO alerts
			(server_id, rule_id, metric, target, severity, threshold, status,
			 message, value, worst, last_value, opened_at, last_seen_at)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, 'open',
		        $7, $8, $8, $8, $9, $9)
		ON CONFLICT (server_id, metric, target, severity) WHERE status = 'open'
		DO UPDATE SET
			last_seen_at = EXCLUDED.last_seen_at,
			last_value   = EXCLUDED.last_value,
			worst        = GREATEST(alerts.worst, EXCLUDED.value),
			message      = EXCLUDED.message
		RETURNING `+alertColumns,
		alert.ServerID, valueOrEmpty(alert.RuleID), alert.Metric, alert.Target,
		alert.Severity, alert.Threshold, alert.Message, alert.Value, alert.OpenedAt)

	opened, err := scanAlert(row)
	if err != nil {
		return Alert{}, fmt.Errorf("open alert: %w", err)
	}
	return opened, nil
}

// ResolveAlert closes the open alert for a condition, if there is one.
//
// Resolution is the machine's decision, never a person's: an operator can
// acknowledge an alert, and cannot mark a full disk as fine. A panel where they
// could is a panel that will one day say a full disk is fine.
func (r *Repository) ResolveAlert(ctx context.Context, serverID, metric, target,
	severity string, at time.Time,
) (Alert, bool, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE alerts
		SET status = 'resolved', resolved_at = $5, last_seen_at = $5
		WHERE server_id = $1::uuid AND metric = $2 AND target = $3
		  AND severity = $4 AND status = 'open'
		RETURNING `+alertColumns,
		serverID, metric, target, severity, at)

	alert, err := scanAlert(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Alert{}, false, nil
		}
		return Alert{}, false, fmt.Errorf("resolve alert: %w", err)
	}
	return alert, true, nil
}

// ListAlerts returns a host's alerts, newest first.
func (r *Repository) ListAlerts(ctx context.Context, serverID, status string, limit int) (
	[]Alert, error,
) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	query := `SELECT ` + alertColumns + ` FROM alerts WHERE server_id = $1::uuid`
	args := []any{serverID}
	if status != "" {
		query += ` AND status = $2`
		args = append(args, status)
	}
	query += ` ORDER BY opened_at DESC LIMIT ` + fmt.Sprintf("%d", limit)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()

	alerts := make([]Alert, 0, 32)
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

// AcknowledgeAlert records that somebody has seen an alert and is dealing with
// it.
//
// It does not resolve it. See ResolveAlert.
func (r *Repository) AcknowledgeAlert(ctx context.Context, id, userID string, at time.Time) (
	Alert, error,
) {
	row := r.pool.QueryRow(ctx, `
		UPDATE alerts
		SET acknowledged_at = $2, acknowledged_by = NULLIF($3, '')::uuid
		WHERE id = $1::uuid
		RETURNING `+alertColumns, id, at, userID)

	alert, err := scanAlert(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Alert{}, ErrAlertNotFound
		}
		return Alert{}, fmt.Errorf("acknowledge alert: %w", err)
	}
	return alert, nil
}

// PruneAlerts removes resolved alerts older than cutoff.
func (r *Repository) PruneAlerts(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM alerts WHERE status = 'resolved' AND resolved_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

const serviceStateColumns = `id, server_id, service, running, status, started_at, ended_at`

func scanServiceState(row pgx.Row) (ServiceState, error) {
	var state ServiceState
	err := row.Scan(&state.ID, &state.ServerID, &state.Service, &state.Running,
		&state.Status, &state.StartedAt, &state.EndedAt)
	if err != nil {
		return ServiceState{}, err
	}
	return state, nil
}

// RecordServiceState notes what a service is doing, opening a new stretch only
// when the state actually changed.
//
// Transitions, not samples. A row per service per poll would be tens of
// thousands a day saying "still running", and the question an operator asks —
// "when did it go down, and for how long" — is answered by the changes alone.
//
// Returns the current state and whether this call started it.
func (r *Repository) RecordServiceState(ctx context.Context, serverID, service string,
	running bool, status string, at time.Time,
) (ServiceState, bool, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return ServiceState{}, false, fmt.Errorf("record service state: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanServiceState(tx.QueryRow(ctx,
		`SELECT `+serviceStateColumns+` FROM service_states
		 WHERE server_id = $1::uuid AND service = $2 AND ended_at IS NULL`,
		serverID, service))

	switch {
	case err == nil && current.Running == running && current.Status == status:
		// Unchanged. Nothing to write, which is the common case by a very wide
		// margin and the whole reason this table stays small.
		return current, false, nil
	case err == nil:
		if _, closeErr := tx.Exec(ctx,
			`UPDATE service_states SET ended_at = $2 WHERE id = $1::uuid`,
			current.ID, at); closeErr != nil {
			return ServiceState{}, false, fmt.Errorf("close the previous service state: %w", closeErr)
		}
	case errors.Is(err, pgx.ErrNoRows):
		// First time this service has been seen.
	default:
		return ServiceState{}, false, fmt.Errorf("read the current service state: %w", err)
	}

	opened, err := scanServiceState(tx.QueryRow(ctx, `
		INSERT INTO service_states (server_id, service, running, status, started_at)
		VALUES ($1::uuid, $2, $3, $4, $5)
		RETURNING `+serviceStateColumns,
		serverID, service, running, status, at))
	if err != nil {
		return ServiceState{}, false, fmt.Errorf("open a service state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ServiceState{}, false, fmt.Errorf("record service state: %w", err)
	}
	return opened, true, nil
}

// CurrentServiceStates returns what every watched service is doing now.
func (r *Repository) CurrentServiceStates(ctx context.Context, serverID string) (
	[]ServiceState, error,
) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+serviceStateColumns+` FROM service_states
		 WHERE server_id = $1::uuid AND ended_at IS NULL ORDER BY service`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list current service states: %w", err)
	}
	defer rows.Close()

	states := make([]ServiceState, 0, 16)
	for rows.Next() {
		state, err := scanServiceState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan service state: %w", err)
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

// ServiceHistory returns a service's recent state changes, newest first.
func (r *Repository) ServiceHistory(ctx context.Context, serverID, service string, limit int) (
	[]ServiceState, error,
) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+serviceStateColumns+` FROM service_states
		 WHERE server_id = $1::uuid AND service = $2
		 ORDER BY started_at DESC LIMIT $3`, serverID, service, limit)
	if err != nil {
		return nil, fmt.Errorf("list service history: %w", err)
	}
	defer rows.Close()

	states := make([]ServiceState, 0, 16)
	for rows.Next() {
		state, err := scanServiceState(rows)
		if err != nil {
			return nil, fmt.Errorf("scan service state: %w", err)
		}
		states = append(states, state)
	}
	return states, rows.Err()
}

// PruneServiceStates removes closed stretches that ended before cutoff.
func (r *Repository) PruneServiceStates(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM service_states WHERE ended_at IS NOT NULL AND ended_at < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune service states: %w", err)
	}
	return tag.RowsAffected(), nil
}

// valueOrEmpty renders an optional id for a NULLIF cast.
func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// translateConstraint turns a database constraint into an error the API can
// explain.
func translateConstraint(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "alert_rules_watch_idx"):
		return ErrDuplicateRule
	case strings.Contains(message, "alert_rules_metric_valid"):
		return errors.New("that is not a metric this panel can measure")
	case strings.Contains(message, "alert_rules_for_sane"):
		return errors.New("a rule's duration must be between 0 seconds and a day")
	case strings.Contains(message, "alert_rules_name_present"):
		return errors.New("a rule needs a name somebody will recognise in an alert")
	default:
		return fmt.Errorf("write alert rule: %w", err)
	}
}
