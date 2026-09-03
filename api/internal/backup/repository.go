// Package backup is the panel's record of what has been backed up, where it
// went, and whether it is still there.
//
// # What a row here claims
//
// A row with status "completed" claims four things: the archive exists at its
// destination, it is this many bytes, it hashes to this, and the panel read it
// back and got the same answer. The last is what separates this from a log:
// "the upload returned success" and "the bytes are there and correct" are
// different statements, and only the second is a backup.
//
// That is why verified_at is its own column rather than part of status. A
// backup that completed and could not be confirmed is a real state somebody
// needs to see, and collapsing it into either "completed" or "failed" would
// hide it.
//
// # Why the details are copied
//
// A backup row carries the website's domain, the destination's name, and the
// manifest, rather than only referencing them. The references are all ON DELETE
// SET NULL, because the moment somebody most needs last night's copy of a site
// is immediately after deleting the site — and a backup that can no longer say
// what it is a backup of is unrestorable in practice.
//
// # Where the credentials are not
//
// Nowhere in this package. A destination's secret lives encrypted in one column
// and is decrypted at the moment it is handed to the Agent; it is never in a job
// payload, never in an audit record, and never in anything this package returns
// to a handler.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNotFound means no such backup, destination or schedule.
	ErrNotFound = errors.New("not found")
	// ErrDestinationInUse means a destination still has schedules pointing at
	// it. Deleting it would leave them unable to run, so it is refused.
	ErrDestinationInUse = errors.New("that destination is still used by a schedule")
	// ErrNameTaken means a destination of that name already exists.
	ErrNameTaken = errors.New("a destination with that name already exists")
)

// Backup statuses.
const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusDeleting  = "deleting"
)

