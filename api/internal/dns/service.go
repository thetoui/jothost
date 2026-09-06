package dns

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/validate"
)

// Audit actions.
//
// Every change here changes what the internet is told about a customer's names.
// A wrong MX record loses mail, a wrong A record takes a site off the air, and
// a deleted zone does both at once — so all of them are recorded, including the
// ones that only change a setting.
const (
	ActionZoneCreate     = "dns.zone.create"
	ActionZoneUpdate     = "dns.zone.update"
	ActionZoneDelete     = "dns.zone.delete"
	ActionRecordCreate   = "dns.record.create"
	ActionRecordUpdate   = "dns.record.update"
	ActionRecordDelete   = "dns.record.delete"
	ActionConfigure      = "dns.configure"
	ActionInstall        = "dns.install"
	ActionProviderAdd    = "dns.provider.add"
	ActionProviderRemove = "dns.provider.remove"
	ActionSync           = "dns.sync"
	// ActionImport is the opposite direction and is recorded as its own
	// action, not as a sync: a trail that called both "dns.sync" could not
	// answer which way somebody moved the records.
	ActionImport = "dns.import"

	ResourceTypeZone     = "dns_zone"
	ResourceTypeRecord   = "dns_record"
	ResourceTypeProvider = "dns_provider"
	ResourceTypeServer   = "server"
)

// Errors returned by the service.
var (
	// ErrUnavailable means the host has no name server.
	ErrUnavailable = errors.New("this host has no DNS server")
	// ErrSlaveRecords means somebody tried to edit a zone this host does not
	// own. A secondary's contents arrive by transfer and are replaced at the
	// next one, so an edit here would be a change that silently reverts.
	ErrSlaveRecords = errors.New("a secondary zone's records come from its primary and cannot be edited here")
	// ErrManagedRecord means a record the panel maintains for itself.
	ErrManagedRecord = errors.New("this record is maintained by the panel and cannot be edited by hand")
	// ErrCNAMEConflict means a CNAME would share a name with another record.
	ErrCNAMEConflict = errors.New("a CNAME must be the only record at its name")
	// ErrNoNameservers means a zone with nothing to delegate to.
	ErrNoNameservers = errors.New("a zone needs at least one name server")
	// ErrDNSSECUnsupported means this BIND cannot sign.
	ErrDNSSECUnsupported = errors.New("this DNS server cannot sign zones")
	// ErrUnknownProvider means a remote provider kind this panel has no client
	// for.
	ErrUnknownProvider = errors.New("this panel has no client for that DNS provider")
)

// Actor is who asked, for the audit trail.
type Actor struct {
	UserID    string
	IPAddress string
	UserAgent string
}

// Websites is what this package needs to know about a site a zone belongs to.
//
// An interface rather than the websites repository, for the reason the FTP and
// cron packages give: this package needs three fields, and depending on the
// whole thing would make the two impossible to change independently.
type Websites interface {
	LookupForDNS(ctx context.Context, id string) (WebsiteRef, error)
}

// WebsiteRef is the site a zone belongs to.
type WebsiteRef struct {
	ID       string
	ServerID string
	Domain   string
	// Address is where the site answers, and is what a seeded A record points
	// at. Empty when the panel does not know, in which case no address record
	// is seeded — a zone with an A record pointing nowhere is worse than a zone
	// with none, because it looks configured.
	Address string
}

// Host reports this machine's own address.
//
// An interface, and asked at the moment it is needed rather than read once at
// startup: a host's address changes — a new floating IP, a move between
// providers — and a value captured when the API booted would go on seeding new
// zones with an address that stopped answering months ago.
type Host interface {
	Address(ctx context.Context) (string, error)
}

// Service manages DNS zones and records.
type Service struct {
	repo     *Repository
	websites Websites
	agent    *agentclient.Client
	audit    *audit.Recorder
	remote   RemoteFactory
	host     Host
	log      *slog.Logger
	serverID string
}

// ServiceOptions configure a Service.
type ServiceOptions struct {
	Repo     *Repository
	Websites Websites
	Agent    *agentclient.Client
	Audit    *audit.Recorder
	Remote   RemoteFactory
	// Host answers what this machine's address is, for the records the panel
	// seeds and for a subdomain's own record.
	Host     Host
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
		agent:    opts.Agent,
		audit:    opts.Audit,
		remote:   opts.Remote,
		host:     opts.Host,
		log:      log,
		serverID: opts.ServerID,
	}
}

