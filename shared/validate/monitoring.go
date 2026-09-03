package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by monitoring validation.
var (
	// ErrInvalidMetric covers a reading this panel cannot take.
	ErrInvalidMetric = errors.New("unsupported metric")
	// ErrInvalidComparison covers an unknown comparison.
	ErrInvalidComparison = errors.New("invalid comparison")
	// ErrInvalidThreshold covers a number outside what the metric can produce.
	ErrInvalidThreshold = errors.New("invalid threshold")
	// ErrInvalidSeverity covers an unknown severity.
	ErrInvalidSeverity = errors.New("invalid severity")
	// ErrInvalidRuleName covers a name that says nothing.
	ErrInvalidRuleName = errors.New("invalid rule name")
	// ErrInvalidAlertDuration covers a sustained-breach window this panel will
	// not accept. It is distinct from fail2ban's ErrInvalidDuration because
	// they bound different things and their messages say different things.
	ErrInvalidAlertDuration = errors.New("invalid alert duration")
)

// Metrics an alert rule can watch.
//
// A closed set, and it is closed for a reason that is not tidiness: each of
// these is a reading the panel actually knows how to take. A rule naming a
// metric nothing produces would sit in the table looking like protection and
// never fire — which is worse than not having it, because somebody is relying
// on it.
const (
	MetricCPU       = "cpu"
	MetricMemory    = "memory"
	MetricDisk      = "disk"
	MetricLoad      = "load"
	MetricSwap      = "swap"
	MetricNetworkRx = "network_rx"
	MetricNetworkTx = "network_tx"
	MetricService   = "service"
)

// AlertMetrics is the supported set, in the order a UI should offer them.
var AlertMetrics = []string{
	MetricCPU, MetricMemory, MetricDisk, MetricSwap, MetricLoad,
	MetricNetworkRx, MetricNetworkTx, MetricService,
}

// Comparisons a rule can make.
//
// Two, and no more. Everything worth alerting on here is a resource crossing a
// line, and an equality test against a floating-point reading is a rule that
// never fires.
const (
	ComparisonAbove = "above"
	ComparisonBelow = "below"
)

// Alert severities.
const (
	SeverityWarning  = "warning"
	SeverityCritical = "critical"
)

// Bounds on a rule.
const (
	// MaxRuleNameLength bounds a rule's name.
	MaxRuleNameLength = 100
	// MaxAlertDuration is the longest sustained breach a rule may require. A
	// day is already far past the point where an alert would be useful.
	MaxAlertDuration = 86400
	// MaxTargetLength bounds a mount point or service name.
	MaxTargetLength = 255
)

// AlertMetric checks a metric name.
func AlertMetric(metric string) error {
	for _, supported := range AlertMetrics {
		if metric == supported {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidMetric, metric, strings.Join(AlertMetrics, ", "))
}

// IsPercentMetric reports whether a metric is measured as a percentage.
//
// It is what bounds the threshold: a rule saying "alert when memory is above
// 150%" is a rule that can never fire, and accepting it would be the panel
// letting somebody switch off an alert while believing they had set one.
func IsPercentMetric(metric string) bool {
	switch metric {
	case MetricCPU, MetricMemory, MetricDisk, MetricSwap:
		return true
	default:
		return false
	}
}

// AlertComparison checks a comparison.
func AlertComparison(comparison string) error {
	switch comparison {
	case ComparisonAbove, ComparisonBelow:
		return nil
	default:
		return fmt.Errorf("%w: a rule compares %q or %q, not %q",
			ErrInvalidComparison, ComparisonAbove, ComparisonBelow, comparison)
	}
}

// AlertSeverity checks a severity.
func AlertSeverity(severity string) error {
	switch severity {
	case SeverityWarning, SeverityCritical:
		return nil
	default:
		return fmt.Errorf("%w: an alert is %q or %q, not %q",
			ErrInvalidSeverity, SeverityWarning, SeverityCritical, severity)
	}
}

// AlertThreshold checks a threshold against what its metric can produce.
func AlertThreshold(metric string, threshold float64) error {
	if threshold < 0 {
		return fmt.Errorf("%w: a threshold cannot be negative", ErrInvalidThreshold)
	}
	if IsPercentMetric(metric) && threshold > 100 {
		return fmt.Errorf(
			"%w: %s is a percentage, so a threshold above 100 could never be reached",
			ErrInvalidThreshold, metric)
	}
	return nil
}

// AlertDuration checks how long a breach must last before it is an alert.
//
// Zero is allowed and is right for exactly one thing: a service being down,
// which is not a reading that fluctuates. For everything else it is the field
// that decides whether the panel is usable — a rule that fires on a single
// sample turns one backup job into a page at three in the morning.
func AlertDuration(seconds int) error {
	if seconds < 0 {
		return fmt.Errorf("%w: a duration cannot be negative", ErrInvalidAlertDuration)
	}
	if seconds > MaxAlertDuration {
		return fmt.Errorf("%w: must be at most %d seconds", ErrInvalidAlertDuration, MaxAlertDuration)
	}
	return nil
}

// AlertRuleName checks a rule's name.
func AlertRuleName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%w: a rule needs a name somebody will recognise in an alert",
			ErrInvalidRuleName)
	}
	if len(trimmed) > MaxRuleNameLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidRuleName, MaxRuleNameLength)
	}
	return nil
}

// AlertTarget checks which instance a rule watches.
//
// A mount point for disk, a service key for service, empty for "any". It is
// checked rather than passed through because it ends up in an alert message and
// in a unique index, not because it reaches a shell — nothing here does.
func AlertTarget(metric, target string) error {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		if metric == MetricService {
			return fmt.Errorf(
				"%w: a service rule must name the service it watches",
				ErrInvalidMetric)
		}
		return nil
	}
	if len(trimmed) > MaxTargetLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidMetric, MaxTargetLength)
	}
	for _, char := range trimmed {
		if char < 0x20 || char == 0x7f {
			return fmt.Errorf("%w: a target may not contain control characters",
				ErrInvalidMetric)
		}
	}
	return nil
}
