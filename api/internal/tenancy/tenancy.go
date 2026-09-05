// Package tenancy owns the hierarchy of accounts, the plans sold to them, the
// subscriptions those plans become, and the enforcement of what a subscription
// may use.
//
// Four ideas, and they are worth naming apart because conflating any two of
// them is how a control panel ends up with quotas that do not hold.
//
// **A tier is not a role.** A role says what somebody may do; a tier says whose
// accounts they may do it to. An operator and a reseller can hold identical
// permissions and still must not see each other's customers, so the hierarchy
// is a separate fact stored on the account and checked separately.
//
// **A plan is not a subscription.** A plan is a promise, edited by whoever
// sells it; a subscription is one customer's instance of that promise, with
// add-ons on top. Editing a plan changes every subscription on it, which is
// what a plan is for and is why a subscription cannot be edited into
// something its plan does not allow.
//
// **A limit is not a count.** Six of the eight quota dimensions are counted
// from rows this panel wrote, so a limit on one is enforceable at the moment
// somebody asks for one more. The other two — disk and bandwidth — are
// measured on the host after the fact, so a limit on one can only ever be
// reported. Calling both "hard limits" without saying which is which is how a
// panel promises enforcement it does not perform.
//
// **Unlimited is not zero.** A nil limit means no limit; a zero limit means
// none at all. They are opposite promises and this package never collapses
// them.
package tenancy

import (
	"errors"
	"time"
)

// Errors returned by the service.
var (
	// ErrNotFound covers a plan, subscription, or account that is not there —
	// and also one the caller may not see, which is deliberate: a reseller
	// probing ids must not be able to tell "no such subscription" apart from
	// "somebody else's subscription".
	ErrNotFound = errors.New("not found")
	// ErrForbidden covers an action inside the caller's permissions but
	// outside their part of the hierarchy.
	ErrForbidden = errors.New("that account is not yours to manage")
	// ErrNameTaken covers a duplicate plan or subscription name.
	ErrNameTaken = errors.New("that name is already in use")
	// ErrPlanInUse covers deleting a plan a subscription is on.
	ErrPlanInUse = errors.New("that plan is in use")
	// ErrSubscriptionInUse covers deleting a subscription that still owns
	// websites.
	ErrSubscriptionInUse = errors.New("that subscription still owns websites")
	// ErrHasChildren covers deleting an account other accounts answer to.
	ErrHasChildren = errors.New("that account still has accounts under it")
	// ErrQuotaExceeded covers a hard limit reached.
	ErrQuotaExceeded = errors.New("quota exceeded")
	// ErrSuspended covers a subscription that has been stopped.
	ErrSuspended = errors.New("that subscription is suspended")
	// ErrNotAnAddon and ErrNotAPlan cover using one kind of plan as the other.
	ErrNotAnAddon = errors.New("that is a plan, not an add-on")
	ErrNotAPlan   = errors.New("that is an add-on, not a plan")
	// ErrCannotImpersonate covers an impersonation the hierarchy forbids.
	ErrCannotImpersonate = errors.New("that account cannot be impersonated by you")
	// ErrAlreadyImpersonating covers an attempt to impersonate from inside an
	// impersonated session. See impersonation.go for why that is refused
	// rather than allowed to nest.
	ErrAlreadyImpersonating = errors.New("an impersonated session cannot impersonate")
)

// Audit actions.
//
// Every one of these changes who may use how much of somebody's server, and
// the last two change who the panel thinks somebody is.
const (
	ActionAccountCreate       = "tenant.account.create"
	ActionAccountUpdate       = "tenant.account.update"
	ActionAccountDelete       = "tenant.account.delete"
	ActionPlanCreate          = "tenant.plan.create"
	ActionPlanUpdate          = "tenant.plan.update"
	ActionPlanDelete          = "tenant.plan.delete"
	ActionSubscriptionCreate  = "tenant.subscription.create"
	ActionSubscriptionUpdate  = "tenant.subscription.update"
	ActionSubscriptionDelete  = "tenant.subscription.delete"
	ActionSubscriptionSuspend = "tenant.subscription.suspend"
	ActionSubscriptionResume  = "tenant.subscription.resume"
	ActionAddonAdd            = "tenant.addon.add"
	ActionAddonRemove         = "tenant.addon.remove"
	ActionIsolationApply      = "tenant.isolation.apply"
	ActionQuotaRefused        = "tenant.quota.refused"
	ActionImpersonateStart    = "tenant.impersonate.start"
	ActionImpersonateEnd      = "tenant.impersonate.end"

	ResourceTypeAccount      = "account"
	ResourceTypePlan         = "service_plan"
	ResourceTypeSubscription = "subscription"
)

// Subscription statuses.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
)

// Isolation states, mirroring the Agent's.
const (
	IsolationNone     = "none"
	IsolationApplied  = "applied"
	IsolationDeclared = "declared"
	IsolationFailed   = "failed"
)

// Account is a panel user seen through the hierarchy.
type Account struct {
	ID       string  `json:"id"`
	Username string  `json:"username"`
	Email    *string `json:"email"`
	Tier     string  `json:"tier"`
	// ParentID is who this account answers to. Nil only for an admin.
	ParentID       *string    `json:"parent_id"`
	ParentUsername *string    `json:"parent_username"`
	FullName       *string    `json:"full_name"`
	Company        *string    `json:"company"`
	Status         string     `json:"status"`
	Roles          []string   `json:"roles"`
	CreatedAt      time.Time  `json:"created_at"`
	LastLoginAt    *time.Time `json:"last_login_at"`
	// Subscriptions is how many this account owns, for a list page that would
	// otherwise need one query per row.
	Subscriptions int `json:"subscriptions"`
}

