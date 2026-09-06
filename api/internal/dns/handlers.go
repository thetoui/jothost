package dns

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
	"github.com/jothost/panel/shared/validate"
)

// Timeouts. Reading and editing are calls to a local daemon; installing pulls a
// package, and a sync is an outbound call to somebody else's API.
const (
	requestTimeout = 60 * time.Second
	installTimeout = 10 * time.Minute
	syncTimeout    = 5 * time.Minute
)

// maxBodyBytes bounds a request body.
const maxBodyBytes = 64 << 10

// Handler serves the DNS endpoints.
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
// Reading needs server.view; every change needs dns.manage, which migration
// 0002 seeded with the rest of the permission set.
//
// It is its own permission and not website.update for a reason this phase makes
// sharper than the others: DNS is the only thing in this panel that can point a
// customer's name at a machine somebody else controls, and it does it without
// touching a single file on this host.
func (h *Handler) Routes(mux *http.ServeMux) {
	guarded := func(permission string, next http.HandlerFunc) http.Handler {
		return h.auth.RequireAuth(h.auth.RequirePermission(permission)(next))
	}

	mux.Handle("GET /api/v1/dns", guarded(rbac.PermServerView, h.overview))
	mux.Handle("POST /api/v1/dns/install", guarded(rbac.PermDNSManage, h.install))
	mux.Handle("PUT /api/v1/dns/settings", guarded(rbac.PermDNSManage, h.settings))

	mux.Handle("GET /api/v1/dns/zones", guarded(rbac.PermServerView, h.listZones))
	mux.Handle("POST /api/v1/dns/zones", guarded(rbac.PermDNSManage, h.createZone))
	mux.Handle("GET /api/v1/dns/zones/{id}", guarded(rbac.PermServerView, h.zone))
	mux.Handle("PATCH /api/v1/dns/zones/{id}", guarded(rbac.PermDNSManage, h.updateZone))
	mux.Handle("DELETE /api/v1/dns/zones/{id}", guarded(rbac.PermDNSManage, h.deleteZone))

	mux.Handle("POST /api/v1/dns/zones/{id}/records", guarded(rbac.PermDNSManage, h.createRecord))
	mux.Handle("PATCH /api/v1/dns/records/{id}", guarded(rbac.PermDNSManage, h.updateRecord))
	mux.Handle("DELETE /api/v1/dns/records/{id}", guarded(rbac.PermDNSManage, h.deleteRecord))

	// Remote providers, and the push to them.
	mux.Handle("POST /api/v1/dns/providers", guarded(rbac.PermDNSManage, h.addProvider))
	mux.Handle("DELETE /api/v1/dns/providers/{id}", guarded(rbac.PermDNSManage, h.removeProvider))
	mux.Handle("POST /api/v1/dns/zones/{id}/sync", guarded(rbac.PermDNSManage, h.sync))
	// The other direction, and its own route rather than a flag on the one
	// above. A push and an import are opposite operations on the same records,
	// and one endpoint that did either depending on a boolean is how somebody
	// eventually sends the wrong boolean and overwrites the side they meant to
	// keep.
	mux.Handle("POST /api/v1/dns/zones/{id}/import", guarded(rbac.PermDNSManage, h.importZone))

	// The website's own DNS tab.
	mux.Handle("GET /api/v1/websites/{id}/dns", guarded(rbac.PermServerView, h.forWebsite))
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	overview, err := h.service.Overview(ctx, httpx.RequestIDFromContext(ctx))
	if err != nil {
		if agentclient.IsUnsupported(err) {
			httpx.OK(w, r, emptyOverview())
			return
		}
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, overview)
}

func (h *Handler) install(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), installTimeout)
	defer cancel()

	status, err := h.service.Install(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, status)
}

// settingsBody is the settings PUT.
type settingsBody struct {
	ListenOn      []string `json:"listen_on"`
	AllowTransfer []string `json:"allow_transfer"`
	DNSSECPolicy  string   `json:"dnssec_policy"`
	DefaultNS     []string `json:"default_ns"`
	DefaultTTL    int      `json:"default_ttl"`
	Hostmaster    string   `json:"hostmaster"`
}

func (h *Handler) settings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body settingsBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	settings, err := h.service.Configure(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		Settings{
			ListenOn:      body.ListenOn,
			AllowTransfer: body.AllowTransfer,
			DNSSECPolicy:  strings.TrimSpace(body.DNSSECPolicy),
			DefaultNS:     body.DefaultNS,
			DefaultTTL:    body.DefaultTTL,
			Hostmaster:    strings.TrimSpace(body.Hostmaster),
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, settings)
}

func (h *Handler) listZones(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	zones, err := h.service.repo.ListZones(ctx, h.service.serverID)
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"zones": zones, "count": len(zones)})
}

