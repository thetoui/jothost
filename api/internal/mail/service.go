package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Every one of these changes who can read or send somebody's mail, which is the
// most private thing this panel touches. Setting a mailbox password is audited
// with particular care: it is the one action in the panel that silently grants
// the ability to read every message in a mailbox, and the owner sees no trace
// of it anywhere else.
const (
	ActionConfigure       = "mail.configure"
	ActionInstall         = "mail.install"
	ActionDomainCreate    = "mail.domain.create"
	ActionDomainUpdate    = "mail.domain.update"
	ActionDomainDelete    = "mail.domain.delete"
	ActionDKIMRotate      = "mail.dkim.rotate"
	ActionMailboxCreate   = "mail.mailbox.create"
	ActionMailboxUpdate   = "mail.mailbox.update"
	ActionMailboxDelete   = "mail.mailbox.delete"
	ActionMailboxPassword = "mail.mailbox.password"
	ActionAliasCreate     = "mail.alias.create"
	ActionAliasDelete     = "mail.alias.delete"
	ActionResponderSet    = "mail.autoresponder.set"
	ActionResponderClear  = "mail.autoresponder.clear"
	ActionWebmailInstall  = "mail.webmail.install"
	ActionWebmailRemove   = "mail.webmail.remove"

	ResourceTypeServer  = "server"
	ResourceTypeDomain  = "mail_domain"
	ResourceTypeMailbox = "mailbox"
	ResourceTypeAlias   = "mail_alias"
	ResourceTypeWebmail = "webmail"
)

// Errors returned by the service.
var (
	// ErrUnavailable means the host has no mail server.
	ErrUnavailable = errors.New("this host has no mail server")
	// ErrNoHostname means mail cannot be switched on without a name for the
	// server.
	ErrNoHostname = errors.New(
		"the mail server needs a fully qualified hostname before it can be switched on")
	// ErrWeakPassword means a mailbox password is too short to be one.
	ErrWeakPassword = errors.New("a mailbox password must be at least 12 characters")
	// ErrNoWebsite means webmail was asked for without a site to serve it from.
	ErrNoWebsite = errors.New("webmail needs a website to serve it from")
)

// MinPasswordLength is the shortest mailbox password the panel will set.
//
// Twelve, which is longer than the panel's own minimum for a login, and that is
// deliberate. A mailbox password is exposed to the whole internet on port 993
// and 587 with no rate limit the panel controls, it is typed into devices that
// remember it forever, and it is the single credential that unlocks a
// customer's correspondence. It is also, in practice, the one people most want
// to make short.
const MinPasswordLength = 12

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Websites is what this package needs to know about a site.
//
// An interface rather than the websites repository, for the reason the ftp and
// cron packages give: this package needs five fields, and depending on the
// whole thing would make the two impossible to change independently.
type Websites interface {
	LookupForMail(ctx context.Context, id string) (WebsiteRef, error)
}

// WebsiteRef is the site a mail setting points at.
type WebsiteRef struct {
	ID           string
	ServerID     string
	Domain       string
	SystemUser   string
	DocumentRoot string
	// SSLEnabled reports whether this site has a certificate the mail server
	// could present.
	SSLEnabled bool
	// CertificatePath and KeyPath are where Phase 6 put the material. They are
	// read fresh on every reconcile rather than copied into the mail settings,
	// so a renewal that moved the files is picked up instead of leaving the
	// mail server presenting a path that no longer exists.
	CertificatePath string
	KeyPath         string
}

// Zones is what this package needs from DNS.
//
// Narrow on purpose. This package knows *what* records a mail domain needs; it
// does not know how to spell one, how to validate one, or how to get one into a
// zone file, and a second place that did would be a second place that could do
// it differently.
type Zones interface {
	ZoneIDFor(ctx context.Context, domain string) (string, bool, error)
	SetManagedRecords(ctx context.Context, requestID, zoneID, name string,
		records []ManagedRecord) error
	PublishedValues(ctx context.Context, zoneID, name, recordType string) ([]string, error)
}

// ManagedRecord mirrors the DNS package's own type.
//
// Declared here as well so this package does not import the DNS package to name
// a struct — the interface above is the whole of the dependency, and the server
// wires the two together.
type ManagedRecord struct {
	Type     string
	Value    string
	Priority int
	TTL      int
}

// Service manages the host's mail.
type Service struct {
	repo     *Repository
	websites Websites
	zones    Zones
	agent    *agentclient.Client
	audit    *audit.Recorder
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Websites Websites
	Zones    Zones
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Log      *slog.Logger
	ServerID string
}

