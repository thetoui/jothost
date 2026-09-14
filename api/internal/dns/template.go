package dns

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The records a new zone starts with.
//
// A zone used to be seeded with two records hard-coded in Go: an A for the
// apex and one for www. Anybody who wanted every new domain to start with an
// MX, a SPF record or a CAA added them by hand to each one, and anybody who
// did not want www deleted it each time.

// Errors returned when a template is refused.
var (
	// ErrTemplateNotFound means no such template on this server.
	ErrTemplateNotFound = errors.New("no such DNS template")
	// ErrTemplateNameTaken means another template already has that name.
	ErrTemplateNameTaken = errors.New("a template with that name already exists")
	// ErrTemplateBuiltin means the operation is not allowed on a built-in.
	ErrTemplateBuiltin = errors.New("the built-in template cannot be removed")
	// ErrTemplateInvalid means a record in the template is not one DNS accepts.
	ErrTemplateInvalid = errors.New("that template record is not valid")
	// ErrUnknownPlaceholder means the template names something unsubstitutable.
	ErrUnknownPlaceholder = errors.New("unknown placeholder")
)

// Placeholders a template record may use.
//
// Deliberately few. A template is a list of records with two things filled in;
// a template language is a thing nobody can debug at three in the morning when
// every new domain is being created with a broken zone.
const (
	// PlaceholderDomain is the zone being created, without a trailing dot.
	PlaceholderDomain = "{domain}"
	// PlaceholderIP is this host's own address.
	PlaceholderIP = "{ip}"
)

// Template is a named set of records applied to a new zone.
type Template struct {
	ID          string           `json:"id"`
	ServerID    string           `json:"server_id"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	IsDefault   bool             `json:"is_default"`
	Builtin     bool             `json:"builtin"`
	Records     []TemplateRecord `json:"records"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

// TemplateRecord is one line of a template.
type TemplateRecord struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Type     string `json:"type"`
	TTL      int    `json:"ttl"`
	Value    string `json:"value"`
	Priority int    `json:"priority"`
	Weight   int    `json:"weight"`
	Port     int    `json:"port"`
	Position int    `json:"position"`
}

// Render substitutes a template record's placeholders.
//
// The zone's own name and the host's address are the only two things a
// template can reach, and both are supplied by the caller rather than read
// here — so rendering is a pure function and can be run at write time against
// a sample to find out whether a template can produce valid records at all.
func (r TemplateRecord) Render(domain, address string) TemplateRecord {
	rendered := r
	rendered.Name = substitute(r.Name, domain, address)
	rendered.Value = substitute(r.Value, domain, address)
	return rendered
}

func substitute(text, domain, address string) string {
	text = strings.ReplaceAll(text, PlaceholderDomain, domain)
	text = strings.ReplaceAll(text, PlaceholderIP, address)
	return text
}

// UnknownPlaceholders reports placeholders a record uses that this panel
// cannot fill in.
//
// A template naming {ipv6} would otherwise be written happily and produce a
// zone containing the literal text "{ipv6}", which is a record that resolves
// to nothing and a bug nobody looks for in a template they wrote weeks ago.
func UnknownPlaceholders(text string) []string {
	var unknown []string
	rest := text

	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			return unknown
		}
		close := strings.Index(rest[open:], "}")
		if close < 0 {
			return unknown
		}
		token := rest[open : open+close+1]
		switch token {
		case PlaceholderDomain, PlaceholderIP:
		default:
			unknown = append(unknown, token)
		}
		rest = rest[open+close+1:]
	}
}

// sampleDomain and sampleAddress are what a template is rendered against when
// it is written.
//
// A template is checked by producing a record from it and validating that,
// rather than by validating the template text — because the text is not a
// record and the rules for one do not apply to the other. "{ip}" is not an
// address; the thing it becomes is.
const (
	sampleDomain  = "example.test"
	sampleAddress = "192.0.2.1"
)

// ValidateTemplateRecord checks that a template line can produce a real record.
//
// Rendered against a sample and then validated as an ordinary record, so a
// template that cannot work is refused when it is saved rather than when
// somebody creates a domain and gets a zone the name server will not load.
func ValidateTemplateRecord(record TemplateRecord) error {
	for _, field := range []struct{ label, text string }{
		{"name", record.Name},
		{"value", record.Value},
	} {
		if unknown := UnknownPlaceholders(field.text); len(unknown) > 0 {
			return fmt.Errorf("%w: %s in the %s. Use %s or %s",
				ErrUnknownPlaceholder, strings.Join(unknown, ", "), field.label,
				PlaceholderDomain, PlaceholderIP)
		}
	}

	rendered := record.Render(sampleDomain, sampleAddress)

	if err := validate.RecordType(rendered.Type); err != nil {
		return fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
	}
	if err := validate.RecordName(rendered.Name); err != nil {
		return fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
	}
	// The whole record, not just its value: MX needs a priority and SRV needs
	// a weight and a port, and a validator that only saw the value would pass
	// a template that produces a record the name server refuses.
	if err := validate.Record(validate.RecordValue{
		Type:     rendered.Type,
		Value:    rendered.Value,
		Priority: rendered.Priority,
		Weight:   rendered.Weight,
		Port:     rendered.Port,
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
	}
	if rendered.TTL != 0 {
		// Zero is "the zone's default", which is the ordinary case and is not
		// a TTL to check.
		if err := validate.TTL(rendered.TTL); err != nil {
			return fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
		}
	}
	return nil
}

// TemplateInput is a template being created or replaced.
type TemplateInput struct {
	Name        string
	Description string
	IsDefault   bool
	Records     []TemplateRecord
}

// Validate checks a whole template.
func (in TemplateInput) Validate() error {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return fmt.Errorf("%w: a template needs a name", ErrTemplateInvalid)
	}
	if len(name) > 80 {
		return fmt.Errorf("%w: the name is longer than 80 characters", ErrTemplateInvalid)
	}
	if len(in.Records) > 100 {
		return fmt.Errorf("%w: a template may hold at most 100 records", ErrTemplateInvalid)
	}

	for i, record := range in.Records {
		if err := ValidateTemplateRecord(record); err != nil {
			return fmt.Errorf("record %d: %w", i+1, err)
		}
	}
	return nil
}