// Destination is where backups are written.
//
// Credentials is deliberately absent: this struct is what handlers serialise,
// and a field that is only sometimes cleared is a field that will one day be
// returned. The secret is read by its own method, at the one call site that
// needs it.
type Destination struct {
	ID       string         `json:"id"`
	ServerID string         `json:"server_id"`
	Name     string         `json:"name"`
	Kind     string         `json:"kind"`
	Config   map[string]any `json:"config"`

	// HasCredentials says whether a secret is stored, without saying anything
	// about it. A page needs to show that an S3 destination is configured; it
	// never needs the key.
	HasCredentials bool `json:"has_credentials"`

	LastCheckAt     *time.Time `json:"last_check_at"`
	LastCheckOK     *bool      `json:"last_check_ok"`
	LastCheckDetail *string    `json:"last_check_detail"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Backup is one archive the panel has taken.
type Backup struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`

	WebsiteID  *string `json:"website_id"`
	DatabaseID *string `json:"database_id"`
	Subject    string  `json:"subject"`
	Type       string  `json:"type"`

	DestinationID   *string `json:"destination_id"`
	DestinationName string  `json:"destination"`

	Path     *string `json:"path"`
	Size     *int64  `json:"size_bytes"`
	Checksum *string `json:"checksum"`

	Status       string     `json:"status"`
	VerifiedAt   *time.Time `json:"verified_at"`
	VerifyDetail *string    `json:"verify_detail"`

	Manifest map[string]any `json:"manifest,omitempty"`
	Error    *string        `json:"error"`

	JobID      *string `json:"job_id"`
	ScheduleID *string `json:"schedule_id"`
	CreatedBy  *string `json:"created_by"`

	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Restorable reports whether this backup is one a restore could use.
//
// Unverified is deliberately not restorable through the ordinary path. A panel
// that offers to restore an archive it could not read back is offering
// something it has no reason to believe will work, at the moment somebody can
// least afford to find out.
func (b Backup) Restorable() bool {
	return b.Status == StatusCompleted && b.VerifiedAt != nil &&
		b.Path != nil && b.DestinationID != nil
}

// Schedule is a standing instruction to take a backup.
type Schedule struct {
	ID       string `json:"id"`
	ServerID string `json:"server_id"`

	Name       string  `json:"name"`
	Type       string  `json:"type"`
	WebsiteID  *string `json:"website_id"`
	DatabaseID *string `json:"database_id"`

	DestinationID   string `json:"destination_id"`
	DestinationName string `json:"destination_name"`

	Hour      int `json:"hour"`
	Minute    int `json:"minute"`
	DayOfWeek int `json:"day_of_week"`

	RetentionDays int `json:"retention_days"`
	KeepLast      int `json:"keep_last"`

	Enabled bool `json:"enabled"`

	LastRunAt    *time.Time `json:"last_run_at"`
	LastStatus   *string    `json:"last_status"`
	LastBackupID *string    `json:"last_backup_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Repository reads and writes the backup tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// ------------------------------------------------------------ destinations

const destinationColumns = `
	d.id::text, d.server_id::text, d.name, d.kind, d.config,
	(d.credentials_encrypted IS NOT NULL) AS has_credentials,
	d.last_check_at, d.last_check_ok, d.last_check_detail,
	d.created_at, d.updated_at`

func scanDestination(row pgx.Row) (Destination, error) {
	var (
		dest   Destination
		config []byte
	)
	err := row.Scan(&dest.ID, &dest.ServerID, &dest.Name, &dest.Kind, &config,
		&dest.HasCredentials,
		&dest.LastCheckAt, &dest.LastCheckOK, &dest.LastCheckDetail,
		&dest.CreatedAt, &dest.UpdatedAt)
	if err != nil {
		return Destination{}, err
	}
	if len(config) > 0 {
		if err := json.Unmarshal(config, &dest.Config); err != nil {
			return Destination{}, fmt.Errorf("decode destination config: %w", err)
		}
	}
	if dest.Config == nil {
		dest.Config = map[string]any{}
	}
	return dest, nil
}

// CreateDestinationParams describes a new destination.
type CreateDestinationParams struct {
	ServerID string
	Name     string
	Kind     string
	Config   map[string]any
	// Credentials is the already-encrypted secret, or empty for a local
	// destination. This repository never sees a plaintext credential.
	Credentials string
}

// CreateDestination stores a destination.
func (r *Repository) CreateDestination(ctx context.Context, params CreateDestinationParams) (
	Destination, error,
) {
	config, err := json.Marshal(params.Config)
	if err != nil {
		return Destination{}, fmt.Errorf("encode destination config: %w", err)
	}

	// The insert is wrapped in a CTE so the returned row can be selected under
	// the same alias the shared column list uses. RETURNING has no alias, and a
	// second copy of the column list without one is a second thing to keep in
	// step.
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO backup_destinations
				(server_id, name, kind, config, credentials_encrypted)
			VALUES ($1::uuid, $2, $3, $4::jsonb, nullif($5, ''))
			RETURNING *
		)
		SELECT `+destinationColumns+` FROM inserted d`,
		params.ServerID, params.Name, params.Kind, config, params.Credentials)

	dest, err := scanDestination(row)
	if err != nil {
		if isUniqueViolation(err) {
			return Destination{}, ErrNameTaken
		}
		return Destination{}, fmt.Errorf("create destination: %w", err)
	}
	return dest, nil
}

