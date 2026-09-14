package dns

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/rbac"
)

// The template endpoints.
//
// Reading needs server.view, writing needs dns.manage — the same split as the
// rest of this package. A template decides what every new domain on the host
// starts with, which is not a per-zone decision.

// TemplateRoutes registers them.
func (h *Handler) TemplateRoutes(mux *http.ServeMux) {
	read := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermServerView)(next))
	}
	manage := func(next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(rbac.PermDNSManage)(next))
	}

	mux.Handle("GET /api/v1/dns/templates", read(h.listTemplates))
	mux.Handle("POST /api/v1/dns/templates", manage(h.createTemplate))
	mux.Handle("GET /api/v1/dns/templates/{id}", read(h.getTemplate))
	mux.Handle("PUT /api/v1/dns/templates/{id}", manage(h.updateTemplate))
	mux.Handle("DELETE /api/v1/dns/templates/{id}", manage(h.deleteTemplate))
}

// templateBody is a template arriving from a client.
//
// The records are the whole list, not a patch. A template is a list and
// replacing it whole means an edit that reorders or removes lines needs no
// vocabulary of its own.
type templateBody struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	IsDefault   bool                 `json:"is_default"`
	Records     []templateRecordBody `json:"records"`
}

type templateRecordBody struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
}

func (b templateBody) input() TemplateInput {
	records := make([]TemplateRecord, 0, len(b.Records))
	for _, record := range b.Records {
		records = append(records, TemplateRecord{
			Name:     strings.TrimSpace(record.Name),
			Type:     strings.ToUpper(strings.TrimSpace(record.Type)),
			TTL:      record.TTL,
			Value:    strings.TrimSpace(record.Value),
			Priority: record.Priority,
			Weight:   record.Weight,
			Port:     record.Port,
		})
	}
	return TemplateInput{
		Name:        strings.TrimSpace(b.Name),
		Description: strings.TrimSpace(b.Description),
		IsDefault:   b.IsDefault,
		Records:     records,
	}
}

func (h *Handler) listTemplates(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	templates, err := h.service.repo.Templates(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{
		"templates": templates,
		// Named here so a client does not have to know them. A form offering
		// the wrong placeholder is a template that silently produces a broken
		// zone weeks later.
		"placeholders": []map[string]string{
			{"token": PlaceholderDomain, "means": "the domain the zone is for"},
			{"token": PlaceholderIP, "means": "this host's own address"},
		},
	})
}

func (h *Handler) getTemplate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	template, err := h.service.repo.Template(ctx, h.service.serverID, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translateTemplate(err))
		return
	}
	httpx.OK(w, r, template)
}

func (h *Handler) createTemplate(w http.ResponseWriter, r *http.Request) {
	h.saveTemplate(w, r, "")
}

func (h *Handler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	h.saveTemplate(w, r, r.PathValue("id"))
}

func (h *Handler) saveTemplate(w http.ResponseWriter, r *http.Request, id string) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body templateBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	input := body.input()
	// Validated before it is written, by rendering each record against a
	// sample and checking the record that comes out. A template that cannot
	// produce a valid record is refused now rather than when somebody creates
	// a domain and gets a zone the name server will not load.
	if err := input.Validate(); err != nil {
		httpx.Error(w, r, httpx.ValidationFailed(err.Error()))
		return
	}

	template, err := h.service.repo.SaveTemplate(ctx, h.service.serverID, id, input)
	if err != nil {
		httpx.Error(w, r, translateTemplate(err))
		return
	}

	h.service.record(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		ActionTemplateSave, "dns_template", template.ID, map[string]any{
			"name":    template.Name,
			"records": len(template.Records),
			"default": template.IsDefault,
		})

	if id == "" {
		httpx.Created(w, r, template)
		return
	}
	httpx.OK(w, r, template)
}

func (h *Handler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	id := r.PathValue("id")
	if err := h.service.repo.DeleteTemplate(ctx, h.service.serverID, id); err != nil {
		httpx.Error(w, r, translateTemplate(err))
		return
	}

	h.service.record(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		ActionTemplateDelete, "dns_template", id, nil)
	httpx.OK(w, r, map[string]bool{"deleted": true})
}

// translateTemplate maps a template failure onto a status.
func translateTemplate(err error) error {
	switch {
	case errors.Is(err, ErrTemplateNotFound):
		return httpx.NotFound("No such DNS template")
	case errors.Is(err, ErrTemplateNameTaken):
		return httpx.Conflict("A template with that name already exists")
	case errors.Is(err, ErrTemplateBuiltin):
		return httpx.Conflict(
			"The built-in template cannot be removed. Edit it, or make another one the default.")
	case errors.Is(err, ErrTemplateInvalid), errors.Is(err, ErrUnknownPlaceholder):
		return httpx.ValidationFailed(err.Error())
	default:
		return translate(err)
	}
}
