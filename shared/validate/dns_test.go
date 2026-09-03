package validate

import "testing"

func TestZoneRefusesAWildcardAndAcceptsAReverseName(t *testing.T) {
	if err := Zone("*.example.com"); err == nil {
		t.Fatal("a wildcard zone was accepted")
	}
	if err := Zone("113.0.203.in-addr.arpa"); err != nil {
		t.Fatalf("a reverse zone was refused: %v", err)
	}
	if err := Zone("example.com"); err != nil {
		t.Fatalf("a forward zone was refused: %v", err)
	}
}

func TestNormalizeRecordNameCollapsesEveryWayOfWritingTheApex(t *testing.T) {
	// All four are the same name. Storing them separately would let two
	// records claim the apex and let a delete miss the one that was meant.
	for _, name := range []string{"", "@", "example.com", "example.com."} {
		if got := NormalizeRecordName(name, "example.com"); got != "@" {
			t.Errorf("NormalizeRecordName(%q) = %q, want @", name, got)
		}
	}
	if got := NormalizeRecordName("WWW.example.com.", "example.com"); got != "www" {
		t.Errorf("a fully qualified name was not made relative: %q", got)
	}
	if got := NormalizeRecordName("www", "example.com"); got != "www" {
		t.Errorf("a relative name was changed: %q", got)
	}
}

func TestRecordNameAllowsServiceLabelsAndOnlyALeadingWildcard(t *testing.T) {
	for _, name := range []string{"@", "www", "_dmarc", "_sip._tcp", "*.dev", "a-b"} {
		if err := RecordName(name); err != nil {
			t.Errorf("RecordName(%q) refused: %v", name, err)
		}
	}
	for _, name := range []string{"", "dev.*", "www.", "-lead", "trail-", "a b"} {
		if err := RecordName(name); err == nil {
			t.Errorf("RecordName(%q) was accepted", name)
		}
	}
}

func TestRecordChecksTheAddressFamilyAndNotJustTheSyntax(t *testing.T) {
	// An AAAA holding an IPv4 address parses, is written, is served, and
	// resolves for nobody. The syntax check alone would let it through.
	if err := Record(RecordValue{Type: RecordAAAA, Value: "203.0.113.1"}); err == nil {
		t.Fatal("an AAAA record with an IPv4 address was accepted")
	}
	if err := Record(RecordValue{Type: RecordA, Value: "2001:db8::1"}); err == nil {
		t.Fatal("an A record with an IPv6 address was accepted")
	}
	if err := Record(RecordValue{Type: RecordA, Value: "203.0.113.1"}); err != nil {
		t.Fatalf("a valid A record was refused: %v", err)
	}
	if err := Record(RecordValue{Type: RecordAAAA, Value: "2001:db8::1"}); err != nil {
		t.Fatalf("a valid AAAA record was refused: %v", err)
	}
}

func TestRecordChecksTheCompositeTypesFieldByField(t *testing.T) {
	srv := RecordValue{Type: RecordSRV, Priority: 10, Weight: 5, Port: 5060, Value: "sip.example.com."}
	if err := Record(srv); err != nil {
		t.Fatalf("a valid SRV record was refused: %v", err)
	}
	noPort := srv
	noPort.Port = 0
	if err := Record(noPort); err == nil {
		t.Error("an SRV record with no port was accepted")
	}
	tooBig := srv
	tooBig.Priority = 70000
	if err := Record(tooBig); err == nil {
		t.Error("an SRV priority above 65535 was accepted")
	}

	caa := RecordValue{Type: RecordCAA, Flags: 0, Tag: "issue", Value: "letsencrypt.org"}
	if err := Record(caa); err != nil {
		t.Fatalf("a valid CAA record was refused: %v", err)
	}
	// "issued" is not a CAA tag. It is not an error in DNS either: it is a
	// policy that silently authorises nobody.
	misspelt := caa
	misspelt.Tag = "issued"
	if err := Record(misspelt); err == nil {
		t.Error("a misspelt CAA tag was accepted")
	}
}

func TestTXTRefusesControlCharactersAndAllowsQuotes(t *testing.T) {
	// SPF and DKIM values contain quotes; the writer escapes them.
	if err := Record(RecordValue{Type: RecordTXT, Value: `v=spf1 include:"a" -all`}); err != nil {
		t.Fatalf("a quoted TXT value was refused: %v", err)
	}
	if err := Record(RecordValue{Type: RecordTXT, Value: "line\nbreak"}); err == nil {
		t.Fatal("a TXT value containing a newline was accepted")
	}
}

