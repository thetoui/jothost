package dns

import (
	"context"
	"errors"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// AddProviderRequest is a set of remote credentials to record.
type AddProviderRequest struct {
	Kind      string
	Label     string
	Token     string
	AccountID string
}

// AddProvider records a remote provider's credentials.
//
// The repository encrypts the token before storing it, against the row's own
// id. It cannot be hashed the way a password is — it has to be sent to the
// provider on every call — and it is a credential that can rewrite every DNS
// record in somebody's account, which is why it is not stored as it arrived.
//
// The credentials are checked far enough to know the panel has a client for
// this kind of provider. They are not used to make a call: an operator who
// pastes a token should be able to save it and sync when they are ready.
func (s *Service) AddProvider(ctx context.Context, actor Actor, requestID string,
	req AddProviderRequest,
) (Provider, error) {
	if strings.TrimSpace(req.Token) == "" {
		return Provider{}, errors.New("a provider needs an API token")
	}
	if _, err := s.buildRemote(req.Kind, req.Token, req.AccountID); err != nil {
		return Provider{}, err
	}

	provider, err := s.repo.CreateProvider(ctx, s.serverID, req.Kind, req.Label,
		req.Token, req.AccountID)
	if err != nil {
		return Provider{}, err
	}

	// The token is not in the audit record, and neither is any part of it.
	s.record(ctx, actor, requestID, ActionProviderAdd, ResourceTypeProvider, provider.ID,
		map[string]any{"kind": provider.Kind, "label": provider.Label})
	return provider, nil
}

// RemoveProvider forgets a remote provider.
//
// Nothing is deleted at the provider. The records this panel pushed there are
// somebody's live DNS, and a panel that withdrew them because an operator
// removed a stored credential would take a domain off the internet as a side
// effect of tidying up.
func (s *Service) RemoveProvider(ctx context.Context, actor Actor, requestID, id string) error {
	if err := s.repo.DeleteProvider(ctx, id); err != nil {
		return err
	}
	s.record(ctx, actor, requestID, ActionProviderRemove, ResourceTypeProvider, id, nil)
	return nil
}

// SyncRequest is a push to a provider.
type SyncRequest struct {
	ZoneID     string
	ProviderID string
	// Prune deletes records at the provider that the panel does not have.
	//
	// Off by default, deliberately. A provider's zone usually holds records
	// this panel never knew about — a verification TXT added in their
	// dashboard, an email provider's setup — and removing what it does not
	// recognise would break them with no warning.
	Prune bool
}

// SyncZone publishes a zone to a remote provider.
func (s *Service) SyncZone(ctx context.Context, actor Actor, requestID string,
	req SyncRequest,
) (SyncResult, error) {
	var result SyncResult

	zone, err := s.repo.GetZone(ctx, req.ZoneID)
	if err != nil {
		return result, err
	}
	if zone.Kind == validate.ZoneSlave {
		return result, ErrSlaveRecords
	}

	remote, err := s.remoteFor(ctx, req.ProviderID)
	if err != nil {
		return result, err
	}

	records, err := s.repo.ListRecords(ctx, zone.ID)
	if err != nil {
		return result, err
	}

	outbound := make([]RemoteRecord, 0, len(records)+len(zone.Nameservers))
	for _, record := range records {
		outbound = append(outbound, RemoteRecord{
			Name:     qualify(record.Name, zone.Name),
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
	// The zone's NS records live in the zone table rather than the record
	// table, so they have to be added here or a synced zone would arrive
	// without them. A provider that maintains its own says so and they are
	// reported as skipped.
	for _, server := range zone.Nameservers {
		outbound = append(outbound, RemoteRecord{
			Name: zone.Name, Type: validate.RecordNS, Value: server, TTL: zone.TTL,
		})
	}

	result, err = remote.SyncZone(ctx, zone.Name, outbound, req.Prune)
	if err != nil {
		// Recorded as a failure rather than swallowed: a provider that has been
		// failing quietly for a month is the thing this field exists to make
		// visible.
		if recordErr := s.repo.RecordSync(ctx, req.ProviderID, "failed", err.Error()); recordErr != nil {
			s.log.Error("could not record a failed DNS sync", "error", recordErr.Error())
		}
		return result, err
	}

	if err := s.repo.RecordSync(ctx, req.ProviderID, "ok", ""); err != nil {
		s.log.Error("could not record a DNS sync", "error", err.Error())
	}

	s.record(ctx, actor, requestID, ActionSync, ResourceTypeZone, zone.ID, map[string]any{
		"zone":    zone.Name,
		"created": result.Created,
		"updated": result.Updated,
		"deleted": result.Deleted,
		"prune":   req.Prune,
	})
	return result, nil
}

// remoteFor builds a client for a stored provider.
func (s *Service) remoteFor(ctx context.Context, providerID string) (Remote, error) {
	kind, token, err := s.repo.ProviderToken(ctx, providerID)
	if err != nil {
		return nil, err
	}

	providers, err := s.repo.ListProviders(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	accountID := ""
	for _, provider := range providers {
		if provider.ID == providerID {
			accountID = provider.AccountID
		}
	}
	return s.buildRemote(kind, token, accountID)
}

// buildRemote makes a client, or says the panel has none for that kind.
func (s *Service) buildRemote(kind, token, accountID string) (Remote, error) {
	if s.remote == nil {
		return nil, ErrUnknownProvider
	}
	return s.remote(kind, token, accountID)
}

// qualify turns a relative owner name into a full one.
func qualify(name, zone string) string {
	if name == "@" {
		return zone
	}
	return name + "." + zone
}

// ---------------------------------------------------------------- subdomains

// EnsureSubdomainRecords publishes a subdomain in its parent's zone.
//
// This is the item Phase 4.1 deferred: at the time there was no zone to put a
// record in, and the note in docs/PHASE4.1.md said it would be a small addition
// once one existed. It is.
//
// It does nothing when the parent has no zone here, which is the common case: a
// subdomain does not need this panel's DNS to work — it is reached through
// whatever already resolves the parent — so a panel that failed subdomain
// creation because it does not host the parent's DNS would be refusing to do
// something it was never asked to do.
func (s *Service) EnsureSubdomainRecords(ctx context.Context, requestID, parent, name, address string) error {
	zone, err := s.repo.ZoneByName(ctx, s.serverID, validate.NormalizeDomain(parent))
	if err != nil {
		if errors.Is(err, ErrZoneNotFound) {
			return nil
		}
		return err
	}
	if zone.Kind == validate.ZoneSlave {
		// The parent's contents come from its primary. Writing here would be a
		// change that is silently replaced at the next transfer.
		return nil
	}

	label := validate.NormalizeRecordName(name, zone.Name)
	if label == "@" {
		return nil
	}
	if address == "" {
		address = s.address(ctx)
	}
	if address == "" {
		return nil
	}

	recordType := validate.RecordA
	if strings.Contains(address, ":") {
		recordType = validate.RecordAAAA
	}

	// Removed and rewritten rather than updated in place: the panel's managed
	// records for a name are whatever this function last wrote, and an address
	// that changed family — a site moved from IPv4 to IPv6 — would otherwise
	// leave the old record behind pointing at a host that no longer answers.
	if _, err := s.repo.DeleteManagedRecords(ctx, zone.ID, label); err != nil {
		return err
	}

	if _, err := s.repo.CreateRecord(ctx, RecordParams{
		ZoneID: zone.ID, Name: label, Type: recordType, Value: address, Managed: true,
	}); err != nil {
		if errors.Is(err, ErrDuplicateRecord) {
			// An operator has already published exactly this by hand. Their
			// record is left alone: it says the same thing, and taking it over
			// would mean deleting a row they own to write an identical one.
			return nil
		}
		return err
	}

	return s.publish(ctx, requestID, zone.ID)
}

// RemoveSubdomainRecords withdraws what EnsureSubdomainRecords published.
//
// Only the panel's own records: a TXT an operator added at the same name — a
// verification token, a service's own record — survives the subdomain being
// deleted, because it was never this function's to remove.
func (s *Service) RemoveSubdomainRecords(ctx context.Context, requestID, parent, name string) error {
	zone, err := s.repo.ZoneByName(ctx, s.serverID, validate.NormalizeDomain(parent))
	if err != nil {
		if errors.Is(err, ErrZoneNotFound) {
			return nil
		}
		return err
	}

	label := validate.NormalizeRecordName(name, zone.Name)
	removed, err := s.repo.DeleteManagedRecords(ctx, zone.ID, label)
	if err != nil {
		return err
	}
	if removed == 0 {
		return nil
	}
	return s.publish(ctx, requestID, zone.ID)
}
