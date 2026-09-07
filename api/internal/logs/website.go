package logs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// A website's own logs, served on the website's own routes.
//
// Not through /api/v1/logs. Those need server.view, which is the permission
// for looking at the whole machine — every site's traffic, the mail queue, the
// audit trail. Somebody who may look after one website should be able to read
// that website's logs without being handed all of that, and the way to arrange
// that is for a site's logs to hang off the site.

// websiteLogKinds are the logs a site has.
//
// Two, and named rather than free text: the key is composed here and the Agent
// looks it up in a catalogue it built itself, so an unknown kind fails at the
// lookup — but refusing it at the edge means the error names the kind rather
// than the key nobody typed.
var websiteLogKinds = map[string]string{
	"access": "access",
	"error":  "error",
}

// ErrUnknownLogKind is returned for a kind a site does not have.
var ErrUnknownLogKind = errors.New("a website has an access log and an error log")

// WebsiteSource is the log source key for one of a website's logs.
//
// It matches logs.SiteSourceKey in the Agent. The two are separate modules and
// cannot share a constant, so this is the place that has to agree with it, and
// the integration checks are what say whether it still does.
func WebsiteSource(domain, kind string) (string, error) {
	if _, ok := websiteLogKinds[kind]; !ok {
		return "", fmt.Errorf("%w: %q is neither", ErrUnknownLogKind, kind)
	}
	normalized := validate.NormalizeDomain(domain)
	if err := validate.ServerName(normalized); err != nil {
		return "", err
	}
	return "site." + normalized + "." + kind, nil
}

// WebsiteRoutes registers the per-website log endpoints.
//
// Guarded by website.view rather than server.view. That is the whole reason
// these exist separately.
func (h *Handler) WebsiteRoutes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermWebsiteView)(next))
	}

	mux.Handle("GET /api/v1/websites/{id}/logs", guarded(h.websiteLogs))
	mux.Handle("GET /api/v1/websites/{id}/logs/{kind}", guarded(h.websiteLogTail))
	mux.Handle("GET /api/v1/websites/{id}/logs/{kind}/download", guarded(h.websiteLogDownload))
}

// websiteLogs lists what logs this site has.
func (h *Handler) websiteLogs(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	domain, ok := h.domainFor(w, r)
	if !ok {
		return
	}

	// Reported from the host's own catalogue rather than assumed, so a site
	// whose log directory is missing says so instead of offering a link that
	// answers "no such log".
	sources, err := h.service.Sources(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	mine := make([]agentclient.LogSource, 0, 2)
	prefix := "site." + domain + "."
	for _, source := range sources.Sources {
		if strings.HasPrefix(source.Key, prefix) {
			mine = append(mine, source)
		}
	}

	w.Header().Set("Cache-Control", "private, no-store")
	httpx.OK(w, r, map[string]any{"domain": domain, "logs": mine})
}

// websiteLogTail returns the end of one of a site's logs.
func (h *Handler) websiteLogTail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readTimeout)
	defer cancel()

	domain, ok := h.domainFor(w, r)
	if !ok {
		return
	}

	key, err := WebsiteSource(domain, r.PathValue("kind"))
	if err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}

	opts, err := tailOptions(r)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	result, err := h.service.Tail(ctx, httpx.RequestIDFromContext(ctx), key, opts)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	if result.Lines == nil {
		result.Lines = []agentclient.LogLine{}
	}

	// A log is a moving target and a cached page of it is quietly wrong. It is
	// also somebody's access log, which no intermediary should keep a copy of.
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.OK(w, r, result)
}

// websiteLogDownload sends one of a site's logs as a file.
func (h *Handler) websiteLogDownload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), downloadTimeout)
	defer cancel()
	r = r.WithContext(ctx)

	domain, ok := h.domainFor(w, r)
	if !ok {
		return
	}

	key, err := WebsiteSource(domain, r.PathValue("kind"))
	if err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}

	// The same body as the host-wide download, so the headers a log is sent
	// with — octet-stream, nosniff, an attachment — are decided in one place.
	// A log line is attacker-influenced content: anyone who can make a request
	// can write one into an access log.
	h.sendLog(w, r, key)
}

// domainFor reads the website and returns its primary domain.
//
// The domain comes from the panel's own record, never from the request. A
// caller names a website by id and the log key is composed from what that
// website actually is, so there is no way to ask for one site's logs by
// naming another site's domain.
func (h *Handler) domainFor(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return "", false
	}

	site, err := h.websites.Get(r.Context(), id)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return "", false
	}
	return validate.NormalizeDomain(site.PrimaryDomain), true
}

// isUUID reports whether a path value looks like an id this panel issued.
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
