package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// requestTimeout bounds an ordinary file request.
const requestTimeout = 30 * time.Second

// transferTimeout bounds an upload or a download, which move real data and
// legitimately take longer than a listing.
const transferTimeout = 30 * time.Minute

// maxBodyBytes bounds a JSON request body. Paths and flags only; file content
// travels through the upload endpoint, not through JSON.
const maxBodyBytes = 64 << 10

// Handler serves the file endpoints.
type Handler struct {
	service *Service
	agent   *agentclient.Client
	auth    *auth.Service
}

// HandlerOptions configures a Handler.
type HandlerOptions struct {
	Service *Service
	Agent   *agentclient.Client
	Auth    *auth.Service
}

// NewHandler builds a Handler.
func NewHandler(opts HandlerOptions) *Handler {
	return &Handler{service: opts.Service, agent: opts.Agent, auth: opts.Auth}
}

// Routes registers the endpoints on mux.
//
// Reads need file.read and every mutation needs file.write. The split matters:
// a viewer can look at a site's files without being able to change one, which
// is the difference between a support account and an administrator.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/files", guarded(rbac.PermFileRead, h.list))
	mux.Handle("GET /api/v1/files/stat", guarded(rbac.PermFileRead, h.stat))
	mux.Handle("GET /api/v1/files/download", guarded(rbac.PermFileRead, h.download))
	mux.Handle("GET /api/v1/files/search", guarded(rbac.PermFileRead, h.search))
	mux.Handle("GET /api/v1/files/content", guarded(rbac.PermFileRead, h.readContent))
	mux.Handle("PUT /api/v1/files/content", guarded(rbac.PermFileWrite, h.writeContent))

	mux.Handle("POST /api/v1/files/upload", guarded(rbac.PermFileWrite, h.upload))
	mux.Handle("POST /api/v1/files/folder", guarded(rbac.PermFileWrite, h.folder))
	mux.Handle("POST /api/v1/files/file", guarded(rbac.PermFileWrite, h.file))
	mux.Handle("POST /api/v1/files/copy", guarded(rbac.PermFileWrite, h.copy))
	mux.Handle("POST /api/v1/files/move", guarded(rbac.PermFileWrite, h.move))
	mux.Handle("POST /api/v1/files/zip", guarded(rbac.PermFileWrite, h.zip))
	mux.Handle("POST /api/v1/files/unzip", guarded(rbac.PermFileWrite, h.unzip))
	mux.Handle("PATCH /api/v1/files", guarded(rbac.PermFileWrite, h.patch))
	mux.Handle("DELETE /api/v1/files", guarded(rbac.PermFileWrite, h.delete))
}

// ----------------------------------------------------------------- reading

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	offset := queryInt(r, "offset", 0)
	limit := queryInt(r, "limit", 0)

	listing, err := h.agent.FileList(ctx, httpx.RequestIDFromContext(r.Context()), target, offset, limit)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}
	httpx.OK(w, r, listing)
}

func (h *Handler) stat(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	entry, err := h.agent.FileStat(ctx, httpx.RequestIDFromContext(r.Context()), target)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}
	httpx.OK(w, r, map[string]any{"entry": entry})
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		httpx.Error(w, r, httpx.ValidationFailed("A search needs something to search for"))
		return
	}

	result, err := h.agent.FileSearch(ctx, httpx.RequestIDFromContext(r.Context()),
		target, query, r.URL.Query().Get("content") == "true", queryInt(r, "limit", 0))
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}
	httpx.OK(w, r, result)
}