// NewService builds a Service.
func NewService(opts ServiceOptions) *Service {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		repo:     opts.Repo,
		websites: opts.Websites,
		zones:    opts.Zones,
		agent:    opts.Agent,
		audit:    opts.Audit,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Overview is the mail page.
type Overview struct {
	Settings Settings       `json:"settings"`
	Status   map[string]any `json:"status"`
	Domains  []DomainView   `json:"domains"`
	// Counts are the totals a page leads with.
	Mailboxes int `json:"mailboxes"`
	Aliases   int `json:"aliases"`
}

// DomainView is a domain together with what the world can actually see of it.
//
// The second half is the phase's whole point. A domain's SPF policy in this
// database is a *setting*; the TXT record a receiving server fetches is the
// only thing that has any effect, and the two can disagree — because DNS was
// never published, because the zone is somewhere else, because somebody edited
// it. A panel that showed only the setting would report a domain as protected
// while every message it sends is treated as unauthenticated.
type DomainView struct {
	Domain
	// Mailboxes is how many this domain has.
	Mailboxes int `json:"mailboxes"`
	// Aliases is how many forwarders it has.
	Aliases int `json:"aliases"`

	// Signing reports whether this host actually holds the private key.
	Signing bool `json:"signing"`

	// DNSManaged reports whether this host serves the domain's zone. When it
	// does not, the three checks below cannot be answered at all — and saying
	// "not known" is the honest answer rather than "not published".
	DNSManaged bool `json:"dns_managed"`

	MXPublished    bool `json:"mx_published"`
	SPFPublished   bool `json:"spf_published"`
	DKIMPublished  bool `json:"dkim_published"`
	DMARCPublished bool `json:"dmarc_published"`

	// Problems are the specific disagreements, in words, so the page does not
	// have to reconstruct them from four booleans.
	Problems []string `json:"problems,omitempty"`
}

// Overview reads the whole mail page.
func (s *Service) Overview(ctx context.Context, requestID string) (Overview, error) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return Overview{}, err
	}

	overview := Overview{Settings: settings}

	status, err := s.agentStatus(ctx, requestID, settings)
	if err != nil {
		// A host the Agent cannot answer for is a host whose mail settings are
		// still worth showing: the operator may be trying to work out why.
		s.log.Warn("could not read the mail server's status", "error", err)
		status = map[string]any{
			"available": false,
			"reason":    "the Agent could not be reached: " + err.Error(),
		}
	}
	overview.Status = status

	domains, err := s.repo.ListDomains(ctx, s.serverID)
	if err != nil {
		return Overview{}, err
	}
	counts, err := s.repo.CountMailboxes(ctx, s.serverID)
	if err != nil {
		return Overview{}, err
	}
	aliases, err := s.repo.AllAliases(ctx, s.serverID)
	if err != nil {
		return Overview{}, err
	}
	signing := signingSet(status)

	views := make([]DomainView, 0, len(domains))
	for _, domain := range domains {
		view := DomainView{
			Domain:    domain,
			Mailboxes: counts[domain.ID],
			Aliases:   len(aliases[domain.ID]),
			Signing:   signing[domain.Domain],
		}
		s.describePublication(ctx, settings, &view)
		views = append(views, view)
		overview.Mailboxes += counts[domain.ID]
		overview.Aliases += len(aliases[domain.ID])
	}
	overview.Domains = views

	return overview, nil
}

// signingSet reads the domains the host reports holding a key for.
func signingSet(status map[string]any) map[string]bool {
	set := map[string]bool{}
	raw, ok := status["signing"].([]any)
	if !ok {
		return set
	}
	for _, entry := range raw {
		if name, ok := entry.(string); ok {
			set[name] = true
		}
	}
	return set
}