// Overview is everything the DNS page shows.
//
// The status is embedded, and its own zone list is republished here under a
// different name. Go resolves the collision by letting the outer field win and
// dropping the embedded one from the JSON silently — so leaving both called
// "zones" would mean the host's list never reached the page at all, with
// nothing anywhere saying so.
type Overview struct {
	agentclient.DNSStatus
	// Zones are the zones the panel has recorded.
	Zones []Zone `json:"zones"`
	// HostZones are the zones the *host* is configured to serve. The two lists
	// disagreeing is worth seeing: a name in this one and not in the other is
	// a zone the next reconcile removes.
	HostZones []string   `json:"host_zones"`
	Settings  Settings   `json:"settings"`
	Providers []Provider `json:"providers"`
}

// Overview reads the whole picture.
//
// The zones come from the panel's record, not from the host. A zone the host
// serves and the panel does not know about is *not* listed as a zone: the
// panel's record is what the next reconcile will make true, so a row the panel
// does not own would be a row about to disappear. It appears instead in
// HostZones, which is what the host actually has.
func (s *Service) Overview(ctx context.Context, requestID string) (Overview, error) {
	overview := Overview{Zones: []Zone{}, Providers: []Provider{}}

	status, err := s.agent.DNSStatusOf(ctx, requestID)
	if err != nil {
		return overview, err
	}
	overview.DNSStatus = status
	overview.HostZones = status.Zones
	if overview.HostZones == nil {
		overview.HostZones = []string{}
	}

	zones, err := s.repo.ListZones(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	overview.Zones = zones

	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	overview.Settings = settings

	providers, err := s.repo.ListProviders(ctx, s.serverID)
	if err != nil {
		return overview, err
	}
	overview.Providers = providers
	return overview, nil
}

// ZoneDetail is one zone with its records and what the host says about it.
type ZoneDetail struct {
	Zone Zone `json:"zone"`
	// State is what the running server reports, including the serial it is
	// actually answering with — which is not the panel's on a signed zone.
	State agentclient.DNSZoneState `json:"state"`
	// Signing is the zone's DNSSEC state, and carries the DS record the
	// parent's registrar needs. Empty when the zone is not signed.
	Signing agentclient.DNSSigningStatus `json:"signing"`
}

// ZoneDetail reads one zone.
func (s *Service) ZoneDetail(ctx context.Context, requestID, id string) (ZoneDetail, error) {
	var detail ZoneDetail

	zone, err := s.repo.GetZone(ctx, id)
	if err != nil {
		return detail, err
	}
	records, err := s.repo.ListRecords(ctx, zone.ID)
	if err != nil {
		return detail, err
	}
	zone.Records = records
	detail.Zone = zone

	// What the host says is fetched separately and is allowed to fail without
	// failing the whole read: a name server that is stopped should not make a
	// zone uneditable, because editing it is how somebody prepares to start it.
	state, err := s.agent.DNSZoneStatus(ctx, requestID, zone.Name)
	if err != nil {
		s.log.Warn("could not read the zone's state from the host",
			"zone", zone.Name, "error", err.Error())
	} else {
		detail.State = state
	}

	if zone.DNSSEC {
		signing, err := s.agent.DNSSigning(ctx, requestID, zone.Name)
		if err != nil {
			s.log.Warn("could not read the zone's signing state",
				"zone", zone.Name, "error", err.Error())
		} else {
			detail.Signing = signing
		}
	}
	return detail, nil
}

// CreateZoneRequest is what an operator asked for.
type CreateZoneRequest struct {
	Name string
	Kind string
	// WebsiteID attaches the zone to a site, which is what makes the site's
	// own DNS tab show it.
	WebsiteID string
	// ReverseNetwork creates a reverse zone. When it is given, Name is derived
	// from it rather than taken from the request: a reverse zone name that does
	// not match its network is a zone nobody ever queries.
	ReverseNetwork string
	PrimaryNS      string
	Hostmaster     string
	Nameservers    []string
	DNSSEC         bool
	AllowTransfer  []string
	AlsoNotify     []string
	Masters        []string
	// SeedRecords asks for the usual starting records: the apex and www
	// pointing at this host. It defaults to on for a forward primary zone,
	// because a zone with nothing in it serves nothing.
	SeedRecords bool
}

// CreateZone records a zone and puts it on the host.
func (s *Service) CreateZone(ctx context.Context, actor Actor, requestID string,
	req CreateZoneRequest,
) (Zone, error) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return Zone{}, err
	}

	if req.Kind == "" {
		req.Kind = validate.ZoneMaster
	}
	if err := validate.ZoneKind(req.Kind); err != nil {
		return Zone{}, err
	}

	name := validate.NormalizeDomain(req.Name)
	if req.ReverseNetwork != "" {
		// Derived, never taken from the request alongside the network: a name
		// and a network that disagree produce a zone that is served and never
		// asked about.
		name, err = validate.ReverseZone(req.ReverseNetwork)
		if err != nil {
			return Zone{}, err
		}
	}
	if err := validate.Zone(name); err != nil {
		return Zone{}, err
	}

	params := CreateZoneParams{
		ServerID:       s.serverID,
		WebsiteID:      req.WebsiteID,
		Name:           name,
		Kind:           req.Kind,
		ReverseNetwork: req.ReverseNetwork,
		Refresh:        3600,
		Retry:          900,
		Expire:         1209600,
		Minimum:        3600,
		TTL:            settings.DefaultTTL,
		DNSSEC:         req.DNSSEC,
		// Empty rather than nil even for a secondary, whose name servers are
		// its primary's business: the column is NOT NULL, and a nil slice is a
		// NULL.
		Nameservers:   emptyIfNil(req.Nameservers),
		AllowTransfer: emptyIfNil(req.AllowTransfer),
		AlsoNotify:    emptyIfNil(req.AlsoNotify),
		Masters:       emptyIfNil(req.Masters),
	}
	if params.TTL == 0 {
		params.TTL = validate.DefaultTTL
	}

	if req.Kind == validate.ZoneMaster {
		params.Nameservers = emptyIfNil(req.Nameservers)
		if len(params.Nameservers) == 0 {
			params.Nameservers = settings.DefaultNS
		}
		if len(params.Nameservers) == 0 {
			// Refused rather than guessed. A zone whose NS records name a
			// server that is not this one, or name nothing, is a zone no
			// resolver will ever be sent to — and the panel cannot know what an
			// operator's name servers are called.
			return Zone{}, ErrNoNameservers
		}

		params.PrimaryNS = firstNonEmpty(req.PrimaryNS, settings.DefaultNS...)
		if params.PrimaryNS == "" {
			params.PrimaryNS = params.Nameservers[0]
		}
		params.Hostmaster = firstNonEmpty(req.Hostmaster, settings.Hostmaster)
		if params.Hostmaster == "" {
			params.Hostmaster = "hostmaster@" + name
		}

		if err := validate.Hostmaster(params.Hostmaster); err != nil {
			return Zone{}, err
		}
		for _, server := range append([]string{params.PrimaryNS}, params.Nameservers...) {
			if err := validate.Hostname(server); err != nil {
				return Zone{}, err
			}
		}
	} else if len(params.Masters) == 0 {
		return Zone{}, errors.New("a secondary zone must name at least one primary to transfer from")
	}

	for _, list := range [][]string{params.AllowTransfer, params.AlsoNotify, params.Masters} {
		for _, address := range list {
			if err := validate.MatchAddress(address); err != nil {
				return Zone{}, err
			}
		}
	}
	if req.DNSSEC {
		if err := s.requireDNSSEC(ctx, requestID); err != nil {
			return Zone{}, err
		}
	}

	if req.WebsiteID != "" {
		site, err := s.websites.LookupForDNS(ctx, req.WebsiteID)
		if err != nil {
			return Zone{}, err
		}
		params.WebsiteID = site.ID
	}

	zone, err := s.repo.CreateZone(ctx, params)
	if err != nil {
		return Zone{}, err
	}

	// Everything after the row exists rolls the row back on failure. A zone the
	// panel lists and the server does not serve is a panel that lies about what
	// is published — and a half-created zone also takes its own name, so the
	// operator's next attempt is refused as a duplicate of something that never
	// worked. That is what happened here before this was written.
	if err := s.finishZone(ctx, requestID, zone, req); err != nil {
		if removeErr := s.repo.DeleteZone(ctx, zone.ID); removeErr != nil {
			s.log.Error("could not remove a zone that was not completed",
				"zone", zone.Name, "error", removeErr.Error())
		}
		return Zone{}, err
	}

	s.record(ctx, actor, requestID, ActionZoneCreate, ResourceTypeZone, zone.ID, map[string]any{
		"zone":   zone.Name,
		"kind":   zone.Kind,
		"dnssec": zone.DNSSEC,
	})
	return s.repo.GetZone(ctx, zone.ID)
}