// ListDestinations returns every destination on a server, oldest first.
func (r *Repository) ListDestinations(ctx context.Context, serverID string) ([]Destination, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+destinationColumns+`
		FROM backup_destinations d
		WHERE d.server_id = $1::uuid
		ORDER BY d.created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("select destinations: %w", err)
	}
	defer rows.Close()

	destinations := []Destination{}
	for rows.Next() {
		dest, err := scanDestination(rows)
		if err != nil {
			return nil, fmt.Errorf("scan destination: %w", err)
		}
		destinations = append(destinations, dest)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate destinations: %w", err)
	}
	return destinations, nil
}

// GetDestination returns one destination.
func (r *Repository) GetDestination(ctx context.Context, id string) (Destination, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+destinationColumns+`
		FROM backup_destinations d WHERE d.id = $1::uuid`, id)

	dest, err := scanDestination(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Destination{}, ErrNotFound
	}
	if err != nil {
		return Destination{}, fmt.Errorf("select destination: %w", err)
	}
	return dest, nil
}

// DestinationSecret returns the encrypted credential for a destination.
//
// A separate query rather than a field on Destination, so that reading a
// destination for display and reading it to use are different acts with
// different call sites. A secret that comes back with every listing is a secret
// that ends up in a log.
func (r *Repository) DestinationSecret(ctx context.Context, id string) (string, error) {
	var secret *string
	err := r.pool.QueryRow(ctx,
		`SELECT credentials_encrypted FROM backup_destinations WHERE id = $1::uuid`, id).
		Scan(&secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("select destination credentials: %w", err)
	}
	if secret == nil {
		return "", nil
	}
	return *secret, nil
}

// UpdateDestinationParams describes a change to a destination.
//
// The kind cannot be changed. A destination that became a different kind would
// keep the backups that point at it while describing somewhere they are not, so
// changing kind means making a new destination.
type UpdateDestinationParams struct {
	Name   *string
	Config map[string]any
	// Credentials replaces the stored secret when non-nil. A nil leaves the
	// existing one alone, which is what lets a page save an S3 destination's
	// bucket without being sent the key it never received.
	Credentials *string
}

// UpdateDestination changes a destination.
func (r *Repository) UpdateDestination(ctx context.Context, id string,
	params UpdateDestinationParams,
) (Destination, error) {
	var config []byte
	if params.Config != nil {
		encoded, err := json.Marshal(params.Config)
		if err != nil {
			return Destination{}, fmt.Errorf("encode destination config: %w", err)
		}
		config = encoded
	}

	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE backup_destinations SET
				name = COALESCE($2, name),
				config = COALESCE($3::jsonb, config),
				credentials_encrypted = CASE
					WHEN $4::text IS NULL THEN credentials_encrypted
					ELSE nullif($4::text, '')
				END,
				updated_at = now()
			WHERE id = $1::uuid
			RETURNING *
		)
		SELECT `+destinationColumns+` FROM updated d`,
		id, params.Name, config, params.Credentials)

	dest, err := scanDestination(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Destination{}, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return Destination{}, ErrNameTaken
		}
		return Destination{}, fmt.Errorf("update destination: %w", err)
	}
	return dest, nil
}

// RecordDestinationCheck stores the outcome of reaching a destination.
func (r *Repository) RecordDestinationCheck(ctx context.Context, id string,
	ok bool, detail string,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE backup_destinations
		SET last_check_at = now(), last_check_ok = $2,
		    last_check_detail = nullif($3, ''), updated_at = now()
		WHERE id = $1::uuid`, id, ok, detail)
	if err != nil {
		return fmt.Errorf("record destination check: %w", err)
	}
	return nil
}

// DeleteDestination removes a destination.
//
// A destination a schedule still points at is refused rather than cascaded.
// ON DELETE RESTRICT in the schema says the same thing; this turns it into a
// message somebody can act on instead of a foreign-key error.
func (r *Repository) DeleteDestination(ctx context.Context, id string) error {
	var schedules int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM backup_schedules WHERE destination_id = $1::uuid`, id).
		Scan(&schedules); err != nil {
		return fmt.Errorf("count schedules: %w", err)
	}
	if schedules > 0 {
		return ErrDestinationInUse
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM backup_destinations WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete destination: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------- backups

const backupColumns = `
	b.id::text, b.server_id::text, b.website_id::text, b.database_id::text,
	b.subject, b.type, b.destination_id::text, b.destination,
	b.path, b.size_bytes, b.checksum,
	b.status, b.verified_at, b.verify_detail, b.manifest, b.error,
	b.job_id::text, b.schedule_id::text, b.created_by::text,
	b.started_at, b.completed_at, b.created_at`

func scanBackup(row pgx.Row) (Backup, error) {
	var (
		item     Backup
		manifest []byte
	)
	err := row.Scan(&item.ID, &item.ServerID, &item.WebsiteID, &item.DatabaseID,
		&item.Subject, &item.Type, &item.DestinationID, &item.DestinationName,
		&item.Path, &item.Size, &item.Checksum,
		&item.Status, &item.VerifiedAt, &item.VerifyDetail, &manifest, &item.Error,
		&item.JobID, &item.ScheduleID, &item.CreatedBy,
		&item.StartedAt, &item.CompletedAt, &item.CreatedAt)
	if err != nil {
		return Backup{}, err
	}
	if len(manifest) > 0 {
		if err := json.Unmarshal(manifest, &item.Manifest); err != nil {
			return Backup{}, fmt.Errorf("decode backup manifest: %w", err)
		}
	}
	return item, nil
}

// CreateBackupParams describes a backup about to be taken.
type CreateBackupParams struct {
	ServerID        string
	WebsiteID       string
	DatabaseID      string
	Subject         string
	Type            string
	DestinationID   string
	DestinationName string
	Key             string
	ScheduleID      string
	CreatedBy       string
}

// CreateBackup records a backup before it starts.
//
// The row exists before the work does, which is what makes a backup that fails
// leave evidence. A row written only on success would mean the panel's answer
// to "did last night's backup run" was silence.
func (r *Repository) CreateBackup(ctx context.Context, params CreateBackupParams) (Backup, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO backups (
				server_id, website_id, database_id, subject, type,
				destination_id, destination, path, status, schedule_id, created_by
			) VALUES (
				$1::uuid, nullif($2, '')::uuid, nullif($3, '')::uuid, $4, $5,
				nullif($6, '')::uuid, $7, $8, 'pending', nullif($9, '')::uuid,
				nullif($10, '')::uuid
			)
			RETURNING *
		)
		SELECT `+backupColumns+` FROM inserted b`,
		params.ServerID, params.WebsiteID, params.DatabaseID, params.Subject, params.Type,
		params.DestinationID, params.DestinationName, params.Key,
		params.ScheduleID, params.CreatedBy)

	item, err := scanBackup(row)
	if err != nil {
		return Backup{}, fmt.Errorf("create backup: %w", err)
	}
	return item, nil
}

// AttachJob records which job is producing a backup, and marks it running.
func (r *Repository) AttachJob(ctx context.Context, id, jobID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE backups SET job_id = $2::uuid, status = 'running', started_at = now()
		WHERE id = $1::uuid AND status = 'pending'`, id, jobID)
	if err != nil {
		return fmt.Errorf("attach job: %w", err)
	}
	return nil
}

