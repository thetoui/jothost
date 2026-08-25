// Package metrics stores and queries the server metric history behind the
// dashboard graph.
//
// The Agent deliberately keeps no history (docs/PHASE2.md section 3.1), so
// persistence lives here: a sampler writes rows on a cadence and the query
// side buckets them into a series the UI can plot.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Sample is one metric reading.
//
// Every field is a pointer because a partially available reading is normal: a
// host may answer for memory while its disk probe times out, and recording a
// zero would be indistinguishable from a genuinely idle machine.
type Sample struct {
	Timestamp     time.Time
	CPUPercent    *float64
	MemoryPercent *float64
	DiskPercent   *float64
	Load1         *float64
	Load5         *float64
	Load15        *float64
	NetworkRx     *int64
	NetworkTx     *int64
}

// Empty reports whether a sample carries no readings at all, which is not
// worth a row.
func (s Sample) Empty() bool {
	return s.CPUPercent == nil && s.MemoryPercent == nil && s.DiskPercent == nil &&
		s.Load1 == nil && s.Load5 == nil && s.Load15 == nil &&
		s.NetworkRx == nil && s.NetworkTx == nil
}

// Point is one bucket of a metric series.
type Point struct {
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    *float64  `json:"cpu_percent"`
	MemoryPercent *float64  `json:"memory_percent"`
	DiskPercent   *float64  `json:"disk_percent"`
	Load1         *float64  `json:"load_1"`
	// NetworkRxPerSecond and NetworkTxPerSecond are derived from the change in
	// the cumulative counters across the bucket. Storing the counter rather
	// than a rate is what makes this possible at any resolution.
	NetworkRxPerSecond *float64 `json:"network_rx_per_second"`
	NetworkTxPerSecond *float64 `json:"network_tx_per_second"`
}

// Range is a supported history window (API_SPEC.md section 5).
type Range string

// Supported ranges.
const (
	Range1h  Range = "1h"
	Range24h Range = "24h"
	Range7d  Range = "7d"
	Range30d Range = "30d"
)

// ErrInvalidRange is returned for a range outside the supported set.
var ErrInvalidRange = errors.New("unsupported range")

// rangeSpec describes how a range is queried.
type rangeSpec struct {
	duration time.Duration
	// bucket is the aggregation width. Each range is sized to produce roughly
	// 100-200 points: enough to see shape, few enough to send and plot.
	bucket time.Duration
}

var rangeSpecs = map[Range]rangeSpec{
	Range1h:  {duration: time.Hour, bucket: time.Minute},
	Range24h: {duration: 24 * time.Hour, bucket: 15 * time.Minute},
	Range7d:  {duration: 7 * 24 * time.Hour, bucket: time.Hour},
	Range30d: {duration: 30 * 24 * time.Hour, bucket: 6 * time.Hour},
}

// ParseRange validates a caller-supplied range.
func ParseRange(value string) (Range, error) {
	if value == "" {
		return Range1h, nil
	}

	r := Range(value)
	if _, ok := rangeSpecs[r]; !ok {
		return "", fmt.Errorf("%w: %q", ErrInvalidRange, value)
	}
	return r, nil
}

// Ranges returns the supported ranges, for documentation and error messages.
func Ranges() []Range {
	return []Range{Range1h, Range24h, Range7d, Range30d}
}

// Series is a metric history response.
type Series struct {
	Range  Range   `json:"range"`
	Bucket string  `json:"bucket"`
	From   string  `json:"from"`
	To     string  `json:"to"`
	Points []Point `json:"points"`
}

