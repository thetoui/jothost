// Package node is the panel's record of the Node.js applications it runs.
//
// As everywhere else, these rows describe intent and the Agent owns what is
// actually running. The one thing the panel is authoritative about is the
// environment: an application's configuration is set here, encrypted here, and
// written to the host from here.
package node

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/secrets"
)

// Application lifecycle states, matching migration 0009's CHECK.
const (
	StatusStopped  = "stopped"
	StatusStarting = "starting"
	StatusRunning  = "running"
	StatusFailed   = "failed"
)

// Errors returned by the repository.
var (
	ErrNotFound = errors.New("Node.js application not found")
	// ErrDuplicateWebsite means the site already runs an application.
	ErrDuplicateWebsite = errors.New("this website already has a Node.js application")
	// ErrPortTaken means another application already has that port.
	ErrPortTaken = errors.New("another application is already using that port")
	// ErrDuplicateName means the name is taken on this server.
	ErrDuplicateName = errors.New("an application with that name already exists")
)

// App is one managed Node.js application.
type App struct {
	ID        string `json:"id"`
	ServerID  string `json:"server_id"`
	WebsiteID string `json:"website_id"`

	Name      string `json:"name"`
	Version   string `json:"node_version"`
	Root      string `json:"application_root"`
	Startup   string `json:"startup_file"`
	Port      int    `json:"port"`
	Status    string `json:"status"`
	Unit      string `json:"systemd_service"`
	Autostart bool   `json:"autostart"`

	LastError *string   `json:"last_error"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// WebsiteDomain and SystemUser are joined from the website, so a listing
	// can show where an application lives without a query per row.
	WebsiteDomain string `json:"website_domain,omitempty"`
	SystemUser    string `json:"system_user,omitempty"`

	// Runtime is what the host says it is doing right now. Absent from a plain
	// listing, which reports the recorded status instead.
	Runtime map[string]any `json:"runtime,omitempty"`
	// Environment is the configuration, values withheld. What is set matters
	// on a listing; what it is set to is a separate, audited read.
	Environment []string `json:"environment,omitempty"`
}

// Repository reads and writes application records.
type Repository struct {
	pool      *pgxpool.Pool
	encrypter *secrets.Encrypter
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool, encrypter *secrets.Encrypter) *Repository {
	return &Repository{pool: pool, encrypter: encrypter}
}

// envContext binds a ciphertext to the row that holds it, so one copied from
// another application's row fails to decrypt rather than revealing its secret.
func envContext(id string) string { return "node_environment:" + id }

// CreateParams describe an application to record.
type CreateParams struct {
	ServerID  string
	WebsiteID string
	Name      string
	Version   string
	Root      string
	Startup   string
	Port      int
	Unit      string
}

// Create writes an application row.
func (r *Repository) Create(ctx context.Context, params CreateParams) (App, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO node_apps
			(server_id, website_id, name, node_version, application_root,
			 startup_file, port, systemd_service)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
		RETURNING id, server_id, website_id, name, node_version, application_root,
		          startup_file, port, status, systemd_service, autostart,
		          last_error, created_at, updated_at`,
		params.ServerID, params.WebsiteID, params.Name, params.Version,
		params.Root, params.Startup, params.Port, nullable(params.Unit))

	app, err := scanApp(row)
	if err != nil {
		return App{}, translateConstraint(err)
	}
	return app, nil
}

// Update changes the parts of an application that can change.
//
// The name and the website cannot: the name is the unit's identity, and moving
// an application to another site would mean a different account, a different
// directory, and a different vhost — which is a new application.
type UpdateParams struct {
	Version   string
	Startup   string
	Port      int
	Autostart *bool
}

// Update applies changes to an application.
func (r *Repository) Update(ctx context.Context, id string, params UpdateParams) (App, error) {
	row := r.pool.QueryRow(ctx, `
		UPDATE node_apps SET
			node_version = COALESCE(NULLIF($2, ''), node_version),
			startup_file = COALESCE(NULLIF($3, ''), startup_file),
			port         = COALESCE(NULLIF($4, 0), port),
			autostart    = COALESCE($5, autostart),
			updated_at   = now()
		WHERE id = $1::uuid
		RETURNING id, server_id, website_id, name, node_version, application_root,
		          startup_file, port, status, systemd_service, autostart,
		          last_error, created_at, updated_at`,
		id, params.Version, params.Startup, params.Port, params.Autostart)

	app, err := scanApp(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return App{}, ErrNotFound
		}
		return App{}, translateConstraint(err)
	}
	return app, nil
}

// SetStatus records what the host reported.
func (r *Repository) SetStatus(ctx context.Context, id, status string, lastError *string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE node_apps SET status = $2, last_error = $3, updated_at = now()
		WHERE id = $1::uuid`, id, status, lastError)
	if err != nil {
		return fmt.Errorf("update application status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get returns one application.
func (r *Repository) Get(ctx context.Context, id string) (App, error) {
	row := r.pool.QueryRow(ctx, selectApps+` WHERE a.id = $1::uuid`, id)

	app, err := scanAppWithJoins(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return App{}, ErrNotFound
		}
		return App{}, fmt.Errorf("select application: %w", err)
	}
	return app, nil
}

// GetByWebsite returns the application a website runs, if it has one.
func (r *Repository) GetByWebsite(ctx context.Context, websiteID string) (App, error) {
	row := r.pool.QueryRow(ctx, selectApps+` WHERE a.website_id = $1::uuid`, websiteID)

	app, err := scanAppWithJoins(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return App{}, ErrNotFound
		}
		return App{}, fmt.Errorf("select application: %w", err)
	}
	return app, nil
}

// List returns every application, newest first.
func (r *Repository) List(ctx context.Context) ([]App, error) {
	rows, err := r.pool.Query(ctx, selectApps+` ORDER BY a.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("select applications: %w", err)
	}
	defer rows.Close()

	apps := make([]App, 0, 8)
	for rows.Next() {
		app, err := scanAppWithJoins(rows)
		if err != nil {
			return nil, fmt.Errorf("scan application: %w", err)
		}
		apps = append(apps, app)
	}
	return apps, rows.Err()
}

