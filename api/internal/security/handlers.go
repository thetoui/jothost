package security

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

// Timeouts. Reading is a database query; a scan walks a filesystem and reaches
// the Agent seven times.
const (
	readTimeout = 30 * time.Second
	scanTimeout = 15 * time.Minute
)

// maxBodyBytes bounds a request body. The only body here carries a reason.
const maxBodyBytes = 8 << 10

// Handler serves the security endpoints.
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
// Everything needs security.view, which migration 0018 adds and grants to admin
// and operator. It is not server.view: a findings list is a list of the ways
// into this machine, with the exact port and the exact path, and it is the most
// sensitive read in the panel.
//
// Reading and accepting share the permission deliberately. Splitting them would
// mean the people who can see "we still allow password logins" are not the
// people who can record that it is deliberate, and the finding would then be
// re-reported forever by a panel nobody can quiet.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermSecurityView)(next))
	}

	mux.Handle("GET /api/v1/security/score", guarded(h.overview))
	mux.Handle("GET /api/v1/security/findings", guarded(h.findings))
	mux.Handle("POST /api/v1/security/scan", guarded(h.scan))
	mux.Handle("PATCH /api/v1/security/findings/{id}", guarded(h.patchFinding))
	mux.Handle("GET /api/v1/security/history", guarded(h.history))
}

// overview answers GET /security/score with everything the page needs.
//
// The score alone would be a number with no way to act on it, and a page that
// had to make two calls to show one card would show the number first and the
// findings a moment later — which is the wrong order to read them in.
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

func (h *Handler) findings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	params := ListParams{
		Status:   r.URL.Query().Get("status"),
		Scanner:  r.URL.Query().Get("scanner"),
		Severity: r.URL.Query().Get("severity"),
		Limit:    limitFrom(r),
	}
	if params.Scanner != "" {
		if err := validate.Scanner(params.Scanner); err != nil {
			httpx.Error(w, r, translate(err))
			return
		}
	}
	if params.Severity != "" {
		if err := validate.FindingSeverity(params.Severity); err != nil {
			httpx.Error(w, r, translate(err))
			return
		}
	}

	findings, err := h.service.Findings(ctx, params)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"findings": findings,
		"count":    len(findings),
		"scanners": validate.Scanners,
	})
}

// scan runs every scanner.
//
// A POST because it is not free: it walks a filesystem and reaches the Agent
// seven times. A GET that did that would be re-run by every retry, every
// prefetch and every refresh of the page.
//
// It is synchronous rather than a job. A scan reads and changes nothing, so
// there is nothing to reconcile if it fails halfway, and an operator pressing
// "scan" wants the answer rather than a job to follow. The timeout is generous
// because the filesystem walk is proportional to how many files a customer has.
func (h *Handler) scan(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), scanTimeout)
	defer cancel()

	scan, err := h.service.Scan(ctx, httpx.RequestIDFromContext(ctx), actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, scan)
}

// patchBody is what may be changed about a finding.
type patchBody struct {
	// Status is "accepted" or "open". There is deliberately no "resolved":
	// whether a weakness still exists is the scanner's to decide, and a panel
	// where a person can mark an open port as closed is a panel that will one
	// day say an open port is closed.
	Status string `json:"status"`
	Reason string `json:"reason"`
}

func (h *Handler) patchFinding(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	var body patchBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	var (
		finding Finding
		err     error
	)
	switch body.Status {
	case StatusAccepted:
		finding, err = h.service.Accept(ctx, r.PathValue("id"), body.Reason, actorFrom(r))
	case StatusOpen:
		finding, err = h.service.Reopen(ctx, r.PathValue("id"), actorFrom(r))
	case StatusResolved:
		httpx.Error(w, r, httpx.ValidationFailed(
			"A finding is resolved when a scan no longer finds it, not by hand. "+
				"Accept it instead if the risk is known and deliberate."))
		return
	default:
		httpx.Error(w, r, httpx.ValidationFailed(
			`A finding may be set to "accepted" or back to "open".`))
		return
	}

	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, finding)
}

func (h *Handler) history(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	scans, err := h.service.History(ctx, limitFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"scans": scans, "count": len(scans)})
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
	case errors.Is(err, ErrScanRunning):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrNotOpen), errors.Is(err, ErrNotAccepted):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidAcceptance),
		errors.Is(err, validate.ErrInvalidFindingSeverity),
		errors.Is(err, validate.ErrInvalidScanner):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsUnsupported(err):
		return httpx.Conflict(agentclient.Message(err))
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
