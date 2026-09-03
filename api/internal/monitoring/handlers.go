package monitoring

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// requestTimeout bounds a request. Everything here is a database query.
const requestTimeout = 30 * time.Second

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the monitoring endpoints.
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

// Routes registers the endpoints on mux.
//
// Reading needs server.view. Changing a rule or acknowledging an alert needs
// monitor.manage, which migration 0016 adds — and which is deliberately not
// server.manage: tuning a threshold that is crying wolf, or acknowledging a
// disk alert at three in the morning, cannot change what the server does, and
// the person who looks after the sites is exactly who needs to do both.
//
// There is no endpoint that resolves an alert. Whether a condition has cleared
// is a fact about the machine, and the monitor decides it.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/monitoring", guarded(rbac.PermServerView, h.overview))
	mux.Handle("GET /api/v1/monitoring/alerts", guarded(rbac.PermServerView, h.alerts))
	mux.Handle("POST /api/v1/monitoring/alerts/{id}/acknowledge",
		guarded(rbac.PermMonitorManage, h.acknowledge))

	mux.Handle("GET /api/v1/monitoring/rules", guarded(rbac.PermServerView, h.listRules))
	mux.Handle("POST /api/v1/monitoring/rules", guarded(rbac.PermMonitorManage, h.createRule))
	mux.Handle("PATCH /api/v1/monitoring/rules/{id}", guarded(rbac.PermMonitorManage, h.updateRule))
	mux.Handle("DELETE /api/v1/monitoring/rules/{id}", guarded(rbac.PermMonitorManage, h.deleteRule))

	mux.Handle("GET /api/v1/monitoring/services/{service}",
		guarded(rbac.PermServerView, h.serviceHistory))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, overview)
}

func (h *Handler) alerts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	limit := 100
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	alerts, err := h.service.Alerts(ctx, r.URL.Query().Get("status"), limit)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"alerts": alerts, "count": len(alerts)})
}

func (h *Handler) acknowledge(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	alert, err := h.service.Acknowledge(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, alert)
}

func (h *Handler) listRules(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	rules, err := h.service.repo.ListRules(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"rules":   rules,
		"count":   len(rules),
		"metrics": validate.AlertMetrics,
	})
}

// ruleBody is a rule to record or change.
//
// ForSeconds and Enabled are pointers because zero and false are both
// meaningful: a duration of zero fires on the first reading, and disabling a
// rule is exactly what somebody sends false for.
type ruleBody struct {
	Name       string  `json:"name"`
	Metric     string  `json:"metric"`
	Target     string  `json:"target"`
	Comparison string  `json:"comparison"`
	Threshold  float64 `json:"threshold"`
	ForSeconds *int    `json:"for_seconds"`
	Severity   string  `json:"severity"`
	Enabled    *bool   `json:"enabled"`
}

func (b ruleBody) request() RuleRequest {
	return RuleRequest{
		Name:       strings.TrimSpace(b.Name),
		Metric:     strings.TrimSpace(b.Metric),
		Target:     strings.TrimSpace(b.Target),
		Comparison: strings.TrimSpace(b.Comparison),
		Threshold:  b.Threshold,
		ForSeconds: b.ForSeconds,
		Severity:   strings.TrimSpace(b.Severity),
		Enabled:    b.Enabled,
	}
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body ruleBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	// A new rule is on unless somebody says otherwise: a rule created and left
	// disabled is a rule nobody notices is not watching.
	request := body.request()
	if request.Enabled == nil {
		enabled := true
		request.Enabled = &enabled
	}

	rule, err := h.service.CreateRule(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx), request)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, rule)
}

func (h *Handler) updateRule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body ruleBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	rule, err := h.service.UpdateRule(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body.request())
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, rule)
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.service.DeleteRule(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) serviceHistory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	states, err := h.service.ServiceHistory(ctx, r.PathValue("service"), limit)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"states": states, "count": len(states)})
}

func decode(r *http.Request, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(into); err != nil {
		return httpx.BadRequest("The request body could not be read: " + err.Error())
	}
	return nil
}

func actorFrom(r *http.Request) Actor {
	claims, _ := auth.ClaimsFromContext(r.Context())
	return Actor{
		UserID:    claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// translate maps a failure to its HTTP shape.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrRuleNotFound), errors.Is(err, ErrAlertNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrDuplicateRule):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrMetricImmutable):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidMetric),
		errors.Is(err, validate.ErrInvalidComparison),
		errors.Is(err, validate.ErrInvalidThreshold),
		errors.Is(err, validate.ErrInvalidSeverity),
		errors.Is(err, validate.ErrInvalidRuleName),
		errors.Is(err, validate.ErrInvalidAlertDuration):
		return httpx.ValidationFailed(err.Error())
	default:
		return err
	}
}