// CompleteBackupParams describes a finished backup.
type CompleteBackupParams struct {
	Path         string
	Size         int64
	Checksum     string
	Verified     bool
	VerifyDetail string
	Manifest     map[string]any
}

// CompleteBackup marks a backup finished.
//
// A backup that could not be verified is stored as *failed*, with the reason.
// The bytes may well be there, but the panel has no basis for saying so, and a
// listing where "completed" sometimes means "probably" is a listing nobody can
// use to decide whether they are safe.
func (r *Repository) CompleteBackup(ctx context.Context, id string,
	params CompleteBackupParams,
) error {
	var manifest []byte
	if len(params.Manifest) > 0 {
		encoded, err := json.Marshal(params.Manifest)
		if err != nil {
			return fmt.Errorf("encode backup manifest: %w", err)
		}
		manifest = encoded
	}

	status := StatusCompleted
	var failure *string
	if !params.Verified {
		status = StatusFailed
		detail := params.VerifyDetail
		if detail == "" {
			detail = "the archive could not be read back from its destination"
		}
		failure = &detail
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE backups SET
			status = $2, path = $3, size_bytes = $4, checksum = $5,
			verified_at = CASE WHEN $6 THEN now() ELSE NULL END,
			verify_detail = nullif($7, ''),
			manifest = $8, error = $9, completed_at = now()
		WHERE id = $1::uuid`,
		id, status, params.Path, params.Size, params.Checksum,
		params.Verified, params.VerifyDetail, manifest, failure)
	if err != nil {
		return fmt.Errorf("complete backup: %w", err)
	}
	return nil
}

// FailBackup marks a backup failed with a reason.
func (r *Repository) FailBackup(ctx context.Context, id, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE backups SET status = 'failed', error = $2, completed_at = now()
		WHERE id = $1::uuid AND status IN ('pending', 'running')`, id, reason)
	if err != nil {
		return fmt.Errorf("fail backup: %w", err)
	}
	return nil
}

