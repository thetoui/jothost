package mail

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
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading is a handful of calls to local daemons; a reconcile writes
// files and may restart two of them; installing pulls packages.
const (
	requestTimeout = 60 * time.Second
	changeTimeout  = 3 * time.Minute
	installTimeout = 20 * time.Minute
)

// maxBodyBytes bounds a request body.
//
// Larger than most of the panel's, because an autoresponder message is a
// paragraph a person wrote and 16 KiB would truncate a long one — which would
// arrive as a validation failure on text the customer can see is short enough.
const maxBodyBytes = 64 << 10

// Handler serves the mail endpoints.
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
// Reading needs mail.view and every change needs mail.manage, which migration
// 0021 adds. Two permissions rather than one, and the split is the point: a
// mailbox holds a customer's correspondence, so seeing that one exists is
// support work while being able to set its password is being able to read every
// message in it. Those are not the same act.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/mail", guarded(rbac.PermMailView, h.overview))
	mux.Handle("PUT /api/v1/mail/settings", guarded(rbac.PermMailManage, h.settings))
	mux.Handle("POST /api/v1/mail/install", guarded(rbac.PermMailManage, h.install))

	mux.Handle("GET /api/v1/mail/domains", guarded(rbac.PermMailView, h.listDomains))
	mux.Handle("POST /api/v1/mail/domains", guarded(rbac.PermMailManage, h.createDomain))
	mux.Handle("PATCH /api/v1/mail/domains/{id}", guarded(rbac.PermMailManage, h.updateDomain))
	mux.Handle("DELETE /api/v1/mail/domains/{id}", guarded(rbac.PermMailManage, h.deleteDomain))
	mux.Handle("POST /api/v1/mail/domains/{id}/dkim", guarded(rbac.PermMailManage, h.rotateDKIM))

	mux.Handle("GET /api/v1/mail/domains/{id}/mailboxes",
		guarded(rbac.PermMailView, h.listMailboxes))
	mux.Handle("POST /api/v1/mail/domains/{id}/mailboxes",
		guarded(rbac.PermMailManage, h.createMailbox))
	mux.Handle("PATCH /api/v1/mail/mailboxes/{id}", guarded(rbac.PermMailManage, h.updateMailbox))
	mux.Handle("DELETE /api/v1/mail/mailboxes/{id}", guarded(rbac.PermMailManage, h.deleteMailbox))
	mux.Handle("PUT /api/v1/mail/mailboxes/{id}/password",
		guarded(rbac.PermMailManage, h.setPassword))

	mux.Handle("PUT /api/v1/mail/mailboxes/{id}/autoresponder",
		guarded(rbac.PermMailManage, h.setResponder))
	mux.Handle("DELETE /api/v1/mail/mailboxes/{id}/autoresponder",
		guarded(rbac.PermMailManage, h.clearResponder))

	mux.Handle("GET /api/v1/mail/domains/{id}/aliases", guarded(rbac.PermMailView, h.listAliases))
	mux.Handle("POST /api/v1/mail/domains/{id}/aliases",
		guarded(rbac.PermMailManage, h.createAlias))
	mux.Handle("DELETE /api/v1/mail/aliases/{id}", guarded(rbac.PermMailManage, h.deleteAlias))

	mux.Handle("POST /api/v1/mail/webmail", guarded(rbac.PermMailManage, h.installWebmail))
	mux.Handle("DELETE /api/v1/mail/webmail", guarded(rbac.PermMailManage, h.removeWebmail))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, overview)
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	var body Settings
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	saved, err := h.service.Configure(ctx, h.actor(r), httpx.RequestIDFromContext(ctx), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, saved)
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Filtering bool `json:"filtering"`
		Antivirus bool `json:"antivirus"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	// Queueing is quick; the install itself is the job's problem. The long
	// timeout that used to be here could never take effect, because the HTTP
	// server closes the connection after API_WRITE_TIMEOUT whatever the
	// handler is doing.
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	job, err := h.service.Install(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		body.Filtering, body.Antivirus)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	// 202: nothing is installed yet, only the intent to install it.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{
		Success: true,
		Data:    map[string]any{"job": job},
	})
}

func (h *Handler) listDomains(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, map[string]any{"domains": overview.Domains})
}

func (h *Handler) createDomain(w http.ResponseWriter, r *http.Request) {
	var body DomainRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	domain, err := h.service.CreateDomain(ctx, h.actor(r), httpx.RequestIDFromContext(ctx), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Created(w, r, domain)
}

func (h *Handler) updateDomain(w http.ResponseWriter, r *http.Request) {
	var body DomainRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	domain, err := h.service.UpdateDomain(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, domain)
}

func (h *Handler) deleteDomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.DeleteDomain(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) rotateDKIM(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	domain, err := h.service.RotateDKIM(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, domain)
}

func (h *Handler) listMailboxes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	boxes, err := h.service.ListMailboxes(ctx, httpx.RequestIDFromContext(ctx),
		r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, map[string]any{"mailboxes": boxes})
}

func (h *Handler) createMailbox(w http.ResponseWriter, r *http.Request) {
	var body MailboxRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	box, err := h.service.CreateMailbox(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Created(w, r, box)
}

func (h *Handler) updateMailbox(w http.ResponseWriter, r *http.Request) {
	var body MailboxRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	box, err := h.service.UpdateMailbox(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, box)
}

func (h *Handler) deleteMailbox(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.DeleteMailbox(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.SetPassword(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body.Password); err != nil {
		h.fail(w, r, err)
		return
	}
	// No body. There is nothing useful to return, and returning the mailbox
	// would put its row — and one day, by accident, its hash — in a reply to a
	// request that carried a password.
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setResponder(w http.ResponseWriter, r *http.Request) {
	var body ResponderRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	responder, err := h.service.SetAutoresponder(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx), r.PathValue("id"), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, responder)
}

func (h *Handler) clearResponder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.ClearAutoresponder(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx), r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listAliases(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	aliases, err := h.service.repo.ListAliases(ctx, r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.OK(w, r, map[string]any{"aliases": aliases})
}

func (h *Handler) createAlias(w http.ResponseWriter, r *http.Request) {
	var body AliasRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	alias, err := h.service.CreateAlias(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.Created(w, r, alias)
}

func (h *Handler) deleteAlias(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.DeleteAlias(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) installWebmail(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebsiteID string `json:"website_id"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	result, err := h.service.InstallWebmail(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		body.WebsiteID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{Success: true, Data: result})
}

