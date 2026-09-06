package dns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Publishing the same zone somewhere else.
//
// The panel's own name server is the authority for what a zone *should* say;
// a remote provider is another place to put it. The sync is therefore one-way,
// from the panel outwards, and it is a deliberate act rather than something
// that happens on every edit — a push that ran automatically would turn a typo
// into a change at a provider the operator may not have been looking at.
//
// Deleting is opt-in for the same reason. A Cloudflare zone usually holds
// records this panel never knew about — a page rule's helper record, a
// verification TXT somebody added in the dashboard, an email provider's
// setup — and a sync that removed everything it did not recognise would break
// them silently. Prune exists because "make it match exactly" is a real thing
// to want; it is not the default because "delete what I do not recognise" is a
// bad default for somebody else's DNS.

// RemoteRecord is one record on its way to a provider.
type RemoteRecord struct {
	// Name is fully qualified: providers address records by their whole name.
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

// SyncResult reports what a push did.
type SyncResult struct {
	// RemoteZoneID is the provider's own id for the zone, kept so a later sync
	// does not have to look it up.
	RemoteZoneID string `json:"remote_zone_id"`
	Created      int    `json:"created"`
	Updated      int    `json:"updated"`
	Deleted      int    `json:"deleted"`
	Unchanged    int    `json:"unchanged"`
	// Skipped names records the provider manages itself and the panel does not
	// touch — its own NS records, most often.
	Skipped []string `json:"skipped,omitempty"`
}

// Remote is a DNS provider somewhere else.
type Remote interface {
	// SyncZone makes the provider's copy of a zone hold these records.
	SyncZone(ctx context.Context, zone string, records []RemoteRecord, prune bool) (SyncResult, error)
	// FetchZone reads the provider's copy, for an import.
	FetchZone(ctx context.Context, zone string) ([]RemoteRecord, error)
}

// RemoteFactory builds a client for a provider kind.
type RemoteFactory func(kind, token, accountID string) (Remote, error)

// DefaultRemoteFactory is the factory the server wires in.
func DefaultRemoteFactory(kind, token, accountID string) (Remote, error) {
	switch kind {
	case "cloudflare":
		return NewCloudflare(token, accountID), nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownProvider, kind)
	}
}

// cloudflareAPI is the base URL, a variable so tests can point it at a stub.
var cloudflareAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare pushes zones to Cloudflare.
type Cloudflare struct {
	token     string
	accountID string
	client    *http.Client
}