// RecordVerification stores the outcome of re-checking a stored backup.
func (r *Repository) RecordVerification(ctx context.Context, id string,
	ok bool, detail string,
) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE backups SET
			verified_at = CASE WHEN $2 THEN now() ELSE NULL END,
			verify_detail = nullif($3, '')
		WHERE id = $1::uuid`, id, ok, detail)
	if err != nil {
		return fmt.Errorf("record verification: %w", err)
	}
	return nil
}

// GetBackup returns one backup.
func (r *Repository) GetBackup(ctx context.Context, id string) (Backup, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+backupColumns+` FROM backups b WHERE b.id = $1::uuid`, id)

	item, err := scanBackup(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Backup{}, ErrNotFound
	}
	if err != nil {
		return Backup{}, fmt.Errorf("select backup: %w", err)
	}
	return item, nil
}

// ListBackupParams filters a listing.
type ListBackupParams struct {
	ServerID   string
	WebsiteID  string
	ScheduleID string
	Type       string
	Status     string
	Limit      int
}

// DefaultBackupLimit bounds a listing.
const DefaultBackupLimit = 50

// MaxBackupLimit is the largest page a caller may ask for.
const MaxBackupLimit = 200

// ListBackups returns backups newest first.
func (r *Repository) ListBackups(ctx context.Context, params ListBackupParams) ([]Backup, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = DefaultBackupLimit
	}
	if limit > MaxBackupLimit {
		limit = MaxBackupLimit
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+backupColumns+`
		FROM backups b
		WHERE b.server_id = $1::uuid
		  AND ($2 = '' OR b.website_id = nullif($2, '')::uuid)
		  AND ($3 = '' OR b.schedule_id = nullif($3, '')::uuid)
		  AND ($4 = '' OR b.type = $4)
		  AND ($5 = '' OR b.status = $5)
		ORDER BY b.created_at DESC
		LIMIT $6`,
		params.ServerID, params.WebsiteID, params.ScheduleID,
		params.Type, params.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("select backups: %w", err)
	}
	defer rows.Close()

	backups := []Backup{}
	for rows.Next() {
		item, err := scanBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan backup: %w", err)
		}
		backups = append(backups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backups: %w", err)
	}
	return backups, nil
}

// PrunableParams describes what retention may remove.
type PrunableParams struct {
	ScheduleID string
	OlderThan  time.Time
	KeepLast   int
}

// Prunable returns the backups a schedule's retention would remove.
//
// The count floor is applied *before* the age filter, not after: the most
// recent KeepLast completed backups are excluded whatever their age. Without
// that ordering, a panel that had been off for longer than the retention window
// would come back, find everything expired, and delete all of it — leaving
// nothing at the exact moment somebody is most likely to need something.
//
// Only *verified* backups count towards the floor. Keeping three archives the
// panel could not read back is keeping nothing.
func (r *Repository) Prunable(ctx context.Context, params PrunableParams) ([]Backup, error) {
	rows, err := r.pool.Query(ctx, `
		WITH kept AS (
			SELECT id FROM backups
			WHERE schedule_id = $1::uuid
			  AND status = 'completed' AND verified_at IS NOT NULL
			ORDER BY created_at DESC
			LIMIT $3
		)
		SELECT `+backupColumns+`
		FROM backups b
		WHERE b.schedule_id = $1::uuid
		  AND b.status IN ('completed', 'failed')
		  AND b.created_at < $2
		  AND b.id NOT IN (SELECT id FROM kept)
		ORDER BY b.created_at`,
		params.ScheduleID, params.OlderThan, params.KeepLast)
	if err != nil {
		return nil, fmt.Errorf("select prunable backups: %w", err)
	}
	defer rows.Close()

	backups := []Backup{}
	for rows.Next() {
		item, err := scanBackup(rows)
		if err != nil {
			return nil, fmt.Errorf("scan backup: %w", err)
		}
		backups = append(backups, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate prunable backups: %w", err)
	}
	return backups, nil
}

// DeleteBackup removes the panel's record of a backup.
func (r *Repository) DeleteBackup(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM backups WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete backup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Stats summarises what a server has.
type Stats struct {
	Total     int        `json:"total"`
	Completed int        `json:"completed"`
	Verified  int        `json:"verified"`
	Failed    int        `json:"failed"`
	Running   int        `json:"running"`
	Bytes     int64      `json:"bytes"`
	LatestAt  *time.Time `json:"latest_at"`
}

// Stats counts a server's backups.
func (r *Repository) Stats(ctx context.Context, serverID string) (Stats, error) {
	var stats Stats
	err := r.pool.QueryRow(ctx, `
		SELECT
			count(*),
			count(*) FILTER (WHERE status = 'completed'),
			count(*) FILTER (WHERE verified_at IS NOT NULL),
			count(*) FILTER (WHERE status = 'failed'),
			count(*) FILTER (WHERE status IN ('pending', 'running')),
			COALESCE(sum(size_bytes) FILTER (WHERE status = 'completed'), 0),
			max(completed_at) FILTER (WHERE status = 'completed')
		FROM backups WHERE server_id = $1::uuid`, serverID).
		Scan(&stats.Total, &stats.Completed, &stats.Verified, &stats.Failed,
			&stats.Running, &stats.Bytes, &stats.LatestAt)
	if err != nil {
		return Stats{}, fmt.Errorf("count backups: %w", err)
	}
	return stats, nil
}

// -------------------------------------------------------------- schedules

const scheduleColumns = `
	s.id::text, s.server_id::text, s.name, s.type,
	s.website_id::text, s.database_id::text,
	s.destination_id::text, COALESCE(d.name, ''),
	s.hour, s.minute, s.day_of_week,
	s.retention_days, s.keep_last, s.enabled,
	s.last_run_at, s.last_status, s.last_backup_id::text,
	s.created_at, s.updated_at`

func scanSchedule(row pgx.Row) (Schedule, error) {
	var schedule Schedule
	err := row.Scan(&schedule.ID, &schedule.ServerID, &schedule.Name, &schedule.Type,
		&schedule.WebsiteID, &schedule.DatabaseID,
		&schedule.DestinationID, &schedule.DestinationName,
		&schedule.Hour, &schedule.Minute, &schedule.DayOfWeek,
		&schedule.RetentionDays, &schedule.KeepLast, &schedule.Enabled,
		&schedule.LastRunAt, &schedule.LastStatus, &schedule.LastBackupID,
		&schedule.CreatedAt, &schedule.UpdatedAt)
	if err != nil {
		return Schedule{}, err
	}
	return schedule, nil
}

// CreateScheduleParams describes a new schedule.
type CreateScheduleParams struct {
	ServerID      string
	Name          string
	Type          string
	WebsiteID     string
	DatabaseID    string
	DestinationID string
	Hour          int
	Minute        int
	DayOfWeek     int
	RetentionDays int
	KeepLast      int
	Enabled       bool
}

// CreateSchedule stores a schedule.
func (r *Repository) CreateSchedule(ctx context.Context, params CreateScheduleParams) (
	Schedule, error,
) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO backup_schedules (
				server_id, name, type, website_id, database_id, destination_id,
				hour, minute, day_of_week, retention_days, keep_last, enabled
			) VALUES (
				$1::uuid, $2, $3, nullif($4, '')::uuid, nullif($5, '')::uuid, $6::uuid,
				$7, $8, $9, $10, $11, $12
			)
			RETURNING *
		)
		SELECT `+scheduleColumns+`
		FROM inserted s
		LEFT JOIN backup_destinations d ON d.id = s.destination_id`,
		params.ServerID, params.Name, params.Type, params.WebsiteID, params.DatabaseID,
		params.DestinationID, params.Hour, params.Minute, params.DayOfWeek,
		params.RetentionDays, params.KeepLast, params.Enabled)

	schedule, err := scanSchedule(row)
	if err != nil {
		return Schedule{}, fmt.Errorf("create schedule: %w", err)
	}
	return schedule, nil
}

