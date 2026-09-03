// Package security is the panel's record of what is wrong with this host.
//
// # What a scan is, and what it is not
//
// A scan asks seven questions and writes down the answers. Five of them are put
// to the phases that already manage the thing being asked about — the SSH
// configuration, the firewall, the certificates, the outstanding updates, the
// intrusion prevention — because a second probe of the same thing is a second
// answer that can disagree with the first. Two are put to the Agent, because
// nothing else in this panel enumerates listening sockets or walks a site's
// files.
//
// # The rule the score turns on
//
// **An unknown is not a pass.** A scanner that could not run contributes
// neither a finding nor a clean bill of health, and the number of checks that
// actually answered travels with the score everywhere it goes. TASKS.md warns
// about exactly this in the dependency note for the phase: a score computed
// from blanks is worse than no score, because it is reassuring.
//
// The same rule governs resolution. A scanner that could not run leaves its
// existing findings alone — an unreadable firewall must never resolve "the
// firewall is disabled". Only a scanner that ran and did not find something is
// allowed to say it has gone.
//
// # Accepting is not resolving
//
// Somebody can record that a risk is known and deliberate, with a reason, which
// stops it being reported as outstanding. It does not delete it and it does not
// hide it: the accepted count sits next to the score forever. And a finding
// whose severity rises above what was accepted is re-opened, because accepting
// "SSH listens on 22" is not accepting "SSH permits root login with a
// password".
package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNotFound means no such finding or scan.
	ErrNotFound = errors.New("not found")
	// ErrNeverScanned means this host has never been looked at, which is a
	// different fact from having nothing wrong with it.
	ErrNeverScanned = errors.New("this host has not been scanned yet")
)

// Finding statuses.
const (
	StatusOpen     = "open"
	StatusAccepted = "accepted"
	StatusResolved = "resolved"
)

// Finding is one thing wrong with the host.
type Finding struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`

	Scanner     string `json:"scanner"`
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Remediation is what to do about it. A finding without a next step is a
	// nag, and a page full of nags is one nobody opens twice.
	Remediation string `json:"remediation"`
	Fingerprint string `json:"fingerprint"`

	Status   string         `json:"status"`
	Metadata map[string]any `json:"metadata,omitempty"`

	// FirstSeenAt is how long this host has been wrong about this, which is
	// usually the sentence that gets something fixed.
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	ResolvedAt  *time.Time `json:"resolved_at"`

	AcceptedAt       *time.Time `json:"accepted_at"`
	AcceptedBy       *string    `json:"accepted_by"`
	AcceptedReason   *string    `json:"accepted_reason"`
	AcceptedSeverity *string    `json:"accepted_severity"`

	CreatedAt time.Time `json:"created_at"`
}

// Scan is one run of the scanners, with the score it produced.
type Scan struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`

	Score int `json:"score"`
	// ChecksRun and ChecksTotal are what makes the score readable. A 100 from
	// one check that ran is not a 100 from seven.
	ChecksRun   int `json:"checks_run"`
	ChecksTotal int `json:"checks_total"`

	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
	Accepted int `json:"accepted"`
	Resolved int `json:"resolved"`

	Scanners []ScannerOutcome `json:"scanners"`

	DurationMS  int       `json:"duration_ms"`
	TriggeredBy *string   `json:"triggered_by"`
	CreatedAt   time.Time `json:"created_at"`
}

// ScannerOutcome records what one scanner did.
type ScannerOutcome struct {
	Scanner string `json:"scanner"`
	// Ran is false when the scanner could not answer. Its findings are then
	// left alone rather than resolved, and it counts towards neither a pass
	// nor a failure in the score.
	Ran      bool   `json:"ran"`
	Reason   string `json:"reason,omitempty"`
	Findings int    `json:"findings"`
	Duration int    `json:"duration_ms"`
}

