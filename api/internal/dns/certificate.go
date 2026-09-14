package dns

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Pointing certificate names at this host before a certificate is issued.
//
// A certificate is issued over the HTTP-01 challenge, which means the CA
// resolves each name on it and fetches a file from whatever answers. A name
// that resolves nowhere, or resolves to a different machine, fails validation
// — and a failed validation spends one of a small number of attempts Let's
// Encrypt allows per hour before it refuses to look again for the rest of it.
//
// So the zones this panel serves are put in order first. It is the same act an
// operator would otherwise do by hand in another tab, one minute before
// clicking Issue and wondering why it failed.

// ActionCertificateAlign names the audit entry for this.
const ActionCertificateAlign = "dns.certificate.align"

// What happened to one name.
const (
	// AlignReady means a record already pointed the name at this host.
	AlignReady = "ready"
	// AlignAdded means the panel wrote the record that was missing.
	AlignAdded = "added"
	// AlignElsewhere means a record exists and names a different machine.
	AlignElsewhere = "elsewhere"
	// AlignAliased means the name is a CNAME, which may or may not lead here.
	AlignAliased = "aliased"
	// AlignNotServed means no zone on this panel covers the name.
	AlignNotServed = "not_served"
	// AlignNoAddress means this host's own address is not known.
	AlignNoAddress = "no_address"
)

// NameOutcome is what the panel found, and did, for one name.
type NameOutcome struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Zone   string `json:"zone,omitempty"`
	Detail string `json:"detail"`
}

// Blocking reports whether this outcome will stop the CA from validating.
//
// AlignNotServed and AlignAliased are not blocking: a name the panel does not
// serve is very often resolved perfectly well somewhere else, and this is not
// the place to declare somebody's working DNS broken.
func (o NameOutcome) Blocking() bool {
	return o.Status == AlignElsewhere || o.Status == AlignNoAddress
}

// Alignment is the whole report.
type Alignment struct {
	// Address is what the panel pointed names at, empty if it does not know.
	Address string        `json:"address"`
	Names   []NameOutcome `json:"names"`
	Added   int           `json:"added"`
	// Blocked counts names that will fail validation as things stand.
	Blocked int `json:"blocked"`
}

// AlignForCertificate points each name at this host in the zones it serves.
//
// It only ever adds a record that is missing. A record naming a different
// machine is reported and left alone: repointing a live domain is a far larger
// act than issuing a certificate, it can take a working site off the internet,
// and nobody clicking Issue is asking for it. The same reasoning the firewall
// page uses for port 53 — say what is wrong, do not silently fix it.
func (s *Service) AlignForCertificate(ctx context.Context, actor Actor, requestID string,
	names []string,
) (Alignment, error) {
	report := Alignment{Address: s.address(ctx)}

	zones, err := s.repo.ListZones(ctx, s.serverID)
	if err != nil {
		return Alignment{}, err
	}

	touched := make(map[string]bool)
	for _, raw := range names {
		name := validate.NormalizeDomain(raw)
		if name == "" {
			continue
		}
		outcome, changed := s.alignName(ctx, report.Address, zones, name)
		report.Names = append(report.Names, outcome)
		if changed != "" {
			touched[changed] = true
			report.Added++
		}
		if outcome.Blocking() {
			report.Blocked++
		}
	}

	if len(touched) > 0 {
		for zoneID := range touched {
			if err := s.publish(ctx, requestID, zoneID); err != nil {
				// The rows are written; the zone file is not. Returning the
				// error rather than the report: a caller told "added" about a
				// record the name server has never seen would issue against
				// DNS that does not exist yet.
				return Alignment{}, fmt.Errorf("publish the zone after adding a record: %w", err)
			}
		}
		s.record(ctx, actor, requestID, ActionCertificateAlign, "dns_zone", "",
			map[string]any{"names": names, "added": report.Added, "address": report.Address})
	}
	return report, nil
}

// alignName handles one name, returning what happened and the zone to publish.
func (s *Service) alignName(ctx context.Context, address string, zones []Zone,
	name string,
) (NameOutcome, string) {
	zone, label, ok := zoneFor(zones, name)
	if !ok {
		return NameOutcome{Name: name, Status: AlignNotServed,
			Detail: "no zone on this server covers this name, so its address is somebody else's to set"}, ""
	}

	records, err := s.repo.ListRecords(ctx, zone.ID)
	if err != nil {
		s.log.Warn("could not read a zone's records while preparing a certificate",
			"zone", zone.Name, "error", err.Error())
		return NameOutcome{Name: name, Zone: zone.Name, Status: AlignNotServed,
			Detail: "the zone's records could not be read"}, ""
	}

	for _, record := range records {
		if !strings.EqualFold(record.Name, label) {
			continue
		}
		switch record.Type {
		case validate.RecordCNAME:
			return NameOutcome{Name: name, Zone: zone.Name, Status: AlignAliased,
				Detail: "an alias to " + record.Value + ", which may or may not lead to this host"}, ""
		case validate.RecordA, validate.RecordAAAA:
			if address != "" && record.Value == address {
				return NameOutcome{Name: name, Zone: zone.Name, Status: AlignReady,
					Detail: "already points at this host"}, ""
			}
			return NameOutcome{Name: name, Zone: zone.Name, Status: AlignElsewhere,
				Detail: "points at " + record.Value +
					", so the certificate authority will look for its challenge on that machine"}, ""
		}
	}

	if address == "" {
		return NameOutcome{Name: name, Zone: zone.Name, Status: AlignNoAddress,
			Detail: "no record for this name, and this host's own address is not known — " +
				"a guess here is a domain pointed at the wrong machine"}, ""
	}

	recordType := validate.RecordA
	if strings.Contains(address, ":") {
		recordType = validate.RecordAAAA
	}
	if _, err := s.repo.CreateRecord(ctx, RecordParams{
		ZoneID: zone.ID, Name: label, Type: recordType, Value: address,
	}); err != nil && !errors.Is(err, ErrDuplicateRecord) {
		s.log.Warn("could not add an address record while preparing a certificate",
			"zone", zone.Name, "name", label, "error", err.Error())
		return NameOutcome{Name: name, Zone: zone.Name, Status: AlignNoAddress,
			Detail: "the record could not be written: " + err.Error()}, ""
	}

	return NameOutcome{Name: name, Zone: zone.Name, Status: AlignAdded,
		Detail: recordType + " record added, pointing at " + address}, zone.ID
}

// zoneFor finds the zone that holds a name, and the name's label within it.
//
// The longest match wins, which is the only answer that can be right: with
// both example.com and shop.example.com served here, a record for
// www.shop.example.com belongs in the second. Putting it in the first would
// write a name the delegated child zone overrides, and the record would have
// no effect that anybody could see.
func zoneFor(zones []Zone, name string) (Zone, string, bool) {
	var best Zone
	var label string
	found := false

	for _, zone := range zones {
		// A secondary holds a copy of somebody else's zone. Writing to it is
		// meaningless: the next transfer replaces whatever was written.
		if zone.Kind != validate.ZoneMaster || zone.ReverseNetwork != "" {
			continue
		}
		zoneName := validate.NormalizeDomain(zone.Name)

		var candidate string
		switch {
		case name == zoneName:
			candidate = "@"
		case strings.HasSuffix(name, "."+zoneName):
			candidate = strings.TrimSuffix(name, "."+zoneName)
		default:
			continue
		}

		if !found || len(zoneName) > len(validate.NormalizeDomain(best.Name)) {
			best, label, found = zone, candidate, true
		}
	}
	return best, label, found
}