// Delete removes an application record.
func (r *Repository) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM node_apps WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete application: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --------------------------------------------------------------- environment

// SetEnv stores one variable, encrypted.
func (r *Repository) SetEnv(ctx context.Context, appID, key, value string) error {
	// The ciphertext is bound to the row, so the row has to exist first. An
	// upsert returning the id gives both in one statement.
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO node_environment (node_app_id, key, value_encrypted)
		VALUES ($1::uuid, $2, '')
		ON CONFLICT (node_app_id, key) DO UPDATE SET updated_at = now()
		RETURNING id`, appID, key).Scan(&id)
	if err != nil {
		return fmt.Errorf("upsert environment variable: %w", err)
	}

	sealed, err := r.encrypter.Encrypt([]byte(value), envContext(id))
	if err != nil {
		return fmt.Errorf("encrypt environment value: %w", err)
	}

	_, err = r.pool.Exec(ctx, `
		UPDATE node_environment SET value_encrypted = $2, updated_at = now()
		WHERE id = $1::uuid`, id, sealed)
	if err != nil {
		return fmt.Errorf("store environment value: %w", err)
	}
	return nil
}

// RemoveEnv deletes one variable.
func (r *Repository) RemoveEnv(ctx context.Context, appID, key string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM node_environment WHERE node_app_id = $1::uuid AND key = $2`, appID, key)
	if err != nil {
		return fmt.Errorf("delete environment variable: %w", err)
	}
	return nil
}

// EnvKeys returns the names that are set, without their values.
//
// What is configured is useful on a listing; what it is configured to is a
// credential, and reading one is a deliberate act with its own audit record.
func (r *Repository) EnvKeys(ctx context.Context, appID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT key FROM node_environment WHERE node_app_id = $1::uuid ORDER BY key`, appID)
	if err != nil {
		return nil, fmt.Errorf("select environment keys: %w", err)
	}
	defer rows.Close()

	keys := make([]string, 0, 8)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan environment key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// Environment decrypts an application's whole configuration.
//
// Used when the environment is written to the host, and by the deliberate read
// the panel audits. Never by a listing.
func (r *Repository) Environment(ctx context.Context, appID string) (map[string]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, key, value_encrypted FROM node_environment
		 WHERE node_app_id = $1::uuid ORDER BY key`, appID)
	if err != nil {
		return nil, fmt.Errorf("select environment: %w", err)
	}
	defer rows.Close()

	env := make(map[string]string, 8)
	for rows.Next() {
		var id, key, sealed string
		if err := rows.Scan(&id, &key, &sealed); err != nil {
			return nil, fmt.Errorf("scan environment: %w", err)
		}
		if sealed == "" {
			continue
		}
		value, err := r.encrypter.Decrypt(sealed, envContext(id))
		if err != nil {
			return nil, fmt.Errorf("decrypt %s: %w", key, err)
		}
		env[key] = string(value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return env, nil
}

// SortedKeys returns a map's keys in order, for stable output.
func SortedKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// ------------------------------------------------------------------ scanning

const selectApps = `
	SELECT a.id, a.server_id, a.website_id, a.name, a.node_version,
	       a.application_root, a.startup_file, a.port, a.status,
	       a.systemd_service, a.autostart, a.last_error, a.created_at, a.updated_at,
	       w.primary_domain, w.system_username
	FROM node_apps a
	JOIN websites w ON w.id = a.website_id`

type scanner interface {
	Scan(dest ...any) error
}

func scanApp(row scanner) (App, error) {
	var app App
	var unit *string
	err := row.Scan(&app.ID, &app.ServerID, &app.WebsiteID, &app.Name, &app.Version,
		&app.Root, &app.Startup, &app.Port, &app.Status, &unit, &app.Autostart,
		&app.LastError, &app.CreatedAt, &app.UpdatedAt)
	if unit != nil {
		app.Unit = *unit
	}
	return app, err
}

func scanAppWithJoins(row scanner) (App, error) {
	var app App
	var unit *string
	err := row.Scan(&app.ID, &app.ServerID, &app.WebsiteID, &app.Name, &app.Version,
		&app.Root, &app.Startup, &app.Port, &app.Status, &unit, &app.Autostart,
		&app.LastError, &app.CreatedAt, &app.UpdatedAt,
		&app.WebsiteDomain, &app.SystemUser)
	if unit != nil {
		app.Unit = *unit
	}
	return app, err
}

func nullable(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// translateConstraint turns a unique-index violation into the error that says
// which one, so the caller can explain it rather than reporting "conflict".
func translateConstraint(err error) error {
	var pgErr interface {
		SQLState() string
		Error() string
	}
	if !errors.As(err, &pgErr) || pgErr.SQLState() != "23505" {
		return fmt.Errorf("write application: %w", err)
	}

	switch message := pgErr.Error(); {
	case contains(message, "node_apps_website_idx"):
		return ErrDuplicateWebsite
	case contains(message, "node_apps_port_idx"):
		return ErrPortTaken
	case contains(message, "node_apps_name_idx"):
		return ErrDuplicateName
	default:
		return fmt.Errorf("write application: %w", err)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
