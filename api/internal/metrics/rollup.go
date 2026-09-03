package metrics

import (
	"context"
	"fmt"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// Aggregated history, and why it exists beside the raw samples.
//
// The two answer different questions. A sample every thirty seconds is what
// makes an hour of history readable; nobody plotting a quarter needs that
// resolution, and keeping a quarter of it would be a million rows per server to
// answer what a few thousand can. So completed hours are summarised, and the
// summaries are kept long after the samples behind them are gone.
//
// Each bucket carries an average *and* a maximum. The average is what a graph
// plots; the maximum is what stops an hour of aggregation hiding the
// five-minute spike that filled a disk — which is exactly the event somebody
// looks back for.

// RollupBucket is the width of an aggregated bucket. An hour is the smallest
// unit anybody asks a quarter-long question in.
const RollupBucket = time.Hour

// Long ranges, served from the rollups.
//
// They are separate constants rather than an extension of the raw ranges
// because they are answered from a different table, and a caller asking for
// them is asking a question the raw samples may no longer be able to answer.
const (
	Range90d Range = "90d"
	Range1y  Range = "1y"
)

// rollupSpecs describes the ranges served from aggregated data.
//
// The bucket is a multiple of RollupBucket: a 90-day window at hourly
// resolution is 2160 points, which is more than a chart can show and more than
// is worth sending.
var rollupSpecs = map[Range]rangeSpec{
	Range90d: {duration: 90 * 24 * time.Hour, bucket: 6 * time.Hour},
	Range1y:  {duration: 365 * 24 * time.Hour, bucket: 24 * time.Hour},
}

// Rollup is one aggregated bucket.
type Rollup struct {
	BucketStart time.Time `json:"bucket_start"`

	CPUAvg    *float64 `json:"cpu_avg"`
	CPUMax    *float64 `json:"cpu_max"`
	MemoryAvg *float64 `json:"memory_avg"`
	MemoryMax *float64 `json:"memory_max"`
	DiskAvg   *float64 `json:"disk_avg"`
	DiskMax   *float64 `json:"disk_max"`
	Load1Avg  *float64 `json:"load_1_avg"`
	Load1Max  *float64 `json:"load_1_max"`

	NetworkRxPerSecond *float64 `json:"network_rx_per_second"`
	NetworkTxPerSecond *float64 `json:"network_tx_per_second"`

	// SampleCount is how many raw readings went into this bucket. A bucket
	// built from two samples is not the same evidence as one built from a
	// hundred, and a reader deserves to be able to tell.
	SampleCount int `json:"sample_count"`
}

// IsRollupRange reports whether a range is served from aggregated data.
func IsRollupRange(rng Range) bool {
	_, ok := rollupSpecs[rng]
	return ok
}

// Rollup builds the summaries for every completed hour that has none.
//
// Completed only: an hour still in progress would be summarised from half its
// samples and then never revisited, which is a permanent wrong number in the
// history. The current hour is left alone until it is over.
//
// It is idempotent by construction — the insert is keyed on the bucket and
// updates what is there — so a panel restarted mid-hour, or one catching up
// after being off for a week, produces the same rows either way.
func (r *Repository) Rollup(ctx context.Context, serverID string, now time.Time) (int, error) {
	// The boundary of the hour in progress. Everything strictly before it is
	// finished and safe to summarise.
	currentBucket := now.UTC().Truncate(RollupBucket)

	tag, err := r.pool.Exec(ctx, `
		INSERT INTO metric_rollups (
			server_id, bucket_start,
			cpu_avg, cpu_max, memory_avg, memory_max,
			disk_avg, disk_max, load_1_avg, load_1_max,
			network_rx_per_second, network_tx_per_second, sample_count)
		SELECT
			$1::uuid,
			bucket,
			avg_cpu, max_cpu, avg_memory, max_memory,
			avg_disk, max_disk, avg_load, max_load,
			-- The counters are cumulative, so the bucket's rate is its delta
			-- over its span. A negative delta means the counter reset — an
			-- interface recreated, a reboot — and produces no rate rather than
			-- a fabricated spike.
			CASE WHEN span > 0 AND rx_max >= rx_min THEN (rx_max - rx_min) / span END,
			CASE WHEN span > 0 AND tx_max >= tx_min THEN (tx_max - tx_min) / span END,
			samples
		FROM (
			SELECT
				date_bin($2::interval, timestamp, timestamptz 'epoch') AS bucket,
				avg(cpu_percent)    AS avg_cpu,
				max(cpu_percent)    AS max_cpu,
				avg(memory_percent) AS avg_memory,
				max(memory_percent) AS max_memory,
				avg(disk_percent)   AS avg_disk,
				max(disk_percent)   AS max_disk,
				avg(load_1)         AS avg_load,
				max(load_1)         AS max_load,
				min(network_rx)     AS rx_min,
				max(network_rx)     AS rx_max,
				min(network_tx)     AS tx_min,
				max(network_tx)     AS tx_max,
				EXTRACT(EPOCH FROM (max(timestamp) - min(timestamp))) AS span,
				count(*)            AS samples
			FROM system_metrics
			WHERE server_id = $1::uuid
			  AND timestamp < $3::timestamptz
			GROUP BY bucket
		) AS buckets
		ON CONFLICT (server_id, bucket_start) DO UPDATE SET
			cpu_avg               = EXCLUDED.cpu_avg,
			cpu_max               = EXCLUDED.cpu_max,
			memory_avg            = EXCLUDED.memory_avg,
			memory_max            = EXCLUDED.memory_max,
			disk_avg              = EXCLUDED.disk_avg,
			disk_max              = EXCLUDED.disk_max,
			load_1_avg            = EXCLUDED.load_1_avg,
			load_1_max            = EXCLUDED.load_1_max,
			network_rx_per_second = EXCLUDED.network_rx_per_second,
			network_tx_per_second = EXCLUDED.network_tx_per_second,
			sample_count          = EXCLUDED.sample_count
		-- Only where the new figure is built on at least as much evidence. A
		-- catch-up pass over a window whose raw samples have since been pruned
		-- would otherwise overwrite a good summary with a worse one.
		WHERE EXCLUDED.sample_count >= metric_rollups.sample_count`,
		serverID, RollupBucket, currentBucket)
	if err != nil {
		return 0, fmt.Errorf("roll up metric samples: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RollupHistory returns a long-range series from the aggregated data.
//
// The points carry averages, because that is what a line on a chart means. The
// maxima are aggregated alongside so a caller can show the worst reading in a
// bucket without a second query — an hour's average of 60% hides a minute at
// 99%, and on a disk that minute is the whole story.
func (r *Repository) RollupHistory(ctx context.Context, serverID string, rng Range,
	now time.Time,
) (Series, error) {
	spec, ok := rollupSpecs[rng]
	if !ok {
		return Series{}, fmt.Errorf("%w: %q", ErrInvalidRange, rng)
	}

	from := now.Add(-spec.duration)

	rows, err := r.pool.Query(ctx, `
		SELECT
			date_bin($2::interval, bucket_start, timestamptz 'epoch') AS bucket,
			avg(cpu_avg), max(cpu_max),
			avg(memory_avg), max(memory_max),
			avg(disk_avg), max(disk_max),
			avg(load_1_avg), max(load_1_max),
			avg(network_rx_per_second), avg(network_tx_per_second),
			sum(sample_count)
		FROM metric_rollups
		WHERE server_id = $1::uuid
		  AND bucket_start >= $3::timestamptz
		  AND bucket_start <= $4::timestamptz
		GROUP BY bucket
		ORDER BY bucket`,
		serverID, spec.bucket, from, now)
	if err != nil {
		return Series{}, fmt.Errorf("query aggregated metric history: %w", err)
	}
	defer rows.Close()

	series := Series{
		Range:  rng,
		Bucket: spec.bucket.String(),
		From:   from.UTC().Format(time.RFC3339),
		To:     now.UTC().Format(time.RFC3339),
		Points: []Point{},
	}

	for rows.Next() {
		var (
			point   Point
			cpuMax  *float64
			memMax  *float64
			diskMax *float64
			loadMax *float64
			samples *int64
		)
		if err := rows.Scan(&point.Timestamp,
			&point.CPUPercent, &cpuMax,
			&point.MemoryPercent, &memMax,
			&point.DiskPercent, &diskMax,
			&point.Load1, &loadMax,
			&point.NetworkRxPerSecond, &point.NetworkTxPerSecond,
			&samples); err != nil {
			return Series{}, fmt.Errorf("scan aggregated metric point: %w", err)
		}

		point.CPUMax = cpuMax
		point.MemoryMax = memMax
		point.DiskMax = diskMax
		point.Load1Max = loadMax
		if samples != nil {
			count := int(*samples)
			point.SampleCount = &count
		}
		series.Points = append(series.Points, point)
	}
	if err := rows.Err(); err != nil {
		return Series{}, fmt.Errorf("iterate aggregated metric points: %w", err)
	}
	return series, nil
}

// PruneRollups deletes summaries older than cutoff.
//
// A far longer cutoff than the raw samples' — that is the entire point of them
// — but not unbounded: a year of hourly buckets is under nine thousand rows per
// server, and a panel that never forgets anything is one whose oldest data
// nobody has looked at since it was written.
func (r *Repository) PruneRollups(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM metric_rollups WHERE bucket_start < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune metric rollups: %w", err)
	}
	return tag.RowsAffected(), nil
}

// CountRollups returns how many buckets a server has.
func (r *Repository) CountRollups(ctx context.Context, serverID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM metric_rollups WHERE server_id = $1::uuid`, serverID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count metric rollups: %w", err)
	}
	return count, nil
}

// BreachedSince reports when the current run of breaching samples began.
//
// It answers the question a duration-based alert rule cannot answer from a
// single reading: "how long has this been true". Asked of the stored samples
// rather than kept in memory, so a panel restarted in the middle of an incident
// does not forget that a machine has been at 99% for an hour — which is exactly
// when a restart is most likely to happen.
//
// The run is found by taking every breaching sample after the most recent one
// that did *not* breach. A gap in sampling does not break the run: the panel
// being off is not evidence that a disk emptied, and treating it as such would
// reset every incident's clock on every deploy.
func (r *Repository) BreachedSince(ctx context.Context, serverID, metric string,
	threshold float64, above bool, now time.Time,
) (time.Time, bool, error) {
	column, ok := breachColumns[metric]
	if !ok {
		return time.Time{}, false, nil
	}

	// The column name comes from a fixed table keyed by a metric this panel
	// defines — never from a request — which is what makes interpolating it
	// into the statement safe. The threshold is a parameter, as it must be.
	comparison := ">"
	if !above {
		comparison = "<"
	}

	// The window is bounded so a host that has been breaching for months does
	// not scan its whole history on every evaluation. A day is far longer than
	// any rule this panel accepts.
	from := now.Add(-24 * time.Hour)

	var since *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT min(timestamp)
		FROM system_metrics
		WHERE server_id = $1::uuid
		  AND timestamp >= $2::timestamptz
		  AND `+column+` IS NOT NULL
		  AND `+column+` `+comparison+` $3
		  AND timestamp > COALESCE((
			SELECT max(timestamp)
			FROM system_metrics
			WHERE server_id = $1::uuid
			  AND timestamp >= $2::timestamptz
			  AND `+column+` IS NOT NULL
			  AND NOT (`+column+` `+comparison+` $3)
		  ), $2::timestamptz)`,
		serverID, from, threshold).Scan(&since)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read how long a threshold has been breached: %w", err)
	}
	if since == nil {
		return time.Time{}, false, nil
	}
	return *since, true, nil
}

// breachColumns maps a metric to the column holding it.
//
// Only the metrics the sample table actually stores. Disk is deliberately
// absent even though the column exists: it holds one whole-host figure, and a
// rule about a single filesystem cannot be answered from it — answering with
// the wrong filesystem's history would be worse than answering with none.
var breachColumns = map[string]string{
	validate.MetricCPU:    "cpu_percent",
	validate.MetricMemory: "memory_percent",
	validate.MetricLoad:   "load_1",
}
