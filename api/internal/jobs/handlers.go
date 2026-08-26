package jobs

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds a job request. These endpoints only read the job
// table; the work itself runs in the worker.
const requestTimeout = 10 * time.Second

// Handler serves the job endpoints.
type Handler struct {
	repo *Repository
	auth *auth.Service
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	Repository *Repository
	Auth       *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{repo: opts.Repository, auth: opts.Auth}
}

// Routes registers the endpoints on mux.
//
// Job payloads describe infrastructure changes — domains, paths, account names
// — so reading them requires the same right as viewing the server itself.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/jobs", guarded(rbac.PermServerView, h.list))
	mux.Handle("GET /api/v1/jobs/{id}", guarded(rbac.PermServerView, h.get))
	mux.Handle("POST /api/v1/jobs/{id}/cancel", guarded(rbac.PermWebsiteUpdate, h.cancel))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	query := r.URL.Query()
	params := ListParams{
		Status:       State(strings.ToUpper(strings.TrimSpace(query.Get("status")))),
		ResourceType: strings.TrimSpace(query.Get("resource_type")),
		ResourceID:   strings.TrimSpace(query.Get("resource_id")),
	}

	if params.Status != "" && !validState(params.Status) {
		httpx.Error(w, r, httpx.BadRequest("status is not a valid job status"))
		return
	}
	if params.ResourceID != "" && !isUUID(params.ResourceID) {
		httpx.Error(w, r, httpx.BadRequest("resource_id must be a UUID"))
		return
	}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			httpx.Error(w, r, httpx.BadRequest("limit must be a positive integer"))
			return
		}
		params.Limit = limit
	}

	found, err := h.repo.List(ctx, params)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, map[string]any{"jobs": found, "count": len(found)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	job, err := h.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httpx.Error(w, r, httpx.NotFound("Job not found"))
			return
		}
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, job)
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	if err := h.repo.Cancel(ctx, id); err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			httpx.Error(w, r, httpx.NotFound("Job not found"))
		case errors.Is(err, ErrTerminal):
			// A job already handed to the Agent is changing the host right now.
			// Reporting it cancelled would be a lie the panel could not undo.
			httpx.Error(w, r, httpx.Conflict(
				"This job has already started and can no longer be cancelled"))
		default:
			httpx.Error(w, r, httpx.Internal(err))
		}
		return
	}

	job, err := h.repo.Get(ctx, id)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, job)
}

func validState(state State) bool {
	switch state {
	case StatePending, StateRunning, StateSuccess, StateFailed, StateCancelled:
		return true
	default:
		return false
	}
}

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