// Repository reads and writes the security tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const findingColumns = `
	f.id::text, f.server_id::text, f.scanner, f.severity, f.category, f.title,
	COALESCE(f.description, ''), COALESCE(f.remediation, ''), f.fingerprint,
	f.status, f.metadata,
	f.first_seen_at, f.last_seen_at, f.resolved_at,
	f.accepted_at, f.accepted_by::text, f.accepted_reason, f.accepted_severity,
	f.created_at`

func scanFinding(row pgx.Row) (Finding, error) {
	var (
		finding  Finding
		metadata []byte
	)
	err := row.Scan(&finding.ID, &finding.ServerID, &finding.Scanner, &finding.Severity,
		&finding.Category, &finding.Title, &finding.Description, &finding.Remediation,
		&finding.Fingerprint, &finding.Status, &metadata,
		&finding.FirstSeenAt, &finding.LastSeenAt, &finding.ResolvedAt,
		&finding.AcceptedAt, &finding.AcceptedBy, &finding.AcceptedReason,
		&finding.AcceptedSeverity, &finding.CreatedAt)
	if err != nil {
		return Finding{}, err
	}
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &finding.Metadata); err != nil {
			return Finding{}, fmt.Errorf("decode finding metadata: %w", err)
		}
	}
	return finding, nil
}

// Observation is a finding as a scanner reports it, before the panel knows
// whether it is new.
type Observation struct {
	Scanner     string
	Severity    string
	Category    string
	Title       string
	Description string
	Remediation string
	Fingerprint string
	Metadata    map[string]any
}

// Record writes one observation, keeping what is already known about it.
//
// The insert keeps first_seen_at on conflict, which is the whole reason the
// fingerprint exists: "this host has permitted password logins since March" is
// a far more useful sentence than "there is a finding", and it is the one that
// gets things fixed.
//
// A finding that was accepted and has since become *worse* is re-opened. That
// is what makes accepting safe to offer: somebody accepting "SSH listens on
// port 22" has not accepted "SSH permits root login with a password", and a
// scanner that silently kept the acceptance across a severity increase would
// turn one considered decision into permanent blindness.
func (r *Repository) Record(ctx context.Context, serverID string, observation Observation,
	now time.Time,
) (Finding, error) {
	var metadata []byte
	if len(observation.Metadata) > 0 {
		encoded, err := json.Marshal(observation.Metadata)
		if err != nil {
			return Finding{}, fmt.Errorf("encode finding metadata: %w", err)
		}
		metadata = encoded
	}

	row := r.pool.QueryRow(ctx, `
		WITH upserted AS (
			INSERT INTO security_findings (
				server_id, scanner, severity, category, title, description,
				remediation, fingerprint, status, metadata,
				first_seen_at, last_seen_at
			) VALUES (
				$1::uuid, $2, $3, $4, $5, nullif($6, ''), nullif($7, ''), $8,
				'open', $9::jsonb, $10, $10
			)
			ON CONFLICT (server_id, fingerprint) WHERE status <> 'resolved'
			DO UPDATE SET
				severity = EXCLUDED.severity,
				category = EXCLUDED.category,
				title = EXCLUDED.title,
				description = EXCLUDED.description,
				remediation = EXCLUDED.remediation,
				metadata = EXCLUDED.metadata,
				last_seen_at = EXCLUDED.last_seen_at,
				-- An accepted finding that has got worse goes back to open, and
				-- the acceptance is cleared: it was a decision about a smaller
				-- problem than the one now present.
				status = CASE
					WHEN security_findings.status = 'accepted'
					     AND severity_rank(EXCLUDED.severity)
					         < severity_rank(security_findings.accepted_severity)
					THEN 'open'
					ELSE security_findings.status
				END,
				accepted_at = CASE
					WHEN security_findings.status = 'accepted'
					     AND severity_rank(EXCLUDED.severity)
					         < severity_rank(security_findings.accepted_severity)
					THEN NULL ELSE security_findings.accepted_at
				END,
				accepted_by = CASE
					WHEN security_findings.status = 'accepted'
					     AND severity_rank(EXCLUDED.severity)
					         < severity_rank(security_findings.accepted_severity)
					THEN NULL ELSE security_findings.accepted_by
				END,
				accepted_reason = CASE
					WHEN security_findings.status = 'accepted'
					     AND severity_rank(EXCLUDED.severity)
					         < severity_rank(security_findings.accepted_severity)
					THEN NULL ELSE security_findings.accepted_reason
				END,
				accepted_severity = CASE
					WHEN security_findings.status = 'accepted'
					     AND severity_rank(EXCLUDED.severity)
					         < severity_rank(security_findings.accepted_severity)
					THEN NULL ELSE security_findings.accepted_severity
				END
			RETURNING *
		)
		SELECT `+findingColumns+` FROM upserted f`,
		serverID, observation.Scanner, observation.Severity, observation.Category,
		observation.Title, observation.Description, observation.Remediation,
		observation.Fingerprint, metadata, now)

	finding, err := scanFinding(row)
	if err != nil {
		return Finding{}, fmt.Errorf("record finding: %w", err)
	}
	return finding, nil
}

// ResolveMissing closes the findings of one scanner that it no longer reports.
//
// It is called only for scanners that actually ran. A scanner that could not
// answer leaves its findings exactly as they were: an unreadable firewall must
// never resolve "the firewall is disabled", and a panel that let it would
// report a host as clean at the moment it stopped being able to check.
func (r *Repository) ResolveMissing(ctx context.Context, serverID, scanner string,
	seen []string, now time.Time,
) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE security_findings SET status = 'resolved', resolved_at = $4
		WHERE server_id = $1::uuid
		  AND scanner = $2
		  AND status <> 'resolved'
		  AND NOT (fingerprint = ANY($3::text[]))`,
		serverID, scanner, seen, now)
	if err != nil {
		return 0, fmt.Errorf("resolve missing findings: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Accept records that a risk is known and deliberate.
func (r *Repository) Accept(ctx context.Context, id, userID, reason string,
	now time.Time,
) (Finding, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE security_findings SET
				status = 'accepted',
				accepted_at = $4,
				accepted_by = nullif($2, '')::uuid,
				accepted_reason = $3,
				-- The severity at the moment of acceptance, so a finding that
				-- later gets worse can be told apart from one that has not.
				accepted_severity = severity
			WHERE id = $1::uuid AND status = 'open'
			RETURNING *
		)
		SELECT `+findingColumns+` FROM updated f`, id, userID, reason, now)

	finding, err := scanFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, fmt.Errorf("accept finding: %w", err)
	}
	return finding, nil
}

// Reopen withdraws an acceptance.
func (r *Repository) Reopen(ctx context.Context, id string) (Finding, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE security_findings SET
				status = 'open',
				accepted_at = NULL, accepted_by = NULL,
				accepted_reason = NULL, accepted_severity = NULL
			WHERE id = $1::uuid AND status = 'accepted'
			RETURNING *
		)
		SELECT `+findingColumns+` FROM updated f`, id)

	finding, err := scanFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, fmt.Errorf("reopen finding: %w", err)
	}
	return finding, nil
}