// Repository reads and writes metric samples.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository builds a Repository.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert records one sample.
func (r *Repository) Insert(ctx context.Context, serverID string, sample Sample) error {
	if sample.Empty() {
		return nil
	}

	timestamp := sample.Timestamp
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	_, err := r.pool.Exec(ctx, `
		INSERT INTO system_metrics
			(server_id, timestamp, cpu_percent, memory_percent, disk_percent,
			 load_1, load_5, load_15, network_rx, network_tx)
		VALUES ($1::uuid, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		serverID, timestamp,
		sample.CPUPercent, sample.MemoryPercent, sample.DiskPercent,
		sample.Load1, sample.Load5, sample.Load15,
		sample.NetworkRx, sample.NetworkTx)
	if err != nil {
		return fmt.Errorf("insert metric sample: %w", err)
	}
	return nil
}

// History returns a bucketed series for a server.
//
// Averaging within a bucket smooths the series without hiding a sustained
// spike, and doing it in Postgres keeps the response small: a 30-day window at
// the sampling interval would otherwise be tens of thousands of rows.
//
// Network is reported as a rate: the counters are cumulative, so the bucket's
// delta is divided by its span. A counter reset (an interface recreated, a
// reboot) produces a negative delta, which is dropped rather than plotted as a
// spike.
func (r *Repository) History(ctx context.Context, serverID string, rng Range, now time.Time) (Series, error) {
	spec, ok := rangeSpecs[rng]
	if !ok {
		return Series{}, fmt.Errorf("%w: %q", ErrInvalidRange, rng)
	}

	from := now.Add(-spec.duration)

	rows, err := r.pool.Query(ctx, `
		WITH bucketed AS (
			SELECT
				date_bin($2::interval, timestamp, $3::timestamptz) AS bucket,
				avg(cpu_percent)    AS cpu_percent,
				avg(memory_percent) AS memory_percent,
				avg(disk_percent)   AS disk_percent,
				avg(load_1)         AS load_1,
				min(network_rx)     AS rx_min,
				max(network_rx)     AS rx_max,
				min(network_tx)     AS tx_min,
				max(network_tx)     AS tx_max,
				min(timestamp)      AS first_seen,
				max(timestamp)      AS last_seen
			FROM system_metrics
			WHERE server_id = $1::uuid
			  AND timestamp >= $3::timestamptz
			  AND timestamp <= $4::timestamptz
			GROUP BY bucket
		)
		SELECT
			bucket,
			cpu_percent,
			memory_percent,
			disk_percent,
			load_1,
			rx_max - rx_min,
			tx_max - tx_min,
			EXTRACT(EPOCH FROM (last_seen - first_seen))
		FROM bucketed
		ORDER BY bucket`,
		serverID, spec.bucket, from, now)
	if err != nil {
		return Series{}, fmt.Errorf("query metric history: %w", err)
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
			rxDelta *int64
			txDelta *int64
			span    *float64
		)
		if err := rows.Scan(&point.Timestamp, &point.CPUPercent, &point.MemoryPercent,
			&point.DiskPercent, &point.Load1, &rxDelta, &txDelta, &span); err != nil {
			return Series{}, fmt.Errorf("scan metric point: %w", err)
		}

		point.NetworkRxPerSecond = perSecond(rxDelta, span)
		point.NetworkTxPerSecond = perSecond(txDelta, span)
		series.Points = append(series.Points, point)
	}
	if err := rows.Err(); err != nil {
		return Series{}, fmt.Errorf("iterate metric points: %w", err)
	}
	return series, nil
}

// Latest returns the most recent sample for a server.
func (r *Repository) Latest(ctx context.Context, serverID string) (Sample, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT timestamp, cpu_percent, memory_percent, disk_percent,
		       load_1, load_5, load_15, network_rx, network_tx
		FROM system_metrics
		WHERE server_id = $1::uuid
		ORDER BY timestamp DESC
		LIMIT 1`, serverID)

	var sample Sample
	err := row.Scan(&sample.Timestamp, &sample.CPUPercent, &sample.MemoryPercent,
		&sample.DiskPercent, &sample.Load1, &sample.Load5, &sample.Load15,
		&sample.NetworkRx, &sample.NetworkTx)
	if err != nil {
		return Sample{}, fmt.Errorf("select latest sample: %w", err)
	}
	return sample, nil
}

// Count returns how many samples a server has, for diagnostics and tests.
func (r *Repository) Count(ctx context.Context, serverID string) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM system_metrics WHERE server_id = $1::uuid`, serverID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count metric samples: %w", err)
	}
	return count, nil
}

// Prune deletes samples older than cutoff and reports how many were removed.
//
// Retention is enforced by deletion rather than by partitioning: at one sample
// per interval this table stays small enough that the simpler mechanism is the
// right one, and partition management is a maintenance burden with no payoff
// at this size.
func (r *Repository) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM system_metrics WHERE timestamp < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune metric samples: %w", err)
	}
	return tag.RowsAffected(), nil
}

// perSecond converts a counter delta over a span into a rate.
func perSecond(delta *int64, span *float64) *float64 {
	// A bucket holding a single sample has no span to divide by, and a
	// negative delta means the counter reset. Both report no rate rather than
	// a fabricated one.
	if delta == nil || span == nil || *span <= 0 || *delta < 0 {
		return nil
	}

	rate := float64(*delta) / *span
	return &rate
}
