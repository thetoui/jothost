package monitoring

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/validate"
)

// The alert lifecycle and the service history, against a real database.
//
// The two properties worth a database to check are the ones an index enforces:
// one open alert per thing being watched, and one open state per service.
// Without either, a flapping host produces hundreds of rows about one problem.

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "monitoring-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func f64(v float64) *float64 { return &v }

// makeRule creates the rule an alert belongs to.
//
// Every alert the monitor opens has a rule, and since migration 0020 an alert's
// identity is that rule plus its target — so a test that opened an alert
// belonging to nothing would be testing a shape the panel never produces.
func makeRule(t *testing.T, repo *Repository, ctx context.Context,
	serverID, metric, target, severity string,
) Rule {
	t.Helper()

	rule, err := repo.CreateRule(ctx, Rule{
		ServerID: serverID, Name: "test " + metric + " " + target + " " + severity,
		Metric: metric, Target: target, Comparison: validate.ComparisonAbove,
		Threshold: 80, ForSeconds: 0, Severity: severity, Enabled: true,
	})
	if err != nil {
		t.Fatalf("create rule: %v", err)
	}
	return rule
}

func TestOpeningTheSameConditionTwiceRefreshesOneAlert(t *testing.T) {
	// Without this a flapping disk would open a new alert every evaluation and
	// somebody would wake to four hundred rows describing one filesystem.
	repo, serverID, ctx := setup(t)
	opened := time.Now().UTC().Truncate(time.Second)
	rule := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "/var",
		validate.SeverityWarning)

	first, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricDisk, Target: "/var",
		Severity: validate.SeverityWarning, Message: "Disk /var is 91% full",
		Value: f64(91), Threshold: f64(80), OpenedAt: opened,
	})
	if err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	second, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricDisk, Target: "/var",
		Severity: validate.SeverityWarning, Message: "Disk /var is 96% full",
		Value: f64(96), Threshold: f64(80), OpenedAt: opened.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("second OpenAlert: %v", err)
	}

	if second.ID != first.ID {
		t.Fatal("a second breach of the same condition opened a second alert")
	}
	// The opening time is the incident's start and does not move.
	if !second.OpenedAt.Equal(first.OpenedAt) {
		t.Errorf("the alert's start moved from %v to %v", first.OpenedAt, second.OpenedAt)
	}
	// The worst reading is kept: "it peaked at 96%" is worth more than "it was
	// at 96% a moment ago".
	if second.Worst == nil || *second.Worst < 96 {
		t.Errorf("worst = %v, want 96", second.Worst)
	}
	if second.Message != "Disk /var is 96% full" {
		t.Errorf("the message was not refreshed: %q", second.Message)
	}

	open, err := repo.ListAlerts(ctx, serverID, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("open alerts = %d, want 1", len(open))
	}
}

func TestTwoSeveritiesOfTheSameConditionAreTwoAlerts(t *testing.T) {
	// A disk that crosses the warning line and then the critical one is two
	// things somebody may want to be told about differently.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	for _, severity := range []string{validate.SeverityWarning, validate.SeverityCritical} {
		rule := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "/var", severity)
		if _, err := repo.OpenAlert(ctx, Alert{
			ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricDisk,
			Target: "/var", Severity: severity, Message: "Disk /var",
			Value: f64(95), OpenedAt: now,
		}); err != nil {
			t.Fatalf("OpenAlert(%s): %v", severity, err)
		}
	}

	open, err := repo.ListAlerts(ctx, serverID, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("open alerts = %d, want one per severity", len(open))
	}
}