// finishZone writes a new zone's records and puts it on the host.
//
// Separated from CreateZone so that every failure after the row exists has one
// rollback rather than three that have to be kept in step.
func (s *Service) finishZone(ctx context.Context, requestID string, zone Zone,
	req CreateZoneRequest,
) error {
	if zone.Kind == validate.ZoneMaster && req.ReverseNetwork == "" {
		// The glue first, and not conditionally: a name server inside its own
		// zone that has no address record makes the zone unloadable, not
		// merely incomplete. named-checkzone refuses it outright, so a zone
		// created without this is a zone that cannot be served at all.
		if err := s.seedGlue(ctx, zone); err != nil {
			return err
		}
		if req.SeedRecords {
			if err := s.seedRecords(ctx, zone); err != nil {
				return err
			}
		}
	}
	return s.reconcile(ctx, requestID)
}

// seedGlue writes address records for the zone's own name servers.
//
// A name server named *inside* the zone it serves is a chicken-and-egg problem
// the DNS solves with glue: to find ns1.example.com a resolver has to ask
// example.com's name server, which is ns1.example.com. The address record in
// the zone itself is what breaks the loop, and without it the zone is not
// merely incomplete — named-checkzone refuses to load it, so the server will
// not start.
//
// This is the first thing this phase got wrong in practice: the panel created a
// zone whose default name servers were ns1 and ns2 inside it, wrote no glue,
// and BIND rejected the file with "NS 'ns1.example.test' has no address
// records". It was right to.
//
// Name servers *outside* the zone need no glue and get none: their addresses
// are somebody else's zone's business, and inventing a record for them here
// would publish an address this panel has no authority over.
func (s *Service) seedGlue(ctx context.Context, zone Zone) error {
	inZone := make([]string, 0, 2)
	for _, server := range append([]string{zone.PrimaryNS}, zone.Nameservers...) {
		name := validate.NormalizeDomain(server)
		if !strings.HasSuffix(name, "."+zone.Name) {
			// Outside the zone, or the apex itself — neither of which this
			// zone can be authoritative about beyond what is already here.
			continue
		}
		label := strings.TrimSuffix(name, "."+zone.Name)
		if label != "" && !contains(inZone, label) {
			inZone = append(inZone, label)
		}
	}
	if len(inZone) == 0 {
		return nil
	}

	address := s.address(ctx)
	if address == "" {
		// Refused with the reason, rather than left for named to refuse with a
		// line number. There is nothing sensible to invent here: an address
		// record pointing at a guess is worse than no zone.
		return fmt.Errorf(
			"%w: %s is inside this zone, so it needs an address record, and this "+
				"host's own address is not known — give the zone name servers "+
				"outside it, or record this server's address first",
			ErrNoNameservers, inZone[0]+"."+zone.Name)
	}

	recordType := validate.RecordA
	if strings.Contains(address, ":") {
		recordType = validate.RecordAAAA
	}
	for _, label := range inZone {
		if _, err := s.repo.CreateRecord(ctx, RecordParams{
			ZoneID: zone.ID, Name: label, Type: recordType, Value: address,
		}); err != nil && !errors.Is(err, ErrDuplicateRecord) {
			return err
		}
	}
	return nil
}