// download streams a file to the browser.
//
// The response is written chunk by chunk as the Agent produces it, so a large
// file costs one chunk of memory rather than its own size. Content-Length is
// therefore not set from a buffer: it comes from a stat before the transfer
// begins, and the transfer is what proves it.
func (h *Handler) download(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	requestID := httpx.RequestIDFromContext(ctx)
	entry, err := h.agent.FileStat(ctx, requestID, target)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}
	if entry.Type != "file" {
		httpx.Error(w, r, httpx.ValidationFailed("Only a regular file can be downloaded"))
		return
	}

	name := path.Base(entry.Path)
	// application/octet-stream and an attachment disposition, always. Serving a
	// customer's .html back with its own content type would run their markup on
	// the panel's origin, which is a stored cross-site scripting hole with the
	// session cookie sitting right there.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition",
		"attachment; filename*=UTF-8''"+urlEncode(name)+"; filename=\""+asciiName(name)+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(entry.Size, 10))
	w.Header().Set("Cache-Control", "private, no-store")

	written, err := h.service.Download(ctx, requestID, target, w)
	if err != nil {
		// The headers are already sent, so there is no way to turn this into a
		// clean error response. Logging it is all that is left, and truncating
		// the body is what tells the client something went wrong.
		slog.Default().Error("file download failed",
			"request_id", requestID, "path", target, "written", written,
			"error", err.Error())
		return
	}
}

// ----------------------------------------------------------------- content

// maxContentBodyBytes bounds a save.
//
// Larger than MaxEditableBytes because the content arrives JSON-encoded, and
// escaping can grow it: a file of quotes and newlines is meaningfully bigger on
// the wire than on disk. The real limit is checked on the decoded content.
const maxContentBodyBytes = 8 << 20

func (h *Handler) readContent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	content, err := h.service.ReadContent(ctx, httpx.RequestIDFromContext(ctx), target)
	if err != nil {
		httpx.Error(w, r, contentError(err))
		return
	}
	httpx.OK(w, r, content)
}

// writeContentRequest is a save from the editor.
type writeContentRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	// Checksum is what the editor loaded. Sent back so a second editor cannot
	// silently overwrite the first one's work.
	Checksum string `json:"checksum,omitempty"`
	// Force saves anyway. The panel asks first; it is never the default.
	Force bool `json:"force,omitempty"`
}

func (h *Handler) writeContent(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxContentBodyBytes))
	decoder.DisallowUnknownFields()

	var body writeContentRequest
	if err := decoder.Decode(&body); err != nil {
		httpx.Error(w, r, httpx.BadRequest("The request body is not valid JSON: "+err.Error()))
		return
	}

	target, err := ValidatePath(body.Path)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	saved, err := h.service.WriteContent(ctx, httpx.RequestIDFromContext(ctx),
		WriteContentRequest{
			Path:     target,
			Content:  body.Content,
			Checksum: body.Checksum,
			Force:    body.Force,
		}, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, contentError(err))
		return
	}
	httpx.OK(w, r, saved)
}

// contentError maps an editor failure onto an HTTP status.
func contentError(err error) error {
	switch {
	case errors.Is(err, ErrFileTooLarge):
		return httpx.ValidationFailed(
			"This file is too large to edit in the browser. Download it instead.")
	case errors.Is(err, ErrFileBinary):
		return httpx.ValidationFailed(
			"This file is not text, so editing it would corrupt it.")
	case errors.Is(err, ErrStaleWrite):
		// 409 rather than 422: nothing about the request is malformed, the
		// world moved underneath it.
		return httpx.Conflict(
			"This file changed since you opened it. Reload it, or save again to overwrite.")
	default:
		return agentError(err)
	}
}

// ----------------------------------------------------------------- writing

func (h *Handler) upload(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	directory, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") {
		httpx.Error(w, r, httpx.BadRequest("An upload must be sent as multipart/form-data"))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxUploadBytes)

	entry, err := h.service.UploadFromMultipart(ctx, httpx.RequestIDFromContext(ctx),
		directory, r, actorFrom(r))
	if err != nil {
		httpx.Error(w, r, uploadError(err))
		return
	}
	httpx.Created(w, r, map[string]any{"entry": entry})
}

// folderRequest creates a directory.
type folderRequest struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Parents bool   `json:"parents"`
}

func (h *Handler) folder(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body folderRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	target, err := resolveTarget(body.Path, body.Name)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	entry, err := h.agent.FileMkdir(ctx, httpx.RequestIDFromContext(ctx), target, body.Parents)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), ActionMkdir, target, nil)
	httpx.Created(w, r, map[string]any{"entry": entry})
}

// fileRequest creates an empty file.
type fileRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (h *Handler) file(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body fileRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	target, err := resolveTarget(body.Path, body.Name)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	entry, err := h.agent.FileCreate(ctx, httpx.RequestIDFromContext(ctx), target)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), ActionCreate, target, nil)
	httpx.Created(w, r, map[string]any{"entry": entry})
}

