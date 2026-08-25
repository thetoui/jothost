// Package servers owns the managed-server record.
//
// The panel manages one host in this phase — multi-server clustering is an
// explicit non-goal in PRD.md section 3 — but the record exists from the start
// because metrics, websites, and firewall rules all reference a server.
package servers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Status values for a managed server.
const (
	StatusOnline  = "online"
	StatusOffline = "offline"
	StatusUnknown = "unknown"
)

// Server is a managed host.
type Server struct {
	ID           string    `json:"id"`
	Hostname     string    `json:"hostname"`
	OSName       *string   `json:"os_name"`
	OSVersion    *string   `json:"os_version"`
	Kernel       *string   `json:"kernel"`
	Architecture *string   `json:"architecture"`
	IPv4         *string   `json:"ipv4"`
	IPv6         *string   `json:"ipv6"`
	Status       string    `json:"status"`
	AgentVersion *string   `json:"agent_version"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Errors returned by the repository.
var (
	ErrNotFound        = errors.New("server not found")
	ErrInvalidHostname = errors.New("hostname is required")
	ErrInvalidStatus   = errors.New("invalid server status")
)

// maxHostnameLength matches the column width in migration 0004.
const maxHostnameLength = 255

// Repository reads and writes servers.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const selectColumns = `
	id::text, hostname, os_name, os_version, kernel, architecture,
	host(ipv4), host(ipv6), status, agent_version, created_at, updated_at`

func scanServer(row pgx.Row) (Server, error) {
	var s Server
	err := row.Scan(&s.ID, &s.Hostname, &s.OSName, &s.OSVersion, &s.Kernel,
		&s.Architecture, &s.IPv4, &s.IPv6, &s.Status, &s.AgentVersion,
		&s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// RegisterParams describes the host as the Agent reported it.
type RegisterParams struct {
	Hostname     string
	OSName       string
	OSVersion    string
	Kernel       string
	Architecture string
	IPv4         string
	IPv6         string
	AgentVersion string
	Status       string
}

// Register creates or refreshes the record for a host.
//
// It is an upsert keyed on hostname so a restart refreshes the existing row
// rather than accumulating a new one each time. Facts the Agent could not
// report are left as NULL rather than stored as empty strings, so "unknown"
// and "empty" stay distinguishable.
func (r *Repository) Register(ctx context.Context, params RegisterParams) (Server, error) {
	hostname := strings.TrimSpace(params.Hostname)
	if hostname == "" {
		return Server{}, ErrInvalidHostname
	}
	if len(hostname) > maxHostnameLength {
		return Server{}, fmt.Errorf("%w: hostname exceeds %d characters", ErrInvalidHostname, maxHostnameLength)
	}

	status := params.Status
	if status == "" {
		status = StatusUnknown
	}
	switch status {
	case StatusOnline, StatusOffline, StatusUnknown:
	default:
		return Server{}, ErrInvalidStatus
	}

	row := r.pool.QueryRow(ctx, `
		INSERT INTO servers
			(hostname, os_name, os_version, kernel, architecture, ipv4, ipv6, status, agent_version)
		VALUES
			($1, nullif($2, ''), nullif($3, ''), nullif($4, ''), nullif($5, ''),
			 $6::inet, $7::inet, $8, nullif($9, ''))
		ON CONFLICT (hostname) DO UPDATE SET
			os_name       = EXCLUDED.os_name,
			os_version    = EXCLUDED.os_version,
			kernel        = EXCLUDED.kernel,
			architecture  = EXCLUDED.architecture,
			ipv4          = COALESCE(EXCLUDED.ipv4, servers.ipv4),
			ipv6          = COALESCE(EXCLUDED.ipv6, servers.ipv6),
			status        = EXCLUDED.status,
			agent_version = EXCLUDED.agent_version,
			updated_at    = now()
		RETURNING `+selectColumns,
		hostname,
		params.OSName, params.OSVersion, params.Kernel, params.Architecture,
		normalizeIP(params.IPv4), normalizeIP(params.IPv6),
		status, params.AgentVersion)

	server, err := scanServer(row)
	if err != nil {
		return Server{}, fmt.Errorf("register server: %w", err)
	}
	return server, nil
}

// Get returns a server by id.
func (r *Repository) Get(ctx context.Context, id string) (Server, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM servers WHERE id = $1::uuid`, id)

	server, err := scanServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, fmt.Errorf("select server: %w", err)
	}
	return server, nil
}

// GetByHostname returns a server by hostname.
func (r *Repository) GetByHostname(ctx context.Context, hostname string) (Server, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+selectColumns+` FROM servers WHERE hostname = $1`, strings.TrimSpace(hostname))

	server, err := scanServer(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Server{}, ErrNotFound
	}
	if err != nil {
		return Server{}, fmt.Errorf("select server by hostname: %w", err)
	}
	return server, nil
}

// List returns every managed server, oldest first.
func (r *Repository) List(ctx context.Context) ([]Server, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+selectColumns+` FROM servers ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("select servers: %w", err)
	}
	defer rows.Close()

	// A non-nil empty slice serialises as [] rather than null.
	servers := []Server{}
	for rows.Next() {
		server, err := scanServer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan server: %w", err)
		}
		servers = append(servers, server)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate servers: %w", err)
	}
	return servers, nil
}

// SetStatus records whether a server is reachable.
func (r *Repository) SetStatus(ctx context.Context, id, status string) error {
	switch status {
	case StatusOnline, StatusOffline, StatusUnknown:
	default:
		return ErrInvalidStatus
	}

	tag, err := r.pool.Exec(ctx,
		`UPDATE servers SET status = $2, updated_at = now() WHERE id = $1::uuid`, id, status)
	if err != nil {
		return fmt.Errorf("update server status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// normalizeIP returns a valid address for the INET column, or nil.
//
// The value comes from host inspection, so an unparsable one is dropped rather
// than passed to Postgres where it would fail the whole upsert.
func normalizeIP(raw string) *string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if net.ParseIP(raw) == nil {
		return nil
	}
	return &raw
}
