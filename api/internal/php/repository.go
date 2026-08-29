// Package php is the panel's record of PHP versions and per-site FPM pools.
//
// As with websites, these rows describe intent and the Agent owns what is
// actually on the host. Version rows are refreshed from the Agent rather than
// asserted by an operator: a panel that offers a version nothing can run
// produces sites that return 502 on their first request.
package php

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Version status values, matching the CHECK constraint in migration 0006.
const (
	StatusAvailable  = "available"
	StatusInstalling = "installing"
	StatusRemoving   = "removing"
	StatusFailed     = "failed"
)

// Version is one PHP version known to the panel.
type Version struct {
	ID          string  `json:"id"`
	Version     string  `json:"version"`
	BinaryPath  *string `json:"binary_path"`
	FPMService  *string `json:"fpm_service"`
	Status      string  `json:"status"`
	Installed   bool    `json:"installed"`
	FullVersion string  `json:"full_version,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// InUse counts the websites running this version. A version still in use
	// cannot be removed without breaking those sites.
	InUse int `json:"in_use"`
}

// Pool is one website's FPM pool.
type Pool struct {
	ID         string `json:"id"`
	WebsiteID  string `json:"website_id"`
	PHPVersion string `json:"php_version"`
	PoolName   string `json:"pool_name"`
	SocketPath string `json:"socket_path"`

	MemoryLimit       *string `json:"memory_limit"`
	MaxChildren       *int    `json:"max_children"`
	UploadMaxFilesize *string `json:"upload_max_filesize"`
	MaxExecutionTime  *int    `json:"max_execution_time"`
	OPcacheEnabled    bool    `json:"opcache_enabled"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("php version not found")
	// ErrPoolNotFound means the website has no PHP configured.
	ErrPoolNotFound = errors.New("this website does not run PHP")
	// ErrVersionInUse means websites still depend on the version.
	ErrVersionInUse = errors.New("php version is still in use by websites")
)

// Repository reads and writes PHP versions and pools.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const versionColumns = `
	id::text, version, binary_path, fpm_service, status, installed,
	created_at, updated_at`

func scanVersion(row pgx.Row) (Version, error) {
	var version Version
	err := row.Scan(&version.ID, &version.Version, &version.BinaryPath,
		&version.FPMService, &version.Status, &version.Installed,
		&version.CreatedAt, &version.UpdatedAt)
	return version, err
}

const poolColumns = `
	id::text, website_id::text, php_version, pool_name, socket_path,
	memory_limit, max_children, upload_max_filesize, max_execution_time,
	opcache_enabled, created_at, updated_at`

func scanPool(row pgx.Row) (Pool, error) {
	var pool Pool
	err := row.Scan(&pool.ID, &pool.WebsiteID, &pool.PHPVersion, &pool.PoolName,
		&pool.SocketPath, &pool.MemoryLimit, &pool.MaxChildren,
		&pool.UploadMaxFilesize, &pool.MaxExecutionTime, &pool.OPcacheEnabled,
		&pool.CreatedAt, &pool.UpdatedAt)
	return pool, err
}

// DetectedVersion is a version as the Agent reported it.
type DetectedVersion struct {
	Version    string
	BinaryPath string
	FPMService string
	Full       string
}

// SyncVersions replaces the installed set with what the Agent found.
//
// Versions the Agent no longer reports are marked uninstalled rather than
// deleted: a website row may still reference one, and deleting the version
// would hide why that site stopped working.
func (r *Repository) SyncVersions(ctx context.Context, detected []DetectedVersion) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin version sync: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	present := make([]string, 0, len(detected))
	for _, version := range detected {
		present = append(present, version.Version)

		_, err := tx.Exec(ctx, `
			INSERT INTO php_versions (version, binary_path, fpm_service, status, installed, updated_at)
			VALUES ($1, nullif($2, ''), nullif($3, ''), 'available', TRUE, now())
			ON CONFLICT (version) DO UPDATE SET
				binary_path = EXCLUDED.binary_path,
				fpm_service = EXCLUDED.fpm_service,
				status      = 'available',
				installed   = TRUE,
				updated_at  = now()`,
			version.Version, version.BinaryPath, version.FPMService)
		if err != nil {
			return fmt.Errorf("upsert php version %s: %w", version.Version, err)
		}
	}

	// Anything not reported is no longer on the host. The row stays so a site
	// referencing it still has something to point at.
	//
	// A removal that has finished is settled here too. A version whose install
	// failed is already installed = FALSE, so keying only off that flag left it
	// stuck in "removing" for good once it had been removed: the sync that
	// should have settled it skipped the one row that needed settling, and the
	// panel went on showing an operation that had already finished.
	//
	// "installing" and "failed" are deliberately left alone. The first is still
	// in flight, and the second is the record of why a version is not here,
	// which is the operator's to clear by removing it.
	_, err = tx.Exec(ctx, `
		UPDATE php_versions
		SET installed   = FALSE,
		    binary_path = NULL,
		    fpm_service = NULL,
		    status      = CASE WHEN status = 'removing' THEN 'available' ELSE status END,
		    updated_at  = now()
		WHERE NOT (version = ANY($1::text[]))
		  AND (installed = TRUE OR status = 'removing')`, present)
	if err != nil {
		return fmt.Errorf("mark removed php versions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit version sync: %w", err)
	}
	return nil
}

// ListVersions returns known versions, newest first, with their usage counts.
func (r *Repository) ListVersions(ctx context.Context) ([]Version, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+versionColumns+`,
		       (SELECT count(*) FROM php_pools p WHERE p.php_version = v.version)
		FROM php_versions v
		-- Numeric ordering: a text sort puts "8.9" above "8.10", which becomes
		-- visibly wrong the first time a PHP 8.10 exists.
		ORDER BY string_to_array(version, '.')::int[] DESC`)
	if err != nil {
		return nil, fmt.Errorf("select php versions: %w", err)
	}
	defer rows.Close()

	versions := []Version{}
	for rows.Next() {
		var version Version
		err := rows.Scan(&version.ID, &version.Version, &version.BinaryPath,
			&version.FPMService, &version.Status, &version.Installed,
			&version.CreatedAt, &version.UpdatedAt, &version.InUse)
		if err != nil {
			return nil, fmt.Errorf("scan php version: %w", err)
		}
		versions = append(versions, version)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate php versions: %w", err)
	}
	return versions, nil
}

// GetVersion returns one version by its selector.
func (r *Repository) GetVersion(ctx context.Context, version string) (Version, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+versionColumns+` FROM php_versions WHERE version = $1`, version)

	found, err := scanVersion(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Version{}, ErrNotFound
	}
	if err != nil {
		return Version{}, fmt.Errorf("select php version: %w", err)
	}

	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM php_pools WHERE php_version = $1`, version).
		Scan(&found.InUse); err != nil {
		return Version{}, fmt.Errorf("count php version usage: %w", err)
	}
	return found, nil
}