// describePublication fills in what DNS actually serves for a domain.
//
// This is the comparison the phase exists to make. Every check here answers
// "would a receiving server see this", not "did somebody tick the box".
func (s *Service) describePublication(ctx context.Context, settings Settings, view *DomainView) {
	if s.zones == nil {
		return
	}
	zoneID, found, err := s.zones.ZoneIDFor(ctx, view.Domain.Domain)
	if err != nil {
		s.log.Warn("could not read the zone for a mail domain",
			"domain", view.Domain.Domain, "error", err)
		return
	}
	if !found {
		// The domain's DNS is somewhere else. The panel cannot check it and
		// says so — which is a different answer from "not published", and the
		// difference matters: one is a fault and the other is a limit of what
		// this host can see.
		view.Problems = append(view.Problems,
			"this host does not serve DNS for "+view.Domain.Domain+
				", so the panel cannot check whether its mail records are published. "+
				"They have to be added wherever its DNS is.")
		return
	}
	view.DNSManaged = true

	mx, _ := s.zones.PublishedValues(ctx, zoneID, "@", validate.RecordMX)
	view.MXPublished = containsValue(mx, settings.Hostname)
	if !view.MXPublished {
		view.Problems = append(view.Problems,
			"no MX record points at this server, so no mail will be delivered here at all")
	}

	apexTXT, _ := s.zones.PublishedValues(ctx, zoneID, "@", validate.RecordTXT)
	view.SPFPublished = hasPrefix(apexTXT, "v=spf1")
	if view.Domain.SPFPolicy != validate.SPFNone && !view.SPFPublished {
		view.Problems = append(view.Problems,
			"an SPF policy is set and no SPF record is published, so mail from this "+
				"domain is unauthenticated as far as every receiving server is concerned")
	}

	if view.Domain.DKIMSelector != "" {
		name := view.Domain.DKIMSelector + "._domainkey"
		published, _ := s.zones.PublishedValues(ctx, zoneID, name, validate.RecordTXT)
		view.DKIMPublished = containsSubstring(published, view.Domain.DKIMPublicKey)
		switch {
		case !view.DKIMPublished:
			view.Problems = append(view.Problems,
				"this domain signs its mail with a key that is not published in DNS, so "+
					"every signature fails to verify — which is worse than not signing")
		case !view.Signing:
			view.Problems = append(view.Problems,
				"a DKIM record is published and this host has no matching key, so the "+
					"domain's mail is going out unsigned")
		}
	}

	if view.Domain.DMARCPolicy != validate.DMARCOff {
		published, _ := s.zones.PublishedValues(ctx, zoneID, "_dmarc", validate.RecordTXT)
		view.DMARCPublished = hasPrefix(published, "v=DMARC1")
		if !view.DMARCPublished {
			view.Problems = append(view.Problems,
				"a DMARC policy is set and no DMARC record is published, so receiving "+
					"servers have nothing to apply")
		}
	}
}

func containsValue(values []string, want string) bool {
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(strings.TrimSuffix(value, "."), strings.TrimSuffix(want, ".")) {
			return true
		}
	}
	return false
}

func hasPrefix(values []string, prefix string) bool {
	for _, value := range values {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, want string) bool {
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.Contains(value, want) {
			return true
		}
	}
	return false
}

// agentStatus asks the host what its mail server is doing.
func (s *Service) agentStatus(ctx context.Context, requestID string,
	settings Settings,
) (map[string]any, error) {
	payload := map[string]any{}
	if settings.WebmailWebsiteID != "" && s.websites != nil {
		if site, err := s.websites.LookupForMail(ctx, settings.WebmailWebsiteID); err == nil {
			payload["webmail_root"] = site.DocumentRoot
		}
	}

	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationMailStatus,
		RequestID: requestID,
		Payload:   payload,
	})
	if err != nil {
		return nil, err
	}
	return response.Data, nil
}

// Configure saves the mail server's settings and applies them.
func (s *Service) Configure(ctx context.Context, actor Actor, requestID string,
	settings Settings,
) (Settings, error) {
	settings.ServerID = s.serverID

	settings.Hostname = validate.NormalizeDomain(settings.Hostname)
	if settings.Enabled {
		if err := validate.MailHostname(settings.Hostname); err != nil {
			return Settings{}, err
		}
	}
	if err := validate.MessageSizeMB(settings.MaxMessageMB); err != nil {
		return Settings{}, err
	}
	if settings.SpamEnabled {
		if err := validate.SpamRejectScore(settings.SpamRejectScore); err != nil {
			return Settings{}, err
		}
	}
	if settings.TLSWebsiteID != "" {
		site, err := s.websites.LookupForMail(ctx, settings.TLSWebsiteID)
		if err != nil {
			return Settings{}, err
		}
		if !site.SSLEnabled {
			return Settings{}, fmt.Errorf(
				"%s has no certificate for the mail server to present", site.Domain)
		}
	}

	saved, err := s.repo.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return saved, err
	}

	s.record(ctx, actor, ActionConfigure, ResourceTypeServer, s.serverID, map[string]any{
		"enabled":     saved.Enabled,
		"hostname":    saved.Hostname,
		"require_tls": saved.RequireTLS,
		"spam":        saved.SpamEnabled,
		"virus":       saved.VirusEnabled,
	})
	return saved, nil
}

