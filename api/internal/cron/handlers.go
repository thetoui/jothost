package cron

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

// Timeouts. Writing a crontab is a file write on the host and should be quick.
// A manual run is somebody's database dump, and takes as long as it takes —
// bounded by the Agent's own limit on the shell it starts.
const (
	requestTimeout = 30 * time.Second
	runTimeout     = 11 * time.Minute
)

// maxBodyBytes bounds a request body. A job is a handful of short strings.
const maxBodyBytes = 8 << 10

// Handler serves the scheduled job endpoints.
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
// Everything needs cron.manage, reads included. There is one cron permission in
// the panel's model, and the reason reading is behind it too is that a job's
// command line is a description of how a site works — where its scripts are,
// what it talks to, sometimes a token in a URL — which is not something to hand
// to every account that may look at a dashboard.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/cron", guarded(rbac.PermCronManage, h.list))
	mux.Handle("POST /api/v1/cron", guarded(rbac.PermCronManage, h.create))
	mux.Handle("GET /api/v1/cron/{id}", guarded(rbac.PermCronManage, h.get))
	mux.Handle("PATCH /api/v1/cron/{id}", guarded(rbac.PermCronManage, h.update))
	mux.Handle("DELETE /api/v1/cron/{id}", guarded(rbac.PermCronManage, h.delete))
	mux.Handle("POST /api/v1/cron/{id}/run", guarded(rbac.PermCronManage, h.run))
	mux.Handle("GET /api/v1/cron/{id}/logs", guarded(rbac.PermCronManage, h.logs))
}

// jobRequest is the create and update body.
type jobRequest struct {
	WebsiteID string `json:"website_id"`
	Name      string `json:"name"`
	Type      string `json:"job_type"`
	Schedule  string `json:"schedule"`
	Target    string `json:"target"`
	Enabled   *bool  `json:"enabled"`
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var (
		jobs []Job
		err  error
	)
	if website := r.URL.Query().Get("website_id"); website != "" {
		jobs, err = h.service.ListForWebsite(ctx, website)
	} else {
		jobs, err = h.service.List(ctx)
	}
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	if jobs == nil {
		jobs = []Job{}
	}
	httpx.OK(w, r, map[string]any{
		"jobs":  jobs,
		"count": len(jobs),
		// The vocabulary the panel offers, so the form does not hard-code a
		// list this API might disagree with.
		"types": validate.JobTypes(),
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	job, err := h.service.Get(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, job)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	body, err := decode(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	job, err := h.service.Create(ctx, httpx.RequestIDFromContext(ctx), Input{
		WebsiteID: body.WebsiteID,
		Name:      body.Name,
		Type:      body.Type,
		Schedule:  body.Schedule,
		Target:    body.Target,
		Enabled:   body.Enabled,
	}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, job)
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	body, err := decode(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	job, err := h.service.Update(ctx, httpx.RequestIDFromContext(ctx), r.PathValue("id"), Input{
		Name:     body.Name,
		Schedule: body.Schedule,
		Target:   body.Target,
		Enabled:  body.Enabled,
	}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, job)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.service.Delete(ctx, httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), actorFrom(r)); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) run(w http.ResponseWriter, r *http.Request) {
	// A manual run holds the request open until the job finishes, which is the
	// point: an operator pressing "run now" is asking what happens, and a
	// response that said "started" would answer a different question.
	ctx, cancel := context.WithTimeout(r.Context(), runTimeout)
	defer cancel()

	result, err := h.service.Run(ctx, httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

// logs points at the log viewer rather than reading the file itself.
//
// The output of every run, scheduled and manual, is collected in one file per
// job, and Phase 11 already serves those with search, filtering and download.
// A second reader here would be a second set of bounds and a second set of
// bugs, so this returns the key to ask for instead.
func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	job, err := h.service.Get(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, map[string]any{
		"job_id": job.ID,
		"source": "cron." + job.ID,
		"url":    "/api/v1/logs/cron." + job.ID,
	})
}

func decode(r *http.Request) (jobRequest, error) {
	var body jobRequest
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	// An unknown field is a caller believing something about this API that is
	// not true, and silently ignoring it is how that belief survives.
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&body); err != nil {
		return jobRequest{}, httpx.BadRequest("The request body could not be read: " + err.Error())
	}
	return body, nil
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
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("Scheduled job not found")
	case errors.Is(err, ErrDuplicateName):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrWebsiteRequired), errors.Is(err, ErrNoAccount),
		errors.Is(err, ErrNoPHP), errors.Is(err, ErrInvalidTarget),
		errors.Is(err, validate.ErrInvalidSchedule),
		errors.Is(err, validate.ErrInvalidCommand),
		errors.Is(err, validate.ErrInvalidJobName),
		errors.Is(err, validate.ErrInvalidJobType):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("This host has no cron daemon, so nothing can be scheduled on it")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	case agentclient.IsInvalidRequest(err):
		return httpx.Conflict(agentclient.Message(err))
	default:
		if strings.Contains(err.Error(), "website not found") {
			return httpx.NotFound("Website not found")
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
