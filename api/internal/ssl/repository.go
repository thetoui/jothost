// Package ssl is the panel's record of the certificates it manages.
//
// As with websites and PHP, these rows describe intent and the Agent owns what
// is actually on disk. A certificate row is written when issuance is queued and
// only becomes "valid" once the Agent has reported a certificate it could read
// and parse — a panel that claims HTTPS works before a handshake has ever
// succeeded is worse than one that says nothing.
package ssl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Providers, matching the CHECK constraint in migration 0007.
const (
	ProviderLetsEncrypt = "letsencrypt"
	ProviderSelfSigned  = "selfsigned"
)

// Certificate lifecycle states.
const (
	StatusPending  = "pending"
	StatusIssuing  = "issuing"
	StatusValid    = "valid"
	StatusExpiring = "expiring"
	StatusExpired  = "expired"
	StatusRevoked  = "revoked"
	StatusFailed   = "failed"
)

// ExpiringWithin is when a certificate starts being called "expiring".
//
// It matches the Agent's own threshold so the panel and the host never
// disagree about whether something needs attention.
const ExpiringWithin = 21 * 24 * time.Hour

// Certificate is one managed certificate.
type Certificate struct {
	ID        string   `json:"id"`
	WebsiteID string   `json:"website_id"`
	Provider  string   `json:"provider"`
	Domains   []string `json:"domains"`

	CertificatePath *string `json:"certificate_path"`
	PrivateKeyPath  *string `json:"private_key_path"`
	Fingerprint     *string `json:"fingerprint"`
	Issuer          *string `json:"issuer"`

	IssuedAt  *time.Time `json:"issued_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	AutoRenew bool       `json:"auto_renew"`
	Status    string     `json:"status"`
	LastError *string    `json:"last_error"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// PrimaryDomain is joined from the website, so a listing can name the site
	// without a second query per row.
	PrimaryDomain string `json:"primary_domain,omitempty"`
}

// DaysRemaining reports how long the certificate is still valid for.
func (c Certificate) DaysRemaining(now time.Time) *int {
	if c.ExpiresAt == nil {
		return nil
	}
	days := int(c.ExpiresAt.Sub(now).Hours() / 24)
	return &days
}

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("no certificate for this website")
)

// Repository reads and writes certificates.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// columns is the unqualified list, for statements over one table — including
// a RETURNING clause, which has no alias to qualify against.
const columns = `
	id::text, website_id::text, provider, domains,
	certificate_path, private_key_path, fingerprint, issuer,
	issued_at, expires_at, auto_renew, status, last_error,
	created_at, updated_at`

// joinedColumns is the same list qualified for the queries that join websites.
const joinedColumns = `
	c.id::text, c.website_id::text, c.provider, c.domains,
	c.certificate_path, c.private_key_path, c.fingerprint, c.issuer,
	c.issued_at, c.expires_at, c.auto_renew, c.status, c.last_error,
	c.created_at, c.updated_at`

func scan(row pgx.Row, withWebsite bool) (Certificate, error) {
	var (
		certificate Certificate
		domains     []byte
	)

	targets := []any{
		&certificate.ID, &certificate.WebsiteID, &certificate.Provider, &domains,
		&certificate.CertificatePath, &certificate.PrivateKeyPath,
		&certificate.Fingerprint, &certificate.Issuer,
		&certificate.IssuedAt, &certificate.ExpiresAt, &certificate.AutoRenew,
		&certificate.Status, &certificate.LastError,
		&certificate.CreatedAt, &certificate.UpdatedAt,
	}
	if withWebsite {
		targets = append(targets, &certificate.PrimaryDomain)
	}

	if err := row.Scan(targets...); err != nil {
		return Certificate{}, err
	}
	if len(domains) > 0 {
		if err := json.Unmarshal(domains, &certificate.Domains); err != nil {
			return Certificate{}, fmt.Errorf("decode certificate domains: %w", err)
		}
	}
	return certificate, nil
}

// UpsertParams records a certificate the panel is about to obtain.
type UpsertParams struct {
	WebsiteID string
	Provider  string
	Domains   []string
	AutoRenew bool
	Status    string
}

