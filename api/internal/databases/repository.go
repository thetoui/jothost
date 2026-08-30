// Package databases is the panel's record of the databases and accounts it
// manages on the host's database servers.
//
// As with websites, PHP, and certificates, these rows describe intent and the
// Agent owns what is actually on the servers. The one thing the panel is
// authoritative about is the account passwords: MySQL and PostgreSQL both keep
// only a hash, so if the panel does not store the password nobody can ever be
// shown it again.
package databases

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/secrets"
)

// Database lifecycle states, matching the CHECK constraint in migration 0008.
const (
	StatusCreating = "creating"
	StatusActive   = "active"
	StatusDeleting = "deleting"
	StatusFailed   = "failed"
)

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("database not found")
	// ErrUserNotFound distinguishes a missing account from a missing database:
	// the two produce different messages and different HTTP shapes.
	ErrUserNotFound = errors.New("database user not found")
	// ErrDuplicate means the name is already taken on that server and engine.
	ErrDuplicate = errors.New("a database with that name already exists on this engine")
	// ErrDuplicateUser means the account already exists.
	ErrDuplicateUser = errors.New("that database user already exists on this engine")
)

// Database is one managed database.
type Database struct {
	ID        string  `json:"id"`
	ServerID  string  `json:"server_id"`
	WebsiteID *string `json:"website_id"`
	Name      string  `json:"name"`
	Engine    string  `json:"engine"`
	Status    string  `json:"status"`

	Charset       *string    `json:"charset"`
	Collation     *string    `json:"collation"`
	SizeBytes     *int64     `json:"size_bytes"`
	SizeCheckedAt *time.Time `json:"size_checked_at"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// WebsiteDomain is joined from the website so a listing can name the site a
	// database belongs to without a query per row.
	WebsiteDomain *string `json:"website_domain"`
	// UserCount is how many accounts have a grant on it, so the panel can warn
	// before deleting something two applications share.
	UserCount int `json:"user_count"`
}

// User is one managed database account.
//
// The password is deliberately absent from this struct. It lives encrypted in
// the row and is decrypted only by RevealPassword, which is a separate,
// separately audited call — so an account cannot have its password leak into a
// listing endpoint by someone adding a field to a JSON response.
type User struct {
	ID        string    `json:"id"`
	ServerID  string    `json:"server_id"`
	Engine    string    `json:"engine"`
	Username  string    `json:"username"`
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// PasswordUpdatedAt lets the panel show how old a credential is without
	// showing the credential.
	PasswordUpdatedAt time.Time `json:"password_updated_at"`
	// Grants are the databases this account may reach, filled in by listings
	// that ask for them.
	Grants []Grant `json:"grants,omitempty"`
}

// Grant is one account's access to one database.
type Grant struct {
	DatabaseID   string `json:"database_id"`
	DatabaseName string `json:"database_name"`
	Privilege    string `json:"privilege"`
}

// Repository reads and writes the panel's database records.
type Repository struct {
	pool      *pgxpool.Pool
	encrypter *secrets.Encrypter
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool, encrypter *secrets.Encrypter) *Repository {
	return &Repository{pool: pool, encrypter: encrypter}
}

// passwordContext binds a ciphertext to the row that holds it.
//
// Without it, a ciphertext copied from one account's row into another's would
// decrypt successfully and hand out the wrong site's password. With it, the
// copy fails to authenticate and the theft is a decryption error.
func passwordContext(userID string) string { return "database_user:" + userID }

// --------------------------------------------------------------- databases

// CreateParams describe a database to record.
type CreateParams struct {
	ServerID  string
	WebsiteID *string
	Name      string
	Engine    string
	Status    string
}

// Create writes a database row.
func (r *Repository) Create(ctx context.Context, params CreateParams) (Database, error) {
	status := params.Status
	if status == "" {
		status = StatusCreating
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO databases (server_id, website_id, name, engine, status)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5)
		RETURNING id, server_id, website_id, name, engine, status,
		          charset, collation_name, size_bytes, size_checked_at, created_at, updated_at`,
		params.ServerID, params.WebsiteID, params.Name, params.Engine, status)

	database, err := scanDatabase(row)
	if err != nil {
		if isUniqueViolation(err) {
			return Database{}, ErrDuplicate
		}
		return Database{}, fmt.Errorf("insert database: %w", err)
	}
	return database, nil
}

