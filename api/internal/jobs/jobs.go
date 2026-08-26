// Package jobs is the durable record of long-running infrastructure work.
//
// The Agent runs the operation and tracks it in memory, but that is lost when
// the Agent restarts (docs/PHASE2.md section 3.1). This table is what the
// panel can still answer from, and what a user's browser polls.
//
// ARCHITECTURE.md section 9 shows a Redis queue between the API and the
// worker. The jobs table is used instead: it survives a restart, which a Redis
// list does not, and `FOR UPDATE SKIP LOCKED` gives the same
// claim-one-and-only-one semantics. The trade-off is documented in
// docs/PHASE4.md section 3.2.
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// State is a job's lifecycle state (PRD.md section 11).
type State string

// Job states.
const (
	StatePending   State = "PENDING"
	StateRunning   State = "RUNNING"
	StateSuccess   State = "SUCCESS"
	StateFailed    State = "FAILED"
	StateCancelled State = "CANCELLED"
)

// Terminal reports whether a job has finished and will not change again.
func (s State) Terminal() bool {
	switch s {
	case StateSuccess, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

// Type names the work a job performs. They mirror the Agent's operation names
// so a job and the audit record it produces line up.
const (
	TypeWebsiteCreate = "website.create"
	TypeWebsiteDelete = "website.delete"
	TypeWebsiteUpdate = "website.update"
)

// Job is one unit of durable work.
type Job struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Status   State          `json:"status"`
	Payload  map[string]any `json:"payload,omitempty"`
	Result   map[string]any `json:"result,omitempty"`
	Error    *string        `json:"error,omitempty"`
	Progress int            `json:"progress"`
	Message  *string        `json:"message,omitempty"`

	CreatedBy    *string `json:"created_by"`
	ResourceType *string `json:"resource_type"`
	ResourceID   *string `json:"resource_id"`

	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("job not found")
	ErrTerminal = errors.New("job has already finished")
)

// Repository reads and writes jobs.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectColumns = `
	id::text, type, status, payload, result, error, progress, message,
	created_by::text, resource_type, resource_id::text,
	created_at, started_at, completed_at`

func scanJob(row pgx.Row) (Job, error) {
	var (
		job     Job
		payload []byte
		result  []byte
	)
	err := row.Scan(&job.ID, &job.Type, &job.Status, &payload, &result,
		&job.Error, &job.Progress, &job.Message,
		&job.CreatedBy, &job.ResourceType, &job.ResourceID,
		&job.CreatedAt, &job.StartedAt, &job.CompletedAt)
	if err != nil {
		return Job{}, err
	}

	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &job.Payload); err != nil {
			return Job{}, fmt.Errorf("decode job payload: %w", err)
		}
	}
	if len(result) > 0 {
		if err := json.Unmarshal(result, &job.Result); err != nil {
			return Job{}, fmt.Errorf("decode job result: %w", err)
		}
	}
	return job, nil
}

// CreateParams describes work to queue.
type CreateParams struct {
	Type    string
	Payload map[string]any
	// CreatedBy is the user who asked for it, for the audit trail.
	CreatedBy    string
	ResourceType string
	ResourceID   string
}

// Create queues a job.
func (r *Repository) Create(ctx context.Context, params CreateParams) (Job, error) {
	var payload []byte
	if len(params.Payload) > 0 {
		encoded, err := json.Marshal(params.Payload)
		if err != nil {
			return Job{}, fmt.Errorf("encode job payload: %w", err)
		}
		payload = encoded
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO jobs (type, status, payload, created_by, resource_type, resource_id)
		VALUES ($1, 'PENDING', $2, nullif($3, '')::uuid, nullif($4, ''), nullif($5, '')::uuid)
		RETURNING `+selectColumns,
		params.Type, payload, params.CreatedBy, params.ResourceType, params.ResourceID)

	job, err := scanJob(row)
	if err != nil {
		return Job{}, fmt.Errorf("create job: %w", err)
	}
	return job, nil
}

// Get returns a job by id.
func (r *Repository) Get(ctx context.Context, id string) (Job, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+selectColumns+` FROM jobs WHERE id = $1::uuid`, id)

	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("select job: %w", err)
	}
	return job, nil
}

