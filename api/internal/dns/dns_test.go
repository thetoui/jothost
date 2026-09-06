package dns

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The panel's own rules, tested without a database or a host: what gets sent to
// a remote provider, and what comes back from one.
//
// The Cloudflare client is exercised against a stub that answers like the real
// API rather than against a mock of my own design, because the thing worth
// testing is the shape of the requests it sends — a record this panel formats
// wrongly is a record that silently becomes something else in somebody's live
// DNS.

func TestToCloudflarePutsTheCompositeTypesInStructuredFields(t *testing.T) {
	// Cloudflare takes SRV and CAA as objects. Formatting them into a string
	// would mean this panel's numbers becoming text and being parsed back by
	// somebody else's parser.
	srv := toCloudflare(RemoteRecord{
		Name: "_sip._tcp.example.com", Type: validate.RecordSRV,
		Priority: 10, Weight: 5, Port: 5060, Value: "sip.example.com.",
	})
	if srv.Data == nil {
		t.Fatal("an SRV record was sent as text")
	}
	if deref(srv.Data.Port) != 5060 || deref(srv.Data.Priority) != 10 {
		t.Errorf("SRV data = %+v", srv.Data)
	}
	if srv.Data.Target != "sip.example.com" {
		t.Errorf("the target kept its trailing dot: %q", srv.Data.Target)
	}

	caa := toCloudflare(RemoteRecord{
		Name: "example.com", Type: validate.RecordCAA,
		Flags: 0, Tag: "issue", Value: "letsencrypt.org",
	})
	if caa.Data == nil || caa.Data.Tag != "issue" {
		t.Fatalf("CAA data = %+v", caa.Data)
	}

	mx := toCloudflare(RemoteRecord{
		Name: "example.com", Type: validate.RecordMX,
		Priority: 10, Value: "mail.example.com.",
	})
	if mx.Priority == nil || *mx.Priority != 10 {
		t.Errorf("the MX priority was not sent: %+v", mx)
	}
}

func TestToCloudflareSendsAnInheritedTTLAsAutomatic(t *testing.T) {
	// The panel's zero means "the zone's default". Cloudflare's zone default is
	// theirs, not this panel's, and their word for it is 1 — a literal 0 is
	// refused as an invalid TTL.
	record := toCloudflare(RemoteRecord{Name: "www.example.com", Type: "A", Value: "203.0.113.1"})
	if record.TTL != 1 {
		t.Fatalf("TTL = %d, want 1 (Cloudflare's automatic)", record.TTL)
	}
}

// stubCloudflare answers like the real API, holding records in memory.
type stubCloudflare struct {
	records []cfRecord
	created []cfRecord
	deleted []string
}

func (s *stubCloudflare) handler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /zones", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Errorf("the token was not sent: %q", r.Header.Get("Authorization"))
		}
		writeCF(w, []map[string]string{{"id": "zone-1", "name": r.URL.Query().Get("name")}})
	})
	mux.HandleFunc("GET /zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") != "1" {
			writeCF(w, []cfRecord{})
			return
		}
		writeCF(w, s.records)
	})
	mux.HandleFunc("POST /zones/zone-1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		var record cfRecord
		if err := json.NewDecoder(r.Body).Decode(&record); err != nil {
			t.Errorf("decode: %v", err)
		}
		s.created = append(s.created, record)
		writeCF(w, record)
	})
	mux.HandleFunc("DELETE /zones/zone-1/dns_records/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.deleted = append(s.deleted, r.PathValue("id"))
		writeCF(w, map[string]string{"id": r.PathValue("id")})
	})
	return mux
}

func writeCF(w http.ResponseWriter, result any) {
	encoded, _ := json.Marshal(result)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":` + string(encoded) + `}`))
}

