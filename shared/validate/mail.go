package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by mail validation.
var (
	// ErrInvalidMailbox covers a mailbox address the panel will not create.
	ErrInvalidMailbox = errors.New("invalid mailbox")
	// ErrInvalidMailDomain covers a domain the panel will not accept mail for.
	ErrInvalidMailDomain = errors.New("invalid mail domain")
	// ErrInvalidMailPolicy covers an SPF or DMARC policy outside the closed
	// sets.
	ErrInvalidMailPolicy = errors.New("invalid mail policy")
	// ErrInvalidMailSetting covers a server-wide setting the mail server would
	// refuse or silently misread.
	ErrInvalidMailSetting = errors.New("invalid mail setting")
)

// Bounds on the parts of an address.
//
// RFC 5321 fixes the local part at 64 octets and the whole address at 254, and
// the panel enforces both: an address longer than the protocol allows is one
// some receiving server along the way will reject, which is a delivery failure
// that surfaces days later as "some of my mail bounces".
const (
	// MaxLocalPartLength is the RFC 5321 limit.
	MaxLocalPartLength = 64
	// MaxMailQuotaMB bounds a mailbox quota at 1 TiB. Not a protocol limit —
	// a guard against a mistyped figure becoming an unbounded mailbox.
	MaxMailQuotaMB = 1048576
	// MaxAutoresponderSubject bounds a vacation subject.
	MaxAutoresponderSubject = 200
	// MaxAutoresponderBody bounds a vacation message.
	MaxAutoresponderBody = 4000
	// MaxDKIMSelectorLength bounds a selector. It becomes a DNS label.
	MaxDKIMSelectorLength = 63
)

// MailLocalPart checks the part of an address before the "@".
//
// The permitted set is deliberately narrower than RFC 5321's. The standard
// allows a quoted local part containing spaces, colons, and very nearly
// anything else, and a panel that accepted one would be writing it into
// Postfix's lookup tables and Dovecot's passwd-file — two line-oriented,
// colon-delimited formats where a colon ends a field early and gives the next
// one a value the panel did not write. That is the same reasoning as the FTP
// user name in Phase 7.1, and it fails the same way: not with an error, but
// with an account whose home directory or password is not the one recorded.
//
// So: letters, digits, and the three separators every real address uses. What
// this refuses is addresses nobody has, and the refusal is visible at the
// moment somebody types one rather than a mystery afterwards.
func MailLocalPart(local string) error {
	trimmed := strings.TrimSpace(local)
	if trimmed == "" {
		return fmt.Errorf("%w: a mailbox name is required", ErrInvalidMailbox)
	}
	if len(trimmed) > MaxLocalPartLength {
		return fmt.Errorf("%w: a mailbox name may be at most %d characters",
			ErrInvalidMailbox, MaxLocalPartLength)
	}
	if trimmed != local {
		return fmt.Errorf("%w: a mailbox name may not begin or end with a space",
			ErrInvalidMailbox)
	}

	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf(
				"%w: %q may contain only letters, digits, dots, underscores and hyphens",
				ErrInvalidMailbox, trimmed)
		}
	}

	// A leading or trailing dot, or two in a row, is invalid per RFC 5321 and
	// is also how a local part becomes a path component that is not the one
	// intended: the Maildir for "a..b" and the Maildir for "a.b" differ by a
	// character most people cannot see in a list.
	if strings.HasPrefix(trimmed, ".") || strings.HasSuffix(trimmed, ".") {
		return fmt.Errorf("%w: a mailbox name may not begin or end with a dot",
			ErrInvalidMailbox)
	}
	if strings.Contains(trimmed, "..") {
		return fmt.Errorf("%w: a mailbox name may not contain two dots in a row",
			ErrInvalidMailbox)
	}
	// A leading hyphen is an option to something, sooner or later.
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: a mailbox name may not begin with a hyphen",
			ErrInvalidMailbox)
	}
	return nil
}

// NormalizeLocalPart lower-cases a local part.
//
// The standard says the local part is case-sensitive and that receiving
// servers should treat it as such. Every mail server anybody actually runs
// treats it case-insensitively, and the alternative here is worse than a
// standards deviation: "Sales@" and "sales@" would be two mailboxes, in two
// Maildirs, and the half of the world that typed the other one would be
// writing to an address the owner never opens.
func NormalizeLocalPart(local string) string {
	return strings.ToLower(strings.TrimSpace(local))
}

// MailAddress checks a complete address and returns it normalised.
//
// It is the local part and the domain checked separately rather than parsed as
// a whole, because both halves are about to be written into configuration the
// panel generates, and the whole-address parser in net/mail accepts forms
// neither of those files can hold.
func MailAddress(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", fmt.Errorf("%w: an address is required", ErrInvalidMailbox)
	}
	if len(trimmed) > MaxEmailLength {
		return "", fmt.Errorf("%w: an address may be at most %d characters",
			ErrInvalidMailbox, MaxEmailLength)
	}

	local, domain, found := strings.Cut(trimmed, "@")
	if !found {
		return "", fmt.Errorf("%w: %q has no domain", ErrInvalidMailbox, trimmed)
	}
	// Cut splits at the *first* "@", so an address with two of them leaves one
	// in the domain half, where Domain refuses it.
	if err := MailLocalPart(local); err != nil {
		return "", err
	}
	if err := Domain(domain); err != nil {
		return "", fmt.Errorf("%w: %s", ErrInvalidMailbox, err)
	}
	return NormalizeLocalPart(local) + "@" + NormalizeDomain(domain), nil
}