// contains reports whether a slice holds a value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// seedRecords writes the records a new zone starts with.
//
// The apex and www, pointing at this host, and nothing else. It is what makes a
// new zone serve the site it was created for rather than an empty answer — and
// it is skipped entirely when the panel does not know this host's address,
// because a zone with an A record pointing at nothing looks configured and is
// worse than a zone with none.
func (s *Service) seedRecords(ctx context.Context, zone Zone) error {
	address := s.address(ctx)
	if address == "" {
		return nil
	}
	recordType := validate.RecordA
	if strings.Contains(address, ":") {
		recordType = validate.RecordAAAA
	}

	for _, name := range []string{"@", "www"} {
		if _, err := s.repo.CreateRecord(ctx, RecordParams{
			ZoneID: zone.ID, Name: name, Type: recordType, Value: address,
		}); err != nil {
			return err
		}
	}
	return nil
}

// address reports this host's own address, or "" when it is not known.
//
// A failure here is logged and treated as "not known" rather than failing the
// operation: not seeding a record is a zone somebody has to add one line to,
// while failing is a zone they cannot create at all.
func (s *Service) address(ctx context.Context) string {
	if s.host == nil {
		return ""
	}
	address, err := s.host.Address(ctx)
	if err != nil {
		s.log.Warn("could not read this host's address", "error", err.Error())
		return ""
	}
	return address
}