// SetVersionStatus records a version's lifecycle state.
//
// It deliberately does not touch `installed`. That column is a fact about the
// host and is owned by SyncVersions, which asks the Agent; status is the
// panel's own view of work in flight. Writing both here let a queued install
// claim a version was present before anything had installed it, which the
// installed-implies-a-binary constraint then rejected.
func (r *Repository) SetVersionStatus(ctx context.Context, version, status string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO php_versions (version, status, installed, updated_at)
		VALUES ($1, $2, FALSE, now())
		ON CONFLICT (version) DO UPDATE SET
			status = EXCLUDED.status,
			updated_at = now()`, version, status)
	if err != nil {
		return fmt.Errorf("update php version status: %w", err)
	}
	return nil
}

// CountWebsitesUsing reports how many sites run a version.
//
// Removing a version those sites depend on would take every one of them
// offline, so this is checked before a removal is queued.
func (r *Repository) CountWebsitesUsing(ctx context.Context, version string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM websites WHERE php_version = $1`, version).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count websites using php: %w", err)
	}
	return count, nil
}

// GetPool returns a website's FPM pool.
func (r *Repository) GetPool(ctx context.Context, websiteID string) (Pool, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+poolColumns+` FROM php_pools WHERE website_id = $1::uuid`, websiteID)

	pool, err := scanPool(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Pool{}, ErrPoolNotFound
	}
	if err != nil {
		return Pool{}, fmt.Errorf("select php pool: %w", err)
	}
	return pool, nil
}

// UpsertParams describes a pool to record.
type UpsertParams struct {
	WebsiteID  string
	PHPVersion string
	PoolName   string
	SocketPath string

	MemoryLimit       string
	UploadMaxFilesize string
	MaxExecutionTime  int
	OPcacheEnabled    bool
	MaxChildren       int
}

// UpsertPool records a website's pool, replacing any previous one.
//
// The website_id column is unique, so switching a site's PHP version updates
// the existing row rather than adding a second: two pools for one site would
// race for the same socket path.
func (r *Repository) UpsertPool(ctx context.Context, params UpsertParams) (Pool, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO php_pools (
			website_id, php_version, pool_name, socket_path,
			memory_limit, upload_max_filesize, max_execution_time,
			opcache_enabled, max_children)
		VALUES ($1::uuid, $2, $3, $4,
			nullif($5, ''), nullif($6, ''), nullif($7, 0), $8, nullif($9, 0))
		ON CONFLICT (website_id) DO UPDATE SET
			php_version         = EXCLUDED.php_version,
			pool_name           = EXCLUDED.pool_name,
			socket_path         = EXCLUDED.socket_path,
			memory_limit        = EXCLUDED.memory_limit,
			upload_max_filesize = EXCLUDED.upload_max_filesize,
			max_execution_time  = EXCLUDED.max_execution_time,
			opcache_enabled     = EXCLUDED.opcache_enabled,
			max_children        = EXCLUDED.max_children,
			updated_at          = now()
		RETURNING `+poolColumns,
		params.WebsiteID, params.PHPVersion, params.PoolName, params.SocketPath,
		params.MemoryLimit, params.UploadMaxFilesize, params.MaxExecutionTime,
		params.OPcacheEnabled, params.MaxChildren)

	pool, err := scanPool(row)
	if err != nil {
		return Pool{}, fmt.Errorf("upsert php pool: %w", err)
	}
	return pool, nil
}

// DeletePool removes a website's pool record.
func (r *Repository) DeletePool(ctx context.Context, websiteID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM php_pools WHERE website_id = $1::uuid`, websiteID)
	if err != nil {
		return fmt.Errorf("delete php pool: %w", err)
	}
	return nil
}

// SetWebsiteVersion records which PHP a website runs, or none.
func (r *Repository) SetWebsiteVersion(ctx context.Context, websiteID, version string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE websites SET php_version = nullif($2, ''), updated_at = now()
		 WHERE id = $1::uuid`, websiteID, version)
	if err != nil {
		return fmt.Errorf("set website php version: %w", err)
	}
	return nil
}