// SetStatus moves a database between lifecycle states.
func (r *Repository) SetStatus(ctx context.Context, id, status string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE databases SET status = $2, updated_at = now() WHERE id = $1::uuid`, id, status)
	if err != nil {
		return fmt.Errorf("update database status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordSize stores a size the Agent measured.
func (r *Repository) RecordSize(ctx context.Context, id string, size int64) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE databases
		SET size_bytes = $2, size_checked_at = now(), updated_at = now()
		WHERE id = $1::uuid`, id, size)
	if err != nil {
		return fmt.Errorf("update database size: %w", err)
	}
	return nil
}

// RecordEncoding stores the character set and collation the server settled on.
//
// Empty values are left alone rather than written as empty strings: an engine
// that does not report them should leave the columns NULL, which the panel
// renders as "server default" instead of as a blank cell.
func (r *Repository) RecordEncoding(ctx context.Context, id, charset, collation string) error {
	if charset == "" && collation == "" {
		return nil
	}

	_, err := r.pool.Exec(ctx, `
		UPDATE databases
		SET charset = COALESCE(NULLIF($2, ''), charset),
		    collation_name = COALESCE(NULLIF($3, ''), collation_name),
		    updated_at = now()
		WHERE id = $1::uuid`, id, charset, collation)
	if err != nil {
		return fmt.Errorf("update database encoding: %w", err)
	}
	return nil
}

// SetWebsite changes which website a database belongs to.
//
// nil unassigns it. The link is a convenience for the panel's own navigation,
// not ownership: nothing about the database itself changes, and the database
// outlives whichever site it was pointed at.
func (r *Repository) SetWebsite(ctx context.Context, id string, websiteID *string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE databases SET website_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid`, id, websiteID)
	if err != nil {
		return fmt.Errorf("update database website: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one database.
func (r *Repository) Get(ctx context.Context, id string) (Database, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT d.id, d.server_id, d.website_id, d.name, d.engine, d.status,
		       d.charset, d.collation_name, d.size_bytes, d.size_checked_at,
		       d.created_at, d.updated_at, w.primary_domain,
		       (SELECT count(*) FROM database_permissions p WHERE p.database_id = d.id)
		FROM databases d
		LEFT JOIN websites w ON w.id = d.website_id
		WHERE d.id = $1::uuid`, id)

	database, err := scanDatabaseWithJoins(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Database{}, ErrNotFound
		}
		return Database{}, fmt.Errorf("select database: %w", err)
	}
	return database, nil
}

// List returns every managed database, newest first.
func (r *Repository) List(ctx context.Context) ([]Database, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT d.id, d.server_id, d.website_id, d.name, d.engine, d.status,
		       d.charset, d.collation_name, d.size_bytes, d.size_checked_at,
		       d.created_at, d.updated_at, w.primary_domain,
		       (SELECT count(*) FROM database_permissions p WHERE p.database_id = d.id)
		FROM databases d
		LEFT JOIN websites w ON w.id = d.website_id
		ORDER BY d.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("select databases: %w", err)
	}
	defer rows.Close()

	databases := make([]Database, 0, 16)
	for rows.Next() {
		database, err := scanDatabaseWithJoins(rows)
		if err != nil {
			return nil, fmt.Errorf("scan database: %w", err)
		}
		databases = append(databases, database)
	}
	return databases, rows.Err()
}

// Delete removes a database row.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM databases WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete database: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ----------------------------------------------------------- database users

// CreateUserParams describe an account to record.
type CreateUserParams struct {
	ServerID string
	Engine   string
	Username string
	Host     string
	Password string
}

