package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Everything here reads or writes a handful of rows; the two that
// reach the Agent — applying a slice and measuring usage — get longer, because
// measuring walks a customer's whole site.
const (
	requestTimeout = 30 * time.Second
	hostTimeout    = 3 * time.Minute
)

// maxBodyBytes bounds an authenticated request body. Nothing here carries free
// text longer than a suspension reason.
const maxBodyBytes = 32 << 10

// Handler serves the tenancy endpoints.
type Handler struct {
	service *Service
	auth    *auth.Service
}

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Service *Service
	Auth    *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{service: opts.Service, auth: opts.Auth}
}

// Routes registers the endpoints.
//
// Reading needs tenant.view; changing needs tenant.manage. Impersonation is
// behind its own permission, because signing in as a customer is a different
// act from provisioning one — and the one endpoint that does not need
// tenant.impersonate is the one that *ends* an impersonation, since a session
// stripped of the permission has to be able to stop being one.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/tenancy/overview", guarded(rbac.PermTenantView, h.overview))

	mux.Handle("GET /api/v1/tenancy/accounts", guarded(rbac.PermTenantView, h.listAccounts))
	mux.Handle("POST /api/v1/tenancy/accounts", guarded(rbac.PermTenantManage, h.createAccount))
	mux.Handle("PATCH /api/v1/tenancy/accounts/{id}", guarded(rbac.PermTenantManage, h.updateAccount))
	mux.Handle("DELETE /api/v1/tenancy/accounts/{id}", guarded(rbac.PermTenantManage, h.deleteAccount))

	mux.Handle("GET /api/v1/tenancy/plans", guarded(rbac.PermTenantView, h.listPlans))
	mux.Handle("POST /api/v1/tenancy/plans", guarded(rbac.PermTenantManage, h.createPlan))
	mux.Handle("GET /api/v1/tenancy/plans/{id}", guarded(rbac.PermTenantView, h.getPlan))
	mux.Handle("PUT /api/v1/tenancy/plans/{id}", guarded(rbac.PermTenantManage, h.updatePlan))
	mux.Handle("DELETE /api/v1/tenancy/plans/{id}", guarded(rbac.PermTenantManage, h.deletePlan))

	mux.Handle("GET /api/v1/tenancy/subscriptions", guarded(rbac.PermTenantView, h.listSubscriptions))
	mux.Handle("POST /api/v1/tenancy/subscriptions", guarded(rbac.PermTenantManage, h.createSubscription))
	mux.Handle("GET /api/v1/tenancy/subscriptions/{id}", guarded(rbac.PermTenantView, h.getSubscription))
	mux.Handle("PATCH /api/v1/tenancy/subscriptions/{id}", guarded(rbac.PermTenantManage, h.updateSubscription))
	mux.Handle("DELETE /api/v1/tenancy/subscriptions/{id}", guarded(rbac.PermTenantManage, h.deleteSubscription))
	mux.Handle("POST /api/v1/tenancy/subscriptions/{id}/status", guarded(rbac.PermTenantManage, h.setStatus))
	mux.Handle("POST /api/v1/tenancy/subscriptions/{id}/addons", guarded(rbac.PermTenantManage, h.addAddon))
	mux.Handle("DELETE /api/v1/tenancy/subscriptions/{id}/addons/{planId}",
		guarded(rbac.PermTenantManage, h.removeAddon))
	mux.Handle("POST /api/v1/tenancy/subscriptions/{id}/websites", guarded(rbac.PermTenantManage, h.assignWebsite))
	mux.Handle("DELETE /api/v1/tenancy/subscriptions/{id}/websites/{websiteId}",
		guarded(rbac.PermTenantManage, h.releaseWebsite))
	mux.Handle("POST /api/v1/tenancy/subscriptions/{id}/measure", guarded(rbac.PermTenantManage, h.measure))
	mux.Handle("POST /api/v1/tenancy/subscriptions/{id}/isolation", guarded(rbac.PermTenantManage, h.applyIsolation))
	mux.Handle("GET /api/v1/tenancy/subscriptions/{id}/quota/{dimension}",
		guarded(rbac.PermTenantView, h.quota))

	mux.Handle("POST /api/v1/tenancy/impersonation",
		guarded(rbac.PermTenantImpersonate, h.startImpersonation))
	mux.Handle("GET /api/v1/tenancy/impersonation",
		h.auth.RequireAuth(http.HandlerFunc(h.currentImpersonation)))
	// No permission: an impersonated session does not carry
	// tenant.impersonate, by design, and it must still be able to end itself.
	mux.Handle("DELETE /api/v1/tenancy/impersonation",
		h.auth.RequireAuth(http.HandlerFunc(h.endImpersonation)))
	mux.Handle("GET /api/v1/tenancy/impersonation/history",
		guarded(rbac.PermTenantView, h.impersonationHistory))
}