// Install puts a mail server on the host.
func (s *Service) Install(ctx context.Context, actor Actor, requestID string,
	filtering, antivirus bool,
) (map[string]any, error) {
	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationMailInstall,
		RequestID: requestID,
		Payload: map[string]any{
			"filtering": filtering,
			"antivirus": antivirus,
		},
	})
	if err != nil {
		return nil, err
	}
	s.record(ctx, actor, ActionInstall, ResourceTypeServer, s.serverID, map[string]any{
		"filtering": filtering, "antivirus": antivirus,
	})
	return response.Data, nil
}

// DomainRequest is a domain being created or changed.
type DomainRequest struct {
	Domain      string `json:"domain"`
	WebsiteID   string `json:"website_id"`
	Active      *bool  `json:"active"`
	CatchAll    string `json:"catch_all"`
	SPFPolicy   string `json:"spf_policy"`
	DMARCPolicy string `json:"dmarc_policy"`
	DMARCRua    string `json:"dmarc_rua"`
}

// CreateDomain records a domain and gives it a signing key.
//
// The key is generated as part of creating the domain rather than offered as a
// later step, because a domain that has to be *remembered* about is one whose
// mail goes out unsigned for however long it takes somebody to remember. The
// records are published straight away where this host serves the zone, and
// reported as needing publishing where it does not.
func (s *Service) CreateDomain(ctx context.Context, actor Actor, requestID string,
	req DomainRequest,
) (Domain, error) {
	name := validate.NormalizeDomain(req.Domain)
	if err := validate.MailDomain(name); err != nil {
		return Domain{}, err
	}
	domain := Domain{
		ServerID:    s.serverID,
		WebsiteID:   req.WebsiteID,
		Domain:      name,
		Active:      true,
		SPFPolicy:   defaulted(req.SPFPolicy, validate.SPFSoft),
		DMARCPolicy: defaulted(req.DMARCPolicy, validate.DMARCNone),
		DMARCRua:    req.DMARCRua,
	}
	if req.Active != nil {
		domain.Active = *req.Active
	}
	if err := s.checkPolicies(&domain, req.CatchAll); err != nil {
		return Domain{}, err
	}

	created, err := s.repo.CreateDomain(ctx, domain)
	if err != nil {
		return Domain{}, err
	}

	// The signing key. A failure here is reported and does not undo the
	// domain: a mail domain with no key is a working mail domain whose mail is
	// unsigned, and the panel says so on the page rather than refusing to
	// create it.
	if _, err := s.rotateDKIM(ctx, requestID, created.ID, created.Domain); err != nil {
		s.log.Warn("could not generate a signing key", "domain", created.Domain, "error", err)
	}

	reloaded, err := s.repo.GetDomain(ctx, created.ID)
	if err != nil {
		return created, err
	}
	if err := s.publishRecords(ctx, requestID, reloaded); err != nil {
		s.log.Warn("could not publish the mail records", "domain", name, "error", err)
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return reloaded, err
	}

	s.record(ctx, actor, ActionDomainCreate, ResourceTypeDomain, created.ID, map[string]any{
		"domain": name,
	})
	return reloaded, nil
}

// UpdateDomain changes a domain's policies.
func (s *Service) UpdateDomain(ctx context.Context, actor Actor, requestID, id string,
	req DomainRequest,
) (Domain, error) {
	existing, err := s.repo.GetDomain(ctx, id)
	if err != nil {
		return Domain{}, err
	}

	updated := existing
	updated.WebsiteID = req.WebsiteID
	if req.Active != nil {
		updated.Active = *req.Active
	}
	if req.SPFPolicy != "" {
		updated.SPFPolicy = req.SPFPolicy
	}
	if req.DMARCPolicy != "" {
		updated.DMARCPolicy = req.DMARCPolicy
	}
	updated.DMARCRua = req.DMARCRua
	if err := s.checkPolicies(&updated, req.CatchAll); err != nil {
		return Domain{}, err
	}

	saved, err := s.repo.UpdateDomain(ctx, id, updated)
	if err != nil {
		return Domain{}, err
	}
	if err := s.publishRecords(ctx, requestID, saved); err != nil {
		s.log.Warn("could not publish the mail records", "domain", saved.Domain, "error", err)
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return saved, err
	}

	s.record(ctx, actor, ActionDomainUpdate, ResourceTypeDomain, id, map[string]any{
		"domain":    saved.Domain,
		"active":    saved.Active,
		"catch_all": saved.CatchAll != "",
	})
	return saved, nil
}

