// Package rbac owns roles, permissions, and the permission check itself.
package rbac

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Built-in role names seeded by migration 0002.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// Permission names seeded by migration 0002. Handlers reference these
// constants rather than string literals so a typo is a compile error instead
// of a silently failing authorization check.
const (
	PermServerView     = "server.view"
	PermServerManage   = "server.manage"
	PermWebsiteView    = "website.view"
	PermWebsiteCreate  = "website.create"
	PermWebsiteUpdate  = "website.update"
	PermWebsiteDelete  = "website.delete"
	PermDatabaseManage = "database.manage"
	PermSSLManage      = "ssl.manage"
	PermFirewallManage = "firewall.manage"
	PermBackupManage   = "backup.manage"
	PermFileRead       = "file.read"
	PermFileWrite      = "file.write"
	PermCronManage     = "cron.manage"
	// PermFTPManage is its own rather than website.update: an FTP credential
	// reaches a site's files without going through the panel, and it keeps
	// working after the person holding it stops being a panel user.
	PermFTPManage  = "ftp.manage"
	PermDNSManage  = "dns.manage"
	PermAuditView  = "audit.view"
	PermUserManage = "user.manage"
)

// ErrRoleNotFound is returned when a named role does not exist.
var ErrRoleNotFound = errors.New("role not found")

// Repository reads roles and permissions.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// RolesForUser returns the role names assigned to a user.
func (r *Repository) RolesForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ro.name
		FROM user_roles ur
		JOIN roles ro ON ro.id = ur.role_id
		WHERE ur.user_id = $1::uuid
		ORDER BY ro.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("select roles for user: %w", err)
	}
	defer rows.Close()

	return collectStrings(rows, "roles")
}

// PermissionsForUser returns the distinct permission names granted to a user
// through their roles.
func (r *Repository) PermissionsForUser(ctx context.Context, userID string) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT DISTINCT p.name
		FROM user_roles ur
		JOIN role_permissions rp ON rp.role_id = ur.role_id
		JOIN permissions p ON p.id = rp.permission_id
		WHERE ur.user_id = $1::uuid
		ORDER BY p.name`, userID)
	if err != nil {
		return nil, fmt.Errorf("select permissions for user: %w", err)
	}
	defer rows.Close()

	return collectStrings(rows, "permissions")
}

// AssignRole grants a role to a user. Re-assigning an existing role is a no-op
// so the operation is idempotent.
func (r *Repository) AssignRole(ctx context.Context, userID, roleName string) error {
	tag, err := r.pool.Exec(ctx, `
		INSERT INTO user_roles (user_id, role_id)
		SELECT $1::uuid, id FROM roles WHERE name = $2
		ON CONFLICT DO NOTHING`, userID, roleName)
	if err != nil {
		return fmt.Errorf("assign role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either the role does not exist or the user already has it; only the
		// former is an error, so it is checked explicitly.
		exists, err := r.roleExists(ctx, roleName)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("%w: %s", ErrRoleNotFound, roleName)
		}
	}
	return nil
}

// RevokeRole removes a role from a user.
func (r *Repository) RevokeRole(ctx context.Context, userID, roleName string) error {
	_, err := r.pool.Exec(ctx, `
		DELETE FROM user_roles
		WHERE user_id = $1::uuid
		  AND role_id = (SELECT id FROM roles WHERE name = $2)`, userID, roleName)
	if err != nil {
		return fmt.Errorf("revoke role: %w", err)
	}
	return nil
}

func (r *Repository) roleExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE name = $1)`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check role exists: %w", err)
	}
	return exists, nil
}

func collectStrings(rows pgx.Rows, what string) ([]string, error) {
	// A non-nil empty slice serialises as [] rather than null.
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", what, err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", what, err)
	}
	return values, nil
}

// Has reports whether permissions contains want.
//
// Permission checks are exact string matches. There is deliberately no
// wildcard or prefix matching: "website.create" must never be satisfied by
// holding "website.create.draft" or by a pattern like "website.*".
func Has(permissions []string, want string) bool {
	for _, p := range permissions {
		if p == want {
			return true
		}
	}
	return false
}

// HasAll reports whether permissions contains every entry in want.
func HasAll(permissions []string, want ...string) bool {
	for _, w := range want {
		if !Has(permissions, w) {
			return false
		}
	}
	return true
}
