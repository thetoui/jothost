package tenancy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func ptr(v int) *int { return &v }

// The slice unit is a file systemd parses as pid 1. The test asserts the
// directives it contains, not that a function was called: what matters is that
// "50%" reaches CPUQuota and "512M" reaches MemoryMax, because those are the
// spellings systemd understands and a bare number is not one of them.
func TestRenderSliceProducesSystemdDirectives(t *testing.T) {
	unit, err := RenderSlice("jothost-sub-abc.slice", "Acme hosting",
		Limits{CPUPercent: ptr(50), MemoryMB: ptr(512), IOWeight: ptr(100)})
	if err != nil {
		t.Fatalf("rendering a slice failed: %v", err)
	}

	for _, want := range []string{
		"[Slice]",
		"CPUQuota=50%",
		"MemoryMax=512M",
		"IOWeight=100",
		"Description=JotHost subscription Acme hosting",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit is missing %q:\n%s", want, unit)
		}
	}
}

// A dimension nobody capped must not appear at all. An empty CPUQuota= would
// be a parse error, and CPUQuota=0% would stop the customer's site.
func TestRenderSliceOmitsWhatWasNotCapped(t *testing.T) {
	unit, err := RenderSlice("jothost-sub-abc.slice", "Acme", Limits{MemoryMB: ptr(256)})
	if err != nil {
		t.Fatalf("rendering a slice failed: %v", err)
	}
	if strings.Contains(unit, "CPUQuota") {
		t.Errorf("a CPU cap nobody set must not appear:\n%s", unit)
	}
	if strings.Contains(unit, "IOWeight") {
		t.Errorf("an IO weight nobody set must not appear:\n%s", unit)
	}
	if !strings.Contains(unit, "MemoryMax=256M") {
		t.Errorf("the cap that was set is missing:\n%s", unit)
	}
}

// Every value in the unit is either an integer this package formatted or a
// name that has been validated. A description carrying a newline would close
// the Description directive and open one of the caller's choosing.
func TestRenderSliceRefusesInjection(t *testing.T) {
	if _, err := RenderSlice("jothost-sub-abc.slice",
		"Acme\nMemoryMax=1K", Limits{MemoryMB: ptr(256)}); err == nil {
		t.Error("a newline in the description must be refused")
	}
	if _, err := RenderSlice("../evil.slice", "Acme", Limits{MemoryMB: ptr(256)}); err == nil {
		t.Error("a slice name that is a path must be refused")
	}
	if _, err := RenderSlice("jothost.slice", "Acme", Limits{MemoryMB: ptr(1)}); err == nil {
		t.Error("a memory cap below the floor must be refused")
	}
}

// On a host with no systemd the unit is still written and the state says
// 'declared'. That distinction is the point of the operation: a panel that
// reported success here would show a green tick for a cap that caps nothing.
func TestApplyIsolationWithoutSystemdDeclaresRatherThanClaims(t *testing.T) {
	dir := t.TempDir()
	provider := NewProvider(Options{UnitDir: dir})

	result, err := provider.ApplyIsolation(context.Background(), "jothost-sub-abc.slice", "Acme",
		Limits{CPUPercent: ptr(25)})
	if err != nil {
		t.Fatalf("applying isolation failed: %v", err)
	}
	if result.State != StateDeclared {
		t.Errorf("a host with no systemd declares limits, it does not apply them; got %q",
			result.State)
	}
	if result.Placed {
		t.Error("nothing is placed in the slice, and the result must not say otherwise")
	}

	written, err := os.ReadFile(filepath.Join(dir, "jothost-sub-abc.slice"))
	if err != nil {
		t.Fatalf("the unit should still be written so a host that gains systemd finds it: %v", err)
	}
	if !strings.Contains(string(written), "CPUQuota=25%") {
		t.Errorf("the written unit does not carry the cap:\n%s", written)
	}
}

