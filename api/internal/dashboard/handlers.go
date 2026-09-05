package dashboard

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/metrics"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/servers"
)

// snapshotTimeout bounds a dashboard request.
//
// It is shorter than the Agent's own operation timeout: a dashboard that hangs
// is worse than one reporting a panel as unavailable, because an operator
// opens it precisely when the host is misbehaving.
const snapshotTimeout = 10 * time.Second

// Handler serves the dashboard, server, and metric endpoints.
type Handler struct {
	service *Service
	servers *servers.Repository
	metrics *metrics.Repository
	auth    *auth.Service
	// defaultServerID is the local host, used when a caller asks for the
	// dashboard without naming a server.
	defaultServerID string
	now             func() time.Time
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	Service         *Service
	Servers         *servers.Repository
	Metrics         *metrics.Repository
	Auth            *auth.Service
	DefaultServerID string
	Now             func() time.Time
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Handler{
		service:         opts.Service,
		servers:         opts.Servers,
		metrics:         opts.Metrics,
		auth:            opts.Auth,
		defaultServerID: opts.DefaultServerID,
		now:             opts.Now,
	}
}

// Routes registers the endpoints on mux.
//
// Everything here is behind authentication and server.view: host metrics
// reveal what is running and how loaded it is, which is not public.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(
			h.auth.RequirePermission(rbac.PermServerView)(next))
	}

	mux.Handle("GET /api/v1/dashboard", guarded(h.dashboard))
	mux.Handle("GET /api/v1/servers", guarded(h.listServers))
	mux.Handle("GET /api/v1/servers/{id}", guarded(h.getServer))
	mux.Handle("GET /api/v1/servers/{id}/metrics", guarded(h.serverMetrics))
	// The host's processes. Same permission as the rest: what is running on a
	// machine says a great deal about it, and this is the one part of the
	// PRD's Server module the panel had collected but never shown.
	mux.Handle("GET /api/v1/server/processes", guarded(h.processes))
}

func (h *Handler) processes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, snapshotTimeout)
	defer cancel()

	query := r.URL.Query()

	limit := 0
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httpx.Error(w, r, httpx.BadRequest("limit must be a positive integer"))
			return
		}
		limit = parsed
	}

	sortBy := strings.ToLower(strings.TrimSpace(query.Get("sort")))
	// Refused rather than quietly corrected: a caller asking for an ordering
	// this host does not have should be told, not handed a different one and
	// left to believe it was honoured.
	if sortBy != "" && !ValidProcessSort(sortBy) {
		httpx.Error(w, r, httpx.BadRequest("sort must be memory or cpu"))
		return
	}

	result, err := h.service.Processes(ctx, httpx.RequestIDFromContext(r.Context()), limit, sortBy)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.OK(w, r, result)
}

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, snapshotTimeout)
	defer cancel()

	serverID := h.defaultServerID
	// An explicit server may be requested; without one the local host is used,
	// which is what a single-server panel always wants.
	if requested := strings.TrimSpace(r.URL.Query().Get("server_id")); requested != "" {
		if !isUUID(requested) {
			httpx.Error(w, r, httpx.BadRequest("server_id must be a UUID"))
			return
		}
		serverID = requested
	}

	if serverID == "" {
		// Registration failed at startup, so there is nothing to report on.
		httpx.Error(w, r, httpx.Unavailable("No server has been registered yet"))
		return
	}

	snapshot, err := h.service.Snapshot(ctx, serverID, httpx.RequestIDFromContext(r.Context()))
	if err != nil {
		if errors.Is(err, servers.ErrNotFound) {
			httpx.Error(w, r, httpx.NotFound("Server not found"))
			return
		}
		httpx.Error(w, r, err)
		return
	}

	httpx.OK(w, r, snapshot)
}

func (h *Handler) listServers(w http.ResponseWriter, r *http.Request) {
	list, err := h.servers.List(r.Context())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.OK(w, r, map[string]any{
		"servers": list,
		"count":   len(list),
	})
}

func (h *Handler) getServer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	server, err := h.servers.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, servers.ErrNotFound) {
			httpx.Error(w, r, httpx.NotFound("Server not found"))
			return
		}
		httpx.Error(w, r, err)
		return
	}

	httpx.OK(w, r, server)
}

func (h *Handler) serverMetrics(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	rng, err := metrics.ParseRange(r.URL.Query().Get("range"))
	if err != nil {
		httpx.Error(w, r, httpx.BadRequest("range must be one of "+rangeList()))
		return
	}

	// The server is resolved first so an unknown id is a 404 rather than an
	// empty series, which would look like a host with no history.
	if _, err := h.servers.Get(r.Context(), id); err != nil {
		if errors.Is(err, servers.ErrNotFound) {
			httpx.Error(w, r, httpx.NotFound("Server not found"))
			return
		}
		httpx.Error(w, r, err)
		return
	}

	series, err := h.metrics.History(r.Context(), id, rng, h.now())
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	httpx.OK(w, r, series)
}

// rangeList renders the supported ranges for an error message.
func rangeList() string {
	values := metrics.Ranges()
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, string(value))
	}
	return strings.Join(parts, ", ")
}

// isUUID reports whether a path or query value is shaped like a UUID.
//
// Validating before the query keeps a malformed id from reaching Postgres as a
// cast error, which would surface as an internal error rather than a 400.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}

	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
