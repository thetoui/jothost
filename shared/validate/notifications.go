package validate

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
)

// Errors returned by notification validation.
var (
	// ErrInvalidChannel covers a channel this panel will not send through.
	ErrInvalidChannel = errors.New("invalid notification channel")
	// ErrInvalidNotifySeverity covers a severity floor outside the scale.
	ErrInvalidNotifySeverity = errors.New("invalid notification severity")
	// ErrInvalidEventKind covers a kind of event this panel does not raise.
	ErrInvalidEventKind = errors.New("invalid event kind")
)

// The channel kinds.
//
// Three, and there is deliberately no "webhook". A channel that accepted a URL
// would be a request forger sitting inside the panel, pointed at whatever an
// admin account can be talked into typing — and the panel runs on the same
// machine as every site it hosts, with a view of the private network around it.
//
// Telegram and LINE each publish one API host, and those are compiled in.
// Email is the exception that proves the rule: its host is the operator's own
// mail server, which is a thing they already run rather than a thing they were
// persuaded to point at.
const (
	// ChannelEmail sends through SMTP.
	ChannelEmail = "email"
	// ChannelTelegram posts to the Telegram Bot API.
	ChannelTelegram = "telegram"
	// ChannelLINE posts to the LINE Messaging API.
	ChannelLINE = "line"
)

// ChannelKinds is the supported set, in the order a UI should offer them.
var ChannelKinds = []string{ChannelEmail, ChannelTelegram, ChannelLINE}

// ChannelKind checks a channel kind.
func ChannelKind(kind string) error {
	for _, known := range ChannelKinds {
		if kind == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidChannel, kind, strings.Join(ChannelKinds, ", "))
}

// The severity scale a notification carries.
//
// A fourth scale in this panel, and the reason is that it has to be the union
// of the other three. Phase 19 grades alerts warning/critical, Phase 15 grades
// findings critical/high/medium/low/info, and a notification carries both. So
// the levels here are the ones a *person deciding whether to be woken* needs,
// and every source maps onto them.
const (
	NotifyCritical = "critical"
	NotifyHigh     = "high"
	NotifyWarning  = "warning"
	NotifyInfo     = "info"
)

// NotifySeverities is the scale, worst first.
var NotifySeverities = []string{NotifyCritical, NotifyHigh, NotifyWarning, NotifyInfo}

// NotifySeverity checks a severity.
func NotifySeverity(severity string) error {
	for _, known := range NotifySeverities {
		if severity == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidNotifySeverity, severity, strings.Join(NotifySeverities, ", "))
}

// NotifyRank returns a sortable rank, lower being worse.
//
// An unknown severity ranks *worst*, which is the opposite of the security
// scale's rule and is deliberate. There, an ungradable finding must not push a
// real critical off the top of a page. Here, the question is whether to send —
// and a message this panel cannot grade is one it should deliver rather than
// silently drop.
func NotifyRank(severity string) int {
	for rank, known := range NotifySeverities {
		if severity == known {
			return rank
		}
	}
	return -1
}

// NotifyAtLeast reports whether severity meets a channel's floor.
func NotifyAtLeast(severity, floor string) bool {
	return NotifyRank(severity) <= NotifyRank(floor)
}

// The kinds of event this panel raises.
//
// A closed list, because a channel can be told which kinds it wants and a kind
// nobody recognises would be a filter that silently matches nothing — a channel
// somebody believes is watching something it is not.
const (
	// EventAlertOpened and EventAlertResolved come from the monitor (Phase 19).
	// Both are worth sending: an alert that resolves itself at four in the
	// morning is the difference between getting up and going back to sleep.
	EventAlertOpened   = "alert.opened"
	EventAlertResolved = "alert.resolved"
	// EventBackupFailed comes from Phase 14. A backup that succeeds is not
	// news; one that fails is the only warning before it matters.
	EventBackupFailed = "backup.failed"
	// EventSSLExpiring comes from Phase 6's renewal sweep.
	EventSSLExpiring = "ssl.expiring"
	// EventSecurityFinding comes from Phase 15, for findings serious enough to
	// interrupt somebody.
	EventSecurityFinding = "security.finding"
	// EventTest is a channel being proved to work.
	EventTest = "test"
)

