package monitoring

import (
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The alert engine's decisions, tested without a database or a host.
//
// The three-way outcome is what these are really about. A rule can be not
// breached, breached but not yet sustained, or sustained — and collapsing the
// middle case into either of the others is how a monitor becomes useless or
// unbearable. Almost every test here is about that middle case.

func rule(overrides func(*Rule)) Rule {
	r := Rule{
		ServerID:   "server-1",
		Name:       "Disk nearly full",
		Metric:     validate.MetricDisk,
		Comparison: validate.ComparisonAbove,
		Threshold:  80,
		ForSeconds: 300,
		Severity:   validate.SeverityWarning,
		Enabled:    true,
	}
	if overrides != nil {
		overrides(&r)
	}
	return r
}

func at(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse %q: %v", value, err)
	}
	return parsed
}

func TestABreachTooBriefToMatterOpensNothing(t *testing.T) {
	// This is where a blip dies. A backup job briefly filling a disk is not an
	// incident, and a rule that pages for it gets turned off within a week —
	// after which nothing is watching at all.
	now := at(t, "2026-09-03T09:00:00Z")
	reading := Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 91,
		Since: now.Add(-2 * time.Minute), Available: true,
	}

	decision := Evaluate(rule(nil), reading, now)
	if !decision.Breached {
		t.Fatal("a reading above the threshold was not breaching")
	}
	if decision.Sustained {
		t.Fatal("two minutes of a five-minute rule was treated as sustained")
	}
}

func TestASustainedBreachOpens(t *testing.T) {
	now := at(t, "2026-09-03T09:00:00Z")
	reading := Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 91,
		Since: now.Add(-10 * time.Minute), Available: true,
	}

	decision := Evaluate(rule(nil), reading, now)
	if !decision.Sustained {
		t.Fatal("ten minutes of a five-minute rule was not sustained")
	}
}

func TestAZeroDurationFiresOnTheFirstReading(t *testing.T) {
	// Right for a service being down, which is not a reading that fluctuates.
	now := at(t, "2026-09-03T09:00:00Z")
	down := rule(func(r *Rule) {
		r.Metric = validate.MetricService
		r.Target = "nginx"
		r.Comparison = validate.ComparisonBelow
		r.Threshold = 1
		r.ForSeconds = 0
	})

	decision := Evaluate(down, Reading{
		Metric: validate.MetricService, Target: "nginx", Value: 0, Available: true,
	}, now)
	if !decision.Sustained {
		t.Fatal("a zero-duration rule did not fire on the first reading")
	}
}

func TestAnUnknownDurationDoesNotFire(t *testing.T) {
	// The condition holds and nothing is known about how long. Erring towards
	// silence is deliberate: the other way round, a monitor fires on its first
	// reading after every restart.
	now := at(t, "2026-09-03T09:00:00Z")
	decision := Evaluate(rule(nil), Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 91, Available: true,
	}, now)

	if !decision.Breached {
		t.Fatal("the reading was not breaching")
	}
	if decision.Sustained {
		t.Fatal("an unknown duration was treated as sustained")
	}
}

func TestAnUnavailableReadingIsNeitherBreachNorRecovery(t *testing.T) {
	// A disk probe that timed out must not resolve a real disk alert, and must
	// not open one. Silence is its own answer.
	now := at(t, "2026-09-03T09:00:00Z")
	decision := Evaluate(rule(nil), Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 0, Available: false,
	}, now)

	if decision.Breached || decision.Sustained {
		t.Fatalf("an unavailable reading produced %+v", decision)
	}
}

func TestBelowComparison(t *testing.T) {
	now := at(t, "2026-09-03T09:00:00Z")
	below := rule(func(r *Rule) {
		r.Comparison = validate.ComparisonBelow
		r.Threshold = 10
		r.ForSeconds = 0
	})

	if !Evaluate(below, Reading{Metric: validate.MetricDisk, Value: 5, Available: true}, now).Sustained {
		t.Error("5 was not below 10")
	}
	if Evaluate(below, Reading{Metric: validate.MetricDisk, Value: 15, Available: true}, now).Breached {
		t.Error("15 was treated as below 10")
	}
}

func TestTheMessageNamesTheReadingTheThresholdAndHowLong(t *testing.T) {
	// The three things somebody woken by an alert needs before deciding whether
	// to get up.
	now := at(t, "2026-09-03T09:00:00Z")
	message := Message(rule(nil), Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 91.4,
		Since: now.Add(-90 * time.Minute), Available: true, Detail: "1.2 GiB free",
	}, now)

	for _, want := range []string{"/var", "91.4%", "above 80%", "1.2 GiB free", "1 hour"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q: %s", want, message)
		}
	}
}

