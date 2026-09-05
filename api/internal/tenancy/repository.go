package tenancy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/jothost/panel/api/internal/db"
	"github.com/jothost/panel/shared/validate"
)

// Repository reads and writes the tenancy tables.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// descendantsCTE is the one definition of "accounts below this one".
//
// Recursive rather than a parent check, because a reseller's customers are one
// level down but an admin's are two, and a scheme that only looked at
// parent_user_id would let a reseller's customer be invisible to the admin who
// owns the machine. It includes the account itself, so "what may I see"
// includes what is mine.
//
// The recursion terminates by construction: every edge runs from a tier to a
// strictly lower one, which validate.TierMayOwn is what guarantees.
const descendantsCTE = `
	WITH RECURSIVE descendants AS (
		SELECT id FROM users WHERE id = $1::uuid
		UNION ALL
		SELECT u.id FROM users u JOIN descendants d ON u.parent_user_id = d.id
	)`

// ---------------------------------------------------------------- accounts

const accountColumns = `
	u.id::text, u.username, u.email, u.tier, u.parent_user_id::text,
	p.username, u.full_name, u.company, u.status, u.created_at, u.last_login_at,
	(SELECT count(*) FROM subscriptions s WHERE s.owner_user_id = u.id)`

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.Username, &a.Email, &a.Tier, &a.ParentID,
		&a.ParentUsername, &a.FullName, &a.Company, &a.Status, &a.CreatedAt,
		&a.LastLoginAt, &a.Subscriptions)
	return a, err
}

// ListAccounts returns the accounts below actorID, including that account.
//
// An admin passes seeAll, which is not the same as being the root of the tree:
// an admin created by the installer has no children at all, and a scheme that
// showed them only their descendants would show the server's owner nothing.
func (r *Repository) ListAccounts(ctx context.Context, actorID string, seeAll bool) ([]Account, error) {
	query := descendantsCTE + `
		SELECT ` + accountColumns + `
		FROM users u
		LEFT JOIN users p ON p.id = u.parent_user_id
		WHERE u.id IN (SELECT id FROM descendants)
		ORDER BY u.tier, u.username`
	if seeAll {
		query = `
		SELECT ` + accountColumns + `
		FROM users u
		LEFT JOIN users p ON p.id = u.parent_user_id
		WHERE $1::uuid IS NOT NULL
		ORDER BY u.tier, u.username`
	}

	rows, err := r.pool.Query(ctx, query, actorID)
	if err != nil {
		return nil, fmt.Errorf("select accounts: %w", err)
	}
	defer rows.Close()

	accounts := []Account{}
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return accounts, nil
}

// GetAccount reads one account.
func (r *Repository) GetAccount(ctx context.Context, id string) (Account, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+accountColumns+`
		FROM users u
		LEFT JOIN users p ON p.id = u.parent_user_id
		WHERE u.id = $1::uuid`, id)

	account, err := scanAccount(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("select account: %w", err)
	}
	return account, nil
}

// IsDescendant reports whether subjectID is at or below actorID.
//
// This is the hierarchy check, and it is a query rather than a walk in Go
// because the answer must be the database's: two API processes editing the
// tree concurrently would otherwise each be reasoning about a copy.
func (r *Repository) IsDescendant(ctx context.Context, actorID, subjectID string) (bool, error) {
	var found bool
	err := r.pool.QueryRow(ctx, descendantsCTE+`
		SELECT EXISTS (SELECT 1 FROM descendants WHERE id = $2::uuid)`,
		actorID, subjectID).Scan(&found)
	if err != nil {
		return false, fmt.Errorf("check the account hierarchy: %w", err)
	}
	return found, nil
}

// CreateAccountParams describes a new account.
type CreateAccountParams struct {
	Username     string
	Email        string
	PasswordHash string
	Tier         string
	ParentID     string
	FullName     string
	Company      string
}

