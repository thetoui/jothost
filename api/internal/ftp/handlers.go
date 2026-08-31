package ftp

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

// Timeouts. Reading is a handful of calls to a local daemon; installing pulls a
// package and its dependencies.
const (
	requestTimeout = 60 * time.Second
	installTimeout = 10 * time.Minute
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the FTP endpoints.
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
// Reading needs server.view; every change needs ftp.manage, which migration
// 0013 adds. It is its own permission rather than website.update because an FTP
// credential reaches a site's files without going through the panel at all, and
// it keeps working after the person who was given it stops being a panel user.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/ftp", guarded(rbac.PermServerView, h.overview))
	mux.Handle("POST /api/v1/ftp/install", guarded(rbac.PermFTPManage, h.install))
	mux.Handle("PUT /api/v1/ftp/settings", guarded(rbac.PermFTPManage, h.settings))

	mux.Handle("GET /api/v1/ftp/sessions", guarded(rbac.PermServerView, h.sessions))
	mux.Handle("DELETE /api/v1/ftp/sessions/{pid}", guarded(rbac.PermFTPManage, h.disconnect))

	mux.Handle("GET /api/v1/ftp/users", guarded(rbac.PermServerView, h.list))
	mux.Handle("POST /api/v1/ftp/users", guarded(rbac.PermFTPManage, h.create))
	mux.Handle("PATCH /api/v1/ftp/users/{id}", guarded(rbac.PermFTPManage, h.update))
	mux.Handle("DELETE /api/v1/ftp/users/{id}", guarded(rbac.PermFTPManage, h.remove))

	// The website's own FTP tab. The same accounts, filtered to one site, so
	// the tab does not have to fetch every account on the host to show three.
	mux.Handle("GET /api/v1/websites/{id}/ftp", guarded(rbac.PermServerView, h.forWebsite))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, emptyOverview())
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, overview)
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	status, err := h.service.Install(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, status)
}

// settingsBody is the settings PUT. An omitted field is left alone, which is
// why every one is a pointer.
type settingsBody struct {
	PassiveFrom       *int    `json:"passive_from"`
	PassiveTo         *int    `json:"passive_to"`
	TLSWebsiteID      *string `json:"tls_website_id"`
	RequireTLS        *bool   `json:"require_tls"`
	MasqueradeAddress *string `json:"masquerade_address"`
	MaxClients        *int    `json:"max_clients"`
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body settingsBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	settings, err := h.service.Configure(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		SettingsRequest{
			PassiveFrom:       body.PassiveFrom,
			PassiveTo:         body.PassiveTo,
			TLSWebsiteID:      body.TLSWebsiteID,
			RequireTLS:        body.RequireTLS,
			MasqueradeAddress: body.MasqueradeAddress,
			MaxClients:        body.MaxClients,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, settings)
}

func (h *Handler) sessions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	sessions, err := h.service.Sessions(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, map[string]any{"sessions": []agentclient.FTPSession{}, "count": 0})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"sessions": sessions, "count": len(sessions)})
}

// disconnect ends one session.
//
// A session is named by its pid because that is what the host reports and what
// the host can act on. The Agent checks it against the live session list *and*
// against /proc before signalling anything — pids are reused, and the Agent
// runs as root, so a signal to the wrong pid would be delivered successfully.
func (h *Handler) disconnect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	pid, err := strconv.Atoi(r.PathValue("pid"))
	if err != nil || pid <= 1 {
		httpx.Error(w, r, httpx.ValidationFailed("That is not an FTP session."))
		return
	}

	if err := h.service.Disconnect(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx), pid); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"pid": pid, "disconnected": true})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"users": overview.Users, "count": len(overview.Users)})
}

func (h *Handler) forWebsite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	users, err := h.service.ForWebsite(ctx, httpx.RequestIDFromContext(ctx), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"users": users, "count": len(users)})
}

// createBody is the body of a create.
type createBody struct {
	WebsiteID string `json:"website_id"`
	Username  string `json:"username"`
	// Optional. Left out, the panel generates one and returns it once — which
	// is the better path, and the page offers it first.
	Password    string `json:"password"`
	HomeSubpath string `json:"home_subpath"`
	AccessLevel string `json:"access_level"`
	QuotaMB     int    `json:"quota_mb"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body createBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	result, err := h.service.Create(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		CreateRequest{
			WebsiteID:   strings.TrimSpace(body.WebsiteID),
			Username:    strings.TrimSpace(body.Username),
			Password:    body.Password,
			HomeSubpath: body.HomeSubpath,
			AccessLevel: body.AccessLevel,
			QuotaMB:     body.QuotaMB,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, result)
}

// updateBody is the body of a change. An omitted field is left alone.
type updateBody struct {
	HomeSubpath *string `json:"home_subpath"`
	AccessLevel *string `json:"access_level"`
	QuotaMB     *int    `json:"quota_mb"`
	Suspended   *bool   `json:"suspended"`
	Password    string  `json:"password"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body updateBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	user, err := h.service.Update(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), UpdateRequest{
			HomeSubpath: body.HomeSubpath,
			AccessLevel: body.AccessLevel,
			QuotaMB:     body.QuotaMB,
			Suspended:   body.Suspended,
			Password:    body.Password,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, user)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	err := h.service.Delete(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// emptyOverview is what a host with no FTP server reports.
//
// A page rather than an error: "this host has no FTP server, here is how to
// install one" is more useful than a failure, and it is the same shape the
// fail2ban page uses.
func emptyOverview() Overview {
	return Overview{
		FTPStatus: agentclient.FTPStatus{
			Accounts: []agentclient.FTPAccount{},
			Sessions: []agentclient.FTPSession{},
			Reason:   ErrUnavailable.Error(),
		},
		Users: []User{},
	}
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

// translate maps a failure to its HTTP shape.
//
// Each of these is a different next step for an operator, which is why they are
// not collapsed: "that name is taken", "this host has no FTP server" and "that
// site has no certificate" are three different things to do.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrDuplicateName):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(ErrUnavailable.Error())
	case errors.Is(err, ErrWebsiteRequired), errors.Is(err, ErrNoAccount),
		errors.Is(err, ErrNoCertificate), errors.Is(err, ErrQuotaUnsupported):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidFTPUser),
		errors.Is(err, validate.ErrInvalidFTPPassword),
		errors.Is(err, validate.ErrInvalidFTPHome),
		errors.Is(err, validate.ErrInvalidFTPAccess),
		errors.Is(err, validate.ErrInvalidFTPQuota),
		errors.Is(err, validate.ErrInvalidPortRange):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