// EventKinds is the full set, in the order a UI should offer them.
var EventKinds = []string{
	EventAlertOpened, EventAlertResolved, EventBackupFailed,
	EventSSLExpiring, EventSecurityFinding,
}

// EventKind checks a kind.
func EventKind(kind string) error {
	if kind == EventTest {
		return nil
	}
	for _, known := range EventKinds {
		if kind == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidEventKind, kind, strings.Join(EventKinds, ", "))
}

// MaxChannelNameLength bounds a channel's display name.
const MaxChannelNameLength = 100

// ChannelName checks the name an operator gives a channel.
func ChannelName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: a name is required", ErrInvalidChannel)
	}
	if len(trimmed) > MaxChannelNameLength {
		return fmt.Errorf("%w: a name may be at most %d characters",
			ErrInvalidChannel, MaxChannelNameLength)
	}
	for _, r := range trimmed {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: a name may not contain control characters",
				ErrInvalidChannel)
		}
	}
	return nil
}

// MaxEmailLength bounds one address.
const MaxEmailLength = 254

// EmailAddress checks one address.
//
// Parsed rather than pattern-matched, and then the parsed form is what gets
// used. An address is about to become a header in a message this panel
// composes, and a value carrying a newline is how somebody injects a second
// header — a Bcc, or a whole second message.
func EmailAddress(address string) (string, error) {
	trimmed := strings.TrimSpace(address)
	if trimmed == "" {
		return "", fmt.Errorf("%w: an address is required", ErrInvalidChannel)
	}
	if len(trimmed) > MaxEmailLength {
		return "", fmt.Errorf("%w: an address may be at most %d characters",
			ErrInvalidChannel, MaxEmailLength)
	}
	if strings.ContainsAny(trimmed, "\r\n") {
		return "", fmt.Errorf("%w: an address may not contain a line break",
			ErrInvalidChannel)
	}

	parsed, err := mail.ParseAddress(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: %q is not an email address", ErrInvalidChannel, trimmed)
	}
	// The bare address, without any display name. A display name is more text
	// this panel would have to escape into a header for no benefit.
	if parsed.Address != trimmed {
		return "", fmt.Errorf(
			"%w: give the address on its own, without a display name", ErrInvalidChannel)
	}
	return parsed.Address, nil
}

// MaxSMTPHostLength bounds a mail server hostname.
const MaxSMTPHostLength = 255

// SMTPHost checks the mail server a channel sends through.
func SMTPHost(host string) error {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return fmt.Errorf("%w: a mail server is required", ErrInvalidChannel)
	}
	if len(trimmed) > MaxSMTPHostLength {
		return fmt.Errorf("%w: a mail server may be at most %d characters",
			ErrInvalidChannel, MaxSMTPHostLength)
	}
	if strings.ContainsAny(trimmed, " \t\r\n/\\@") {
		return fmt.Errorf("%w: a mail server is a hostname or an address, on its own",
			ErrInvalidChannel)
	}
	return nil
}

// SMTPPort checks the port.
func SMTPPort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("%w: a port is between 1 and 65535", ErrInvalidChannel)
	}
	return nil
}

// SMTP transport security.
const (
	// SMTPStartTLS connects in the clear and upgrades. Port 587's usual mode.
	SMTPStartTLS = "starttls"
	// SMTPImplicit connects with TLS from the first byte. Port 465.
	SMTPImplicit = "tls"
	// SMTPPlain is no encryption at all, and is accepted only for a server on
	// this machine. A password sent in the clear to anywhere else is a password
	// given away, and the alert it was protecting is the least of it.
	SMTPPlain = "none"
)

// SMTPSecurities is the supported set.
var SMTPSecurities = []string{SMTPStartTLS, SMTPImplicit, SMTPPlain}