func TestResolvingClosesTheAlertAndLetsANewOneOpenLater(t *testing.T) {
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()
	rule := makeRule(t, repo, ctx, serverID, validate.MetricMemory, "",
		validate.SeverityWarning)

	if _, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricMemory,
		Severity: validate.SeverityWarning, Message: "Memory high",
		Value: f64(91), OpenedAt: now,
	}); err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	resolved, found, err := repo.ResolveAlert(ctx, serverID, rule.ID, "",
		now.Add(time.Hour))
	if err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	}
	if !found {
		t.Fatal("an open alert was not found to resolve")
	}
	if resolved.Status != StatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("alert = %+v", resolved)
	}

	// Resolving again finds nothing, which is what an already-clear condition
	// looks like on every subsequent evaluation.
	if _, found, err := repo.ResolveAlert(ctx, serverID, rule.ID, "",
		now.Add(2*time.Hour)); err != nil {
		t.Fatalf("second ResolveAlert: %v", err)
	} else if found {
		t.Fatal("a resolved alert was resolved twice")
	}

	// And the condition returning opens a *new* incident rather than reviving
	// the old one, because they are different incidents.
	reopened, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricMemory,
		Severity: validate.SeverityWarning, Message: "Memory high again",
		Value: f64(92), OpenedAt: now.Add(3 * time.Hour),
	})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.ID == resolved.ID {
		t.Fatal("a resolved alert was reused for a later incident")
	}
}

func TestAcknowledgingDoesNotResolve(t *testing.T) {
	// A panel where a person can mark a full disk as fine is a panel that will
	// one day say a full disk is fine.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()
	rule := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "/var",
		validate.SeverityCritical)

	opened, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricDisk, Target: "/",
		Severity: validate.SeverityCritical, Message: "Disk / is 99% full",
		Value: f64(99), OpenedAt: now,
	})
	if err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	acknowledged, err := repo.AcknowledgeAlert(ctx, opened.ID, "", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("AcknowledgeAlert: %v", err)
	}
	if acknowledged.AcknowledgedAt == nil {
		t.Fatal("the acknowledgement was not recorded")
	}
	if acknowledged.Status != StatusOpen {
		t.Fatalf("acknowledging closed the alert: %s", acknowledged.Status)
	}
}

func TestARuleCannotBeDuplicated(t *testing.T) {
	// Two rules on the same metric, target and severity would both fire, and
	// the operator would get two alerts about one problem.
	repo, serverID, ctx := setup(t)

	rule := Rule{
		ServerID: serverID, Name: "Disk warning", Metric: validate.MetricDisk,
		Comparison: validate.ComparisonAbove, Threshold: 80, ForSeconds: 300,
		Severity: validate.SeverityWarning, Enabled: true,
	}
	if _, err := repo.CreateRule(ctx, rule); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	rule.Name = "Disk warning, again"
	rule.Threshold = 70
	if _, err := repo.CreateRule(ctx, rule); !errors.Is(err, ErrDuplicateRule) {
		t.Fatalf("error = %v, want a duplicate", err)
	}
}

func TestDeletingARuleKeepsTheAlertsItCaught(t *testing.T) {
	// An alert still has to describe itself after its rule is gone, which is
	// why it carries its own copy of what it was about.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	created, err := repo.CreateRule(ctx, Rule{
		ServerID: serverID, Name: "Memory", Metric: validate.MetricMemory,
		Comparison: validate.ComparisonAbove, Threshold: 90, ForSeconds: 60,
		Severity: validate.SeverityWarning, Enabled: true,
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	ruleID := created.ID
	if _, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &ruleID, Metric: validate.MetricMemory,
		Severity: validate.SeverityWarning, Message: "Memory is 95% used",
		Value: f64(95), Threshold: f64(90), OpenedAt: now,
	}); err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	if err := repo.DeleteRule(ctx, created.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	alerts, err := repo.ListAlerts(ctx, serverID, "", 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Fatalf("alerts = %d, want the history kept", len(alerts))
	}
	if alerts[0].Message != "Memory is 95% used" || alerts[0].Metric != validate.MetricMemory {
		t.Errorf("the alert no longer describes itself: %+v", alerts[0])
	}
	if alerts[0].RuleID != nil {
		t.Errorf("the alert still points at a deleted rule: %v", *alerts[0].RuleID)
	}
}

