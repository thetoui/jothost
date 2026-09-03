package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jothost/panel/agent/internal/dns"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// The DNS operations' request boundary.
//
// A request carries zones and records: names that have been checked to be
// domain names, values that have been checked against the grammar of their own
// record type, numbers, and addresses that have been parsed. It carries no
// paths — every file this phase writes is named after the zone, and a zone name
// contains no separator and cannot be ".." — and no configuration directives.
//
// The one thing that arrives looking like configuration is the address match
// list for transfers, and every element of it is re-checked here with
// validate.MatchAddress before it can reach a file named parses. That check is
// what makes the semicolons in the generated file syntax the panel produced,
// rather than text that arrived from somewhere.

// dnsPayload carries what these operations need.
type dnsPayload struct {
	Settings dnsSettingsPayload `json:"settings"`
	// Zones is the complete set this host should serve. Anything else the
	// panel put there is removed, which is what makes a delete take effect.
	Zones []dnsZonePayload `json:"zones"`
	// Zone names a single zone, for the operations that ask about one.
	Zone string `json:"zone"`
}

type dnsSettingsPayload struct {
	ListenOn      []string `json:"listen_on"`
	AllowTransfer []string `json:"allow_transfer"`
	DNSSECPolicy  string   `json:"dnssec_policy"`
}

type dnsZonePayload struct {
	Name        string             `json:"name"`
	Kind        string             `json:"kind"`
	PrimaryNS   string             `json:"primary_ns"`
	Hostmaster  string             `json:"hostmaster"`
	Serial      int64              `json:"serial"`
	Refresh     int                `json:"refresh"`
	Retry       int                `json:"retry"`
	Expire      int                `json:"expire"`
	Minimum     int                `json:"minimum"`
	TTL         int                `json:"ttl"`
	Nameservers []string           `json:"nameservers"`
	Records     []dnsRecordPayload `json:"records"`

	DNSSEC        bool     `json:"dnssec"`
	AllowTransfer []string `json:"allow_transfer"`
	AlsoNotify    []string `json:"also_notify"`
	Masters       []string `json:"masters"`
}

// dnsRecordPayload is one record as it arrives.
type dnsRecordPayload struct {
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

// desired converts a request into the state the provider works from, checking
// everything on the way.
//
// The checks are here as well as in the API on purpose. CLAUDE.md's rule is
// that the Agent validates what it is given rather than trusting a caller to
// have done it: the API and the Agent are separate programs, and a panel that
// only checked in one of them would be one deployment mistake away from writing
// unchecked text into a file a root daemon parses.
func (p dnsPayload) desired() (dns.Desired, error) {
	zones := make([]dns.Zone, 0, len(p.Zones))
	for _, zone := range p.Zones {
		converted, err := zone.zone()
		if err != nil {
			return dns.Desired{}, err
		}
		zones = append(zones, converted)
	}

	for _, list := range [][]string{p.Settings.ListenOn, p.Settings.AllowTransfer} {
		if err := checkMatchList(list); err != nil {
			return dns.Desired{}, err
		}
	}
	if err := checkPolicyName(p.Settings.DNSSECPolicy); err != nil {
		return dns.Desired{}, err
	}

	return dns.Desired{
		Settings: dns.Settings{
			ListenOn:      p.Settings.ListenOn,
			AllowTransfer: p.Settings.AllowTransfer,
			DNSSECPolicy:  p.Settings.DNSSECPolicy,
		},
		Zones: zones,
	}, nil
}

// zone converts and checks one zone.
func (z dnsZonePayload) zone() (dns.Zone, error) {
	if err := validate.Zone(z.Name); err != nil {
		return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}
	if err := validate.ZoneKind(z.Kind); err != nil {
		return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	if z.Kind == validate.ZoneMaster {
		if err := validate.Hostmaster(z.Hostmaster); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		if err := validate.Hostname(z.PrimaryNS); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		if err := validate.SOATimers(z.Refresh, z.Retry, z.Expire, z.Minimum); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		if z.TTL < validate.MinTTL || z.TTL > validate.MaxTTL {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload,
				fmt.Sprintf("a zone's default TTL must be between %d and %d seconds",
					validate.MinTTL, validate.MaxTTL), nil)
		}
		for _, server := range z.Nameservers {
			if err := validate.Hostname(server); err != nil {
				return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
			}
		}
	}

	for _, list := range [][]string{z.AllowTransfer, z.AlsoNotify, z.Masters} {
		if err := checkMatchList(list); err != nil {
			return dns.Zone{}, err
		}
	}

	records := make([]dns.Record, 0, len(z.Records))
	for _, record := range z.Records {
		if err := validate.RecordName(record.Name); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		if err := validate.TTL(record.TTL); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		if err := validate.Record(validate.RecordValue{
			Type:     record.Type,
			Value:    record.Value,
			Priority: record.Priority,
			Weight:   record.Weight,
			Port:     record.Port,
			Flags:    record.Flags,
			Tag:      record.Tag,
		}); err != nil {
			return dns.Zone{}, Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
		records = append(records, dns.Record{
			Name:     record.Name,
			Type:     record.Type,
			TTL:      record.TTL,
			Value:    record.Value,
			Priority: record.Priority,
			Weight:   record.Weight,
			Port:     record.Port,
			Flags:    record.Flags,
			Tag:      record.Tag,
		})
	}

	return dns.Zone{
		Name:          z.Name,
		Kind:          z.Kind,
		PrimaryNS:     z.PrimaryNS,
		Hostmaster:    z.Hostmaster,
		Serial:        z.Serial,
		Refresh:       z.Refresh,
		Retry:         z.Retry,
		Expire:        z.Expire,
		Minimum:       z.Minimum,
		TTL:           z.TTL,
		Nameservers:   z.Nameservers,
		Records:       records,
		DNSSEC:        z.DNSSEC,
		AllowTransfer: z.AllowTransfer,
		AlsoNotify:    z.AlsoNotify,
		Masters:       z.Masters,
	}, nil
}

// checkMatchList checks every element of an address match list.
func checkMatchList(addresses []string) error {
	for _, address := range addresses {
		if err := validate.MatchAddress(address); err != nil {
			return Fail(protocol.CodeInvalidPayload, err.Error(), err)
		}
	}
	return nil
}

// checkPolicyName checks a dnssec-policy name.
//
// It is written into a configuration file as a quoted string, so what matters
// is that it cannot end the string: a name is letters, digits, dashes and
// underscores, which is what BIND's own grammar allows anyway.
func checkPolicyName(name string) error {
	if name == "" {
		return nil
	}
	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9', char == '-', char == '_':
		default:
			return Fail(protocol.CodeInvalidPayload,
				fmt.Sprintf("a DNSSEC policy name may not contain %q", string(char)), nil)
		}
	}
	return nil
}

