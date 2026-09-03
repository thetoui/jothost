package security

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/validate"
)

// The findings table, against a real database.
//
// The three properties worth a database to check are the ones that decide
// whether a security page is usable a month after it is switched on: that a
// rescan updates rather than duplicates, that a scanner which could not run
// resolves nothing, and that accepting a risk cannot quietly cover a worse one.

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "security-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func observation(overrides func(*Observation)) Observation {
	o := Observation{
		Scanner:     validate.ScannerFirewall,
		Severity:    validate.FindingHigh,
		Category:    "firewall",
		Title:       "The firewall is installed but not enabled",
		Description: "Rules are configured and none of them are in force.",
		Remediation: "Enable the firewall from the Firewall page.",
		Fingerprint: "firewall:disabled",
	}
	if overrides != nil {
		overrides(&o)
	}
	return o
}

func TestARescanUpdatesRatherThanDuplicating(t *testing.T) {
	// Without the fingerprint every scan inserts a fresh copy, and an operator
	// running a nightly scan accumulates three hundred rows describing one
	// setting.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	first, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	second, err := repo.Record(ctx, serverID, observation(nil), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("record again: %v", err)
	}

	if first.ID != second.ID {
		t.Fatal("a rescan produced a second row for the same finding")
	}
	// first_seen_at is kept, because "this host has been wrong about this since
	// March" is usually the sentence that gets something fixed.
	if !second.FirstSeenAt.Equal(first.FirstSeenAt) {
		t.Errorf("first_seen_at moved: %v then %v", first.FirstSeenAt, second.FirstSeenAt)
	}
	if !second.LastSeenAt.After(first.LastSeenAt) {
		t.Error("last_seen_at did not advance")
	}
}

func TestAScannerThatRanResolvesWhatItNoLongerFinds(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	if _, err := repo.Record(ctx, serverID, observation(nil), now); err != nil {
		t.Fatalf("record: %v", err)
	}

	// The next scan finds nothing from this scanner.
	resolved, err := repo.ResolveMissing(ctx, serverID, validate.ScannerFirewall,
		[]string{}, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ResolveMissing: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("resolved = %d, want 1", resolved)
	}

	counts, err := repo.Counts(ctx, serverID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Open != 0 {
		t.Errorf("open = %d, want 0", counts.Open)
	}
}

func TestResolvingOneScannerLeavesAnothersFindingsAlone(t *testing.T) {
	// This is the shape of the rule that matters most: an unreadable firewall
	// must never resolve "the firewall is disabled". The service enforces it by
	// not calling ResolveMissing at all for a scanner that could not run — and
	// this proves the scoping the call depends on.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	if _, err := repo.Record(ctx, serverID, observation(nil), now); err != nil {
		t.Fatalf("record firewall: %v", err)
	}
	ssh := observation(func(o *Observation) {
		o.Scanner = validate.ScannerSSH
		o.Category = "ssh"
		o.Title = "SSH permits password authentication"
		o.Fingerprint = "ssh:password-auth"
	})
	if _, err := repo.Record(ctx, serverID, ssh, now); err != nil {
		t.Fatalf("record ssh: %v", err)
	}

	// The SSH scanner runs and finds nothing; the firewall scanner did not run.
	if _, err := repo.ResolveMissing(ctx, serverID, validate.ScannerSSH,
		[]string{}, now.Add(time.Hour)); err != nil {
		t.Fatalf("ResolveMissing: %v", err)
	}

	findings, err := repo.List(ctx, ListParams{ServerID: serverID, Status: StatusOpen})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(findings) != 1 || findings[0].Scanner != validate.ScannerFirewall {
		t.Fatalf("open findings = %+v, want only the firewall one", findings)
	}
}

func TestAcceptingRequiresAReasonAtTheSchemaLevel(t *testing.T) {
	// A mute button on a security page is how a real problem becomes permanent,
	// so the rule is in the schema as well as in Go — a value cannot reach the
	// table through some future code path that forgets to validate.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	if _, err := repo.Accept(ctx, finding.ID, "", "", now); err == nil {
		t.Fatal("a finding was accepted with no reason")
	}
}

