package dns

import (
	"context"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// ImportRequest asks for a provider's copy of a zone to be brought in.
type ImportRequest struct {
	ZoneID     string `json:"-"`
	ProviderID string `json:"provider_id"`
	// Replace discards the panel's records for this zone and takes the
	// provider's as they are.
	//
	// Without it the import adds what the panel does not have and leaves
	// everything else alone, which is the answer for the ordinary case: a zone
	// already at Cloudflare that somebody wants to start managing here. With
	// it, the panel's copy becomes the provider's copy exactly.
	//
	// Either way this is a deliberate, separate action. The sync in the other
	// direction never runs it: a push that decided on its own to run backwards
	// is the two-way sync this design does not have.
	Replace bool `json:"replace"`
}

// ImportResult reports what came in.
type ImportResult struct {
	// Imported is how many records were added to the panel.
	Imported int `json:"imported"`
	// Replaced is how many of the panel's own records were discarded, which is
	// zero unless Replace was asked for.
	Replaced int `json:"replaced"`
	// Skipped names records the panel will not hold: the provider's own NS and
	// SOA, and anything of a type this panel does not support. Reported rather
	// than dropped in silence, because "your zone is now in the panel" and
	// "your zone is now in the panel apart from four records" are different
	// things to be told.
	Skipped []string `json:"skipped"`
}

// ImportZone reads a provider's copy of a zone into the panel.
//
// The counterpart to SyncZone and the reason this panel's Cloudflare support
// can honestly be called push-only: the direction is chosen by which endpoint
// is called, not by a heuristic about which side looks newer. Nothing here runs
// automatically, and nothing else in the panel calls it.
func (s *Service) ImportZone(ctx context.Context, actor Actor, requestID string,
	req ImportRequest,
) (ImportResult, error) {
	var result ImportResult
	result.Skipped = []string{}

	zone, err := s.repo.GetZone(ctx, req.ZoneID)
	if err != nil {
		return result, err
	}
	// A slave's records are transferred from its master and are not the
	// panel's to write, whatever a provider says it holds.
	if zone.Kind == validate.ZoneSlave {
		return result, ErrSlaveRecords
	}

	remote, err := s.remoteFor(ctx, req.ProviderID)
	if err != nil {
		return result, err
	}

	incoming, err := remote.FetchZone(ctx, zone.Name)
	if err != nil {
		if recordErr := s.repo.RecordSync(ctx, req.ProviderID, "failed", err.Error()); recordErr != nil {
			s.log.Error("could not record a failed DNS import", "error", recordErr.Error())
		}
		return result, err
	}

	existing, err := s.repo.ListRecords(ctx, zone.ID)
	if err != nil {
		return result, err
	}

	// Read before anything is written, so a zone whose provider copy is
	// unusable is left exactly as it was rather than half replaced.
	wanted := make([]RecordParams, 0, len(incoming))
	for _, item := range incoming {
		record := RecordParams{
			ZoneID:   zone.ID,
			Name:     relativeTo(item.Name, zone.Name),
			Type:     strings.ToUpper(item.Type),
			TTL:      item.TTL,
			Value:    item.Value,
			Priority: item.Priority,
			Weight:   item.Weight,
			Port:     item.Port,
			Flags:    item.Flags,
			Tag:      item.Tag,
		}

		// Validated on the way in exactly as a typed one is, through the same
		// functions the form goes through. A provider is not a trusted source
		// of records: it is a third party's database, and a value that would be
		// refused from a form has no business bypassing the check because it
		// arrived over HTTPS.
		if err := validateImported(record); err != nil {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s %s: %v", record.Type, record.Name, err))
			continue
		}
		wanted = append(wanted, record)
	}

	if req.Replace {
		removed, err := s.repo.DeleteUnmanagedRecords(ctx, zone.ID)
		if err != nil {
			return result, err
		}
		result.Replaced = removed
		existing = nil
	}

	held := make(map[string]struct{}, len(existing))
	for _, record := range existing {
		held[identity(record.Name, record.Type, record.Value)] = struct{}{}
	}

	for _, record := range wanted {
		if _, already := held[identity(record.Name, record.Type, record.Value)]; already {
			continue
		}
		if _, err := s.repo.CreateRecord(ctx, record); err != nil {
			return result, err
		}
		result.Imported++
	}

	s.record(ctx, actor, requestID, ActionImport, ResourceTypeZone, zone.ID,
		map[string]any{
			"zone":     zone.Name,
			"provider": req.ProviderID,
			"imported": result.Imported,
			"replaced": result.Replaced,
			"skipped":  len(result.Skipped),
		})

	// The zone file on the host is regenerated from the panel's records, so an
	// import that changed them has to be published or the name server goes on
	// answering with what was there before.
	if result.Imported > 0 || result.Replaced > 0 {
		if err := s.publish(ctx, zone.ID, requestID); err != nil {
			return result, fmt.Errorf("the records were imported but the zone was not published: %w", err)
		}
	}

	return result, nil
}

// identity is what makes two records the same record.
//
// Name, type and value, because that is what DNS answers with. TTL is left out
// deliberately: a record that differs only in its TTL is the same record, and
// counting it as new would import a duplicate on every run.
func identity(name, kind, value string) string {
	return strings.ToLower(name) + "\x00" +
		strings.ToUpper(kind) + "\x00" +
		strings.ToLower(strings.TrimSuffix(value, "."))
}

// validateImported runs an imported record through the same checks a typed one
// gets, and no fewer.
func validateImported(record RecordParams) error {
	if err := validate.RecordType(record.Type); err != nil {
		return err
	}
	if err := validate.RecordName(record.Name); err != nil {
		return err
	}
	if record.TTL != 0 {
		if err := validate.TTL(record.TTL); err != nil {
			return err
		}
	}
	return validate.Record(validate.RecordValue{
		Type:     record.Type,
		Value:    record.Value,
		Priority: record.Priority,
		Weight:   record.Weight,
		Port:     record.Port,
		Flags:    record.Flags,
		Tag:      record.Tag,
	})
}

// relativeTo turns a provider's fully qualified name into the zone-relative
// one the panel stores. It is qualify() in reverse.
func relativeTo(name, zone string) string {
	lower := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	apex := strings.TrimSuffix(strings.ToLower(zone), ".")

	if lower == apex || lower == "" {
		return "@"
	}
	if strings.HasSuffix(lower, "."+apex) {
		return lower[:len(lower)-len(apex)-1]
	}
	// Not inside the zone at all. Returned as it came rather than forced to
	// fit: it will fail validation above and be reported as skipped, which is
	// more useful than being silently rewritten into a name nobody asked for.
	return lower
}
