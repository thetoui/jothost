package tenancy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/validate"
)

// Service holds the tenancy use cases.
type Service struct {
	repo     *Repository
	users    *users.Repository
	rbac     *rbac.Repository
	sessions *sessions.Repository
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	now      func() time.Time
}

// Dependencies bundles what a Service needs.
type Dependencies struct {
	Repo     *Repository
	Users    *users.Repository
	RBAC     *rbac.Repository
	Sessions *sessions.Repository
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Log      *slog.Logger
}

// NewService builds a Service.
func NewService(deps Dependencies) *Service {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     deps.Repo,
		users:    deps.Users,
		rbac:     deps.RBAC,
		sessions: deps.Sessions,
		agent:    deps.Agent,
		audit:    deps.Audit,
		log:      log,
		now:      time.Now,
	}
}

// seesEverything reports whether an actor is above the hierarchy rather than
// inside it.
//
// An admin created by the installer has no children at all, so scoping them to
// their descendants would show the owner of the machine nothing. Every other
// tier is scoped.
func seesEverything(actor Actor) bool { return actor.Tier == validate.TierAdmin }

// ---------------------------------------------------------------- accounts

// Accounts returns the accounts an actor may see.
func (s *Service) Accounts(ctx context.Context, actor Actor) ([]Account, error) {
	accounts, err := s.repo.ListAccounts(ctx, actor.UserID, seesEverything(actor))
	if err != nil {
		return nil, err
	}
	for i := range accounts {
		roles, err := s.rbac.RolesForUser(ctx, accounts[i].ID)
		if err != nil {
			return nil, err
		}
		accounts[i].Roles = roles
	}
	return accounts, nil
}