func TestAcceptingKeepsTheFindingAndTheReason(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	accepted, err := repo.Accept(ctx, finding.ID, "",
		"this host is behind a hardware firewall", now)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.Status != StatusAccepted {
		t.Errorf("status = %q, want accepted", accepted.Status)
	}
	if accepted.AcceptedReason == nil ||
		*accepted.AcceptedReason != "this host is behind a hardware firewall" {
		t.Errorf("reason = %v", accepted.AcceptedReason)
	}
	// The severity at the moment of acceptance, which is what lets a finding
	// that later gets worse be told apart from one that has not.
	if accepted.AcceptedSeverity == nil || *accepted.AcceptedSeverity != validate.FindingHigh {
		t.Errorf("accepted_severity = %v, want high", accepted.AcceptedSeverity)
	}
}

func TestAnAcceptedFindingThatGetsWorseIsReopened(t *testing.T) {
	// The rule that makes accepting safe to offer at all. Somebody accepting
	// "SSH listens on port 22" has not accepted "SSH permits root login with a
	// password", and a scan that kept the acceptance across a severity increase
	// would turn one considered decision into permanent blindness.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID,
		observation(func(o *Observation) { o.Severity = validate.FindingMedium }), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.Accept(ctx, finding.ID, "", "accepted while we migrate", now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	// The next scan finds the same thing, worse.
	worse, err := repo.Record(ctx, serverID,
		observation(func(o *Observation) { o.Severity = validate.FindingCritical }),
		now.Add(time.Hour))
	if err != nil {
		t.Fatalf("record worse: %v", err)
	}

	if worse.Status != StatusOpen {
		t.Fatalf("status = %q, want open: an accepted finding that got worse must come back",
			worse.Status)
	}
	if worse.AcceptedReason != nil {
		t.Error("the old acceptance was kept against a worse finding")
	}
}

func TestAnAcceptedFindingThatStaysTheSameStaysAccepted(t *testing.T) {
	// The other half of the rule. If every rescan re-opened accepted findings,
	// accepting would be useless and the page would nag forever about a
	// deliberate choice.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.Accept(ctx, finding.ID, "", "deliberate, reviewed quarterly", now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	again, err := repo.Record(ctx, serverID, observation(nil), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("record again: %v", err)
	}
	if again.Status != StatusAccepted {
		t.Fatalf("status = %q, want accepted", again.Status)
	}
}