func TestAServiceMessageSaysWhatIsDownAndForHowLong(t *testing.T) {
	now := at(t, "2026-09-03T09:00:00Z")
	down := rule(func(r *Rule) {
		r.Metric = validate.MetricService
		r.Target = "nginx"
		r.Name = "nginx down"
	})

	message := Message(down, Reading{
		Metric: validate.MetricService, Target: "nginx", Value: 0,
		Since: now.Add(-3 * time.Hour), Available: true,
	}, now)

	if !strings.Contains(message, "nginx is not running") {
		t.Errorf("message = %q", message)
	}
	if !strings.Contains(message, "3 hours") {
		t.Errorf("the message does not say how long: %q", message)
	}
}

func TestAShortBreachIsNotDescribedAsHavingLasted(t *testing.T) {
	// "for 0 minutes" reads as a bug. Under a minute, the duration is simply
	// not mentioned.
	now := at(t, "2026-09-03T09:00:00Z")
	message := Message(rule(nil), Reading{
		Metric: validate.MetricDisk, Target: "/var", Value: 91,
		Since: now.Add(-20 * time.Second), Available: true,
	}, now)

	if strings.Contains(message, " for ") {
		t.Errorf("a twenty-second breach was described as having lasted: %q", message)
	}
}

func TestDefaultRulesCoverTheDashboardsOwnThresholds(t *testing.T) {
	// Nothing changes meaning when a host is upgraded into this phase: the
	// numbers are the ones Phase 3 already used, now with a duration each —
	// which is the part Phase 3 had no way to express.
	rules := DefaultRules("server-1")
	if len(rules) == 0 {
		t.Fatal("a host would start with nothing being watched")
	}

	found := map[string]float64{}
	for _, r := range rules {
		if r.ForSeconds <= 0 {
			t.Errorf("the default rule %q fires on a single reading", r.Name)
		}
		if !r.Enabled {
			t.Errorf("the default rule %q is off, so nothing is watching", r.Name)
		}
		if err := validate.AlertMetric(r.Metric); err != nil {
			t.Errorf("the default rule %q watches something unmeasurable: %v", r.Name, err)
		}
		if err := validate.AlertThreshold(r.Metric, r.Threshold); err != nil {
			t.Errorf("the default rule %q has an unreachable threshold: %v", r.Name, err)
		}
		found[r.Metric+"."+r.Severity] = r.Threshold
	}

	for key, want := range map[string]float64{
		"disk.warning": 80, "disk.critical": 90,
		"memory.warning": 85, "memory.critical": 95,
		"load.warning": 1.5, "load.critical": 3,
	} {
		if found[key] != want {
			t.Errorf("%s = %v, want %v (Phase 3's number)", key, found[key], want)
		}
	}
}

func TestDefaultRulesDoNotCollideWithEachOther(t *testing.T) {
	// One rule per metric, target and severity — the unique index says so, and
	// a default set that violated it would fail on a fresh install.
	seen := map[string]bool{}
	for _, r := range DefaultRules("server-1") {
		key := r.Metric + "|" + r.Target + "|" + r.Severity
		if seen[key] {
			t.Fatalf("two default rules watch %s", key)
		}
		seen[key] = true
	}
}

func TestServiceStateDurationMeasuresToNowWhileOpen(t *testing.T) {
	now := at(t, "2026-09-03T09:00:00Z")
	started := now.Add(-2 * time.Hour)
	ended := now.Add(-time.Hour)

	open := ServiceState{StartedAt: started}
	if open.Duration(now) != 2*time.Hour {
		t.Errorf("an open state lasted %v, want 2h", open.Duration(now))
	}

	closed := ServiceState{StartedAt: started, EndedAt: &ended}
	if closed.Duration(now) != time.Hour {
		t.Errorf("a closed state lasted %v, want 1h", closed.Duration(now))
	}
}

func TestFormatDurationReadsLikeSomebodyWouldSayIt(t *testing.T) {
	cases := map[time.Duration]string{
		90 * time.Second:    "1 minutes",
		45 * time.Minute:    "45 minutes",
		time.Hour:           "1 hour",
		5 * time.Hour:       "5 hours",
		72 * time.Hour:      "3 days",
		30 * 24 * time.Hour: "30 days",
	}
	for duration, want := range cases {
		if got := formatDuration(duration); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", duration, got, want)
		}
	}
}
