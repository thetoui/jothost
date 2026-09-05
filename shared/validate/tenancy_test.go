package validate_test

import (
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

// The hierarchy's safety property is not "parents are checked". It is that a
// cycle cannot be built, and the reason is that every edge runs from a tier to
// a strictly lower one. Equal tiers are the case worth pinning: a reseller who
// could own a reseller could be owned by one, and then the two could own each
// other.
func TestTierMayOwnIsStrict(t *testing.T) {
	allowed := [][2]string{
		{validate.TierAdmin, validate.TierReseller},
		{validate.TierAdmin, validate.TierCustomer},
		{validate.TierReseller, validate.TierCustomer},
	}
	for _, pair := range allowed {
		if !validate.TierMayOwn(pair[0], pair[1]) {
			t.Errorf("a %s should be able to own a %s", pair[0], pair[1])
		}
	}

	refused := [][2]string{
		{validate.TierAdmin, validate.TierAdmin},
		{validate.TierReseller, validate.TierReseller},
		{validate.TierCustomer, validate.TierCustomer},
		{validate.TierReseller, validate.TierAdmin},
		{validate.TierCustomer, validate.TierReseller},
		{validate.TierCustomer, validate.TierAdmin},
		{"", validate.TierCustomer},
		{validate.TierAdmin, "superuser"},
	}
	for _, pair := range refused {
		if validate.TierMayOwn(pair[0], pair[1]) {
			t.Errorf("a %q must not be able to own a %q", pair[0], pair[1])
		}
	}
}

// A limit that is absent and a limit of zero are opposite promises. The
// validator must accept both, because a plan including no mailboxes is a real
// plan and so is one with no mailbox limit.
func TestQuotaLimitAcceptsUnlimitedAndZero(t *testing.T) {
	if err := validate.QuotaLimit(validate.DimensionMailboxes, nil); err != nil {
		t.Errorf("unlimited must be valid: %v", err)
	}
	zero := 0
	if err := validate.QuotaLimit(validate.DimensionMailboxes, &zero); err != nil {
		t.Errorf("a limit of none must be valid: %v", err)
	}
	negative := -1
	if err := validate.QuotaLimit(validate.DimensionMailboxes, &negative); err == nil {
		t.Error("a negative limit is a typo, not a smaller limit")
	}
	huge := validate.MaxLimitValue + 1
	if err := validate.QuotaLimit(validate.DimensionMailboxes, &huge); err == nil {
		t.Error("an implausible limit should be refused")
	}
	if err := validate.QuotaLimit("processes", nil); err == nil {
		t.Error("an unknown dimension should be refused")
	}
}

// A slice name becomes a filename under /etc/systemd/system and an argument to
// systemctl. The refusals are what stop it becoming a path or an option.
func TestSliceName(t *testing.T) {
	if err := validate.SliceName("jothost-sub-abc123.slice"); err != nil {
		t.Errorf("an ordinary slice name should be accepted: %v", err)
	}

	refused := map[string]string{
		"no suffix":        "jothost-sub-abc",
		"a path":           "../../etc/passwd.slice",
		"a slash":          "jothost/sub.slice",
		"a leading hyphen": "-jothost.slice",
		"an option":        "--now.slice",
		"empty stem":       ".slice",
		"upper case":       "JotHost.slice",
		"a newline":        "jothost\nDescription=x.slice",
		"a space":          "jothost sub.slice",
	}
	for what, name := range refused {
		if err := validate.SliceName(name); err == nil {
			t.Errorf("%s must be refused: %q", what, name)
		}
	}
}

// Isolation values that systemd would accept and values that would break a
// host. A memory cap below the floor kills everything that starts in the
// slice, which looks to a customer exactly like a broken server.
func TestIsolationBounds(t *testing.T) {
	ok := func(v int) *int { return &v }

	if err := validate.Isolation(ok(200), ok(512), ok(100)); err != nil {
		t.Errorf("more than one core is an ordinary setting: %v", err)
	}
	if err := validate.Isolation(nil, nil, nil); err != nil {
		t.Errorf("capping nothing is the default and must be valid: %v", err)
	}
	if err := validate.Isolation(ok(0), nil, nil); err == nil {
		t.Error("a CPU quota of zero would stop the site entirely")
	}
	if err := validate.Isolation(nil, ok(4), nil); err == nil {
		t.Error("a memory cap below the floor kills every process that starts")
	}
	if err := validate.Isolation(nil, nil, ok(0)); err == nil {
		t.Error("an IO weight outside systemd's range should be refused")
	}
	if err := validate.Isolation(nil, nil, ok(20000)); err == nil {
		t.Error("an IO weight above systemd's range should be refused")
	}
}

// A name reaches a page, a log line and a systemd unit's Description. A
// newline in the last of those closes a directive and opens another.
func TestPlanNameRefusesControlCharacters(t *testing.T) {
	if err := validate.PlanName("Starter"); err != nil {
		t.Errorf("an ordinary name should be accepted: %v", err)
	}
	if err := validate.PlanName(""); err == nil {
		t.Error("a plan needs a name")
	}
	if err := validate.PlanName("   "); err == nil {
		t.Error("whitespace is not a name")
	}
	if err := validate.PlanName("Starter\nMemoryMax=1K"); err == nil {
		t.Error("a newline in a name would be a directive in a unit file")
	}
	if err := validate.PlanName(strings.Repeat("a", validate.MaxPlanNameLength+1)); err == nil {
		t.Error("an over-long name should be refused")
	}
}
