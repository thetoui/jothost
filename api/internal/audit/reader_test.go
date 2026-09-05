package audit_test

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/logger"
)

// newReader builds the pair every test here needs: something to write the
// trail with, and the reader under test.
func newReader(t *testing.T, deps *testsupport.Deps) (*audit.Recorder, *audit.Reader) {
	t.Helper()
	log := logger.New(logger.Options{Service: "api", Level: "error", Output: io.Discard})
	return audit.NewRecorder(deps.Pool, log), audit.NewReader(deps.Pool)
}

// seed writes one event and returns the recorder's error, so a test can be
// explicit that recording succeeded before reading anything back.
func seed(t *testing.T, recorder *audit.Recorder, event audit.Event) {
	t.Helper()
	if err := recorder.Record(context.Background(), event); err != nil {
		t.Fatalf("record %s: %v", event.Action, err)
	}
}

func newUser(t *testing.T, deps *testsupport.Deps, username string) string {
	t.Helper()
	var id string
	err := deps.Pool.QueryRow(context.Background(),
		`INSERT INTO users (username, email, password_hash, status)
		 VALUES ($1, $2, 'x', 'active') RETURNING id`,
		username, username+"@example.test").Scan(&id)
	if err != nil {
		t.Fatalf("create user %s: %v", username, err)
	}
	return id
}