// Get returns one finding.
func (r *Repository) Get(ctx context.Context, id string) (Finding, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+findingColumns+` FROM security_findings f WHERE f.id = $1::uuid`, id)

	finding, err := scanFinding(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Finding{}, ErrNotFound
	}
	if err != nil {
		return Finding{}, fmt.Errorf("select finding: %w", err)
	}
	return finding, nil
}

// ListParams filters a findings listing.
type ListParams struct {
	ServerID string
	Status   string
	Scanner  string
	Severity string
	Limit    int
}

// Listing bounds.
const (
	DefaultFindingLimit = 200
	MaxFindingLimit     = 500
)

// List returns findings, worst first.
//
// Ordered by severity and then by how long the host has been wrong about it,
// because the page's job is to say what to fix first — and between two equally
// severe things, the one that has been there longest is the one nobody is
// dealing with.
func (r *Repository) List(ctx context.Context, params ListParams) ([]Finding, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = DefaultFindingLimit
	}
	if limit > MaxFindingLimit {
		limit = MaxFindingLimit
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+findingColumns+`
		FROM security_findings f
		WHERE f.server_id = $1::uuid
		  AND ($2 = '' OR f.status = $2)
		  AND ($3 = '' OR f.scanner = $3)
		  AND ($4 = '' OR f.severity = $4)
		ORDER BY severity_rank(f.severity), f.first_seen_at
		LIMIT $5`,
		params.ServerID, params.Status, params.Scanner, params.Severity, limit)
	if err != nil {
		return nil, fmt.Errorf("select findings: %w", err)
	}
	defer rows.Close()

	findings := []Finding{}
	for rows.Next() {
		finding, err := scanFinding(rows)
		if err != nil {
			return nil, fmt.Errorf("scan finding: %w", err)
		}
		findings = append(findings, finding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate findings: %w", err)
	}
	return findings, nil
}

// Counts summarises a server's live findings.
type Counts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
	// Open is everything not accepted and not resolved.
	Open int `json:"open"`
	// Accepted is never hidden. It sits next to the score forever, because a
	// score carried by accepted risk is a different thing from a clean one.
	Accepted int `json:"accepted"`
}

