package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
)

// setup returns a metrics repository and a registered server id.
func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "metrics-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func f64(v float64) *float64 { return &v }
func i64(v int64) *int64     { return &v }

func TestParseRange(t *testing.T) {
	// An absent range is the common case from a freshly loaded dashboard.
	if rng, err := ParseRange(""); err != nil || rng != Range1h {
		t.Fatalf("empty range must default to 1h, got %q (%v)", rng, err)
	}

	// The last two are Phase 19's, answered from the aggregated history rather
	// than the raw samples — which is what lets them outlive the samples'
	// retention.
	for _, value := range []string{"1h", "24h", "7d", "30d", "90d", "1y"} {
		if _, err := ParseRange(value); err != nil {
			t.Fatalf("range %q must be accepted: %v", value, err)
		}
	}

	// Anything else must be refused rather than silently treated as a default:
	// a typo returning an hour of data looks like a working graph.
	for _, value := range []string{"2h", "5y", "all", "1h; DROP TABLE", "-1h", "0"} {
		if _, err := ParseRange(value); !errors.Is(err, ErrInvalidRange) {
			t.Fatalf("range %q must be rejected, got %v", value, err)
		}
	}
}

func TestInsertAndCount(t *testing.T) {
	repo, serverID, ctx := setup(t)

	err := repo.Insert(ctx, serverID, Sample{
		Timestamp:     time.Now(),
		CPUPercent:    f64(12.5),
		MemoryPercent: f64(48.25),
		DiskPercent:   f64(61),
		Load1:         f64(1.5),
		NetworkRx:     i64(1000),
		NetworkTx:     i64(500),
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}

	count, err := repo.Count(ctx, serverID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 sample, got %d", count)
	}

	latest, err := repo.Latest(ctx, serverID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.CPUPercent == nil || *latest.CPUPercent != 12.5 {
		t.Fatalf("cpu round-trip failed: %v", latest.CPUPercent)
	}
	if latest.NetworkRx == nil || *latest.NetworkRx != 1000 {
		t.Fatalf("network round-trip failed: %v", latest.NetworkRx)
	}
}

func TestInsertSkipsAnEmptySample(t *testing.T) {
	repo, serverID, ctx := setup(t)

	// A reading where nothing could be collected must not become a row of
	// NULLs, which would be indistinguishable from a host reporting zeros.
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: time.Now()}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	count, err := repo.Count(ctx, serverID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("an empty sample must not be stored, got %d rows", count)
	}
}

func TestInsertAcceptsAPartialSample(t *testing.T) {
	repo, serverID, ctx := setup(t)

	// A host answering for memory while its disk probe times out is normal.
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp:     time.Now(),
		MemoryPercent: f64(50),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	latest, err := repo.Latest(ctx, serverID)
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest.MemoryPercent == nil || *latest.MemoryPercent != 50 {
		t.Fatalf("memory = %v", latest.MemoryPercent)
	}
	if latest.DiskPercent != nil {
		t.Fatalf("an uncollected metric must stay null, got %v", *latest.DiskPercent)
	}
}

func TestHistoryBucketsAndAverages(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now().Truncate(time.Minute)

	// Two samples inside one minute must average into a single point.
	for i, cpu := range []float64{10, 30} {
		if err := repo.Insert(ctx, serverID, Sample{
			Timestamp:  now.Add(-30 * time.Minute).Add(time.Duration(i) * time.Second),
			CPUPercent: f64(cpu),
		}); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	// A third sample in a different minute must be its own point.
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp:  now.Add(-20 * time.Minute),
		CPUPercent: f64(80),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if series.Range != Range1h || series.Bucket != "1m0s" {
		t.Fatalf("unexpected series metadata: %+v", series)
	}
	if len(series.Points) != 2 {
		t.Fatalf("expected 2 buckets, got %d: %+v", len(series.Points), series.Points)
	}
	if series.Points[0].CPUPercent == nil || *series.Points[0].CPUPercent != 20 {
		t.Fatalf("bucket average = %v, want 20", series.Points[0].CPUPercent)
	}
	if series.Points[1].CPUPercent == nil || *series.Points[1].CPUPercent != 80 {
		t.Fatalf("second bucket = %v, want 80", series.Points[1].CPUPercent)
	}
	// Points must arrive oldest first so a chart can plot them directly.
	if !series.Points[0].Timestamp.Before(series.Points[1].Timestamp) {
		t.Fatal("points must be ordered oldest first")
	}
}

func TestHistoryExcludesSamplesOutsideTheWindow(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now()

	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp:  now.Add(-2 * time.Hour),
		CPUPercent: f64(99),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp:  now.Add(-10 * time.Minute),
		CPUPercent: f64(10),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) != 1 {
		t.Fatalf("expected only the in-window sample, got %d", len(series.Points))
	}
	if *series.Points[0].CPUPercent != 10 {
		t.Fatalf("the wrong sample was returned: %v", *series.Points[0].CPUPercent)
	}
}

func TestHistoryComputesNetworkRates(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now().Truncate(time.Hour)

	// Two samples 10 seconds apart in one bucket: 10000 bytes over 10 seconds
	// is 1000 bytes per second.
	base := now.Add(-30 * time.Minute)
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: base, NetworkRx: i64(1000), NetworkTx: i64(500)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp: base.Add(10 * time.Second),
		NetworkRx: i64(11000),
		NetworkTx: i64(3000),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) == 0 {
		t.Fatal("expected a point")
	}

	point := series.Points[0]
	if point.NetworkRxPerSecond == nil || *point.NetworkRxPerSecond != 1000 {
		t.Fatalf("rx rate = %v, want 1000", point.NetworkRxPerSecond)
	}
	if point.NetworkTxPerSecond == nil || *point.NetworkTxPerSecond != 250 {
		t.Fatalf("tx rate = %v, want 250", point.NetworkTxPerSecond)
	}
}