// ListSchedules returns a server's schedules, oldest first.
func (r *Repository) ListSchedules(ctx context.Context, serverID string) ([]Schedule, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM backup_schedules s
		LEFT JOIN backup_destinations d ON d.id = s.destination_id
		WHERE s.server_id = $1::uuid
		ORDER BY s.created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("select schedules: %w", err)
	}
	defer rows.Close()

	schedules := []Schedule{}
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schedules: %w", err)
	}
	return schedules, nil
}

// GetSchedule returns one schedule.
func (r *Repository) GetSchedule(ctx context.Context, id string) (Schedule, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+scheduleColumns+`
		FROM backup_schedules s
		LEFT JOIN backup_destinations d ON d.id = s.destination_id
		WHERE s.id = $1::uuid`, id)

	schedule, err := scanSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("select schedule: %w", err)
	}
	return schedule, nil
}

// UpdateScheduleParams describes a change to a schedule.
//
// The type and what it points at cannot change. A schedule that started backing
// up a different site would leave its own history attached to backups of
// something else, and retention would then prune one site's archives on
// another's rules.
type UpdateScheduleParams struct {
	Name          *string
	DestinationID *string
	Hour          *int
	Minute        *int
	DayOfWeek     *int
	RetentionDays *int
	KeepLast      *int
	Enabled       *bool
}