func TestAnAcceptedFindingThatGetsBetterStaysAccepted(t *testing.T) {
	// Accepting a critical risk covers the medium version of it. Re-opening on
	// an *improvement* would be the panel arguing with a decision that has
	// become more conservative, not less.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID,
		observation(func(o *Observation) { o.Severity = validate.FindingCritical }), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.Accept(ctx, finding.ID, "", "known and scheduled for next week", now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	better, err := repo.Record(ctx, serverID,
		observation(func(o *Observation) { o.Severity = validate.FindingLow }),
		now.Add(time.Hour))
	if err != nil {
		t.Fatalf("record better: %v", err)
	}
	if better.Status != StatusAccepted {
		t.Errorf("status = %q, want accepted", better.Status)
	}
}

func TestAcceptedFindingsAreNotCountedAsOpen(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.Accept(ctx, finding.ID, "", "behind a hardware firewall", now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	counts, err := repo.Counts(ctx, serverID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Open != 0 {
		t.Errorf("open = %d, want 0", counts.Open)
	}
	if counts.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", counts.Accepted)
	}
	if counts.High != 0 {
		t.Errorf("high = %d, want 0: an accepted finding is not outstanding", counts.High)
	}
}

func TestSomethingFixedAndBrokenAgainIsTwoIncidents(t *testing.T) {
	// The unique index is partial on "not resolved" precisely so this works. A
	// setting put right in March and undone in June is two incidents, and
	// collapsing them would hide the second.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	first, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.ResolveMissing(ctx, serverID, validate.ScannerFirewall,
		[]string{}, now.Add(time.Hour)); err != nil {
		t.Fatalf("ResolveMissing: %v", err)
	}

	// It comes back.
	again, err := repo.Record(ctx, serverID, observation(nil), now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("record again: %v", err)
	}
	if again.Status != StatusOpen {
		t.Errorf("status = %q, want open", again.Status)
	}
	// A new row, not the old one reopened: this is a second incident, and its
	// first_seen_at is when it came back rather than when it first happened.
	if again.ID == first.ID {
		t.Fatal("the resolved finding was reused, so the two incidents are one row")
	}
	if !again.FirstSeenAt.After(first.FirstSeenAt) {
		t.Errorf("first_seen_at = %v, want it later than the first incident's %v",
			again.FirstSeenAt, first.FirstSeenAt)
	}

	all, err := repo.List(ctx, ListParams{ServerID: serverID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("rows = %d, want 2: the resolved one and the new one", len(all))
	}
}

func TestFindingsAreListedWorstFirst(t *testing.T) {
	// The page's job is to say what to fix first.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	severities := []string{
		validate.FindingLow, validate.FindingCritical,
		validate.FindingMedium, validate.FindingHigh,
	}
	for i, severity := range severities {
		o := observation(func(o *Observation) {
			o.Severity = severity
			o.Fingerprint = "test:" + severity
		})
		if _, err := repo.Record(ctx, serverID, o, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatalf("record %s: %v", severity, err)
		}
	}

	findings, err := repo.List(ctx, ListParams{ServerID: serverID, Status: StatusOpen})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{
		validate.FindingCritical, validate.FindingHigh,
		validate.FindingMedium, validate.FindingLow,
	}
	if len(findings) != len(want) {
		t.Fatalf("findings = %d, want %d", len(findings), len(want))
	}
	for i, severity := range want {
		if findings[i].Severity != severity {
			t.Errorf("position %d = %q, want %q", i, findings[i].Severity, severity)
		}
	}
}

func TestAHostThatHasNeverBeenScannedSaysSo(t *testing.T) {
	// A host with no findings and a host nobody has looked at are opposite
	// facts that a findings table alone renders identically, as no rows.
	repo, serverID, ctx := setup(t)

	if _, err := repo.LatestScan(ctx, serverID); !errors.Is(err, ErrNeverScanned) {
		t.Fatalf("LatestScan = %v, want ErrNeverScanned", err)
	}
}

func TestAScanRecordsHowManyChecksAnswered(t *testing.T) {
	repo, serverID, ctx := setup(t)

	stored, err := repo.RecordScan(ctx, Scan{
		ServerID: serverID, Score: 80, ChecksRun: 5, ChecksTotal: 7,
		High: 1, Scanners: []ScannerOutcome{
			{Scanner: validate.ScannerSSL, Ran: false, Reason: "certificates unreadable"},
		},
	})
	if err != nil {
		t.Fatalf("RecordScan: %v", err)
	}
	if stored.ChecksRun != 5 || stored.ChecksTotal != 7 {
		t.Errorf("checks = %d of %d, want 5 of 7", stored.ChecksRun, stored.ChecksTotal)
	}
	if len(stored.Scanners) != 1 || stored.Scanners[0].Ran {
		t.Errorf("scanner outcomes did not round-trip: %+v", stored.Scanners)
	}

	latest, err := repo.LatestScan(ctx, serverID)
	if err != nil {
		t.Fatalf("LatestScan: %v", err)
	}
	if latest.ID != stored.ID {
		t.Error("the latest scan is not the one just recorded")
	}
}

func TestASchemaLevelGuardOnSeverity(t *testing.T) {
	// The scale is a closed set in the schema too, so a value cannot reach the
	// table through some future code path that forgets to validate.
	repo, serverID, ctx := setup(t)

	_, err := repo.Record(ctx, serverID,
		observation(func(o *Observation) { o.Severity = "catastrophic" }), time.Now().UTC())
	if err == nil {
		t.Fatal("a severity outside the scale was accepted")
	}
}

func TestReopeningWithdrawsAnAcceptance(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	finding, err := repo.Record(ctx, serverID, observation(nil), now)
	if err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, err := repo.Accept(ctx, finding.ID, "", "temporarily accepted", now); err != nil {
		t.Fatalf("Accept: %v", err)
	}

	reopened, err := repo.Reopen(ctx, finding.ID)
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if reopened.Status != StatusOpen {
		t.Errorf("status = %q, want open", reopened.Status)
	}
	if reopened.AcceptedReason != nil || reopened.AcceptedAt != nil {
		t.Error("the acceptance was not cleared")
	}
}

func TestPruningLeavesLiveFindingsAlone(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	if _, err := repo.Record(ctx, serverID, observation(nil), now); err != nil {
		t.Fatalf("record: %v", err)
	}

	removed, err := repo.PruneResolved(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("PruneResolved: %v", err)
	}
	if removed != 0 {
		t.Errorf("pruning removed %d live finding(s)", removed)
	}

	counts, err := repo.Counts(ctx, serverID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.Open != 1 {
		t.Errorf("open = %d, want 1", counts.Open)
	}
}