// dnsProvider returns the provider, or the error a caller should see when this
// host has no name server.
func (r *Registry) dnsProvider() (*dns.Provider, error) {
	if r.deps.DNS == nil || !r.deps.DNS.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"a DNS server is not installed on this host", nil)
	}
	return r.deps.DNS, nil
}

// handleDNSStatus reports the name server and what it serves.
func (r *Registry) handleDNSStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	if r.deps.DNS == nil {
		return structToMap(dns.Status{
			Zones: []string{}, ListenOn: []string{}, Reason: dns.ErrUnavailable.Error(),
		})
	}
	status := r.deps.DNS.Status(ctx, r.canInstallPackages())
	status.FirewallOpen, status.FirewallReason = r.dnsFirewallReport(ctx)
	return structToMap(status)
}

// handleDNSInstall puts a name server on the host.
func (r *Registry) handleDNSInstall(ctx context.Context, req protocol.Request,
	reporter *jobs.Reporter,
) (map[string]any, error) {
	var payload struct{}
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if r.deps.DNS != nil && r.deps.DNS.Available() {
		// Already there. Idempotence is what makes a retry safe.
		return structToMap(r.deps.DNS.Status(ctx, false))
	}
	if r.deps.PHPInstaller == nil || !r.deps.PHPInstaller.Available() {
		return nil, Fail(protocol.CodeUnsupported,
			"this host has no package manager the panel can install with", nil)
	}

	// The server, the checkers, and the DNSSEC tooling.
	//
	// The checkers are not optional. Without named-checkzone the panel would
	// write zone files it cannot validate, and one bad zone file is a server
	// that will not start — taking every other zone on the host with it.
	for _, pkg := range []string{"bind", "bind-tools", "bind-dnssec-tools"} {
		if err := r.deps.PHPInstaller.InstallPackage(ctx, pkg, reporterFunc(reporter)); err != nil {
			return nil, err
		}
	}

	if r.deps.DNS == nil {
		return map[string]any{"available": true}, nil
	}
	return structToMap(r.deps.DNS.Status(ctx, r.canInstallPackages()))
}

// handleDNSReconcile makes the host serve what the panel has recorded.
func (r *Registry) handleDNSReconcile(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.dnsProvider()
	if err != nil {
		return nil, err
	}
	var payload dnsPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}

	desired, err := payload.desired()
	if err != nil {
		return nil, err
	}

	result, err := provider.Reconcile(ctx, desired)
	if err != nil {
		return nil, dnsError(err)
	}
	return structToMap(result)
}

// handleDNSZoneStatus asks the running server about one zone.
func (r *Registry) handleDNSZoneStatus(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.dnsProvider()
	if err != nil {
		return nil, err
	}
	var payload dnsPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.Zone(payload.Zone); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	state, err := provider.ZoneStatus(ctx, payload.Zone)
	if err != nil {
		return nil, dnsError(err)
	}
	return structToMap(state)
}

