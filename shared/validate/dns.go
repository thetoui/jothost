package validate

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// Errors returned by DNS validation.
var (
	// ErrInvalidZone covers a zone name this panel will not serve.
	ErrInvalidZone = errors.New("invalid DNS zone")
	// ErrInvalidRecordType covers a type outside the supported set.
	ErrInvalidRecordType = errors.New("unsupported DNS record type")
	// ErrInvalidRecordName covers an owner name that could not survive a zone
	// file.
	ErrInvalidRecordName = errors.New("invalid DNS record name")
	// ErrInvalidRecordValue covers rdata that is wrong for its type.
	ErrInvalidRecordValue = errors.New("invalid DNS record value")
	// ErrInvalidTTL covers a lifetime outside the range this panel writes.
	ErrInvalidTTL = errors.New("invalid TTL")
	// ErrInvalidSOA covers a start-of-authority field.
	ErrInvalidSOA = errors.New("invalid SOA value")
	// ErrInvalidNetwork covers a network a reverse zone cannot be built from.
	ErrInvalidNetwork = errors.New("invalid network")
)

// Record types this panel writes.
//
// The list is the PRD's, and it is a list rather than "whatever the operator
// types" for the reason every allowlist in this project exists: each type has
// its own rdata grammar, and a type the panel cannot check is a line it writes
// into a file a root process parses without knowing what it says.
//
// PTR is here in addition to the PRD's set because a reverse zone with no PTR
// records is an empty zone, and reverse zones are in this phase's task list.
const (
	RecordA     = "A"
	RecordAAAA  = "AAAA"
	RecordCNAME = "CNAME"
	RecordMX    = "MX"
	RecordTXT   = "TXT"
	RecordNS    = "NS"
	RecordCAA   = "CAA"
	RecordSRV   = "SRV"
	RecordPTR   = "PTR"
)

// RecordTypes is the supported set, in the order a UI should offer them.
var RecordTypes = []string{
	RecordA, RecordAAAA, RecordCNAME, RecordMX, RecordTXT,
	RecordNS, RecordCAA, RecordSRV, RecordPTR,
}

// Zone kinds. A master zone is served from a zone file this panel writes; a
// slave zone is transferred from somewhere else and this panel does not own its
// contents.
const (
	ZoneMaster = "master"
	ZoneSlave  = "slave"
)

// TTL bounds.
//
// Zero is not "no caching" here, it is "use the zone's default" — a per-record
// TTL of zero is legal in DNS and almost never what somebody filling in a form
// means. The floor of one minute is what stops a typo producing a record every
// resolver on the internet re-asks for continuously; the ceiling of a week is
// what stops one being cached past the point anybody can fix it.
const (
	MinTTL     = 60
	MaxTTL     = 604800
	DefaultTTL = 3600
)

// SOA timer bounds, from RFC 1912 section 2.2 with room either side.
const (
	MinRefresh = 300
	MaxRefresh = 86400
	MinRetry   = 60
	MaxRetry   = 28800
	MinExpire  = 604800
	MaxExpire  = 31536000
	MinMinimum = 60
	MaxMinimum = 86400
)

// MaxTXTString is the longest single character-string in a TXT record. Longer
// values are legal and are split into several strings when written; this is the
// limit on one of them, and the point at which splitting has to happen.
const MaxTXTString = 255

// MaxTXTValue bounds the whole record.
const MaxTXTValue = 4096

// CAA tags this panel writes. An unknown tag is not an error in DNS, but a
// misspelled "issue" is a certificate authority policy that silently does
// nothing — which is the failure this refuses.
var caaTags = map[string]bool{"issue": true, "issuewild": true, "iodef": true}

// Zone checks a zone apex name.
//
// A reverse zone is accepted by the same rule as a forward one: "0.113.203
// .in-addr.arpa" is a domain name and is checked as one, which is what stops a
// hand-typed reverse zone from carrying something a zone file would read as
// syntax.
func Zone(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a zone must have a name", ErrInvalidZone)
	}
	if err := Domain(name); err != nil {
		return fmt.Errorf("%w: %s", ErrInvalidZone, err.Error())
	}
	if IsWildcard(name) {
		return fmt.Errorf("%w: a zone cannot be a wildcard", ErrInvalidZone)
	}
	return nil
}