// UpdateZoneRequest is a change to a zone. Nil fields are left alone.
type UpdateZoneRequest struct {
	PrimaryNS     *string
	Hostmaster    *string
	Refresh       *int
	Retry         *int
	Expire        *int
	Minimum       *int
	TTL           *int
	Nameservers   *[]string
	DNSSEC        *bool
	AllowTransfer *[]string
	AlsoNotify    *[]string
	Masters       *[]string
	WebsiteID     *string
}

// UpdateZone changes a zone and reconciles the host.
func (s *Service) UpdateZone(ctx context.Context, actor Actor, requestID, id string,
	req UpdateZoneRequest,
) (Zone, error) {
	existing, err := s.repo.GetZone(ctx, id)
	if err != nil {
		return Zone{}, err
	}

	if req.Hostmaster != nil {
		if err := validate.Hostmaster(*req.Hostmaster); err != nil {
			return Zone{}, err
		}
	}
	if req.PrimaryNS != nil {
		if err := validate.Hostname(*req.PrimaryNS); err != nil {
			return Zone{}, err
		}
	}
	if req.Nameservers != nil {
		if len(*req.Nameservers) == 0 && existing.Kind == validate.ZoneMaster {
			return Zone{}, ErrNoNameservers
		}
		for _, server := range *req.Nameservers {
			if err := validate.Hostname(server); err != nil {
				return Zone{}, err
			}
		}
	}
	for _, list := range []*[]string{req.AllowTransfer, req.AlsoNotify, req.Masters} {
		if list == nil {
			continue
		}
		for _, address := range *list {
			if err := validate.MatchAddress(address); err != nil {
				return Zone{}, err
			}
		}
	}
	if req.DNSSEC != nil && *req.DNSSEC && !existing.DNSSEC {
		if err := s.requireDNSSEC(ctx, requestID); err != nil {
			return Zone{}, err
		}
	}

	zone, err := s.repo.UpdateZone(ctx, id, UpdateZoneParams{
		PrimaryNS: req.PrimaryNS, Hostmaster: req.Hostmaster,
		Refresh: req.Refresh, Retry: req.Retry, Expire: req.Expire,
		Minimum: req.Minimum, TTL: req.TTL, Nameservers: req.Nameservers,
		DNSSEC: req.DNSSEC, AllowTransfer: req.AllowTransfer,
		AlsoNotify: req.AlsoNotify, Masters: req.Masters, WebsiteID: req.WebsiteID,
	})
	if err != nil {
		return Zone{}, err
	}

	if err := s.reconcile(ctx, requestID); err != nil {
		return Zone{}, err
	}

	details := map[string]any{"zone": zone.Name}
	if req.DNSSEC != nil && *req.DNSSEC != existing.DNSSEC {
		details["dnssec"] = *req.DNSSEC
	}
	s.record(ctx, actor, requestID, ActionZoneUpdate, ResourceTypeZone, zone.ID, details)
	return zone, nil
}

