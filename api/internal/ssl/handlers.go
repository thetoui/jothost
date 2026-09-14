package ssl

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/websites"
)

// requestTimeout bounds an SSL request. These endpoints read the database and
// queue work; issuance itself runs in the worker.
const requestTimeout = 10 * time.Second

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the certificate endpoints.
type Handler struct {
	service *Service
	repo    *Repository
	agent   *agentclient.Client
	auth    *auth.Service
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	Service *Service
	Repo    *Repository
	Agent   *agentclient.Client
	Auth    *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{service: opts.Service, repo: opts.Repo, agent: opts.Agent, auth: opts.Auth}
}

// Routes registers the endpoints on mux.
//
// Certificates are gated on ssl.manage rather than a website permission: a
// certificate is the site's identity, and issuing or revoking one is a
// different kind of act from editing its content.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/ssl", guarded(rbac.PermWebsiteView, h.list))
	mux.Handle("GET /api/v1/ssl/providers", guarded(rbac.PermWebsiteView, h.providers))
	mux.Handle("GET /api/v1/websites/{id}/ssl", guarded(rbac.PermWebsiteView, h.get))
	mux.Handle("POST /api/v1/websites/{id}/ssl/issue", guarded(rbac.PermSSLManage, h.issue))
	mux.Handle("POST /api/v1/websites/{id}/ssl/renew", guarded(rbac.PermSSLManage, h.renew))
	mux.Handle("POST /api/v1/websites/{id}/ssl/revoke", guarded(rbac.PermSSLManage, h.revoke))
	mux.Handle("PATCH /api/v1/websites/{id}/ssl", guarded(rbac.PermSSLManage, h.configure))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	certificates, err := h.repo.List(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	now := time.Now()
	expiring := 0
	for _, certificate := range certificates {
		if certificate.Status == StatusExpiring || certificate.Status == StatusExpired {
			expiring++
		}
	}

	httpx.OK(w, r, map[string]any{
		"certificates": decorate(certificates, now),
		"count":        len(certificates),
		// Surfaced separately so the page can lead with what needs attention
		// rather than making the reader count rows.
		"needing_attention": expiring,
	})
}

// providers reports which certificate providers this host can use.
func (h *Handler) providers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	capabilities, err := h.agent.SSLProviders(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		// A host the Agent cannot answer for still supports nothing worse than
		// self-signed, which needs no tooling at all.
		httpx.OK(w, r, map[string]any{
			"selfsigned":  true,
			"letsencrypt": false,
			"detail":      "the agent could not be reached, so only self-signed certificates are offered",
		})
		return
	}

	httpx.OK(w, r, capabilities)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	certificate, err := h.repo.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			// A site without a certificate is a valid configuration, so this is
			// 200 with enabled false rather than a 404 that would suggest the
			// website itself is missing.
			httpx.OK(w, r, map[string]any{"enabled": false})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, map[string]any{
		"enabled":     true,
		"certificate": decorateOne(certificate, time.Now()),
	})
}

// issueBody asks for a certificate.
type issueBody struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	// Staging uses Let's Encrypt's staging environment, which does not consume
	// the strict production rate limits while a deployment is being set up.
	Staging         bool  `json:"staging"`
	RedirectToHTTPS *bool `json:"https_redirect"`
	AutoRenew       *bool `json:"auto_renew"`
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body issueBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	request := IssueRequest{
		WebsiteID: id,
		Provider:  body.Provider,
		Email:     body.Email,
		Staging:   body.Staging,
		// Both default on: a certificate nobody redirects to is mostly
		// decorative, and one that does not renew fails in 90 days.
		RedirectToHTTPS: true,
		AutoRenew:       true,
		Actor:           actorFrom(r),
	}
	if body.RedirectToHTTPS != nil {
		request.RedirectToHTTPS = *body.RedirectToHTTPS
	}
	if body.AutoRenew != nil {
		request.AutoRenew = *body.AutoRenew
	}

	result, err := h.service.Issue(ctx, request)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// 202: the certificate does not exist yet, only the intent to obtain one.
	//
	// The DNS report rides along rather than being left in the log: names the
	// panel could not point at this host are the usual reason issuance fails,
	// and the moment to say so is while somebody is still looking at the
	// dialog they clicked Issue in.
	payload := map[string]any{"job": result.Job}
	if result.DNS != nil {
		payload["dns"] = result.DNS
	}
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{Success: true, Data: payload})
}

func (h *Handler) renew(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	job, err := h.service.Renew(ctx, id, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	accepted(w, r, job)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	job, err := h.service.Revoke(ctx, id, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	accepted(w, r, job)
}

// configureBody changes settings that need no new certificate.
type configureBody struct {
	AutoRenew       *bool `json:"auto_renew"`
	RedirectToHTTPS *bool `json:"https_redirect"`
}

func (h *Handler) configure(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body configureBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	job, err := h.service.Configure(ctx, ConfigureRequest{
		WebsiteID:       id,
		AutoRenew:       body.AutoRenew,
		RedirectToHTTPS: body.RedirectToHTTPS,
		Actor:           actorFrom(r),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// Only a redirect change rewrites the vhost, so only that produces a job.
	// Reporting 202 with no job would leave a client polling for nothing.
	if job == nil {
		httpx.OK(w, r, map[string]any{"updated": true})
		return
	}
	accepted(w, r, *job)
}

// decorate adds derived fields the UI needs on every certificate.
func decorate(certificates []Certificate, now time.Time) []map[string]any {
	out := make([]map[string]any, 0, len(certificates))
	for _, certificate := range certificates {
		out = append(out, decorateOne(certificate, now))
	}
	return out
}

func decorateOne(certificate Certificate, now time.Time) map[string]any {
	return map[string]any{
		"id":               certificate.ID,
		"website_id":       certificate.WebsiteID,
		"primary_domain":   certificate.PrimaryDomain,
		"provider":         certificate.Provider,
		"domains":          certificate.Domains,
		"issuer":           certificate.Issuer,
		"fingerprint":      certificate.Fingerprint,
		"issued_at":        certificate.IssuedAt,
		"expires_at":       certificate.ExpiresAt,
		"auto_renew":       certificate.AutoRenew,
		"status":           certificate.Status,
		"last_error":       certificate.LastError,
		"certificate_path": certificate.CertificatePath,
		// Computed here rather than in the browser so every client agrees on
		// what "12 days left" means, including one in another time zone.
		"days_remaining": certificate.DaysRemaining(now),
	}
}

func accepted(w http.ResponseWriter, r *http.Request, job any) {
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

// translate maps a domain error to its HTTP shape.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrNoCertificate):
		return httpx.NotFound("This website has no certificate")
	case errors.Is(err, websites.ErrNotFound):
		return httpx.NotFound("Website not found")
	case errors.Is(err, ErrWebsiteNotReady):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrProviderUnavailable):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrInvalidProvider):
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
