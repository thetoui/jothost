package dns

import (
	"context"
	"errors"
	"fmt"

	"github.com/jothost/panel/shared/validate"
)

// Records the panel maintains on behalf of another phase.
//
// Phase 26 needs to publish four kinds of record for every mail domain — MX,
// SPF, DKIM and DMARC — and none of that belongs in the mail package. A DNS
// record is this package's to spell, to validate, and to get into a zone file;
// a second place that knew how to write one would be a second place that could
// write it differently.
//
// What the mail package gets instead is this: name a zone, name an owner, and
// hand over the values. The records are marked managed, which is what makes
// them visible on the DNS page and not editable there — a DKIM record edited by
// hand would leave the panel and the zone disagreeing about a key the panel is
// responsible for.

// ManagedRecord is one record another phase asks this one to publish.
type ManagedRecord struct {
	// Type is the record type, from validate's set.
	Type string
	// Value is the record's data.
	Value string
	// Priority applies to MX and is ignored otherwise.
	Priority int
	// TTL is the record's lifetime. Zero takes the zone's default.
	TTL int
}

// ZoneIDFor finds the zone this host serves for a domain.
//
// Exact matches only. A mail domain's records go in that domain's own zone, and
// falling back to a parent zone would put an MX for "mail.example.com" into
// "example.com" — which works, and is not what the operator asked for, and is
// invisible until somebody looks at the zone.
func (s *Service) ZoneIDFor(ctx context.Context, domain string) (string, bool, error) {
	zone, err := s.repo.ZoneByName(ctx, s.serverID, validate.NormalizeDomain(domain))
	if err != nil {
		if errors.Is(err, ErrZoneNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	return zone.ID, true, nil
}

// SetManagedRecords replaces the managed records at one owner name.
//
// Replaces, rather than adds: this is how a rotated DKIM key stops publishing
// its predecessor, and how turning SPF off actually removes the record instead
// of leaving one nobody maintains. An empty list is therefore a deletion, and a
// deliberate one.
//
// The zone's serial is bumped and the name server reloaded, because a rewritten
// zone at an unchanged serial is a change every secondary in the world ignores.
func (s *Service) SetManagedRecords(ctx context.Context, requestID, zoneID, name string,
	records []ManagedRecord,
) error {
	if zoneID == "" {
		return fmt.Errorf("a managed record needs a zone")
	}

	if _, err := s.repo.DeleteManagedRecords(ctx, zoneID, name); err != nil {
		return err
	}

	for _, record := range records {
		if err := validate.RecordType(record.Type); err != nil {
			return err
		}
		if _, err := s.repo.CreateRecord(ctx, RecordParams{
			ZoneID:   zoneID,
			Name:     name,
			Type:     record.Type,
			Value:    record.Value,
			Priority: record.Priority,
			TTL:      record.TTL,
			Managed:  true,
		}); err != nil && !errors.Is(err, ErrDuplicateRecord) {
			return err
		}
	}

	return s.publish(ctx, requestID, zoneID)
}

// PublishedValues reports what this host is actually serving at an owner name.
//
// The point of it is Phase 26's central comparison. A mail server signs with a
// key it has; the world verifies against a key DNS publishes; and the two are
// different facts. A panel that reported the first as though it were the second
// would say a domain is signed while every message it sends is treated as
// unsigned.
//
// This answers the second half — for the zones this host is authoritative for.
// It cannot answer for a domain whose DNS is somewhere else, and the caller has
// to say so rather than reporting an absence as a failure.
func (s *Service) PublishedValues(ctx context.Context, zoneID, name, recordType string) ([]string, error) {
	records, err := s.repo.ListRecords(ctx, zoneID)
	if err != nil {
		return nil, err
	}
	var values []string
	for _, record := range records {
		if record.Name == name && record.Type == recordType {
			values = append(values, record.Value)
		}
	}
	return values, nil
}