// CreateUser writes an account row with its password encrypted.
//
// The insert and the encryption happen in one transaction because the
// ciphertext is bound to the row's own id: the id has to exist before the
// password can be sealed against it, and a failure between the two would leave
// an account whose password nobody can read.
func (r *Repository) CreateUser(ctx context.Context, params CreateUserParams) (User, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id string
	// A placeholder is written first because password_encrypted is NOT NULL and
	// the real ciphertext needs the id this insert is about to generate.
	err = tx.QueryRow(ctx, `
		INSERT INTO database_users (server_id, engine, username, host, password_encrypted)
		VALUES ($1::uuid, $2, $3, $4, '')
		RETURNING id`,
		params.ServerID, params.Engine, params.Username, params.Host).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicateUser
		}
		return User{}, fmt.Errorf("insert database user: %w", err)
	}

	sealed, err := r.encrypter.Encrypt([]byte(params.Password), passwordContext(id))
	if err != nil {
		return User{}, fmt.Errorf("encrypt database password: %w", err)
	}

	row := tx.QueryRow(ctx, `
		UPDATE database_users
		SET password_encrypted = $2, password_updated_at = now(), updated_at = now()
		WHERE id = $1::uuid
		RETURNING id, server_id, engine, username, host, created_at, updated_at, password_updated_at`,
		id, sealed)

	user, err := scanUser(row)
	if err != nil {
		return User{}, fmt.Errorf("store database password: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit: %w", err)
	}
	return user, nil
}

// SetPassword replaces an account's stored password.
func (r *Repository) SetPassword(ctx context.Context, id, password string) error {
	sealed, err := r.encrypter.Encrypt([]byte(password), passwordContext(id))
	if err != nil {
		return fmt.Errorf("encrypt database password: %w", err)
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE database_users
		SET password_encrypted = $2, password_updated_at = now(), updated_at = now()
		WHERE id = $1::uuid`, id, sealed)
	if err != nil {
		return fmt.Errorf("update database password: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// RevealPassword decrypts an account's password.
//
// It is a separate call from every listing so that showing a password is
// always a deliberate act with its own audit event, rather than something that
// happens as a side effect of loading a page.
func (r *Repository) RevealPassword(ctx context.Context, id string) (string, error) {
	var sealed string
	err := r.pool.QueryRow(ctx,
		`SELECT password_encrypted FROM database_users WHERE id = $1::uuid`, id).Scan(&sealed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrUserNotFound
		}
		return "", fmt.Errorf("select database password: %w", err)
	}

	plaintext, err := r.encrypter.Decrypt(sealed, passwordContext(id))
	if err != nil {
		return "", fmt.Errorf("decrypt database password: %w", err)
	}
	return string(plaintext), nil
}

// GetUser returns one account.
func (r *Repository) GetUser(ctx context.Context, id string) (User, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, server_id, engine, username, host, created_at, updated_at, password_updated_at
		FROM database_users WHERE id = $1::uuid`, id)

	user, err := scanUser(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return User{}, ErrUserNotFound
		}
		return User{}, fmt.Errorf("select database user: %w", err)
	}
	return user, nil
}