// ZoneKind checks a master/slave choice.
func ZoneKind(kind string) error {
	switch kind {
	case ZoneMaster, ZoneSlave:
		return nil
	default:
		return fmt.Errorf("%w: a zone is %q or %q, not %q",
			ErrInvalidZone, ZoneMaster, ZoneSlave, kind)
	}
}

// RecordType checks a record type.
func RecordType(recordType string) error {
	for _, supported := range RecordTypes {
		if recordType == supported {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidRecordType, recordType, strings.Join(RecordTypes, ", "))
}

// NormalizeRecordName reduces an owner name to the form this panel stores.
//
// The apex is stored as "@" whatever it was typed as, including as the zone's
// own name: "example.com", "example.com." and "@" inside example.com are one
// name, and storing them separately would let two records claim the apex and
// let a delete miss the one somebody meant.
func NormalizeRecordName(name, zone string) string {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	trimmed = strings.TrimSuffix(trimmed, ".")
	if trimmed == "" || trimmed == "@" {
		return "@"
	}
	if zone != "" {
		zone = NormalizeDomain(zone)
		if trimmed == zone {
			return "@"
		}
		if strings.HasSuffix(trimmed, "."+zone) {
			return strings.TrimSuffix(trimmed, "."+zone)
		}
	}
	return trimmed
}

// RecordName checks an owner name, relative to its zone.
//
// The name is relative because that is what a zone file holds, and because an
// absolute name in the owner column is how a record ends up in a zone it was
// not meant for. Underscore labels are allowed — "_dmarc", "_sip._tcp" — since
// the records this panel exists to publish for Phase 26 all live under them.
func RecordName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a record must have a name, or %q for the zone itself",
			ErrInvalidRecordName, "@")
	}
	if name == "@" {
		return nil
	}
	if len(name) > MaxDomainLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidRecordName, MaxDomainLength)
	}
	if strings.HasSuffix(name, ".") {
		return fmt.Errorf(
			"%w: it is relative to its zone, so it must not end with a dot",
			ErrInvalidRecordName)
	}

	labels := strings.Split(name, ".")
	for index, label := range labels {
		if label == "*" {
			// A wildcard is only a wildcard as the leftmost label. Anywhere
			// else it is a literal asterisk, which matches one name nobody will
			// ever ask for.
			if index != 0 {
				return fmt.Errorf(
					"%w: a wildcard must be the first label, as in %q",
					ErrInvalidRecordName, "*.dev")
			}
			continue
		}
		if err := recordLabel(label); err != nil {
			return err
		}
	}
	return nil
}

// recordLabel checks one label of an owner name.
func recordLabel(label string) error {
	if label == "" {
		return fmt.Errorf("%w: empty label", ErrInvalidRecordName)
	}
	if len(label) > MaxLabelLength {
		return fmt.Errorf("%w: label exceeds %d characters",
			ErrInvalidRecordName, MaxLabelLength)
	}
	for index, char := range label {
		switch {
		case char >= 'a' && char <= 'z',
			char >= '0' && char <= '9':
		case char == '_' && index == 0:
			// A service label: _dmarc, _acme-challenge, _sip.
		case char == '-' && index != 0 && index != len(label)-1:
		default:
			return fmt.Errorf("%w: %q may not contain %q",
				ErrInvalidRecordName, label, string(char))
		}
	}
	return nil
}

// Hostname checks a name used as the *target* of a record.
//
// Unlike an owner name this may be absolute, and usually should be: "mail" in
// an MX record means mail.example.com, and an operator who meant Google's
// servers and typed a name without the trailing dot has published a mail server
// that does not exist. The panel cannot tell which was meant, so it accepts
// both and the zone writer preserves exactly what was given.
func Hostname(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a target name is required", ErrInvalidRecordValue)
	}
	if len(name) > MaxDomainLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidRecordValue, MaxDomainLength)
	}

	trimmed := strings.TrimSuffix(name, ".")
	if trimmed == "" {
		// "." is the root, which is what an SRV target uses to say "this
		// service is not available here". It is only meaningful absolute.
		return nil
	}
	for _, label := range strings.Split(trimmed, ".") {
		if label == "*" {
			return fmt.Errorf("%w: a target name cannot be a wildcard",
				ErrInvalidRecordValue)
		}
		if err := recordLabel(label); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidRecordValue,
				strings.TrimPrefix(err.Error(), ErrInvalidRecordName.Error()+": "))
		}
	}
	return nil
}

