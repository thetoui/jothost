//go:build linux

package dns

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// The parsers here are tested against output copied from a live BIND 9.18.49,
// not invented.
//
// `rndc dnssec -status` and `rndc zonestatus` print text meant for a person and
// are the only interface BIND offers for "what is this zone's signing state"
// and "what is the server actually serving". A stub printing what I imagined
// that output looks like would test my imagination — which is how Phase 7.1
// shipped a session monitor that silently dropped every session.
//
// One of the samples below exists because reality surprised me: zonestatus
// reports *two* serials, and with inline signing they differ. The file said
// 2026090302 and the server was serving 2026090304, seconds after loading it.

func init() {
	timeNow = func() time.Time { return time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC) }
}

// Frozen output from the live server.
const (
	namedVersion = "BIND 9.18.49 (Extended Support Version) <id:cd4a53b>"

	dnssecStatusOutput = `dnssec-policy: default
current time:  Wed Sep  2 22:46:48 2026

key: 4079 (ECDSAP256SHA256), CSK
  published:      yes - since Wed Sep  2 22:46:44 2026
  key signing:    yes - since Wed Sep  2 22:46:44 2026
  zone signing:   yes - since Wed Sep  2 22:46:44 2026

  No rollover scheduled
  - goal:           omnipresent
  - dnskey:         rumoured
  - ds:             hidden
  - zone rrsig:     rumoured
  - key rrsig:      rumoured
`

	zoneStatusOutput = `name: example.test
type: primary
files: /var/bind/jothost/example.test.zone
serial: 2026090302
signed serial: 2026090304
nodes: 3
last loaded: Wed, 02 Sep 2026 22:46:44 GMT
secure: yes
inline signing: yes
key maintenance: automatic
next key event: Thu, 03 Sep 2026 00:51:44 GMT
dynamic: no
`

	dsFromKeyOutput = "example.test. IN DS 4079 13 2 " +
		"814CBB0808F91D6E5A76320511A9F8811FFC5BD5910EC2B6A93F743E05C05351\n"

	checkconfPrintOutput = `options {
	directory "/var/bind";
	listen-on  {
		"any";
	};
	recursion no;
	allow-transfer  {
		"none";
	};
};
`
)

// stubService stands in for the service manager.
type stubService struct {
	starts  int
	running bool
	fail    error
}

func (s *stubService) Start(context.Context) error {
	s.starts++
	if s.fail != nil {
		return s.fail
	}
	s.running = true
	return nil
}
func (s *stubService) Running(context.Context) bool { return s.running }