func TestTTLTreatsZeroAsTheZoneDefault(t *testing.T) {
	if err := TTL(0); err != nil {
		t.Fatalf("TTL(0) refused: %v", err)
	}
	if err := TTL(30); err == nil {
		t.Error("a 30-second TTL was accepted")
	}
	if err := TTL(3600); err != nil {
		t.Errorf("TTL(3600) refused: %v", err)
	}
}

func TestSOATimersRefuseTheCombinationsThatHealSlowly(t *testing.T) {
	if err := SOATimers(3600, 900, 1209600, 3600); err != nil {
		t.Fatalf("sensible timers were refused: %v", err)
	}
	// A retry longer than the refresh: a zone that heals more slowly the more
	// it breaks. Each value is in range on its own.
	if err := SOATimers(3600, 7200, 1209600, 3600); err == nil {
		t.Error("a retry longer than the refresh was accepted")
	}
	if err := SOATimers(3600, 900, 1209600, 0); err == nil {
		t.Error("a zero negative-cache TTL was accepted")
	}
}

func TestHostmasterRefusesWhatCannotBecomeAnSOAMailbox(t *testing.T) {
	if err := Hostmaster("hostmaster@example.com"); err != nil {
		t.Fatalf("a valid hostmaster was refused: %v", err)
	}
	// The writer escapes the dot in the local part. What it cannot do
	// anything sensible with is a name that is not an address at all.
	if err := Hostmaster("john.doe@example.com"); err != nil {
		t.Fatalf("a dotted local part was refused: %v", err)
	}
	for _, bad := range []string{"", "hostmaster", "@example.com", "a@b@c.com", "a b@example.com"} {
		if err := Hostmaster(bad); err == nil {
			t.Errorf("Hostmaster(%q) was accepted", bad)
		}
	}
}

func TestReverseZoneOnlyBuildsTheAlignedPrefixes(t *testing.T) {
	cases := map[string]string{
		"203.0.113.0/24": "113.0.203.in-addr.arpa",
		"198.51.0.0/16":  "51.198.in-addr.arpa",
		"10.0.0.0/8":     "10.in-addr.arpa",
		"2001:db8::/32":  "8.b.d.0.1.0.0.2.ip6.arpa",
	}
	for network, want := range cases {
		got, err := ReverseZone(network)
		if err != nil {
			t.Errorf("ReverseZone(%q): %v", network, err)
			continue
		}
		if got != want {
			t.Errorf("ReverseZone(%q) = %q, want %q", network, got, want)
		}
	}

	// A /25 has a reverse zone, but only as an RFC 2317 delegation the
	// address's owner has to make. Generating one would produce a zone that is
	// correct, served, and never asked.
	if _, err := ReverseZone("203.0.113.0/25"); err == nil {
		t.Error("a /25 reverse zone was generated")
	}
	if _, err := ReverseZone("not a network"); err == nil {
		t.Error("a non-network was accepted")
	}
}

func TestReversePointerNameRefusesAnAddressOutsideTheZone(t *testing.T) {
	name, err := ReversePointerName("203.0.113.7", "203.0.113.0/24")
	if err != nil {
		t.Fatalf("an address inside the zone was refused: %v", err)
	}
	if name != "7" {
		t.Fatalf("owner name = %q, want 7", name)
	}

	// Written into the wrong zone this record is invisible until somebody's
	// mail is rejected.
	if _, err := ReversePointerName("198.51.100.7", "203.0.113.0/24"); err == nil {
		t.Fatal("a PTR for an address outside the zone was accepted")
	}
}

func TestRecordTypeIsAnAllowlist(t *testing.T) {
	for _, supported := range RecordTypes {
		if err := RecordType(supported); err != nil {
			t.Errorf("RecordType(%q) refused: %v", supported, err)
		}
	}
	for _, unsupported := range []string{"", "a", "ANY", "AXFR", "DNSKEY"} {
		if err := RecordType(unsupported); err == nil {
			t.Errorf("RecordType(%q) was accepted", unsupported)
		}
	}
}