// NewCloudflare builds a client.
func NewCloudflare(token, accountID string) *Cloudflare {
	return &Cloudflare{
		token:     token,
		accountID: accountID,
		// A bounded client, because this is an outbound call made while an
		// operator waits: without a timeout a provider having a bad day becomes
		// the panel having a bad day.
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// cfRecord is Cloudflare's shape for a record.
type cfRecord struct {
	ID       string  `json:"id,omitempty"`
	Type     string  `json:"type"`
	Name     string  `json:"name"`
	Content  string  `json:"content,omitempty"`
	TTL      int     `json:"ttl,omitempty"`
	Priority *int    `json:"priority,omitempty"`
	Data     *cfData `json:"data,omitempty"`
}

// cfData carries the composite types' fields.
//
// Cloudflare takes SRV and CAA as structured objects rather than as text, which
// suits this panel exactly: the numbers are already numbers here, and never
// have to be formatted into a string and parsed back.
type cfData struct {
	Priority *int   `json:"priority,omitempty"`
	Weight   *int   `json:"weight,omitempty"`
	Port     *int   `json:"port,omitempty"`
	Target   string `json:"target,omitempty"`
	Flags    *int   `json:"flags,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Value    string `json:"value,omitempty"`
}

// cfResponse is Cloudflare's envelope.
type cfResponse struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

// SyncZone makes Cloudflare's copy of a zone hold these records.
func (c *Cloudflare) SyncZone(ctx context.Context, zone string, records []RemoteRecord,
	prune bool,
) (SyncResult, error) {
	result := SyncResult{Skipped: []string{}}

	zoneID, err := c.zoneID(ctx, zone)
	if err != nil {
		return result, err
	}
	result.RemoteZoneID = zoneID

	existing, err := c.listRecords(ctx, zoneID)
	if err != nil {
		return result, err
	}

	// Keyed by what identifies a record to a resolver rather than by the
	// provider's id: the panel has no memory of which remote record is which,
	// and a key of (type, name, value) is what makes a second sync an update
	// rather than a duplicate.
	remaining := make(map[string]cfRecord, len(existing))
	for _, record := range existing {
		remaining[remoteKey(record.Type, record.Name, contentOf(record))] = record
	}

	for _, record := range records {
		// Cloudflare maintains the zone's own NS records and refuses changes to
		// them. Sending them produces an error about something the operator did
		// not ask for.
		if record.Type == validate.RecordNS && strings.EqualFold(record.Name, zone) {
			result.Skipped = append(result.Skipped,
				fmt.Sprintf("%s NS (Cloudflare maintains its own)", record.Name))
			continue
		}

		payload := toCloudflare(record)
		key := remoteKey(payload.Type, payload.Name, contentOf(payload))
		if current, found := remaining[key]; found {
			delete(remaining, key)
			if current.TTL == payload.TTL {
				result.Unchanged++
				continue
			}
			if err := c.updateRecord(ctx, zoneID, current.ID, payload); err != nil {
				return result, err
			}
			result.Updated++
			continue
		}

		if err := c.createRecord(ctx, zoneID, payload); err != nil {
			return result, err
		}
		result.Created++
	}

	if prune {
		for _, leftover := range remaining {
			if leftover.Type == "NS" || leftover.Type == "SOA" {
				result.Skipped = append(result.Skipped, leftover.Name+" "+leftover.Type)
				continue
			}
			if err := c.deleteRecord(ctx, zoneID, leftover.ID); err != nil {
				return result, err
			}
			result.Deleted++
		}
	}
	return result, nil
}

// remoteKey identifies a record by what a resolver sees.
func remoteKey(recordType, name, content string) string {
	return strings.ToLower(recordType + "|" + strings.TrimSuffix(name, ".") + "|" + content)
}

// contentOf renders a record's value for comparison.
func contentOf(record cfRecord) string {
	if record.Data == nil {
		return record.Content
	}
	data := record.Data
	if data.Tag != "" {
		return fmt.Sprintf("%d %s %s", deref(data.Flags), data.Tag, data.Value)
	}
	return fmt.Sprintf("%d %d %d %s",
		deref(data.Priority), deref(data.Weight), deref(data.Port),
		strings.TrimSuffix(data.Target, "."))
}

func deref(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func intPtr(value int) *int { return &value }

// toCloudflare converts a record to Cloudflare's shape.
func toCloudflare(record RemoteRecord) cfRecord {
	// Cloudflare's "automatic" TTL is 1, which is what a record with no TTL of
	// its own should get: the panel's zero means "the zone's default", and the
	// zone's default at Cloudflare is theirs, not this panel's.
	ttl := record.TTL
	if ttl == 0 {
		ttl = 1
	}

	out := cfRecord{
		Type: record.Type,
		Name: strings.TrimSuffix(record.Name, "."),
		TTL:  ttl,
	}
	switch record.Type {
	case validate.RecordMX:
		out.Content = strings.TrimSuffix(record.Value, ".")
		out.Priority = intPtr(record.Priority)
	case validate.RecordSRV:
		out.Data = &cfData{
			Priority: intPtr(record.Priority),
			Weight:   intPtr(record.Weight),
			Port:     intPtr(record.Port),
			Target:   strings.TrimSuffix(record.Value, "."),
		}
	case validate.RecordCAA:
		out.Data = &cfData{
			Flags: intPtr(record.Flags),
			Tag:   record.Tag,
			Value: record.Value,
		}
	case validate.RecordCNAME, validate.RecordNS, validate.RecordPTR:
		out.Content = strings.TrimSuffix(record.Value, ".")
	default:
		out.Content = record.Value
	}
	return out
}

// zoneID looks up Cloudflare's id for a zone.
func (c *Cloudflare) zoneID(ctx context.Context, zone string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/zones?name="+url.QueryEscape(zone), nil)
	if err != nil {
		return "", err
	}
	var zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &zones); err != nil {
		return "", fmt.Errorf("read the Cloudflare zone list: %w", err)
	}
	for _, candidate := range zones {
		if strings.EqualFold(candidate.Name, zone) {
			return candidate.ID, nil
		}
	}
	return "", fmt.Errorf("this Cloudflare account has no zone called %q; "+
		"the zone has to exist there before the panel can publish to it", zone)
}

// listRecords reads a zone's records, following Cloudflare's paging.
func (c *Cloudflare) listRecords(ctx context.Context, zoneID string) ([]cfRecord, error) {
	records := make([]cfRecord, 0, 32)
	// Paged, because a zone with more than a hundred records is ordinary and a
	// sync that read only the first page would recreate everything after it on
	// every run.
	for page := 1; page <= 20; page++ {
		path := fmt.Sprintf("/zones/%s/dns_records?per_page=100&page=%d", zoneID, page)
		raw, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var batch []cfRecord
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("read the Cloudflare records: %w", err)
		}
		records = append(records, batch...)
		if len(batch) < 100 {
			break
		}
	}
	return records, nil
}

func (c *Cloudflare) createRecord(ctx context.Context, zoneID string, record cfRecord) error {
	_, err := c.do(ctx, http.MethodPost, "/zones/"+zoneID+"/dns_records", record)
	return err
}

func (c *Cloudflare) updateRecord(ctx context.Context, zoneID, id string, record cfRecord) error {
	_, err := c.do(ctx, http.MethodPut, "/zones/"+zoneID+"/dns_records/"+id, record)
	return err
}

func (c *Cloudflare) deleteRecord(ctx context.Context, zoneID, id string) error {
	_, err := c.do(ctx, http.MethodDelete, "/zones/"+zoneID+"/dns_records/"+id, nil)
	return err
}

// do makes one API call.
func (c *Cloudflare) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode the Cloudflare request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	request, err := http.NewRequestWithContext(ctx, method, cloudflareAPI+path, reader)
	if err != nil {
		return nil, fmt.Errorf("build the Cloudflare request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")

	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("call Cloudflare: %w", err)
	}
	defer response.Body.Close()

	var envelope cfResponse
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("read Cloudflare's reply (HTTP %d): %w", response.StatusCode, err)
	}
	if !envelope.Success {
		// Cloudflare's own message, which says what is wrong far better than a
		// status code does — "record already exists", "invalid TTL for a free
		// zone". The token is not in it and is never logged.
		messages := make([]string, 0, len(envelope.Errors))
		for _, apiError := range envelope.Errors {
			messages = append(messages, apiError.Message)
		}
		if len(messages) == 0 {
			messages = append(messages, fmt.Sprintf("HTTP %d", response.StatusCode))
		}
		return nil, fmt.Errorf("Cloudflare refused the request: %s", strings.Join(messages, "; "))
	}
	return envelope.Result, nil
}

// FetchZone reads a provider's copy of a zone.
//
// The other direction, and deliberately a separate method rather than a flag on
// SyncZone. A push and an import are opposite operations on the same data, and
// a single entry point that did one or the other depending on an argument is
// how somebody eventually passes the wrong argument and overwrites the side
// they meant to keep.
func (c *Cloudflare) FetchZone(ctx context.Context, zone string) ([]RemoteRecord, error) {
	id, err := c.zoneID(ctx, zone)
	if err != nil {
		return nil, err
	}

	remote, err := c.listRecords(ctx, id)
	if err != nil {
		return nil, err
	}

	records := make([]RemoteRecord, 0, len(remote))
	for _, item := range remote {
		// SOA and the zone's own NS records belong to whoever serves it.
		// Cloudflare's name servers are not the panel's to hold, and importing
		// them would produce records the panel shows as editable and every
		// later push refuses.
		if item.Type == "SOA" {
			continue
		}
		if item.Type == "NS" && strings.EqualFold(strings.TrimSuffix(item.Name, "."),
			strings.TrimSuffix(zone, ".")) {
			continue
		}

		record := RemoteRecord{
			Name:     item.Name,
			Type:     item.Type,
			TTL:      item.TTL,
			Value:    contentOf(item),
			Priority: deref(item.Priority),
		}
		// Cloudflare's automatic TTL is 1, which is its way of saying "we
		// decide". The panel stores that as zero, its own way of saying "the
		// zone's default" — importing it as a literal one-second TTL would be
		// a lie the next push would send straight back.
		if record.TTL == 1 {
			record.TTL = 0
		}
		if item.Data != nil {
			record.Weight = deref(item.Data.Weight)
			record.Port = deref(item.Data.Port)
			record.Flags = deref(item.Data.Flags)
			record.Tag = item.Data.Tag
		}
		records = append(records, record)
	}
	return records, nil
}