// TestReaderReturnsTheTrailNewestFirst is the check the panel never had: the
// audit trail has been written since the first phase and, until now, could not
// be read back through anything but a database client.
func TestReaderReturnsTheTrailNewestFirst(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	actor := newUser(t, deps, "auditor")
	for _, action := range []string{"website.create", "website.delete", "ssl.issue"} {
		seed(t, recorder, audit.Event{
			UserID: actor, Action: action, Status: audit.StatusSuccess,
		})
	}

	entries, total, err := reader.List(context.Background(), audit.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	// Newest first, because that is the order every question about a trail is
	// asked in.
	for i := 1; i < len(entries); i++ {
		if entries[i].CreatedAt.After(entries[i-1].CreatedAt) {
			t.Fatalf("entries are not newest first: %v then %v",
				entries[i-1].CreatedAt, entries[i].CreatedAt)
		}
	}
}

// TestReaderNamesTheActor is why the query joins users at all. A trail that
// answers "who" with a UUID has not answered it.
func TestReaderNamesTheActor(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	actor := newUser(t, deps, "namedactor")
	seed(t, recorder, audit.Event{
		UserID: actor, Action: "website.delete", Status: audit.StatusSuccess,
	})

	entries, _, err := reader.List(context.Background(), audit.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Username == nil || *entries[0].Username != "namedactor" {
		t.Fatalf("username = %v, want namedactor", entries[0].Username)
	}
}

// TestReaderKeepsEventsWithNoActor guards the case the schema was written for:
// a failed login against a username that does not exist has nobody to name,
// and it is exactly the event somebody reading a trail wants to see.
func TestReaderKeepsEventsWithNoActor(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	seed(t, recorder, audit.Event{
		Action: audit.ActionLoginFailed, Status: audit.StatusFailure,
		IPAddress: "203.0.113.7",
	})

	entries, total, err := reader.List(context.Background(), audit.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(entries) != 1 {
		t.Fatalf("an event with no actor was dropped: total %d, %d entries", total, len(entries))
	}
	if entries[0].UserID != nil || entries[0].Username != nil {
		t.Fatalf("expected no actor, got %v / %v", entries[0].UserID, entries[0].Username)
	}
	if entries[0].IPAddress == nil || *entries[0].IPAddress != "203.0.113.7" {
		t.Fatalf("address = %v, want 203.0.113.7", entries[0].IPAddress)
	}
}

// TestReaderSurvivesADeletedActor is the property migration 0001 chose
// ON DELETE SET NULL for: removing an account must not erase what it did.
func TestReaderSurvivesADeletedActor(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	actor := newUser(t, deps, "departing")
	seed(t, recorder, audit.Event{
		UserID: actor, Action: "website.delete", Status: audit.StatusSuccess,
	})

	if _, err := deps.Pool.Exec(context.Background(),
		`DELETE FROM users WHERE id = $1`, actor); err != nil {
		t.Fatalf("delete the actor: %v", err)
	}

	entries, total, err := reader.List(context.Background(), audit.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Fatalf("deleting the actor erased the record: total = %d", total)
	}
	if entries[0].Action != "website.delete" {
		t.Fatalf("action = %q, want website.delete", entries[0].Action)
	}
	// The row survives; the name cannot. Saying so is more honest than
	// inventing one.
	if entries[0].Username != nil {
		t.Fatalf("username = %v, want none after the account was deleted", entries[0].Username)
	}
}

func TestReaderFilters(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	alice := newUser(t, deps, "alice")
	bob := newUser(t, deps, "bob")
	website := "11111111-2222-3333-4444-555555555555"

	seed(t, recorder, audit.Event{UserID: alice, Action: "website.create",
		ResourceType: "website", ResourceID: website, Status: audit.StatusSuccess})
	seed(t, recorder, audit.Event{UserID: alice, Action: "website.delete",
		ResourceType: "website", ResourceID: website, Status: audit.StatusSuccess})
	seed(t, recorder, audit.Event{UserID: bob, Action: "firewall.change",
		Status: audit.StatusFailure})

	cases := []struct {
		name   string
		params audit.ListParams
		want   int
	}{
		{"by action", audit.ListParams{Action: "website.create"}, 1},
		{"by actor", audit.ListParams{UserID: alice}, 2},
		{"by outcome", audit.ListParams{Status: audit.StatusFailure}, 1},
		{"by resource", audit.ListParams{ResourceType: "website", ResourceID: website}, 2},
		{"by actor and action together", audit.ListParams{
			UserID: alice, Action: "website.delete"}, 1},
		{"a filter matching nothing", audit.ListParams{Action: "never.happened"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entries, total, err := reader.List(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if total != tc.want || len(entries) != tc.want {
				t.Fatalf("got %d entries (total %d), want %d", len(entries), total, tc.want)
			}
		})
	}
}

func TestReaderFiltersByTime(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	seed(t, recorder, audit.Event{Action: "website.create", Status: audit.StatusSuccess})

	future := time.Now().Add(time.Hour)
	past := time.Now().Add(-time.Hour)

	_, total, err := reader.List(context.Background(), audit.ListParams{Since: &future})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 0 {
		t.Fatalf("an event was returned from before the window: %d", total)
	}

	_, total, err = reader.List(context.Background(), audit.ListParams{Since: &past})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 {
		t.Fatalf("the event was excluded from a window containing it: %d", total)
	}
}

// TestReaderPagesWithoutLosingTheTotal is the reason List returns a count at
// all: a page of 2 out of 5 must not look like a history of 2.
func TestReaderPagesWithoutLosingTheTotal(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	for i := 0; i < 5; i++ {
		seed(t, recorder, audit.Event{Action: "website.create", Status: audit.StatusSuccess})
	}

	first, total, err := reader.List(context.Background(), audit.ListParams{Limit: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5 — paging must not shrink the count", total)
	}
	if len(first) != 2 {
		t.Fatalf("page held %d, want 2", len(first))
	}

	second, _, err := reader.List(context.Background(), audit.ListParams{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(second) != 2 {
		t.Fatalf("second page held %d, want 2", len(second))
	}
	// Pages must not overlap. Ordering by created_at alone would let two rows
	// written in the same millisecond appear on both, which is why the query
	// breaks the tie on id.
	for _, a := range first {
		for _, b := range second {
			if a.ID == b.ID {
				t.Fatalf("entry %s appeared on both pages", a.ID)
			}
		}
	}
}

// TestReaderCapsTheLimit stops a caller asking the panel to load its own
// history into memory. This table grows for the life of the installation.
func TestReaderCapsTheLimit(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	for i := 0; i < 3; i++ {
		seed(t, recorder, audit.Event{Action: "website.create", Status: audit.StatusSuccess})
	}

	entries, _, err := reader.List(context.Background(),
		audit.ListParams{Limit: audit.MaxLimit + 5000})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
}

// TestReaderReturnsMetadata proves the context an event carries survives the
// round trip. Without it "website.delete" cannot say which website.
func TestReaderReturnsMetadata(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	seed(t, recorder, audit.Event{
		Action: "website.delete", Status: audit.StatusSuccess,
		Metadata: map[string]any{"domain": "example.test"},
	})

	entries, _, err := reader.List(context.Background(), audit.ListParams{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Metadata["domain"] != "example.test" {
		t.Fatalf("metadata = %v, want the domain", entries[0].Metadata)
	}
}

// TestActionsAreReadFromTheTrail keeps the filter honest. The action names are
// not a list in this package — every feature appends its own — so offering a
// filter built from constants would offer names nothing ever recorded.
func TestActionsAreReadFromTheTrail(t *testing.T) {
	deps := testsupport.Require(t)
	recorder, reader := newReader(t, deps)

	seed(t, recorder, audit.Event{Action: "website.create", Status: audit.StatusSuccess})
	seed(t, recorder, audit.Event{Action: "website.create", Status: audit.StatusSuccess})
	seed(t, recorder, audit.Event{Action: "backup.restore", Status: audit.StatusSuccess})

	actions, err := reader.Actions(context.Background())
	if err != nil {
		t.Fatalf("Actions: %v", err)
	}
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want two distinct names", actions)
	}
	if actions[0] != "backup.restore" || actions[1] != "website.create" {
		t.Fatalf("actions = %v, want them sorted", actions)
	}
}
