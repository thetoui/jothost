// Package cron is the panel's record of the host's scheduled jobs.
//
// The rows here describe intent; the host's crontab files are what actually
// runs, and the Agent owns those. The relationship is one-way and deliberate:
// after every change, the panel hands the Agent the *complete* set of jobs for
// the affected account and the Agent rewrites its block from that. Nothing
// merges, nothing is incremental, and the file therefore cannot drift into
// saying something the database does not.
package cron

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcomes recorded against a job, matching migration 0012's CHECK.
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("scheduled job not found")
	// ErrDuplicateName means the website already has a job with that name.
	ErrDuplicateName = errors.New("this website already has a job with that name")
)

// Job is one scheduled job.
type Job struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id"`

	Name     string `json:"name"`
	Type     string `json:"job_type"`
	Schedule string `json:"schedule"`
	// Target is what the operator entered: a script path, a URL, or a command.
	Target string `json:"target"`
	// Command is what is written into the crontab. Shown as well as the
	// target, because "what will actually run" is the question an operator has
	// when a job does not do what they expected.
	Command string `json:"command"`
	Enabled bool   `json:"enabled"`

	LastRunAt    *time.Time `json:"last_run_at"`
	LastStatus   *string    `json:"last_status"`
	LastExitCode *int       `json:"last_exit_code"`
	LastDuration *int64     `json:"last_duration_ms"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// WebsiteDomain and SystemUser are joined from the website. The account is
	// what the job runs as, and showing it is how an operator knows the job is
	// not running as root.
	WebsiteDomain string `json:"website_domain,omitempty"`
	SystemUser    string `json:"system_user,omitempty"`

	// NextRunAt is computed from the schedule rather than stored: it is a
	// property of the expression and the clock, and a stored copy would be
	// wrong the moment either changed. Nil when the schedule can never fire.
	NextRunAt *time.Time `json:"next_run_at"`
}

// Repository reads and writes job records.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateParams describe a job to record.
type CreateParams struct {
	ServerID  string
	WebsiteID string
	Name      string
	Type      string
	Schedule  string
	Target    string
	Command   string
	Enabled   bool
}

// Create writes a job row.
func (r *Repository) Create(ctx context.Context, params CreateParams) (Job, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO cron_jobs
			(server_id, website_id, name, job_type, schedule, target, command, enabled)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
		RETURNING `+jobColumns,
		params.ServerID, params.WebsiteID, params.Name, params.Type,
		params.Schedule, params.Target, params.Command, params.Enabled)

	job, err := scanJob(row)
	if err != nil {
		return Job{}, translateConstraint(err)
	}
	return job, nil
}

// UpdateParams describe a change to a job.
//
// The website cannot change: a job runs as that site's account, in that site's
// directory, and moving it elsewhere is a different job. Everything an operator
// would reasonably edit is here.
type UpdateParams struct {
	Name     *string
	Schedule *string
	Target   *string
	Command  *string
	Enabled  *bool
}

// Update applies changes to a job.
func (r *Repository) Update(ctx context.Context, id string, params UpdateParams) (Job, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE cron_jobs SET
			name       = COALESCE($2, name),
			schedule   = COALESCE($3, schedule),
			target     = COALESCE($4, target),
			command    = COALESCE($5, command),
			enabled    = COALESCE($6, enabled),
			updated_at = now()
		WHERE id = $1::uuid
		RETURNING `+jobColumns,
		id, params.Name, params.Schedule, params.Target, params.Command, params.Enabled)

	job, err := scanJob(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, translateConstraint(err)
	}
	return job, nil
}

// RecordRun stores the outcome of a run.
func (r *Repository) RecordRun(ctx context.Context, id, status string,
	exitCode int, durationMS int64,
) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE cron_jobs SET
			last_run_at      = now(),
			last_status      = $2,
			last_exit_code   = $3,
			last_duration_ms = $4,
			updated_at       = now()
		WHERE id = $1::uuid`, id, status, exitCode, durationMS)
	if err != nil {
		return fmt.Errorf("record the job run: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one job.
func (r *Repository) Get(ctx context.Context, id string) (Job, error) {
	row := r.pool.QueryRow(ctx, selectJobs+` WHERE c.id = $1::uuid`, id)

	job, err := scanJobWithJoins(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, fmt.Errorf("select job: %w", err)
	}
	return job, nil
}

// List returns every job, newest first.
func (r *Repository) List(ctx context.Context) ([]Job, error) {
	return r.query(ctx, selectJobs+` ORDER BY c.created_at DESC`)
}

// ListForWebsite returns one website's jobs.
func (r *Repository) ListForWebsite(ctx context.Context, websiteID string) ([]Job, error) {
	return r.query(ctx, selectJobs+` WHERE c.website_id = $1::uuid
		ORDER BY c.created_at DESC`, websiteID)
}

// ListForAccount returns every job that runs as one system account.
//
// This is what the Agent is handed: the complete set for the account whose
// crontab is about to be rewritten. Complete, because the Agent generates the
// file rather than editing it, and a set missing a job would delete that job
// from the host.
func (r *Repository) ListForAccount(ctx context.Context, serverID, account string) ([]Job, error) {
	return r.query(ctx, selectJobs+` WHERE c.server_id = $1::uuid
		AND w.system_username = $2
		ORDER BY c.created_at`, serverID, account)
}

func (r *Repository) query(ctx context.Context, sql string, args ...any) ([]Job, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("select jobs: %w", err)
	}
	defer rows.Close()

	jobs := make([]Job, 0, 8)
	for rows.Next() {
		job, err := scanJobWithJoins(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

// Delete removes a job record.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM cron_jobs WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const jobColumns = `id, server_id, website_id, name, job_type, schedule, target,
	command, enabled, last_run_at, last_status, last_exit_code,
	last_duration_ms, created_at, updated_at`

const selectJobs = `
	SELECT c.id, c.server_id, c.website_id, c.name, c.job_type, c.schedule,
	       c.target, c.command, c.enabled, c.last_run_at, c.last_status,
	       c.last_exit_code, c.last_duration_ms, c.created_at, c.updated_at,
	       w.primary_domain, w.system_username
	FROM cron_jobs c
	JOIN websites w ON w.id = c.website_id`

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.ServerID, &job.WebsiteID, &job.Name, &job.Type,
		&job.Schedule, &job.Target, &job.Command, &job.Enabled,
		&job.LastRunAt, &job.LastStatus, &job.LastExitCode, &job.LastDuration,
		&job.CreatedAt, &job.UpdatedAt)
	return job, err
}

func scanJobWithJoins(row scanner) (Job, error) {
	var job Job
	err := row.Scan(&job.ID, &job.ServerID, &job.WebsiteID, &job.Name, &job.Type,
		&job.Schedule, &job.Target, &job.Command, &job.Enabled,
		&job.LastRunAt, &job.LastStatus, &job.LastExitCode, &job.LastDuration,
		&job.CreatedAt, &job.UpdatedAt, &job.WebsiteDomain, &job.SystemUser)
	return job, err
}

// translateConstraint turns a unique-index violation into the error that says
// which one, so the caller can explain it rather than reporting "conflict".
func translateConstraint(err error) error {
	var pgErr interface {
		SQLState() string
		Error() string
	}
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("write job: %w", err)
	}
	if pgErr.SQLState() == "23505" && strings.Contains(pgErr.Error(), "cron_jobs_name_idx") {
		return ErrDuplicateName
	}
	return fmt.Errorf("write job: %w", err)
}
