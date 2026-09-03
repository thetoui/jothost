package dns

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Record is one resource record in a zone.
//
// The composite types keep their numbers in their own fields. A single text
// column would mean the zone writer parsing operator text at the moment it
// writes a file a root daemon reads, which is the one thing this panel avoids
// everywhere else.
type Record struct {
	// Name is relative to the zone, or "@" for the zone itself.
	Name string `json:"name"`
	Type string `json:"type"`
	// TTL of zero means the zone's default, which is what the file's $TTL
	// gives it — so the line is written without one.
	TTL   int    `json:"ttl"`
	Value string `json:"value"`
	// Priority is used by MX and SRV, Weight and Port by SRV, Flags and Tag by
	// CAA.
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Flags    int    `json:"flags"`
	Tag      string `json:"tag"`
}

// Zone is everything needed to write one zone file and one zone statement.
type Zone struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// PrimaryNS is the name in the SOA's MNAME field: the server secondaries
	// are told to ask.
	PrimaryNS  string `json:"primary_ns"`
	Hostmaster string `json:"hostmaster"`
	Serial     int64  `json:"serial"`
	Refresh    int    `json:"refresh"`
	Retry      int    `json:"retry"`
	Expire     int    `json:"expire"`
	Minimum    int    `json:"minimum"`
	TTL        int    `json:"ttl"`
	// Nameservers become the NS records at the apex. A zone with none is a
	// zone no resolver can be pointed at, so the writer refuses one.
	Nameservers []string `json:"nameservers"`
	Records     []Record `json:"records"`

	// DNSSEC asks named to sign the zone and to keep it signed. Key
	// generation, signing and rollover are all named's, through
	// dnssec-policy — see the package comment.
	DNSSEC bool `json:"dnssec"`
	// AllowTransfer lists the addresses permitted to pull the whole zone.
	// Empty means nobody, which is what BIND should always have been.
	AllowTransfer []string `json:"allow_transfer"`
	// AlsoNotify lists secondaries to notify beyond those named by NS
	// records — the ones that are not published, which is most of them.
	AlsoNotify []string `json:"also_notify"`
	// Masters is where a slave zone is transferred from.
	Masters []string `json:"masters"`
}

// RenderZone writes a zone file.
//
// Everything in it is generated. There is no passthrough of operator text
// except inside quoted strings, which are escaped, and inside names, which have
// been validated as names.
func RenderZone(zone Zone) (string, error) {
	if err := validate.Zone(zone.Name); err != nil {
		return "", err
	}
	if len(zone.Nameservers) == 0 {
		return "", fmt.Errorf("%w: %s has no name servers, so nothing could be delegated to it",
			validate.ErrInvalidZone, zone.Name)
	}

	var out strings.Builder
	fmt.Fprintf(&out, "%s. Do not edit: it is rewritten whenever a record changes.\n", marker)
	fmt.Fprintf(&out, "; zone: %s\n", zone.Name)
	fmt.Fprintf(&out, "$ORIGIN %s.\n", zone.Name)
	fmt.Fprintf(&out, "$TTL %d\n\n", zone.TTL)

	fmt.Fprintf(&out, "@\tIN\tSOA\t%s %s (\n", absolute(zone.PrimaryNS), soaMailbox(zone.Hostmaster))
	fmt.Fprintf(&out, "\t\t\t%d\t; serial\n", zone.Serial)
	fmt.Fprintf(&out, "\t\t\t%d\t; refresh\n", zone.Refresh)
	fmt.Fprintf(&out, "\t\t\t%d\t; retry\n", zone.Retry)
	fmt.Fprintf(&out, "\t\t\t%d\t; expire\n", zone.Expire)
	fmt.Fprintf(&out, "\t\t\t%d )\t; negative cache TTL\n\n", zone.Minimum)

	for _, server := range zone.Nameservers {
		fmt.Fprintf(&out, "@\tIN\tNS\t%s\n", absolute(server))
	}
	out.WriteString("\n")

	records := append([]Record(nil), zone.Records...)
	sortRecords(records)
	for _, record := range records {
		line, err := renderRecord(record)
		if err != nil {
			return "", err
		}
		out.WriteString(line)
	}
	return out.String(), nil
}