func TestServiceStateOnlyWritesARowWhenSomethingChanged(t *testing.T) {
	// A row per service per poll would be tens of thousands a day saying "still
	// running". The changes alone answer the question anybody asks.
	repo, serverID, ctx := setup(t)
	start := time.Now().UTC().Truncate(time.Second)

	for index := range 5 {
		_, changed, err := repo.RecordServiceState(ctx, serverID, "nginx", true, "running",
			start.Add(time.Duration(index)*time.Minute))
		if err != nil {
			t.Fatalf("RecordServiceState: %v", err)
		}
		if index == 0 && !changed {
			t.Fatal("the first reading did not open a state")
		}
		if index > 0 && changed {
			t.Fatalf("an unchanged reading at %d opened a new state", index)
		}
	}

	history, err := repo.ServiceHistory(ctx, serverID, "nginx", 10)
	if err != nil {
		t.Fatalf("ServiceHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("states = %d, want 1", len(history))
	}
}

func TestServiceStateClosesTheOldStretchWhenItChanges(t *testing.T) {
	repo, serverID, ctx := setup(t)
	start := time.Now().UTC().Truncate(time.Second)

	if _, _, err := repo.RecordServiceState(ctx, serverID, "nginx", true, "running", start); err != nil {
		t.Fatalf("first: %v", err)
	}
	down := start.Add(10 * time.Minute)
	if _, changed, err := repo.RecordServiceState(ctx, serverID, "nginx", false, "stopped", down); err != nil {
		t.Fatalf("second: %v", err)
	} else if !changed {
		t.Fatal("a state change was not recorded")
	}

	history, err := repo.ServiceHistory(ctx, serverID, "nginx", 10)
	if err != nil {
		t.Fatalf("ServiceHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("states = %d, want 2", len(history))
	}

	// Newest first: the current stretch is open, the previous one is closed at
	// the moment the new one began — which is what makes "how long was it down"
	// a subtraction rather than a search.
	if history[0].EndedAt != nil {
		t.Error("the current state is not open")
	}
	if history[1].EndedAt == nil || !history[1].EndedAt.UTC().Equal(down) {
		t.Errorf("the previous state was not closed at the change: %+v", history[1])
	}
	if history[1].Duration(down) != 10*time.Minute {
		t.Errorf("the previous state lasted %v, want 10m", history[1].Duration(down))
	}

	current, err := repo.CurrentServiceStates(ctx, serverID)
	if err != nil {
		t.Fatalf("CurrentServiceStates: %v", err)
	}
	if len(current) != 1 || current[0].Running {
		t.Fatalf("current = %+v", current)
	}
}

func TestDefaultsAreWrittenOnceAndNotPutBack(t *testing.T) {
	// A host whose operator deleted a rule they did not want must not have it
	// restored on the next restart — that is how a panel teaches people to
	// ignore it.
	repo, serverID, ctx := setup(t)

	written, err := repo.EnsureDefaults(ctx, serverID)
	if err != nil {
		t.Fatalf("EnsureDefaults: %v", err)
	}
	if written == 0 {
		t.Fatal("no default rules were written")
	}

	rules, err := repo.ListRules(ctx, serverID)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if err := repo.DeleteRule(ctx, rules[0].ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	again, err := repo.EnsureDefaults(ctx, serverID)
	if err != nil {
		t.Fatalf("second EnsureDefaults: %v", err)
	}
	if again != 0 {
		t.Fatalf("%d rules were written to a host that already had some", again)
	}

	after, err := repo.ListRules(ctx, serverID)
	if err != nil {
		t.Fatalf("ListRules: %v", err)
	}
	if len(after) != len(rules)-1 {
		t.Fatalf("rules = %d, want the deleted one to stay deleted", len(after))
	}
}

func TestPruningKeepsOpenAlertsAndCurrentStates(t *testing.T) {
	repo, serverID, ctx := setup(t)
	old := time.Now().UTC().Add(-200 * 24 * time.Hour)
	rule := makeRule(t, repo, ctx, serverID, validate.MetricCPU, "",
		validate.SeverityWarning)

	if _, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricCPU,
		Severity: validate.SeverityWarning, Message: "CPU", Value: f64(99), OpenedAt: old,
	}); err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}
	if _, _, err := repo.RecordServiceState(ctx, serverID, "nginx", true, "running", old); err != nil {
		t.Fatalf("RecordServiceState: %v", err)
	}

	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	if removed, err := repo.PruneAlerts(ctx, cutoff); err != nil {
		t.Fatalf("PruneAlerts: %v", err)
	} else if removed != 0 {
		t.Fatalf("an open alert was pruned")
	}
	if removed, err := repo.PruneServiceStates(ctx, cutoff); err != nil {
		t.Fatalf("PruneServiceStates: %v", err)
	} else if removed != 0 {
		t.Fatalf("the current service state was pruned")
	}
}