// recording writes stubs for every tool this package drives and returns a
// Provider using them, plus a function returning the calls they saw.
func recording(t *testing.T, scripts map[string]string) (*Provider, *stubService, func() []string) {
	t.Helper()

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tools := []string{
		CommandNamed, CommandNamedCheckconf, CommandNamedCheckzone,
		CommandRndc, CommandDNSSECFromKey,
	}
	specs := make([]command.Spec, 0, len(tools))
	for _, name := range tools {
		stub := filepath.Join(binDir, name)
		body := "#!/bin/sh\nprintf '%s %s\\n' " + name + " \"$*\" >> " + callLog + "\n"
		body += scripts[name]
		if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
		specs = append(specs, command.Spec{Name: name, Path: stub})
	}

	runner, err := command.NewRunner(specs...)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	service := &stubService{running: true}
	provider := NewProvider(Options{
		Runner: runner,
		Paths: Paths{
			ConfigDir: filepath.Join(dir, "bind"),
			StateDir:  filepath.Join(dir, "zones"),
		},
		Service: service,
	})
	if err := os.MkdirAll(provider.paths.ConfigDir, 0o755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}

	calls := func() []string {
		data, err := os.ReadFile(callLog)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return provider, service, calls
}

// versionScript makes `named -v` answer like the real one.
func versionScript() map[string]string {
	return map[string]string{
		CommandNamed: "if [ \"$1\" = \"-v\" ]; then echo '" + namedVersion + "'; fi\n",
	}
}

func exampleZone() Zone {
	return Zone{
		Name:        "example.test",
		Kind:        validate.ZoneMaster,
		PrimaryNS:   "ns1.example.test.",
		Hostmaster:  "hostmaster@example.test",
		Serial:      2026090301,
		Refresh:     3600,
		Retry:       900,
		Expire:      1209600,
		Minimum:     3600,
		TTL:         3600,
		Nameservers: []string{"ns1.example.test."},
		Records: []Record{
			{Name: "www", Type: validate.RecordA, Value: "203.0.113.20"},
			{Name: "@", Type: validate.RecordMX, Priority: 10, Value: "mail.example.test."},
		},
	}
}

func TestRenderZoneWritesAFileNamedCheckzoneAccepts(t *testing.T) {
	// The shape is checked here; that a real named-checkzone accepts it is
	// checked in tests/integration/phase13_dns.sh, against the real tool.
	content, err := RenderZone(exampleZone())
	if err != nil {
		t.Fatalf("RenderZone: %v", err)
	}

	for _, want := range []string{
		"$ORIGIN example.test.",
		"$TTL 3600",
		"SOA\tns1.example.test. hostmaster.example.test.",
		"2026090301\t; serial",
		"@\tIN\tNS\tns1.example.test.",
		"www\t\tIN\tA\t203.0.113.20",
		"@\t\tIN\tMX\t10 mail.example.test.",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("the zone file does not contain %q:\n%s", want, content)
		}
	}
}

func TestRenderZoneCommentsWithASemicolon(t *testing.T) {
	// A zone file's comment character is ";". This header started as "#", which
	// is not a comment at all: named read the line as a resource record and
	// refused the zone with "unknown RR type 'Managed'" — a name server that
	// will not start, from a file that looked fine.
	content, err := RenderZone(exampleZone())
	if err != nil {
		t.Fatalf("RenderZone: %v", err)
	}
	for index, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "#") {
			t.Fatalf("line %d begins with a #, which a zone file does not treat as a comment: %q",
				index+1, line)
		}
	}
	if !strings.HasPrefix(content, ";") {
		t.Fatalf("the file does not start with a comment:\n%s", content)
	}
}

func TestRenderZoneRefusesAZoneWithNoNameServers(t *testing.T) {
	// A zone nothing can be delegated to. It is valid as a file and useless as
	// a zone, and named-checkzone only warns about it.
	zone := exampleZone()
	zone.Nameservers = nil
	if _, err := RenderZone(zone); err == nil {
		t.Fatal("a zone with no NS records was rendered")
	}
}

func TestRenderZoneEscapesTheDotInAHostmasterLocalPart(t *testing.T) {
	// "john.doe@example.test" written naively becomes john.doe.example.test.,
	// which is doe@example.test — a different mailbox, and nobody notices
	// until they need the mail.
	zone := exampleZone()
	zone.Hostmaster = "john.doe@example.test"
	content, err := RenderZone(zone)
	if err != nil {
		t.Fatalf("RenderZone: %v", err)
	}
	if !strings.Contains(content, `john\.doe.example.test.`) {
		t.Fatalf("the local part's dot was not escaped:\n%s", content)
	}
}

func TestRenderZoneSplitsALongTXTValue(t *testing.T) {
	// A DKIM key is routinely longer than 255 bytes. One long string is a zone
	// file named refuses to load, which is a name server that does not start.
	zone := exampleZone()
	long := strings.Repeat("k", 400)
	zone.Records = append(zone.Records, Record{
		Name: "selector._domainkey", Type: validate.RecordTXT, Value: long,
	})
	content, err := RenderZone(zone)
	if err != nil {
		t.Fatalf("RenderZone: %v", err)
	}
	line := ""
	for _, candidate := range strings.Split(content, "\n") {
		if strings.Contains(candidate, "_domainkey") {
			line = candidate
		}
	}
	if !strings.HasPrefix(strings.SplitN(line, "TXT\t", 2)[1], "(") {
		t.Fatalf("a long TXT value was not split into strings: %s", line)
	}
	if strings.Contains(line, strings.Repeat("k", 256)) {
		t.Fatalf("a string longer than 255 bytes was written: %s", line)
	}
}