func TestSyncCreatesWhatIsMissingAndLeavesTheRestAlone(t *testing.T) {
	stub := &stubCloudflare{records: []cfRecord{
		{ID: "r1", Type: "A", Name: "www.example.com", Content: "203.0.113.1", TTL: 1},
		// A record somebody added in Cloudflare's dashboard that this panel has
		// never heard of.
		{ID: "r2", Type: "TXT", Name: "example.com", Content: "google-site-verification=abc", TTL: 1},
	}}
	server := httptest.NewServer(stub.handler(t))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("test-token", "")
	result, err := client.SyncZone(context.Background(), "example.com", []RemoteRecord{
		{Name: "www.example.com", Type: "A", Value: "203.0.113.1"},
		{Name: "mail.example.com", Type: "A", Value: "203.0.113.2"},
	}, false)
	if err != nil {
		t.Fatalf("SyncZone: %v", err)
	}

	if result.Created != 1 || result.Unchanged != 1 {
		t.Errorf("created = %d, unchanged = %d, want 1 and 1", result.Created, result.Unchanged)
	}
	// Without prune, the verification TXT the panel does not know about is left
	// exactly where it was.
	if len(stub.deleted) != 0 {
		t.Errorf("records were deleted without prune: %v", stub.deleted)
	}
	if len(stub.created) != 1 || stub.created[0].Name != "mail.example.com" {
		t.Errorf("created = %+v", stub.created)
	}
}

func TestSyncWithPruneRemovesWhatThePanelDoesNotHave(t *testing.T) {
	stub := &stubCloudflare{records: []cfRecord{
		{ID: "r1", Type: "A", Name: "www.example.com", Content: "203.0.113.1", TTL: 1},
		{ID: "r2", Type: "A", Name: "old.example.com", Content: "203.0.113.9", TTL: 1},
		// Cloudflare's own NS records, which it maintains and refuses to have
		// deleted. Sending a delete for them is an error about something the
		// operator did not ask for.
		{ID: "r3", Type: "NS", Name: "example.com", Content: "ns1.cloudflare.com", TTL: 1},
	}}
	server := httptest.NewServer(stub.handler(t))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("test-token", "")
	result, err := client.SyncZone(context.Background(), "example.com", []RemoteRecord{
		{Name: "www.example.com", Type: "A", Value: "203.0.113.1"},
	}, true)
	if err != nil {
		t.Fatalf("SyncZone: %v", err)
	}

	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}
	if len(stub.deleted) != 1 || stub.deleted[0] != "r2" {
		t.Errorf("deleted = %v, want only the stale record", stub.deleted)
	}
	if len(result.Skipped) == 0 {
		t.Error("Cloudflare's own NS records were not reported as skipped")
	}
}

func TestSyncSkipsTheZonesOwnNameServers(t *testing.T) {
	stub := &stubCloudflare{}
	server := httptest.NewServer(stub.handler(t))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("test-token", "")
	result, err := client.SyncZone(context.Background(), "example.com", []RemoteRecord{
		{Name: "example.com", Type: validate.RecordNS, Value: "ns1.example.com."},
		{Name: "www.example.com", Type: "A", Value: "203.0.113.1"},
	}, false)
	if err != nil {
		t.Fatalf("SyncZone: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("created = %d, want only the A record", result.Created)
	}
	if len(result.Skipped) != 1 || !strings.Contains(result.Skipped[0], "NS") {
		t.Errorf("skipped = %v", result.Skipped)
	}
}

func TestSyncReportsTheProvidersOwnErrorMessage(t *testing.T) {
	// Cloudflare's message says what is wrong far better than a status code
	// does. The token must not be in it, and is not: it is only ever a header.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"success":false,"errors":[{"code":9103,"message":"Invalid access token"}],"result":null}`))
	}))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("wrong-token", "")
	_, err := client.SyncZone(context.Background(), "example.com", nil, false)
	if err == nil {
		t.Fatal("a refused request did not produce an error")
	}
	if !strings.Contains(err.Error(), "Invalid access token") {
		t.Errorf("the provider's own message was lost: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-token") {
		t.Errorf("the token appeared in an error message: %v", err)
	}
}

func TestUnknownProviderKindIsRefusedBeforeAnythingIsStored(t *testing.T) {
	if _, err := DefaultRemoteFactory("route53", "token", ""); err == nil {
		t.Fatal("a provider this panel has no client for was accepted")
	}
	if _, err := DefaultRemoteFactory("cloudflare", "token", ""); err != nil {
		t.Fatalf("cloudflare was refused: %v", err)
	}
}

func TestQualifyBuildsTheNameAProviderExpects(t *testing.T) {
	if got := qualify("@", "example.com"); got != "example.com" {
		t.Errorf("the apex qualified to %q", got)
	}
	if got := qualify("www", "example.com"); got != "www.example.com" {
		t.Errorf("a label qualified to %q", got)
	}
}