// Upsert records a certificate, replacing any previous one for the website.
//
// website_id is unique, so re-issuing replaces rather than accumulating: two
// rows for one site would make the panel's idea of "the certificate" ambiguous
// exactly when someone is trying to work out why HTTPS is broken.
func (r *Repository) Upsert(ctx context.Context, params UpsertParams) (Certificate, error) {
	domains, err := json.Marshal(params.Domains)
	if err != nil {
		return Certificate{}, fmt.Errorf("encode certificate domains: %w", err)
	}

	status := params.Status
	if status == "" {
		status = StatusPending
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO ssl_certificates (website_id, provider, domains, auto_renew, status)
		VALUES ($1::uuid, $2, $3, $4, $5)
		ON CONFLICT (website_id) DO UPDATE SET
			provider   = EXCLUDED.provider,
			domains    = EXCLUDED.domains,
			auto_renew = EXCLUDED.auto_renew,
			status     = EXCLUDED.status,
			-- A new attempt clears the previous failure, so a stale message
			-- does not sit beside a certificate that has since succeeded.
			last_error = NULL,
			updated_at = now()
		RETURNING `+columns,
		params.WebsiteID, params.Provider, domains, params.AutoRenew, status)

	certificate, err := scan(row, false)
	if err != nil {
		return Certificate{}, fmt.Errorf("upsert certificate: %w", err)
	}
	return certificate, nil
}

// IssuedParams records what the Agent actually produced.
type IssuedParams struct {
	WebsiteID       string
	Provider        string
	Domains         []string
	CertificatePath string
	PrivateKeyPath  string
	Fingerprint     string
	Issuer          string
	IssuedAt        time.Time
	ExpiresAt       time.Time
}

// MarkIssued records a certificate the Agent has confirmed.
//
// The status is derived from the expiry the certificate itself carries rather
// than assumed to be valid: a certificate can be issued already inside its
// renewal window, and calling that "valid" would hide it from the sweep.
func (r *Repository) MarkIssued(ctx context.Context, params IssuedParams, now time.Time) error {
	domains, err := json.Marshal(params.Domains)
	if err != nil {
		return fmt.Errorf("encode certificate domains: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		UPDATE ssl_certificates
		SET provider         = $2,
		    domains          = $3,
		    certificate_path = nullif($4, ''),
		    private_key_path = nullif($5, ''),
		    fingerprint      = nullif($6, ''),
		    issuer           = nullif($7, ''),
		    issued_at        = $8,
		    expires_at       = $9,
		    status           = $10,
		    last_error       = NULL,
		    updated_at       = now()
		WHERE website_id = $1::uuid`,
		params.WebsiteID, params.Provider, domains,
		params.CertificatePath, params.PrivateKeyPath,
		params.Fingerprint, params.Issuer,
		params.IssuedAt, params.ExpiresAt,
		StatusFor(params.ExpiresAt, now))
	if err != nil {
		return fmt.Errorf("record issued certificate: %w", err)
	}
	return nil
}

// StatusFor derives a lifecycle state from an expiry date.
func StatusFor(expiresAt, now time.Time) string {
	switch {
	case expiresAt.Before(now):
		return StatusExpired
	case expiresAt.Sub(now) <= ExpiringWithin:
		return StatusExpiring
	default:
		return StatusValid
	}
}

