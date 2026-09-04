package mail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/validate"
)

// reconcile hands the host the whole of what it should serve.
//
// Every change in this package ends here, and it sends the complete
// configuration rather than the change — the same shape as the FTP and DNS
// phases, for the same reason: a host converged to a description can be
// reasoned about, and a host that has had a sequence of changes applied to it
// cannot.
//
// It is called after the database write and before the reply. That ordering is
// deliberate: a mailbox that exists in the panel and not on the host is
// visible and fixable, while one that exists on the host and not in the panel
// is a login nothing lists.
func (s *Service) reconcile(ctx context.Context, requestID string) error {
	desired, err := s.desired(ctx)
	if err != nil {
		return err
	}
	if _, err := s.agent.Do(ctx, protocol.Request{
		Operation: protocol.OperationMailReconcile,
		RequestID: requestID,
		Payload:   desired,
	}); err != nil {
		return fmt.Errorf("apply the mail configuration: %w", err)
	}
	return nil
}

// desired assembles the whole host configuration from the panel's record.
func (s *Service) desired(ctx context.Context) (map[string]any, error) {
	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	domains, err := s.repo.ListDomains(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	mailboxes, err := s.repo.AllMailboxes(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	aliases, err := s.repo.AllAliases(ctx, s.serverID)
	if err != nil {
		return nil, err
	}
	responders, err := s.repo.ListAutoresponders(ctx, s.serverID)
	if err != nil {
		return nil, err
	}

	certificate, key := s.certificate(ctx, settings)

	payloadDomains := make([]map[string]any, 0, len(domains))
	for _, domain := range domains {
		boxes := make([]map[string]any, 0, len(mailboxes[domain.ID]))
		for _, box := range mailboxes[domain.ID] {
			entry := map[string]any{
				"local_part":    box.LocalPart,
				"password_hash": box.PasswordHash(),
				"quota_mb":      box.QuotaMB,
				"active":        box.Active,
			}
			if responder, ok := responders[box.ID]; ok && responder.Active {
				entry["autoresponder"] = map[string]any{
					"subject":       responder.Subject,
					"body":          responder.Body,
					"interval_days": responder.IntervalDays,
					"active":        activeNow(responder),
				}
			}
			boxes = append(boxes, entry)
		}

		forwarders := make([]map[string]any, 0, len(aliases[domain.ID]))
		for _, alias := range aliases[domain.ID] {
			forwarders = append(forwarders, map[string]any{
				"source":      alias.Source,
				"destination": alias.Destination,
				"active":      alias.Active,
			})
		}

		payloadDomains = append(payloadDomains, map[string]any{
			"name":          domain.Domain,
			"active":        domain.Active,
			"catch_all":     domain.CatchAll,
			"dkim_selector": domain.DKIMSelector,
			"mailboxes":     boxes,
			"aliases":       forwarders,
		})
	}

	return map[string]any{
		"settings": map[string]any{
			"enabled":           settings.Enabled,
			"hostname":          settings.Hostname,
			"tls_certificate":   certificate,
			"tls_key":           key,
			"require_tls":       settings.RequireTLS,
			"spam_enabled":      settings.SpamEnabled,
			"spam_reject_score": settings.SpamRejectScore,
			"virus_enabled":     settings.VirusEnabled,
			"max_message_mb":    settings.MaxMessageMB,
		},
		"domains": payloadDomains,
	}, nil
}

// timeNow is the clock, replaceable in tests.
var timeNow = time.Now

// activeNow reports whether a vacation reply applies at this moment.
//
// The dates are evaluated here rather than on the host, because the host would
// need a scheduled job to notice an autoresponder expiring — and a reply that
// keeps firing three weeks after somebody came back is the failure everybody
// has seen.
//
// The cost is that the *next* reconcile is what turns it off. The panel
// reconciles on every mail change, so the boundary is soft; the alternative is
// a timer, which is a Phase 10 dependency this phase does not need.
func activeNow(responder Autoresponder) bool {
	if !responder.Active {
		return false
	}
	now := timeNow()
	if responder.StartsAt != nil && now.Before(*responder.StartsAt) {
		return false
	}
	if responder.EndsAt != nil && now.After(*responder.EndsAt) {
		return false
	}
	return true
}

// certificate resolves the material the mail server presents.
//
// Read fresh from the SSL phase on every reconcile rather than copied into the
// mail settings. A renewal moves the files, and a copied path is one that
// silently goes stale — leaving a mail server presenting a certificate that is
// not there, which is a daemon that will not start.
func (s *Service) certificate(ctx context.Context, settings Settings) (string, string) {
	if settings.TLSWebsiteID == "" || s.websites == nil {
		return "", ""
	}
	site, err := s.websites.LookupForMail(ctx, settings.TLSWebsiteID)
	if err != nil {
		s.log.Warn("could not read the mail server's certificate", "error", err)
		return "", ""
	}
	if !site.SSLEnabled {
		return "", ""
	}
	return site.CertificatePath, site.KeyPath
}

// ---------------------------------------------------------------- DNS records

// The records a mail domain needs, and what each one is for.
//
// All four are published together, because three of them are worthless
// individually. An SPF record with no MX describes a domain that receives no
// mail; a DKIM key with no DMARC policy is a signature nobody is asked to
// check; a DMARC policy with no SPF or DKIM is an instruction to reject the
// domain's own mail.

// publishRecords writes a domain's mail records into its zone.
//
// Where this host serves the zone. Where it does not, nothing is written and
// the overview says so — a panel that silently did nothing would leave an
// operator believing the records were published because the page said the
// policy was set.
func (s *Service) publishRecords(ctx context.Context, requestID string, domain Domain) error {
	if s.zones == nil {
		return nil
	}
	zoneID, found, err := s.zones.ZoneIDFor(ctx, domain.Domain)
	if err != nil || !found {
		return err
	}

	settings, err := s.repo.Settings(ctx, s.serverID)
	if err != nil {
		return err
	}
	if settings.Hostname == "" {
		return fmt.Errorf("the mail server has no hostname, so there is nothing for an MX to point at")
	}

	// The apex carries the MX and the SPF record.
	apex := []ManagedRecord{{
		Type: validate.RecordMX, Value: settings.Hostname, Priority: 10,
	}}
	if spf := spfRecord(domain.SPFPolicy); spf != "" {
		apex = append(apex, ManagedRecord{Type: validate.RecordTXT, Value: spf})
	}
	if err := s.zones.SetManagedRecords(ctx, requestID, zoneID, "@", apex); err != nil {
		return err
	}

	if domain.DKIMSelector != "" && domain.DKIMPublicKey != "" {
		name := domain.DKIMSelector + "._domainkey"
		if err := s.zones.SetManagedRecords(ctx, requestID, zoneID, name, []ManagedRecord{{
			Type:  validate.RecordTXT,
			Value: dkimRecord(domain.DKIMPublicKey),
		}}); err != nil {
			return err
		}
	}

	dmarc := []ManagedRecord{}
	if value := dmarcRecord(domain.DMARCPolicy, domain.DMARCRua); value != "" {
		dmarc = append(dmarc, ManagedRecord{Type: validate.RecordTXT, Value: value})
	}
	return s.zones.SetManagedRecords(ctx, requestID, zoneID, "_dmarc", dmarc)
}

// withdrawRecords removes the records for a domain that is being deleted.
//
// The MX first in intent if not in order: an MX pointing at a server that no
// longer accepts the domain's mail is how mail bounces for weeks after somebody
// thought they had cleaned up.
func (s *Service) withdrawRecords(ctx context.Context, requestID string, domain Domain) error {
	if s.zones == nil {
		return nil
	}
	zoneID, found, err := s.zones.ZoneIDFor(ctx, domain.Domain)
	if err != nil || !found {
		return err
	}

	names := []string{"@", "_dmarc"}
	if domain.DKIMSelector != "" {
		names = append(names, domain.DKIMSelector+"._domainkey")
	}
	for _, name := range names {
		if err := s.zones.SetManagedRecords(ctx, requestID, zoneID, name, nil); err != nil {
			return err
		}
	}
	return nil
}

// spfRecord builds the SPF policy.
//
// "mx a" rather than an explicit address, and that is the choice worth
// explaining. An ip4: mechanism naming this host's address is more precise and
// becomes wrong the day the server is renumbered — silently, because SPF failure
// is not an error anybody here would see. "mx a" authorises whatever the
// domain's own MX and A records point at, which is this host by construction and
// stays correct through a move.
//
// There is deliberately no way to ask for "+all". A record that authorises
// everybody is worse than no record: it tells every receiver that the forgery
// they are looking at is legitimate.
func spfRecord(policy string) string {
	switch policy {
	case validate.SPFSoft:
		return "v=spf1 mx a ~all"
	case validate.SPFStrict:
		return "v=spf1 mx a -all"
	default:
		return ""
	}
}

// dkimRecord wraps a public key in the tags a verifier looks for.
func dkimRecord(publicKey string) string {
	// k=rsa is stated rather than left to the default, because the default
	// differs between implementations and an unstated algorithm is one some
	// verifier guesses wrong.
	return "v=DKIM1; k=rsa; p=" + publicKey
}

// dmarcRecord builds the DMARC policy.
//
// The reporting address is included when there is one, and its absence is worth
// a word: without rua, a DMARC record still instructs receivers, and the domain
// owner never finds out what it did. The reports are how somebody discovers
// that their invoicing system was never in their SPF record — before they move
// the policy to reject and stop their own invoices.
func dmarcRecord(policy, rua string) string {
	if policy == validate.DMARCOff || policy == "" {
		return ""
	}
	var out strings.Builder
	out.WriteString("v=DMARC1; p=" + policy)
	if rua != "" {
		out.WriteString("; rua=mailto:" + rua)
	}
	// Apply the policy to every message rather than a sample. "pct=" defaults
	// to 100 and is left unstated for that reason: a value here would be a
	// number an operator has to reason about to no benefit.
	return out.String()
}

// agentRequest builds the quota request.
func agentRequest(requestID string, addresses []string) protocol.Request {
	return protocol.Request{
		Operation: protocol.OperationMailQuota,
		RequestID: requestID,
		Payload:   map[string]any{"addresses": addresses},
	}
}

// record writes an audit entry.
//
// Asynchronously, as the rest of the panel does: an audit write that failed
// must not undo a change that already succeeded, because the two would then
// disagree in the worse direction — a host that was changed and a trail saying
// it was not.
func (s *Service) record(ctx context.Context, actor Actor, action, resourceType,
	resourceID string, metadata map[string]any,
) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAsync(ctx, audit.Event{
		UserID:       actor.UserID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		IPAddress:    actor.IPAddress,
		UserAgent:    actor.UserAgent,
		Status:       "success",
		Metadata:     metadata,
	})
}