// TTL checks a record lifetime. Zero means the zone's default.
func TTL(ttl int) error {
	if ttl == 0 {
		return nil
	}
	if ttl < MinTTL || ttl > MaxTTL {
		return fmt.Errorf("%w: must be 0 for the zone default, or between %d and %d seconds",
			ErrInvalidTTL, MinTTL, MaxTTL)
	}
	return nil
}

// RecordValue is the rdata of one record, in the fields its type needs.
//
// The composite types keep their numbers in their own fields rather than in the
// value string. It is a small deviation from DATABASE.md section 20, written up
// there, and the reason is the whole point of this file: an SRV record whose
// weight, port and target live in one text column can only be written to a zone
// file by parsing operator text at the moment of writing, which is the thing
// this panel does not do anywhere else.
type RecordValue struct {
	Type string
	// Value is the address, the target name, the text, or the CAA value.
	Value string
	// Priority is used by MX and SRV.
	Priority int
	// Weight and Port are used by SRV.
	Weight int
	Port   int
	// Flags and Tag are used by CAA.
	Flags int
	Tag   string
}

// Record checks a whole record.
func Record(value RecordValue) error {
	if err := RecordType(value.Type); err != nil {
		return err
	}

	switch value.Type {
	case RecordA:
		return ipValue(value.Value, false)
	case RecordAAAA:
		return ipValue(value.Value, true)
	case RecordCNAME, RecordNS, RecordPTR:
		return Hostname(value.Value)
	case RecordMX:
		if err := port16(value.Priority, "priority"); err != nil {
			return err
		}
		return Hostname(value.Value)
	case RecordTXT:
		return txtValue(value.Value)
	case RecordCAA:
		return caaValue(value)
	case RecordSRV:
		return srvValue(value)
	default:
		// Unreachable while RecordTypes and this switch agree, and a panic
		// waiting to happen if they ever stop agreeing silently.
		return fmt.Errorf("%w: %q has no value rule", ErrInvalidRecordType, value.Type)
	}
}

// ipValue checks an address of the right family.
//
// The family is checked, not just the syntax. An AAAA record holding an IPv4
// address is accepted by a zone file parser that reads it as a mapped address,
// and produces a name that resolves for nobody.
func ipValue(value string, wantV6 bool) error {
	address := net.ParseIP(strings.TrimSpace(value))
	if address == nil {
		return fmt.Errorf("%w: %q is not an IP address", ErrInvalidRecordValue, value)
	}
	isV4 := address.To4() != nil
	if wantV6 && isV4 {
		return fmt.Errorf("%w: an AAAA record needs an IPv6 address; %q is IPv4",
			ErrInvalidRecordValue, value)
	}
	if !wantV6 && !isV4 {
		return fmt.Errorf("%w: an A record needs an IPv4 address; %q is IPv6",
			ErrInvalidRecordValue, value)
	}
	return nil
}

// txtValue checks free text.
//
// The characters refused are the ones that would end the record early or the
// line with it. A quote inside the text is allowed and is escaped when written,
// because SPF and DKIM values legitimately contain them.
func txtValue(value string) error {
	if value == "" {
		return fmt.Errorf("%w: a TXT record needs text", ErrInvalidRecordValue)
	}
	if len(value) > MaxTXTValue {
		return fmt.Errorf("%w: text must be at most %d characters",
			ErrInvalidRecordValue, MaxTXTValue)
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return fmt.Errorf("%w: text may not contain control characters",
				ErrInvalidRecordValue)
		}
	}
	return nil
}

// caaValue checks a certificate authority authorisation record.
func caaValue(value RecordValue) error {
	if value.Flags < 0 || value.Flags > 255 {
		return fmt.Errorf("%w: CAA flags must be between 0 and 255", ErrInvalidRecordValue)
	}
	if !caaTags[value.Tag] {
		return fmt.Errorf("%w: a CAA tag is issue, issuewild or iodef, not %q",
			ErrInvalidRecordValue, value.Tag)
	}
	if value.Value == "" {
		return fmt.Errorf("%w: a CAA record needs a value, such as %q",
			ErrInvalidRecordValue, "letsencrypt.org")
	}
	return txtValue(value.Value)
}

