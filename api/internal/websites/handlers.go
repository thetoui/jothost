package websites

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// requestTimeout bounds a website request.
//
// These endpoints only read the database and queue work; the provisioning
// itself runs in the worker, so no request here waits on the host.
const requestTimeout = 10 * time.Second

// maxBodyBytes bounds a request body. These payloads are a few short strings;
// anything larger is a mistake or an attempt to exhaust memory.
const maxBodyBytes = 16 << 10

// Handler serves the website and domain endpoints.
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
// Each verb carries its own permission rather than one blanket "website"
// right: viewing which sites exist is very different from deleting one.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/websites", guarded(rbac.PermWebsiteView, h.list))
	mux.Handle("POST /api/v1/websites", guarded(rbac.PermWebsiteCreate, h.create))
	mux.Handle("GET /api/v1/websites/{id}", guarded(rbac.PermWebsiteView, h.get))
	mux.Handle("PATCH /api/v1/websites/{id}", guarded(rbac.PermWebsiteUpdate, h.update))
	mux.Handle("DELETE /api/v1/websites/{id}", guarded(rbac.PermWebsiteDelete, h.delete))

	// A subdomain is a website, so creating one is a website.create right and
	// removing one is website.delete: the same authority, over the same kind
	// of thing.
	mux.Handle("GET /api/v1/websites/{id}/subdomains",
		guarded(rbac.PermWebsiteView, h.listSubdomains))
	mux.Handle("POST /api/v1/websites/{id}/subdomains",
		guarded(rbac.PermWebsiteCreate, h.createSubdomain))
	mux.Handle("DELETE /api/v1/subdomains/{id}",
		guarded(rbac.PermWebsiteDelete, h.deleteSubdomain))

	mux.Handle("GET /api/v1/websites/{id}/domains", guarded(rbac.PermWebsiteView, h.listDomains))
	mux.Handle("POST /api/v1/websites/{id}/domains", guarded(rbac.PermWebsiteUpdate, h.addDomain))
	mux.Handle("DELETE /api/v1/domains/{id}", guarded(rbac.PermWebsiteUpdate, h.removeDomain))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	params := ListParams{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		// Subdomains are left out unless asked for: this listing is what the
		// websites page shows, and a site with twenty subdomains would
		// otherwise fill it on its own.
		IncludeSubdomains: r.URL.Query().Get("include_subdomains") == "true",
	}
	if params.Status != "" && !validStatus(params.Status) {
		httpx.Error(w, r, httpx.BadRequest("status is not a valid website status"))
		return
	}

	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit <= 0 {
			httpx.Error(w, r, httpx.BadRequest("limit must be a positive integer"))
			return
		}
		params.Limit = limit
	}

	sites, err := h.repo.List(ctx, params)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, map[string]any{"websites": sites, "count": len(sites)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	site, err := h.repo.Get(ctx, id)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, site)
}