// checkPolicies validates a domain's settings.
func (s *Service) checkPolicies(domain *Domain, catchAll string) error {
	if err := validate.SPFPolicy(domain.SPFPolicy); err != nil {
		return err
	}
	if err := validate.DMARCPolicy(domain.DMARCPolicy); err != nil {
		return err
	}
	if domain.DMARCRua != "" {
		address, err := validate.MailAddress(domain.DMARCRua)
		if err != nil {
			return err
		}
		domain.DMARCRua = address
	}
	if catchAll != "" {
		address, err := validate.MailAddress(catchAll)
		if err != nil {
			return err
		}
		domain.CatchAll = address
	} else {
		domain.CatchAll = ""
	}
	return nil
}

// DeleteDomain removes a domain, its mailboxes and its forwarders.
func (s *Service) DeleteDomain(ctx context.Context, actor Actor, requestID, id string) error {
	domain, err := s.repo.GetDomain(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteDomain(ctx, id); err != nil {
		return err
	}

	// The signing key goes with it. A key left on the host for a domain the
	// panel no longer serves is a key that would sign again if the domain were
	// recreated — with a public half nothing publishes any more.
	if domain.DKIMSelector != "" {
		if _, err := s.agent.Do(ctx, protocol.Request{
			Operation: protocol.OperationMailDKIMRemove,
			RequestID: requestID,
			Payload: map[string]any{
				"domain": domain.Domain, "selector": domain.DKIMSelector,
			},
		}); err != nil {
			s.log.Warn("could not remove the signing key",
				"domain", domain.Domain, "error", err)
		}
	}

	// The published records go too, where this host serves the zone. Leaving an
	// MX pointing at a server that no longer accepts the domain's mail is how
	// mail bounces for weeks after somebody thought they had cleaned up.
	if err := s.withdrawRecords(ctx, requestID, domain); err != nil {
		s.log.Warn("could not withdraw the mail records",
			"domain", domain.Domain, "error", err)
	}

	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}
	s.record(ctx, actor, ActionDomainDelete, ResourceTypeDomain, id, map[string]any{
		"domain": domain.Domain,
	})
	return nil
}

// RotateDKIM gives a domain a new signing key.
func (s *Service) RotateDKIM(ctx context.Context, actor Actor, requestID, id string) (Domain, error) {
	domain, err := s.repo.GetDomain(ctx, id)
	if err != nil {
		return Domain{}, err
	}
	previous := domain.DKIMSelector

	updated, err := s.rotateDKIM(ctx, requestID, id, domain.Domain)
	if err != nil {
		return Domain{}, err
	}
	if err := s.publishRecords(ctx, requestID, updated); err != nil {
		s.log.Warn("could not publish the new signing key",
			"domain", domain.Domain, "error", err)
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return updated, err
	}

	// The old key is deleted only now, after the new one is generated, recorded
	// and in force. The other order would leave a window with no key at all,
	// during which every message this domain sends goes out unsigned.
	if previous != "" && previous != updated.DKIMSelector {
		if _, err := s.agent.Do(ctx, protocol.Request{
			Operation: protocol.OperationMailDKIMRemove,
			RequestID: requestID,
			Payload:   map[string]any{"domain": domain.Domain, "selector": previous},
		}); err != nil {
			s.log.Warn("could not remove the previous signing key",
				"domain", domain.Domain, "error", err)
		}
	}

	s.record(ctx, actor, ActionDKIMRotate, ResourceTypeDomain, id, map[string]any{
		"domain": domain.Domain, "selector": updated.DKIMSelector,
	})
	return updated, nil
}

// rotateDKIM asks the host for a key and records its public half.
func (s *Service) rotateDKIM(ctx context.Context, requestID, id, name string) (Domain, error) {
	now := time.Now().UTC()
	selector := selectorFor(now)

	response, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationMailDKIMGenerate,
		RequestID: requestID,
		Payload:   map[string]any{"domain": name, "selector": selector},
	})
	if err != nil {
		return Domain{}, err
	}
	publicKey, _ := response.Data["public_key"].(string)
	if publicKey == "" {
		return Domain{}, fmt.Errorf("the host generated a key and returned no public half")
	}
	return s.repo.SaveDKIM(ctx, id, selector, publicKey)
}

// selectorFor names a key by the month it was made.
//
// A date rather than a fixed name, because a selector's whole purpose is to let
// a domain have two keys at once during a rotation. A fixed "default" selector
// makes that impossible: the new key would have to replace the old one at the
// same name, and every message signed with the old key that is still in flight
// would fail to verify.
//
// The second and later rotations inside one month get a suffix, so rotating
// twice in a week is possible rather than a silent no-op.
func selectorFor(now time.Time) string {
	return fmt.Sprintf("jh%04d%02d%02d%02d", now.Year(), int(now.Month()),
		now.Day(), now.Hour())
}

func defaulted(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
