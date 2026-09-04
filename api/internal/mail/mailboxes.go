package mail

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jothost/panel/shared/crypt"
	"github.com/jothost/panel/shared/validate"
)

// MailboxRequest is a mailbox being created or changed.
type MailboxRequest struct {
	LocalPart string `json:"local_part"`
	Password  string `json:"password"`
	QuotaMB   *int   `json:"quota_mb"`
	Active    *bool  `json:"active"`
}

// MailboxView is a mailbox together with how full it is.
type MailboxView struct {
	Mailbox
	// Address is the full address, so a page does not have to join it.
	Address string `json:"address"`
	// UsedMB is what Dovecot reports. Known is false when the panel could not
	// ask — a mailbox nobody has logged into has no Maildir yet, and a host
	// the Agent cannot reach has no answer at all. Reporting either as zero
	// would show a full mailbox as having room.
	UsedMB int  `json:"used_mb"`
	Known  bool `json:"quota_known"`
	// Autoresponder is the vacation reply, when there is one.
	Autoresponder *Autoresponder `json:"autoresponder,omitempty"`
}

// CreateMailbox adds a mailbox.
//
// The password is hashed here, at the HTTP boundary, and the plaintext goes no
// further: not into the database, not over the socket to the privileged Agent,
// not into a log. What the rest of the panel handles is a hash, which cannot be
// replayed against IMAP.
func (s *Service) CreateMailbox(ctx context.Context, actor Actor, requestID, domainID string,
	req MailboxRequest,
) (Mailbox, error) {
	domain, err := s.repo.GetDomain(ctx, domainID)
	if err != nil {
		return Mailbox{}, err
	}

	local := validate.NormalizeLocalPart(req.LocalPart)
	if err := validate.MailLocalPart(local); err != nil {
		return Mailbox{}, err
	}
	if err := checkPassword(req.Password); err != nil {
		return Mailbox{}, err
	}

	box := Mailbox{DomainID: domainID, LocalPart: local, QuotaMB: 2048, Active: true}
	if req.QuotaMB != nil {
		box.QuotaMB = *req.QuotaMB
	}
	if req.Active != nil {
		box.Active = *req.Active
	}
	if err := validate.MailQuotaMB(box.QuotaMB); err != nil {
		return Mailbox{}, err
	}

	hash, err := crypt.SchemedHash(req.Password)
	if err != nil {
		return Mailbox{}, fmt.Errorf("hash the mailbox password: %w", err)
	}

	created, err := s.repo.CreateMailbox(ctx, box, hash)
	if err != nil {
		return Mailbox{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return created, err
	}

	s.record(ctx, actor, ActionMailboxCreate, ResourceTypeMailbox, created.ID, map[string]any{
		"address":  local + "@" + domain.Domain,
		"quota_mb": created.QuotaMB,
	})
	return created, nil
}

// UpdateMailbox changes a mailbox's quota or suspends it.
func (s *Service) UpdateMailbox(ctx context.Context, actor Actor, requestID, id string,
	req MailboxRequest,
) (Mailbox, error) {
	existing, err := s.repo.GetMailbox(ctx, id)
	if err != nil {
		return Mailbox{}, err
	}

	quota := existing.QuotaMB
	if req.QuotaMB != nil {
		quota = *req.QuotaMB
	}
	if err := validate.MailQuotaMB(quota); err != nil {
		return Mailbox{}, err
	}
	active := existing.Active
	if req.Active != nil {
		active = *req.Active
	}

	updated, err := s.repo.UpdateMailbox(ctx, id, quota, active)
	if err != nil {
		return Mailbox{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return updated, err
	}

	s.record(ctx, actor, ActionMailboxUpdate, ResourceTypeMailbox, id, map[string]any{
		"quota_mb": updated.QuotaMB,
		"active":   updated.Active,
	})
	return updated, nil
}

// SetPassword replaces a mailbox's password.
//
// Audited with the mailbox named, and that is the point of auditing it: this is
// the one action in the panel that grants the ability to read every message in
// somebody's mailbox, silently, leaving no trace the owner will ever see. The
// audit record says it happened and who did it. It does not say what the
// password is.
func (s *Service) SetPassword(ctx context.Context, actor Actor, requestID, id,
	password string,
) error {
	box, err := s.repo.GetMailbox(ctx, id)
	if err != nil {
		return err
	}
	if err := checkPassword(password); err != nil {
		return err
	}

	hash, err := crypt.SchemedHash(password)
	if err != nil {
		return fmt.Errorf("hash the mailbox password: %w", err)
	}
	if err := s.repo.SetPassword(ctx, id, hash); err != nil {
		return err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}

	domain, err := s.repo.GetDomain(ctx, box.DomainID)
	if err != nil {
		return err
	}
	s.record(ctx, actor, ActionMailboxPassword, ResourceTypeMailbox, id, map[string]any{
		"address": box.LocalPart + "@" + domain.Domain,
	})
	return nil
}

// DeleteMailbox removes a mailbox.
//
// The Maildir is deliberately left on the host. Deleting a row is a panel
// action and can be undone by recreating it; deleting somebody's mail is not,
// and a control panel should not do the irreversible half as a side effect of
// the reversible one. The files are the operator's to remove, and the
// documentation says so.
func (s *Service) DeleteMailbox(ctx context.Context, actor Actor, requestID, id string) error {
	box, err := s.repo.GetMailbox(ctx, id)
	if err != nil {
		return err
	}
	domain, err := s.repo.GetDomain(ctx, box.DomainID)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteMailbox(ctx, id); err != nil {
		return err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}

	s.record(ctx, actor, ActionMailboxDelete, ResourceTypeMailbox, id, map[string]any{
		"address": box.LocalPart + "@" + domain.Domain,
		"note":    "the mailbox's messages are still on the host",
	})
	return nil
}

// ListMailboxes reads a domain's mailboxes with their usage.
func (s *Service) ListMailboxes(ctx context.Context, requestID, domainID string) ([]MailboxView, error) {
	domain, err := s.repo.GetDomain(ctx, domainID)
	if err != nil {
		return nil, err
	}
	boxes, err := s.repo.ListMailboxes(ctx, domainID)
	if err != nil {
		return nil, err
	}
	responders, err := s.repo.ListAutoresponders(ctx, s.serverID)
	if err != nil {
		return nil, err
	}

	addresses := make([]string, 0, len(boxes))
	for _, box := range boxes {
		addresses = append(addresses, box.LocalPart+"@"+domain.Domain)
	}
	usage := s.quotaUsage(ctx, requestID, addresses)

	views := make([]MailboxView, 0, len(boxes))
	for _, box := range boxes {
		address := box.LocalPart + "@" + domain.Domain
		view := MailboxView{Mailbox: box, Address: address}
		if entry, ok := usage[address]; ok {
			view.UsedMB = entry.usedMB
			view.Known = entry.known
		}
		if responder, ok := responders[box.ID]; ok {
			copied := responder
			view.Autoresponder = &copied
		}
		views = append(views, view)
	}
	return views, nil
}

// quotaEntry is one mailbox's usage as the host reports it.
type quotaEntry struct {
	usedMB int
	known  bool
}

// quotaUsage asks the host how full each mailbox is.
//
// A failure here is not a failure of the page. A host that cannot be reached
// leaves every mailbox's usage unknown, and unknown is shown as unknown — the
// alternative, showing zero, would tell a customer their full mailbox is empty.
func (s *Service) quotaUsage(ctx context.Context, requestID string,
	addresses []string,
) map[string]quotaEntry {
	usage := map[string]quotaEntry{}
	if len(addresses) == 0 {
		return usage
	}

	response, err := s.agent.Do(ctx, agentRequest(requestID, addresses))
	if err != nil {
		s.log.Warn("could not read mailbox usage", "error", err)
		return usage
	}
	raw, ok := response.Data["usage"].(map[string]any)
	if !ok {
		return usage
	}
	for address, value := range raw {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		known, _ := entry["known"].(bool)
		used, _ := entry["used_mb"].(float64)
		usage[address] = quotaEntry{usedMB: int(used), known: known}
	}
	return usage
}

// AliasRequest is a forwarder being created.
type AliasRequest struct {
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Active      *bool  `json:"active"`
}

// CreateAlias adds a forwarder.
func (s *Service) CreateAlias(ctx context.Context, actor Actor, requestID, domainID string,
	req AliasRequest,
) (Alias, error) {
	domain, err := s.repo.GetDomain(ctx, domainID)
	if err != nil {
		return Alias{}, err
	}

	source := validate.NormalizeLocalPart(req.Source)
	if err := validate.MailLocalPart(source); err != nil {
		return Alias{}, err
	}
	destination, err := validate.MailAddress(req.Destination)
	if err != nil {
		return Alias{}, err
	}
	// A forwarder that points at itself is a delivery loop, and Postfix
	// discovers it by bouncing the message after twenty hops rather than by
	// refusing to load the table.
	if destination == source+"@"+domain.Domain {
		return Alias{}, fmt.Errorf("a forwarder cannot point at itself")
	}

	alias := Alias{
		DomainID: domainID, Source: source, Destination: destination, Active: true,
	}
	if req.Active != nil {
		alias.Active = *req.Active
	}

	created, err := s.repo.CreateAlias(ctx, alias)
	if err != nil {
		return Alias{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return created, err
	}

	s.record(ctx, actor, ActionAliasCreate, ResourceTypeAlias, created.ID, map[string]any{
		"source":      source + "@" + domain.Domain,
		"destination": destination,
	})
	return created, nil
}

// DeleteAlias removes a forwarder.
func (s *Service) DeleteAlias(ctx context.Context, actor Actor, requestID, id string) error {
	alias, err := s.repo.GetAlias(ctx, id)
	if err != nil {
		return err
	}
	if err := s.repo.DeleteAlias(ctx, id); err != nil {
		return err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}
	s.record(ctx, actor, ActionAliasDelete, ResourceTypeAlias, id, map[string]any{
		"source":      alias.Source,
		"destination": alias.Destination,
	})
	return nil
}

// ResponderRequest is a vacation reply being set.
type ResponderRequest struct {
	Subject      string `json:"subject"`
	Body         string `json:"body"`
	IntervalDays int    `json:"interval_days"`
	Active       *bool  `json:"active"`
}

// SetAutoresponder writes a mailbox's vacation reply.
func (s *Service) SetAutoresponder(ctx context.Context, actor Actor, requestID, mailboxID string,
	req ResponderRequest,
) (Autoresponder, error) {
	if _, err := s.repo.GetMailbox(ctx, mailboxID); err != nil {
		return Autoresponder{}, err
	}

	responder := Autoresponder{
		MailboxID:    mailboxID,
		Subject:      strings.TrimSpace(req.Subject),
		Body:         req.Body,
		IntervalDays: req.IntervalDays,
		Active:       true,
	}
	if responder.IntervalDays == 0 {
		responder.IntervalDays = 7
	}
	if req.Active != nil {
		responder.Active = *req.Active
	}

	if err := validate.AutoresponderSubject(responder.Subject); err != nil {
		return Autoresponder{}, err
	}
	if err := validate.AutoresponderBody(responder.Body); err != nil {
		return Autoresponder{}, err
	}
	if err := validate.AutoresponderDays(responder.IntervalDays); err != nil {
		return Autoresponder{}, err
	}

	saved, err := s.repo.SaveAutoresponder(ctx, responder)
	if err != nil {
		return Autoresponder{}, err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return saved, err
	}
	s.record(ctx, actor, ActionResponderSet, ResourceTypeMailbox, mailboxID, map[string]any{
		"active": saved.Active,
	})
	return saved, nil
}

// ClearAutoresponder removes a vacation reply.
func (s *Service) ClearAutoresponder(ctx context.Context, actor Actor, requestID,
	mailboxID string,
) error {
	if err := s.repo.DeleteAutoresponder(ctx, mailboxID); err != nil {
		if errors.Is(err, ErrNotFound) {
			// Removing one that is not there is the state the caller asked
			// for, so it is not a failure.
			return nil
		}
		return err
	}
	if err := s.reconcile(ctx, requestID); err != nil {
		return err
	}
	s.record(ctx, actor, ActionResponderClear, ResourceTypeMailbox, mailboxID, nil)
	return nil
}

// checkPassword refuses a mailbox password that is too short to be one.
//
// Length only, and deliberately not a composition rule. A twelve-character
// requirement removes the passwords that fall to an online guessing attack
// against a port the whole internet can reach; "must contain a symbol" removes
// nothing an attacker does and produces "Password1!" on every host in the
// world.
func checkPassword(password string) error {
	if len([]rune(password)) < MinPasswordLength {
		return ErrWeakPassword
	}
	if strings.ContainsAny(password, "\r\n") {
		// It is about to be hashed, so a line break would be harmless — but a
		// password nobody's mail client can send is one that produces "password
		// incorrect" for a password that is correct.
		return fmt.Errorf("a mailbox password may not contain a line break")
	}
	return nil
}
