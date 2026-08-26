// Package websites is the panel's record of the sites it hosts.
//
// The rows here describe intent; the Agent owns what is actually on disk. The
// two are reconciled through jobs: a website is created in "creating", a job
// provisions it, and the job's outcome moves it to "active" or "failed". A row
// therefore never claims a site works before anything has served a request.
package websites

import (
	"context"
	"errors"
	"fmt"
	"time"

	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status values a website can hold. They match the CHECK constraint in
// migration 0005; changing one requires changing the other.
const (
	StatusCreating  = "creating"
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusFailed    = "failed"
	StatusDeleting  = "deleting"
)

// Domain types.
const (
	DomainPrimary   = "primary"
	DomainAlias     = "alias"
	DomainSubdomain = "subdomain"
	DomainRedirect  = "redirect"
)

// Website is one hosted site.
type Website struct {
	ID            string  `json:"id"`
	ServerID      string  `json:"server_id"`
	Name          *string `json:"name"`
	PrimaryDomain string  `json:"primary_domain"`
	DocumentRoot  string  `json:"document_root"`
	SystemUser    string  `json:"system_user"`
	PHPVersion    *string `json:"php_version"`
	Status        string  `json:"status"`
	SSLEnabled    bool    `json:"ssl_enabled"`
	HTTPSRedirect bool    `json:"https_redirect"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Domains is populated by Get, not by List: a listing does not need every
	// site's alias table, and fetching it would be a query per row.
	Domains []Domain `json:"domains,omitempty"`
}

// Domain is a hostname pointing at a website.
type Domain struct {
	ID         string    `json:"id"`
	WebsiteID  string    `json:"website_id"`
	Domain     string    `json:"domain"`
	Type       string    `json:"type"`
	Status     string    `json:"status"`
	RedirectTo *string   `json:"redirect_to"`
	CreatedAt  time.Time `json:"created_at"`
}

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("website not found")
	// ErrDomainTaken means another website already claims the hostname. Two
	// sites answering to one name makes the vhost's server_name ambiguous.
	ErrDomainTaken = errors.New("domain is already in use")
	// ErrUserTaken means the derived system account is already assigned.
	ErrUserTaken = errors.New("system user is already in use")
	// ErrPrimaryDomain means a caller tried to detach a site's own identity.
	ErrPrimaryDomain = errors.New("the primary domain cannot be removed")
)

// Repository reads and writes websites and their domains.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// The column is system_username, not system_user: PostgreSQL 16 reserved
// SYSTEM_USER as a keyword. It is aliased back so the JSON contract and the
// Go field keep the name the API specifies.
const websiteColumns = `
	id::text, server_id::text, name, primary_domain, document_root,
	system_username AS system_user,
	php_version, status, ssl_enabled, https_redirect, created_at, updated_at`

func scanWebsite(row pgx.Row) (Website, error) {
	var site Website
	err := row.Scan(&site.ID, &site.ServerID, &site.Name, &site.PrimaryDomain,
		&site.DocumentRoot, &site.SystemUser, &site.PHPVersion, &site.Status,
		&site.SSLEnabled, &site.HTTPSRedirect, &site.CreatedAt, &site.UpdatedAt)
	return site, err
}

const domainColumns = `
	id::text, website_id::text, domain, type, status, redirect_to, created_at`

func scanDomain(row pgx.Row) (Domain, error) {
	var domain Domain
	err := row.Scan(&domain.ID, &domain.WebsiteID, &domain.Domain, &domain.Type,
		&domain.Status, &domain.RedirectTo, &domain.CreatedAt)
	return domain, err
}

// CreateParams describes a website to record.
type CreateParams struct {
	ServerID      string
	Name          string
	PrimaryDomain string
	DocumentRoot  string
	SystemUser    string
}

// Create inserts a website and its primary domain in one transaction.
//
// Both rows are written together because a website without its primary domain
// is not a valid site: the vhost would have no server_name. A partial write
// here would leave a row the Agent cannot provision and the user cannot fix.
func (r *Repository) Create(ctx context.Context, params CreateParams) (Website, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return Website{}, fmt.Errorf("begin website transaction: %w", err)
	}
	defer func() {
		// Rollback after a successful commit is a no-op, so this is safe as an
		// unconditional cleanup.
		_ = tx.Rollback(ctx)
	}()

	row := tx.QueryRow(ctx, `
		INSERT INTO websites (server_id, name, primary_domain, document_root, system_username, status)
		VALUES ($1::uuid, nullif($2, ''), $3, $4, $5, 'creating')
		RETURNING `+websiteColumns,
		params.ServerID, params.Name, params.PrimaryDomain,
		params.DocumentRoot, params.SystemUser)

	site, err := scanWebsite(row)
	if err != nil {
		return Website{}, translateConflict(err)
	}

	domainRow := tx.QueryRow(ctx, `
		INSERT INTO domains (website_id, domain, type, status)
		VALUES ($1::uuid, $2, 'primary', 'active')
		RETURNING `+domainColumns, site.ID, params.PrimaryDomain)

	primary, err := scanDomain(domainRow)
	if err != nil {
		return Website{}, translateConflict(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Website{}, fmt.Errorf("commit website: %w", err)
	}

	site.Domains = []Domain{primary}
	return site, nil
}

// translateConflict turns a unique-violation into a meaningful error.
//
// The constraint name is what distinguishes "that domain is taken" from "that
// account is taken"; without it the user gets one unhelpful message for two
// different problems.
func translateConflict(err error) error {
	var pgErr interface{ SQLState() string }
	if !errors.As(err, &pgErr) || pgErr.SQLState() != "23505" {
		return fmt.Errorf("write website: %w", err)
	}

	message := err.Error()
	switch {
	case strings.Contains(message, "primary_domain"),
		strings.Contains(message, "domains_domain_key"):
		return ErrDomainTaken
	case strings.Contains(message, "system_user"),
		strings.Contains(message, "system_username"):
		return ErrUserTaken
	default:
		return fmt.Errorf("write website: %w", err)
	}
}

// Get returns a website with its domains.
func (r *Repository) Get(ctx context.Context, id string) (Website, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+websiteColumns+` FROM websites WHERE id = $1::uuid`, id)

	site, err := scanWebsite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Website{}, ErrNotFound
	}
	if err != nil {
		return Website{}, fmt.Errorf("select website: %w", err)
	}

	domains, err := r.ListDomains(ctx, site.ID)
	if err != nil {
		return Website{}, err
	}
	site.Domains = domains
	return site, nil
}

// GetByDomain returns the website owning a hostname.
func (r *Repository) GetByDomain(ctx context.Context, domain string) (Website, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+websiteColumns+` FROM websites WHERE primary_domain = $1`, domain)

	site, err := scanWebsite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Website{}, ErrNotFound
	}
	if err != nil {
		return Website{}, fmt.Errorf("select website by domain: %w", err)
	}
	return site, nil
}

// ListParams filters a website listing.
type ListParams struct {
	Status string
	Limit  int
}

// DefaultListLimit bounds a website listing.
const DefaultListLimit = 100

// MaxListLimit is the largest page a caller may request.
const MaxListLimit = 500

// List returns websites, newest first.
func (r *Repository) List(ctx context.Context, params ListParams) ([]Website, error) {
	limit := params.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+websiteColumns+`
		FROM websites
		WHERE ($1 = '' OR status = $1)
		ORDER BY created_at DESC
		LIMIT $2`, params.Status, limit)
	if err != nil {
		return nil, fmt.Errorf("select websites: %w", err)
	}
	defer rows.Close()

	sites := []Website{}
	for rows.Next() {
		site, err := scanWebsite(rows)
		if err != nil {
			return nil, fmt.Errorf("scan website: %w", err)
		}
		sites = append(sites, site)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate websites: %w", err)
	}
	return sites, nil
}

// SetStatus moves a website to a new lifecycle state.
func (r *Repository) SetStatus(ctx context.Context, id, status string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE websites SET status = $2, updated_at = now() WHERE id = $1::uuid`,
		id, status)
	if err != nil {
		return fmt.Errorf("update website status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateParams carries the mutable fields of a website.
//
// Pointers distinguish "leave this alone" from "set it to the zero value",
// which a plain struct cannot express.
type UpdateParams struct {
	Name          *string
	HTTPSRedirect *bool
}

// Update applies mutable fields to a website.
func (r *Repository) Update(ctx context.Context, id string, params UpdateParams) (Website, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE websites
		SET name           = COALESCE($2, name),
		    https_redirect = COALESCE($3, https_redirect),
		    updated_at     = now()
		WHERE id = $1::uuid
		RETURNING `+websiteColumns, id, params.Name, params.HTTPSRedirect)

	site, err := scanWebsite(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Website{}, ErrNotFound
	}
	if err != nil {
		return Website{}, fmt.Errorf("update website: %w", err)
	}
	return site, nil
}

// Delete removes a website row. Its domains cascade.
//
// This runs only after the Agent reports the site gone from disk: deleting the
// row first would strand the files with nothing in the panel referring to them.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM websites WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete website: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListDomains returns a website's domains, primary first.
func (r *Repository) ListDomains(ctx context.Context, websiteID string) ([]Domain, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+domainColumns+`
		FROM domains
		WHERE website_id = $1::uuid
		ORDER BY (type = 'primary') DESC, domain`, websiteID)
	if err != nil {
		return nil, fmt.Errorf("select domains: %w", err)
	}
	defer rows.Close()

	domains := []Domain{}
	for rows.Next() {
		domain, err := scanDomain(rows)
		if err != nil {
			return nil, fmt.Errorf("scan domain: %w", err)
		}
		domains = append(domains, domain)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate domains: %w", err)
	}
	return domains, nil
}

// AddDomainParams describes a hostname to attach to a website.
type AddDomainParams struct {
	WebsiteID  string
	Domain     string
	Type       string
	RedirectTo string
}

// AddDomain attaches a hostname to a website.
func (r *Repository) AddDomain(ctx context.Context, params AddDomainParams) (Domain, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO domains (website_id, domain, type, status, redirect_to)
		VALUES ($1::uuid, $2, $3, 'active', nullif($4, ''))
		RETURNING `+domainColumns,
		params.WebsiteID, params.Domain, params.Type, params.RedirectTo)

	domain, err := scanDomain(row)
	if err != nil {
		return Domain{}, translateConflict(err)
	}
	return domain, nil
}

// GetDomain returns one domain.
func (r *Repository) GetDomain(ctx context.Context, id string) (Domain, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM domains WHERE id = $1::uuid`, id)

	domain, err := scanDomain(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	if err != nil {
		return Domain{}, fmt.Errorf("select domain: %w", err)
	}
	return domain, nil
}

// DeleteDomain removes a hostname from a website.
//
// The primary domain cannot be removed this way: it is the site's identity and
// its vhost's server_name. Removing it would leave a site nothing can reach.
func (r *Repository) DeleteDomain(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM domains WHERE id = $1::uuid AND type <> 'primary'`, id)
	if err != nil {
		return fmt.Errorf("delete domain: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Nothing was deleted for one of two reasons, and telling a user their
		// primary domain "does not exist" would send them looking for the
		// wrong problem.
		domain, err := r.GetDomain(ctx, id)
		if err != nil {
			return err
		}
		if domain.Type == DomainPrimary {
			return ErrPrimaryDomain
		}
		return ErrNotFound
	}
	return nil
}