func (h *Handler) zone(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	detail, err := h.service.ZoneDetail(ctx, httpx.RequestIDFromContext(ctx), r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, detail)
}

// createZoneBody is the body of a zone create.
type createZoneBody struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// ReverseNetwork creates a reverse zone. When it is given the name is
	// derived from it, so a name and network that disagree cannot be recorded.
	ReverseNetwork string   `json:"reverse_network"`
	WebsiteID      string   `json:"website_id"`
	PrimaryNS      string   `json:"primary_ns"`
	Hostmaster     string   `json:"hostmaster"`
	Nameservers    []string `json:"nameservers"`
	DNSSEC         bool     `json:"dnssec"`
	AllowTransfer  []string `json:"allow_transfer"`
	AlsoNotify     []string `json:"also_notify"`
	Masters        []string `json:"masters"`
	// SeedRecords defaults to true for a forward primary zone: a zone with
	// nothing in it answers nothing, which is rarely what somebody creating one
	// wants. Sending false gets an empty zone.
	SeedRecords *bool `json:"seed_records"`
}

func (h *Handler) createZone(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body createZoneBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	seed := true
	if body.SeedRecords != nil {
		seed = *body.SeedRecords
	}

	zone, err := h.service.CreateZone(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		CreateZoneRequest{
			Name:           strings.TrimSpace(body.Name),
			Kind:           strings.TrimSpace(body.Kind),
			WebsiteID:      strings.TrimSpace(body.WebsiteID),
			ReverseNetwork: strings.TrimSpace(body.ReverseNetwork),
			PrimaryNS:      strings.TrimSpace(body.PrimaryNS),
			Hostmaster:     strings.TrimSpace(body.Hostmaster),
			Nameservers:    body.Nameservers,
			DNSSEC:         body.DNSSEC,
			AllowTransfer:  body.AllowTransfer,
			AlsoNotify:     body.AlsoNotify,
			Masters:        body.Masters,
			SeedRecords:    seed,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, zone)
}

// updateZoneBody is a zone PATCH. An omitted field is left alone, which is why
// every one is a pointer.
type updateZoneBody struct {
	PrimaryNS     *string   `json:"primary_ns"`
	Hostmaster    *string   `json:"hostmaster"`
	Refresh       *int      `json:"refresh"`
	Retry         *int      `json:"retry"`
	Expire        *int      `json:"expire"`
	Minimum       *int      `json:"minimum"`
	TTL           *int      `json:"ttl"`
	Nameservers   *[]string `json:"nameservers"`
	DNSSEC        *bool     `json:"dnssec"`
	AllowTransfer *[]string `json:"allow_transfer"`
	AlsoNotify    *[]string `json:"also_notify"`
	Masters       *[]string `json:"masters"`
	WebsiteID     *string   `json:"website_id"`
}

func (h *Handler) updateZone(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body updateZoneBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	zone, err := h.service.UpdateZone(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), UpdateZoneRequest{
			PrimaryNS: body.PrimaryNS, Hostmaster: body.Hostmaster,
			Refresh: body.Refresh, Retry: body.Retry, Expire: body.Expire,
			Minimum: body.Minimum, TTL: body.TTL, Nameservers: body.Nameservers,
			DNSSEC: body.DNSSEC, AllowTransfer: body.AllowTransfer,
			AlsoNotify: body.AlsoNotify, Masters: body.Masters,
			WebsiteID: body.WebsiteID,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, zone)
}

func (h *Handler) deleteZone(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.service.DeleteZone(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// recordBody is a record create or update.
type recordBody struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Flags    int    `json:"flags"`
	Tag      string `json:"tag"`
}

func (b recordBody) request() RecordRequest {
	return RecordRequest{
		Name:     strings.TrimSpace(b.Name),
		Type:     strings.ToUpper(strings.TrimSpace(b.Type)),
		TTL:      b.TTL,
		Value:    strings.TrimSpace(b.Value),
		Priority: b.Priority,
		Weight:   b.Weight,
		Port:     b.Port,
		Flags:    b.Flags,
		Tag:      strings.ToLower(strings.TrimSpace(b.Tag)),
	}
}

func (h *Handler) createRecord(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body recordBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	record, err := h.service.CreateRecord(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body.request())
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, record)
}

func (h *Handler) updateRecord(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body recordBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	record, err := h.service.UpdateRecord(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id"), body.request())
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, record)
}

func (h *Handler) deleteRecord(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.service.DeleteRecord(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// providerBody carries a remote provider's credentials.
//
// The token arrives here once and is never sent back: it is encrypted on the
// way in, and no response in this package carries it.
type providerBody struct {
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Token     string `json:"token"`
	AccountID string `json:"account_id"`
}

func (h *Handler) addProvider(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	var body providerBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	provider, err := h.service.AddProvider(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		AddProviderRequest{
			Kind:      strings.TrimSpace(body.Kind),
			Label:     strings.TrimSpace(body.Label),
			Token:     strings.TrimSpace(body.Token),
			AccountID: strings.TrimSpace(body.AccountID),
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.Created(w, r, provider)
}

func (h *Handler) removeProvider(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	if err := h.service.RemoveProvider(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		r.PathValue("id")); err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"deleted": true})
}

// syncBody asks for a push to a provider.
type syncBody struct {
	ProviderID string `json:"provider_id"`
	// Prune deletes records at the provider that the panel does not have. It
	// has to be asked for: a provider's zone usually holds records this panel
	// never knew about.
	Prune bool `json:"prune"`
}

func (h *Handler) sync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()

	var body syncBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	result, err := h.service.SyncZone(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		SyncRequest{
			ZoneID:     r.PathValue("id"),
			ProviderID: strings.TrimSpace(body.ProviderID),
			Prune:      body.Prune,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

// importBody asks for a provider's copy of a zone.
type importBody struct {
	ProviderID string `json:"provider_id"`
	// Replace discards what the panel holds and takes the provider's copy as
	// it stands. Without it the import only adds what is missing.
	Replace bool `json:"replace"`
}

func (h *Handler) importZone(w http.ResponseWriter, r *http.Request) {
	// The same bound as a sync: this is one call to somebody else's API and
	// then a write per record.
	ctx, cancel := context.WithTimeout(r.Context(), syncTimeout)
	defer cancel()

	var body importBody
	if err := decode(r, &body); err != nil {
		httpx.Error(w, r, err)
		return
	}

	result, err := h.service.ImportZone(ctx, actorFrom(r), httpx.RequestIDFromContext(ctx),
		ImportRequest{
			ZoneID:     r.PathValue("id"),
			ProviderID: strings.TrimSpace(body.ProviderID),
			Replace:    body.Replace,
		})
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, result)
}

func (h *Handler) forWebsite(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
	defer cancel()

	zones, err := h.service.ForWebsite(ctx, r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, translate(err))
		return
	}
	httpx.OK(w, r, map[string]any{"zones": zones, "count": len(zones)})
}

// emptyOverview is what the page gets on a host with no name server: the same
// shape, so nothing has to special-case a missing field.
func emptyOverview() Overview {
	return Overview{
		DNSStatus: agentclient.DNSStatus{
			Zones: []string{}, ListenOn: []string{}, Warnings: []string{},
			Reason: ErrUnavailable.Error(),
		},
		Zones:     []Zone{},
		HostZones: []string{},
		Providers: []Provider{},
	}
}

func decode(r *http.Request, into any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxBodyBytes))
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

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// translate maps a failure to its HTTP shape.
//
// Each of these is a different next step for an operator: "that zone is already
// here", "a CNAME cannot share a name", "this host has no name server" and
// "that zone's records come from its primary" are four different things to do.
func translate(err error) error {
	var apiErr *httpx.APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}

	switch {
	case errors.Is(err, ErrZoneNotFound), errors.Is(err, ErrRecordNotFound),
		errors.Is(err, ErrProviderNotFound):
		return httpx.NotFound(err.Error())
	case errors.Is(err, ErrDuplicateZone), errors.Is(err, ErrDuplicateRecord):
		return httpx.Conflict(err.Error())
	case errors.Is(err, ErrUnavailable), agentclient.IsUnsupported(err):
		return httpx.Conflict(ErrUnavailable.Error())
	case errors.Is(err, ErrSlaveRecords), errors.Is(err, ErrManagedRecord),
		errors.Is(err, ErrCNAMEConflict), errors.Is(err, ErrNoNameservers),
		errors.Is(err, ErrDNSSECUnsupported), errors.Is(err, ErrUnknownProvider):
		return httpx.ValidationFailed(err.Error())
	case errors.Is(err, validate.ErrInvalidZone),
		errors.Is(err, validate.ErrInvalidRecordType),
		errors.Is(err, validate.ErrInvalidRecordName),
		errors.Is(err, validate.ErrInvalidRecordValue),
		errors.Is(err, validate.ErrInvalidTTL),
		errors.Is(err, validate.ErrInvalidSOA),
		errors.Is(err, validate.ErrInvalidNetwork),
		errors.Is(err, validate.ErrInvalidDomain):
		return httpx.ValidationFailed(err.Error())
	case agentclient.IsNotFound(err):
		return httpx.NotFound(agentclient.Message(err))
	case agentclient.IsInvalidPayload(err), agentclient.IsInvalidRequest(err):
		return httpx.ValidationFailed(agentclient.Message(err))
	default:
		return err
	}
}
