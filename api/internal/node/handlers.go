package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds a request.
//
// Longer than the database handlers': starting a process and waiting for it to
// bind is the slow case, and it is still bounded.
const requestTimeout = 60 * time.Second

// installTimeout bounds installing dependencies, which downloads a whole
// dependency tree.
const installTimeout = 10 * time.Minute

// maxBodyBytes bounds a request body.
const maxBodyBytes = 32 << 10

// Handler serves the Node.js endpoints.
type Handler struct {
	service *Service
	repo    *Repository
	auth    *auth.Service
}

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Service *Service
	Repo    *Repository
	Auth    *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{service: opts.Service, repo: opts.Repo, auth: opts.Auth}
}

// Routes registers the endpoints on mux.
//
// Reading is gated on website.view and every change on website.update: a
// Node.js application is what a website serves, so the permission that governs
// changing a site is the one that governs changing what it runs.
//
// The environment is the exception. Its values are credentials, so revealing
// them needs server.manage — the same bar as anything else that hands back a
// working secret.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/node/versions", guarded(rbac.PermWebsiteView, h.versions))

	// Installing a runtime changes the whole host, not one site, so it needs
	// the permission that governs the server rather than a website.
	mux.Handle("POST /api/v1/node/versions/install", guarded(rbac.PermServerManage, h.install))
	mux.Handle("DELETE /api/v1/node/versions/{package}",
		guarded(rbac.PermServerManage, h.uninstall))

	mux.Handle("GET /api/v1/node/apps", guarded(rbac.PermWebsiteView, h.list))
	mux.Handle("POST /api/v1/node/apps", guarded(rbac.PermWebsiteUpdate, h.create))
	mux.Handle("GET /api/v1/node/apps/{id}", guarded(rbac.PermWebsiteView, h.get))
	mux.Handle("DELETE /api/v1/node/apps/{id}", guarded(rbac.PermWebsiteUpdate, h.delete))

	mux.Handle("POST /api/v1/node/apps/{id}/start", guarded(rbac.PermWebsiteUpdate, h.start))
	mux.Handle("POST /api/v1/node/apps/{id}/stop", guarded(rbac.PermWebsiteUpdate, h.stop))
	mux.Handle("POST /api/v1/node/apps/{id}/restart", guarded(rbac.PermWebsiteUpdate, h.restart))
	mux.Handle("GET /api/v1/node/apps/{id}/logs", guarded(rbac.PermWebsiteView, h.logs))
	mux.Handle("POST /api/v1/node/apps/{id}/dependencies",
		guarded(rbac.PermWebsiteUpdate, h.installDependencies))

	mux.Handle("PUT /api/v1/node/apps/{id}/environment", guarded(rbac.PermWebsiteUpdate, h.setEnv))
	mux.Handle("DELETE /api/v1/node/apps/{id}/environment/{key}",
		guarded(rbac.PermWebsiteUpdate, h.removeEnv))
	mux.Handle("GET /api/v1/node/apps/{id}/environment",
		guarded(rbac.PermServerManage, h.revealEnv))
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	versions, err := h.service.Versions(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		// A host the Agent cannot answer for runs nothing, which the page
		// renders rather than showing as an error.
		httpx.OK(w, r, map[string]any{
			"versions": []any{}, "count": 0, "available": false,
			"offers": []any{}, "can_install": false,
			"detail": "the agent could not be reached, so no runtime is offered",
		})
		return
	}
	httpx.OK(w, r, versions)
}

// installBody names the release line to install.
type installBody struct {
	// Package is one of the values the versions endpoint offered. There is no
	// free-text version: the Agent keeps the only table of package names.
	Package string `json:"package"`
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	// Installing downloads and unpacks a runtime, so it gets the long timeout.
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	var body installBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	version, err := h.service.InstallRuntime(ctx, httpx.RequestIDFromContext(ctx),
		body.Package, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"version": version, "installed": true})
}

func (h *Handler) uninstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	pkg := r.PathValue("package")
	if pkg == "" {
		httpx.Error(w, r, httpx.BadRequest("a package name is required"))
		return
	}

	if err := h.service.RemoveRuntime(ctx, httpx.RequestIDFromContext(ctx),
		pkg, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"removed": true})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	apps, err := h.service.List(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.OK(w, r, map[string]any{"applications": apps, "count": len(apps)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	app, err := h.service.Get(ctx, httpx.RequestIDFromContext(ctx), id)
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"application": app})
}

