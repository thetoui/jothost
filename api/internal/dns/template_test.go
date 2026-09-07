package dns_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/api/internal/dns"
)

// A template is validated by rendering it against a sample and checking the
// record that comes out, rather than by checking the template text. The text
// is not a record: "{ip}" is not an address, and the thing it becomes is.

func TestATemplateIsCheckedAsTheRecordItBecomes(t *testing.T) {
	// "{ip}" is not a valid A value, and this must be accepted anyway.
	record := dns.TemplateRecord{Name: "@", Type: "A", Value: dns.PlaceholderIP}
	if err := dns.ValidateTemplateRecord(record); err != nil {
		t.Fatalf("a placeholder A record was refused: %v", err)
	}

	// And what it becomes really is checked: "not-an-address" is not.
	bad := dns.TemplateRecord{Name: "@", Type: "A", Value: "not-an-address"}
	if err := dns.ValidateTemplateRecord(bad); err == nil {
		t.Fatal("an A record with a non-address value was accepted")
	}
}

func TestAPlaceholderThePanelCannotFillIsRefused(t *testing.T) {
	// Without this the template is written happily and every zone made from it
	// contains the literal text "{ipv6}" — a record resolving to nothing, in a
	// template nobody thinks to re-read weeks later.
	record := dns.TemplateRecord{Name: "@", Type: "AAAA", Value: "{ipv6}"}
	err := dns.ValidateTemplateRecord(record)
	if !errors.Is(err, dns.ErrUnknownPlaceholder) {
		t.Fatalf("an unknown placeholder was not refused: %v", err)
	}
	if !strings.Contains(err.Error(), "{ipv6}") {
		t.Fatalf("the error does not name the placeholder: %v", err)
	}
}

func TestTheDomainPlaceholderWorksInBothFields(t *testing.T) {
	record := dns.TemplateRecord{Name: "mail", Type: "CNAME", Value: dns.PlaceholderDomain}
	if err := dns.ValidateTemplateRecord(record); err != nil {
		t.Fatalf("a CNAME to the domain was refused: %v", err)
	}

	rendered := record.Render("shop.example", "203.0.113.9")
	if rendered.Value != "shop.example" {
		t.Fatalf("value rendered to %q", rendered.Value)
	}

	// And in the name, for a template that seeds a record under the domain.
	named := dns.TemplateRecord{Name: dns.PlaceholderDomain, Type: "TXT", Value: "hello"}
	if got := named.Render("shop.example", "203.0.113.9").Name; got != "shop.example" {
		t.Fatalf("name rendered to %q", got)
	}
}

func TestARecordNeedingMoreThanAValueIsCheckedWholly(t *testing.T) {
	// A validator that only looked at the value would pass a template whose
	// records a name server then refuses, and the failure would land on
	// whoever next created a domain rather than on whoever wrote the template.
	//
	// An MX priority of zero is *not* one of those cases: zero is legal and
	// commonly used, so it is accepted here. This test asserted otherwise
	// first, and the code was right.
	mx := dns.TemplateRecord{Name: "@", Type: "MX", Value: "mail." + dns.PlaceholderDomain}
	if err := dns.ValidateTemplateRecord(mx); err != nil {
		t.Fatalf("an MX with priority zero was refused: %v", err)
	}
	mx.Priority = 10
	if err := dns.ValidateTemplateRecord(mx); err != nil {
		t.Fatalf("a complete MX was refused: %v", err)
	}

	// An SRV with no port is refused, which is the case that matters: the
	// port is not in the value, so only a whole-record check finds it.
	srv := dns.TemplateRecord{Name: "_sip._tcp", Type: "SRV", Value: "sip." + dns.PlaceholderDomain}
	if err := dns.ValidateTemplateRecord(srv); err == nil {
		t.Fatal("an SRV with no port was accepted")
	}
	srv.Port = 5060
	srv.Weight = 10
	srv.Priority = 10
	if err := dns.ValidateTemplateRecord(srv); err != nil {
		t.Fatalf("a complete SRV was refused: %v", err)
	}

	// An MX whose value is not a hostname is refused whatever the priority.
	if err := dns.ValidateTemplateRecord(dns.TemplateRecord{
		Name: "@", Type: "MX", Value: "not a hostname", Priority: 10,
	}); err == nil {
		t.Fatal("an MX with a nonsense target was accepted")
	}
}

func TestTheBuiltInSeedStillValidates(t *testing.T) {
	// What migration 0028 inserts, and what the panel did in Go before it.
	for _, name := range []string{"@", "www"} {
		record := dns.TemplateRecord{Name: name, Type: "A", Value: dns.PlaceholderIP}
		if err := dns.ValidateTemplateRecord(record); err != nil {
			t.Fatalf("the built-in seed record %q is not valid: %v", name, err)
		}
	}
}

func TestAWholeTemplateIsChecked(t *testing.T) {
	good := dns.TemplateInput{
		Name: "With mail",
		Records: []dns.TemplateRecord{
			{Name: "@", Type: "A", Value: dns.PlaceholderIP},
			{Name: "@", Type: "MX", Value: "mail." + dns.PlaceholderDomain, Priority: 10},
			{Name: "@", Type: "TXT", Value: "v=spf1 a mx -all"},
		},
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("a reasonable template was refused: %v", err)
	}

	nameless := dns.TemplateInput{Records: good.Records}
	if err := nameless.Validate(); err == nil {
		t.Fatal("a template with no name was accepted")
	}

	// The error says which record, because a template with twenty lines and
	// "invalid record" is a template nobody can fix.
	broken := dns.TemplateInput{
		Name: "Broken",
		Records: []dns.TemplateRecord{
			{Name: "@", Type: "A", Value: dns.PlaceholderIP},
			{Name: "@", Type: "A", Value: "nonsense"},
		},
	}
	err := broken.Validate()
	if err == nil {
		t.Fatal("a template with a broken record was accepted")
	}
	if !strings.Contains(err.Error(), "record 2") {
		t.Fatalf("the error does not say which record: %v", err)
	}
}