// ListParams filters a job listing.
type ListParams struct {
	Status       State
	ResourceType string
	ResourceID   string
	Limit        int
}

// DefaultListLimit bounds a job listing.
const DefaultListLimit = 50

// MaxListLimit is the largest page a caller may request.
const MaxListLimit = 200

// List returns jobs newest first.
func (r *Repository) List(ctx context.Context, params ListParams) ([]Job, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+selectColumns+`
		FROM jobs
		WHERE ($1 = '' OR status = $1)
		  AND ($2 = '' OR resource_type = $2)
		  AND ($3 = '' OR resource_id = nullif($3, '')::uuid)
		ORDER BY created_at DESC
		LIMIT $4`,
		string(params.Status), params.ResourceType, params.ResourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("select jobs: %w", err)
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate jobs: %w", err)
	}
	return jobs, nil
}

// Claim takes the oldest pending job and marks it running.
//
// SKIP LOCKED is what makes this safe with more than one worker: a row another
// worker has locked is passed over rather than waited on, so two workers never
// run the same job and neither blocks.
func (r *Repository) Claim(ctx context.Context) (Job, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE jobs
		SET status = 'RUNNING', started_at = now()
		WHERE id = (
			SELECT id FROM jobs
			WHERE status = 'PENDING'
			ORDER BY created_at
			FOR UPDATE SKIP LOCKED
			LIMIT 1
		)
		RETURNING `+selectColumns)

	job, err := scanJob(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, fmt.Errorf("claim job: %w", err)
	}
	return job, nil
}

// Progress records how far a running job has got.
func (r *Repository) Progress(ctx context.Context, id string, percent int, message string) error {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	// The status guard stops a late progress report from resurrecting a job
	// that has already finished or been cancelled.
	_, err := r.pool.Exec(ctx, `
		UPDATE jobs SET progress = $2, message = nullif($3, '')
		WHERE id = $1::uuid AND status = 'RUNNING'`, id, percent, message)
	if err != nil {
		return fmt.Errorf("update job progress: %w", err)
	}
	return nil
}

// Complete marks a job successful.
func (r *Repository) Complete(ctx context.Context, id string, result map[string]any) error {
	var encoded []byte
	if len(result) > 0 {
		payload, err := json.Marshal(result)
		if err != nil {
			return fmt.Errorf("encode job result: %w", err)
		}
		encoded = payload
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'SUCCESS', progress = 100, result = $2, completed_at = now()
		WHERE id = $1::uuid`, id, encoded)
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	return nil
}

// Fail marks a job failed.
//
// The reason is stored and shown to the user, so callers must pass something
// safe to display rather than an internal error string.
func (r *Repository) Fail(ctx context.Context, id, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE jobs SET status = 'FAILED', error = $2, completed_at = now()
		WHERE id = $1::uuid`, id, reason)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	return nil
}

// Cancel marks a pending job cancelled.
//
// Only pending jobs can be cancelled here: work already dispatched to the
// Agent is running on the host, and marking the record cancelled while the
// host keeps going would make the panel lie about what happened.
func (r *Repository) Cancel(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE jobs SET status = 'CANCELLED', completed_at = now()
		WHERE id = $1::uuid AND status = 'PENDING'`, id)
	if err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the job does not exist or it is past cancelling; the caller
		// distinguishes them by fetching it.
		job, err := r.Get(ctx, id)
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: %s is %s", ErrTerminal, id, job.Status)
	}
	return nil
}

// RequeueStale returns jobs left RUNNING by a crashed worker to PENDING.
//
// A job whose worker died is otherwise stuck forever: nothing is executing it
// and nothing will claim it again. Running this at startup is what makes an
// API restart recover rather than strand work.
func (r *Repository) RequeueStale(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'PENDING', started_at = NULL, progress = 0,
		    message = 'Restarted after an interrupted run'
		WHERE status = 'RUNNING' AND started_at < now() - $1::interval`, olderThan)
	if err != nil {
		return 0, fmt.Errorf("requeue stale jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}
