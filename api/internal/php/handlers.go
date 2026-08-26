package php

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

// requestTimeout bounds a PHP request. These endpoints read the database and
// queue work; installation itself runs in the worker.
const requestTimeout = 10 * time.Second

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the PHP endpoints.
type Handler struct {
	service *Service
	repo    *Repository
	auth    *auth.Service
}

// HandlerOptions configures a Handler.
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
// Installing a PHP version changes the whole server, so it is gated on
// server.manage rather than a website permission: it is not something a user
// who may only edit their own site should be able to do.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/php/versions", guarded(rbac.PermServerView, h.listVersions))
	mux.Handle("GET /api/v1/php/versions/{version}", guarded(rbac.PermServerView, h.getVersion))
	mux.Handle("POST /api/v1/php/versions/install", guarded(rbac.PermServerManage, h.install))
	mux.Handle("DELETE /api/v1/php/versions/{version}", guarded(rbac.PermServerManage, h.uninstall))

	mux.Handle("GET /api/v1/websites/{id}/php", guarded(rbac.PermWebsiteView, h.getWebsitePHP))
	mux.Handle("PATCH /api/v1/websites/{id}/php", guarded(rbac.PermWebsiteUpdate, h.setWebsitePHP))
	mux.Handle("GET /api/v1/websites/{id}/php/config", guarded(rbac.PermWebsiteView, h.getConfig))
	mux.Handle("PATCH /api/v1/websites/{id}/php/config", guarded(rbac.PermWebsiteUpdate, h.setConfig))
}

func (h *Handler) listVersions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	versions, err := h.repo.ListVersions(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, map[string]any{"versions": versions, "count": len(versions)})
}

func (h *Handler) getVersion(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	version := validate.NormalizePHPVersion(r.PathValue("version"))
	if err := validate.PHPVersion(version); err != nil {
		httpx.Error(w, r, httpx.BadRequest("version must be a major.minor release such as 8.3"))
		return
	}

	found, err := h.repo.GetVersion(ctx, version)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, found)
}

// installBody names a version to install.
type installBody struct {
	Version string `json:"version"`
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body installBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	job, err := h.service.Install(ctx, body.Version, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// 202: the version is not on the host yet, only scheduled to be.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

func (h *Handler) uninstall(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	version := validate.NormalizePHPVersion(r.PathValue("version"))
	if err := validate.PHPVersion(version); err != nil {
		httpx.Error(w, r, httpx.BadRequest("version must be a major.minor release such as 8.3"))
		return
	}

	job, err := h.service.Uninstall(ctx, version, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

func (h *Handler) getWebsitePHP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	pool, err := h.repo.GetPool(ctx, id)
	if err != nil {
		if errors.Is(err, ErrPoolNotFound) {
			// Not an error: a static site is a valid configuration, and a 404
			// would suggest the website itself was missing.
			httpx.OK(w, r, map[string]any{"enabled": false})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, map[string]any{"enabled": true, "pool": pool})
}

// setPHPBody selects a website's PHP version.
type setPHPBody struct {
	// Version is the PHP to run, and an explicit null turns PHP off.
	//
	// It is RawMessage rather than *string because a *string cannot tell an
	// absent field from an explicit null — JSON null and a missing key both
	// decode to nil — and those mean different things here: one is "leave PHP
	// alone", the other is "make this a static site".
	Version json.RawMessage `json:"version"`

	MemoryLimit       *string `json:"memory_limit"`
	UploadMaxFilesize *string `json:"upload_max_filesize"`
	MaxExecutionTime  *int    `json:"max_execution_time"`
	OPcacheEnabled    *bool   `json:"opcache"`
	MaxChildren       *int    `json:"max_children"`
}

func (h *Handler) setWebsitePHP(w http.ResponseWriter, r *http.Request) {
	h.applyPHPChange(w, r, true)
}

func (h *Handler) getConfig(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	pool, err := h.repo.GetPool(ctx, id)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// Rendered in the shape API_SPEC section 9 documents, rather than as the
	// raw row: these are php.ini names and a caller should see them as such.
	httpx.OK(w, r, map[string]any{
		"memory_limit":        derefString(pool.MemoryLimit, DefaultMemoryLimit),
		"upload_max_filesize": derefString(pool.UploadMaxFilesize, DefaultUploadMaxFilesize),
		"max_execution_time":  derefInt(pool.MaxExecutionTime, DefaultMaxExecutionTime),
		"opcache":             pool.OPcacheEnabled,
		"max_children":        derefInt(pool.MaxChildren, 0),
		"version":             pool.PHPVersion,
	})
}

// setConfig changes php.ini values without changing the version.
func (h *Handler) setConfig(w http.ResponseWriter, r *http.Request) {
	h.applyPHPChange(w, r, false)
}

// applyPHPChange handles both the version and the configuration endpoints.
//
// They differ only in whether the version may change; both rewrite the pool
// and reload it, because a php.ini value that is written but not reloaded is a
// setting the panel claims is active and is not.
func (h *Handler) applyPHPChange(w http.ResponseWriter, r *http.Request, allowVersionChange bool) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body setPHPBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	request := SetRequest{
		WebsiteID: id,
		Settings: Settings{
			MemoryLimit:       body.MemoryLimit,
			UploadMaxFilesize: body.UploadMaxFilesize,
			MaxExecutionTime:  body.MaxExecutionTime,
			OPcacheEnabled:    body.OPcacheEnabled,
			MaxChildren:       body.MaxChildren,
		},
		Actor: actorFrom(r),
	}

	if allowVersionChange {
		version, present, err := decodeVersion(body.Version)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		if !present {
			httpx.Error(w, r, httpx.BadRequest(
				"version is required; pass null to turn PHP off"))
			return
		}
		request.Version = version
	} else {
		// The configuration endpoint keeps whatever version the site runs. A
		// site with no pool has nothing to configure.
		pool, err := h.repo.GetPool(ctx, id)
		if err != nil {
			httpx.Error(w, r, translate(err))
			return
		}
		request.Version = pool.PHPVersion
	}

	job, err := h.service.SetVersion(ctx, request)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

// translate maps a domain error to its HTTP shape.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return httpx.NotFound("That PHP version is not known to this server")
	case errors.Is(err, ErrPoolNotFound):
		return httpx.NotFound("This website does not run PHP")
	case errors.Is(err, websites.ErrNotFound):
		return httpx.NotFound("Website not found")
	case errors.Is(err, ErrVersionInUse):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrVersionUnavailable):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrWebsiteNotReady):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrInstallUnsupported):
		return httpx.New(http.StatusServiceUnavailable, httpx.CodeUnavailable, err.Error())
	case errors.Is(err, validate.ErrInvalidPHPVersion),
		errors.Is(err, validate.ErrInvalidPHPSetting):
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

// decodeVersion reads the version field, distinguishing absent from null.
//
// It reports the version, whether the field was present at all, and an error
// for anything that is neither a string nor null.
func decodeVersion(raw json.RawMessage) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	if string(raw) == "null" {
		// Present and explicitly null: turn PHP off.
		return "", true, nil
	}

	var version string
	if err := json.Unmarshal(raw, &version); err != nil {
		return "", false, httpx.BadRequest("version must be a string or null")
	}
	return version, true, nil
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

func derefString(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func derefInt(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
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
