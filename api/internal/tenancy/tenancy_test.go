package tenancy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

func ptr(v int) *int { return &v }

// Unlimited and zero are opposite promises, and the add-on arithmetic is where
// conflating them does the most damage. An unlimited plan that gained an
// add-on must not become a limited one.
func TestEffectiveLimitsUnlimitedAbsorbsAddons(t *testing.T) {
	plan := Limits{MaxWebsites: nil, MaxMailboxes: ptr(5), MaxDatabases: ptr(0)}
	addons := []Addon{{
		Quantity: 2,
		Limits:   Limits{MaxWebsites: ptr(10), MaxMailboxes: ptr(5), MaxDatabases: ptr(1)},
	}}

	got := EffectiveLimits(plan, addons)

	if got.MaxWebsites != nil {
		t.Errorf("an unlimited dimension stays unlimited; got %d", *got.MaxWebsites)
	}
	if got.MaxMailboxes == nil || *got.MaxMailboxes != 15 {
		t.Errorf("5 plus two lots of 5 is 15; got %v", got.MaxMailboxes)
	}
	// A plan including none of something, plus an add-on, is a plan that now
	// includes some. That is what buying an add-on means, and it only works
	// because zero is a number here rather than a spelling of "no limit".
	if got.MaxDatabases == nil || *got.MaxDatabases != 2 {
		t.Errorf("none plus two lots of one is two; got %v", got.MaxDatabases)
	}
}

// An add-on that says nothing about a dimension adds nothing to it. There is
// no meaning for an increment of infinity.
func TestEffectiveLimitsIgnoresSilentAddons(t *testing.T) {
	plan := Limits{MaxFTPUsers: ptr(3)}
	got := EffectiveLimits(plan, []Addon{{Quantity: 5, Limits: Limits{}}})
	if got.MaxFTPUsers == nil || *got.MaxFTPUsers != 3 {
		t.Errorf("an add-on that caps nothing adds nothing; got %v", got.MaxFTPUsers)
	}
}

// The guard refuses at wiring time to enforce a dimension it cannot enforce.
// Disk and bandwidth are measured on the host after the fact, so there is no
// request that could be refused to keep one inside its limit — and a route
// claiming otherwise would be a promise nothing keeps.
func TestNewGuardRefusesMeasuredDimensions(t *testing.T) {
	for _, dimension := range []string{validate.DimensionDisk, validate.DimensionBandwidth} {
		_, err := NewGuard(&Service{}, nil, map[string]Rule{
			"POST /api/v1/websites": {Dimension: dimension, Subject: SubjectCaller},
		})
		if err == nil {
			t.Errorf("the guard must refuse to enforce %s", dimension)
		}
	}

	if _, err := NewGuard(&Service{}, nil, map[string]Rule{
		"POST /api/v1/websites": {Dimension: validate.DimensionWebsites, Subject: "vibes"},
	}); err == nil {
		t.Error("the guard must refuse a subject it does not know how to resolve")
	}

	if _, err := NewGuard(&Service{}, nil, map[string]Rule{
		"POST /api/v1/websites": {
			Dimension: validate.DimensionWebsites, Subject: SubjectCaller,
		},
	}); err != nil {
		t.Errorf("an ordinary rule should be accepted: %v", err)
	}
}