// Limits is one set of quota dimensions.
//
// Every field is a pointer and nil means unlimited. That is the whole reason
// they are pointers: a plan with no mailbox limit and a plan including no
// mailboxes are opposite promises, and an int cannot hold both.
type Limits struct {
	DiskMB        *int `json:"disk_mb"`
	BandwidthMB   *int `json:"bandwidth_mb"`
	MaxWebsites   *int `json:"max_websites"`
	MaxDatabases  *int `json:"max_databases"`
	MaxMailboxes  *int `json:"max_mailboxes"`
	MaxFTPUsers   *int `json:"max_ftp_users"`
	MaxCronJobs   *int `json:"max_cron_jobs"`
	MaxSubdomains *int `json:"max_subdomains"`
}

// Isolation is the resource cap a plan carries.
type Isolation struct {
	CPUPercent *int `json:"cpu_percent"`
	MemoryMB   *int `json:"memory_mb"`
	IOWeight   *int `json:"io_weight"`
}

// Plan is a service plan or an add-on.
type Plan struct {
	ID            string  `json:"id"`
	OwnerUserID   *string `json:"owner_user_id"`
	OwnerUsername *string `json:"owner_username"`
	Name          string  `json:"name"`
	Description   string  `json:"description"`
	Kind          string  `json:"kind"`
	Limits        Limits  `json:"limits"`
	// Enforcement is 'hard' or 'soft'. It decides what happens when a counted
	// dimension is full; see quota.go for what it can and cannot mean for a
	// measured one.
	Enforcement   string    `json:"enforcement"`
	Isolation     Isolation `json:"isolation"`
	Subscriptions int       `json:"subscriptions"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Addon is an add-on attached to a subscription.
type Addon struct {
	PlanID   string `json:"plan_id"`
	Name     string `json:"name"`
	Quantity int    `json:"quantity"`
	Limits   Limits `json:"limits"`
}

// Usage is what a subscription was measured and counted to be using.
//
// The counted dimensions are plain integers because the panel wrote every row
// and knows the number exactly. The measured ones are pointers because the
// answer may be "not measured", which is not the same as zero and must not be
// rendered as one.
type Usage struct {
	Websites   int `json:"websites"`
	Databases  int `json:"databases"`
	Mailboxes  int `json:"mailboxes"`
	FTPUsers   int `json:"ftp_users"`
	CronJobs   int `json:"cron_jobs"`
	Subdomains int `json:"subdomains"`

	DiskBytes      *int64 `json:"disk_bytes"`
	BandwidthBytes *int64 `json:"bandwidth_bytes"`

	PeriodStart  *time.Time `json:"period_start"`
	MeasuredAt   *time.Time `json:"measured_at"`
	MeasureError string     `json:"measure_error"`
}

// Subscription is one customer's instance of a plan.
type Subscription struct {
	ID            string `json:"id"`
	OwnerUserID   string `json:"owner_user_id"`
	OwnerUsername string `json:"owner_username"`
	PlanID        string `json:"plan_id"`
	PlanName      string `json:"plan_name"`
	Name          string `json:"name"`
	Status        string `json:"status"`

	SuspendedReason string     `json:"suspended_reason"`
	SuspendedAt     *time.Time `json:"suspended_at"`

	SliceName          string     `json:"slice_name"`
	IsolationState     string     `json:"isolation_state"`
	IsolationDetail    string     `json:"isolation_detail"`
	IsolationAppliedAt *time.Time `json:"isolation_applied_at"`

	// Enforcement and Limits are the plan's, with add-ons folded in. They are
	// on the subscription because that is where a caller needs them: what this
	// customer may use, not what the plan says before add-ons.
	Enforcement string    `json:"enforcement"`
	Limits      Limits    `json:"limits"`
	Isolation   Isolation `json:"isolation"`
	Addons      []Addon   `json:"addons"`

	Usage     Usage     `json:"usage"`
	Websites  []Website `json:"websites"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Website is the little a subscription page needs about a site it owns.
type Website struct {
	ID            string `json:"id"`
	PrimaryDomain string `json:"primary_domain"`
	Status        string `json:"status"`
	DocumentRoot  string `json:"document_root"`
}

// Impersonation is one record of somebody using the panel as somebody else.
type Impersonation struct {
	ID              string     `json:"id"`
	ActorUserID     string     `json:"actor_user_id"`
	ActorUsername   string     `json:"actor_username"`
	SubjectUserID   string     `json:"subject_user_id"`
	SubjectUsername string     `json:"subject_username"`
	Reason          string     `json:"reason"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
}

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	Username  string
	IPAddress string
	UserAgent string
	// Tier and Permissions decide what this actor may reach. They come from
	// the request's claims plus one lookup, never from the request body.
	Tier        string
	Permissions []string
	// ImpersonatorUserID is set when this actor is a session somebody else
	// opened by impersonating. It is carried into every audit row so the trail
	// says who actually did it rather than whose name was on the session.
	ImpersonatorUserID string
}