func TestRenderZoneRefusesRecordsItsOwnValidatorRejects(t *testing.T) {
	zone := exampleZone()
	zone.Records = []Record{{Name: "www", Type: validate.RecordA, Value: "not-an-address"}}
	if _, err := RenderZone(zone); !errors.Is(err, validate.ErrInvalidRecordValue) {
		t.Fatalf("error = %v, want an invalid value", err)
	}
}

func TestRenderIncludeDeniesTransfersUnlessSomebodyWasNamed(t *testing.T) {
	// BIND's own default is to allow a transfer to anyone, which hands every
	// name in a customer's zone to whoever asks for it.
	include, err := RenderInclude(Paths{}, Settings{}, []Zone{exampleZone()})
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if !strings.Contains(include, "allow-transfer { none; };") {
		t.Fatalf("transfers were not denied:\n%s", include)
	}

	zone := exampleZone()
	zone.AllowTransfer = []string{"198.51.100.5", "203.0.113.0/24"}
	include, err = RenderInclude(Paths{}, Settings{}, []Zone{zone})
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	if !strings.Contains(include, "allow-transfer { 198.51.100.5; 203.0.113.0/24; };") {
		t.Fatalf("the transfer list was not written:\n%s", include)
	}
}

func TestRenderIncludeSignsOnlyPrimaryZones(t *testing.T) {
	primary := exampleZone()
	primary.DNSSEC = true

	secondary := Zone{
		Name:    "mirror.test",
		Kind:    validate.ZoneSlave,
		Masters: []string{"198.51.100.5"},
		// Asking named to sign a zone it receives already signed is a
		// configuration it refuses to load, so the renderer must drop this.
		DNSSEC: true,
	}

	include, err := RenderInclude(Paths{}, Settings{}, []Zone{primary, secondary})
	if err != nil {
		t.Fatalf("RenderInclude: %v", err)
	}
	blocks := strings.SplitN(include, `zone "mirror.test"`, 2)
	if len(blocks) != 2 {
		t.Fatalf("the secondary zone was not written:\n%s", include)
	}
	if !strings.Contains(blocks[0], "dnssec-policy") {
		t.Error("the primary zone was not signed")
	}
	if strings.Contains(blocks[1], "dnssec-policy") {
		t.Error("the secondary zone was given a signing policy")
	}
	if !strings.Contains(blocks[1], "primaries { 198.51.100.5; };") {
		t.Errorf("the secondary was not pointed at its primary:\n%s", blocks[1])
	}
}

func TestRenderIncludeRefusesASecondaryWithNoPrimary(t *testing.T) {
	_, err := RenderInclude(Paths{}, Settings{}, []Zone{{
		Name: "mirror.test", Kind: validate.ZoneSlave,
	}})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want an invalid configuration", err)
	}
}

func TestAddIncludeIsAppendedOnceAndOnlyOnce(t *testing.T) {
	paths := Paths{ConfigDir: "/etc/bind"}.withDefaults()
	foreign := "options {\n\trecursion no;\n};\n"

	if HasInclude(foreign, paths) {
		t.Fatal("a configuration with no include was reported as having one")
	}
	extended := AddInclude(foreign, paths)
	if !HasInclude(extended, paths) {
		t.Fatal("the include was not added")
	}
	if !strings.Contains(extended, "options {") {
		t.Fatal("the operator's own configuration was not preserved")
	}
	// A second include of the same file is a duplicate-zone error at the next
	// reload, which is a server that will not start.
	if strings.Count(extended, paths.Include()) != 1 {
		t.Fatalf("the include appears more than once:\n%s", extended)
	}
}

