package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Errors returned by the repository.
var (
	// ErrNotFound covers a row that is not there.
	ErrNotFound = errors.New("not found")
	// ErrDuplicate covers a domain, mailbox or forwarder that already exists.
	ErrDuplicate = errors.New("already exists")
)

// Repository reads and writes the mail tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// Settings are the mail server's own options, as the panel records them.
type Settings struct {
	ServerID string `json:"server_id"`

	Enabled  bool   `json:"enabled"`
	Hostname string `json:"hostname"`

	TLSWebsiteID string `json:"tls_website_id,omitempty"`
	RequireTLS   bool   `json:"require_tls"`

	SpamEnabled     bool `json:"spam_enabled"`
	SpamRejectScore int  `json:"spam_reject_score"`
	VirusEnabled    bool `json:"virus_enabled"`

	MaxMessageMB int `json:"max_message_mb"`

	WebmailWebsiteID string `json:"webmail_website_id,omitempty"`
	WebmailVersion   string `json:"webmail_version,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Domain is one domain this host accepts mail for.
type Domain struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id,omitempty"`

	Domain   string `json:"domain"`
	Active   bool   `json:"active"`
	CatchAll string `json:"catch_all"`

	DKIMSelector string `json:"dkim_selector"`
	// DKIMPublicKey is the base64 body of the published record. The private
	// half is not here and is not in this database at all: see
	// docs/PHASE26.md.
	DKIMPublicKey string     `json:"dkim_public_key,omitempty"`
	DKIMCreatedAt *time.Time `json:"dkim_created_at,omitempty"`

	SPFPolicy   string `json:"spf_policy"`
	DMARCPolicy string `json:"dmarc_policy"`
	DMARCRua    string `json:"dmarc_rua"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Mailbox is one address that receives and can log in.
type Mailbox struct {
	ID        string `json:"id"`
	DomainID  string `json:"domain_id"`
	LocalPart string `json:"local_part"`
	QuotaMB   int    `json:"quota_mb"`
	Active    bool   `json:"active"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// passwordHash is deliberately unexported and has no JSON tag.
	//
	// It is read by the reconcile path, which needs it to write Dovecot's
	// passwd-file, and it must never reach an HTTP response — a hash is not a
	// credential, but publishing every mailbox's hash over an API would turn
	// one leaked reply into an offline attack against every mailbox on the
	// host. The struct not having a tag is the enforcement.
	passwordHash string
}

// PasswordHash returns the stored hash, for the reconcile path only.
func (m Mailbox) PasswordHash() string { return m.passwordHash }

// Alias forwards mail from one local part to another address.
type Alias struct {
	ID          string    `json:"id"`
	DomainID    string    `json:"domain_id"`
	Source      string    `json:"source"`
	Destination string    `json:"destination"`
	Active      bool      `json:"active"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Autoresponder is a mailbox's vacation reply.
type Autoresponder struct {
	MailboxID    string     `json:"mailbox_id"`
	Subject      string     `json:"subject"`
	Body         string     `json:"body"`
	StartsAt     *time.Time `json:"starts_at,omitempty"`
	EndsAt       *time.Time `json:"ends_at,omitempty"`
	IntervalDays int        `json:"interval_days"`
	Active       bool       `json:"active"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

const settingsColumns = `
	server_id, enabled, hostname, COALESCE(tls_website_id::text, ''), require_tls,
	spam_enabled, spam_reject_score, virus_enabled, max_message_mb,
	COALESCE(webmail_website_id::text, ''), webmail_version, created_at, updated_at`

// Settings reads the mail settings, creating the row on first use.
//
// Created rather than returned empty, because the defaults in the migration are
// the panel's actual policy — TLS required, spam filtering on, virus scanning
// off — and a caller reading a zero-valued struct would see the opposite of all
// three.
func (r *Repository) Settings(ctx context.Context, serverID string) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		WITH created AS (
			INSERT INTO mail_settings (server_id) VALUES ($1::uuid)
			ON CONFLICT (server_id) DO NOTHING
			RETURNING `+settingsColumns+`
		)
		SELECT * FROM created
		UNION ALL
		SELECT `+settingsColumns+` FROM mail_settings WHERE server_id = $1::uuid
		LIMIT 1`, serverID)
	return scanSettings(row)
}

// SaveSettings writes the mail settings.
func (r *Repository) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		WITH saved AS (
			INSERT INTO mail_settings (
				server_id, enabled, hostname, tls_website_id, require_tls,
				spam_enabled, spam_reject_score, virus_enabled, max_message_mb
			) VALUES (
				$1::uuid, $2, $3, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9
			)
			ON CONFLICT (server_id) DO UPDATE SET
				enabled           = EXCLUDED.enabled,
				hostname          = EXCLUDED.hostname,
				tls_website_id    = EXCLUDED.tls_website_id,
				require_tls       = EXCLUDED.require_tls,
				spam_enabled      = EXCLUDED.spam_enabled,
				spam_reject_score = EXCLUDED.spam_reject_score,
				virus_enabled     = EXCLUDED.virus_enabled,
				max_message_mb    = EXCLUDED.max_message_mb,
				updated_at        = now()
			RETURNING `+settingsColumns+`
		)
		SELECT * FROM saved`,
		settings.ServerID, settings.Enabled, settings.Hostname, settings.TLSWebsiteID,
		settings.RequireTLS, settings.SpamEnabled, settings.SpamRejectScore,
		settings.VirusEnabled, settings.MaxMessageMB)
	return scanSettings(row)
}