// requireDescendant refuses an action against an account outside the actor's
// part of the tree.
//
// The refusal is ErrNotFound rather than ErrForbidden on purpose: a reseller
// walking ids must not be able to tell "no such account" from "somebody else's
// account", because the second answer confirms an account exists.
func (s *Service) requireDescendant(ctx context.Context, actor Actor, subjectID string) error {
	if seesEverything(actor) {
		if _, err := s.repo.GetAccount(ctx, subjectID); err != nil {
			return err
		}
		return nil
	}
	ok, err := s.repo.IsDescendant(ctx, actor.UserID, subjectID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// CreateAccountRequest is a new account as somebody described it.
type CreateAccountRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
	Tier     string `json:"tier"`
	FullName string `json:"full_name"`
	Company  string `json:"company"`
	// ParentID is optional and defaults to the actor. It exists so an admin
	// can create a customer under a named reseller in one step rather than
	// creating it under themselves and moving it.
	ParentID string `json:"parent_id"`
}

// CreateAccount creates an account below the actor.
//
// The tier rule is the security boundary of this whole phase: an account may
// only ever be created strictly below its parent. A reseller cannot create a
// reseller, and nobody but an admin creates an admin — which is also why the
// tree cannot contain a cycle, since every edge runs from a higher tier to a
// lower one and following parents must terminate.
func (s *Service) CreateAccount(ctx context.Context, actor Actor, req CreateAccountRequest) (Account, error) {
	if err := validate.AccountTier(req.Tier); err != nil {
		return Account{}, err
	}
	username := users.NormalizeUsername(req.Username)
	if err := users.ValidateUsername(username); err != nil {
		return Account{}, err
	}
	if err := users.ValidateEmail(req.Email); err != nil {
		return Account{}, err
	}
	if err := validate.PersonName(req.FullName); err != nil {
		return Account{}, err
	}
	if err := validate.PersonName(req.Company); err != nil {
		return Account{}, err
	}

	parentID := req.ParentID
	if parentID == "" {
		parentID = actor.UserID
	}
	if parentID != actor.UserID {
		if err := s.requireDescendant(ctx, actor, parentID); err != nil {
			return Account{}, err
		}
	}
	parentTier, err := s.repo.TierOf(ctx, parentID)
	if err != nil {
		return Account{}, err
	}
	if !validate.TierMayOwn(parentTier, req.Tier) {
		return Account{}, fmt.Errorf(
			"%w: a %s account cannot own a %s account — an account is always created "+
				"strictly below its parent", ErrForbidden, parentTier, req.Tier)
	}

	hash, err := secrets.HashPassword(req.Password)
	if err != nil {
		return Account{}, fmt.Errorf("hash the password: %w", err)
	}

	account, err := s.repo.CreateAccount(ctx, CreateAccountParams{
		Username:     username,
		Email:        req.Email,
		PasswordHash: hash,
		Tier:         req.Tier,
		ParentID:     parentID,
		FullName:     req.FullName,
		Company:      req.Company,
	})
	if err != nil {
		return Account{}, err
	}

	// A reseller gets the reseller role. A customer gets none, and that is
	// deliberate rather than an omission: this phase scopes tenancy and not
	// the resource listings, so a role granting website.view today would show
	// a customer every website on the host. docs/PHASE22.md says so, and names
	// it as the next piece of work.
	account.Roles = []string{}
	if req.Tier == validate.TierReseller {
		if err := s.rbac.AssignRole(ctx, account.ID, rbac.RoleReseller); err != nil {
			return Account{}, fmt.Errorf("grant the reseller role: %w", err)
		}
		account.Roles = []string{rbac.RoleReseller}
	}

	s.record(ctx, actor, ActionAccountCreate, ResourceTypeAccount, account.ID,
		audit.StatusSuccess, map[string]any{
			"username": account.Username,
			"tier":     account.Tier,
			"parent":   parentID,
		})
	return account, nil
}

// UpdateAccount edits an account's contact details or status.
func (s *Service) UpdateAccount(ctx context.Context, actor Actor, id string,
	params UpdateAccountParams,
) (Account, error) {
	if err := s.requireDescendant(ctx, actor, id); err != nil {
		return Account{}, err
	}
	if id == actor.UserID && params.Status != nil && *params.Status != users.StatusActive {
		// Refused rather than allowed and regretted: an admin who disables
		// their own account has locked themselves out of the panel that would
		// let them re-enable it.
		return Account{}, fmt.Errorf("%w: an account cannot disable itself", ErrForbidden)
	}
	if params.Status != nil {
		switch *params.Status {
		case users.StatusActive, users.StatusDisabled, users.StatusLocked:
		default:
			return Account{}, users.ErrInvalidUserState
		}
	}
	if params.FullName != nil {
		if err := validate.PersonName(*params.FullName); err != nil {
			return Account{}, err
		}
	}
	if params.Company != nil {
		if err := validate.PersonName(*params.Company); err != nil {
			return Account{}, err
		}
	}

	account, err := s.repo.UpdateAccount(ctx, id, params)
	if err != nil {
		return Account{}, err
	}
	s.record(ctx, actor, ActionAccountUpdate, ResourceTypeAccount, id,
		audit.StatusSuccess, map[string]any{"username": account.Username})
	return account, nil
}

// DeleteAccount removes an account with nothing under it.
func (s *Service) DeleteAccount(ctx context.Context, actor Actor, id string) error {
	if id == actor.UserID {
		return fmt.Errorf("%w: an account cannot delete itself", ErrForbidden)
	}
	if err := s.requireDescendant(ctx, actor, id); err != nil {
		return err
	}
	account, err := s.repo.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAccount(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionAccountDelete, ResourceTypeAccount, id,
		audit.StatusSuccess, map[string]any{"username": account.Username})
	return nil
}

// ------------------------------------------------------------------- plans

// Plans returns the plans an actor may sell.
func (s *Service) Plans(ctx context.Context, actor Actor) ([]Plan, error) {
	return s.repo.ListPlans(ctx, actor.UserID, seesEverything(actor))
}

// Plan returns one plan the actor may see.
func (s *Service) Plan(ctx context.Context, actor Actor, id string) (Plan, error) {
	plan, err := s.repo.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if !s.mayUsePlan(actor, plan) {
		return Plan{}, ErrNotFound
	}
	return plan, nil
}

// mayUsePlan reports whether an actor may sell a plan.
func (s *Service) mayUsePlan(actor Actor, plan Plan) bool {
	if seesEverything(actor) {
		return true
	}
	if plan.OwnerUserID == nil {
		// The admin's own catalogue, published to everybody.
		return true
	}
	return *plan.OwnerUserID == actor.UserID
}

// mayEditPlan reports whether an actor may change a plan.
//
// Narrower than mayUsePlan on purpose: a reseller may *sell* the admin's
// published plans and may not *edit* them. Conflating the two would let a
// reseller raise the limits on the plan every other reseller sells.
func (s *Service) mayEditPlan(actor Actor, plan Plan) bool {
	if seesEverything(actor) {
		return true
	}
	return plan.OwnerUserID != nil && *plan.OwnerUserID == actor.UserID
}

// PlanRequest is a plan as somebody described it.
type PlanRequest struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Kind        string    `json:"kind"`
	Limits      Limits    `json:"limits"`
	Enforcement string    `json:"enforcement"`
	Isolation   Isolation `json:"isolation"`
}