// actor resolves who is asking.
func (h *Handler) actor(w http.ResponseWriter, r *http.Request) (Actor, bool) {
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return Actor{}, false
	}
	actor, err := h.service.ActorFor(r.Context(), claims.UserID, claims.Username,
		claims.Permissions, r)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return Actor{}, false
	}
	actor.ImpersonatorUserID = claims.ImpersonatorUserID
	return actor, true
}

// overview is what the page opens with.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}

	accounts, err := h.service.Accounts(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	plans, err := h.service.Plans(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	subscriptions, err := h.service.Subscriptions(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	// A failure to reach the Agent is reported as "not known" rather than as a
	// failed request: the accounts and plans above are real and useful, and a
	// page that showed nothing because one host was unreachable would be worse
	// than one that says it could not ask.
	host, hostErr := h.service.HostStatus(ctx)
	if hostErr != nil {
		host.IsolationDetail = "the host could not be asked what it enforces: " + hostErr.Error()
	}

	httpx.OK(w, r, map[string]any{
		"accounts":      accounts,
		"plans":         plans,
		"subscriptions": subscriptions,
		"host":          host,
		"actor": map[string]any{
			"tier":         actor.Tier,
			"user_id":      actor.UserID,
			"impersonated": actor.ImpersonatorUserID != "",
		},
	})
}

// ---------------------------------------------------------------- accounts

func (h *Handler) listAccounts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	accounts, err := h.service.Accounts(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"accounts": accounts})
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req CreateAccountRequest
	if !decode(w, r, &req) {
		return
	}
	account, err := h.service.CreateAccount(ctx, actor, req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, account)
}

func (h *Handler) updateAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req UpdateAccountParams
	if !decode(w, r, &req) {
		return
	}
	account, err := h.service.UpdateAccount(ctx, actor, r.PathValue("id"), req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, account)
}

func (h *Handler) deleteAccount(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteAccount(ctx, actor, r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// ------------------------------------------------------------------- plans

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	plans, err := h.service.Plans(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"plans": plans})
}

func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	plan, err := h.service.Plan(ctx, actor, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, plan)
}

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req PlanRequest
	if !decode(w, r, &req) {
		return
	}
	plan, err := h.service.CreatePlan(ctx, actor, req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, plan)
}

func (h *Handler) updatePlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req PlanRequest
	if !decode(w, r, &req) {
		return
	}
	plan, err := h.service.UpdatePlan(ctx, actor, r.PathValue("id"), req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, plan)
}

func (h *Handler) deletePlan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.DeletePlan(ctx, actor, r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// ----------------------------------------------------------- subscriptions

func (h *Handler) listSubscriptions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscriptions, err := h.service.Subscriptions(ctx, actor)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"subscriptions": subscriptions})
}

