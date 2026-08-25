package collectors

import (
	"fmt"
	"strings"
)

// MemoryStats is a memory usage sample. All byte figures are bytes, not the
// kibibytes /proc/meminfo reports, so callers never have to remember the unit.
type MemoryStats struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	FreeBytes      uint64 `json:"free_bytes"`
	BuffersBytes   uint64 `json:"buffers_bytes"`
	CachedBytes    uint64 `json:"cached_bytes"`

	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapUsedBytes  uint64 `json:"swap_used_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`

	UsedPercent     float64 `json:"used_percent"`
	SwapUsedPercent float64 `json:"swap_used_percent"`
}

// Memory reads /proc/meminfo.
//
// "Used" is derived from MemAvailable rather than from MemFree. MemFree
// excludes reclaimable page cache, so using it reports a healthy Linux box as
// almost out of memory — the classic misreading that makes dashboards lie.
func (c *Collector) Memory() (MemoryStats, error) {
	path, err := c.procPath("meminfo")
	if err != nil {
		return MemoryStats{}, err
	}

	lines, err := readLines(path)
	if err != nil {
		return MemoryStats{}, fmt.Errorf("%w: /proc/meminfo is unreadable", ErrUnsupported)
	}

	values := make(map[string]uint64, 16)
	for _, line := range lines {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}

		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		value, err := parseUint(fields[0])
		if err != nil {
			continue
		}
		// meminfo reports kB for everything except a few page counters, which
		// carry no unit suffix.
		if len(fields) > 1 && strings.EqualFold(fields[1], "kB") {
			value *= 1024
		}
		values[key] = value
	}

	total, ok := values["MemTotal"]
	if !ok {
		return MemoryStats{}, fmt.Errorf("%w: MemTotal missing from /proc/meminfo", ErrMalformed)
	}

	stats := MemoryStats{
		TotalBytes:   total,
		FreeBytes:    values["MemFree"],
		BuffersBytes: values["Buffers"],
		CachedBytes:  values["Cached"],
	}

	available, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		// Kernels before 3.14 lack MemAvailable; approximate it the way the
		// kernel does, rather than falling back to MemFree.
		available = stats.FreeBytes + stats.BuffersBytes + stats.CachedBytes
	}
	if available > total {
		available = total
	}
	stats.AvailableBytes = available
	stats.UsedBytes = total - available
	stats.UsedPercent = percent(stats.UsedBytes, total)

	stats.SwapTotalBytes = values["SwapTotal"]
	stats.SwapFreeBytes = values["SwapFree"]
	if stats.SwapTotalBytes >= stats.SwapFreeBytes {
		stats.SwapUsedBytes = stats.SwapTotalBytes - stats.SwapFreeBytes
	}
	stats.SwapUsedPercent = percent(stats.SwapUsedBytes, stats.SwapTotalBytes)

	return stats, nil
}
