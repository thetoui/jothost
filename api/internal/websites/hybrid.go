package websites

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jothost/panel/shared/validate"
)

// Errors returned by the hybrid arrangement.
var (
	// ErrNoBackendPort means every port in the range is taken. A host with 900
	// sites in hybrid mode is a real answer, and a clearer one than a port of
	// zero silently reaching a configuration file.
	ErrNoBackendPort = errors.New("no Apache backend port is available")
	// ErrPortInUse means the port is held by something the panel knows about.
	ErrPortInUse = errors.New("that port is already in use on this server")
)

// WebserverMode returns the arrangement a server runs.
//
// It reads the servers table rather than taking the mode as an argument,
// because every caller that builds a vhost needs it and none of them should
// have to be told: a payload built against a stale mode is a site pointed at a
// backend that is not there.
func (r *Repository) WebserverMode(ctx context.Context, serverID string) (string, error) {
	var mode string
	err := r.pool.QueryRow(ctx,
		`SELECT webserver_mode FROM servers WHERE id = $1::uuid`, serverID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		// A website whose server row is gone is a broken record, not a reason
		// to guess at an arrangement.
		return "", fmt.Errorf("server %s: %w", serverID, ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("select webserver mode: %w", err)
	}
	return mode, nil
}

// SetWebserverMode records the arrangement a server runs.
func (r *Repository) SetWebserverMode(ctx context.Context, serverID, mode string) error {
	if err := validate.WebserverMode(mode); err != nil {
		return err
	}

	tag, err := r.pool.Exec(ctx,
		`UPDATE servers SET webserver_mode = $2 WHERE id = $1::uuid`, serverID, mode)
	if err != nil {
		return fmt.Errorf("update webserver mode: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AssignBackendPort gives a website a loopback port for Apache, if it has none.
//
// It returns the port the site now holds, whether it was assigned here or
// already recorded. Keeping an existing one is the point: switching a host to
// nginx and back should not renumber every backend on it.
//
// The scan is a single statement rather than a loop of "is this one free":
// two websites being created at the same moment would otherwise both be told
// the same port is free, and the second would fail on the unique index after
// its configuration had already been written.
func (r *Repository) AssignBackendPort(ctx context.Context, site Website) (int, error) {
	if site.ApachePort != nil {
		return *site.ApachePort, nil
	}

	var port int
	err := r.pool.QueryRow(ctx, `
		UPDATE websites
		SET apache_port = (
			SELECT candidate
			FROM generate_series($2::int, $3::int) AS candidate
			WHERE NOT EXISTS (
				SELECT 1 FROM websites other
				WHERE other.server_id = websites.server_id
				  AND other.apache_port = candidate
			)
			  AND NOT EXISTS (
				-- A Node application's port is on the same host and in a range
				-- that overlaps this one. Handing Apache a port an application
				-- already holds would mean one of the two failing to bind, and
				-- the panel unable to say which.
				SELECT 1 FROM node_apps app
				WHERE app.server_id = websites.server_id
				  AND app.port = candidate
			)
			ORDER BY candidate
			LIMIT 1
		),
		    updated_at = now()
		WHERE id = $1::uuid
		RETURNING apache_port`,
		site.ID, validate.MinBackendPort, validate.MaxBackendPort).Scan(&port)

	if err != nil {
		// A null result means the sub-select found nothing: every port in the
		// range is taken.
		if isNullScan(err) {
			return 0, fmt.Errorf("%w: all %d ports between %d and %d are taken",
				ErrNoBackendPort, validate.BackendPortCount,
				validate.MinBackendPort, validate.MaxBackendPort)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("assign backend port: %w", err)
	}

	if err := validate.BackendPort(port); err != nil {
		return 0, err
	}
	return port, nil
}

// BackendPortTaken reports whether a port is already claimed on a server.
//
// It is the other half of the two-way check: a Node application asking for a
// port has to see the Apache backends, exactly as the allocator above sees the
// applications. Two tables cannot be constrained against each other in SQL
// without a trigger on both, so the check lives in one function each side
// calls.
func (r *Repository) BackendPortTaken(ctx context.Context, serverID string, port int) (string, error) {
	var domain string
	err := r.pool.QueryRow(ctx, `
		SELECT primary_domain FROM websites
		WHERE server_id = $1::uuid AND apache_port = $2`, serverID, port).Scan(&domain)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("check backend port: %w", err)
	}
	return domain, nil
}

// ListForReconcile returns the websites whose configuration has to be rewritten
// when the host's arrangement changes.
//
// Sites still being created or deleted are left out: each already has a job in
// flight that will write its configuration from the record, which by then holds
// the new mode.
func (r *Repository) ListForReconcile(ctx context.Context, serverID string) ([]Website, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+websiteColumns+`
		FROM websites
		WHERE server_id = $1::uuid
		  AND status IN ('active', 'failed')
		ORDER BY parent_website_id NULLS FIRST, created_at`, serverID)
	if err != nil {
		return nil, fmt.Errorf("select websites to reconcile: %w", err)
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

// isNullScan reports whether a scan failed because the value was NULL.
//
// pgx returns a generic conversion error for this, and the difference matters:
// "the host is full" is an answer for a user, where "cannot scan NULL into
// *int" is an answer for nobody.
func isNullScan(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "cannot scan NULL") ||
		strings.Contains(message, "converting NULL")
}

// joinArrangement gives a new site the backend port it needs, if the host runs
// the hybrid arrangement.
//
// A site created while the host runs Apache is served through Apache from the
// moment it exists. Waiting for the next mode change would make "which sites
// use Apache" depend on when each was created — a fact nobody could reason
// about, and one that would show up as .htaccess working on some sites and not
// others.
func (s *Service) joinArrangement(ctx context.Context, site Website) (Website, error) {
	mode, err := s.repo.WebserverMode(ctx, site.ServerID)
	if err != nil {
		return site, err
	}
	if mode != validate.WebserverHybrid {
		return site, nil
	}

	port, err := s.repo.AssignBackendPort(ctx, site)
	if err != nil {
		return site, err
	}
	site.ApachePort = &port
	return site, nil
}
