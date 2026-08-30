package services

import (
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds a request.
//
// Generous because a service being restarted is a real daemon starting: a
// database with a large buffer pool takes seconds, and a timeout shorter than
// that would report a failure the host is in the middle of completing.
const requestTimeout = 60 * time.Second

// Handler serves the service endpoints.
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
// Reading needs server.view; every verb needs server.manage. Restarting the
// database is not authority over one website — it is authority over the host,
// and everything on it.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/services", guarded(rbac.PermServerView, h.list))
	// One route per verb rather than a verb in the body: each is a distinct
	// action with its own audit record, and a path that says what it does is
	// one a reverse proxy or an audit log can be read against.
	mux.Handle("POST /api/v1/services/{key}/start",
		guarded(rbac.PermServerManage, h.act(VerbStart)))
	mux.Handle("POST /api/v1/services/{key}/stop",
		guarded(rbac.PermServerManage, h.act(VerbStop)))
	mux.Handle("POST /api/v1/services/{key}/restart",
		guarded(rbac.PermServerManage, h.act(VerbRestart)))
	mux.Handle("POST /api/v1/services/{key}/enable",
		guarded(rbac.PermServerManage, h.act(VerbEnable)))
	mux.Handle("POST /api/v1/services/{key}/disable",
		guarded(rbac.PermServerManage, h.act(VerbDisable)))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	result, err := h.service.List(ctx, httpx.RequestIDFromContext(r.Context()))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			// A host with no service manager is a configuration, not a fault.
			// The panel says so and shows an empty list rather than an error.
			httpx.OK(w, r, agentclient.ServiceDetectResult{
				Services:     []agentclient.DetectedService{},
				Controllable: false,
			})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	if result.Services == nil {
		result.Services = []agentclient.DetectedService{}
	}
	httpx.OK(w, r, result)
}

// act returns the handler for one verb.
func (h *Handler) act(verb string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()

		key := r.PathValue("key")
		if key == "" {
			httpx.Error(w, r, httpx.BadRequest("a service is required"))
			return
		}

		claims, _ := auth.ClaimsFromContext(r.Context())
		result, err := h.service.Act(ctx, httpx.RequestIDFromContext(r.Context()),
			key, verb, Actor{
				UserID:    claims.UserID,
				IPAddress: clientIP(r),
				UserAgent: r.UserAgent(),
			})
		if err != nil {
			httpx.Error(w, r, translate(err))
			return
		}

		httpx.OK(w, r, result)
	}
}

// translate maps a failure to its HTTP shape.
//
// The Agent's own message is passed through for the cases a user can act on —
// "SSH cannot be stopped from the panel", "no service manager on this host" —
// because a message written for a person is worth more than a code.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrUnknownAction):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("This host has no service manager, so services cannot be controlled here")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidRequest(err):
		return httpx.Conflict(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
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