// SetStatus records a lifecycle state and an optional reason.
//
// The reason is shown to the user, so callers pass something safe to display
// rather than an internal error string.
func (r *Repository) SetStatus(ctx context.Context, websiteID, status, reason string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE ssl_certificates
		SET status = $2, last_error = nullif($3, ''), updated_at = now()
		WHERE website_id = $1::uuid`, websiteID, status, reason)
	if err != nil {
		return fmt.Errorf("update certificate status: %w", err)
	}
	return nil
}

// SetAutoRenew turns automatic renewal on or off.
func (r *Repository) SetAutoRenew(ctx context.Context, websiteID string, enabled bool) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE ssl_certificates SET auto_renew = $2, updated_at = now()
		WHERE website_id = $1::uuid`, websiteID, enabled)
	if err != nil {
		return fmt.Errorf("update auto renew: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRenewalAttempted records that the sweep tried this certificate.
//
// Without it a certificate that fails to renew is retried on every sweep,
// which for Let's Encrypt means walking straight into a rate limit.
func (r *Repository) MarkRenewalAttempted(ctx context.Context, websiteID string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE ssl_certificates SET last_renewal_attempt = now(), updated_at = now()
		WHERE website_id = $1::uuid`, websiteID)
	if err != nil {
		return fmt.Errorf("record renewal attempt: %w", err)
	}
	return nil
}

// Get returns a website's certificate.
func (r *Repository) Get(ctx context.Context, websiteID string) (Certificate, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM ssl_certificates WHERE website_id = $1::uuid`, websiteID)

	certificate, err := scan(row, false)
	if errors.Is(err, pgx.ErrNoRows) {
		return Certificate{}, ErrNotFound
	}
	if err != nil {
		return Certificate{}, fmt.Errorf("select certificate: %w", err)
	}
	return certificate, nil
}

// List returns every certificate, soonest to expire first.
//
// That ordering is the point of the page: what needs attention is what runs
// out first, and a list sorted by name buries it.
func (r *Repository) List(ctx context.Context) ([]Certificate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+joinedColumns+`, w.primary_domain
		FROM ssl_certificates c
		JOIN websites w ON w.id = c.website_id
		ORDER BY c.expires_at ASC NULLS LAST, w.primary_domain`)
	if err != nil {
		return nil, fmt.Errorf("select certificates: %w", err)
	}
	defer rows.Close()

	certificates := []Certificate{}
	for rows.Next() {
		certificate, err := scan(rows, true)
		if err != nil {
			return nil, fmt.Errorf("scan certificate: %w", err)
		}
		certificates = append(certificates, certificate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate certificates: %w", err)
	}
	return certificates, nil
}

// DueForRenewal returns certificates the sweep should renew.
//
// A certificate is due when it is inside the renewal window, automatic renewal
// is on, and it has not been attempted recently. The retry gap is what keeps a
// persistently failing certificate from being retried every few minutes.
func (r *Repository) DueForRenewal(ctx context.Context, within, retryAfter time.Duration) ([]Certificate, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+joinedColumns+`, w.primary_domain
		FROM ssl_certificates c
		JOIN websites w ON w.id = c.website_id
		WHERE c.auto_renew = TRUE
		  AND c.expires_at IS NOT NULL
		  AND c.expires_at <= now() + $1::interval
		  AND c.status NOT IN ('revoked', 'issuing', 'pending')
		  AND (c.last_renewal_attempt IS NULL
		       OR c.last_renewal_attempt < now() - $2::interval)
		ORDER BY c.expires_at ASC`, within, retryAfter)
	if err != nil {
		return nil, fmt.Errorf("select renewable certificates: %w", err)
	}
	defer rows.Close()

	certificates := []Certificate{}
	for rows.Next() {
		certificate, err := scan(rows, true)
		if err != nil {
			return nil, fmt.Errorf("scan certificate: %w", err)
		}
		certificates = append(certificates, certificate)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate renewable certificates: %w", err)
	}
	return certificates, nil
}

// RefreshStatuses moves certificates between valid, expiring, and expired.
//
// Time passing is the only input, so nothing else triggers it: a certificate
// that was valid yesterday and expires today has to change state without
// anyone touching it.
func (r *Repository) RefreshStatuses(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE ssl_certificates
		SET status = CASE
		        WHEN expires_at < now() THEN 'expired'
		        WHEN expires_at <= now() + $1::interval THEN 'expiring'
		        ELSE 'valid'
		    END,
		    updated_at = now()
		WHERE expires_at IS NOT NULL
		  AND status IN ('valid', 'expiring', 'expired')
		  AND status <> CASE
		        WHEN expires_at < now() THEN 'expired'
		        WHEN expires_at <= now() + $1::interval THEN 'expiring'
		        ELSE 'valid'
		    END`, ExpiringWithin)
	if err != nil {
		return 0, fmt.Errorf("refresh certificate statuses: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Delete removes a website's certificate record.
func (r *Repository) Delete(ctx context.Context, websiteID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM ssl_certificates WHERE website_id = $1::uuid`, websiteID)
	if err != nil {
		return fmt.Errorf("delete certificate: %w", err)
	}
	return nil
}

// SetWebsiteSSL records whether a website serves HTTPS and redirects to it.
func (r *Repository) SetWebsiteSSL(ctx context.Context, websiteID string, enabled, redirect bool) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE websites SET ssl_enabled = $2, https_redirect = $3, updated_at = now()
		WHERE id = $1::uuid`, websiteID, enabled, redirect)
	if err != nil {
		return fmt.Errorf("update website ssl: %w", err)
	}
	return nil
}
