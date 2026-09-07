package dns

import (
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// Which zone a certificate's name belongs in, and which outcomes are worth
// stopping for. Both decisions are made without a database because both are
// judgements rather than queries — and both are wrong in ways nothing else
// would report: a record written into the wrong zone is served by nobody, and
// a blocking outcome that does not block is a failed ACME challenge.

func master(name string) Zone {
	return Zone{ID: "zone-" + name, Name: name, Kind: validate.ZoneMaster}
}

func TestTheLongestMatchingZoneWins(t *testing.T) {
	// With both example.com and shop.example.com served here, a record for
	// www.shop.example.com belongs in the second. Written into the first it
	// would be a name the delegated child overrides — present in a zone file,
	// answered by nobody, and invisible until a certificate fails.
	zones := []Zone{master("example.com"), master("shop.example.com")}

	zone, label, ok := zoneFor(zones, "www.shop.example.com")
	if !ok {
		t.Fatal("no zone was found for a name this host serves")
	}
	if zone.Name != "shop.example.com" {
		t.Fatalf("the name went into %q", zone.Name)
	}
	if label != "www" {
		t.Fatalf("label = %q, want www", label)
	}
}

func TestTheApexBecomesAtSign(t *testing.T) {
	zone, label, ok := zoneFor([]Zone{master("example.com")}, "example.com")
	if !ok || zone.Name != "example.com" {
		t.Fatalf("the apex did not match its own zone: %v %q", ok, zone.Name)
	}
	if label != "@" {
		t.Fatalf("label = %q, want @", label)
	}
}

func TestANameThisHostDoesNotServeMatchesNothing(t *testing.T) {
	// Deliberately not a suffix trick: "notexample.com" ends with the same
	// letters as "example.com" and belongs to somebody else entirely.
	for _, name := range []string{"elsewhere.test", "notexample.com"} {
		if _, _, ok := zoneFor([]Zone{master("example.com")}, name); ok {
			t.Fatalf("%q was matched to a zone that does not contain it", name)
		}
	}
}

func TestASecondaryZoneIsNeverWrittenTo(t *testing.T) {
	// A secondary is a copy of somebody else's zone. A record added here
	// survives until the next transfer and then vanishes, which is worse than
	// never adding it: the panel would report the name ready.
	zones := []Zone{{ID: "zone-1", Name: "example.com", Kind: validate.ZoneSlave}}
	if _, _, ok := zoneFor(zones, "www.example.com"); ok {
		t.Fatal("a secondary zone was offered as somewhere to write a record")
	}
}

func TestAReverseZoneIsNotUsedForAName(t *testing.T) {
	// A reverse zone's names are addresses backwards. Nothing on a certificate
	// belongs in one, and the suffix test alone would not say so.
	zones := []Zone{{
		ID: "zone-1", Name: "113.0.203.in-addr.arpa",
		Kind: validate.ZoneMaster, ReverseNetwork: "203.0.113.0/24",
	}}
	if _, _, ok := zoneFor(zones, "10.113.0.203.in-addr.arpa"); ok {
		t.Fatal("a reverse zone was offered as somewhere to write an address record")
	}
}

func TestOnlyTheOutcomesThatFailValidationBlock(t *testing.T) {
	// A name pointing at another machine will fail: the authority fetches the
	// challenge from there. A name this panel does not serve will usually
	// succeed — its DNS lives somewhere else and is very likely correct — so
	// treating it as a problem would put a warning on every ordinary issuance.
	blocking := map[string]bool{
		AlignElsewhere: true,
		AlignNoAddress: true,
		AlignReady:     false,
		AlignAdded:     false,
		AlignAliased:   false,
		AlignNotServed: false,
	}
	for status, want := range blocking {
		if got := (NameOutcome{Status: status}).Blocking(); got != want {
			t.Errorf("%s blocking = %v, want %v", status, got, want)
		}
	}
}
