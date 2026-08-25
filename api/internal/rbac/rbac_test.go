package rbac

import (
	"context"
	"errors"
	"testing"

	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/api/internal/users"
)

const testHash = "$argon2id$v=19$m=47104,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaA"

// setup returns an rbac repository and the id of a fresh user.
func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	user, err := users.NewRepository(deps.Pool).Create(ctx, users.CreateParams{
		Username:     "testuser",
		PasswordHash: testHash,
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return NewRepository(deps.Pool), user.ID, ctx
}

func TestNewUserHasNoRolesOrPermissions(t *testing.T) {
	repo, userID, ctx := setup(t)

	roles, err := repo.RolesForUser(ctx, userID)
	if err != nil {
		t.Fatalf("RolesForUser: %v", err)
	}
	if len(roles) != 0 {
		t.Fatalf("a new user must have no roles, got %v", roles)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}
	if len(permissions) != 0 {
		t.Fatalf("a new user must have no permissions, got %v", permissions)
	}
}

func TestAdminRoleGrantsEveryPermission(t *testing.T) {
	repo, userID, ctx := setup(t)

	if err := repo.AssignRole(ctx, userID, RoleAdmin); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}

	required := []string{
		PermServerView, PermServerManage,
		PermWebsiteView, PermWebsiteCreate, PermWebsiteUpdate, PermWebsiteDelete,
		PermDatabaseManage, PermSSLManage, PermFirewallManage, PermBackupManage,
		PermFileRead, PermFileWrite, PermCronManage, PermDNSManage,
		PermAuditView, PermUserManage,
	}
	if !HasAll(permissions, required...) {
		t.Fatalf("admin must hold every permission, got %v", permissions)
	}
}

func TestViewerRoleIsReadOnly(t *testing.T) {
	repo, userID, ctx := setup(t)

	if err := repo.AssignRole(ctx, userID, RoleViewer); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}

	if !Has(permissions, PermServerView) {
		t.Fatalf("viewer must be able to view the server, got %v", permissions)
	}
	// The point of the role: no mutating permission may leak in.
	for _, forbidden := range []string{
		PermWebsiteCreate, PermWebsiteDelete, PermFileWrite,
		PermFirewallManage, PermUserManage, PermServerManage,
	} {
		if Has(permissions, forbidden) {
			t.Fatalf("viewer must not hold %q", forbidden)
		}
	}
}

func TestOperatorCannotManageUsersOrFirewall(t *testing.T) {
	repo, userID, ctx := setup(t)

	if err := repo.AssignRole(ctx, userID, RoleOperator); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}

	if !HasAll(permissions, PermWebsiteCreate, PermDatabaseManage, PermSSLManage) {
		t.Fatalf("operator must hold hosting permissions, got %v", permissions)
	}
	for _, forbidden := range []string{PermUserManage, PermFirewallManage, PermServerManage} {
		if Has(permissions, forbidden) {
			t.Fatalf("operator must not hold %q", forbidden)
		}
	}
}

func TestAssignRoleIsIdempotent(t *testing.T) {
	repo, userID, ctx := setup(t)

	for i := 0; i < 3; i++ {
		if err := repo.AssignRole(ctx, userID, RoleViewer); err != nil {
			t.Fatalf("AssignRole attempt %d: %v", i, err)
		}
	}

	roles, err := repo.RolesForUser(ctx, userID)
	if err != nil {
		t.Fatalf("RolesForUser: %v", err)
	}
	if len(roles) != 1 || roles[0] != RoleViewer {
		t.Fatalf("expected exactly one viewer role, got %v", roles)
	}
}

func TestAssignUnknownRoleFails(t *testing.T) {
	repo, userID, ctx := setup(t)

	err := repo.AssignRole(ctx, userID, "superadmin")
	if !errors.Is(err, ErrRoleNotFound) {
		t.Fatalf("expected ErrRoleNotFound, got %v", err)
	}
}

func TestRevokeRole(t *testing.T) {
	repo, userID, ctx := setup(t)

	if err := repo.AssignRole(ctx, userID, RoleAdmin); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if err := repo.RevokeRole(ctx, userID, RoleAdmin); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}
	if len(permissions) != 0 {
		t.Fatalf("revoking the role must remove its permissions, got %v", permissions)
	}

	// Revoking again must not error.
	if err := repo.RevokeRole(ctx, userID, RoleAdmin); err != nil {
		t.Fatalf("RevokeRole must be idempotent: %v", err)
	}
}

func TestMultipleRolesUnionPermissions(t *testing.T) {
	repo, userID, ctx := setup(t)

	if err := repo.AssignRole(ctx, userID, RoleViewer); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	if err := repo.AssignRole(ctx, userID, RoleOperator); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	permissions, err := repo.PermissionsForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}

	// Overlapping permissions must appear once, not twice.
	seen := make(map[string]int)
	for _, p := range permissions {
		seen[p]++
	}
	for name, count := range seen {
		if count > 1 {
			t.Fatalf("permission %q appears %d times", name, count)
		}
	}
	if !Has(permissions, PermWebsiteCreate) {
		t.Fatal("the union must include operator permissions")
	}
}

func TestHasRequiresAnExactMatch(t *testing.T) {
	permissions := []string{"website.create", "server.view"}

	if !Has(permissions, "website.create") {
		t.Fatal("an exact match must be found")
	}
	// Prefix, suffix, wildcard, and case variants must never satisfy a check.
	for _, candidate := range []string{
		"website", "website.", "website.create.draft", "WEBSITE.CREATE",
		"website.*", "*", "", "site.create",
	} {
		if Has(permissions, candidate) {
			t.Fatalf("permission %q must not match", candidate)
		}
	}
}

func TestHasAll(t *testing.T) {
	permissions := []string{"a", "b", "c"}

	if !HasAll(permissions, "a", "c") {
		t.Fatal("HasAll must accept a held subset")
	}
	if HasAll(permissions, "a", "d") {
		t.Fatal("HasAll must reject a missing permission")
	}
	if !HasAll(permissions) {
		t.Fatal("HasAll with no requirements must pass")
	}
	if HasAll(nil, "a") {
		t.Fatal("an empty permission set must satisfy nothing")
	}
}
