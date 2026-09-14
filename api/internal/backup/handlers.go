package backup

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading is a database query; checking a destination and verifying
// an archive both reach storage that may be on the other side of the world.
const (
	readTimeout   = 30 * time.Second
	checkTimeout  = 5 * time.Minute
	verifyTimeout = 60 * time.Minute
)

// maxBodyBytes bounds a request body.
//
// Larger than most in this panel, because an SFTP destination carries a private
// key and a host key: both are text, and a request that could not hold them
// would make the whole destination kind unusable.
const maxBodyBytes = 64 << 10

// Handler serves the backup endpoints.
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
// Everything here needs backup.manage, which migration 0002 seeded and granted
// to admin and operator. Reading is not split out to a lesser permission: a
// backup listing names every website and database on the host, and a manifest
// names every file in them. That is a map of the machine, and it belongs behind
// the same permission as the thing it describes.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermBackupManage)(next))
	}

	mux.Handle("GET /api/v1/backups", guarded(h.overview))
	mux.Handle("POST /api/v1/backups", guarded(h.create))
	mux.Handle("GET /api/v1/backups/{id}", guarded(h.get))
	mux.Handle("DELETE /api/v1/backups/{id}", guarded(h.delete))
	mux.Handle("POST /api/v1/backups/{id}/restore", guarded(h.restore))
	mux.Handle("POST /api/v1/backups/{id}/verify", guarded(h.verify))

	mux.Handle("GET /api/v1/backup-destinations", guarded(h.listDestinations))
	mux.Handle("POST /api/v1/backup-destinations", guarded(h.createDestination))
	mux.Handle("PATCH /api/v1/backup-destinations/{id}", guarded(h.updateDestination))
	mux.Handle("DELETE /api/v1/backup-destinations/{id}", guarded(h.deleteDestination))
	mux.Handle("POST /api/v1/backup-destinations/{id}/check", guarded(h.checkDestination))

	mux.Handle("GET /api/v1/backup-schedules", guarded(h.listSchedules))
	mux.Handle("POST /api/v1/backup-schedules", guarded(h.createSchedule))
	mux.Handle("PATCH /api/v1/backup-schedules/{id}", guarded(h.updateSchedule))
	mux.Handle("DELETE /api/v1/backup-schedules/{id}", guarded(h.deleteSchedule))
	mux.Handle("POST /api/v1/backup-schedules/{id}/run", guarded(h.runSchedule))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx), limitFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, overview)
}

