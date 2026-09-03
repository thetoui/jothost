package updates

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading is a database query; checking refreshes a package index
// over the network; applying downloads and installs.
const (
	readTimeout  = 30 * time.Second
	checkTimeout = 10 * time.Minute
	applyTimeout = 30 * time.Minute
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 32 << 10

// Handler serves the update endpoints.
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
// Reading needs server.view: knowing a host is behind is not itself a
// privilege, and hiding it from the people who look after the sites on it would
// make the panel worse at the one job this phase has.
//
// Everything that changes the host needs update.manage, which migration 0015
// adds. It is not server.manage, because applying an update restarts daemons
// and can change the version of PHP a customer's site runs on — a different
// kind of decision from restarting a service somebody already chose to run.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/updates", guarded(rbac.PermServerView, h.overview))
	mux.Handle("POST /api/v1/updates/check", guarded(rbac.PermUpdateManage, h.check))
	mux.Handle("POST /api/v1/updates/apply", guarded(rbac.PermUpdateManage, h.apply))
	mux.Handle("POST /api/v1/updates/revert", guarded(rbac.PermUpdateManage, h.revert))
	mux.Handle("PUT /api/v1/updates/settings", guarded(rbac.PermUpdateManage, h.settings))
	mux.Handle("GET /api/v1/updates/history", guarded(rbac.PermServerView, h.history))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, overview)
}

// check refreshes the package index and reads what is outstanding.
//
// It is a POST because it is not free: it reaches the network and refreshes the
// host's package index. A GET that did that would be re-run by every retry,
// every prefetch and every refresh of the page.
func (h *Handler) check(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	result, err := h.service.CheckNow(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

// applyBody is what to install.
type applyBody struct {
	// Packages names what to apply. Empty applies everything outstanding.
	Packages []string `json:"packages"`
	// SecurityOnly applies the pending security fixes and nothing else.
	SecurityOnly bool `json:"security_only"`
}

func (h *Handler) apply(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()

	var body applyBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	run, err := h.service.Apply(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		ApplyRequest{
			Packages:     trimAll(body.Packages),
			SecurityOnly: body.SecurityOnly,
			Trigger:      TriggerManual,
		})
	if err != nil {
		// A run that started and then failed is answered with the run, not with
		// an error envelope — and that is deliberate.
		//
		// An upgrade that failed halfway still moved packages. The record of
		// which ones is the single most useful thing the caller can be given,
		// and an error body has nowhere to put it. The run carries status
		// "failed" and the package manager's own message, so nothing is hidden;
		// what is avoided is throwing away the part that matters.
		//
		// A request that never became a run — nothing outstanding, one already
		// running, a name that is not a package — is an error, because there is
		// no record to return.
		if run.ID != "" {
			httpx.OK(w, r, run)
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, run)
}

// revertBody names one package to put back.
type revertBody struct {
	Package string `json:"package"`
	Version string `json:"version"`
}

func (h *Handler) revert(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), applyTimeout)
	defer cancel()

	var body revertBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	run, err := h.service.Revert(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		strings.TrimSpace(body.Package), strings.TrimSpace(body.Version))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, run)
}

// settingsBody is the automatic-update settings.
type settingsBody struct {
	Policy             string   `json:"policy"`
	CheckIntervalHours int      `json:"check_interval_hours"`
	DayOfWeek          *int     `json:"day_of_week"`
	Hour               *int     `json:"hour"`
	Minute             *int     `json:"minute"`
	Excluded           []string `json:"excluded"`
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var body settingsBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	// The window's fields are pointers so that "every day" — which is -1, not
	// zero — survives a body that omits them. Zero is Sunday.
	settings := Settings{
		Policy:             strings.TrimSpace(body.Policy),
		CheckIntervalHours: body.CheckIntervalHours,
		DayOfWeek:          validate.EveryDay,
		Hour:               3,
		Excluded:           trimAll(body.Excluded),
	}
	if body.DayOfWeek != nil {
		settings.DayOfWeek = *body.DayOfWeek
	}
	if body.Hour != nil {
		settings.Hour = *body.Hour
	}
	if body.Minute != nil {
		settings.Minute = *body.Minute
	}

	saved, err := h.service.Configure(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx), settings)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, saved)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	runs, err := h.service.History(ctx, limit)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"runs": runs, "count": len(runs)})
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

// trimAll trims each entry and drops the empty ones.
func trimAll(values []string) []string {
	if values == nil {
		return nil
	}
	kept := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return kept
}

// translate maps a failure to its HTTP shape.
//
// Each of these is a different next step: "nothing to do", "one is already
// running", "this host cannot tell security updates apart" and "that version is
// gone" are four different things for an operator to know.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNoCheck), errors.Is(err, ErrRunNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrAlreadyRunning):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(ErrUnavailable.Error())
	case errors.Is(err, ErrNothingToDo), errors.Is(err, ErrSecurityUnknown),
		errors.Is(err, ErrExcluded):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidPackage),
		errors.Is(err, validate.ErrInvalidPackageVersion),
		errors.Is(err, validate.ErrInvalidUpdatePolicy),
		errors.Is(err, validate.ErrInvalidUpdateWindow):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
