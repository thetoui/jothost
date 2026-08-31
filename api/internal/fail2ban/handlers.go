package fail2ban

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading is a handful of calls to a local daemon; installing pulls a
// package and its dependencies.
const (
	requestTimeout = 60 * time.Second
	installTimeout = 10 * time.Minute
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the intrusion-prevention endpoints.
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
// Reading needs server.view; every change needs firewall.manage. A ban is a
// firewall rule, and the permission that governs the firewall is the one that
// should govern the thing writing rules into it — an account that may not open
// a port should not be able to close one for everybody either.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/security/fail2ban", guarded(rbac.PermServerView, h.status))
	mux.Handle("POST /api/v1/security/fail2ban/install",
		guarded(rbac.PermFirewallManage, h.install))
	mux.Handle("GET /api/v1/security/fail2ban/jails", guarded(rbac.PermServerView, h.jails))
	mux.Handle("PATCH /api/v1/security/fail2ban/jails/{jail}",
		guarded(rbac.PermFirewallManage, h.configure))
	mux.Handle("PUT /api/v1/security/fail2ban/ignored",
		guarded(rbac.PermFirewallManage, h.setIgnored))
	mux.Handle("GET /api/v1/security/fail2ban/banned", guarded(rbac.PermServerView, h.banned))
	mux.Handle("POST /api/v1/security/fail2ban/ban", guarded(rbac.PermFirewallManage, h.ban))
	mux.Handle("POST /api/v1/security/fail2ban/unban", guarded(rbac.PermFirewallManage, h.unban))

	// API_SPEC section 27 spells the daemon's own switch as enable and disable.
	// They turn the *service* on and off, which is the service manager's job —
	// so they are aliases that say so rather than a second way to do it.
	mux.Handle("POST /api/v1/security/fail2ban/enable",
		guarded(rbac.PermFirewallManage, h.serviceRedirect))
	mux.Handle("POST /api/v1/security/fail2ban/disable",
		guarded(rbac.PermFirewallManage, h.serviceRedirect))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.service.Status(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, emptyStatus())
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, normalise(status))
}

// jails is the same information, filtered to the jails.
//
// A separate route because API_SPEC section 27 has one, and because a page that
// only wants the table should not have to ask for the whole status.
func (h *Handler) jails(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.service.Status(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, map[string]any{"jails": []agentclient.Fail2BanJail{}, "count": 0})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	jails := status.Jails
	if jails == nil {
		jails = []agentclient.Fail2BanJail{}
	}
	httpx.OK(w, r, map[string]any{"jails": jails, "count": len(jails)})
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	status, err := h.service.Install(ctx, httpx.RequestIDFromContext(ctx), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, normalise(status))
}

// configureBody is the per-jail PATCH body. An omitted field is left alone.
type configureBody struct {
	Enabled  *bool `json:"enabled"`
	MaxRetry *int  `json:"max_retry"`
	FindTime *int  `json:"find_time"`
	BanTime  *int  `json:"ban_time"`
}

func (h *Handler) configure(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	jail := r.PathValue("jail")
	if err := validate.JailName(jail); err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}

	var body configureBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	result, err := h.service.Configure(ctx, httpx.RequestIDFromContext(ctx),
		agentclient.Fail2BanChange{
			Jail:     jail,
			Enabled:  body.Enabled,
			MaxRetry: body.MaxRetry,
			FindTime: body.FindTime,
			BanTime:  body.BanTime,
		}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

// ignoredBody is the ignore-list body.
type ignoredBody struct {
	Ignored []string `json:"ignored"`
}

func (h *Handler) setIgnored(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body ignoredBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	// Checked here as well as in the Agent, so a typo comes back as a message
	// about that address rather than as a round trip and a generic refusal.
	for _, address := range body.Ignored {
		if err := validate.BanIP(address); err != nil {
			httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
			return
		}
	}

	ignored, err := h.service.SetIgnored(ctx, httpx.RequestIDFromContext(ctx),
		body.Ignored, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"ignored": ignored, "count": len(ignored)})
}

func (h *Handler) banned(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	list, err := h.service.Banned(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, map[string]any{"banned": []agentclient.Fail2BanBanned{}, "count": 0})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	if list.Banned == nil {
		list.Banned = []agentclient.Fail2BanBanned{}
	}
	httpx.OK(w, r, list)
}

// banBody names an address and the jail to act in.
type banBody struct {
	Jail    string `json:"jail"`
	Address string `json:"address"`
}

func (h *Handler) ban(w http.ResponseWriter, r *http.Request)   { h.banOrUnban(w, r, true) }
func (h *Handler) unban(w http.ResponseWriter, r *http.Request) { h.banOrUnban(w, r, false) }

func (h *Handler) banOrUnban(w http.ResponseWriter, r *http.Request, ban bool) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body banBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := validate.JailName(body.Jail); err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}
	if err := validate.BanIP(body.Address); err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}

	requestID := httpx.RequestIDFromContext(ctx)
	var err error
	if ban {
		err = h.service.Ban(ctx, requestID, body.Jail, body.Address, actorFrom(r))
	} else {
		err = h.service.Unban(ctx, requestID, body.Jail, body.Address, actorFrom(r))
	}
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, map[string]any{
		"jail": body.Jail, "address": body.Address, "banned": ban,
	})
}

// serviceRedirect answers the enable and disable routes from the specification.
//
// They are not implemented as a second lifecycle: starting and stopping a daemon
// is the service manager's job, and two places that start the same thing is how
// a panel ends up disagreeing with itself about whether it is running.
func (h *Handler) serviceRedirect(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, httpx.Conflict(
		"fail2ban is started and stopped from the Services page, where every daemon "+
			"on this host is. Use POST /api/v1/services/fail2ban/start or /stop."))
}

// emptyStatus is what a host with no fail2ban looks like.
func emptyStatus() agentclient.Fail2BanStatus {
	return agentclient.Fail2BanStatus{
		Jails:   []agentclient.Fail2BanJail{},
		Ignored: []string{},
		Reason:  "fail2ban is not installed on this host",
	}
}

// normalise fills in the empty slices JSON would otherwise render as null.
func normalise(status agentclient.Fail2BanStatus) agentclient.Fail2BanStatus {
	if status.Jails == nil {
		status.Jails = []agentclient.Fail2BanJail{}
	}
	if status.Ignored == nil {
		status.Ignored = []string{}
	}
	for i := range status.Jails {
		if status.Jails[i].Banned == nil {
			status.Jails[i].Banned = []string{}
		}
		if status.Jails[i].LogPaths == nil {
			status.Jails[i].LogPaths = []string{}
		}
	}
	return status
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

// translate maps a failure to its HTTP shape.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNothingToDo):
		return httpx.ValidationFailed("No setting was given to change")
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("fail2ban is not installed on this host")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	case agentclient.IsInvalidRequest(err):
		return httpx.Conflict(agentclient.Message(err))
	default:
		if strings.Contains(err.Error(), "not running") {
			return httpx.Conflict(agentclient.Message(err))
		}
		return httpx.Internal(err)
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