// MailDomain checks a domain the panel will accept mail for.
//
// A mail domain must be a real, resolvable name rather than a label: the MX
// record that points at this host lives in its zone, and a single-label name
// has no zone. Domain already refuses those, so this adds the one rule mail has
// that DNS does not — a wildcard is not a thing a mail domain can be, because
// there is no way to publish DKIM for it and no way to enumerate its
// mailboxes.
func MailDomain(domain string) error {
	trimmed := strings.TrimSpace(domain)
	if strings.Contains(trimmed, "*") {
		return fmt.Errorf("%w: mail cannot be accepted for a wildcard domain",
			ErrInvalidMailDomain)
	}
	if err := Domain(trimmed); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidMailDomain, err)
	}
	return nil
}

// MailQuotaMB checks a mailbox size limit.
//
// Zero is permitted and means unlimited, which is the same convention the FTP
// accounts use. It is not the default: see the migration.
func MailQuotaMB(quota int) error {
	if quota < 0 {
		return fmt.Errorf("%w: a mailbox quota cannot be negative", ErrInvalidMailbox)
	}
	if quota > MaxMailQuotaMB {
		return fmt.Errorf("%w: a mailbox quota may be at most %d MB",
			ErrInvalidMailbox, MaxMailQuotaMB)
	}
	return nil
}

// The SPF policies the panel will publish.
//
// Three, and the missing fourth is the point. "+all" — pass anything — is a
// record that is worse than having none: it tells every receiver that the
// forgery they are looking at is authorised. There is no way to ask for it
// here.
const (
	// SPFNone publishes no SPF record at all.
	SPFNone = "none"
	// SPFSoft ends the record with "~all": mail from elsewhere is marked, not
	// refused. This is the right first setting for a domain whose owner is not
	// yet certain what else sends as them.
	SPFSoft = "soft"
	// SPFStrict ends the record with "-all": mail from anywhere else is
	// forged. Correct, and the way a domain's own newsletter provider stops
	// being delivered.
	SPFStrict = "strict"
)

// SPFPolicies is the supported set, in increasing severity.
var SPFPolicies = []string{SPFNone, SPFSoft, SPFStrict}

// SPFPolicy checks an SPF policy.
func SPFPolicy(policy string) error {
	for _, known := range SPFPolicies {
		if policy == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidMailPolicy, policy, strings.Join(SPFPolicies, ", "))
}

// The DMARC policies, which are the standard's own three.
const (
	// DMARCNone asks receivers to do nothing but report. It is not a no-op:
	// the reports are how a domain owner finds out what else sends as them,
	// and going straight to reject without them is how a company discovers its
	// invoicing system was never in its SPF record.
	DMARCNone = "none"
	// DMARCQuarantine asks receivers to treat failures as suspicious.
	DMARCQuarantine = "quarantine"
	// DMARCReject asks receivers to refuse them.
	DMARCReject = "reject"
	// DMARCOff publishes no DMARC record.
	DMARCOff = "off"
)

// DMARCPolicies is the supported set, in increasing severity.
var DMARCPolicies = []string{DMARCOff, DMARCNone, DMARCQuarantine, DMARCReject}

// DMARCPolicy checks a DMARC policy.
func DMARCPolicy(policy string) error {
	for _, known := range DMARCPolicies {
		if policy == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidMailPolicy, policy, strings.Join(DMARCPolicies, ", "))
}

// DKIMSelector checks the name a signing key is published under.
//
// It becomes a DNS label in "<selector>._domainkey.<domain>", and it becomes a
// filename under the key directory. Both is why the set is this narrow: a
// selector with a dot would silently move the record into a different zone, and
// one with a slash would move the key file into a different directory.
func DKIMSelector(selector string) error {
	trimmed := strings.TrimSpace(selector)
	if trimmed == "" {
		return fmt.Errorf("%w: a DKIM selector is required", ErrInvalidMailDomain)
	}
	if len(trimmed) > MaxDKIMSelectorLength {
		return fmt.Errorf("%w: a DKIM selector may be at most %d characters",
			ErrInvalidMailDomain, MaxDKIMSelectorLength)
	}
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case r == '-':
		default:
			return fmt.Errorf(
				"%w: a DKIM selector may contain only lower-case letters, digits and hyphens",
				ErrInvalidMailDomain)
		}
	}
	if strings.HasPrefix(trimmed, "-") || strings.HasSuffix(trimmed, "-") {
		return fmt.Errorf("%w: a DKIM selector may not begin or end with a hyphen",
			ErrInvalidMailDomain)
	}
	return nil
}