// SaveWebmail records where webmail was installed.
//
// Its own method rather than a field on SaveSettings, because webmail is
// installed by a long-running job and the settings form is saved by a person.
// One overwriting the other is how "I installed webmail and the panel forgot"
// happens.
func (r *Repository) SaveWebmail(ctx context.Context, serverID, websiteID, version string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO mail_settings (server_id, webmail_website_id, webmail_version)
		VALUES ($1::uuid, NULLIF($2, '')::uuid, $3)
		ON CONFLICT (server_id) DO UPDATE SET
			webmail_website_id = EXCLUDED.webmail_website_id,
			webmail_version    = EXCLUDED.webmail_version,
			updated_at         = now()`,
		serverID, websiteID, version)
	if err != nil {
		return fmt.Errorf("record the webmail installation: %w", err)
	}
	return nil
}

func scanSettings(row pgx.Row) (Settings, error) {
	var settings Settings
	err := row.Scan(&settings.ServerID, &settings.Enabled, &settings.Hostname,
		&settings.TLSWebsiteID, &settings.RequireTLS, &settings.SpamEnabled,
		&settings.SpamRejectScore, &settings.VirusEnabled, &settings.MaxMessageMB,
		&settings.WebmailWebsiteID, &settings.WebmailVersion,
		&settings.CreatedAt, &settings.UpdatedAt)
	if err != nil {
		return Settings{}, fmt.Errorf("read the mail settings: %w", err)
	}
	return settings, nil
}

const domainColumns = `
	id, server_id, COALESCE(website_id::text, ''), domain, active, catch_all,
	dkim_selector, dkim_public_key, dkim_created_at,
	spf_policy, dmarc_policy, dmarc_rua, created_at, updated_at`

// CreateDomain records a domain this host accepts mail for.
func (r *Repository) CreateDomain(ctx context.Context, domain Domain) (Domain, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mail_domains (
				server_id, website_id, domain, active, catch_all,
				spf_policy, dmarc_policy, dmarc_rua
			) VALUES ($1::uuid, NULLIF($2, '')::uuid, $3, $4, $5, $6, $7, $8)
			RETURNING `+domainColumns+`
		)
		SELECT * FROM inserted`,
		domain.ServerID, domain.WebsiteID, domain.Domain, domain.Active,
		domain.CatchAll, domain.SPFPolicy, domain.DMARCPolicy, domain.DMARCRua)
	created, err := scanDomain(row)
	return created, mapWriteError(err, "mail domain")
}

// UpdateDomain changes a domain's settings.
func (r *Repository) UpdateDomain(ctx context.Context, id string, domain Domain) (Domain, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE mail_domains SET
				website_id   = NULLIF($2, '')::uuid,
				active       = $3,
				catch_all    = $4,
				spf_policy   = $5,
				dmarc_policy = $6,
				dmarc_rua    = $7,
				updated_at   = now()
			WHERE id = $1::uuid
			RETURNING `+domainColumns+`
		)
		SELECT * FROM updated`,
		id, domain.WebsiteID, domain.Active, domain.CatchAll,
		domain.SPFPolicy, domain.DMARCPolicy, domain.DMARCRua)
	updated, err := scanDomain(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return updated, mapWriteError(err, "mail domain")
}

// SaveDKIM records the public half of a domain's signing key.
//
// Both fields together, always. A selector with no key is a record that would
// be published empty, and a key with no selector is one nothing can find — the
// schema refuses either half alone, and so does this.
func (r *Repository) SaveDKIM(ctx context.Context, id, selector, publicKey string) (Domain, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE mail_domains SET
				dkim_selector   = $2::text,
				dkim_public_key = $3,
				dkim_created_at = CASE WHEN $2::text = '' THEN NULL ELSE now() END,
				updated_at      = now()
			WHERE id = $1::uuid
			RETURNING `+domainColumns+`
		)
		SELECT * FROM updated`, id, selector, publicKey)
	updated, err := scanDomain(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return updated, err
}