// CreateAccount inserts an account inside the hierarchy.
func (r *Repository) CreateAccount(ctx context.Context, params CreateAccountParams) (Account, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO users (username, email, password_hash, status, tier,
		                   parent_user_id, full_name, company)
		VALUES ($1, $2, $3, 'active', $4, $5::uuid, $6, $7)
		RETURNING id::text`,
		params.Username, nullable(params.Email), params.PasswordHash, params.Tier,
		params.ParentID, nullable(params.FullName), nullable(params.Company)).Scan(&id)
	if err != nil {
		switch {
		case db.IsUniqueViolation(err, "users_username_key"):
			return Account{}, fmt.Errorf("%w: that username is taken", ErrNameTaken)
		case db.IsUniqueViolation(err, "users_email_key"):
			return Account{}, fmt.Errorf("%w: that email address is taken", ErrNameTaken)
		default:
			return Account{}, fmt.Errorf("insert account: %w", err)
		}
	}
	return r.GetAccount(ctx, id)
}

// UpdateAccountParams describes an edit. A nil field is left alone.
type UpdateAccountParams struct {
	FullName *string
	Company  *string
	Status   *string
}

// UpdateAccount edits an account's contact details or status.
func (r *Repository) UpdateAccount(ctx context.Context, id string, params UpdateAccountParams) (Account, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE users SET
			full_name = COALESCE($2, full_name),
			company   = COALESCE($3, company),
			status    = COALESCE($4, status),
			updated_at = now()
		WHERE id = $1::uuid`,
		id, params.FullName, params.Company, params.Status)
	if err != nil {
		return Account{}, fmt.Errorf("update account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Account{}, ErrNotFound
	}
	return r.GetAccount(ctx, id)
}

// DeleteAccount removes an account.
//
// The foreign keys do the refusing: a reseller with customers, or an account
// with subscriptions, cannot be deleted, and the error says which. That is
// deliberate — deleting a reseller whose customers cascade away is how a
// panel loses the record of who owned a hundred websites.
func (r *Repository) DeleteAccount(ctx context.Context, id string) error {
	var children, subscriptions int
	err := r.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM users WHERE parent_user_id = $1::uuid),
		       (SELECT count(*) FROM subscriptions WHERE owner_user_id = $1::uuid)`,
		id).Scan(&children, &subscriptions)
	if err != nil {
		return fmt.Errorf("check what depends on the account: %w", err)
	}
	if children > 0 {
		return fmt.Errorf("%w: %d of them", ErrHasChildren, children)
	}
	if subscriptions > 0 {
		return fmt.Errorf("%w: it still owns %d subscription(s)", ErrForbidden, subscriptions)
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------------- plans

const planColumns = `
	p.id::text, p.owner_user_id::text, o.username, p.name, p.description, p.kind,
	p.disk_mb, p.bandwidth_mb, p.max_websites, p.max_databases, p.max_mailboxes,
	p.max_ftp_users, p.max_cron_jobs, p.max_subdomains,
	p.enforcement, p.cpu_percent, p.memory_mb, p.io_weight,
	(SELECT count(*) FROM subscriptions s WHERE s.plan_id = p.id),
	p.created_at, p.updated_at`

func scanPlan(row pgx.Row) (Plan, error) {
	var p Plan
	err := row.Scan(&p.ID, &p.OwnerUserID, &p.OwnerUsername, &p.Name, &p.Description, &p.Kind,
		&p.Limits.DiskMB, &p.Limits.BandwidthMB, &p.Limits.MaxWebsites, &p.Limits.MaxDatabases,
		&p.Limits.MaxMailboxes, &p.Limits.MaxFTPUsers, &p.Limits.MaxCronJobs,
		&p.Limits.MaxSubdomains, &p.Enforcement, &p.Isolation.CPUPercent,
		&p.Isolation.MemoryMB, &p.Isolation.IOWeight, &p.Subscriptions,
		&p.CreatedAt, &p.UpdatedAt)
	return p, err
}

// ListPlans returns the plans a caller may sell.
//
// A plan is visible when it is the admin's own catalogue (no owner) or belongs
// to the caller. A reseller does not see another reseller's price list, which
// is not merely tidiness: a plan's name and limits are commercial information.
func (r *Repository) ListPlans(ctx context.Context, actorID string, seeAll bool) ([]Plan, error) {
	filter := `WHERE p.owner_user_id IS NULL OR p.owner_user_id = $1::uuid`
	if seeAll {
		filter = `WHERE $1::uuid IS NOT NULL`
	}

	rows, err := r.pool.Query(ctx, `
		SELECT `+planColumns+`
		FROM service_plans p
		LEFT JOIN users o ON o.id = p.owner_user_id
		`+filter+`
		ORDER BY p.kind, lower(p.name)`, actorID)
	if err != nil {
		return nil, fmt.Errorf("select plans: %w", err)
	}
	defer rows.Close()

	plans := []Plan{}
	for rows.Next() {
		plan, err := scanPlan(rows)
		if err != nil {
			return nil, fmt.Errorf("scan plan: %w", err)
		}
		plans = append(plans, plan)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plans: %w", err)
	}
	return plans, nil
}

// GetPlan reads one plan.
func (r *Repository) GetPlan(ctx context.Context, id string) (Plan, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+planColumns+`
		FROM service_plans p
		LEFT JOIN users o ON o.id = p.owner_user_id
		WHERE p.id = $1::uuid`, id)

	plan, err := scanPlan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, ErrNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("select plan: %w", err)
	}
	return plan, nil
}

// PlanInput is a plan as somebody described it.
type PlanInput struct {
	OwnerUserID *string
	Name        string
	Description string
	Kind        string
	Limits      Limits
	Enforcement string
	Isolation   Isolation
}

// CreatePlan inserts a plan or an add-on.
func (r *Repository) CreatePlan(ctx context.Context, input PlanInput) (Plan, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO service_plans (
			owner_user_id, name, description, kind,
			disk_mb, bandwidth_mb, max_websites, max_databases, max_mailboxes,
			max_ftp_users, max_cron_jobs, max_subdomains,
			enforcement, cpu_percent, memory_mb, io_weight)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id::text`,
		input.OwnerUserID, strings.TrimSpace(input.Name), input.Description, input.Kind,
		input.Limits.DiskMB, input.Limits.BandwidthMB, input.Limits.MaxWebsites,
		input.Limits.MaxDatabases, input.Limits.MaxMailboxes, input.Limits.MaxFTPUsers,
		input.Limits.MaxCronJobs, input.Limits.MaxSubdomains,
		input.Enforcement, input.Isolation.CPUPercent, input.Isolation.MemoryMB,
		input.Isolation.IOWeight).Scan(&id)
	if err != nil {
		if db.IsUniqueViolation(err, "service_plans_owner_name_idx") ||
			db.IsUniqueViolation(err, "service_plans_global_name_idx") {
			return Plan{}, fmt.Errorf("%w: a plan called %q already exists", ErrNameTaken, input.Name)
		}
		return Plan{}, fmt.Errorf("insert plan: %w", err)
	}
	return r.GetPlan(ctx, id)
}

