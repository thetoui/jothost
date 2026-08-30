package databases

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds a database request.
//
// It is longer than the SSL handlers' because these operations are synchronous:
// the request is held while the host actually runs the DDL. A DROP DATABASE on
// a large database is the slow case, and thirty seconds is generous for it
// while still bounded.
const requestTimeout = 30 * time.Second

// maxBodyBytes bounds a request body.
const maxBodyBytes = 16 << 10

// Handler serves the database endpoints.
type Handler struct {
	service *Service
	repo    *Repository
	auth    *auth.Service
}

// HandlerOptions configure a Handler.
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
// Reading is gated on database.manage rather than a weaker view permission,
// unlike websites. The reason is the password endpoint: a role that can list
// databases and their accounts is one step from a role that can read the
// credentials, and splitting the two would invite exactly that mistake later.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermDatabaseManage)(next))
	}

	mux.Handle("GET /api/v1/databases", guarded(h.list))
	mux.Handle("POST /api/v1/databases", guarded(h.create))
	mux.Handle("GET /api/v1/databases/engines", guarded(h.engines))
	mux.Handle("GET /api/v1/databases/{id}", guarded(h.get))
	mux.Handle("DELETE /api/v1/databases/{id}", guarded(h.delete))
	mux.Handle("POST /api/v1/databases/{id}/size", guarded(h.refreshSize))

	mux.Handle("GET /api/v1/databases/{id}/users", guarded(h.listUsers))
	mux.Handle("POST /api/v1/databases/{id}/users", guarded(h.addUser))
	mux.Handle("PATCH /api/v1/databases/{id}/users/{userId}", guarded(h.setGrant))

	mux.Handle("GET /api/v1/database-users", guarded(h.listAllUsers))
	mux.Handle("DELETE /api/v1/database-users/{id}", guarded(h.deleteUser))
	mux.Handle("PATCH /api/v1/database-users/{id}/password", guarded(h.setPassword))
	mux.Handle("GET /api/v1/database-users/{id}/password", guarded(h.revealPassword))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	records, err := h.repo.List(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	var total int64
	for _, record := range records {
		if record.SizeBytes != nil {
			total += *record.SizeBytes
		}
	}

	httpx.OK(w, r, map[string]any{
		"databases": records,
		"count":     len(records),
		// Surfaced separately so the page can lead with the number rather than
		// making the reader add up a column.
		"total_size_bytes": total,
	})
}

func (h *Handler) engines(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	engines, err := h.service.Engines(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		// A host the Agent cannot answer for runs nothing the panel can manage,
		// which is a state the page renders rather than an error it shows.
		httpx.OK(w, r, map[string]any{
			"engines":    []any{},
			"available":  false,
			"privileges": Privileges(),
			"detail":     "the agent could not be reached, so no database engine is offered",
		})
		return
	}

	httpx.OK(w, r, map[string]any{
		"engines":    engines.Engines,
		"available":  engines.Available,
		"privileges": Privileges(),
	})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	record, err := h.repo.Get(ctx, id)
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	users, err := h.repo.ListUsers(ctx, id)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}

	httpx.OK(w, r, map[string]any{"database": record, "users": users})
}