// The guard matches routes with a ServeMux of its own, so its idea of which
// requests it covers is the router's idea — and, just as importantly, it
// resolves the same path values the router will.
//
// The second half is the regression this test exists for. Asking a mux which
// handler it *would* pick answers the pattern question and leaves PathValue
// empty, so a rule that reads {id} reads "". That fails silently: the lookup
// finds nothing, the guard charges the caller instead of the owner, and the
// limit quietly stops binding. It was found by watching a real mailbox
// creation return an internal error about an empty UUID.
func TestGuardResolvesTheSameRoutesAndPathValuesTheRouterWould(t *testing.T) {
	guard, err := NewGuard(&Service{}, nil, map[string]Rule{
		"POST /api/v1/websites": {
			Dimension: validate.DimensionWebsites, Subject: SubjectBodySubscription,
		},
		"POST /api/v1/websites/{id}/subdomains": {
			Dimension: validate.DimensionSubdomains, Subject: SubjectPathWebsite,
		},
		"POST /api/v1/mail/domains/{id}/mailboxes": {
			Dimension: validate.DimensionMailboxes, Subject: SubjectPathMailDomain,
		},
	})
	if err != nil {
		t.Fatalf("building the guard failed: %v", err)
	}

	cases := []struct {
		method, path string
		want         string
		wantID       string
	}{
		{"POST", "/api/v1/websites", validate.DimensionWebsites, ""},
		{"POST", "/api/v1/websites/site-7/subdomains", validate.DimensionSubdomains, "site-7"},
		{
			"POST", "/api/v1/mail/domains/domain-3/mailboxes",
			validate.DimensionMailboxes, "domain-3",
		},
		// Not guarded: reading is not creating, and neither is a route that
		// merely starts with a guarded one.
		{"GET", "/api/v1/websites", "", ""},
		{"POST", "/api/v1/websites/abc/domains", "", ""},
		{"POST", "/api/v1/mail/domains", "", ""},
		{"POST", "/api/v1/databases", "", ""},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, nil)
		rule, routed, ok := guard.resolve(r)
		got, gotID := "", ""
		if ok {
			got = rule.Dimension
			gotID = routed.PathValue("id")
		}
		if got != tc.want {
			t.Errorf("%s %s: guarded by %q, want %q", tc.method, tc.path, got, tc.want)
		}
		if gotID != tc.wantID {
			t.Errorf("%s %s: the {id} resolved to %q, want %q — an empty one means the "+
				"guard would charge the caller instead of the owner",
				tc.method, tc.path, gotID, tc.wantID)
		}
	}
}

// The guard reads an identifier out of the body, and the handler must still
// receive every byte of it. A guard that consumed the body would turn every
// guarded creation into a request with no fields in it.
func TestGuardLeavesTheBodyReadable(t *testing.T) {
	guard := &Guard{}
	payload := `{"domain":"example.com","subscription_id":"sub-1"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/websites", strings.NewReader(payload))

	fields := guard.readBody(r)
	if got := stringField(fields, "subscription_id"); got != "sub-1" {
		t.Errorf("the guard did not read the subscription id; got %q", got)
	}

	rest := make([]byte, len(payload))
	n, _ := r.Body.Read(rest)
	if string(rest[:n]) != payload {
		t.Errorf("the handler must still see the whole body; it got %q", rest[:n])
	}
}

// A body that is not JSON is not the guard's problem: the handler will reject
// it with an error that explains itself. The guard must not turn that into a
// quota failure.
func TestGuardIsLenientAboutBodiesItCannotParse(t *testing.T) {
	guard := &Guard{}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/websites",
		strings.NewReader("not json at all"))
	if fields := guard.readBody(r); fields != nil {
		t.Error("an unparseable body should yield nothing rather than an error")
	}

	body, _ := readAll(r)
	if body != "not json at all" {
		t.Errorf("the handler must still see the body; it got %q", body)
	}
}

// A slice name is derived from the subscription's id with the dashes removed,
// because systemd reads a dash as a level of nesting: "jothost-sub-a-b.slice"
// is a child of "jothost-sub-a.slice", which would put one customer's cap
// inside another's.
func TestSliceNameForIsFlat(t *testing.T) {
	name := SliceNameFor("2f86077f-6f85-42b4-82ef-7255f43fd495")
	if err := validate.SliceName(name); err != nil {
		t.Fatalf("the derived name must be a valid slice name: %v", err)
	}
	if strings.Count(strings.TrimSuffix(name, ".slice"), "-") != 2 {
		t.Errorf("the id's dashes must not become systemd nesting: %q", name)
	}
	if name != SliceNameFor("2f86077f-6f85-42b4-82ef-7255f43fd495") {
		t.Error("the name must be stable: a renamed unit leaves the old one behind")
	}
}

// readAll drains a request body for the assertions above.
func readAll(r *http.Request) (string, error) {
	var out strings.Builder
	buf := make([]byte, 256)
	for {
		n, err := r.Body.Read(buf)
		out.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return out.String(), nil
			}
			return out.String(), err
		}
	}
}
