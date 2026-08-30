package webserver

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
)

// requestTimeout bounds a request that only reads and queues.
const requestTimeout = 30 * time.Second

// installTimeout bounds installing Apache, which downloads packages.
const installTimeout = 10 * time.Minute

// maxBodyBytes bounds a request body. This one carries a single word.
const maxBodyBytes = 4 << 10

// Handler serves the web server endpoints.
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
// Reading needs server.view and every change needs server.manage. The
// arrangement is a property of the whole host: changing it rewrites every
// site's configuration, which is not authority over one website.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/webserver", guarded(rbac.PermServerView, h.status))
	mux.Handle("PUT /api/v1/webserver", guarded(rbac.PermServerManage, h.setMode))
	mux.Handle("POST /api/v1/webserver/apache/install",
		guarded(rbac.PermServerManage, h.installApache))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.service.Status(ctx, httpx.RequestIDFromContext(r.Context()))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, status)
}

// modeBody is the request body for changing the arrangement.
type modeBody struct {
	Mode string `json:"mode"`
}

func (h *Handler) setMode(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body modeBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	result, err := h.service.SetMode(ctx, httpx.RequestIDFromContext(r.Context()),
		body.Mode, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// 202: the record has changed, and every site's configuration is being
	// rewritten. The client follows the jobs to learn when the host agrees.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    result,
	})
}

func (h *Handler) installApache(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	status, err := h.service.InstallApache(ctx, httpx.RequestIDFromContext(r.Context()),
		actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, status)
}

// translate maps a domain error to its HTTP shape.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrApacheUnavailable):
		return httpx.Conflict(
			"Apache is not installed on this host; install it before switching arrangement")
	case errors.Is(err, ErrAlreadySet):
		return httpx.Conflict(err.Error())
	case errors.Is(err, websites.ErrNoBackendPort):
		return httpx.Conflict(err.Error())
	case errors.Is(err, websites.ErrNotFound):
		return httpx.NotFound("Server not found")
	case errors.Is(err, validate.ErrInvalidWebserverMode):
		return httpx.ValidationFailed(err.Error())
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
