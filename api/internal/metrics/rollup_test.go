package metrics

import (
	"testing"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The aggregated history, and the question a duration-based alert rule asks of
// the raw samples.

func TestRollupSummarisesOnlyCompletedHours(t *testing.T) {
	// An hour still in progress would be summarised from half its samples and
	// then never revisited, which is a permanent wrong number in the history.
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 30, 0, 0, time.UTC)
	finished := time.Date(2026, 9, 3, 9, 15, 0, 0, time.UTC)
	inProgress := time.Date(2026, 9, 3, 10, 15, 0, 0, time.UTC)

	for _, when := range []time.Time{finished, finished.Add(time.Minute), inProgress} {
		if err := repo.Insert(ctx, serverID, Sample{
			Timestamp: when, CPUPercent: f64(50),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	if _, err := repo.Rollup(ctx, serverID, now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	count, err := repo.CountRollups(ctx, serverID)
	if err != nil {
		t.Fatalf("CountRollups: %v", err)
	}
	if count != 1 {
		t.Fatalf("buckets = %d, want only the completed hour", count)
	}
}

func TestRollupKeepsTheWorstReadingNotJustTheAverage(t *testing.T) {
	// An hour's average of 55% hides a minute at 99%, and on a disk that minute
	// is the whole story — which is exactly the event somebody looks back for.
	repo, serverID, ctx := setup(t)

	hour := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	now := hour.Add(90 * time.Minute)

	for index, value := range []float64{10, 10, 99, 10} {
		if err := repo.Insert(ctx, serverID, Sample{
			Timestamp:   hour.Add(time.Duration(index) * time.Minute),
			DiskPercent: f64(value),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	if _, err := repo.Rollup(ctx, serverID, now); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	series, err := repo.RollupHistory(ctx, serverID, Range90d, now)
	if err != nil {
		t.Fatalf("RollupHistory: %v", err)
	}
	if len(series.Points) != 1 {
		t.Fatalf("points = %d, want 1", len(series.Points))
	}

	point := series.Points[0]
	if point.DiskPercent == nil || *point.DiskPercent > 40 {
		t.Errorf("the average is %v, which is not an average", point.DiskPercent)
	}
	if point.DiskMax == nil || *point.DiskMax < 99 {
		t.Errorf("the worst reading was lost: %v", point.DiskMax)
	}
	if point.SampleCount == nil || *point.SampleCount != 4 {
		t.Errorf("sample count = %v, want 4", point.SampleCount)
	}
}

func TestRollupIsIdempotent(t *testing.T) {
	// A panel restarted mid-hour, or one catching up after a week off, must
	// produce the same rows either way.
	repo, serverID, ctx := setup(t)

	hour := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	now := hour.Add(2 * time.Hour)
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: hour, CPUPercent: f64(42)}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for range 3 {
		if _, err := repo.Rollup(ctx, serverID, now); err != nil {
			t.Fatalf("Rollup: %v", err)
		}
	}

	count, err := repo.CountRollups(ctx, serverID)
	if err != nil {
		t.Fatalf("CountRollups: %v", err)
	}
	if count != 1 {
		t.Fatalf("three passes produced %d buckets", count)
	}
}

func TestALongRangeFallsBackToTheRawSamples(t *testing.T) {
	// A panel whose first hour has not finished has no summaries, and answering
	// "no history" when the raw samples are sitting right there would be a
	// chart that is empty for an hour after installation.
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 30, 0, 0, time.UTC)
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp: now.Add(-10 * time.Minute), CPUPercent: f64(33),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range90d, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) == 0 {
		t.Fatal("a long range with no summaries returned nothing")
	}
}

func TestPruneRollupsRemovesOldBuckets(t *testing.T) {
	repo, serverID, ctx := setup(t)

	old := time.Date(2025, 1, 1, 9, 0, 0, 0, time.UTC)
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: old, CPUPercent: f64(1)}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if _, err := repo.Rollup(ctx, serverID, old.Add(2*time.Hour)); err != nil {
		t.Fatalf("Rollup: %v", err)
	}

	removed, err := repo.PruneRollups(ctx, old.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("PruneRollups: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
}

func TestBreachedSinceFindsTheStartOfTheCurrentRun(t *testing.T) {
	// The question a duration-based rule cannot answer from one reading, asked
	// of the stored samples so it survives a restart.
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	// Below, below, then a run of three above. The run starts at -20m.
	samples := []struct {
		offset time.Duration
		value  float64
	}{
		{-40 * time.Minute, 10},
		{-30 * time.Minute, 20},
		{-20 * time.Minute, 95},
		{-10 * time.Minute, 96},
		{-1 * time.Minute, 97},
	}
	for _, sample := range samples {
		if err := repo.Insert(ctx, serverID, Sample{
			Timestamp: now.Add(sample.offset), MemoryPercent: f64(sample.value),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	since, found, err := repo.BreachedSince(ctx, serverID, validate.MetricMemory, 90, true, now)
	if err != nil {
		t.Fatalf("BreachedSince: %v", err)
	}
	if !found {
		t.Fatal("a run of breaching samples was not found")
	}
	if want := now.Add(-20 * time.Minute); !since.UTC().Equal(want.UTC()) {
		t.Fatalf("since = %v, want %v", since.UTC(), want.UTC())
	}
}

func TestBreachedSinceIgnoresAnOlderRunThatRecovered(t *testing.T) {
	// A machine that was full last night and is full again now has been full
	// since now, not since last night. Getting this wrong would make every
	// recovered incident count towards the next one's duration.
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	for _, sample := range []struct {
		offset time.Duration
		value  float64
	}{
		{-6 * time.Hour, 99},
		{-5 * time.Hour, 99},
		{-4 * time.Hour, 10}, // recovered
		{-2 * time.Minute, 99},
	} {
		if err := repo.Insert(ctx, serverID, Sample{
			Timestamp: now.Add(sample.offset), MemoryPercent: f64(sample.value),
		}); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	since, found, err := repo.BreachedSince(ctx, serverID, validate.MetricMemory, 90, true, now)
	if err != nil {
		t.Fatalf("BreachedSince: %v", err)
	}
	if !found {
		t.Fatal("the current run was not found")
	}
	if want := now.Add(-2 * time.Minute); !since.UTC().Equal(want.UTC()) {
		t.Fatalf("since = %v, want the current run's start %v", since.UTC(), want.UTC())
	}
}

func TestBreachedSinceReportsNothingWhenNothingBreaches(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp: now.Add(-time.Minute), MemoryPercent: f64(20),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, found, err := repo.BreachedSince(ctx, serverID, validate.MetricMemory, 90, true, now); err != nil {
		t.Fatalf("BreachedSince: %v", err)
	} else if found {
		t.Fatal("a quiet host was reported as breaching")
	}
}

func TestBreachedSinceHasNoAnswerForDisk(t *testing.T) {
	// The sample table holds one whole-host disk figure, and a rule about a
	// single filesystem cannot be answered from it. Answering with the wrong
	// filesystem's history would be worse than answering with none.
	repo, serverID, ctx := setup(t)

	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp: now.Add(-time.Minute), DiskPercent: f64(99),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if _, found, err := repo.BreachedSince(ctx, serverID, validate.MetricDisk, 90, true, now); err != nil {
		t.Fatalf("BreachedSince: %v", err)
	} else if found {
		t.Fatal("a per-filesystem question was answered from the whole-host figure")
	}
}