// validatePlan checks a plan description.
func validatePlan(req *PlanRequest) error {
	if req.Kind == "" {
		req.Kind = validate.PlanKindPlan
	}
	if req.Enforcement == "" {
		req.Enforcement = validate.EnforcementHard
	}
	if err := validate.PlanKind(req.Kind); err != nil {
		return err
	}
	if err := validate.PlanName(req.Name); err != nil {
		return err
	}
	if err := validate.Enforcement(req.Enforcement); err != nil {
		return err
	}
	for dimension, value := range map[string]*int{
		validate.DimensionDisk:       req.Limits.DiskMB,
		validate.DimensionBandwidth:  req.Limits.BandwidthMB,
		validate.DimensionWebsites:   req.Limits.MaxWebsites,
		validate.DimensionDatabases:  req.Limits.MaxDatabases,
		validate.DimensionMailboxes:  req.Limits.MaxMailboxes,
		validate.DimensionFTPUsers:   req.Limits.MaxFTPUsers,
		validate.DimensionCronJobs:   req.Limits.MaxCronJobs,
		validate.DimensionSubdomains: req.Limits.MaxSubdomains,
	} {
		if err := validate.QuotaLimit(dimension, value); err != nil {
			return err
		}
	}
	return validate.Isolation(req.Isolation.CPUPercent, req.Isolation.MemoryMB,
		req.Isolation.IOWeight)
}

// CreatePlan builds a plan or an add-on.
func (s *Service) CreatePlan(ctx context.Context, actor Actor, req PlanRequest) (Plan, error) {
	if err := validatePlan(&req); err != nil {
		return Plan{}, err
	}

	// An admin's plan is published to everybody; a reseller's is their own.
	// The owner is taken from the actor rather than from the request, because
	// a request that could name an owner is a request that could plant a plan
	// in somebody else's catalogue.
	var owner *string
	if !seesEverything(actor) {
		id := actor.UserID
		owner = &id
	}

	plan, err := s.repo.CreatePlan(ctx, PlanInput{
		OwnerUserID: owner,
		Name:        req.Name,
		Description: req.Description,
		Kind:        req.Kind,
		Limits:      req.Limits,
		Enforcement: req.Enforcement,
		Isolation:   req.Isolation,
	})
	if err != nil {
		return Plan{}, err
	}
	s.record(ctx, actor, ActionPlanCreate, ResourceTypePlan, plan.ID,
		audit.StatusSuccess, map[string]any{"name": plan.Name, "kind": plan.Kind})
	return plan, nil
}

// UpdatePlan replaces a plan's limits.
//
// Every subscription on the plan is affected, which is what a plan is for, and
// the isolation is re-applied for each of them so a CPU cap changed here
// reaches the host rather than waiting for somebody to touch each customer.
func (s *Service) UpdatePlan(ctx context.Context, actor Actor, id string, req PlanRequest) (Plan, error) {
	existing, err := s.repo.GetPlan(ctx, id)
	if err != nil {
		return Plan{}, err
	}
	if !s.mayEditPlan(actor, existing) {
		if s.mayUsePlan(actor, existing) {
			return Plan{}, fmt.Errorf(
				"%w: this plan belongs to the server's administrator. Copy it into your "+
					"own catalogue to change it", ErrForbidden)
		}
		return Plan{}, ErrNotFound
	}
	req.Kind = existing.Kind
	if err := validatePlan(&req); err != nil {
		return Plan{}, err
	}

	plan, err := s.repo.UpdatePlan(ctx, id, PlanInput{
		Name:        req.Name,
		Description: req.Description,
		Limits:      req.Limits,
		Enforcement: req.Enforcement,
		Isolation:   req.Isolation,
	})
	if err != nil {
		return Plan{}, err
	}
	s.record(ctx, actor, ActionPlanUpdate, ResourceTypePlan, id,
		audit.StatusSuccess, map[string]any{"name": plan.Name})

	s.reapplyIsolationForPlan(ctx, actor, id)
	return plan, nil
}