// GetDomain reads one domain.
func (r *Repository) GetDomain(ctx context.Context, id string) (Domain, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+domainColumns+` FROM mail_domains d WHERE id = $1::uuid`, id)
	domain, err := scanDomain(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Domain{}, ErrNotFound
	}
	return domain, err
}

// ListDomains reads every domain on a host.
func (r *Repository) ListDomains(ctx context.Context, serverID string) ([]Domain, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+domainColumns+` FROM mail_domains d
		 WHERE server_id = $1::uuid ORDER BY domain`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the mail domains: %w", err)
	}
	defer rows.Close()

	domains := []Domain{}
	for rows.Next() {
		domain, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		domains = append(domains, domain)
	}
	return domains, rows.Err()
}

// DeleteDomain removes a domain, its mailboxes and its forwarders.
func (r *Repository) DeleteDomain(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_domains WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete the mail domain: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanDomain(row pgx.Row) (Domain, error) {
	var domain Domain
	err := row.Scan(&domain.ID, &domain.ServerID, &domain.WebsiteID, &domain.Domain,
		&domain.Active, &domain.CatchAll, &domain.DKIMSelector, &domain.DKIMPublicKey,
		&domain.DKIMCreatedAt, &domain.SPFPolicy, &domain.DMARCPolicy, &domain.DMARCRua,
		&domain.CreatedAt, &domain.UpdatedAt)
	if err != nil {
		return Domain{}, err
	}
	return domain, nil
}

const mailboxColumns = `
	id, domain_id, local_part, password_hash, quota_mb, active, created_at, updated_at`

// CreateMailbox records a mailbox.
func (r *Repository) CreateMailbox(ctx context.Context, box Mailbox, hash string) (Mailbox, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mailboxes (domain_id, local_part, password_hash, quota_mb, active)
			VALUES ($1::uuid, $2, $3, $4, $5)
			RETURNING `+mailboxColumns+`
		)
		SELECT * FROM inserted`,
		box.DomainID, box.LocalPart, hash, box.QuotaMB, box.Active)
	created, err := scanMailbox(row)
	return created, mapWriteError(err, "mailbox")
}

// UpdateMailbox changes a mailbox's quota and whether it is active.
//
// Not its address. Renaming a mailbox would leave its Maildir under the old
// name and its mail unreachable, and the panel has no way to move a Maildir
// while Dovecot may be writing into it. "Rename" is create, forward, delete —
// three deliberate acts.
func (r *Repository) UpdateMailbox(ctx context.Context, id string, quotaMB int,
	active bool,
) (Mailbox, error) {
	row := r.pool.QueryRow(ctx, `
		WITH updated AS (
			UPDATE mailboxes SET quota_mb = $2, active = $3, updated_at = now()
			WHERE id = $1::uuid
			RETURNING `+mailboxColumns+`
		)
		SELECT * FROM updated`, id, quotaMB, active)
	updated, err := scanMailbox(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mailbox{}, ErrNotFound
	}
	return updated, err
}

// SetPassword replaces a mailbox's password hash.
func (r *Repository) SetPassword(ctx context.Context, id, hash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE mailboxes SET password_hash = $2, updated_at = now() WHERE id = $1::uuid`,
		id, hash)
	if err != nil {
		return fmt.Errorf("set the mailbox password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetMailbox reads one mailbox.
func (r *Repository) GetMailbox(ctx context.Context, id string) (Mailbox, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes m WHERE id = $1::uuid`, id)
	box, err := scanMailbox(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Mailbox{}, ErrNotFound
	}
	return box, err
}

// ListMailboxes reads a domain's mailboxes.
func (r *Repository) ListMailboxes(ctx context.Context, domainID string) ([]Mailbox, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+mailboxColumns+` FROM mailboxes m
		 WHERE domain_id = $1::uuid ORDER BY local_part`, domainID)
	if err != nil {
		return nil, fmt.Errorf("list the mailboxes: %w", err)
	}
	defer rows.Close()

	boxes := []Mailbox{}
	for rows.Next() {
		box, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		boxes = append(boxes, box)
	}
	return boxes, rows.Err()
}

// DeleteMailbox removes a mailbox.
func (r *Repository) DeleteMailbox(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mailboxes WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete the mailbox: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanMailbox(row pgx.Row) (Mailbox, error) {
	var box Mailbox
	err := row.Scan(&box.ID, &box.DomainID, &box.LocalPart, &box.passwordHash,
		&box.QuotaMB, &box.Active, &box.CreatedAt, &box.UpdatedAt)
	if err != nil {
		return Mailbox{}, err
	}
	return box, nil
}

const aliasColumns = `id, domain_id, source, destination, active, created_at, updated_at`

// CreateAlias records a forwarder.
func (r *Repository) CreateAlias(ctx context.Context, alias Alias) (Alias, error) {
	row := r.pool.QueryRow(ctx, `
		WITH inserted AS (
			INSERT INTO mail_aliases (domain_id, source, destination, active)
			VALUES ($1::uuid, $2, $3, $4)
			RETURNING `+aliasColumns+`
		)
		SELECT * FROM inserted`,
		alias.DomainID, alias.Source, alias.Destination, alias.Active)
	created, err := scanAlias(row)
	return created, mapWriteError(err, "forwarder")
}

// ListAliases reads a domain's forwarders.
func (r *Repository) ListAliases(ctx context.Context, domainID string) ([]Alias, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+aliasColumns+` FROM mail_aliases a
		 WHERE domain_id = $1::uuid ORDER BY source, destination`, domainID)
	if err != nil {
		return nil, fmt.Errorf("list the forwarders: %w", err)
	}
	defer rows.Close()

	aliases := []Alias{}
	for rows.Next() {
		alias, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

// GetAlias reads one forwarder.
func (r *Repository) GetAlias(ctx context.Context, id string) (Alias, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+aliasColumns+` FROM mail_aliases a WHERE id = $1::uuid`, id)
	alias, err := scanAlias(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Alias{}, ErrNotFound
	}
	return alias, err
}

// DeleteAlias removes a forwarder.
func (r *Repository) DeleteAlias(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM mail_aliases WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete the forwarder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanAlias(row pgx.Row) (Alias, error) {
	var alias Alias
	err := row.Scan(&alias.ID, &alias.DomainID, &alias.Source, &alias.Destination,
		&alias.Active, &alias.CreatedAt, &alias.UpdatedAt)
	if err != nil {
		return Alias{}, err
	}
	return alias, nil
}

const responderColumns = `
	mailbox_id, subject, body, starts_at, ends_at, interval_days, active,
	created_at, updated_at`

// SaveAutoresponder writes a mailbox's vacation reply.
func (r *Repository) SaveAutoresponder(ctx context.Context,
	responder Autoresponder,
) (Autoresponder, error) {
	row := r.pool.QueryRow(ctx, `
		WITH saved AS (
			INSERT INTO mail_autoresponders (
				mailbox_id, subject, body, starts_at, ends_at, interval_days, active
			) VALUES ($1::uuid, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (mailbox_id) DO UPDATE SET
				subject       = EXCLUDED.subject,
				body          = EXCLUDED.body,
				starts_at     = EXCLUDED.starts_at,
				ends_at       = EXCLUDED.ends_at,
				interval_days = EXCLUDED.interval_days,
				active        = EXCLUDED.active,
				updated_at    = now()
			RETURNING `+responderColumns+`
		)
		SELECT * FROM saved`,
		responder.MailboxID, responder.Subject, responder.Body, responder.StartsAt,
		responder.EndsAt, responder.IntervalDays, responder.Active)
	return scanResponder(row)
}

// GetAutoresponder reads one mailbox's vacation reply.
func (r *Repository) GetAutoresponder(ctx context.Context, mailboxID string) (Autoresponder, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+responderColumns+` FROM mail_autoresponders a WHERE mailbox_id = $1::uuid`,
		mailboxID)
	responder, err := scanResponder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Autoresponder{}, ErrNotFound
	}
	return responder, err
}

// DeleteAutoresponder removes a vacation reply.
func (r *Repository) DeleteAutoresponder(ctx context.Context, mailboxID string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM mail_autoresponders WHERE mailbox_id = $1::uuid`, mailboxID)
	if err != nil {
		return fmt.Errorf("delete the autoresponder: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListAutoresponders reads every vacation reply on a host, by mailbox.
//
// One query rather than one per mailbox: the reconcile path needs all of them
// and a host with a thousand mailboxes would otherwise make a thousand round
// trips to build one file.
func (r *Repository) ListAutoresponders(ctx context.Context,
	serverID string,
) (map[string]Autoresponder, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+responderColumnsJoined+`
		FROM mail_autoresponders a
		JOIN mailboxes m ON m.id = a.mailbox_id
		JOIN mail_domains d ON d.id = m.domain_id
		WHERE d.server_id = $1::uuid`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the autoresponders: %w", err)
	}
	defer rows.Close()

	responders := map[string]Autoresponder{}
	for rows.Next() {
		responder, err := scanResponder(rows)
		if err != nil {
			return nil, err
		}
		responders[responder.MailboxID] = responder
	}
	return responders, rows.Err()
}

func scanResponder(row pgx.Row) (Autoresponder, error) {
	var responder Autoresponder
	err := row.Scan(&responder.MailboxID, &responder.Subject, &responder.Body,
		&responder.StartsAt, &responder.EndsAt, &responder.IntervalDays,
		&responder.Active, &responder.CreatedAt, &responder.UpdatedAt)
	if err != nil {
		return Autoresponder{}, err
	}
	return responder, nil
}

// The same column lists, qualified for the queries that join.
//
// A bare "id" in a join against mail_domains is ambiguous, and Postgres says so
// rather than guessing — which is the right behaviour and the reason these
// exist separately: one list for the single-table reads, one for the joins, and
// neither able to drift into the other silently.
const (
	mailboxColumnsJoined = `
	m.id, m.domain_id, m.local_part, m.password_hash, m.quota_mb, m.active,
	m.created_at, m.updated_at`

	aliasColumnsJoined = `
	a.id, a.domain_id, a.source, a.destination, a.active, a.created_at, a.updated_at`

	responderColumnsJoined = `
	a.mailbox_id, a.subject, a.body, a.starts_at, a.ends_at, a.interval_days,
	a.active, a.created_at, a.updated_at`
)

// AllMailboxes reads every mailbox on a host, by domain.
//
// The reconcile path's query. Same reason as ListAutoresponders: the whole
// host's configuration is written in one act, so it is read in one.
func (r *Repository) AllMailboxes(ctx context.Context,
	serverID string,
) (map[string][]Mailbox, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+mailboxColumnsJoined+`
		FROM mailboxes m
		JOIN mail_domains d ON d.id = m.domain_id
		WHERE d.server_id = $1::uuid
		ORDER BY m.local_part`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the mailboxes: %w", err)
	}
	defer rows.Close()

	byDomain := map[string][]Mailbox{}
	for rows.Next() {
		box, err := scanMailbox(rows)
		if err != nil {
			return nil, err
		}
		byDomain[box.DomainID] = append(byDomain[box.DomainID], box)
	}
	return byDomain, rows.Err()
}

// AllAliases reads every forwarder on a host, by domain.
func (r *Repository) AllAliases(ctx context.Context, serverID string) (map[string][]Alias, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+aliasColumnsJoined+`
		FROM mail_aliases a
		JOIN mail_domains d ON d.id = a.domain_id
		WHERE d.server_id = $1::uuid
		ORDER BY a.source`, serverID)
	if err != nil {
		return nil, fmt.Errorf("list the forwarders: %w", err)
	}
	defer rows.Close()

	byDomain := map[string][]Alias{}
	for rows.Next() {
		alias, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		byDomain[alias.DomainID] = append(byDomain[alias.DomainID], alias)
	}
	return byDomain, rows.Err()
}

// CountMailboxes reports how many mailboxes each domain has.
func (r *Repository) CountMailboxes(ctx context.Context, serverID string) (map[string]int, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT m.domain_id, count(*)
		FROM mailboxes m
		JOIN mail_domains d ON d.id = m.domain_id
		WHERE d.server_id = $1::uuid
		GROUP BY m.domain_id`, serverID)
	if err != nil {
		return nil, fmt.Errorf("count the mailboxes: %w", err)
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var id string
		var count int
		if err := rows.Scan(&id, &count); err != nil {
			return nil, err
		}
		counts[id] = count
	}
	return counts, rows.Err()
}

// mapWriteError turns a constraint violation into an error a caller can act on.
//
// A unique violation here is nearly always a person creating something that is
// already there, and "already exists" is a better answer than a database error
// string — which would also be a way to leak the schema into an HTTP reply.
func mapWriteError(err error, what string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return fmt.Errorf("%w: this %s", ErrDuplicate, what)
		case "23503":
			return fmt.Errorf("%w: the %s it belongs to", ErrNotFound, what)
		case "23514":
			// A check constraint. The name says which rule was broken, and it
			// is more useful than the generic message: these constraints
			// mirror the validation in Go, so reaching one means a code path
			// skipped it.
			return fmt.Errorf("the %s is not valid (%s)", what,
				strings.TrimPrefix(pgErr.ConstraintName, "mail_"))
		}
	}
	return err
}