// transferRequest moves or copies one path to another.
type transferRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Overwrite   bool   `json:"overwrite"`
}

func (h *Handler) copy(w http.ResponseWriter, r *http.Request) {
	h.transfer(w, r, ActionCopy)
}

func (h *Handler) move(w http.ResponseWriter, r *http.Request) {
	h.transfer(w, r, ActionMove)
}

func (h *Handler) transfer(w http.ResponseWriter, r *http.Request, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	var body transferRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	source, err := ValidatePath(body.Source)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}
	destination, err := ValidatePath(body.Destination)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	requestID := httpx.RequestIDFromContext(ctx)
	var entry agentclient.FileEntry
	if action == ActionCopy {
		entry, err = h.agent.FileCopy(ctx, requestID, source, destination, body.Overwrite)
	} else {
		entry, err = h.agent.FileMove(ctx, requestID, source, destination, body.Overwrite)
	}
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), action, destination,
		map[string]any{"source": source})
	httpx.OK(w, r, map[string]any{"entry": entry})
}

// patchRequest changes a path: its name, or its permissions.
type patchRequest struct {
	Path string `json:"path"`
	// Name renames in place. It is a single segment, never a path.
	Name string `json:"name,omitempty"`
	// Mode is an octal permission string such as "0644".
	Mode      string `json:"mode,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
}

func (h *Handler) patch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body patchRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	target, err := ValidatePath(body.Path)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}
	if body.Name == "" && body.Mode == "" {
		httpx.Error(w, r, httpx.ValidationFailed(
			"Give a name to rename to, or a mode to change permissions to"))
		return
	}

	requestID := httpx.RequestIDFromContext(ctx)
	actor := actorFrom(r)
	entry := agentclient.FileEntry{}

	if body.Mode != "" {
		entry, err = h.agent.FileChmod(ctx, requestID, target, body.Mode, body.Recursive)
		if err != nil {
			httpx.Error(w, r, agentError(err))
			return
		}
		h.service.Record(ctx, actor, ActionChmod, target,
			map[string]any{"mode": body.Mode, "recursive": body.Recursive})
	}

	if body.Name != "" {
		name, nameErr := SafeName(body.Name)
		if nameErr != nil {
			httpx.Error(w, r, pathError(nameErr))
			return
		}
		// A rename is a move to a sibling. Doing it as a move means one place
		// enforces what a destination may be.
		destination := JoinPath(path.Dir(target), name)
		entry, err = h.agent.FileMove(ctx, requestID, target, destination, false)
		if err != nil {
			httpx.Error(w, r, agentError(err))
			return
		}
		h.service.Record(ctx, actor, ActionMove, destination,
			map[string]any{"source": target, "rename": true})
	}

	httpx.OK(w, r, map[string]any{"entry": entry})
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	target, err := ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}
	recursive := r.URL.Query().Get("recursive") == "true"

	if err := h.agent.FileDelete(ctx, httpx.RequestIDFromContext(ctx), target, recursive); err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), ActionDelete, target,
		map[string]any{"recursive": recursive})
	httpx.OK(w, r, map[string]any{"path": target, "deleted": true})
}

// zipRequest archives one or more paths.
type zipRequest struct {
	Sources     []string `json:"sources"`
	Destination string   `json:"destination"`
}

func (h *Handler) zip(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	var body zipRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(body.Sources) == 0 {
		httpx.Error(w, r, httpx.ValidationFailed("Select at least one file or folder to archive"))
		return
	}

	sources := make([]string, 0, len(body.Sources))
	for _, source := range body.Sources {
		clean, err := ValidatePath(source)
		if err != nil {
			httpx.Error(w, r, pathError(err))
			return
		}
		sources = append(sources, clean)
	}

	destination, err := ValidatePath(body.Destination)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	result, err := h.agent.FileArchive(ctx, httpx.RequestIDFromContext(ctx), sources, destination)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), ActionArchive, destination,
		map[string]any{"sources": len(sources), "entries": result.Entries})
	httpx.Created(w, r, result)
}

// unzipRequest extracts an archive.
type unzipRequest struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
}

func (h *Handler) unzip(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), transferTimeout)
	defer cancel()

	var body unzipRequest
	if err := decode(w, r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	archive, err := ValidatePath(body.Path)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}
	destination, err := ValidatePath(body.Destination)
	if err != nil {
		httpx.Error(w, r, pathError(err))
		return
	}

	result, err := h.agent.FileExtract(ctx, httpx.RequestIDFromContext(ctx), archive, destination)
	if err != nil {
		httpx.Error(w, r, agentError(err))
		return
	}

	h.service.Record(ctx, actorFrom(r), ActionExtract, destination,
		map[string]any{"archive": archive, "entries": result.Entries})
	httpx.OK(w, r, result)
}

// ----------------------------------------------------------------- helpers

// resolveTarget builds a child path from a directory and a name.
//
// The name is always required. Allowing it to be empty and falling back to the
// directory itself was the alternative, and it means an empty form field
// quietly creates — or reports having created — the parent instead of the thing
// the person was naming. A directory and a name is one shape, and one shape
// cannot be misread.
func resolveTarget(directory, name string) (string, error) {
	clean, err := ValidatePath(directory)
	if err != nil {
		return "", err
	}
	safe, err := SafeName(name)
	if err != nil {
		return "", err
	}
	return JoinPath(clean, safe), nil
}

// pathError maps a path validation failure onto an HTTP error.
func pathError(err error) error {
	switch {
	case errors.Is(err, ErrPathRequired):
		return httpx.ValidationFailed("A path is required")
	case errors.Is(err, ErrPathNotAbsolute):
		return httpx.ValidationFailed("The path must be absolute")
	case errors.Is(err, ErrPathTraversal):
		return httpx.ValidationFailed("The path must not contain '..'")
	case errors.Is(err, ErrPathNullByte):
		return httpx.ValidationFailed("The path contains an invalid character")
	case errors.Is(err, ErrPathTooLong):
		return httpx.ValidationFailed("The path is too long")
	case errors.Is(err, ErrNameInvalid):
		return httpx.ValidationFailed("A name must be a single file or folder name")
	default:
		return httpx.ValidationFailed("The path is not valid")
	}
}

// uploadError maps an upload failure onto an HTTP error.
func uploadError(err error) error {
	switch {
	case errors.Is(err, ErrNoFile):
		return httpx.ValidationFailed("The request contained no file")
	case errors.Is(err, ErrNameInvalid), errors.Is(err, ErrPathNullByte),
		errors.Is(err, ErrPathTooLong):
		return pathError(err)
	default:
		return agentError(err)
	}
}

// agentError maps an Agent refusal onto an HTTP status.
//
// The Agent already collapses "outside your root" into "not found", so the
// mapping here does not have to be careful about leaking layout; it only has to
// preserve the distinction the Agent chose to make.
func agentError(err error) error {
	var failure *agentclient.ErrOperationFailed
	if !errors.As(err, &failure) {
		return httpx.Internal(err)
	}

	switch failure.Code {
	case "NOT_FOUND":
		return httpx.NotFound(failure.Message)
	case "INVALID_PAYLOAD":
		return httpx.ValidationFailed(failure.Message)
	case "UNSUPPORTED":
		return httpx.Conflict(failure.Message)
	default:
		return httpx.Internal(fmt.Errorf("agent: %s: %s", failure.Code, failure.Message))
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

func queryInt(r *http.Request, name string, fallback int) int {
	value := r.URL.Query().Get(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

// urlEncode percent-encodes a filename for the RFC 5987 form of
// Content-Disposition, which is what carries a non-ASCII name correctly.
func urlEncode(name string) string {
	return url.PathEscape(name)
}

// asciiName reduces a filename to something safe for the legacy quoted form of
// Content-Disposition.
//
// A quote or a newline in this header lets a filename inject header content, so
// anything outside printable ASCII is replaced rather than escaped.
func asciiName(name string) string {
	var builder strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r > 0x7e, r == '"', r == '\\':
			builder.WriteByte('_')
		default:
			builder.WriteRune(r)
		}
	}
	if builder.Len() == 0 {
		return "download"
	}
	return builder.String()
}