// handleDNSSigning reports a zone's keys and the DS record its parent needs.
func (r *Registry) handleDNSSigning(ctx context.Context, req protocol.Request,
	_ *jobs.Reporter,
) (map[string]any, error) {
	provider, err := r.dnsProvider()
	if err != nil {
		return nil, err
	}
	var payload dnsPayload
	if err := decodePayload(req, &payload); err != nil {
		return nil, err
	}
	if err := validate.Zone(payload.Zone); err != nil {
		return nil, Fail(protocol.CodeInvalidPayload, err.Error(), err)
	}

	status, err := provider.Signing(ctx, payload.Zone)
	if err != nil {
		return nil, dnsError(err)
	}
	return structToMap(status)
}

// dnsFirewallReport says whether port 53 is actually reachable.
//
// Reported, not opened — the boundary Phases 17 and 7.1 drew, and for the same
// reason: a firewall change is its own act with its own protocol (CLAUDE.md
// section 19), and making one as a side effect of adding a DNS record would
// open a port without the operator seeing it and without the audit trail
// saying a firewall change happened.
//
// Saying it is worth the trouble here because of how DNS fails when the port is
// closed. The panel's own checks pass, `dig` from the server itself answers
// perfectly, and the zone is simply invisible from the internet — there is no
// error anywhere to find.
func (r *Registry) dnsFirewallReport(ctx context.Context) (bool, string) {
	provider := r.deps.Firewall
	if provider == nil || !provider.Available() {
		return true, ""
	}
	status, err := provider.Status(ctx)
	if err != nil {
		return false, "the firewall's rules could not be read: " + err.Error()
	}
	if !status.Enabled {
		return true, ""
	}

	for _, rule := range status.Rules {
		if !strings.EqualFold(rule.Action, "allow") {
			continue
		}
		if rule.Direction != "" && !strings.EqualFold(rule.Direction, "in") {
			continue
		}
		if portCovered(rule.Port, 53) {
			return true, ""
		}
	}
	return false, "the firewall is on and no rule allows port 53, so no resolver " +
		"outside this machine can look anything up here"
}

// dnsService starts the name server through the service manager.
//
// Through the service manager rather than by running named, so that one thing
// on this host owns the daemon's lifecycle: a panel that started named itself
// would produce a server the Services page reports as stopped while it is
// answering queries.
type dnsService struct{ registry *Registry }

// DNSServiceFor returns the starter for a registry, for the Agent to hand back
// to the DNS provider once both exist.
func DNSServiceFor(r *Registry) dns.Servicer { return dnsService{registry: r} }

// namedServiceNames are what the unit is called. Distributions disagree:
// Alpine and RHEL call it named, Debian calls it bind9.
var namedServiceNames = []string{"named", "bind9"}

func (d dnsService) Start(ctx context.Context) error {
	provider := d.registry.deps.Services
	if provider == nil || !provider.Available() {
		return fmt.Errorf("this host has no service manager to start the name server with")
	}

	var lastErr error
	for _, name := range namedServiceNames {
		_, unit, err := provider.UnitFor(ctx, name, nil)
		if err != nil {
			lastErr = err
			continue
		}
		// Restart rather than start: named reads its whole configuration at
		// startup, so a server that is already running has to be given the new
		// one, and restarting one that is stopped starts it.
		return provider.Restart(ctx, unit)
	}
	return fmt.Errorf("find the DNS service: %w", lastErr)
}

func (d dnsService) Running(ctx context.Context) bool {
	provider := d.registry.deps.Services
	if provider == nil || !provider.Available() {
		return false
	}
	for _, name := range namedServiceNames {
		status, err := provider.Status(ctx, name)
		if err == nil && status.Running {
			return true
		}
	}
	return false
}

// dnsError maps this package's errors onto protocol codes.
func dnsError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, dns.ErrUnavailable):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, dns.ErrNoDNSSEC):
		return Fail(protocol.CodeUnsupported, err.Error(), err)
	case errors.Is(err, dns.ErrNotRunning):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, dns.ErrUnknownZone):
		return Fail(protocol.CodeNotFound, err.Error(), err)
	case errors.Is(err, dns.ErrInvalidConfig), errors.Is(err, dns.ErrInvalidZone),
		errors.Is(err, dns.ErrForeignConfig):
		return Fail(protocol.CodeInvalidRequest, err.Error(), err)
	case errors.Is(err, validate.ErrInvalidZone), errors.Is(err, validate.ErrInvalidRecordValue),
		errors.Is(err, validate.ErrInvalidRecordName), errors.Is(err, validate.ErrInvalidRecordType),
		errors.Is(err, validate.ErrInvalidSOA), errors.Is(err, validate.ErrInvalidTTL):
		return Fail(protocol.CodeInvalidPayload, err.Error(), err)
	default:
		return Fail(protocol.CodeInternal, err.Error(), err)
	}
}
