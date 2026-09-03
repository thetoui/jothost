package monitoring

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Reading is one value a rule can be judged against.
//
// It carries how long the condition has already held, because that is what
// separates an incident from a blip and it cannot be answered from a single
// number.
type Reading struct {
	// Metric and Target say what was read.
	Metric string
	Target string
	// Value is the reading itself. For a service it is 1 when running and 0
	// when not, so one comparison covers everything.
	Value float64
	// Since is when the current condition began — the earliest sample in the
	// run of consecutive readings that all breach, or the start of the current
	// service state. Zero means "only this reading is known", which fires only
	// rules with no duration.
	Since time.Time
	// Available reports whether the reading could be taken at all. An
	// unavailable metric is not a breach and is not a recovery: it is silence,
	// and the difference matters — see Evaluate.
	Available bool
	// Detail is added to the alert's message, such as a filesystem's size.
	Detail string
}

// Decision is what a rule concluded about a reading.
type Decision struct {
	Rule    Rule
	Reading Reading
	// Breached reports whether the condition holds right now.
	Breached bool
	// Sustained reports whether it has held for the rule's whole duration,
	// which is what actually opens an alert.
	Sustained bool
}

// Evaluate judges one reading against one rule.
//
// The three-way outcome matters. A rule can be *not breached* (resolve any open
// alert), *breached but not yet sustained* (do nothing — this is where a blip
// dies), or *sustained* (open). Collapsing the middle case into either of the
// others is how a monitor becomes either useless or unbearable.
func Evaluate(rule Rule, reading Reading, now time.Time) Decision {
	decision := Decision{Rule: rule, Reading: reading}

	// An unavailable reading says nothing. It is deliberately neither a breach
	// nor a recovery: a disk probe that timed out must not resolve a real disk
	// alert, and must not open one either.
	if !reading.Available {
		return decision
	}

	switch rule.Comparison {
	case validate.ComparisonBelow:
		decision.Breached = reading.Value < rule.Threshold
	default:
		decision.Breached = reading.Value > rule.Threshold
	}
	if !decision.Breached {
		return decision
	}

	if rule.ForSeconds <= 0 {
		decision.Sustained = true
		return decision
	}
	if reading.Since.IsZero() {
		// The condition holds, and nothing is known about how long it has held.
		// Treated as not yet sustained, which errs towards silence — a monitor
		// that guesses in the other direction is one that fires on its first
		// reading after every restart.
		return decision
	}

	decision.Sustained = now.Sub(reading.Since) >= time.Duration(rule.ForSeconds)*time.Second
	return decision
}

// Message renders what an alert says.
//
// It names the reading, the threshold it crossed and how long it has held,
// because those three are what somebody woken by it needs before they decide
// whether to get up.
func Message(rule Rule, reading Reading, now time.Time) string {
	if rule.Metric == validate.MetricService {
		return fmt.Sprintf("%s is not running%s", reading.Target, heldFor(reading, now))
	}

	subject := rule.Name
	if reading.Target != "" {
		subject = fmt.Sprintf("%s (%s)", rule.Name, reading.Target)
	}

	comparison := "above"
	if rule.Comparison == validate.ComparisonBelow {
		comparison = "below"
	}

	unit := ""
	if validate.IsPercentMetric(rule.Metric) {
		unit = "%"
	}

	message := fmt.Sprintf("%s: %s%s is %s %s%s",
		subject, formatValue(reading.Value), unit, comparison,
		formatValue(rule.Threshold), unit)
	if reading.Detail != "" {
		message += " (" + reading.Detail + ")"
	}
	return message + heldFor(reading, now)
}

// heldFor renders how long a condition has held, or nothing when unknown.
func heldFor(reading Reading, now time.Time) string {
	if reading.Since.IsZero() {
		return ""
	}
	held := now.Sub(reading.Since)
	if held < time.Minute {
		return ""
	}
	return " for " + formatDuration(held)
}

// formatDuration renders a span the way somebody would say it.
func formatDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		hours := int(d.Hours())
		if hours == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", hours)
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}

// formatValue renders a reading without trailing noise.
func formatValue(value float64) string {
	if value == math.Trunc(value) {
		return fmt.Sprintf("%.0f", value)
	}
	return fmt.Sprintf("%.1f", value)
}

// DefaultRules are what a host starts with.
//
// They exist because a monitoring page with no rules monitors nothing, and an
// operator who has to invent thresholds before the panel does anything will
// mostly not bother. The numbers are the ones Phase 3's dashboard already used,
// so nothing changes meaning when a host is upgraded into this phase — and each
// carries a duration, which is the part Phase 3 had no way to express.
func DefaultRules(serverID string) []Rule {
	return []Rule{
		{
			ServerID: serverID, Name: "Disk nearly full", Metric: validate.MetricDisk,
			Comparison: validate.ComparisonAbove, Threshold: 80, ForSeconds: 300,
			Severity: validate.SeverityWarning, Enabled: true,
		},
		{
			ServerID: serverID, Name: "Disk critically full", Metric: validate.MetricDisk,
			Comparison: validate.ComparisonAbove, Threshold: 90, ForSeconds: 300,
			Severity: validate.SeverityCritical, Enabled: true,
		},
		{
			ServerID: serverID, Name: "Memory high", Metric: validate.MetricMemory,
			Comparison: validate.ComparisonAbove, Threshold: 85, ForSeconds: 600,
			Severity: validate.SeverityWarning, Enabled: true,
		},
		{
			ServerID: serverID, Name: "Memory critical", Metric: validate.MetricMemory,
			Comparison: validate.ComparisonAbove, Threshold: 95, ForSeconds: 600,
			Severity: validate.SeverityCritical, Enabled: true,
		},
		{
			// Swap in use on a server usually means memory pressure rather than
			// a healthy overcommit, so it is worth its own rule.
			ServerID: serverID, Name: "Swap in use", Metric: validate.MetricSwap,
			Comparison: validate.ComparisonAbove, Threshold: 50, ForSeconds: 900,
			Severity: validate.SeverityWarning, Enabled: true,
		},
		{
			// Per core, so the same number means the same thing on a 2-core and
			// a 64-core machine.
			ServerID: serverID, Name: "Load high", Metric: validate.MetricLoad,
			Comparison: validate.ComparisonAbove, Threshold: 1.5, ForSeconds: 600,
			Severity: validate.SeverityWarning, Enabled: true,
		},
		{
			ServerID: serverID, Name: "Load critical", Metric: validate.MetricLoad,
			Comparison: validate.ComparisonAbove, Threshold: 3, ForSeconds: 600,
			Severity: validate.SeverityCritical, Enabled: true,
		},
		{
			// CPU is deliberately generous and slow. A server at 90% CPU for
			// ten minutes is a server doing its job; the alert is for the one
			// that never comes back down.
			ServerID: serverID, Name: "CPU saturated", Metric: validate.MetricCPU,
			Comparison: validate.ComparisonAbove, Threshold: 90, ForSeconds: 1800,
			Severity: validate.SeverityWarning, Enabled: true,
		},
	}
}

// EnsureDefaults writes the default rules for a host that has none.
//
// Only when it has none. A host whose operator has deleted a rule they did not
// want must not have it put back on the next restart — that is how a panel
// teaches people to ignore it.
func (r *Repository) EnsureDefaults(ctx context.Context, serverID string) (int, error) {
	existing, err := r.ListRules(ctx, serverID)
	if err != nil {
		return 0, err
	}
	if len(existing) > 0 {
		return 0, nil
	}

	written := 0
	for _, rule := range DefaultRules(serverID) {
		if _, err := r.CreateRule(ctx, rule); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}