// DeleteZone stops serving a zone.
func (s *Service) DeleteZone(ctx context.Context, actor Actor, requestID, id string) error {
	zone, err := s.repo.GetZone(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteZone(ctx, id); err != nil {
		return err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}

	s.record(ctx, actor, requestID, ActionZoneDelete, ResourceTypeZone, id, map[string]any{
		"zone": zone.Name,
	})
	return nil
}

// RecordRequest is a record to write.
type RecordRequest struct {
	Name     string
	Type     string
	TTL      int
	Value    string
	Priority int
	Weight   int
	Port     int
	Flags    int
	Tag      string
}

// CreateRecord adds a record to a zone.
func (s *Service) CreateRecord(ctx context.Context, actor Actor, requestID, zoneID string,
	req RecordRequest,
) (Record, error) {
	zone, err := s.repo.GetZone(ctx, zoneID)
	if err != nil {
		return Record{}, err
	}
	params, err := s.checkRecord(ctx, zone, req, "")
	if err != nil {
		return Record{}, err
	}

	record, err := s.repo.CreateRecord(ctx, params)
	if err != nil {
		return Record{}, err
	}
	if err := s.publish(ctx, requestID, zone.ID); err != nil {
		if removeErr := s.repo.DeleteRecord(ctx, record.ID); removeErr != nil {
			s.log.Error("could not remove a record the host refused",
				"zone", zone.Name, "error", removeErr.Error())
		}
		return Record{}, err
	}

	s.record(ctx, actor, requestID, ActionRecordCreate, ResourceTypeRecord, record.ID,
		map[string]any{
			"zone": zone.Name, "name": record.Name,
			"type": record.Type, "value": record.Value,
		})
	return record, nil
}

// UpdateRecord replaces a record's contents.
func (s *Service) UpdateRecord(ctx context.Context, actor Actor, requestID, id string,
	req RecordRequest,
) (Record, error) {
	existing, err := s.repo.GetRecord(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if existing.Managed {
		return Record{}, ErrManagedRecord
	}
	zone, err := s.repo.GetZone(ctx, existing.ZoneID)
	if err != nil {
		return Record{}, err
	}
	params, err := s.checkRecord(ctx, zone, req, id)
	if err != nil {
		return Record{}, err
	}

	record, err := s.repo.UpdateRecord(ctx, id, params)
	if err != nil {
		return Record{}, err
	}
	if err := s.publish(ctx, requestID, zone.ID); err != nil {
		// Put back what was there. A record the host refused must not be left
		// in the panel's record as though it had been published.
		if _, restoreErr := s.repo.UpdateRecord(ctx, id, RecordParams{
			ZoneID: existing.ZoneID, Name: existing.Name, Type: existing.Type,
			TTL: existing.TTL, Value: existing.Value, Priority: existing.Priority,
			Weight: existing.Weight, Port: existing.Port, Flags: existing.Flags,
			Tag: existing.Tag,
		}); restoreErr != nil {
			s.log.Error("could not restore a record after the host refused a change",
				"record", id, "error", restoreErr.Error())
		}
		return Record{}, err
	}

	s.record(ctx, actor, requestID, ActionRecordUpdate, ResourceTypeRecord, record.ID,
		map[string]any{
			"zone": zone.Name, "name": record.Name,
			"type": record.Type, "value": record.Value,
		})
	return record, nil
}

// DeleteRecord removes a record.
func (s *Service) DeleteRecord(ctx context.Context, actor Actor, requestID, id string) error {
	existing, err := s.repo.GetRecord(ctx, id)
	if err != nil {
		return err
	}
	if existing.Managed {
		return ErrManagedRecord
	}
	zone, err := s.repo.GetZone(ctx, existing.ZoneID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteRecord(ctx, id); err != nil {
		return err
	}
	if err := s.publish(ctx, requestID, zone.ID); err != nil {
		return err
	}

	s.record(ctx, actor, requestID, ActionRecordDelete, ResourceTypeRecord, id, map[string]any{
		"zone": zone.Name, "name": existing.Name, "type": existing.Type,
	})
	return nil
}

// checkRecord validates a record against its type and its zone.
func (s *Service) checkRecord(ctx context.Context, zone Zone, req RecordRequest,
	excludeID string,
) (RecordParams, error) {
	if zone.Kind == validate.ZoneSlave {
		return RecordParams{}, ErrSlaveRecords
	}
	if err := validate.RecordType(req.Type); err != nil {
		return RecordParams{}, err
	}

	name := validate.NormalizeRecordName(req.Name, zone.Name)
	if err := validate.RecordName(name); err != nil {
		return RecordParams{}, err
	}
	if err := validate.TTL(req.TTL); err != nil {
		return RecordParams{}, err
	}

	value := strings.TrimSpace(req.Value)
	if req.Type == validate.RecordPTR && zone.ReverseNetwork != "" {
		// A PTR is addressed by the address it is for, which is friendlier and
		// is checkable: an owner name typed by hand into the wrong reverse zone
		// is a record no resolver ever asks this server for.
		if derived, err := validate.ReversePointerName(req.Name, zone.ReverseNetwork); err == nil {
			name = derived
		}
	}

	if err := validate.Record(validate.RecordValue{
		Type: req.Type, Value: value, Priority: req.Priority,
		Weight: req.Weight, Port: req.Port, Flags: req.Flags, Tag: req.Tag,
	}); err != nil {
		return RecordParams{}, err
	}

	if err := s.checkCNAME(ctx, zone.ID, name, req.Type, excludeID); err != nil {
		return RecordParams{}, err
	}

	return RecordParams{
		ZoneID: zone.ID, Name: name, Type: req.Type, TTL: req.TTL,
		Value: value, Priority: req.Priority, Weight: req.Weight,
		Port: req.Port, Flags: req.Flags, Tag: req.Tag,
	}, nil
}

// checkCNAME enforces the rule that a CNAME is alone at its name.
//
// RFC 1034: if a name has a CNAME, it has no other records. named-checkzone
// refuses a zone that breaks it, so without this check the failure would arrive
// as a rejected zone file — accurate, and about a file the operator never saw,
// naming a line number rather than the record they just added.
//
// The apex is the common case: a CNAME there collides with the SOA and NS
// records every zone has, which is why "CNAME at the apex" is a thing hosting
// support explains several times a week.
func (s *Service) checkCNAME(ctx context.Context, zoneID, name, recordType, excludeID string) error {
	if name == "@" && recordType == validate.RecordCNAME {
		return fmt.Errorf(
			"%w: at the zone's own name it would collide with the SOA and NS records every zone has",
			ErrCNAMEConflict)
	}

	records, err := s.repo.ListRecords(ctx, zoneID)
	if err != nil {
		return err
	}
	for _, existing := range records {
		if existing.ID == excludeID || existing.Name != name {
			continue
		}
		if recordType == validate.RecordCNAME || existing.Type == validate.RecordCNAME {
			return fmt.Errorf("%w: %s already has a record of type %s",
				ErrCNAMEConflict, name, existing.Type)
		}
	}
	return nil
}

// publish bumps a zone's serial and reconciles the host.
//
// The bump is not optional and is not an optimisation. The zone file is being
// rewritten, and a secondary compares serials to decide whether to transfer —
// so a rewritten zone at an unchanged serial is a change every secondary in the
// world ignores, indefinitely.
func (s *Service) publish(ctx context.Context, requestID, zoneID string) error {
	if err := s.repo.BumpSerial(ctx, zoneID); err != nil {
		return err
	}
	return s.reconcile(ctx, requestID)
}

// Configure saves the name server's settings.
func (s *Service) Configure(ctx context.Context, actor Actor, requestID string,
	settings Settings,
) (Settings, error) {
	settings.ServerID = s.serverID
	if settings.DNSSECPolicy == "" {
		settings.DNSSECPolicy = "default"
	}
	if settings.DefaultTTL == 0 {
		settings.DefaultTTL = validate.DefaultTTL
	}
	if settings.Hostmaster != "" {
		if err := validate.Hostmaster(settings.Hostmaster); err != nil {
			return Settings{}, err
		}
	}
	for _, server := range settings.DefaultNS {
		if err := validate.Hostname(server); err != nil {
			return Settings{}, err
		}
	}
	for _, list := range [][]string{settings.ListenOn, settings.AllowTransfer} {
		for _, address := range list {
			if err := validate.MatchAddress(address); err != nil {
				return Settings{}, err
			}
		}
	}

	settings.ListenOn = emptyIfNil(settings.ListenOn)
	settings.AllowTransfer = emptyIfNil(settings.AllowTransfer)
	settings.DefaultNS = emptyIfNil(settings.DefaultNS)

	saved, err := s.repo.SaveSettings(ctx, settings)
	if err != nil {
		return Settings{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return Settings{}, err
	}

	s.record(ctx, actor, requestID, ActionConfigure, ResourceTypeServer, s.serverID,
		map[string]any{"dnssec_policy": saved.DNSSECPolicy})
	return saved, nil
}

// Install puts a name server on the host.
func (s *Service) Install(ctx context.Context, actor Actor, requestID string) (
	agentclient.DNSStatus, error,
) {
	status, err := s.agent.DNSInstall(ctx, requestID)
	if err != nil {
		return status, err
	}
	s.record(ctx, actor, requestID, ActionInstall, ResourceTypeServer, s.serverID,
		map[string]any{"version": status.Version})
	return status, nil
}

// requireDNSSEC refuses signing on a server that cannot do it.
//
// Asked of the host rather than assumed, because the failure it prevents is the
// quiet kind: a zone recorded as signed on a server that cannot sign is a zone
// the panel reports as protected and that is served with no signatures at all.
func (s *Service) requireDNSSEC(ctx context.Context, requestID string) error {
	status, err := s.agent.DNSStatusOf(ctx, requestID)
	if err != nil {
		return err
	}
	if !status.SupportsDNSSEC {
		return fmt.Errorf("%w: %s has no dnssec-policy", ErrDNSSECUnsupported, status.Version)
	}
	return nil
}

// reconcile hands the Agent the complete set of zones.
func (s *Service) reconcile(ctx context.Context, requestID string) error {
	zones, err := s.repo.ListZones(ctx, s.serverID)
	if err != nil {
		return err
	}
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return err
	}

	desired := agentclient.DNSDesired{
		Settings: agentclient.DNSSettings{
			ListenOn:      settings.ListenOn,
			AllowTransfer: settings.AllowTransfer,
			DNSSECPolicy:  settings.DNSSECPolicy,
		},
		Zones: make([]agentclient.DNSZone, 0, len(zones)),
	}

	for _, zone := range zones {
		records, err := s.repo.ListRecords(ctx, zone.ID)
		if err != nil {
			return err
		}
		converted := make([]agentclient.DNSRecord, 0, len(records))
		for _, record := range records {
			converted = append(converted, agentclient.DNSRecord{
				Name: record.Name, Type: record.Type, TTL: record.TTL,
				Value: record.Value, Priority: record.Priority,
				Weight: record.Weight, Port: record.Port,
				Flags: record.Flags, Tag: record.Tag,
			})
		}
		desired.Zones = append(desired.Zones, agentclient.DNSZone{
			Name: zone.Name, Kind: zone.Kind, PrimaryNS: zone.PrimaryNS,
			Hostmaster: zone.Hostmaster, Serial: zone.Serial,
			Refresh: zone.Refresh, Retry: zone.Retry, Expire: zone.Expire,
			Minimum: zone.Minimum, TTL: zone.TTL,
			Nameservers: zone.Nameservers, Records: converted,
			DNSSEC: zone.DNSSEC, AllowTransfer: zone.AllowTransfer,
			AlsoNotify: zone.AlsoNotify, Masters: zone.Masters,
		})
	}

	result, err := s.agent.DNSReconcile(ctx, requestID, desired)
	if err != nil {
		return err
	}
	for _, warning := range result.Warnings {
		s.log.Warn("the DNS reconcile reported a problem", "warning", warning)
	}
	return nil
}

// record writes an audit event.
func (s *Service) record(ctx context.Context, actor Actor, requestID, action,
	resourceType, resourceID string, details map[string]any,
) {
	if s.audit == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	details["request_id"] = requestID

	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     details,
	})
}

// emptyIfNil returns a non-nil slice, so a JSON response carries [] rather than
// null and a database column takes an empty array rather than NULL.
func emptyIfNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// firstNonEmpty returns the first value that is not blank.
func firstNonEmpty(first string, rest ...string) string {
	if strings.TrimSpace(first) != "" {
		return strings.TrimSpace(first)
	}
	for _, candidate := range rest {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return ""
}

// ForWebsite returns the zones attached to one website.
//
// The site's own DNS tab uses it, so the tab does not have to fetch every zone
// on the host to show one.
func (s *Service) ForWebsite(ctx context.Context, websiteID string) ([]Zone, error) {
	zones, err := s.repo.ListZones(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	mine := make([]Zone, 0, 2)
	for _, zone := range zones {
		if zone.WebsiteID == websiteID {
			mine = append(mine, zone)
		}
	}
	return mine, nil
}