// srvValue checks a service record.
func srvValue(value RecordValue) error {
	if err := port16(value.Priority, "priority"); err != nil {
		return err
	}
	if err := port16(value.Weight, "weight"); err != nil {
		return err
	}
	if value.Port < 1 || value.Port > 65535 {
		return fmt.Errorf("%w: an SRV port must be between 1 and 65535", ErrInvalidRecordValue)
	}
	return Hostname(value.Value)
}

// port16 checks an unsigned 16-bit field.
func port16(number int, field string) error {
	if number < 0 || number > 65535 {
		return fmt.Errorf("%w: %s must be between 0 and 65535", ErrInvalidRecordValue, field)
	}
	return nil
}

// Hostmaster checks the mailbox an SOA record names.
//
// It is given and stored as an email address because that is what it means and
// what an operator can check. The dotted zone-file form is produced by the
// writer, which also escapes a dot in the local part — "john.doe@example.com"
// becomes "john\.doe.example.com.", and a writer that did not escape it would
// publish mail for a different mailbox entirely.
func Hostmaster(email string) error {
	if email == "" {
		return fmt.Errorf("%w: a zone needs a hostmaster address", ErrInvalidSOA)
	}
	at := strings.Index(email, "@")
	if at <= 0 || at != strings.LastIndex(email, "@") {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalidSOA, email)
	}
	local, domain := email[:at], email[at+1:]
	if local == "" || len(email) > MaxDomainLength {
		return fmt.Errorf("%w: %q is not an email address", ErrInvalidSOA, email)
	}
	for _, char := range local {
		switch {
		case char >= 'a' && char <= 'z', char >= '0' && char <= '9':
		case char == '.' || char == '-' || char == '_' || char == '+':
		default:
			return fmt.Errorf("%w: %q may not appear in the hostmaster address",
				ErrInvalidSOA, string(char))
		}
	}
	return Domain(domain)
}

// SOATimers checks the four zone timers.
func SOATimers(refresh, retry, expire, minimum int) error {
	if refresh < MinRefresh || refresh > MaxRefresh {
		return fmt.Errorf("%w: refresh must be between %d and %d seconds",
			ErrInvalidSOA, MinRefresh, MaxRefresh)
	}
	if retry < MinRetry || retry > MaxRetry {
		return fmt.Errorf("%w: retry must be between %d and %d seconds",
			ErrInvalidSOA, MinRetry, MaxRetry)
	}
	if retry >= refresh {
		// A retry longer than the refresh means a secondary that failed once
		// waits longer to try again than it would have waited to refresh
		// anyway, which is a zone that heals more slowly the more it breaks.
		return fmt.Errorf("%w: retry must be shorter than refresh", ErrInvalidSOA)
	}
	if expire < MinExpire || expire > MaxExpire {
		return fmt.Errorf("%w: expire must be between %d and %d seconds",
			ErrInvalidSOA, MinExpire, MaxExpire)
	}
	if expire <= refresh {
		return fmt.Errorf("%w: expire must be longer than refresh", ErrInvalidSOA)
	}
	if minimum < MinMinimum || minimum > MaxMinimum {
		return fmt.Errorf("%w: the negative cache TTL must be between %d and %d seconds",
			ErrInvalidSOA, MinMinimum, MaxMinimum)
	}
	return nil
}

