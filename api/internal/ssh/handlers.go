package ssh

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds a request. A configure call validates the whole
// configuration with sshd and restarts the server, which is a few seconds on a
// busy host.
const requestTimeout = 60 * time.Second

// maxBodyBytes bounds a request body. A public key is the largest thing here.
const maxBodyBytes = 16 << 10

// rootLoginValues are what PermitRootLogin may be set to, matching the Agent's
// own list. Checked here as well so a bad value is refused with a message about
// SSH rather than a round trip and a generic one.
var rootLoginValues = map[string]bool{
	"yes":                  true,
	"no":                   true,
	"prohibit-password":    true,
	"without-password":     true,
	"forced-commands-only": true,
}

// Handler serves the SSH endpoints.
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
// Reading needs server.view; every change needs server.manage. There is no
// finer permission than that in the panel's model, and this is not the place to
// invent one: changing sshd is authority over how the machine is administered,
// which is the broadest thing the panel does.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/security/ssh", guarded(rbac.PermServerView, h.status))
	mux.Handle("PATCH /api/v1/security/ssh", guarded(rbac.PermServerManage, h.configure))
	mux.Handle("GET /api/v1/security/ssh/keys", guarded(rbac.PermServerView, h.keys))
	mux.Handle("POST /api/v1/security/ssh/keys", guarded(rbac.PermServerManage, h.addKey))
	mux.Handle("DELETE /api/v1/security/ssh/keys/{fingerprint}",
		guarded(rbac.PermServerManage, h.removeKey))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	status, err := h.service.Status(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			// A host with no SSH server is a configuration, not a fault.
			httpx.OK(w, r, agentclient.SSHStatus{
				Config: agentclient.SSHConfig{
					Ports:  []int{},
					Reason: "the SSH server is not installed on this host",
				},
				Accounts: []agentclient.SSHAccount{},
				Findings: []agentclient.SSHFinding{},
			})
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	if status.Accounts == nil {
		status.Accounts = []agentclient.SSHAccount{}
	}
	if status.Findings == nil {
		status.Findings = []agentclient.SSHFinding{}
	}
	if status.Config.Ports == nil {
		status.Config.Ports = []int{}
	}
	httpx.OK(w, r, status)
}

// configureBody is the PATCH body.
//
// Pointers so an omitted field is left alone. A body of `{}` changes nothing
// and is refused rather than treated as a request to reset every setting.
type configureBody struct {
	Port                   *int    `json:"port"`
	RootLogin              *string `json:"root_login"`
	PasswordAuthentication *bool   `json:"password_authentication"`
	PubkeyAuthentication   *bool   `json:"pubkey_authentication"`
	PermitEmptyPasswords   *bool   `json:"permit_empty_passwords"`
	X11Forwarding          *bool   `json:"x11_forwarding"`
	MaxAuthTries           *int    `json:"max_auth_tries"`
}

func (h *Handler) configure(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body configureBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if body.RootLogin != nil {
		value := strings.ToLower(strings.TrimSpace(*body.RootLogin))
		if !rootLoginValues[value] {
			httpx.Error(w, r, httpx.ValidationFailed(
				"root_login must be yes, no, prohibit-password or forced-commands-only"))
			return
		}
		body.RootLogin = &value
	}
	if body.Port != nil && (*body.Port < 1 || *body.Port > 65535) {
		httpx.Error(w, r, httpx.ValidationFailed("port must be between 1 and 65535"))
		return
	}
	if body.MaxAuthTries != nil && (*body.MaxAuthTries < 1 || *body.MaxAuthTries > 100) {
		httpx.Error(w, r, httpx.ValidationFailed("max_auth_tries must be between 1 and 100"))
		return
	}

	result, err := h.service.Configure(ctx, httpx.RequestIDFromContext(ctx),
		agentclient.SSHChange{
			Port:                   body.Port,
			RootLogin:              body.RootLogin,
			PasswordAuthentication: body.PasswordAuthentication,
			PubkeyAuthentication:   body.PubkeyAuthentication,
			PermitEmptyPasswords:   body.PermitEmptyPasswords,
			X11Forwarding:          body.X11Forwarding,
			MaxAuthTries:           body.MaxAuthTries,
		}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

func (h *Handler) keys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	account := strings.TrimSpace(r.URL.Query().Get("account"))
	if account == "" {
		httpx.Error(w, r, httpx.BadRequest("an account is required"))
		return
	}

	list, err := h.service.Keys(ctx, httpx.RequestIDFromContext(ctx), account)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	if list.Keys == nil {
		list.Keys = []agentclient.SSHKey{}
	}
	httpx.OK(w, r, list)
}

// keyBody is the add-key body.
type keyBody struct {
	Account string `json:"account"`
	Key     string `json:"key"`
}

func (h *Handler) addKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body keyBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if strings.TrimSpace(body.Account) == "" {
		httpx.Error(w, r, httpx.BadRequest("an account is required"))
		return
	}
	if strings.TrimSpace(body.Key) == "" {
		httpx.Error(w, r, httpx.ValidationFailed("a public key is required"))
		return
	}

	key, err := h.service.AddKey(ctx, httpx.RequestIDFromContext(ctx),
		strings.TrimSpace(body.Account), body.Key, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, key)
}

// removeKey withdraws a key.
//
// The path segment is the fingerprint — API_SPEC section 26 calls it `:id`, and
// a fingerprint is the only identifier a key has that survives another key being
// removed. The account is a query parameter because the same key may be
// authorised for more than one of them.
func (h *Handler) removeKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	account := strings.TrimSpace(r.URL.Query().Get("account"))
	if account == "" {
		httpx.Error(w, r, httpx.BadRequest("an account is required"))
		return
	}

	fingerprint := r.PathValue("fingerprint")
	if fingerprint == "" {
		httpx.Error(w, r, httpx.BadRequest("a key fingerprint is required"))
		return
	}

	key, err := h.service.RemoveKey(ctx, httpx.RequestIDFromContext(ctx),
		account, fingerprint, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, key)
}

func decode(r *http.Request, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
	// An unknown field is a caller believing something about this API that is
	// not true, and silently ignoring it is how that belief survives.
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

// translate maps a failure to its HTTP shape.
//
// The Agent's own message is passed through for everything a person can act on.
// In this phase that matters more than usual: "no account on this host has an
// authorised SSH key, so turning off password authentication would leave no way
// in. Add a key first" is the whole value of the refusal, and a generic
// "conflict" would throw it away.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrNothingToDo):
		return httpx.ValidationFailed("No setting was given to change")
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict("This host has no SSH server, so there is nothing to configure")
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	case agentclient.IsInvalidRequest(err):
		return httpx.Conflict(agentclient.Message(err))
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