// UpdatePlan replaces a plan's limits.
//
// A whole replacement rather than a patch, and that is the honest shape: the
// limits are one promise, and editing them one field at a time through
// COALESCE would make "unset the mailbox limit" indistinguishable from "leave
// it alone" — which is exactly the unlimited/zero confusion this package
// exists to avoid.
func (r *Repository) UpdatePlan(ctx context.Context, id string, input PlanInput) (Plan, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE service_plans SET
			name = $2, description = $3,
			disk_mb = $4, bandwidth_mb = $5, max_websites = $6, max_databases = $7,
			max_mailboxes = $8, max_ftp_users = $9, max_cron_jobs = $10,
			max_subdomains = $11, enforcement = $12,
			cpu_percent = $13, memory_mb = $14, io_weight = $15,
			updated_at = now()
		WHERE id = $1::uuid`,
		id, strings.TrimSpace(input.Name), input.Description,
		input.Limits.DiskMB, input.Limits.BandwidthMB, input.Limits.MaxWebsites,
		input.Limits.MaxDatabases, input.Limits.MaxMailboxes, input.Limits.MaxFTPUsers,
		input.Limits.MaxCronJobs, input.Limits.MaxSubdomains, input.Enforcement,
		input.Isolation.CPUPercent, input.Isolation.MemoryMB, input.Isolation.IOWeight)
	if err != nil {
		if db.IsUniqueViolation(err, "service_plans_owner_name_idx") ||
			db.IsUniqueViolation(err, "service_plans_global_name_idx") {
			return Plan{}, fmt.Errorf("%w: a plan called %q already exists", ErrNameTaken, input.Name)
		}
		return Plan{}, fmt.Errorf("update plan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Plan{}, ErrNotFound
	}
	return r.GetPlan(ctx, id)
}

// DeletePlan removes a plan nothing is on.
func (r *Repository) DeletePlan(ctx context.Context, id string) error {
	var inUse int
	err := r.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM subscriptions WHERE plan_id = $1::uuid)
		     + (SELECT count(*) FROM subscription_addons WHERE plan_id = $1::uuid)`,
		id).Scan(&inUse)
	if err != nil {
		return fmt.Errorf("check whether the plan is in use: %w", err)
	}
	if inUse > 0 {
		return fmt.Errorf("%w: %d subscription(s) are on it", ErrPlanInUse, inUse)
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM service_plans WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete plan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------- subscriptions

const subscriptionColumns = `
	s.id::text, s.owner_user_id::text, o.username, s.plan_id::text, pl.name,
	s.name, s.status, s.suspended_reason, s.suspended_at,
	s.slice_name, s.isolation_state, s.isolation_detail, s.isolation_applied_at,
	s.created_at, s.updated_at`

func scanSubscription(row pgx.Row) (Subscription, error) {
	var s Subscription
	err := row.Scan(&s.ID, &s.OwnerUserID, &s.OwnerUsername, &s.PlanID, &s.PlanName,
		&s.Name, &s.Status, &s.SuspendedReason, &s.SuspendedAt,
		&s.SliceName, &s.IsolationState, &s.IsolationDetail, &s.IsolationAppliedAt,
		&s.CreatedAt, &s.UpdatedAt)
	return s, err
}

// ListSubscriptions returns the subscriptions owned at or below actorID.
func (r *Repository) ListSubscriptions(ctx context.Context, actorID string, seeAll bool) ([]Subscription, error) {
	query := descendantsCTE + `
		SELECT ` + subscriptionColumns + `
		FROM subscriptions s
		JOIN users o ON o.id = s.owner_user_id
		JOIN service_plans pl ON pl.id = s.plan_id
		WHERE s.owner_user_id IN (SELECT id FROM descendants)
		ORDER BY o.username, lower(s.name)`
	if seeAll {
		query = `
		SELECT ` + subscriptionColumns + `
		FROM subscriptions s
		JOIN users o ON o.id = s.owner_user_id
		JOIN service_plans pl ON pl.id = s.plan_id
		WHERE $1::uuid IS NOT NULL
		ORDER BY o.username, lower(s.name)`
	}

	rows, err := r.pool.Query(ctx, query, actorID)
	if err != nil {
		return nil, fmt.Errorf("select subscriptions: %w", err)
	}
	defer rows.Close()

	subscriptions := []Subscription{}
	for rows.Next() {
		subscription, err := scanSubscription(rows)
		if err != nil {
			return nil, fmt.Errorf("scan subscription: %w", err)
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate subscriptions: %w", err)
	}
	return subscriptions, nil
}

// GetSubscription reads one subscription without its add-ons or usage.
func (r *Repository) GetSubscription(ctx context.Context, id string) (Subscription, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+subscriptionColumns+`
		FROM subscriptions s
		JOIN users o ON o.id = s.owner_user_id
		JOIN service_plans pl ON pl.id = s.plan_id
		WHERE s.id = $1::uuid`, id)

	subscription, err := scanSubscription(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subscription{}, ErrNotFound
	}
	if err != nil {
		return Subscription{}, fmt.Errorf("select subscription: %w", err)
	}
	return subscription, nil
}

// CreateSubscription inserts a subscription.
func (r *Repository) CreateSubscription(ctx context.Context, ownerID, planID, name string) (Subscription, error) {
	var id string
	err := r.pool.QueryRow(ctx, `
		INSERT INTO subscriptions (owner_user_id, plan_id, name)
		VALUES ($1::uuid, $2::uuid, $3)
		RETURNING id::text`, ownerID, planID, strings.TrimSpace(name)).Scan(&id)
	if err != nil {
		if db.IsUniqueViolation(err, "subscriptions_owner_name_idx") {
			return Subscription{}, fmt.Errorf(
				"%w: that account already has a subscription called %q", ErrNameTaken, name)
		}
		return Subscription{}, fmt.Errorf("insert subscription: %w", err)
	}

	// The slice name is derived from the id rather than the name, because the
	// name can be edited and a systemd unit that renamed itself would leave
	// the old one behind capping nothing.
	slice := SliceNameFor(id)
	if _, err := r.pool.Exec(ctx,
		`UPDATE subscriptions SET slice_name = $2 WHERE id = $1::uuid`, id, slice); err != nil {
		return Subscription{}, fmt.Errorf("record the subscription's slice name: %w", err)
	}
	return r.GetSubscription(ctx, id)
}

// SliceNameFor builds the systemd slice name for a subscription.
//
// From the id, and with the dashes removed: systemd reads a dash in a slice
// name as a level of nesting, so "jothost-sub-a-b.slice" would be a child of
// "jothost-sub-a.slice" — a hierarchy nobody intended, in which one
// subscription's cap silently contains another's.
func SliceNameFor(subscriptionID string) string {
	return "jothost-sub-" + strings.ReplaceAll(subscriptionID, "-", "") + ".slice"
}

// SetSubscriptionPlan moves a subscription onto another plan.
func (r *Repository) SetSubscriptionPlan(ctx context.Context, id, planID string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE subscriptions SET plan_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid`, id, planID)
	if err != nil {
		return fmt.Errorf("assign the plan: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameSubscription changes a subscription's name.
func (r *Repository) RenameSubscription(ctx context.Context, id, name string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE subscriptions SET name = $2, updated_at = now() WHERE id = $1::uuid`,
		id, strings.TrimSpace(name))
	if err != nil {
		if db.IsUniqueViolation(err, "subscriptions_owner_name_idx") {
			return fmt.Errorf("%w: that account already has a subscription called %q",
				ErrNameTaken, name)
		}
		return fmt.Errorf("rename the subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSubscriptionStatus suspends or resumes a subscription.
func (r *Repository) SetSubscriptionStatus(ctx context.Context, id, status, reason string) error {
	var suspendedAt *time.Time
	if status == StatusSuspended {
		now := time.Now().UTC()
		suspendedAt = &now
	} else {
		reason = ""
	}

	tag, err := r.pool.Exec(ctx, `
		UPDATE subscriptions
		SET status = $2, suspended_reason = $3, suspended_at = $4, updated_at = now()
		WHERE id = $1::uuid`, id, status, reason, suspendedAt)
	if err != nil {
		return fmt.Errorf("set the subscription status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetIsolationState records what the host said about a subscription's limits.
func (r *Repository) SetIsolationState(ctx context.Context, id, state, detail string) error {
	var appliedAt *time.Time
	if state == IsolationApplied {
		now := time.Now().UTC()
		appliedAt = &now
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE subscriptions
		SET isolation_state = $2, isolation_detail = $3,
		    isolation_applied_at = $4, updated_at = now()
		WHERE id = $1::uuid`, id, state, detail, appliedAt)
	if err != nil {
		return fmt.Errorf("record the isolation state: %w", err)
	}
	return nil
}

// DeleteSubscription removes a subscription that owns no websites.
func (r *Repository) DeleteSubscription(ctx context.Context, id string) error {
	var websites int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM websites WHERE subscription_id = $1::uuid`, id).
		Scan(&websites); err != nil {
		return fmt.Errorf("check what the subscription owns: %w", err)
	}
	if websites > 0 {
		return fmt.Errorf("%w: %d website(s). Move or delete them first",
			ErrSubscriptionInUse, websites)
	}

	tag, err := r.pool.Exec(ctx, `DELETE FROM subscriptions WHERE id = $1::uuid`, id)
	if err != nil {
		return fmt.Errorf("delete subscription: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------------ addons

// ListAddons returns a subscription's add-ons with the limits each contributes.
func (r *Repository) ListAddons(ctx context.Context, subscriptionID string) ([]Addon, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT a.plan_id::text, p.name, a.quantity,
		       p.disk_mb, p.bandwidth_mb, p.max_websites, p.max_databases,
		       p.max_mailboxes, p.max_ftp_users, p.max_cron_jobs, p.max_subdomains
		FROM subscription_addons a
		JOIN service_plans p ON p.id = a.plan_id
		WHERE a.subscription_id = $1::uuid
		ORDER BY lower(p.name)`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("select add-ons: %w", err)
	}
	defer rows.Close()

	addons := []Addon{}
	for rows.Next() {
		var a Addon
		if err := rows.Scan(&a.PlanID, &a.Name, &a.Quantity,
			&a.Limits.DiskMB, &a.Limits.BandwidthMB, &a.Limits.MaxWebsites,
			&a.Limits.MaxDatabases, &a.Limits.MaxMailboxes, &a.Limits.MaxFTPUsers,
			&a.Limits.MaxCronJobs, &a.Limits.MaxSubdomains); err != nil {
			return nil, fmt.Errorf("scan add-on: %w", err)
		}
		addons = append(addons, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate add-ons: %w", err)
	}
	return addons, nil
}

// SetAddon attaches an add-on to a subscription, or changes how many.
func (r *Repository) SetAddon(ctx context.Context, subscriptionID, planID string, quantity int) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO subscription_addons (subscription_id, plan_id, quantity)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (subscription_id, plan_id) DO UPDATE SET quantity = EXCLUDED.quantity`,
		subscriptionID, planID, quantity)
	if err != nil {
		return fmt.Errorf("attach the add-on: %w", err)
	}
	return nil
}

// RemoveAddon detaches an add-on.
func (r *Repository) RemoveAddon(ctx context.Context, subscriptionID, planID string) error {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM subscription_addons
		WHERE subscription_id = $1::uuid AND plan_id = $2::uuid`, subscriptionID, planID)
	if err != nil {
		return fmt.Errorf("remove the add-on: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ------------------------------------------------------------------ usage

// CountUsage counts what a subscription is using, from the rows this panel
// wrote itself.
//
// One query rather than six, because six would be six moments in time: a
// caller checking a website limit while a database is being created would
// otherwise get counts that never coexisted. Every count reaches the
// subscription through websites, which is what makes the website the unit of
// tenancy in this panel.
//
// Subdomains are counted apart from websites and are excluded from the website
// count, because they are the same table. A plan selling "5 websites, 20
// subdomains" would otherwise be sold and then enforced as something else.
func (r *Repository) CountUsage(ctx context.Context, subscriptionID string) (Usage, error) {
	var usage Usage
	err := r.pool.QueryRow(ctx, `
		WITH owned AS (
			SELECT id, parent_website_id FROM websites WHERE subscription_id = $1::uuid
		)
		SELECT
			(SELECT count(*) FROM owned WHERE parent_website_id IS NULL),
			(SELECT count(*) FROM owned WHERE parent_website_id IS NOT NULL),
			(SELECT count(*) FROM databases  d WHERE d.website_id  IN (SELECT id FROM owned)),
			(SELECT count(*) FROM cron_jobs  c WHERE c.website_id  IN (SELECT id FROM owned)),
			(SELECT count(*) FROM ftp_users  f WHERE f.website_id  IN (SELECT id FROM owned)),
			(SELECT count(*) FROM mailboxes  m
			   JOIN mail_domains md ON md.id = m.domain_id
			  WHERE md.website_id IN (SELECT id FROM owned))`,
		subscriptionID).Scan(&usage.Websites, &usage.Subdomains, &usage.Databases,
		&usage.CronJobs, &usage.FTPUsers, &usage.Mailboxes)
	if err != nil {
		return Usage{}, fmt.Errorf("count what the subscription uses: %w", err)
	}

	measured, err := r.readMeasuredUsage(ctx, subscriptionID)
	if err != nil {
		return Usage{}, err
	}
	usage.DiskBytes = measured.DiskBytes
	usage.BandwidthBytes = measured.BandwidthBytes
	usage.PeriodStart = measured.PeriodStart
	usage.MeasuredAt = measured.MeasuredAt
	usage.MeasureError = measured.MeasureError
	return usage, nil
}

// readMeasuredUsage reads the disk and bandwidth figures a sampler wrote.
//
// A subscription that has never been measured has no row, and that is returned
// as nil rather than zero. See the column comments in migration 0023: "not
// measured" and "using nothing" are opposite things to tell a customer.
func (r *Repository) readMeasuredUsage(ctx context.Context, subscriptionID string) (Usage, error) {
	var usage Usage
	err := r.pool.QueryRow(ctx, `
		SELECT disk_bytes, bandwidth_bytes, period_start, measured_at, measure_error
		FROM subscription_usage WHERE subscription_id = $1::uuid`, subscriptionID).
		Scan(&usage.DiskBytes, &usage.BandwidthBytes, &usage.PeriodStart,
			&usage.MeasuredAt, &usage.MeasureError)
	if errors.Is(err, pgx.ErrNoRows) {
		return Usage{}, nil
	}
	if err != nil {
		return Usage{}, fmt.Errorf("read the measured usage: %w", err)
	}
	return usage, nil
}

// RecordUsageParams is one measurement.
type RecordUsageParams struct {
	SubscriptionID string
	PeriodStart    time.Time
	DiskBytes      *int64
	// RawBandwidth is what the host's access logs currently total. The delta
	// against the last reading is what accumulates; a reading lower than the
	// last means a log rotated, and the whole reading is the delta.
	RawBandwidth *int64
	Error        string
}

// RecordUsage stores a measurement, accumulating bandwidth across rotations.
//
// The accumulation is done in SQL rather than read-modify-write in Go, so two
// samplers cannot both read the old total and both add their delta to it.
func (r *Repository) RecordUsage(ctx context.Context, params RecordUsageParams) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO subscription_usage (
			subscription_id, period_start, disk_bytes,
			bandwidth_bytes, bandwidth_raw_bytes, measured_at, measure_error, updated_at)
		VALUES ($1::uuid, $2, $3, COALESCE($4, 0), $4, now(), $5, now())
		ON CONFLICT (subscription_id) DO UPDATE SET
			disk_bytes = EXCLUDED.disk_bytes,
			-- A new period starts the count again; within one, add the delta.
			-- A raw reading below the last one means the log rotated, so the
			-- whole reading is the delta rather than a negative number.
			bandwidth_bytes = CASE
				WHEN EXCLUDED.period_start > subscription_usage.period_start
					THEN COALESCE(EXCLUDED.bandwidth_raw_bytes, 0)
				WHEN EXCLUDED.bandwidth_raw_bytes IS NULL
					THEN subscription_usage.bandwidth_bytes
				WHEN subscription_usage.bandwidth_raw_bytes IS NULL
					THEN EXCLUDED.bandwidth_raw_bytes
				WHEN EXCLUDED.bandwidth_raw_bytes >= subscription_usage.bandwidth_raw_bytes
					THEN COALESCE(subscription_usage.bandwidth_bytes, 0)
					   + EXCLUDED.bandwidth_raw_bytes - subscription_usage.bandwidth_raw_bytes
				ELSE COALESCE(subscription_usage.bandwidth_bytes, 0)
				   + EXCLUDED.bandwidth_raw_bytes
			END,
			bandwidth_raw_bytes = COALESCE(EXCLUDED.bandwidth_raw_bytes,
			                               subscription_usage.bandwidth_raw_bytes),
			period_start = EXCLUDED.period_start,
			measured_at = now(),
			measure_error = EXCLUDED.measure_error,
			updated_at = now()`,
		params.SubscriptionID, params.PeriodStart, params.DiskBytes,
		params.RawBandwidth, params.Error)
	if err != nil {
		return fmt.Errorf("record the measured usage: %w", err)
	}
	return nil
}

// --------------------------------------------------------------- websites

// ListWebsites returns the websites a subscription owns.
func (r *Repository) ListWebsites(ctx context.Context, subscriptionID string) ([]Website, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id::text, primary_domain, status, document_root
		FROM websites WHERE subscription_id = $1::uuid
		ORDER BY primary_domain`, subscriptionID)
	if err != nil {
		return nil, fmt.Errorf("select the subscription's websites: %w", err)
	}
	defer rows.Close()

	websites := []Website{}
	for rows.Next() {
		var w Website
		if err := rows.Scan(&w.ID, &w.PrimaryDomain, &w.Status, &w.DocumentRoot); err != nil {
			return nil, fmt.Errorf("scan website: %w", err)
		}
		websites = append(websites, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate websites: %w", err)
	}
	return websites, nil
}

// AssignWebsite puts a website inside a subscription, or takes it out.
func (r *Repository) AssignWebsite(ctx context.Context, websiteID string, subscriptionID *string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE websites SET subscription_id = $2::uuid, updated_at = now()
		WHERE id = $1::uuid`, websiteID, subscriptionID)
	if err != nil {
		return fmt.Errorf("assign the website: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SubscriptionForWebsite returns the subscription a website belongs to, or
// ErrNotFound when it belongs to none.
func (r *Repository) SubscriptionForWebsite(ctx context.Context, websiteID string) (string, error) {
	// An empty id is "no such thing", not a query. Passed through it becomes
	// "invalid input syntax for type uuid", which reaches a caller as an
	// internal error about a request that was merely about nothing.
	if strings.TrimSpace(websiteID) == "" {
		return "", ErrNotFound
	}
	var id *string
	err := r.pool.QueryRow(ctx,
		`SELECT subscription_id::text FROM websites WHERE id = $1::uuid`, websiteID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && id == nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find the website's subscription: %w", err)
	}
	return *id, nil
}

// WebsiteForMailDomain returns the website a mail domain belongs to.
//
// A mail domain need not belong to a website — a domain that exists only for
// mail is an ordinary thing to have — and that answer is ErrNotFound, which
// the quota guard reads as "nothing to charge" rather than as a failure.
func (r *Repository) WebsiteForMailDomain(ctx context.Context, domainID string) (string, error) {
	// An empty id is "no such thing", not a query. Passed through it becomes
	// "invalid input syntax for type uuid", which reaches a caller as an
	// internal error about a request that was merely about nothing.
	if strings.TrimSpace(domainID) == "" {
		return "", ErrNotFound
	}
	var id *string
	err := r.pool.QueryRow(ctx,
		`SELECT website_id::text FROM mail_domains WHERE id = $1::uuid`, domainID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && id == nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find the mail domain's website: %w", err)
	}
	return *id, nil
}

// SubscriptionsForOwner returns the ids a user owns, for the quota guard.
func (r *Repository) SubscriptionsForOwner(ctx context.Context, ownerID string) ([]string, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id::text FROM subscriptions WHERE owner_user_id = $1::uuid ORDER BY created_at`,
		ownerID)
	if err != nil {
		return nil, fmt.Errorf("select the account's subscriptions: %w", err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan subscription id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListAllForMeasurement returns every subscription with the document roots of
// the websites it owns, for the sampler.
func (r *Repository) ListAllForMeasurement(ctx context.Context) (map[string][]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id::text, COALESCE(w.document_root, '')
		FROM subscriptions s
		LEFT JOIN websites w ON w.subscription_id = s.id
		ORDER BY s.id`)
	if err != nil {
		return nil, fmt.Errorf("select subscriptions to measure: %w", err)
	}
	defer rows.Close()

	roots := map[string][]string{}
	for rows.Next() {
		var id, root string
		if err := rows.Scan(&id, &root); err != nil {
			return nil, fmt.Errorf("scan a subscription to measure: %w", err)
		}
		if _, ok := roots[id]; !ok {
			roots[id] = []string{}
		}
		if root != "" {
			roots[id] = append(roots[id], root)
		}
	}
	return roots, rows.Err()
}

// ------------------------------------------------------------ tier lookup

// TierOf returns an account's tier.
func (r *Repository) TierOf(ctx context.Context, userID string) (string, error) {
	var tier string
	err := r.pool.QueryRow(ctx, `SELECT tier FROM users WHERE id = $1::uuid`, userID).Scan(&tier)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read the account tier: %w", err)
	}
	if err := validate.AccountTier(tier); err != nil {
		return "", err
	}
	return tier, nil
}

// nullable turns an empty string into a NULL.
//
// An empty email must be NULL, not "", or the unique index collides across
// every account without an address.
func nullable(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