// DeletePlan removes a plan nothing is on.
func (s *Service) DeletePlan(ctx context.Context, actor Actor, id string) error {
	plan, err := s.repo.GetPlan(ctx, id)
	if err != nil {
		return err
	}
	if !s.mayEditPlan(actor, plan) {
		return ErrNotFound
	}
	if err := s.repo.DeletePlan(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, ActionPlanDelete, ResourceTypePlan, id,
		audit.StatusSuccess, map[string]any{"name": plan.Name})
	return nil
}

// ----------------------------------------------------------- subscriptions

// Subscriptions returns the subscriptions an actor may see, each with its
// effective limits and what it is using.
func (s *Service) Subscriptions(ctx context.Context, actor Actor) ([]Subscription, error) {
	list, err := s.repo.ListSubscriptions(ctx, actor.UserID, seesEverything(actor))
	if err != nil {
		return nil, err
	}
	for i := range list {
		if err := s.decorate(ctx, &list[i]); err != nil {
			return nil, err
		}
	}
	return list, nil
}

// Subscription returns one subscription with its websites.
func (s *Service) Subscription(ctx context.Context, actor Actor, id string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	if err := s.decorate(ctx, &subscription); err != nil {
		return Subscription{}, err
	}
	websites, err := s.repo.ListWebsites(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	subscription.Websites = websites
	return subscription, nil
}

// decorate fills in a subscription's effective limits and usage.
func (s *Service) decorate(ctx context.Context, subscription *Subscription) error {
	plan, err := s.repo.GetPlan(ctx, subscription.PlanID)
	if err != nil {
		return err
	}
	addons, err := s.repo.ListAddons(ctx, subscription.ID)
	if err != nil {
		return err
	}
	usage, err := s.repo.CountUsage(ctx, subscription.ID)
	if err != nil {
		return err
	}

	subscription.Addons = addons
	subscription.Enforcement = plan.Enforcement
	subscription.Limits = EffectiveLimits(plan.Limits, addons)
	subscription.Isolation = plan.Isolation
	subscription.Usage = usage
	if subscription.Websites == nil {
		subscription.Websites = []Website{}
	}
	return nil
}

// CreateSubscriptionRequest is a subscription as somebody described it.
type CreateSubscriptionRequest struct {
	OwnerUserID string `json:"owner_user_id"`
	PlanID      string `json:"plan_id"`
	Name        string `json:"name"`
}

// CreateSubscription puts a customer on a plan.
func (s *Service) CreateSubscription(ctx context.Context, actor Actor,
	req CreateSubscriptionRequest,
) (Subscription, error) {
	if err := validate.SubscriptionName(req.Name); err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, req.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	plan, err := s.repo.GetPlan(ctx, req.PlanID)
	if err != nil {
		return Subscription{}, err
	}
	if !s.mayUsePlan(actor, plan) {
		return Subscription{}, ErrNotFound
	}
	if plan.Kind != validate.PlanKindPlan {
		return Subscription{}, ErrNotAPlan
	}

	subscription, err := s.repo.CreateSubscription(ctx, req.OwnerUserID, req.PlanID, req.Name)
	if err != nil {
		return Subscription{}, err
	}
	s.record(ctx, actor, ActionSubscriptionCreate, ResourceTypeSubscription, subscription.ID,
		audit.StatusSuccess, map[string]any{
			"name": subscription.Name, "plan": plan.Name, "owner": req.OwnerUserID,
		})

	s.applyIsolation(ctx, actor, subscription.ID)
	return s.Subscription(ctx, actor, subscription.ID)
}

// UpdateSubscriptionRequest changes a subscription. A nil field is left alone.
type UpdateSubscriptionRequest struct {
	Name   *string `json:"name"`
	PlanID *string `json:"plan_id"`
}

// UpdateSubscription renames a subscription or moves it onto another plan.
func (s *Service) UpdateSubscription(ctx context.Context, actor Actor, id string,
	req UpdateSubscriptionRequest,
) (Subscription, error) {
	existing, err := s.repo.GetSubscription(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, existing.OwnerUserID); err != nil {
		return Subscription{}, err
	}

	if req.Name != nil {
		if err := validate.SubscriptionName(*req.Name); err != nil {
			return Subscription{}, err
		}
		if err := s.repo.RenameSubscription(ctx, id, *req.Name); err != nil {
			return Subscription{}, err
		}
	}
	if req.PlanID != nil {
		plan, err := s.repo.GetPlan(ctx, *req.PlanID)
		if err != nil {
			return Subscription{}, err
		}
		if !s.mayUsePlan(actor, plan) {
			return Subscription{}, ErrNotFound
		}
		if plan.Kind != validate.PlanKindPlan {
			return Subscription{}, ErrNotAPlan
		}
		// A plan change is allowed even when the new plan's limits are below
		// what the customer already uses. Refusing would leave an operator
		// unable to downgrade anybody, and the panel would rather show a
		// subscription over its limit — which the page does, plainly — than
		// make the smaller plan unsellable.
		if err := s.repo.SetSubscriptionPlan(ctx, id, *req.PlanID); err != nil {
			return Subscription{}, err
		}
	}

	s.record(ctx, actor, ActionSubscriptionUpdate, ResourceTypeSubscription, id,
		audit.StatusSuccess, nil)
	s.applyIsolation(ctx, actor, id)
	return s.Subscription(ctx, actor, id)
}

// SetSubscriptionStatus suspends or resumes a subscription.
//
// Suspension stops a subscription growing: every quota-guarded creation is
// refused while it holds. It does *not* take the customer's websites offline
// — see docs/PHASE22.md, which says so rather than letting the word imply it.
func (s *Service) SetSubscriptionStatus(ctx context.Context, actor Actor, id, status, reason string) (Subscription, error) {
	switch status {
	case StatusActive, StatusSuspended:
	default:
		return Subscription{}, fmt.Errorf("a subscription is active or suspended, not %q", status)
	}
	if err := validate.Reason(reason); err != nil {
		return Subscription{}, err
	}

	existing, err := s.repo.GetSubscription(ctx, id)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, existing.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	if err := s.repo.SetSubscriptionStatus(ctx, id, status, reason); err != nil {
		return Subscription{}, err
	}

	action := ActionSubscriptionResume
	if status == StatusSuspended {
		action = ActionSubscriptionSuspend
	}
	s.record(ctx, actor, action, ResourceTypeSubscription, id,
		audit.StatusSuccess, map[string]any{"reason": reason})
	return s.Subscription(ctx, actor, id)
}

// DeleteSubscription removes a subscription that owns no websites.
func (s *Service) DeleteSubscription(ctx context.Context, actor Actor, id string) error {
	existing, err := s.repo.GetSubscription(ctx, id)
	if err != nil {
		return err
	}
	if err := s.requireDescendant(ctx, actor, existing.OwnerUserID); err != nil {
		return err
	}
	if err := s.repo.DeleteSubscription(ctx, id); err != nil {
		return err
	}

	// The slice goes with it. A cgroup left behind for a subscription that no
	// longer exists is a limit nobody can find the owner of.
	if existing.SliceName != "" && s.agent != nil {
		if _, err := s.agent.TenantIsolationRemove(ctx, httpx.RequestIDFromContext(ctx), existing.SliceName); err != nil {
			s.log.Warn("deleted a subscription but its slice could not be removed",
				"subscription_id", id, "slice", existing.SliceName, logger.KeyError, err.Error())
		}
	}

	s.record(ctx, actor, ActionSubscriptionDelete, ResourceTypeSubscription, id,
		audit.StatusSuccess, map[string]any{"name": existing.Name})
	return nil
}

// ------------------------------------------------------------------ addons

// AddAddon attaches an add-on to a subscription.
func (s *Service) AddAddon(ctx context.Context, actor Actor, subscriptionID, planID string,
	quantity int,
) (Subscription, error) {
	if err := validate.AddonQuantity(quantity); err != nil {
		return Subscription{}, err
	}
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	plan, err := s.repo.GetPlan(ctx, planID)
	if err != nil {
		return Subscription{}, err
	}
	if !s.mayUsePlan(actor, plan) {
		return Subscription{}, ErrNotFound
	}
	if plan.Kind != validate.PlanKindAddon {
		return Subscription{}, ErrNotAnAddon
	}

	if err := s.repo.SetAddon(ctx, subscriptionID, planID, quantity); err != nil {
		return Subscription{}, err
	}
	s.record(ctx, actor, ActionAddonAdd, ResourceTypeSubscription, subscriptionID,
		audit.StatusSuccess, map[string]any{"addon": plan.Name, "quantity": quantity})
	return s.Subscription(ctx, actor, subscriptionID)
}

// RemoveAddon detaches an add-on.
func (s *Service) RemoveAddon(ctx context.Context, actor Actor, subscriptionID, planID string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	if err := s.repo.RemoveAddon(ctx, subscriptionID, planID); err != nil {
		return Subscription{}, err
	}
	s.record(ctx, actor, ActionAddonRemove, ResourceTypeSubscription, subscriptionID,
		audit.StatusSuccess, map[string]any{"addon": planID})
	return s.Subscription(ctx, actor, subscriptionID)
}

// -------------------------------------------------------------- websites

// AssignWebsite puts a website inside a subscription, or takes it out.
func (s *Service) AssignWebsite(ctx context.Context, actor Actor, subscriptionID, websiteID string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	if err := s.repo.AssignWebsite(ctx, websiteID, &subscriptionID); err != nil {
		return Subscription{}, err
	}
	s.record(ctx, actor, ActionSubscriptionUpdate, ResourceTypeSubscription, subscriptionID,
		audit.StatusSuccess, map[string]any{"website_assigned": websiteID})
	return s.Subscription(ctx, actor, subscriptionID)
}

// ReleaseWebsite takes a website out of its subscription.
func (s *Service) ReleaseWebsite(ctx context.Context, actor Actor, subscriptionID, websiteID string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	owner, err := s.repo.SubscriptionForWebsite(ctx, websiteID)
	if err != nil || owner != subscriptionID {
		return Subscription{}, ErrNotFound
	}
	if err := s.repo.AssignWebsite(ctx, websiteID, nil); err != nil {
		return Subscription{}, err
	}
	s.record(ctx, actor, ActionSubscriptionUpdate, ResourceTypeSubscription, subscriptionID,
		audit.StatusSuccess, map[string]any{"website_released": websiteID})
	return s.Subscription(ctx, actor, subscriptionID)
}

// AssignNewWebsite records which subscription a freshly created website is in.
//
// Called by the websites service after a create, so a customer's site is owned
// by the subscription whose quota was just counted against. Without this, the
// count that authorised the creation would never rise and a limit of one
// website would let somebody create as many as they liked.
//
// The named subscription wins over the creator's own, because that is how a
// reseller creates a site *for* a customer — and it is the same value the
// quota guard read to decide whose plan was being spent, so the site lands in
// the subscription that paid for it.
func (s *Service) AssignNewWebsite(ctx context.Context, ownerUserID, subscriptionID,
	websiteID string,
) error {
	if subscriptionID != "" {
		if _, err := s.repo.GetSubscription(ctx, subscriptionID); err != nil {
			return err
		}
		return s.repo.AssignWebsite(ctx, websiteID, &subscriptionID)
	}

	ids, err := s.repo.SubscriptionsForOwner(ctx, ownerUserID)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		// The server's own administrator owns the machine rather than a slice
		// of it. Their websites belong to no subscription, which is what the
		// nullable column is for.
		return nil
	}
	return s.repo.AssignWebsite(ctx, websiteID, &ids[0])
}

