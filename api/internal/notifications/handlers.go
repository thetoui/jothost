package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading is a database query; a test send reaches a mail server or
// an API and waits for it to answer.
const (
	readTimeout = 30 * time.Second
	testTimeout = 2 * time.Minute
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the notification endpoints.
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
// Everything needs notification.manage, which migration 0019 adds and grants to
// admin only. It is not server.manage: a channel holds an SMTP password or a bot
// token, and changing where the panel sends its alerts is how somebody quietly
// stops them arriving.
//
// Unlike monitor.manage, operators do not get it. Silencing a false alarm at
// three in the morning is an operator's job; silencing every alert on the
// machine is not.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermNotificationManage)(next))
	}

	mux.Handle("GET /api/v1/notifications", guarded(h.overview))
	mux.Handle("GET /api/v1/notifications/deliveries", guarded(h.deliveries))

	mux.Handle("GET /api/v1/notification-channels", guarded(h.listChannels))
	mux.Handle("POST /api/v1/notification-channels", guarded(h.createChannel))
	mux.Handle("PATCH /api/v1/notification-channels/{id}", guarded(h.updateChannel))
	mux.Handle("DELETE /api/v1/notification-channels/{id}", guarded(h.deleteChannel))
	mux.Handle("POST /api/v1/notification-channels/{id}/test", guarded(h.testChannel))
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

// deliveries is the record of what arrived and what did not.
//
// It is the most important read in this phase, because a notification system
// cannot report its own failure through itself. When delivery is broken, this
// is the only place that says so.
func (h *Handler) deliveries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	status := r.URL.Query().Get("status")
	if status != "" && status != StatusPending && status != StatusSent &&
		status != StatusFailed {
		httpx.Error(w, r, httpx.ValidationFailed(
			`A delivery is "pending", "sent" or "failed".`))
		return
	}

	deliveries, err := h.service.Deliveries(ctx, status, limitFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deliveries": deliveries, "count": len(deliveries)})
}

func (h *Handler) listChannels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	channels, err := h.service.repo.ListChannels(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"channels":    channels,
		"count":       len(channels),
		"kinds":       validate.ChannelKinds,
		"event_kinds": validate.EventKinds,
		"severities":  validate.NotifySeverities,
	})
}

func (h *Handler) createChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input ChannelInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}

	channel, err := h.service.CreateChannel(ctx, input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, channel)
}

func (h *Handler) updateChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var input ChannelInput
	if err := decode(r, &input); err != nil {
		httpx.Error(w, r, err)
		return
	}

	channel, err := h.service.UpdateChannel(ctx, r.PathValue("id"), input, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, channel)
}

func (h *Handler) deleteChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	if err := h.service.DeleteChannel(ctx, r.PathValue("id"), actorFrom(r)); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// testChannel sends a message through one channel, now.
//
// A POST because it sends. It answers 200 with the channel whether or not the
// message got through: "we could not reach it, and here is what the server said"
// is the answer, and it belongs on the channel where the page shows it. An error
// envelope would have nowhere to put the detail, and the detail is the point.
func (h *Handler) testChannel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()

	channel, err := h.service.Test(ctx, r.PathValue("id"), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, channel)
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

func limitFrom(r *http.Request) int {
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			return parsed
		}
	}
	return 0
}

// translate maps a failure to its HTTP shape.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrNameTaken):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrInvalidChannel), errors.Is(err, ErrNoChannels):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidChannel),
		errors.Is(err, validate.ErrInvalidNotifySeverity),
		errors.Is(err, validate.ErrInvalidEventKind):
		return httpx.ValidationFailed(err.Error())
	default:
		return err
	}
}
