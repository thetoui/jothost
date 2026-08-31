// Package ftp is the panel's record of the host's FTP accounts.
//
// The rows here describe intent; proftpd's password file is what actually
// authenticates, and the Agent owns that. The relationship is one-way and
// deliberate, and it is the same arrangement the cron package uses: after every
// change the panel hands the Agent the *complete* set of accounts and the Agent
// makes the host match it. Nothing merges, nothing is incremental, and the
// password file therefore cannot drift into saying something the database does
// not.
//
// There is no password column. The panel writes a password to the host once,
// into proftpd's own hashed file, and then does not have it. Storing one —
// even encrypted — would put a copy of every customer's FTP credential in a
// database that is backed up, replicated, and read by every part of the panel
// that touches this table. "Show me the password" is answered by setting a new
// one.
package ftp

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
	ErrNotFound = errors.New("FTP account not found")
	// ErrDuplicateName means the host already has an account with that name.
	//
	// The host, not the website: proftpd's password file has a single
	// namespace, so two websites cannot each have a "backup" account.
	ErrDuplicateName = errors.New("this server already has an FTP account with that name")
)

// User is one FTP account as the panel records it.
type User struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id"`

	Username string `json:"username"`
	// HomeSubpath is relative to the website's document root. Empty means the
	// document root itself.
	HomeSubpath string `json:"home_subpath"`
	AccessLevel string `json:"access_level"`
	QuotaMB     int    `json:"quota_mb"`
	Suspended   bool   `json:"suspended"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Joined from the website. The account is what sessions run as, and showing
	// it is how an operator sees that FTP is not a login to the machine.
	WebsiteDomain string `json:"website_domain,omitempty"`
	SystemUser    string `json:"system_user,omitempty"`
	DocumentRoot  string `json:"document_root,omitempty"`

	// Home is the absolute directory the account is confined to, computed from
	// the document root and the subpath. Computed rather than stored: a stored
	// copy would be wrong the moment a website's document root moved.
	Home string `json:"home"`

	// UsedMB and Locked come from the host, not from this table, and are filled
	// in by the service from the Agent's status. They are zero here.
	UsedMB float64 `json:"used_mb"`
	Locked bool    `json:"locked"`
	// MissingOnHost reports an account the panel has recorded that the FTP
	// server does not have. It happens when the host's password file is lost —
	// a rebuilt machine, a restore — and it cannot be repaired automatically,
	// because nothing anywhere holds the password. Setting a new one puts the
	// account back.
	MissingOnHost bool `json:"missing_on_host"`
}

// Settings are the FTP server's own options for one host.
type Settings struct {
	ServerID    string `json:"server_id"`
	PassiveFrom int    `json:"passive_from"`
	PassiveTo   int    `json:"passive_to"`
	// TLSWebsiteID names the website whose certificate FTPS presents. Empty
	// means FTPS is not offered.
	TLSWebsiteID      string `json:"tls_website_id"`
	RequireTLS        bool   `json:"require_tls"`
	MasqueradeAddress string `json:"masquerade_address"`
	MaxClients        int    `json:"max_clients"`

	// TLSDomain is joined from that website, for the page to show.
	TLSDomain string `json:"tls_domain,omitempty"`
}

// DefaultSettings are what a host gets before anybody has chosen.
//
// The passive range is a hundred ports well above the privileged range and
// above what a panel-managed host already uses. It is a default rather than an
// absence because there is no workable "no range": without one every passive
// transfer on a firewalled host hangs.
func DefaultSettings(serverID string) Settings {
	return Settings{
		ServerID:    serverID,
		PassiveFrom: 30000,
		PassiveTo:   30100,
	}
}

// Repository reads and writes FTP records.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// CreateParams describe an account to record.
type CreateParams struct {
	ServerID    string
	WebsiteID   string
	Username    string
	HomeSubpath string
	AccessLevel string
	QuotaMB     int
}

// Create writes an account row.
func (r *Repository) Create(ctx context.Context, params CreateParams) (User, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO ftp_users
			(server_id, website_id, username, home_subpath, access_level, quota_mb)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6)
		RETURNING `+userColumns,
		params.ServerID, params.WebsiteID, params.Username,
		params.HomeSubpath, params.AccessLevel, params.QuotaMB)

	user, err := scanUser(row)
	if err != nil {
		return User{}, translateConstraint(err)
	}
	return user, nil
}

// UpdateParams describe a change to an account.
//
// The website cannot change, and neither can the name. An account confined to a
// different site is a different account, and renaming one in proftpd's password
// file is a delete and a create with a password nobody has.
type UpdateParams struct {
	HomeSubpath *string
	AccessLevel *string
	QuotaMB     *int
	Suspended   *bool
}