// ---------------------------------------------------------------- isolation

// applyIsolation sends a subscription's caps to the host and records what the
// host said it could do about them.
//
// A failure here does not fail the operation that triggered it: a subscription
// that exists with its limits unenforced is a state the panel can show and an
// operator can retry, while a subscription that failed to be created because
// a slice could not be written is a customer who cannot be signed up.
func (s *Service) applyIsolation(ctx context.Context, actor Actor, subscriptionID string) {
	if s.agent == nil {
		return
	}
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		s.log.Warn("could not read a subscription to apply its limits",
			"subscription_id", subscriptionID, logger.KeyError, err.Error())
		return
	}
	plan, err := s.repo.GetPlan(ctx, subscription.PlanID)
	if err != nil {
		s.log.Warn("could not read a plan to apply its limits",
			"subscription_id", subscriptionID, logger.KeyError, err.Error())
		return
	}

	slice := subscription.SliceName
	if slice == "" {
		slice = SliceNameFor(subscriptionID)
	}
	result, err := s.agent.TenantIsolationApply(ctx, httpx.RequestIDFromContext(ctx), slice,
		subscription.Name, plan.Isolation.CPUPercent, plan.Isolation.MemoryMB,
		plan.Isolation.IOWeight)
	if err != nil {
		if recErr := s.repo.SetIsolationState(ctx, subscriptionID, IsolationFailed,
			err.Error()); recErr != nil {
			s.log.Error("could not record a failed isolation apply",
				logger.KeyError, recErr.Error())
		}
		s.log.Warn("could not apply a subscription's resource limits",
			"subscription_id", subscriptionID, logger.KeyError, err.Error())
		return
	}

	if err := s.repo.SetIsolationState(ctx, subscriptionID, result.State, result.Detail); err != nil {
		s.log.Error("could not record the isolation state", logger.KeyError, err.Error())
	}
	s.record(ctx, actor, ActionIsolationApply, ResourceTypeSubscription, subscriptionID,
		audit.StatusSuccess, map[string]any{"state": result.State, "slice": result.Slice})
}