// createBody asks for a backup.
type createBody struct {
	Type          string `json:"type"`
	WebsiteID     string `json:"website_id"`
	DatabaseID    string `json:"database_id"`
	DestinationID string `json:"destination_id"`
	// IncludeDatabases is a pointer so that omitting it means "yes".
	//
	// A website's files restored without the schema the application expects
	// produce a site that is broken in a more confusing way than one that is
	// simply gone, so the safe default is the one that happens when nobody
	// thought about it.
	IncludeDatabases *bool `json:"include_databases"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var body createBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	include := true
	if body.IncludeDatabases != nil {
		include = *body.IncludeDatabases
	}

	item, err := h.service.Create(ctx, CreateRequest{
		Type:             body.Type,
		WebsiteID:        body.WebsiteID,
		DatabaseID:       body.DatabaseID,
		DestinationID:    body.DestinationID,
		IncludeDatabases: include,
	}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, item)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	item, err := h.service.GetBackup(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, item)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	err := h.service.Delete(ctx, r.PathValue("id"),
		httpx.RequestIDFromContext(ctx), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// restoreBody confirms a restore.
type restoreBody struct {
	Confirm      string   `json:"confirm"`
	Sites        []string `json:"sites"`
	Databases    []string `json:"databases"`
	KeepPrevious bool     `json:"keep_previous"`
}

// restore queues putting a backup back.
//
// It is answered with the *job*, not with a result, because a restore of a real
// site takes minutes and a request that waited for it would time out first. The
// job is what the page follows.
func (h *Handler) restore(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var body restoreBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	job, err := h.service.Restore(ctx, r.PathValue("id"), RestoreRequest{
		Confirm:      body.Confirm,
		Sites:        body.Sites,
		Databases:    body.Databases,
		KeepPrevious: body.KeepPrevious,
	}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	// 202: unlike everything else here, this one really does finish later.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()

	result, err := h.service.Verify(ctx, r.PathValue("id"),
		httpx.RequestIDFromContext(ctx), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	// A backup that failed verification is answered with 200 and ok: false, not
	// with an error. The check ran and produced an answer; the answer is bad
	// news about the archive, not a failure of the request — and an error
	// envelope has nowhere to put the detail that says which part is wrong.
	httpx.OK(w, r, result)
}

// -------------------------------------------------------- destinations

func (h *Handler) listDestinations(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	destinations, err := h.service.repo.ListDestinations(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"destinations": destinations,
		"count":        len(destinations),
		"kinds":        validate.DestinationKinds,
	})
}

func (h *Handler) createDestination(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input DestinationInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}

	dest, err := h.service.CreateDestination(ctx, input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, dest)
}

func (h *Handler) updateDestination(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input DestinationInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}

	dest, err := h.service.UpdateDestination(ctx, r.PathValue("id"), input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, dest)
}

func (h *Handler) deleteDestination(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	if err := h.service.DeleteDestination(ctx, r.PathValue("id"), actorFrom(r)); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// checkDestination writes a test object and reads it back.
//
// A POST because it writes. It answers 200 with the destination whether or not
// the check passed: "we could not reach it, and here is what the storage said"
// is the answer, and it belongs on the destination where the page shows it.
func (h *Handler) checkDestination(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), checkTimeout)
	defer cancel()

	dest, err := h.service.CheckDestination(ctx, r.PathValue("id"),
		httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, dest)
}

// ------------------------------------------------------------ schedules

func (h *Handler) listSchedules(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	schedules, err := h.service.repo.ListSchedules(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"schedules": schedules, "count": len(schedules)})
}

func (h *Handler) createSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input ScheduleInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if input.RetentionDays == 0 {
		input.RetentionDays = 14
	}
	if input.KeepLast == 0 {
		input.KeepLast = 3
	}

	schedule, err := h.service.CreateSchedule(ctx, input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, schedule)
}

func (h *Handler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input ScheduleInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}

	schedule, err := h.service.UpdateSchedule(ctx, r.PathValue("id"), input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, schedule)
}

func (h *Handler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	if err := h.service.DeleteSchedule(ctx, r.PathValue("id"), actorFrom(r)); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) runSchedule(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	item, err := h.service.RunSchedule(ctx, r.PathValue("id"), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    item,
	})
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
		UserID:          claims.UserID,
		IPAddress:       clientIP(r),
		UserAgent:       r.UserAgent(),
		CanManageServer: rbac.Has(claims.Permissions, rbac.PermServerManage),
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitFrom reads a listing's page size.
func limitFrom(r *http.Request) int {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			return parsed
		}
	}
	return 0
}

// translate maps a failure to its HTTP shape.
//
// Each of these is a different next step. "This backup is not one we can
// restore", "you did not confirm", and "that destination is still used by a
// schedule" are three different things somebody has to do something about, and
// collapsing them into one 400 would make the page unable to say which.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrPanelNeedsServerManage):
		return httpx.Forbidden(err.Error())
	case errors.Is(err, ErrPanelRestoreOnHost):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, ErrNameTaken), errors.Is(err, ErrDestinationInUse):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(agentclient.Message(err))
	case errors.Is(err, ErrNotConfirmed), errors.Is(err, ErrNotRestorable),
		errors.Is(err, ErrNothingToBackUp), errors.Is(err, ErrInvalidDestination),
		errors.Is(err, ErrDestinationUnchecked):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidBackupType),
		errors.Is(err, validate.ErrInvalidBackupKey),
		errors.Is(err, validate.ErrInvalidDestination),
		errors.Is(err, validate.ErrInvalidRetention),
		errors.Is(err, validate.ErrInvalidBackupSchedule),
		errors.Is(err, validate.ErrInvalidChecksum):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
