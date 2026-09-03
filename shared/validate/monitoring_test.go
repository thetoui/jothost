package validate

import "testing"

func TestAlertMetricIsAClosedSet(t *testing.T) {
	// A rule naming a metric nothing produces would sit in the table looking
	// like protection and never fire, which is worse than not having it.
	for _, metric := range AlertMetrics {
		if err := AlertMetric(metric); err != nil {
			t.Errorf("AlertMetric(%q) refused: %v", metric, err)
		}
	}
	for _, bad := range []string{"", "CPU", "temperature", "disk_io"} {
		if err := AlertMetric(bad); err == nil {
			t.Errorf("AlertMetric(%q) was accepted", bad)
		}
	}
}

func TestAThresholdAbove100OnAPercentageIsRefused(t *testing.T) {
	// It could never be reached, so accepting it would let somebody switch an
	// alert off while believing they had set one.
	for _, metric := range []string{MetricCPU, MetricMemory, MetricDisk, MetricSwap} {
		if err := AlertThreshold(metric, 150); err == nil {
			t.Errorf("a 150%% threshold on %s was accepted", metric)
		}
		if err := AlertThreshold(metric, 90); err != nil {
			t.Errorf("a 90%% threshold on %s was refused: %v", metric, err)
		}
	}

	// Load and network rates are not percentages and have no such ceiling.
	if err := AlertThreshold(MetricLoad, 8); err != nil {
		t.Errorf("a load threshold of 8 was refused: %v", err)
	}
	if err := AlertThreshold(MetricNetworkRx, 12_000_000); err != nil {
		t.Errorf("a network threshold was refused: %v", err)
	}
	if err := AlertThreshold(MetricLoad, -1); err == nil {
		t.Error("a negative threshold was accepted")
	}
}

func TestAlertDurationAllowsZeroAndBoundsTheRest(t *testing.T) {
	// Zero is right for a service being down, which is not a reading that
	// fluctuates; it is wrong for almost everything else, which is a matter for
	// the defaults rather than for this check.
	if err := AlertDuration(0); err != nil {
		t.Fatalf("a zero duration was refused: %v", err)
	}
	if err := AlertDuration(300); err != nil {
		t.Fatalf("five minutes was refused: %v", err)
	}
	if err := AlertDuration(-1); err == nil {
		t.Error("a negative duration was accepted")
	}
	if err := AlertDuration(MaxAlertDuration + 1); err == nil {
		t.Error("a duration longer than a day was accepted")
	}
}

func TestAServiceRuleMustNameItsService(t *testing.T) {
	// "Alert when a service is down" without saying which is a rule the
	// evaluator cannot act on.
	if err := AlertTarget(MetricService, ""); err == nil {
		t.Fatal("a service rule with no service was accepted")
	}
	if err := AlertTarget(MetricService, "nginx"); err != nil {
		t.Fatalf("a named service was refused: %v", err)
	}
	// A disk rule with no target watches every filesystem, which is what
	// somebody setting a general threshold means.
	if err := AlertTarget(MetricDisk, ""); err != nil {
		t.Fatalf("a disk rule with no mount point was refused: %v", err)
	}
	if err := AlertTarget(MetricDisk, "/var"); err != nil {
		t.Fatalf("a mount point was refused: %v", err)
	}
	if err := AlertTarget(MetricDisk, "/var\nrm"); err == nil {
		t.Error("a target containing a control character was accepted")
	}
}

func TestComparisonSeverityAndNameAreAllowlists(t *testing.T) {
	for _, comparison := range []string{ComparisonAbove, ComparisonBelow} {
		if err := AlertComparison(comparison); err != nil {
			t.Errorf("AlertComparison(%q) refused: %v", comparison, err)
		}
	}
	for _, bad := range []string{"", "equals", "!=", "ABOVE"} {
		if err := AlertComparison(bad); err == nil {
			t.Errorf("AlertComparison(%q) was accepted", bad)
		}
	}

	for _, severity := range []string{SeverityWarning, SeverityCritical} {
		if err := AlertSeverity(severity); err != nil {
			t.Errorf("AlertSeverity(%q) refused: %v", severity, err)
		}
	}
	if err := AlertSeverity("info"); err == nil {
		t.Error("an unknown severity was accepted")
	}

	if err := AlertRuleName("   "); err == nil {
		t.Error("a blank rule name was accepted")
	}
	if err := AlertRuleName("Disk nearly full"); err != nil {
		t.Errorf("a sensible name was refused: %v", err)
	}
}

func TestPercentMetricsAreTheOnesMeasuredInPercent(t *testing.T) {
	for _, metric := range []string{MetricCPU, MetricMemory, MetricDisk, MetricSwap} {
		if !IsPercentMetric(metric) {
			t.Errorf("%s should be a percentage", metric)
		}
	}
	for _, metric := range []string{MetricLoad, MetricNetworkRx, MetricNetworkTx, MetricService} {
		if IsPercentMetric(metric) {
			t.Errorf("%s should not be a percentage", metric)
		}
	}
}