// SMTPSecurity checks the transport mode against the host it applies to.
//
// Plain SMTP is refused by default and possible with an explicit
// acknowledgement, which is the same answer Phase 14 reached for a plain-http S3
// endpoint and for the same reason. An internal mail relay on a private network
// is an ordinary arrangement — a company Postfix that has never had a
// certificate — and a panel that flatly refused it would be worked around
// rather than obeyed.
//
// So somebody has to say, in the channel, that they accept a password and every
// alert travelling where anyone on the path can read them. A loopback address
// needs no acknowledgement: a connection that never leaves the machine cannot be
// read on the way.
func SMTPSecurity(mode, host string, allowInsecure bool) error {
	known := false
	for _, candidate := range SMTPSecurities {
		if mode == candidate {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("%w: %q is not one of %s",
			ErrInvalidChannel, mode, strings.Join(SMTPSecurities, ", "))
	}

	if mode == SMTPPlain && !isLoopbackHost(strings.TrimSpace(host)) && !allowInsecure {
		return fmt.Errorf(
			"%w: a mail server that is not on this machine must use TLS, or the "+
				"password and every alert travel in the clear; accept that "+
				"explicitly if the relay is on a private network", ErrInvalidChannel)
	}
	return nil
}

// MaxTokenLength bounds a bot token or channel access token.
//
// Generous: LINE's channel access tokens are long, and a limit that refused a
// real one would make the channel unusable for a reason nobody could guess.
const MaxTokenLength = 512

// BotToken checks a Telegram bot token or a LINE channel access token.
//
// The rule that earns its place is the whitespace one. Both tokens go into an
// HTTP header or a URL path, and a value carrying a newline is header injection
// in one case and a mangled request in the other.
func BotToken(token string) error {
	if token == "" {
		return fmt.Errorf("%w: a token is required", ErrInvalidChannel)
	}
	if len(token) > MaxTokenLength {
		return fmt.Errorf("%w: a token may be at most %d characters",
			ErrInvalidChannel, MaxTokenLength)
	}
	for _, r := range token {
		if r <= 0x20 || r == 0x7f {
			return fmt.Errorf("%w: a token may not contain spaces or control characters",
				ErrInvalidChannel)
		}
	}
	return nil
}

// MaxRecipientLength bounds a chat or user identifier.
const MaxRecipientLength = 128

// ChatRecipient checks a Telegram chat id or a LINE user or group id.
//
// Both are opaque identifiers that go into a JSON body, so the rule is narrow:
// printable, no whitespace, no control characters. A Telegram chat id may be
// negative, which is why a leading dash is allowed here where it is refused for
// a package name or an SFTP user — nothing here becomes a command-line
// argument.
func ChatRecipient(recipient string) error {
	if recipient == "" {
		return fmt.Errorf("%w: a recipient is required", ErrInvalidChannel)
	}
	if len(recipient) > MaxRecipientLength {
		return fmt.Errorf("%w: a recipient may be at most %d characters",
			ErrInvalidChannel, MaxRecipientLength)
	}
	for _, r := range recipient {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == ':'
		if !ok {
			return fmt.Errorf(
				"%w: a recipient may contain only letters, digits and - _ . :",
				ErrInvalidChannel)
		}
	}
	return nil
}

// MaxHeaderLength bounds a subject line before it becomes a header.
const MaxHeaderLength = 900

// HeaderText checks a value that is about to become an email header.
//
// The whole of this function is the CRLF rule. Everything else about a subject
// line is cosmetic; a newline in one is how a message gains a header its author
// did not write, and the text here is composed from a host's own output — a
// process name, a mount point, a domain — which is not this panel's to trust.
func HeaderText(value string) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%w: a header may not contain a line break", ErrInvalidChannel)
	}
	if len(value) > MaxHeaderLength {
		return fmt.Errorf("%w: a header may be at most %d characters",
			ErrInvalidChannel, MaxHeaderLength)
	}
	return nil
}

// MaxDedupeKeyLength bounds an event's identity.
const MaxDedupeKeyLength = 200

// DedupeKey checks the identity of the thing an event reports.
//
// It is the key of a unique index, and that index is the whole of the panel's
// protection against flooding: a disk sitting above its threshold for a week is
// one event or it is ten thousand emails, and which one depends entirely on
// this value being stable and storable.
func DedupeKey(key string) error {
	if key == "" {
		return fmt.Errorf("%w: an event needs a dedupe key", ErrInvalidEventKind)
	}
	if len(key) > MaxDedupeKeyLength {
		return fmt.Errorf("%w: a dedupe key may be at most %d characters",
			ErrInvalidEventKind, MaxDedupeKeyLength)
	}
	for _, r := range key {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '.' || r == '-' || r == '_' ||
			r == ':' || r == '/'
		if !ok {
			return fmt.Errorf(
				"%w: a dedupe key may contain only letters, digits and . - _ : /",
				ErrInvalidEventKind)
		}
	}
	return nil
}