// UpdateSchedule changes a schedule.
func (r *Repository) UpdateSchedule(ctx context.Context, id string,
	params UpdateScheduleParams,
) (Schedule, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE backup_schedules SET
				name = COALESCE($2, name),
				destination_id = COALESCE(nullif($3, '')::uuid, destination_id),
				hour = COALESCE($4, hour),
				minute = COALESCE($5, minute),
				day_of_week = COALESCE($6, day_of_week),
				retention_days = COALESCE($7, retention_days),
				keep_last = COALESCE($8, keep_last),
				enabled = COALESCE($9, enabled),
				updated_at = now()
			WHERE id = $1::uuid
			RETURNING *
		)
		SELECT `+scheduleColumns+`
		FROM updated s
		LEFT JOIN backup_destinations d ON d.id = s.destination_id`,
		id, params.Name, params.DestinationID, params.Hour, params.Minute,
		params.DayOfWeek, params.RetentionDays, params.KeepLast, params.Enabled)

	schedule, err := scanSchedule(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Schedule{}, ErrNotFound
	}
	if err != nil {
		return Schedule{}, fmt.Errorf("update schedule: %w", err)
	}
	return schedule, nil
}

// RecordScheduleRun stores what a schedule's run produced.
func (r *Repository) RecordScheduleRun(ctx context.Context, id, status, backupID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE backup_schedules SET
			last_run_at = now(), last_status = $2,
			last_backup_id = COALESCE(nullif($3, '')::uuid, last_backup_id),
			updated_at = now()
		WHERE id = $1::uuid`, id, status, backupID)
	if err != nil {
		return fmt.Errorf("record schedule run: %w", err)
	}
	return nil
}

// DeleteSchedule removes a schedule.
//
// The backups it took stay. They are the only reason it existed, and deleting a
// schedule is a decision about the future.
func (r *Repository) DeleteSchedule(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM backup_schedules WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DueSchedules returns the enabled schedules whose time has come.
//
// "Due" means: enabled, the day matches, the time of day has passed, and it has
// not already run since that time today. The last clause is what stops a
// scheduler that ticks every minute taking sixty backups an hour.
func (r *Repository) DueSchedules(ctx context.Context, serverID string, now time.Time) (
	[]Schedule, error,
) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+scheduleColumns+`
		FROM backup_schedules s
		LEFT JOIN backup_destinations d ON d.id = s.destination_id
		WHERE s.server_id = $1::uuid
		  AND s.enabled
		  AND (s.day_of_week = -1 OR s.day_of_week = $3::int)
		  AND ($4::int * 60 + $5::int) >= (s.hour * 60 + s.minute)
		  AND (
		    s.last_run_at IS NULL
		    OR s.last_run_at < date_trunc('day', $2::timestamptz)
		         + make_interval(hours => s.hour, mins => s.minute)
		  )
		ORDER BY s.hour, s.minute`,
		serverID, now, int(now.UTC().Weekday()), now.UTC().Hour(), now.UTC().Minute())
	if err != nil {
		return nil, fmt.Errorf("select due schedules: %w", err)
	}
	defer rows.Close()

	schedules := []Schedule{}
	for rows.Next() {
		schedule, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan schedule: %w", err)
		}
		schedules = append(schedules, schedule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate due schedules: %w", err)
	}
	return schedules, nil
}

// isUniqueViolation reports whether an error is a duplicate-key failure.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// attachSchedule records which schedule asked for a backup.
//
// Set after the row is created rather than as part of creating it, because the
// same creation path serves a manual backup and a scheduled one, and a manual
// backup belongs to no schedule.
func (r *Repository) attachSchedule(ctx context.Context, id, scheduleID string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE backups SET schedule_id = $2::uuid WHERE id = $1::uuid`, id, scheduleID)
	if err != nil {
		return fmt.Errorf("attach schedule: %w", err)
	}
	return nil
}