// ReverseZone builds the in-addr.arpa or ip6.arpa zone name for a network.
//
// Only the byte- and nibble-aligned prefixes are accepted: /8, /16 and /24 for
// IPv4, and multiples of four bits for IPv6. A /25 has a reverse zone too, but
// it is the RFC 2317 delegation form, which only works if the address's owner
// delegates it — so a panel that generated one would produce a zone that is
// correct, served, and never asked.
func ReverseZone(network string) (string, error) {
	_, prefix, err := net.ParseCIDR(strings.TrimSpace(network))
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a network in CIDR form, such as %q",
			ErrInvalidNetwork, network, "203.0.113.0/24")
	}

	ones, bits := prefix.Mask.Size()
	if bits == 32 {
		if ones != 8 && ones != 16 && ones != 24 {
			return "", fmt.Errorf(
				"%w: a reverse zone needs a /8, /16 or /24; %q cannot be served as a zone of its own",
				ErrInvalidNetwork, network)
		}
		address := prefix.IP.To4()
		labels := make([]string, 0, 4)
		for index := ones/8 - 1; index >= 0; index-- {
			labels = append(labels, fmt.Sprintf("%d", address[index]))
		}
		return strings.Join(labels, ".") + ".in-addr.arpa", nil
	}

	if ones%4 != 0 || ones == 0 {
		return "", fmt.Errorf(
			"%w: an IPv6 reverse zone needs a prefix that is a multiple of 4 bits",
			ErrInvalidNetwork)
	}
	address := prefix.IP.To16()
	nibbles := make([]string, 0, ones/4)
	for index := ones/4 - 1; index >= 0; index-- {
		octet := address[index/2]
		if index%2 == 0 {
			octet >>= 4
		}
		nibbles = append(nibbles, fmt.Sprintf("%x", octet&0x0f))
	}
	return strings.Join(nibbles, ".") + ".ip6.arpa", nil
}

// ReversePointerName returns the owner name a PTR for an address takes inside a
// reverse zone, or an error if the address is not in it.
//
// The check that the address belongs to the zone is the point. A PTR for
// 198.51.100.7 written into the zone for 203.0.113.0/24 is a record no resolver
// will ever ask that server for, and it is the kind of mistake that is invisible
// until somebody's mail is rejected.
func ReversePointerName(address, network string) (string, error) {
	parsed := net.ParseIP(strings.TrimSpace(address))
	if parsed == nil {
		return "", fmt.Errorf("%w: %q is not an IP address", ErrInvalidRecordValue, address)
	}
	_, prefix, err := net.ParseCIDR(strings.TrimSpace(network))
	if err != nil {
		return "", fmt.Errorf("%w: %q is not a network in CIDR form",
			ErrInvalidNetwork, network)
	}
	if !prefix.Contains(parsed) {
		return "", fmt.Errorf("%w: %s is not inside %s, so this zone is never asked about it",
			ErrInvalidRecordValue, address, network)
	}

	zone, err := ReverseZone(network)
	if err != nil {
		return "", err
	}

	full := reverseName(parsed)
	suffix := "." + zone
	if !strings.HasSuffix(full, suffix) {
		return "", fmt.Errorf("%w: %s does not belong in %s", ErrInvalidRecordValue, address, zone)
	}
	return strings.TrimSuffix(full, suffix), nil
}

// reverseName returns the full reverse name of an address.
func reverseName(address net.IP) string {
	if v4 := address.To4(); v4 != nil {
		return fmt.Sprintf("%d.%d.%d.%d.in-addr.arpa", v4[3], v4[2], v4[1], v4[0])
	}
	v6 := address.To16()
	nibbles := make([]string, 0, 32)
	for index := len(v6) - 1; index >= 0; index-- {
		nibbles = append(nibbles, fmt.Sprintf("%x", v6[index]&0x0f))
		nibbles = append(nibbles, fmt.Sprintf("%x", v6[index]>>4))
	}
	return strings.Join(nibbles, ".") + ".ip6.arpa"
}

// matchKeywords are the address-match-list words this panel will write.
//
// BIND accepts more — a key name, an ACL name, a negated element — and none of
// them are produced here. Everything the panel writes into a match list is
// either one of these words or an address it has parsed, which is what makes
// the semicolons in the generated file syntax rather than input.
var matchKeywords = map[string]bool{
	"any": true, "none": true, "localhost": true, "localnets": true,
}

// MatchAddress checks one element of an address match list.
func MatchAddress(value string) error {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	if trimmed == "" {
		return fmt.Errorf("%w: an empty address", ErrInvalidRecordValue)
	}
	if matchKeywords[trimmed] {
		return nil
	}
	if strings.Contains(trimmed, "/") {
		if _, _, err := net.ParseCIDR(trimmed); err != nil {
			return fmt.Errorf("%w: %q is not a network in CIDR form",
				ErrInvalidNetwork, value)
		}
		return nil
	}
	if net.ParseIP(trimmed) == nil {
		return fmt.Errorf(
			"%w: %q is not an address, a network, or one of any, none, localhost, localnets",
			ErrInvalidRecordValue, value)
	}
	return nil
}