func (h *Handler) getSubscription(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.Subscription(ctx, actor, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) createSubscription(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req CreateSubscriptionRequest
	if !decode(w, r, &req) {
		return
	}
	subscription, err := h.service.CreateSubscription(ctx, actor, req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, subscription)
}

func (h *Handler) updateSubscription(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req UpdateSubscriptionRequest
	if !decode(w, r, &req) {
		return
	}
	subscription, err := h.service.UpdateSubscription(ctx, actor, r.PathValue("id"), req)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) deleteSubscription(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteSubscription(ctx, actor, r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) setStatus(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}
	subscription, err := h.service.SetSubscriptionStatus(ctx, actor, r.PathValue("id"),
		req.Status, req.Reason)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) addAddon(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req struct {
		PlanID   string `json:"plan_id"`
		Quantity int    `json:"quantity"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Quantity == 0 {
		req.Quantity = 1
	}
	subscription, err := h.service.AddAddon(ctx, actor, r.PathValue("id"), req.PlanID, req.Quantity)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) removeAddon(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.RemoveAddon(ctx, actor, r.PathValue("id"), r.PathValue("planId"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) assignWebsite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req struct {
		WebsiteID string `json:"website_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	subscription, err := h.service.AssignWebsite(ctx, actor, r.PathValue("id"), req.WebsiteID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) releaseWebsite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.ReleaseWebsite(ctx, actor, r.PathValue("id"),
		r.PathValue("websiteId"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) measure(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.MeasureOne(ctx, actor, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

func (h *Handler) applyIsolation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.ApplyIsolation(ctx, actor, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, subscription)
}

// quota answers whether one more of something would be allowed.
//
// Read-only and needs only tenant.view: it is what a page uses to grey out a
// "create" button before somebody fills in a form and is refused. The refusal
// that matters is the guard's, on the create request itself.
func (h *Handler) quota(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	subscription, err := h.service.Subscription(ctx, actor, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	dimension := r.PathValue("dimension")
	if err := validate.QuotaDimension(dimension); err != nil {
		httpx.Error(w, r, httpx.BadRequest(err.Error()))
		return
	}
	decision, err := h.service.CheckQuota(ctx, subscription.ID, dimension)
	if err != nil {
		httpx.Error(w, r, httpx.BadRequest(err.Error()))
		return
	}
	httpx.OK(w, r, decision)
}

// ----------------------------------------------------------- impersonation

func (h *Handler) startImpersonation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	var req struct {
		UserID string `json:"user_id"`
		Reason string `json:"reason"`
	}
	if !decode(w, r, &req) {
		return
	}

	result, err := h.service.StartImpersonation(ctx, actor, req.UserID, req.Reason,
		auth.RequestContext{IPAddress: actor.IPAddress, UserAgent: actor.UserAgent}, h.auth)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, map[string]any{
		"tokens":  result.Tokens,
		"subject": req.UserID,
	})
}

func (h *Handler) currentImpersonation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}
	record, err := h.service.CurrentImpersonation(ctx, claims)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"impersonation": record})
}

func (h *Handler) endImpersonation(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		httpx.Error(w, r, httpx.Unauthorized("Authentication required"))
		return
	}
	if err := h.service.EndImpersonation(ctx, claims, h.auth, clientIP(r), r.UserAgent()); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"ended": true})
}

func (h *Handler) impersonationHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	actor, ok := h.actor(w, r)
	if !ok {
		return
	}
	records, err := h.service.ImpersonationHistory(ctx, actor, 50)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"impersonations": records})
}

// ---------------------------------------------------------------- plumbing

// decode reads a JSON body under a size cap.
func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil && !errors.Is(err, io.EOF) {
		httpx.Error(w, r, httpx.BadRequest("The request body is not valid JSON: "+err.Error()))
		return false
	}
	return true
}

// translate maps a failure to its HTTP shape.
//
// ErrNotFound covers both "no such thing" and "not yours", and that is
// deliberate: a reseller walking ids must not be able to tell them apart,
// because the second answer confirms the thing exists.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrForbidden), errors.Is(err, ErrCannotImpersonate),
		errors.Is(err, ErrAlreadyImpersonating):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrNameTaken), errors.Is(err, ErrPlanInUse),
		errors.Is(err, ErrSubscriptionInUse), errors.Is(err, ErrHasChildren),
		errors.Is(err, ErrQuotaExceeded), errors.Is(err, ErrSuspended):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrNotAnAddon), errors.Is(err, ErrNotAPlan),
		errors.Is(err, validate.ErrInvalidTier),
		errors.Is(err, validate.ErrInvalidPlanName),
		errors.Is(err, validate.ErrInvalidLimit),
		errors.Is(err, validate.ErrInvalidEnforcement),
		errors.Is(err, validate.ErrInvalidDimension),
		errors.Is(err, validate.ErrInvalidSlice):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, auth.ErrAccountInactive):
		return httpx.Conflict("that account is not active, so it cannot be signed in to")
	default:
		return httpx.Internal(err)
	}
}