// Limits that cap nothing leave no slice behind. A cgroup that exists to limit
// nothing is one the next operator has to work out the purpose of.
func TestApplyIsolationWithNoLimitsRemovesTheSlice(t *testing.T) {
	dir := t.TempDir()
	provider := NewProvider(Options{UnitDir: dir})
	path := filepath.Join(dir, "jothost-sub-abc.slice")

	if _, err := provider.ApplyIsolation(context.Background(), "jothost-sub-abc.slice", "Acme",
		Limits{MemoryMB: ptr(256)}); err != nil {
		t.Fatalf("applying isolation failed: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the unit should exist: %v", err)
	}

	result, err := provider.ApplyIsolation(context.Background(), "jothost-sub-abc.slice", "Acme", Limits{})
	if err != nil {
		t.Fatalf("clearing isolation failed: %v", err)
	}
	if result.State != StateNone {
		t.Errorf("clearing every limit leaves no slice; got %q", result.State)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the unit should have been removed")
	}

	// And doing it again is not an error: this runs on every edit, including
	// the first, and a panel that failed the first edit of a subscription
	// because there was nothing to delete would be one nobody could configure.
	if _, err := provider.ApplyIsolation(context.Background(), "jothost-sub-abc.slice", "Acme",
		Limits{}); err != nil {
		t.Errorf("clearing limits twice must not fail: %v", err)
	}
}

// The bandwidth figure comes from a file a visitor writes into. The parse is
// pinned against the shapes an access log really contains — including the ones
// somebody sends on purpose.
func TestLogBytesReadsCombinedFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	lines := []string{
		`1.2.3.4 - - [09/Feb/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 396 "-" "curl/8.0"`,
		`1.2.3.4 - - [09/Feb/2026:10:00:01 +0000] "GET /a b HTTP/1.1" 200 100 "-" "Mozilla 5.0"`,
		// A HEAD response has no body; nginx writes a dash, which is not a
		// number and is not zero bytes of anything countable.
		`1.2.3.4 - - [09/Feb/2026:10:00:02 +0000] "HEAD / HTTP/1.1" 200 - "-" "curl"`,
		// A request line a visitor chose, containing what looks like a field.
		`1.2.3.4 - - [09/Feb/2026:10:00:03 +0000] "GET /?x=200 999999 HTTP/1.1" 200 7 "-" "-"`,
		`garbage that is not a log line at all`,
		``,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing the log failed: %v", err)
	}

	total, truncated, err := logBytes(path)
	if err != nil {
		t.Fatalf("reading the log failed: %v", err)
	}
	if truncated {
		t.Error("a short log is not truncated")
	}
	if want := int64(396 + 100 + 7); total != want {
		t.Errorf("the byte total is %d, want %d — the fourth line is the one that "+
			"matters: 999999 is inside the request a visitor sent", total, want)
	}
}

// A site with no visitors has no access log yet. Calling that a measurement
// failure would mark every new subscription as unmeasurable.
func TestLogBytesTreatsAMissingLogAsNoTraffic(t *testing.T) {
	total, truncated, err := logBytes(filepath.Join(t.TempDir(), "nothing.log"))
	if err != nil {
		t.Fatalf("a missing access log is not an error: %v", err)
	}
	if total != 0 || truncated {
		t.Errorf("a missing log means no traffic yet; got %d bytes, truncated=%v",
			total, truncated)
	}
}

// du prints "1234\t/path". Only the first field is read, and only as a number.
func TestParseDu(t *testing.T) {
	cases := map[string]struct {
		out  string
		want int64
		ok   bool
	}{
		"a tab":                {"1234\t/var/www/example.com\n", 1234, true},
		"spaces":               {"56   /var/www/x\n", 56, true},
		"a path with a tab":    {"7\t/var/www/odd\tname\n", 7, true},
		"nothing":              {"", 0, false},
		"not a number":         {"total\t/var/www\n", 0, false},
		"a negative":           {"-5\t/var/www\n", 0, false},
		"a warning line first": {"\n99\t/var/www\n", 99, true},
	}
	for what, tc := range cases {
		got, ok := parseDu(tc.out)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("%s: parseDu(%q) = %d, %v; want %d, %v",
				what, tc.out, got, ok, tc.want, tc.ok)
		}
	}
}

// A site that cannot be measured must not be reported as using nothing. The
// totals stay nil, and Partial says the rest are a floor.
func TestUsageDistinguishesNotMeasuredFromZero(t *testing.T) {
	provider := NewProvider(Options{})

	result := provider.Usage(context.Background(), []string{"/var/www/nowhere/public"})
	if result.DiskBytes != nil {
		t.Errorf("nothing could be measured, so the total must be nil, not %d",
			*result.DiskBytes)
	}
	if !result.Partial {
		t.Error("a measurement that could not read a site is partial and must say so")
	}
	if len(result.Sites) != 1 || result.Sites[0].Error == "" {
		t.Error("the site that failed should carry the reason")
	}
}