// ListUsers returns the accounts with a grant on one database.
func (r *Repository) ListUsers(ctx context.Context, databaseID string) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id, u.server_id, u.engine, u.username, u.host,
		       u.created_at, u.updated_at, u.password_updated_at, p.privilege
		FROM database_users u
		JOIN database_permissions p ON p.database_user_id = u.id
		WHERE p.database_id = $1::uuid
		ORDER BY u.username, u.host`, databaseID)
	if err != nil {
		return nil, fmt.Errorf("select database users: %w", err)
	}
	defer rows.Close()

	users := make([]User, 0, 4)
	for rows.Next() {
		var user User
		var privilege string
		if err := rows.Scan(&user.ID, &user.ServerID, &user.Engine, &user.Username, &user.Host,
			&user.CreatedAt, &user.UpdatedAt, &user.PasswordUpdatedAt, &privilege); err != nil {
			return nil, fmt.Errorf("scan database user: %w", err)
		}
		user.Grants = []Grant{{DatabaseID: databaseID, Privilege: privilege}}
		users = append(users, user)
	}
	return users, rows.Err()
}

// ListAllUsers returns every account on the server, with its grants.
func (r *Repository) ListAllUsers(ctx context.Context) ([]User, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT u.id, u.server_id, u.engine, u.username, u.host,
		       u.created_at, u.updated_at, u.password_updated_at,
		       COALESCE(d.id::text, ''), COALESCE(d.name, ''), COALESCE(p.privilege, '')
		FROM database_users u
		LEFT JOIN database_permissions p ON p.database_user_id = u.id
		LEFT JOIN databases d ON d.id = p.database_id
		ORDER BY u.username, u.host, d.name`)
	if err != nil {
		return nil, fmt.Errorf("select database users: %w", err)
	}
	defer rows.Close()

	// A user with three grants arrives as three rows; they are folded back
	// into one record here rather than issuing a query per user.
	order := make([]string, 0, 16)
	byID := make(map[string]*User, 16)

	for rows.Next() {
		var user User
		var databaseID, databaseName, privilege string
		if err := rows.Scan(&user.ID, &user.ServerID, &user.Engine, &user.Username, &user.Host,
			&user.CreatedAt, &user.UpdatedAt, &user.PasswordUpdatedAt,
			&databaseID, &databaseName, &privilege); err != nil {
			return nil, fmt.Errorf("scan database user: %w", err)
		}

		existing, seen := byID[user.ID]
		if !seen {
			user.Grants = make([]Grant, 0, 2)
			byID[user.ID] = &user
			order = append(order, user.ID)
			existing = &user
		}
		if privilege != "" {
			existing.Grants = append(existing.Grants, Grant{
				DatabaseID: databaseID, DatabaseName: databaseName, Privilege: privilege,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	users := make([]User, 0, len(order))
	for _, id := range order {
		users = append(users, *byID[id])
	}
	return users, nil
}

// DeleteUser removes an account row.
func (r *Repository) DeleteUser(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM database_users WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete database user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrUserNotFound
	}
	return nil
}

// ----------------------------------------------------------- permissions

// SetGrant records an account's privilege on a database.
func (r *Repository) SetGrant(ctx context.Context, userID, databaseID, privilege string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO database_permissions (database_user_id, database_id, privilege)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (database_user_id, database_id)
		DO UPDATE SET privilege = EXCLUDED.privilege, updated_at = now()`,
		userID, databaseID, privilege)
	if err != nil {
		return fmt.Errorf("upsert database permission: %w", err)
	}
	return nil
}

// RemoveGrant removes an account's access to a database.
func (r *Repository) RemoveGrant(ctx context.Context, userID, databaseID string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM database_permissions
		WHERE database_user_id = $1::uuid AND database_id = $2::uuid`, userID, databaseID)
	if err != nil {
		return fmt.Errorf("delete database permission: %w", err)
	}
	return nil
}

// GrantsForUser returns every database an account may reach.
func (r *Repository) GrantsForUser(ctx context.Context, userID string) ([]Grant, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT d.id, d.name, p.privilege
		FROM database_permissions p
		JOIN databases d ON d.id = p.database_id
		WHERE p.database_user_id = $1::uuid
		ORDER BY d.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("select database permissions: %w", err)
	}
	defer rows.Close()

	grants := make([]Grant, 0, 4)
	for rows.Next() {
		var grant Grant
		if err := rows.Scan(&grant.DatabaseID, &grant.DatabaseName, &grant.Privilege); err != nil {
			return nil, fmt.Errorf("scan database permission: %w", err)
		}
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

// ------------------------------------------------------------------ scanning

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanDatabase(row scanner) (Database, error) {
	var database Database
	err := row.Scan(&database.ID, &database.ServerID, &database.WebsiteID, &database.Name,
		&database.Engine, &database.Status, &database.Charset, &database.Collation,
		&database.SizeBytes, &database.SizeCheckedAt, &database.CreatedAt, &database.UpdatedAt)
	return database, err
}

func scanDatabaseWithJoins(row scanner) (Database, error) {
	var database Database
	err := row.Scan(&database.ID, &database.ServerID, &database.WebsiteID, &database.Name,
		&database.Engine, &database.Status, &database.Charset, &database.Collation,
		&database.SizeBytes, &database.SizeCheckedAt, &database.CreatedAt, &database.UpdatedAt,
		&database.WebsiteDomain, &database.UserCount)
	return database, err
}

func scanUser(row scanner) (User, error) {
	var user User
	err := row.Scan(&user.ID, &user.ServerID, &user.Engine, &user.Username, &user.Host,
		&user.CreatedAt, &user.UpdatedAt, &user.PasswordUpdatedAt)
	return user, err
}

// isUniqueViolation reports whether an error is a unique-constraint failure.
//
// The SQLSTATE is matched rather than the message text, which is localised on
// some servers and would make this quietly stop working.
func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