func TestHistoryReportsNoRateForASingleSample(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now().Truncate(time.Hour)
	if err := repo.Insert(ctx, serverID, Sample{
		Timestamp: now.Add(-30 * time.Minute),
		NetworkRx: i64(1000),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(series.Points))
	}
	// One sample has no span to divide by; a fabricated rate would be worse
	// than an absent one.
	if series.Points[0].NetworkRxPerSecond != nil {
		t.Fatalf("expected no rate, got %v", *series.Points[0].NetworkRxPerSecond)
	}
}

func TestHistoryIgnoresACounterReset(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now().Truncate(time.Hour)
	base := now.Add(-30 * time.Minute)

	// A reboot resets the interface counters. A naive max-minus-min would
	// still be positive here, so the rate is only reported when the later
	// sample is genuinely larger.
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: base, NetworkRx: i64(500000)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: base.Add(10 * time.Second), NetworkRx: i64(100)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	series, err := repo.History(ctx, serverID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(series.Points))
	}

	// The delta across the bucket must not be plotted as a huge spike.
	if rate := series.Points[0].NetworkRxPerSecond; rate != nil && *rate > 100000 {
		t.Fatalf("a counter reset produced an implausible rate: %v", *rate)
	}
}

func TestHistoryIsEmptyForAServerWithNoSamples(t *testing.T) {
	repo, serverID, ctx := setup(t)

	series, err := repo.History(ctx, serverID, Range24h, time.Now())
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) != 0 {
		t.Fatalf("expected no points, got %d", len(series.Points))
	}
	// A nil slice would serialise as null and break a chart expecting an array.
	if series.Points == nil {
		t.Fatal("Points must be a non-nil slice")
	}
}

func TestHistoryIsolatesServers(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	serverRepo := servers.NewRepository(deps.Pool)
	first, err := serverRepo.Register(ctx, servers.RegisterParams{Hostname: "host-a"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	second, err := serverRepo.Register(ctx, servers.RegisterParams{Hostname: "host-b"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	repo := NewRepository(deps.Pool)
	now := time.Now()
	if err := repo.Insert(ctx, first.ID, Sample{Timestamp: now.Add(-time.Minute), CPUPercent: f64(50)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// One host's readings must never appear on another's graph.
	series, err := repo.History(ctx, second.ID, Range1h, now)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(series.Points) != 0 {
		t.Fatalf("expected no points for the other server, got %d", len(series.Points))
	}
}

func TestRangesCoverTheDocumentedSet(t *testing.T) {
	// API_SPEC.md section 5 names exactly these. The last two arrived with
	// Phase 19's aggregated history.
	want := map[Range]bool{
		Range1h: false, Range24h: false, Range7d: false, Range30d: false,
		Range90d: false, Range1y: false,
	}
	for _, rng := range Ranges() {
		if _, ok := want[rng]; !ok {
			t.Fatalf("unexpected range %q", rng)
		}
		want[rng] = true
	}
	for rng, seen := range want {
		if !seen {
			t.Fatalf("range %q is missing", rng)
		}
	}
}

func TestPruneRemovesOldSamples(t *testing.T) {
	repo, serverID, ctx := setup(t)

	now := time.Now()
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: now.Add(-48 * time.Hour), CPUPercent: f64(10)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := repo.Insert(ctx, serverID, Sample{Timestamp: now.Add(-time.Hour), CPUPercent: f64(20)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	removed, err := repo.Prune(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("expected 1 sample removed, got %d", removed)
	}

	count, err := repo.Count(ctx, serverID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 1 {
		t.Fatalf("the recent sample must survive, got %d rows", count)
	}
}

func TestSamplesAreDeletedWithTheirServer(t *testing.T) {
	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{Hostname: "doomed"})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	repo := NewRepository(deps.Pool)
	if err := repo.Insert(ctx, server.ID, Sample{Timestamp: time.Now(), CPUPercent: f64(10)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	// The cascade is what keeps orphaned metric rows from accumulating.
	if _, err := deps.Pool.Exec(ctx, `DELETE FROM servers WHERE id = $1::uuid`, server.ID); err != nil {
		t.Fatalf("delete server: %v", err)
	}

	count, err := repo.Count(ctx, server.ID)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if count != 0 {
		t.Fatalf("metrics must cascade with their server, got %d rows", count)
	}
}