// sortRecords puts a zone file in a stable order.
//
// Stability is what makes the file diffable and what stops an unchanged zone
// being rewritten — and rewriting an unchanged zone means a reload, a
// re-signing, and a serial bump that every secondary in the world then pulls.
func sortRecords(records []Record) {
	sort.SliceStable(records, func(i, j int) bool {
		left, right := records[i], records[j]
		if left.Name != right.Name {
			// The apex first, then names in order. "@" sorts before letters
			// anyway, but saying so is cheaper than relying on it.
			if left.Name == "@" {
				return true
			}
			if right.Name == "@" {
				return false
			}
			return left.Name < right.Name
		}
		if left.Type != right.Type {
			return left.Type < right.Type
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.Value < right.Value
	})
}

// renderRecord writes one resource record line.
func renderRecord(record Record) (string, error) {
	if err := validate.RecordName(record.Name); err != nil {
		return "", err
	}
	if err := validate.TTL(record.TTL); err != nil {
		return "", err
	}
	if err := validate.Record(validate.RecordValue{
		Type:     record.Type,
		Value:    record.Value,
		Priority: record.Priority,
		Weight:   record.Weight,
		Port:     record.Port,
		Flags:    record.Flags,
		Tag:      record.Tag,
	}); err != nil {
		return "", err
	}

	ttl := ""
	if record.TTL > 0 {
		ttl = fmt.Sprintf("%d", record.TTL)
	}

	var rdata string
	switch record.Type {
	case validate.RecordA, validate.RecordAAAA:
		rdata = strings.TrimSpace(record.Value)
	case validate.RecordCNAME, validate.RecordNS, validate.RecordPTR:
		rdata = absolute(record.Value)
	case validate.RecordMX:
		rdata = fmt.Sprintf("%d %s", record.Priority, absolute(record.Value))
	case validate.RecordSRV:
		rdata = fmt.Sprintf("%d %d %d %s",
			record.Priority, record.Weight, record.Port, absolute(record.Value))
	case validate.RecordCAA:
		rdata = fmt.Sprintf("%d %s %s", record.Flags, record.Tag, quote(record.Value))
	case validate.RecordTXT:
		rdata = txtStrings(record.Value)
	default:
		return "", fmt.Errorf("%w: %q", validate.ErrInvalidRecordType, record.Type)
	}

	return fmt.Sprintf("%s\t%s\tIN\t%s\t%s\n", record.Name, ttl, record.Type, rdata), nil
}

// absolute makes a target name fully qualified when it already is.
//
// It does *not* add a dot to a name that lacks one. A bare "mail" in an MX
// record means mail.<zone>, and an operator who typed "aspmx.l.google.com"
// without a dot has published a mail server that does not exist — but the panel
// cannot know which they meant, and quietly appending a dot to one and not the
// other would make the same input mean different things in different places.
// What it does do is normalise the root and reject nothing, because the value
// has already been through validate.Hostname.
func absolute(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "."
	}
	return trimmed
}

// soaMailbox turns an email address into the SOA's RNAME field.
//
// The dot in a local part is escaped. "john.doe@example.com" written naively
// becomes "john.doe.example.com.", which is a different mailbox — mail about
// this zone would go to doe@example.com — and it is the kind of error nobody
// notices until they need the mail.
func soaMailbox(email string) string {
	at := strings.Index(email, "@")
	if at < 0 {
		return absolute(email)
	}
	local := strings.ReplaceAll(email[:at], ".", `\.`)
	return local + "." + strings.TrimSuffix(email[at+1:], ".") + "."
}

// quote wraps a value in quotes, escaping what would end the string early.
func quote(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

// txtStrings splits a TXT value into the character-strings a record holds.
//
// A single string is limited to 255 bytes by the wire format. Longer values —
// a DKIM public key is routinely longer — are legal and are carried as several
// strings, which resolvers concatenate. A writer that emitted one long string
// would produce a zone file named refuses to load, which is a name server that
// does not start.
func txtStrings(value string) string {
	if len(value) <= validate.MaxTXTString {
		return quote(value)
	}
	parts := make([]string, 0, len(value)/validate.MaxTXTString+1)
	for start := 0; start < len(value); start += validate.MaxTXTString {
		end := start + validate.MaxTXTString
		if end > len(value) {
			end = len(value)
		}
		parts = append(parts, quote(value[start:end]))
	}
	return "(" + strings.Join(parts, " ") + ")"
}
