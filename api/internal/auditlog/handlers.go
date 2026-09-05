// Package auditlog serves the audit trail for reading.
//
// Separate from the audit package that writes it, and not by preference: the
// auth service records audit events, so a handler that needs auth for its
// permission checks cannot live beside the recorder without a cycle. The split
// is honest about the two directions anyway — one package appends, this one
// only ever reads.
package auditlog

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// readTimeout bounds a query against a table that only ever grows.
const readTimeout = 30 * time.Second

// Handler serves the audit trail.
type Handler struct {
	reader *audit.Reader
	auth   *auth.Service
}

// HandlerOptions configure a Handler.
type HandlerOptions struct {
	Reader *audit.Reader
	Auth   *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{reader: opts.Reader, auth: opts.Auth}
}

// Routes registers the endpoints on mux.
//
// `audit.view` and nothing else. The permission has existed since migration
// 0001 and guarded nothing at all: the trail was written from the first phase
// and there was no way to read it through the panel, so the answer to "who
// deleted that website" was to open a database connection. Phase 24's sweep
// found it by pointing two checks at /api/v1/audit and passing on the 404.
//
// It is a separate permission from server.view on purpose. The trail records
// who did what and from which address, across every customer on the host; that
// is a different thing to be trusted with from seeing how much disk is left.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/audit", guarded(rbac.PermAuditView, h.list))
	mux.Handle("GET /api/v1/audit/actions", guarded(rbac.PermAuditView, h.actions))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	query := r.URL.Query()
	params := audit.ListParams{
		Action:       strings.TrimSpace(query.Get("action")),
		UserID:       strings.TrimSpace(query.Get("user_id")),
		ResourceType: strings.TrimSpace(query.Get("resource_type")),
		ResourceID:   strings.TrimSpace(query.Get("resource_id")),
		Status:       strings.ToUpper(strings.TrimSpace(query.Get("status"))),
	}

	// The two id filters are validated rather than passed through. Postgres
	// refuses a malformed uuid with an error that would surface as a 500, and
	// a bad filter is the caller's mistake, not the server's.
	for name, value := range map[string]string{
		"user_id": params.UserID, "resource_id": params.ResourceID,
	} {
		if value != "" && !isUUID(value) {
			httpx.Error(w, r, httpx.BadRequest(name+" must be a UUID"))
			return
		}
	}

	if params.Status != "" && params.Status != audit.StatusSuccess && params.Status != audit.StatusFailure {
		httpx.Error(w, r, httpx.BadRequest("status must be SUCCESS or FAILURE"))
		return
	}

	for name, dst := range map[string]**time.Time{
		"since": &params.Since, "until": &params.Until,
	} {
		raw := strings.TrimSpace(query.Get(name))
		if raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			httpx.Error(w, r, httpx.BadRequest(name+" must be an RFC 3339 timestamp"))
			return
		}
		*dst = &parsed
	}

	if raw := query.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			httpx.Error(w, r, httpx.BadRequest("limit must be a positive integer"))
			return
		}
		params.Limit = limit
	}
	if raw := query.Get("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			httpx.Error(w, r, httpx.BadRequest("offset must be zero or more"))
			return
		}
		params.Offset = offset
	}

	entries, total, err := h.reader.List(ctx, params)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	// `total` is the number that matched the filter, not the number returned.
	// Without it a page of 50 cannot be told from a history of 50.
	httpx.OK(w, r, map[string]any{
		"entries": entries,
		"count":   len(entries),
		"total":   total,
		"limit":   effectiveLimit(params.Limit),
		"offset":  params.Offset,
	})
}

func (h *Handler) actions(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	actions, err := h.reader.Actions(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.OK(w, r, map[string]any{"actions": actions, "count": len(actions)})
}

// effectiveLimit reports the limit actually applied, which is not always the
// one asked for. A caller that requested 5,000 rows should be told it got 200
// rather than being left to infer it from a short page.
func effectiveLimit(requested int) int {
	switch {
	case requested <= 0:
		return audit.DefaultLimit
	case requested > audit.MaxLimit:
		return audit.MaxLimit
	default:
		return requested
	}
}

// isUUID reports whether value is shaped like a UUID.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}