// ApplyIsolation is the operator-triggered version, which reports its result.
func (s *Service) ApplyIsolation(ctx context.Context, actor Actor, subscriptionID string) (Subscription, error) {
	subscription, err := s.repo.GetSubscription(ctx, subscriptionID)
	if err != nil {
		return Subscription{}, err
	}
	if err := s.requireDescendant(ctx, actor, subscription.OwnerUserID); err != nil {
		return Subscription{}, err
	}
	s.applyIsolation(ctx, actor, subscriptionID)
	return s.Subscription(ctx, actor, subscriptionID)
}

// reapplyIsolationForPlan pushes a plan's changed caps to every subscription
// on it.
func (s *Service) reapplyIsolationForPlan(ctx context.Context, actor Actor, planID string) {
	if s.agent == nil {
		return
	}
	subscriptions, err := s.repo.ListSubscriptions(ctx, actor.UserID, true)
	if err != nil {
		s.log.Warn("could not re-apply limits after a plan change", logger.KeyError, err.Error())
		return
	}
	for _, subscription := range subscriptions {
		if subscription.PlanID == planID {
			s.applyIsolation(ctx, actor, subscription.ID)
		}
	}
}

// HostStatus reports what this host can enforce.
func (s *Service) HostStatus(ctx context.Context) (agentclient.TenantStatus, error) {
	if s.agent == nil {
		return agentclient.TenantStatus{
			IsolationDetail: "the Agent is not reachable, so nothing can be enforced",
		}, nil
	}
	return s.agent.TenantStatusOf(ctx, httpx.RequestIDFromContext(ctx))
}