func TestDefaultSettingsAreAnAuthoritativeServersDefaults(t *testing.T) {
	settings := DefaultSettings("server-1")
	if settings.DNSSECPolicy != "default" {
		t.Errorf("policy = %q", settings.DNSSECPolicy)
	}
	// Empty rather than a guess: transfers are denied unless somebody is named,
	// and BIND's own default of allowing them to anyone hands every name in a
	// customer's zone to whoever asks.
	if len(settings.AllowTransfer) != 0 {
		t.Errorf("transfers were allowed by default: %v", settings.AllowTransfer)
	}
	if settings.DefaultTTL != 3600 {
		t.Errorf("default TTL = %d", settings.DefaultTTL)
	}
}

// TestFetchZoneReadsTheProvidersCopy is the import's own half of the round
// trip: the panel can now read a zone it did not write.
func TestFetchZoneReadsTheProvidersCopy(t *testing.T) {
	stub := &stubCloudflare{records: []cfRecord{
		{ID: "r1", Type: "A", Name: "www.example.com", Content: "203.0.113.1", TTL: 1},
		{ID: "r2", Type: "MX", Name: "example.com", Content: "mail.example.com", TTL: 3600,
			Priority: intPtr(10)},
	}}
	server := httptest.NewServer(stub.handler(t))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("test-token", "")
	records, err := client.FetchZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("FetchZone: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2: %+v", len(records), records)
	}

	byName := map[string]RemoteRecord{}
	for _, record := range records {
		byName[record.Name] = record
	}

	// Cloudflare's automatic TTL is 1, which is its way of saying "we decide".
	// The panel says the same thing with 0, and importing a literal one-second
	// TTL would be a lie the next push would send straight back.
	if got := byName["www.example.com"].TTL; got != 0 {
		t.Errorf("automatic TTL came back as %d, want 0", got)
	}
	if got := byName["example.com"].TTL; got != 3600 {
		t.Errorf("a real TTL was not preserved: %d", got)
	}
	if got := byName["example.com"].Priority; got != 10 {
		t.Errorf("MX priority = %d, want 10", got)
	}
}

// TestFetchZoneLeavesTheProvidersOwnRecordsBehind.
//
// A zone's SOA and its apex NS belong to whoever serves it. Importing
// Cloudflare's would put records in the panel that it shows as editable and
// that every later push refuses.
func TestFetchZoneLeavesTheProvidersOwnRecordsBehind(t *testing.T) {
	stub := &stubCloudflare{records: []cfRecord{
		{ID: "r1", Type: "SOA", Name: "example.com", Content: "ns.cloudflare.com ...", TTL: 1},
		{ID: "r2", Type: "NS", Name: "example.com", Content: "kim.ns.cloudflare.com", TTL: 1},
		// An NS below the apex is a delegation the zone's owner made, and it is
		// theirs to keep.
		{ID: "r3", Type: "NS", Name: "sub.example.com", Content: "ns1.elsewhere.test", TTL: 1},
		{ID: "r4", Type: "A", Name: "www.example.com", Content: "203.0.113.1", TTL: 1},
	}}
	server := httptest.NewServer(stub.handler(t))
	defer server.Close()

	previous := cloudflareAPI
	cloudflareAPI = server.URL
	defer func() { cloudflareAPI = previous }()

	client := NewCloudflare("test-token", "")
	records, err := client.FetchZone(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("FetchZone: %v", err)
	}

	for _, record := range records {
		if record.Type == "SOA" {
			t.Errorf("the provider's SOA was imported")
		}
		if record.Type == "NS" && record.Name == "example.com" {
			t.Errorf("the provider's own apex NS was imported")
		}
	}
	// The delegation below the apex is the owner's and must survive.
	var kept bool
	for _, record := range records {
		if record.Type == "NS" && record.Name == "sub.example.com" {
			kept = true
		}
	}
	if !kept {
		t.Error("a delegation below the apex was dropped")
	}
}

// TestRelativeToIsQualifyInReverse.
//
// The panel stores names relative to the zone and providers address them
// absolutely, so an import that got this wrong would write "www.example.com"
// as a label inside example.com and produce www.example.com.example.com.
func TestRelativeToIsQualifyInReverse(t *testing.T) {
	cases := map[string]string{
		"example.com":     "@",
		"example.com.":    "@",
		"www.example.com": "www",
		"WWW.EXAMPLE.COM": "www",
		"a.b.example.com": "a.b",
		"elsewhere.test":  "elsewhere.test",
	}
	for input, want := range cases {
		if got := relativeTo(input, "example.com"); got != want {
			t.Errorf("relativeTo(%q) = %q, want %q", input, got, want)
		}
	}
}