// createBody is the request body for creating a website.
type createBody struct {
	Domain string `json:"domain"`
	Name   string `json:"name"`
	// SSLEnabled is accepted so the API can reject it explicitly. Ignoring an
	// unknown field would let a client believe SSL was configured.
	SSLEnabled bool `json:"ssl_enabled"`
	// SubscriptionID puts the new site inside a customer's subscription.
	//
	// Optional, and empty means the creator's own subscription — or none at
	// all, which is what an administrator's sites have. It is also what the
	// quota guard reads to decide whose plan this creation spends, so a
	// reseller creating a site for a customer is charged to the customer.
	SubscriptionID string `json:"subscription_id"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	var body createBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	result, err := h.service.Create(ctx, CreateRequest{
		Domain:         body.Domain,
		Name:           body.Name,
		SSLEnabled:     body.SSLEnabled,
		SubscriptionID: body.SubscriptionID,
		Actor:          claims.UserID,
		IPAddress:      clientIP(r),
		UserAgent:      r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// 201 with the job attached: the row exists now, the site does not yet.
	// The client polls the job to learn when it does.
	httpx.Created(w, r, result)
}

// updateBody is the request body for changing a website.
type updateBody struct {
	Name          *string `json:"name"`
	HTTPSRedirect *bool   `json:"https_redirect"`
	// AllowOverride turns .htaccess on or off for this site. It changes the
	// Apache vhost, so it is applied through the same rewrite every other
	// configuration change goes through rather than by editing a file.
	AllowOverride *bool `json:"allow_override"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body updateBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if body.HTTPSRedirect != nil && *body.HTTPSRedirect {
		// Redirecting to HTTPS before a certificate exists takes the site
		// offline, so it is refused rather than obeyed.
		httpx.Error(w, r, httpx.BadRequest(
			"https_redirect requires SSL, which is not available yet"))
		return
	}

	site, err := h.service.Update(ctx, id, UpdateParams{
		Name:          body.Name,
		HTTPSRedirect: body.HTTPSRedirect,
		AllowOverride: body.AllowOverride,
	}, UpdateActor{
		Actor:     actorID(r),
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, site)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	job, err := h.service.Delete(ctx, DeleteRequest{
		WebsiteID: id,
		Actor:     claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	// 202, not 204: the site is not gone yet, only scheduled to be.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

// subdomainBody is the request body for creating a subdomain.
type subdomainBody struct {
	// Name is the label beneath the parent, not a full hostname: "shop", or
	// "dev.shop", or "*". Taking a label rather than a hostname is what makes
	// it impossible to create a site under a domain the parent does not own —
	// the full name is derived here, from the parent's own record.
	Name             string `json:"name"`
	DocumentRootMode string `json:"document_root_mode"`
	PHPPoolMode      string `json:"php_pool_mode"`
	SystemUserMode   string `json:"system_user_mode"`
}

func (h *Handler) listSubdomains(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	subdomains, err := h.service.ListSubdomains(ctx, id)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.OK(w, r, map[string]any{"subdomains": subdomains, "count": len(subdomains)})
}

func (h *Handler) createSubdomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body subdomainBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	result, err := h.service.CreateSubdomain(ctx, CreateSubdomainRequest{
		ParentID:         id,
		Name:             body.Name,
		DocumentRootMode: body.DocumentRootMode,
		PHPPoolMode:      body.PHPPoolMode,
		SystemUserMode:   body.SystemUserMode,
		Actor:            claims.UserID,
		IPAddress:        clientIP(r),
		UserAgent:        r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.Created(w, r, result)
}

func (h *Handler) deleteSubdomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	job, err := h.service.DeleteSubdomain(ctx, DeleteRequest{
		WebsiteID: id,
		Actor:     claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

func (h *Handler) listDomains(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	// The website is fetched first so an unknown id is a 404 rather than an
	// empty list, which would suggest the site exists with no domains.
	if _, err := h.repo.Get(ctx, id); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	domains, err := h.repo.ListDomains(ctx, id)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, map[string]any{"domains": domains, "count": len(domains)})
}

// domainBody is the request body for attaching a hostname.
type domainBody struct {
	Domain     string `json:"domain"`
	Type       string `json:"type"`
	RedirectTo string `json:"redirect_to"`
}

func (h *Handler) addDomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	var body domainBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	domain, job, err := h.service.AddDomain(ctx, AddDomainRequest{
		WebsiteID:  id,
		Domain:     body.Domain,
		Type:       body.Type,
		RedirectTo: body.RedirectTo,
		Actor:      claims.UserID,
		IPAddress:  clientIP(r),
		UserAgent:  r.UserAgent(),
	})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}

	httpx.Created(w, r, map[string]any{"domain": domain, "job": job})
}

func (h *Handler) removeDomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := withTimeout(r)
	defer cancel()

	id := r.PathValue("id")
	if !isUUID(id) {
		httpx.Error(w, r, httpx.BadRequest("id must be a UUID"))
		return
	}

	claims, _ := auth.ClaimsFromContext(r.Context())

	job, err := h.service.RemoveDomain(ctx, RemoveDomainRequest{
		DomainID:  id,
		Actor:     claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	})
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
//
// Anything unrecognised becomes a 500 with the detail kept internal, so a
// database message never reaches a client.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotFound), errors.Is(err, jobs.ErrNotFound):
		return httpx.NotFound("Website not found")
	case errors.Is(err, ErrDomainTaken):
		return httpx.Conflict("That domain is already in use by another website")
	case errors.Is(err, ErrUserTaken):
		return httpx.Conflict("That system user is already in use")
	case errors.Is(err, ErrPrimaryDomain):
		return httpx.BadRequest(
			"The primary domain cannot be removed; delete the website instead")
	case errors.Is(err, ErrSSLUnsupported):
		return httpx.BadRequest("SSL is not available yet")
	case errors.Is(err, ErrRedirectUnsupported):
		return httpx.BadRequest("Redirect domains are not available yet")
	case errors.Is(err, ErrInvalidState), errors.Is(err, ErrParentNotReady):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrHasSubdomains):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrNestedSubdomain):
		return httpx.BadRequest(err.Error())
	case errors.Is(err, ErrNotSubdomain):
		return httpx.BadRequest(
			"That website is not a subdomain; delete it as a website instead")
	case errors.Is(err, validate.ErrInvalidSubdomain),
		errors.Is(err, validate.ErrInvalidDocumentRootMode),
		errors.Is(err, validate.ErrInvalidPHPPoolMode),
		errors.Is(err, validate.ErrInvalidSystemUserMode):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidDomain),
		errors.Is(err, validate.ErrInvalidSystemUser):
		return httpx.ValidationFailed(err.Error())
	default:
		return httpx.Internal(err)
	}
}

// decode reads a JSON body, rejecting unknown fields.
//
// Strictness matters here: a client that misspells "domain" should be told,
// not have a website created with an empty one.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return httpx.BadRequest("The request body is not valid JSON: " + err.Error())
	}
	return nil
}

func validStatus(status string) bool {
	switch status {
	case StatusCreating, StatusActive, StatusSuspended, StatusFailed, StatusDeleting:
		return true
	default:
		return false
	}
}

// actorID returns the user making the request, for the audit trail.
func actorID(r *http.Request) string {
	claims, _ := auth.ClaimsFromContext(r.Context())
	return claims.UserID
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