// createBody asks for a database.
type createBody struct {
	Name string `json:"name"`
	// Engine may be omitted on a host running one server, which is most of
	// them: making everybody name their only engine is a pointless question.
	Engine    string `json:"engine"`
	WebsiteID string `json:"website_id"`
	// CreateUser defaults to true. A database with no account cannot be used
	// by anything, so creating one alone is the unusual case, not the default.
	CreateUser *bool  `json:"create_user"`
	Username   string `json:"username"`
	Host       string `json:"host"`
	Password   string `json:"password"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body createBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	request := CreateRequest{
		Name:       body.Name,
		Engine:     body.Engine,
		WebsiteID:  body.WebsiteID,
		CreateUser: true,
		Username:   body.Username,
		Host:       body.Host,
		Password:   body.Password,
		Actor:      actorFrom(r),
		RequestID:  httpx.RequestIDFromContext(ctx),
	}
	if body.CreateUser != nil {
		request.CreateUser = *body.CreateUser
	}

	result, err := h.service.Create(ctx, request)
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	// 201, not 202: the database exists on the host by the time this is
	// written, so there is nothing for the caller to wait for.
	httpx.Created(w, r, userPayload(result.Database, result.User, result.Password))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.Delete(ctx, httpx.RequestIDFromContext(ctx), id, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

func (h *Handler) refreshSize(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	record, err := h.service.RefreshSize(ctx, httpx.RequestIDFromContext(ctx), id)
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"database": record})
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	users, err := h.repo.ListUsers(ctx, id)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.OK(w, r, map[string]any{"users": users, "count": len(users)})
}

func (h *Handler) listAllUsers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	users, err := h.repo.ListAllUsers(ctx)
	if err != nil {
		httpx.Error(w, r, httpx.Internal(err))
		return
	}
	httpx.OK(w, r, map[string]any{"users": users, "count": len(users)})
}

// addUserBody asks for an account on a database.
type addUserBody struct {
	Username  string `json:"username"`
	Host      string `json:"host"`
	Password  string `json:"password"`
	Privilege string `json:"privilege"`
}

func (h *Handler) addUser(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var body addUserBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	user, password, err := h.service.AddUser(ctx, AddUserRequest{
		DatabaseID: id,
		Username:   body.Username,
		Host:       body.Host,
		Password:   body.Password,
		Privilege:  body.Privilege,
		Actor:      actorFrom(r),
		RequestID:  httpx.RequestIDFromContext(ctx),
	})
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	httpx.Created(w, r, map[string]any{"user": user, "password": password})
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	if err := h.service.DeleteUser(ctx, httpx.RequestIDFromContext(ctx), id, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// passwordBody changes an account's password.
type passwordBody struct {
	// An empty password asks the host to generate one, which is the path the
	// panel's "regenerate" button takes.
	Password string `json:"password"`
}

func (h *Handler) setPassword(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	var body passwordBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	password, err := h.service.SetPassword(ctx, httpx.RequestIDFromContext(ctx),
		id, body.Password, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"password": password, "changed": true})
}

func (h *Handler) revealPassword(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}

	password, err := h.service.RevealPassword(ctx, id, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}

	// No-store, because this response body is a working credential and a
	// browser or proxy cache holding it is a copy nobody is tracking.
	w.Header().Set("Cache-Control", "no-store")
	httpx.OK(w, r, map[string]any{"password": password})
}

// grantBody changes an account's access to a database.
type grantBody struct {
	// An empty privilege revokes.
	Privilege string `json:"privilege"`
}

func (h *Handler) setGrant(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	userID, ok := pathUUID(w, r, "userId")
	if !ok {
		return
	}

	var body grantBody
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	if err := h.service.SetGrant(ctx, httpx.RequestIDFromContext(ctx),
		id, userID, body.Privilege, actorFrom(r)); err != nil {
		httpx.Error(w, r, Translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"privilege": body.Privilege, "updated": true})
}

// userPayload renders a created database with its account.
func userPayload(database Database, user *User, password string) map[string]any {
	payload := map[string]any{"database": database}
	if user != nil {
		payload["user"] = *user
		payload["password"] = password
	}
	return payload
}

// decode reads a JSON body, rejecting unknown fields.
//
// A misspelled "privilege" that was ignored would silently grant the default
// level to somebody, so an unrecognised field is an error rather than noise.
func decode(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(dst); err != nil {
		return httpx.BadRequest("The request body is not valid JSON: " + err.Error())
	}
	return nil
}

// pathUUID reads and validates a UUID path value.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value := r.PathValue(name)
	if !isUUID(value) {
		httpx.Error(w, r, httpx.BadRequest(name+" must be a UUID"))
		return "", false
	}
	return value, true
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