// Update applies changes to an account.
func (r *Repository) Update(ctx context.Context, id string, params UpdateParams) (User, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE ftp_users SET
			home_subpath = COALESCE($2, home_subpath),
			access_level = COALESCE($3, access_level),
			quota_mb     = COALESCE($4, quota_mb),
			suspended    = COALESCE($5, suspended),
			updated_at   = now()
		WHERE id = $1::uuid
		RETURNING `+userColumns,
		id, params.HomeSubpath, params.AccessLevel, params.QuotaMB, params.Suspended)

	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, translateConstraint(err)
	}
	return user, nil
}

// Get returns one account.
func (r *Repository) Get(ctx context.Context, id string) (User, error) {
	row := r.pool.QueryRow(ctx, selectUsers+` WHERE f.id = $1::uuid`, id)

	user, err := scanUserWithJoins(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrNotFound
		}
		return User{}, fmt.Errorf("select FTP account: %w", err)
	}
	return user, nil
}

// List returns every account on a host.
//
// Ordered by name rather than by creation, because this is also the set handed
// to the Agent, and a stable order makes two reconciles of an unchanged panel
// produce byte-identical configuration.
func (r *Repository) List(ctx context.Context, serverID string) ([]User, error) {
	return r.query(ctx, selectUsers+` WHERE f.server_id = $1::uuid
		ORDER BY f.username`, serverID)
}

// ListForWebsite returns one website's accounts.
func (r *Repository) ListForWebsite(ctx context.Context, websiteID string) ([]User, error) {
	return r.query(ctx, selectUsers+` WHERE f.website_id = $1::uuid
		ORDER BY f.username`, websiteID)
}

// Delete removes an account record.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM ftp_users WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete FTP account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Settings returns a host's FTP settings, or the defaults where none are
// recorded.
//
// A missing row is not an error. A host that has never had FTP configured has
// no row, and returning the defaults is what lets the page render before
// anything has been saved.
func (r *Repository) Settings(ctx context.Context, serverID string) (Settings, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT s.server_id, s.passive_from, s.passive_to,
		       COALESCE(s.tls_website_id::text, ''), s.require_tls,
		       s.masquerade_address, s.max_clients,
		       COALESCE(w.primary_domain, '')
		FROM ftp_settings s
		LEFT JOIN websites w ON w.id = s.tls_website_id
		WHERE s.server_id = $1::uuid`, serverID)

	var settings Settings
	err := row.Scan(&settings.ServerID, &settings.PassiveFrom, &settings.PassiveTo,
		&settings.TLSWebsiteID, &settings.RequireTLS,
		&settings.MasqueradeAddress, &settings.MaxClients, &settings.TLSDomain)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DefaultSettings(serverID), nil
		}
		return Settings{}, fmt.Errorf("select FTP settings: %w", err)
	}
	return settings, nil
}

// SaveSettings records a host's FTP settings.
func (r *Repository) SaveSettings(ctx context.Context, settings Settings) (Settings, error) {
	// A nil rather than an empty string, so the foreign key is satisfied when
	// no certificate is bound.
	var website any
	if settings.TLSWebsiteID != "" {
		website = settings.TLSWebsiteID
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO ftp_settings
			(server_id, passive_from, passive_to, tls_website_id, require_tls,
			 masquerade_address, max_clients)
		VALUES ($1::uuid, $2, $3, $4::uuid, $5, $6, $7)
		ON CONFLICT (server_id) DO UPDATE SET
			passive_from       = EXCLUDED.passive_from,
			passive_to         = EXCLUDED.passive_to,
			tls_website_id     = EXCLUDED.tls_website_id,
			require_tls        = EXCLUDED.require_tls,
			masquerade_address = EXCLUDED.masquerade_address,
			max_clients        = EXCLUDED.max_clients,
			updated_at         = now()`,
		settings.ServerID, settings.PassiveFrom, settings.PassiveTo, website,
		settings.RequireTLS, settings.MasqueradeAddress, settings.MaxClients)
	if err != nil {
		return Settings{}, fmt.Errorf("write FTP settings: %w", err)
	}
	return r.Settings(ctx, settings.ServerID)
}

func (r *Repository) query(ctx context.Context, sql string, args ...any) ([]User, error) {
	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("select FTP accounts: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, 8)
	for rows.Next() {
		user, err := scanUserWithJoins(rows)
		if err != nil {
			return nil, fmt.Errorf("scan FTP account: %w", err)
		}
		users = append(users, user)
	}
	return users, rows.Err()
}

const userColumns = `id, server_id, website_id, username, home_subpath,
	access_level, quota_mb, suspended, created_at, updated_at`

const selectUsers = `
	SELECT f.id, f.server_id, f.website_id, f.username, f.home_subpath,
	       f.access_level, f.quota_mb, f.suspended, f.created_at, f.updated_at,
	       w.primary_domain, w.system_username, w.document_root
	FROM ftp_users f
	JOIN websites w ON w.id = f.website_id`

type scanner interface {
	Scan(dest ...any) error
}

func scanUser(row scanner) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.ServerID, &user.WebsiteID, &user.Username,
		&user.HomeSubpath, &user.AccessLevel, &user.QuotaMB, &user.Suspended,
		&user.CreatedAt, &user.UpdatedAt)
	return user, err
}

func scanUserWithJoins(row scanner) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.ServerID, &user.WebsiteID, &user.Username,
		&user.HomeSubpath, &user.AccessLevel, &user.QuotaMB, &user.Suspended,
		&user.CreatedAt, &user.UpdatedAt,
		&user.WebsiteDomain, &user.SystemUser, &user.DocumentRoot)
	if err != nil {
		return user, err
	}
	user.Home = AbsoluteHome(user.DocumentRoot, user.HomeSubpath)
	return user, nil
}

// AbsoluteHome resolves an account's home against its website's document root.
//
// The subpath has already been validated — no leading slash, no ".." — both in
// Go and by a CHECK constraint, so this only joins. It does not clean, because
// cleaning a path that should never have needed it is how an escape becomes a
// silent redirection instead of a refusal.
func AbsoluteHome(documentRoot, subpath string) string {
	if subpath == "" {
		return documentRoot
	}
	return strings.TrimRight(documentRoot, "/") + "/" + subpath
}

// translateConstraint turns a unique-index violation into the error that says
// which one, so the caller can explain it rather than reporting "conflict".
func translateConstraint(err error) error {
	var pgErr interface {
		SQLState() string
		Error() string
	}
	if !errors.As(err, &pgErr) {
		return fmt.Errorf("write FTP account: %w", err)
	}
	if pgErr.SQLState() == "23505" && strings.Contains(pgErr.Error(), "ftp_users_username_idx") {
		return ErrDuplicateName
	}
	return fmt.Errorf("write FTP account: %w", err)
}