// Counts returns the live finding counts for a server.
func (r *Repository) Counts(ctx context.Context, serverID string) (Counts, error) {
	var counts Counts
	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE severity = 'critical' AND status = 'open'),
			count(*) FILTER (WHERE severity = 'high' AND status = 'open'),
			count(*) FILTER (WHERE severity = 'medium' AND status = 'open'),
			count(*) FILTER (WHERE severity = 'low' AND status = 'open'),
			count(*) FILTER (WHERE severity = 'info' AND status = 'open'),
			count(*) FILTER (WHERE status = 'open'),
			count(*) FILTER (WHERE status = 'accepted')
		FROM security_findings WHERE server_id = $1::uuid`, serverID).
		Scan(&counts.Critical, &counts.High, &counts.Medium, &counts.Low,
			&counts.Info, &counts.Open, &counts.Accepted)
	if err != nil {
		return Counts{}, fmt.Errorf("count findings: %w", err)
	}
	return counts, nil
}

// ------------------------------------------------------------------ scans

const scanColumns = `
	s.id::text, s.server_id::text, s.score, s.checks_run, s.checks_total,
	s.critical, s.high, s.medium, s.low, s.info, s.accepted, s.resolved,
	s.scanners, s.duration_ms, s.triggered_by::text, s.created_at`

func scanScan(row pgx.Row) (Scan, error) {
	var (
		scan     Scan
		scanners []byte
	)
	err := row.Scan(&scan.ID, &scan.ServerID, &scan.Score, &scan.ChecksRun,
		&scan.ChecksTotal, &scan.Critical, &scan.High, &scan.Medium, &scan.Low,
		&scan.Info, &scan.Accepted, &scan.Resolved, &scanners, &scan.DurationMS,
		&scan.TriggeredBy, &scan.CreatedAt)
	if err != nil {
		return Scan{}, err
	}
	if len(scanners) > 0 {
		if err := json.Unmarshal(scanners, &scan.Scanners); err != nil {
			return Scan{}, fmt.Errorf("decode scanner outcomes: %w", err)
		}
	}
	if scan.Scanners == nil {
		scan.Scanners = []ScannerOutcome{}
	}
	return scan, nil
}

// RecordScan stores the outcome of one run.
func (r *Repository) RecordScan(ctx context.Context, scan Scan) (Scan, error) {
	scanners, err := json.Marshal(scan.Scanners)
	if err != nil {
		return Scan{}, fmt.Errorf("encode scanner outcomes: %w", err)
	}

	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO security_scans (
				server_id, score, checks_run, checks_total,
				critical, high, medium, low, info, accepted, resolved,
				scanners, duration_ms, triggered_by
			) VALUES (
				$1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
				$12::jsonb, $13, nullif($14, '')::uuid
			)
			RETURNING *
		)
		SELECT `+scanColumns+` FROM inserted s`,
		scan.ServerID, scan.Score, scan.ChecksRun, scan.ChecksTotal,
		scan.Critical, scan.High, scan.Medium, scan.Low, scan.Info,
		scan.Accepted, scan.Resolved, scanners, scan.DurationMS,
		stringOrEmpty(scan.TriggeredBy))

	stored, err := scanScan(row)
	if err != nil {
		return Scan{}, fmt.Errorf("record scan: %w", err)
	}
	return stored, nil
}

// LatestScan returns the most recent scan, or ErrNeverScanned.
//
// The distinct error matters: a host with no findings and a host that has never
// been looked at are opposite facts that a findings table alone renders
// identically, as no rows.
func (r *Repository) LatestScan(ctx context.Context, serverID string) (Scan, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+scanColumns+`
		FROM security_scans s
		WHERE s.server_id = $1::uuid
		ORDER BY s.created_at DESC LIMIT 1`, serverID)

	scan, err := scanScan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scan{}, ErrNeverScanned
	}
	if err != nil {
		return Scan{}, fmt.Errorf("select latest scan: %w", err)
	}
	return scan, nil
}

// ScanHistory returns recent scans, newest first.
func (r *Repository) ScanHistory(ctx context.Context, serverID string, limit int) (
	[]Scan, error,
) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+scanColumns+`
		FROM security_scans s
		WHERE s.server_id = $1::uuid
		ORDER BY s.created_at DESC
		LIMIT $2`, serverID, limit)
	if err != nil {
		return nil, fmt.Errorf("select scan history: %w", err)
	}
	defer rows.Close()

	scans := []Scan{}
	for rows.Next() {
		scan, err := scanScan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		scans = append(scans, scan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scans: %w", err)
	}
	return scans, nil
}

// PruneScans removes scan history older than the retention window.
//
// The findings are not pruned with them. A finding is current state and goes
// when it is resolved; a scan is a reading and there is no reason to keep a
// year of them.
func (r *Repository) PruneScans(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM security_scans WHERE created_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("prune scans: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PruneResolved removes long-resolved findings.
func (r *Repository) PruneResolved(ctx context.Context, olderThan time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM security_findings
		 WHERE status = 'resolved' AND resolved_at < $1`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("prune resolved findings: %w", err)
	}
	return tag.RowsAffected(), nil
}

func stringOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