// ----------------------------------------------------------------- audit

// record writes an audit event.
//
// The impersonator is carried in the metadata rather than replacing the user
// id, because both facts matter: the session acted as the customer, and
// somebody else opened it. A trail that recorded only one of those cannot
// answer "who actually did this".
func (s *Service) record(ctx context.Context, actor Actor, action, resourceType,
	resourceID, status string, metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	if actor.ImpersonatorUserID != "" {
		if metadata == nil {
			metadata = map[string]any{}
		}
		metadata["impersonated_by"] = actor.ImpersonatorUserID
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       status,
		Metadata:     metadata,
	})
}

// recordRefusal notes a request the quota guard turned away.
//
// Audited because a refusal is the panel telling a paying customer no, and the
// first question afterwards is always "when, and what were they at". Without a
// row, the only evidence is a 409 in an access log with no numbers in it.
func (s *Service) recordRefusal(ctx context.Context, userID, subscriptionID string,
	decision Decision, r *http.Request,
) {
	if s.audit == nil {
		return
	}
	limit := any(nil)
	if decision.Limit != nil {
		limit = *decision.Limit
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       userID,
		Action:       ActionQuotaRefused,
		ResourceType: ResourceTypeSubscription,
		ResourceID:   subscriptionID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		Status:       audit.StatusFailure,
		Metadata: map[string]any{
			"dimension": decision.Dimension,
			"used":      decision.Used,
			"limit":     limit,
			"path":      r.URL.Path,
		},
	})
}

// ActorFor builds an Actor from an authenticated request.
//
// The tier comes from the database rather than from the token, and that is
// deliberate: a token is minted at login and lives for its TTL, so a tier read
// from one would be a tier as it was when somebody signed in. Demoting a
// reseller must take effect now, not in fifteen minutes.
func (s *Service) ActorFor(ctx context.Context, userID, username string,
	permissions []string, r *http.Request,
) (Actor, error) {
	tier, err := s.repo.TierOf(ctx, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Actor{}, ErrForbidden
		}
		return Actor{}, err
	}
	return Actor{
		UserID:      userID,
		Username:    username,
		Tier:        tier,
		Permissions: permissions,
		IPAddress:   clientIP(r),
		UserAgent:   r.UserAgent(),
	}, nil
}

// clientIP is the caller's address without its port, for the audit trail.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