func TestTwoRulesWatchingOneTargetDoNotFightOverItsAlert(t *testing.T) {
	// The bug migration 0020 exists for, and it is worth a test of its own
	// because it was invisible until Phase 20 turned each turn of the fight
	// into an email.
	//
	// A general "any disk above 85%" and a specific "this one above 60%" may
	// both watch /var at the same severity — the rules index permits it,
	// because "" and "/var" are different targets. Under the older alert key
	// they shared one row: the rule that was not breaching resolved the alert
	// the other had just opened, and the next evaluation reversed it.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()

	general := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "",
		validate.SeverityCritical)
	specific := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "/var",
		validate.SeverityCritical)

	// The specific rule breaches and opens an alert about /var.
	opened, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &specific.ID, Metric: validate.MetricDisk,
		Target: "/var", Severity: validate.SeverityCritical,
		Message: "Disk /var is 70% full", Value: f64(70), OpenedAt: now,
	})
	if err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	// The general rule is not breaching, so the monitor resolves *its* alert
	// for the same target. It must not touch the other rule's.
	if _, found, err := repo.ResolveAlert(ctx, serverID, general.ID, "/var",
		now.Add(time.Minute)); err != nil {
		t.Fatalf("ResolveAlert: %v", err)
	} else if found {
		t.Fatal("a rule resolved an alert it did not raise")
	}

	still, err := repo.ListAlerts(ctx, serverID, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(still) != 1 || still[0].ID != opened.ID {
		t.Fatalf("open alerts = %+v, want the one the specific rule raised", still)
	}

	// And both rules may hold an alert about the same target at once, which is
	// what the new key permits and the old one could not represent.
	if _, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &general.ID, Metric: validate.MetricDisk,
		Target: "/var", Severity: validate.SeverityCritical,
		Message: "Disk /var is 90% full", Value: f64(90), OpenedAt: now,
	}); err != nil {
		t.Fatalf("the second rule could not open its own alert: %v", err)
	}

	both, err := repo.ListAlerts(ctx, serverID, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(both) != 2 {
		t.Fatalf("open alerts = %d, want one per rule", len(both))
	}
}

func TestDeletingARuleResolvesItsOpenAlerts(t *testing.T) {
	// Nothing else ever could: the monitor resolves an alert by evaluating the
	// rule that raised it, and that rule is gone. An alert left open here would
	// stay open forever about a condition nobody is watching.
	repo, serverID, ctx := setup(t)
	now := time.Now().UTC()
	rule := makeRule(t, repo, ctx, serverID, validate.MetricDisk, "/var",
		validate.SeverityCritical)

	if _, err := repo.OpenAlert(ctx, Alert{
		ServerID: serverID, RuleID: &rule.ID, Metric: validate.MetricDisk,
		Target: "/var", Severity: validate.SeverityCritical,
		Message: "Disk /var is 96% full", Value: f64(96), OpenedAt: now,
	}); err != nil {
		t.Fatalf("OpenAlert: %v", err)
	}

	if err := repo.DeleteRule(ctx, rule.ID); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}

	open, err := repo.ListAlerts(ctx, serverID, StatusOpen, 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("open alerts = %d, want none after the rule was deleted", len(open))
	}

	// The alert itself survives: deleting a rule must not erase the record of
	// what it caught.
	all, err := repo.ListAlerts(ctx, serverID, "", 10)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("alerts = %d, want the resolved one kept", len(all))
	}
}
