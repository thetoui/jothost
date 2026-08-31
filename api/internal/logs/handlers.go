package logs

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// Timeouts. Reading a tail is a bounded read of a file and should be quick;
// a download is a whole file crossing two hops, and a large access log takes
// as long as it takes.
const (
	readTimeout     = 30 * time.Second
	downloadTimeout = 10 * time.Minute
)

// Handler serves the log endpoints.
type Handler struct {
	service *Service
	auth    *auth.Service
	log     *slog.Logger
}

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Service *Service
	Auth    *auth.Service
	Log     *slog.Logger
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Handler{service: opts.Service, auth: opts.Auth, log: log}
}

// Routes registers the endpoints on mux.
//
// Everything here is a read, and all of it needs server.view. The host's logs
// are not one website's business: an access log names every site on the
// machine, and the authentication log names the people who administer it.
//
// The routes API_SPEC section 19 lists — /logs/nginx/access, /logs/php and the
// rest — are the catalogue keys with their dots written as path segments. Both
// spellings reach the same place, so the documented URLs work and the keys that
// only exist at runtime (php.8.4, node.myapp.error) have a URL too.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/logs", guarded(rbac.PermServerView, h.sources))
	mux.Handle("GET /api/v1/logs/{key}", guarded(rbac.PermServerView, h.tail))
	mux.Handle("GET /api/v1/logs/{key}/download", guarded(rbac.PermServerView, h.download))
	// The spec's two-segment spelling: /logs/nginx/access is nginx.access.
	mux.Handle("GET /api/v1/logs/{group}/{name}", guarded(rbac.PermServerView, h.tail))
	mux.Handle("GET /api/v1/logs/{group}/{name}/download",
		guarded(rbac.PermServerView, h.download))
}

// sourceKey reads the log key out of the path.
//
// A two-segment path is joined with a dot, which is how the spec's URLs and the
// Agent's keys are the same thing. Nothing else about the value is interpreted
// here: it is passed to the Agent, which matches it against its own catalogue
// and refuses anything else. This function must not be tempted into "helpfully"
// accepting a path.
func sourceKey(r *http.Request) string {
	if key := r.PathValue("key"); key != "" {
		return key
	}
	group, name := r.PathValue("group"), r.PathValue("name")
	if group == "" || name == "" {
		return ""
	}
	return group + "." + name
}

func (h *Handler) sources(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	result, err := h.service.Sources(ctx, httpx.RequestIDFromContext(r.Context()))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			// A host whose Agent cannot read logs is a configuration, not a
			// fault. The panel says so with an empty list rather than an error.
			httpx.OK(w, r, agentclient.LogSourceList{
				Sources: []agentclient.LogSource{},
				Levels:  []string{},
			})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	if result.Sources == nil {
		result.Sources = []agentclient.LogSource{}
	}
	httpx.OK(w, r, result)
}

func (h *Handler) tail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	key := sourceKey(r)
	if key == "" {
		httpx.Error(w, r, httpx.BadRequest("a log is required"))
		return
	}

	opts, err := tailOptions(r)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	result, err := h.service.Tail(ctx, httpx.RequestIDFromContext(r.Context()), key, opts)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	if result.Lines == nil {
		result.Lines = []agentclient.LogLine{}
	}
	// A log is a moving target, and a cached page of it is a page that is
	// quietly wrong. It is also somebody's access log, which no intermediary
	// should be keeping a copy of.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.OK(w, r, result)
}

// tailOptions reads the query string.
//
// `from` and `to` in API_SPEC section 19 are deliberately not implemented here;
// see docs/PHASE11.md section 6. Filtering by time means parsing each daemon's
// own timestamp format, and a filter that silently returns nothing because it
// failed to parse a format is worse than one that is not offered.
func tailOptions(r *http.Request) (agentclient.LogTailOptions, error) {
	query := r.URL.Query()

	opts := agentclient.LogTailOptions{
		Lines:  DefaultLines,
		Search: strings.TrimSpace(query.Get("search")),
		Level:  strings.ToLower(strings.TrimSpace(query.Get("level"))),
	}

	if raw := query.Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			return opts, httpx.ValidationFailed("limit must be a number")
		}
		opts.Lines = value
	}
	if raw := query.Get("after"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return opts, httpx.ValidationFailed("after must be a number")
		}
		opts.After = value
	}
	if opts.Lines <= 0 {
		opts.Lines = DefaultLines
	}

	if err := ValidateOptions(opts); err != nil {
		return opts, err
	}
	return opts, nil
}

func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), downloadTimeout)
	defer cancel()

	key := sourceKey(r)
	if key == "" {
		httpx.Error(w, r, httpx.BadRequest("a log is required"))
		return
	}

	requestID := httpx.RequestIDFromContext(ctx)

	// The size is read first so a log that is not on this host fails with a
	// clean error rather than with headers already sent and a truncated body.
	sources, err := h.service.Sources(ctx, requestID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	var found *agentclient.LogSource
	for i := range sources.Sources {
		if sources.Sources[i].Key == key {
			found = &sources.Sources[i]
			break
		}
	}
	if found == nil {
		httpx.Error(w, r, httpx.NotFound("That log is not one this panel reads"))
		return
	}
	if !found.Present {
		httpx.Error(w, r, httpx.NotFound("This host does not have that log"))
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	// octet-stream and an attachment disposition, always. A log file is
	// attacker-influenced content — anyone who can make a request can write a
	// line into an access log — and serving it back as anything a browser
	// renders would run it on the panel's own origin.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+downloadName(key)+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(found.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")

	written, err := h.service.Download(ctx, requestID, key, Actor{
		UserID:    claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	}, w)
	if err != nil {
		// The headers are already sent, so there is no turning this into a
		// clean error response. Logging it is what is left, and the short body
		// is what tells the client something went wrong.
		h.log.Error("log download failed",
			"request_id", requestID, "source", key, "written", written,
			"error", err.Error())
		return
	}
}

// downloadName turns a key into a filename.
//
// Built from the key rather than from the file's own path: the path is the
// host's business, and a filename of "error.log" tells whoever opens it three
// days later nothing about which error log it was.
func downloadName(key string) string {
	safe := make([]rune, 0, len(key)+4)
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			safe = append(safe, r)
		default:
			safe = append(safe, '-')
		}
	}
	return string(safe) + ".log"
}

// translate maps a failure to its HTTP shape.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrInvalidOption):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("This host cannot read logs")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
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