func TestReconcileWritesValidatesAndReloads(t *testing.T) {
	provider, service, calls := recording(t, versionScript())

	result, err := provider.Reconcile(context.Background(), Desired{
		Zones: []Zone{exampleZone()},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	if len(result.Changed) != 1 || result.Changed[0] != "example.test" {
		t.Errorf("changed = %v, want example.test", result.Changed)
	}
	if !result.CreatedConfig {
		t.Error("a host with no named.conf was not given one")
	}
	if !result.Reloaded {
		t.Error("the change was not reloaded")
	}
	if _, err := os.Stat(provider.paths.ZoneFile("example.test")); err != nil {
		t.Errorf("the zone file was not written: %v", err)
	}

	joined := strings.Join(calls(), "\n")
	// The candidate is checked before anything is installed, and the whole
	// configuration is checked before the server is told to read it.
	if !strings.Contains(joined, "named-checkzone example.test") {
		t.Errorf("the zone was not validated:\n%s", joined)
	}
	if !strings.Contains(joined, "rndc reconfig") {
		t.Errorf("the server was not reconfigured:\n%s", joined)
	}
	if service.starts != 0 {
		t.Errorf("a running server was restarted %d times", service.starts)
	}
}

func TestReconcileLeavesAnUnchangedZoneAlone(t *testing.T) {
	// Rewriting an unchanged zone means a reload, a re-signing, and a serial
	// every secondary on the internet then pulls.
	provider, _, _ := recording(t, versionScript())
	ctx := context.Background()

	if _, err := provider.Reconcile(ctx, Desired{Zones: []Zone{exampleZone()}}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	path := provider.paths.ZoneFile("example.test")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	result, err := provider.Reconcile(ctx, Desired{Zones: []Zone{exampleZone()}})
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(result.Changed) != 0 {
		t.Errorf("changed = %v, want nothing", result.Changed)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("an unchanged zone file was rewritten")
	}
}

func TestReconcileRemovesAZoneThePanelNoLongerHas(t *testing.T) {
	provider, _, _ := recording(t, versionScript())
	ctx := context.Background()

	first := exampleZone()
	second := exampleZone()
	second.Name = "other.test"
	second.PrimaryNS = "ns1.other.test."
	second.Hostmaster = "hostmaster@other.test"
	second.Nameservers = []string{"ns1.other.test."}

	if _, err := provider.Reconcile(ctx, Desired{Zones: []Zone{first, second}}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	// A journal beside the file, as named leaves after signing. It has to go
	// with the zone: named replays a journal over whatever file it finds, so a
	// recreated zone would be served as the old contents.
	journal := provider.paths.ZoneFile("other.test") + ".jnl"
	if err := os.WriteFile(journal, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write journal: %v", err)
	}

	result, err := provider.Reconcile(ctx, Desired{Zones: []Zone{first}})
	if err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "other.test" {
		t.Fatalf("removed = %v, want other.test", result.Removed)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Error("the removed zone's journal was left behind")
	}
	if _, err := os.Stat(provider.paths.ZoneFile("other.test")); !os.IsNotExist(err) {
		t.Error("the removed zone's file was left behind")
	}
}

func TestReconcileRollsBackWhenNamedRefusesTheConfiguration(t *testing.T) {
	scripts := versionScript()
	// named-checkconf refuses everything, as it would for a zone that collides
	// with one defined elsewhere on the host.
	scripts[CommandNamedCheckconf] = "echo 'named.conf:9: zone example.test: already exists' >&2\nexit 1\n"

	provider, _, _ := recording(t, scripts)
	_, err := provider.Reconcile(context.Background(), Desired{Zones: []Zone{exampleZone()}})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("error = %v, want an invalid configuration", err)
	}

	// Nothing named would read at its next start may be left behind: a
	// rejected configuration on disk is a server that fails to come back after
	// a reboot, and takes every zone with it.
	if _, err := os.Stat(provider.paths.MainConf()); !os.IsNotExist(err) {
		t.Error("the rejected named.conf was left in place")
	}
	if _, err := os.Stat(provider.paths.Include()); !os.IsNotExist(err) {
		t.Error("the rejected zone list was left in place")
	}
	if _, err := os.Stat(provider.paths.ZoneFile("example.test")); !os.IsNotExist(err) {
		t.Error("the zone file was left in place")
	}
}

func TestReconcileRefusesAZoneNamedCheckzoneRejects(t *testing.T) {
	scripts := versionScript()
	scripts[CommandNamedCheckzone] = "echo 'zone example.test/IN: not loaded due to errors.' >&2\nexit 1\n"

	provider, _, _ := recording(t, scripts)
	_, err := provider.Reconcile(context.Background(), Desired{Zones: []Zone{exampleZone()}})
	if !errors.Is(err, ErrInvalidZone) {
		t.Fatalf("error = %v, want an invalid zone", err)
	}
	// Refused before installation, so there is nothing to roll back.
	if _, err := os.Stat(provider.paths.ZoneFile("example.test")); !os.IsNotExist(err) {
		t.Error("a zone the checker rejected was installed")
	}
	// And the candidate it checked is not left lying in the zone directory,
	// where the next reconcile would find it.
	entries, err := os.ReadDir(provider.paths.StateDir)
	if err != nil {
		t.Fatalf("read the zone directory: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".candidate-") {
			t.Errorf("a checked candidate was left behind: %s", entry.Name())
		}
	}
}

func TestReconcileRefusesSigningOnABINDThatCannotSign(t *testing.T) {
	scripts := map[string]string{
		CommandNamed: "if [ \"$1\" = \"-v\" ]; then echo 'BIND 9.11.36 (Extended Support Version)'; fi\n",
	}
	provider, _, _ := recording(t, scripts)

	zone := exampleZone()
	zone.DNSSEC = true
	_, err := provider.Reconcile(context.Background(), Desired{Zones: []Zone{zone}})
	if !errors.Is(err, ErrNoDNSSEC) {
		t.Fatalf("error = %v, want a server that cannot sign", err)
	}
}

func TestReconcileStartsAServerThatIsNotRunning(t *testing.T) {
	provider, service, calls := recording(t, versionScript())
	service.running = false

	result, err := provider.Reconcile(context.Background(), Desired{Zones: []Zone{exampleZone()}})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if service.starts != 1 {
		t.Errorf("the server was started %d times, want 1", service.starts)
	}
	if !result.Reloaded {
		t.Error("the server was started and the result says it was not")
	}
	// A server that has just started has read everything; telling it to
	// reconfigure would be asking a process that is still coming up.
	if strings.Contains(strings.Join(calls(), "\n"), "rndc reconfig") {
		t.Error("a freshly started server was also reconfigured")
	}
}

func TestSigningReadsTheRealRndcOutput(t *testing.T) {
	scripts := versionScript()
	scripts[CommandRndc] = "cat <<'EOF'\n" + dnssecStatusOutput + "EOF\n"
	scripts[CommandDNSSECFromKey] = "cat <<'EOF'\n" + dsFromKeyOutput + "EOF\n"

	provider, _, _ := recording(t, scripts)
	if err := provider.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	// A key file for the zone, which is what dsRecords looks for.
	key := filepath.Join(provider.paths.KeyDir(), "Kexample.test.+013+04079.key")
	if err := os.WriteFile(key, []byte("; key\n"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}

	status, err := provider.Signing(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("Signing: %v", err)
	}
	if status.Policy != "default" {
		t.Errorf("policy = %q, want default", status.Policy)
	}
	if len(status.Keys) != 1 {
		t.Fatalf("keys = %d, want 1", len(status.Keys))
	}
	key0 := status.Keys[0]
	if key0.ID != 4079 || key0.Role != "CSK" || key0.Algorithm != "ECDSAP256SHA256" {
		t.Errorf("key = %+v", key0)
	}
	if !key0.Published || !key0.KeySigning || !key0.ZoneSigning {
		t.Errorf("the key's three states were not all read: %+v", key0)
	}
	if key0.RolloverText != "No rollover scheduled" {
		t.Errorf("rollover = %q", key0.RolloverText)
	}

	if len(status.DS) != 1 {
		t.Fatalf("ds = %d, want 1", len(status.DS))
	}
	if status.DS[0].KeyTag != 4079 || status.DS[0].Algorithm != 13 || status.DS[0].DigestType != 2 {
		t.Errorf("ds = %+v", status.DS[0])
	}
}

func TestZoneStatusKeepsTheTwoSerialsApart(t *testing.T) {
	// The measured surprise: with inline signing the served serial diverges
	// from the file's within seconds. A panel that compared the served serial
	// with its own would report drift on every signed zone, forever.
	scripts := versionScript()
	scripts[CommandRndc] = "cat <<'EOF'\n" + zoneStatusOutput + "EOF\n"

	provider, _, _ := recording(t, scripts)
	state, err := provider.ZoneStatus(context.Background(), "example.test")
	if err != nil {
		t.Fatalf("ZoneStatus: %v", err)
	}
	if !state.Loaded {
		t.Fatal("the zone was reported as not loaded")
	}
	if state.Serial != 2026090302 {
		t.Errorf("serial = %d, want the file's 2026090302", state.Serial)
	}
	if state.SignedSerial != 2026090304 {
		t.Errorf("signed serial = %d, want the served 2026090304", state.SignedSerial)
	}
	if !state.Secure || state.Type != "primary" {
		t.Errorf("state = %+v", state)
	}
}

func TestZoneStatusReportsAnUnknownZoneRatherThanFailing(t *testing.T) {
	scripts := versionScript()
	scripts[CommandRndc] = "echo 'rndc: 'zonestatus' failed: not found' >&2\nexit 1\n"

	provider, _, _ := recording(t, scripts)
	state, err := provider.ZoneStatus(context.Background(), "missing.test")
	if err != nil {
		t.Fatalf("ZoneStatus: %v", err)
	}
	if state.Loaded {
		t.Error("a zone the server does not have was reported as loaded")
	}
	if state.Reason == "" {
		t.Error("no reason was given for a zone the server does not have")
	}
}

func TestStatusWarnsAboutTheThingsThatLookHealthy(t *testing.T) {
	// A recursive authoritative server, and one nobody outside can reach.
	// Neither produces an error anywhere.
	scripts := versionScript()
	scripts[CommandNamedCheckconf] = `if [ "$1" = "-p" ]; then
cat <<'EOF'
options {
	listen-on  {
		"127.0.0.1";
	};
	recursion yes;
};
EOF
fi
`
	provider, _, _ := recording(t, scripts)
	status := provider.Status(context.Background(), true)

	if !status.Available || status.Version != "9.18.49" {
		t.Fatalf("status = %+v", status)
	}
	joined := strings.Join(status.Warnings, "\n")
	if !strings.Contains(joined, "recursive") {
		t.Errorf("no warning about recursion:\n%s", joined)
	}
	if !strings.Contains(joined, "loopback") {
		t.Errorf("no warning about the listen address:\n%s", joined)
	}
}

func TestStatusReadsTheGlobalOptionsWithoutChangingThem(t *testing.T) {
	scripts := versionScript()
	scripts[CommandNamedCheckconf] = "if [ \"$1\" = \"-p\" ]; then cat <<'EOF'\n" +
		checkconfPrintOutput + "EOF\nfi\n"

	provider, _, _ := recording(t, scripts)
	status := provider.Status(context.Background(), false)

	if status.Recursion {
		t.Error("recursion was read as on")
	}
	if len(status.ListenOn) != 1 || status.ListenOn[0] != "any" {
		t.Errorf("listen-on = %v, want [any]", status.ListenOn)
	}
	if len(status.Warnings) != 0 {
		t.Errorf("a correct server produced warnings: %v", status.Warnings)
	}
}

func TestServedZonesReadsWhatTheServerWasTold(t *testing.T) {
	provider, _, _ := recording(t, versionScript())
	if _, err := provider.Reconcile(context.Background(), Desired{
		Zones: []Zone{exampleZone()},
	}); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	zones := provider.ServedZones()
	if len(zones) != 1 || zones[0] != "example.test" {
		t.Fatalf("zones = %v", zones)
	}
}

func TestUnavailableWithoutTheCheckers(t *testing.T) {
	// A host with named and no named-checkzone would let the panel write zone
	// files it cannot validate, and an unvalidated zone file is a server that
	// fails to start — taking every other zone on the host with it.
	dir := t.TempDir()
	stub := filepath.Join(dir, CommandNamed)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	runner, err := command.NewRunner(command.Spec{Name: CommandNamed, Path: stub})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	provider := NewProvider(Options{Runner: runner, Paths: Paths{ConfigDir: dir, StateDir: dir}})

	if provider.Available() {
		t.Fatal("a host with no checkers was reported as usable")
	}
	if _, err := provider.Reconcile(context.Background(), Desired{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestReconcileRewritesThePanelsOwnNamedConfWhenSettingsChange(t *testing.T) {
	// Written once and never again, listen-on would be a setting that silently
	// did nothing after the first zone.
	provider, _, _ := recording(t, versionScript())
	ctx := context.Background()

	if _, err := provider.Reconcile(ctx, Desired{Zones: []Zone{exampleZone()}}); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if _, err := provider.Reconcile(ctx, Desired{
		Settings: Settings{ListenOn: []string{"127.0.0.1", "203.0.113.10"}},
		Zones:    []Zone{exampleZone()},
	}); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}

	data, err := os.ReadFile(provider.paths.MainConf())
	if err != nil {
		t.Fatalf("read named.conf: %v", err)
	}
	if !strings.Contains(string(data), "listen-on { 127.0.0.1; 203.0.113.10; };") {
		t.Fatalf("the listen addresses were not applied:\n%s", data)
	}
}

func TestReconcileLeavesAForeignNamedConfAloneApartFromTheInclude(t *testing.T) {
	provider, _, _ := recording(t, versionScript())
	foreign := "options {\n\tlisten-on { 127.0.0.1; };\n\trecursion yes;\n};\n"
	if err := os.WriteFile(provider.paths.MainConf(), []byte(foreign), 0o640); err != nil {
		t.Fatalf("write named.conf: %v", err)
	}

	result, err := provider.Reconcile(context.Background(), Desired{
		Settings: Settings{ListenOn: []string{"203.0.113.10"}},
		Zones:    []Zone{exampleZone()},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !result.AddedInclude {
		t.Error("the include line was not added")
	}

	data, err := os.ReadFile(provider.paths.MainConf())
	if err != nil {
		t.Fatalf("read named.conf: %v", err)
	}
	// An operator who set listen-on to one address did so for a reason, and a
	// panel that silently widened it would open a name server to the internet
	// as a side effect of adding a record.
	if !strings.Contains(string(data), "listen-on { 127.0.0.1; };") {
		t.Errorf("somebody else's settings were rewritten:\n%s", data)
	}
	if strings.Contains(string(data), "203.0.113.10") {
		t.Errorf("the panel's listen address was forced into a foreign file:\n%s", data)
	}
}