func (h *Handler) removeWebmail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.RemoveWebmail(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx)); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decode reads a JSON body, answering 400 and returning false if it cannot.
func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	// Unknown fields are refused rather than ignored: a typo in a field name
	// would otherwise be a setting silently not applied, which is exactly the
	// class of failure this phase is about.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		httpx.Error(w, r, httpx.BadRequest(
			"The request body could not be read: "+err.Error()))
		return false
	}
	return true
}

// actor is who made the request.
func (h *Handler) actor(r *http.Request) Actor {
	claims, _ := auth.ClaimsFromContext(r.Context())
	return Actor{
		UserID:    claims.UserID,
		IPAddress: clientIP(r),
		UserAgent: r.UserAgent(),
	}
}

// clientIP is the address the request came from.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// fail maps a service error onto its HTTP shape.
//
// Each case is a different thing for the person reading it to do, which is why
// they are not collapsed: "that domain already has a mailbox called sales" and
// "the mail server is not installed" both fail, and only one of them is
// something the operator can fix in the form in front of them.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	httpx.Error(w, r, translate(err))
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
	case errors.Is(err, ErrDuplicate):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(ErrUnavailable.Error())
	case errors.Is(err, ErrWeakPassword), errors.Is(err, ErrNoHostname),
		errors.Is(err, ErrNoWebsite):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidMailbox),
		errors.Is(err, validate.ErrInvalidMailDomain),
		errors.Is(err, validate.ErrInvalidMailPolicy),
		errors.Is(err, validate.ErrInvalidMailSetting),
		errors.Is(err, validate.ErrInvalidDomain):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		// The daemons' own refusals arrive here — "Postfix rejected this
		// configuration: ..." — and they are passed through rather than
		// replaced. Hiding them would leave an operator with a change that did
		// not apply and no way to find out why, and they contain no secrets:
		// nothing in this package puts one in an error.
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