// createBody asks for a new application.
type createBody struct {
	WebsiteID string `json:"website_id"`
	// Name defaults to one derived from the domain. It becomes a systemd unit
	// name, so it is validated tightly.
	Name string `json:"name"`
	// Version may be omitted on a host with one runtime, which is most of them.
	Version string `json:"node_version"`
	// Startup defaults to server.js, which is the convention.
	Startup string `json:"startup_file"`
	Port    int    `json:"port"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body createBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !isUUID(body.WebsiteID) {
		httpx.Error(w, r, httpx.BadRequest("website_id must be a UUID"))
		return
	}

	app, err := h.service.Create(ctx, CreateRequest{
		WebsiteID: body.WebsiteID,
		Name:      body.Name,
		Version:   body.Version,
		Startup:   body.Startup,
		Port:      body.Port,
		Actor:     actorFrom(r),
		RequestID: httpx.RequestIDFromContext(ctx),
	})
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	// 201: the application exists on the host and is ready to start. It is not
	// running, because creating and starting are separate decisions.
	httpx.Created(w, r, map[string]any{"application": app})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.Delete(ctx, httpx.RequestIDFromContext(ctx), id, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.service.Start)
}

func (h *Handler) stop(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.service.Stop)
}

func (h *Handler) restart(w http.ResponseWriter, r *http.Request) {
	h.lifecycle(w, r, h.service.Restart)
}

// lifecycle is the shared shape of start, stop, and restart.
func (h *Handler) lifecycle(w http.ResponseWriter, r *http.Request,
	action func(context.Context, string, string, Actor) (App, error),
) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	app, err := action(ctx, httpx.RequestIDFromContext(ctx), id, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"application": app})
}

func (h *Handler) logs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	lines := 200
	if raw := r.URL.Query().Get("lines"); raw != "" {
		parsed, err := parsePositive(raw, 2000)
		if err != nil {
			httpx.Error(w, r, httpx.BadRequest(err.Error()))
			return
		}
		lines = parsed
	}

	logs, err := h.service.Logs(ctx, httpx.RequestIDFromContext(ctx), id, lines)
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, logs)
}

func (h *Handler) installDependencies(w http.ResponseWriter, r *http.Request) {
	// Its own timeout: npm downloads a dependency tree, and the request holds
	// until it finishes so the response says whether it worked.
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.InstallDependencies(ctx, httpx.RequestIDFromContext(ctx),
		id, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"installed": true})
}

// envBody sets one environment variable.
type envBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (h *Handler) setEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var body envBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.SetEnv(ctx, httpx.RequestIDFromContext(ctx),
		id, body.Key, body.Value, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	// The application is deliberately not restarted: a process reads its
	// environment once, so this takes effect on the next restart, and
	// restarting somebody's application as a side effect of editing a setting
	// is not the panel's decision.
	httpx.OK(w, r, map[string]any{
		"updated": true,
		"detail":  "the change takes effect the next time the application is restarted",
	})
}

func (h *Handler) removeEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	key := r.PathValue("key")
	if key == "" {
		httpx.Error(w, r, httpx.BadRequest("a variable name is required"))
		return
	}

	if err := h.service.RemoveEnv(ctx, httpx.RequestIDFromContext(ctx),
		id, key, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"removed": true,
		"detail":  "the change takes effect the next time the application is restarted",
	})
}

func (h *Handler) revealEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	env, err := h.service.RevealEnv(ctx, id, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	// No-store: this response body is a set of credentials, and a browser or
	// proxy cache holding it is a copy nobody is tracking.
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, r, map[string]any{"environment": env})
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

// parsePositive reads a bounded positive integer from a query parameter.
func parsePositive(raw string, max int) (int, error) {
	value := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, errInvalidLines
		}
		value = value*10 + int(r-'0')
		if value > max {
			return 0, errInvalidLines
		}
	}
	if value == 0 {
		return 0, errInvalidLines
	}
	return value, nil
}

var errInvalidLines = httpx.BadRequest(
	"lines must be a positive whole number of at most 2000")

func pathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value := r.PathValue(name)
	if !isUUID(value) {
		httpx.Error(w, r, httpx.BadRequest(name+" must be a UUID"))
		return "", false
	}
	return value, true
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