// AutoresponderText checks a vacation subject or body.
//
// The refusal that matters is not length. This text is written into a Sieve
// script, where it becomes a quoted string, and Sieve's quoting is exactly as
// forgiving as C's: a lone double quote ends the string and everything after it
// is script. The generator escapes both characters that need escaping, and this
// refuses the ones no amount of escaping makes safe — a NUL, which truncates
// the file at the C library, and the control characters that would let a line
// end in the middle of a directive.
//
// Belt and braces on purpose: the escaping is the protection, and this is the
// second one, because a vacation message is text a customer types and the
// script it lands in runs on the mail server.
func AutoresponderText(text, field string, limit int) error {
	if len(text) > limit {
		return fmt.Errorf("%w: an autoresponder %s may be at most %d characters",
			ErrInvalidMailbox, field, limit)
	}
	for _, r := range text {
		if r == '\n' || r == '\t' {
			// A body is genuinely multi-line. A subject is checked for these
			// separately by its caller, because a newline in a header is a
			// second header.
			continue
		}
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: an autoresponder %s may not contain control characters",
				ErrInvalidMailbox, field)
		}
	}
	return nil
}

// AutoresponderSubject checks the subject line of a vacation reply.
//
// Stricter than the body by one rule: it becomes a header, and a header
// containing a line break is two headers.
func AutoresponderSubject(subject string) error {
	if strings.ContainsAny(subject, "\r\n") {
		return fmt.Errorf("%w: an autoresponder subject may not contain a line break",
			ErrInvalidMailbox)
	}
	return AutoresponderText(subject, "subject", MaxAutoresponderSubject)
}

// AutoresponderBody checks the message a vacation reply sends.
func AutoresponderBody(body string) error {
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("%w: an autoresponder needs a message", ErrInvalidMailbox)
	}
	return AutoresponderText(body, "message", MaxAutoresponderBody)
}

// MailHostname checks the name the mail server calls itself.
//
// It is the name in the HELO/EHLO greeting and in every Received header, and
// getting it wrong is the most common reason a correctly configured server's
// mail is rejected: a receiving server checks that the greeting name resolves
// and that it matches the reverse DNS of the connecting address, and "localhost"
// fails both. So it must be a fully qualified name — a bare label is refused
// here rather than accepted and rejected by strangers.
func MailHostname(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: the mail server needs a hostname", ErrInvalidMailSetting)
	}
	if err := Domain(trimmed); err != nil {
		return fmt.Errorf("%w: the mail hostname %s", ErrInvalidMailSetting, err)
	}
	if !strings.Contains(trimmed, ".") {
		return fmt.Errorf(
			"%w: the mail hostname must be fully qualified, like mail.example.com",
			ErrInvalidMailSetting)
	}
	return nil
}

// MaxMessageSizeMB bounds the largest message the server will accept.
const (
	// MinMessageSizeMB is the smallest limit the panel will set. Below this,
	// ordinary mail with an attached photograph bounces.
	MinMessageSizeMB = 1
	// MaxMessageSizeMB is the largest. Above it, one message can fill a
	// mailbox quota and the queue at the same time.
	MaxMessageSizeMB = 512
)

// MessageSizeMB checks the message size limit.
func MessageSizeMB(size int) error {
	if size < MinMessageSizeMB || size > MaxMessageSizeMB {
		return fmt.Errorf("%w: the message size limit must be between %d and %d MB",
			ErrInvalidMailSetting, MinMessageSizeMB, MaxMessageSizeMB)
	}
	return nil
}

// Spam score bounds.
//
// Rspamd's scale is open-ended but its default actions sit between 4 and 15,
// and a threshold outside that range is one of two mistakes: too low rejects
// ordinary mail, too high rejects nothing at all. Neither is visible until
// somebody's mail goes missing, so the panel refuses both.
const (
	// MinSpamScore is the lowest reject threshold the panel will set.
	MinSpamScore = 5
	// MaxSpamScore is the highest.
	MaxSpamScore = 30
)

// SpamRejectScore checks the score above which a message is refused.
func SpamRejectScore(score int) error {
	if score < MinSpamScore || score > MaxSpamScore {
		return fmt.Errorf("%w: the spam reject score must be between %d and %d",
			ErrInvalidMailSetting, MinSpamScore, MaxSpamScore)
	}
	return nil
}

// AutoresponderDays checks how long a vacation reply waits before replying to
// the same sender again.
//
// Sieve's vacation extension requires at least one day and the panel caps it at
// a month. The reason there is a minimum at all is that zero would mean a reply
// to every message — including to the auto-reply of anybody else who is also
// away, which is a loop that ends when one of the two mailboxes is full.
func AutoresponderDays(days int) error {
	if days < 1 || days > 30 {
		return fmt.Errorf("%w: an autoresponder must wait between 1 and 30 days",
			ErrInvalidMailbox)
	}
	return nil
}
