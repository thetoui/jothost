package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// requestTimeout bounds a request.
//
// A firewall change reloads ufw, which rebuilds the whole ruleset; on a host
// with many rules that takes seconds rather than milliseconds.
const requestTimeout = 60 * time.Second

// maxBodyBytes bounds a request body. A rule is a handful of short strings.
const maxBodyBytes = 8 << 10

// Handler serves the firewall endpoints.
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
// Reading needs server.view; every change needs server.manage. A firewall
// governs the whole machine, so authority over one website is not enough.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/firewall", guarded(rbac.PermServerView, h.status))
	mux.Handle("POST /api/v1/firewall/rules", guarded(rbac.PermServerManage, h.addRule))
	mux.Handle("DELETE /api/v1/firewall/rules", guarded(rbac.PermServerManage, h.deleteRule))
	mux.Handle("POST /api/v1/firewall/enable", guarded(rbac.PermServerManage, h.enable))
	mux.Handle("POST /api/v1/firewall/disable", guarded(rbac.PermServerManage, h.disable))
	mux.Handle("PUT /api/v1/firewall/default", guarded(rbac.PermServerManage, h.setDefault))

	// The two halves of the safety protocol. Confirming is an ordinary request
	// that has to cross the network the change governs — which is what makes
	// it proof that the change did not cut the host off.
	mux.Handle("POST /api/v1/firewall/changes/{id}/confirm",
		guarded(rbac.PermServerManage, h.confirm))
	mux.Handle("POST /api/v1/firewall/changes/{id}/rollback",
		guarded(rbac.PermServerManage, h.rollback))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.service.Status(ctx, httpx.RequestIDFromContext(r.Context()))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			// A host with no firewall is a configuration, not a fault.
			httpx.OK(w, r, agentclient.FirewallStatus{
				Rules:  []agentclient.FirewallRule{},
				Reason: agentclient.Message(err),
			})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	if status.Rules == nil {
		status.Rules = []agentclient.FirewallRule{}
	}
	httpx.OK(w, r, status)
}

// ruleBody is a rule and how long to hold it provisionally.
type ruleBody struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
	Protocol  string `json:"protocol"`
	Port      string `json:"port"`
	Source    string `json:"source"`
	Comment   string `json:"comment"`
	// WindowSeconds is how long before the change is undone unless confirmed.
	// Zero takes the Agent's default.
	WindowSeconds int `json:"window_seconds"`
}

func (b ruleBody) rule() agentclient.FirewallRule {
	direction := b.Direction
	if direction == "" {
		direction = validate.FirewallIn
	}
	protocol := b.Protocol
	if protocol == "" {
		protocol = validate.ProtocolAny
	}
	source := b.Source
	if source == "" {
		source = validate.SourceAny
	}

	return agentclient.FirewallRule{
		Action:    b.Action,
		Direction: direction,
		Protocol:  protocol,
		Port:      b.Port,
		Source:    source,
		Comment:   b.Comment,
	}
}

func (h *Handler) addRule(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, func(body ruleBody) agentclient.FirewallChange {
		return agentclient.FirewallChange{
			Kind:          "rule.add",
			Rule:          body.rule(),
			WindowSeconds: body.WindowSeconds,
		}
	})
}

func (h *Handler) deleteRule(w http.ResponseWriter, r *http.Request) {
	// A body on a DELETE rather than an id in the path: a rule is identified by
	// what it does, and ufw's own numbering renumbers on every change — so a
	// path segment naming position 3 would mean something different by the time
	// it arrived.
	h.change(w, r, func(body ruleBody) agentclient.FirewallChange {
		return agentclient.FirewallChange{
			Kind:          "rule.delete",
			Rule:          body.rule(),
			WindowSeconds: body.WindowSeconds,
		}
	})
}

func (h *Handler) enable(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, func(body ruleBody) agentclient.FirewallChange {
		return agentclient.FirewallChange{Kind: "enable", WindowSeconds: body.WindowSeconds}
	})
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	h.change(w, r, func(body ruleBody) agentclient.FirewallChange {
		return agentclient.FirewallChange{Kind: "disable", WindowSeconds: body.WindowSeconds}
	})
}

// defaultBody changes a default policy.
type defaultBody struct {
	Direction     string `json:"direction"`
	Policy        string `json:"policy"`
	WindowSeconds int    `json:"window_seconds"`
}

func (h *Handler) setDefault(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body defaultBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	direction := body.Direction
	if direction == "" {
		direction = validate.FirewallIn
	}

	pending, err := h.service.Change(ctx, httpx.RequestIDFromContext(r.Context()),
		agentclient.FirewallChange{
			Kind:          "default",
			Policy:        body.Policy,
			Direction:     direction,
			WindowSeconds: body.WindowSeconds,
		}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	respondPending(w, r, pending)
}

// change is the shared path for everything that alters the firewall.
func (h *Handler) change(w http.ResponseWriter, r *http.Request,
	build func(ruleBody) agentclient.FirewallChange,
) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body ruleBody
	if r.ContentLength != 0 {
		if err := decode(w, r, &body); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}

	pending, err := h.service.Change(ctx, httpx.RequestIDFromContext(r.Context()),
		build(body), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	respondPending(w, r, pending)
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	confirmed, err := h.service.Confirm(ctx, httpx.RequestIDFromContext(r.Context()),
		r.PathValue("id"), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, confirmed)
}

func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	rolled, err := h.service.Rollback(ctx, httpx.RequestIDFromContext(r.Context()),
		r.PathValue("id"), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, rolled)
}

// respondPending answers a change with 202 and its deadline.
//
// 202 rather than 200: the change is live but provisional, and the client's
// next job is to confirm it. A 200 would say "done", which is the one thing
// this is not until something has come back through the network.
func respondPending(w http.ResponseWriter, r *http.Request, pending agentclient.FirewallPending) {
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    pending,
	})
}

// translate maps a failure to its HTTP shape.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("This host has no firewall the panel can manage")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidRequest(err):
		// The Agent's own words, which name the port and say what to do.
		return httpx.Conflict(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return httpx.Internal(err)
	}
}

// decode reads a JSON body, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return httpx.BadRequest("The request body is not valid JSON: " + err.Error())
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
