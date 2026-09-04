package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading asks the Agent about a working tree; a change writes a row
// and queues a job. Nothing here waits for a deployment: that is what the queue
// is for.
const (
	requestTimeout = 60 * time.Second
	changeTimeout  = 2 * time.Minute
)

// maxBodyBytes bounds an authenticated request body.
//
// Larger than most of the panel's, because a deployment script is a shell
// script somebody wrote and 16 KiB would truncate a long one — which would
// arrive as a validation failure on text the operator can see is short enough.
const maxBodyBytes = 128 << 10

// Handler serves the deployment endpoints.
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
// Every route but one needs authentication. The exception is the webhook, which
// is called by a forge and cannot carry a session — it authenticates with an
// HMAC over its own body instead, and webhook.go sets out exactly what that
// does and does not protect.
//
// Reading needs deploy.view. Changing needs deploy.manage, and so does reading
// a deployment's log: a build prints whatever the build printed, which
// regularly includes a token in a URL.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/deployments", guarded(rbac.PermDeployView, h.overview))
	mux.Handle("POST /api/v1/deployments/repositories",
		guarded(rbac.PermDeployManage, h.configure))
	mux.Handle("GET /api/v1/deployments/repositories/{id}",
		guarded(rbac.PermDeployView, h.detail))
	mux.Handle("DELETE /api/v1/deployments/repositories/{id}",
		guarded(rbac.PermDeployManage, h.remove))
	mux.Handle("PUT /api/v1/deployments/repositories/{id}/actions",
		guarded(rbac.PermDeployManage, h.setActions))
	mux.Handle("POST /api/v1/deployments/repositories/{id}/key",
		guarded(rbac.PermDeployManage, h.generateKey))
	mux.Handle("POST /api/v1/deployments/repositories/{id}/deploy",
		guarded(rbac.PermDeployManage, h.deploy))
	mux.Handle("POST /api/v1/deployments/repositories/{id}/rollback",
		guarded(rbac.PermDeployManage, h.rollback))

	// Under "runs" rather than directly under "deployments": a bare
	// "/deployments/{id}" collides with "/deployments/repositories/{id}" —
	// Go's router refuses to guess which of the two a request for
	// "/deployments/repositories" means, and it is right to.
	mux.Handle("GET /api/v1/deployments/runs/{id}", guarded(rbac.PermDeployView, h.deployment))
	mux.Handle("GET /api/v1/deployments/runs/{id}/log", guarded(rbac.PermDeployManage, h.log))

	// The webhook. No authentication middleware, deliberately: see webhook.go.
	mux.HandleFunc("POST /api/v1/webhooks/deploy/{token}", h.webhook)
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	repositories, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"repositories": repositories})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	view, err := h.service.Detail(ctx, httpx.RequestIDFromContext(ctx), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, view)
}

func (h *Handler) configure(w http.ResponseWriter, r *http.Request) {
	var body RepositoryRequest
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	repository, err := h.service.Configure(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx), body)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, repository)
}

func (h *Handler) remove(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	if err := h.service.Remove(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) setActions(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Steps []string `json:"steps"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	actions, err := h.service.SetActions(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx), r.PathValue("id"), body.Steps)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"actions": actions})
}

func (h *Handler) generateKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	repository, err := h.service.GenerateKey(ctx, h.actor(r),
		httpx.RequestIDFromContext(ctx), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, repository)
}

func (h *Handler) deploy(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Commit string `json:"commit"`
	}
	if r.ContentLength > 0 && !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	deployment, err := h.service.Deploy(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), validate.TriggerManual, body.Commit)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	// Accepted rather than OK: the deployment is queued, and the reply is how
	// the page finds the row to follow.
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{Success: true, Data: deployment})
}

// rollback deploys the commit the website was on before its last deployment.
//
// A deployment rather than an undo, which is the honest shape: it checks out an
// older commit and runs the same steps. What it cannot do is un-run a
// migration or remove a file a build wrote, and the panel says so rather than
// implying otherwise.
func (h *Handler) rollback(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Commit string `json:"commit"`
	}
	if !h.decode(w, r, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	deployment, err := h.service.Deploy(ctx, h.actor(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), validate.TriggerRollback, body.Commit)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.WriteJSON(w, r, http.StatusAccepted, httpx.Envelope{Success: true, Data: deployment})
}

func (h *Handler) deployment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	deployment, err := h.service.DeploymentLog(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	// Without the log. Seeing that a deployment failed is deploy.view; reading
	// what it printed is deploy.manage, on the route below.
	deployment.Log = ""
	httpx.OK(w, r, deployment)
}

func (h *Handler) log(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	deployment, err := h.service.DeploymentLog(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"id":        deployment.ID,
		"status":    deployment.Status,
		"log":       deployment.Log,
		"truncated": deployment.LogTruncated,
	})
}

// webhook receives a push from a forge.
//
// The one unauthenticated route in the panel that causes code to run. Three
// things about how it is written matter:
//
//   - The body is read under a hard cap, once, and verified before it is
//     parsed. Parsing first would mean deciding what a request means before
//     knowing whether to believe it.
//   - Every failure answers the same way. An unauthenticated caller learns
//     whether their signature was right and nothing else — not whether the
//     token exists, not which repository it names, not what branch it deploys.
//   - The reply never carries a repository, a website, or a path.
func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), changeTimeout)
	defer cancel()

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhookBody+1))
	if err != nil {
		httpx.Error(w, r, httpx.BadRequest("The request body could not be read."))
		return
	}
	if len(body) > MaxWebhookBody {
		httpx.Error(w, r, httpx.BadRequest("That payload is larger than this panel accepts."))
		return
	}

	result, err := h.service.Webhook(ctx, httpx.RequestIDFromContext(ctx), WebhookRequest{
		Token: r.PathValue("token"),
		Body:  body,
		// Whichever header the forge used. Both are read rather than one
		// chosen from the configured provider, because a repository configured
		// as "generic" may be behind anything.
		Signature: firstHeader(r,
			"X-Hub-Signature-256", "X-Gitlab-Token", "X-Signature-256"),
		Event: firstHeader(r, "X-GitHub-Event", "X-Gitlab-Event"),
	})
	if err != nil {
		if errors.Is(err, ErrUnverified) || errors.Is(err, ErrNoSignature) {
			// One answer for every way of failing to authenticate.
			httpx.Error(w, r, httpx.Unauthorized("This request could not be verified."))
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}

	status := http.StatusOK
	if result.Accepted {
		status = http.StatusAccepted
	}
	httpx.WriteJSON(w, r, status, httpx.Envelope{Success: true, Data: result})
}

// firstHeader returns the first of these headers that is set.
func firstHeader(r *http.Request, names ...string) string {
	for _, name := range names {
		if value := r.Header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

// decode reads a JSON body.
func (h *Handler) decode(w http.ResponseWriter, r *http.Request, target any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// translate maps a failure to its HTTP shape.
//
// ErrInProgress is a 409 rather than a 500, and that is the case worth being
// deliberate about: two deployments at once is not a fault, it is somebody
// pressing the button twice or a push arriving mid-build, and the right answer
// is to say so.
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
	case errors.Is(err, ErrInProgress):
		return httpx.Conflict(ErrInProgress.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(ErrUnavailable.Error())
	case errors.Is(err, ErrNoAccount), errors.Is(err, ErrNoWebhook):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidRemote),
		errors.Is(err, validate.ErrInvalidBranch),
		errors.Is(err, validate.ErrInvalidAction),
		errors.Is(err, validate.ErrInvalidScript):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
