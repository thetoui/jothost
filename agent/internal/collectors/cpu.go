package collectors

import (
	"fmt"
	"strings"
	"time"
)

// CPUTimes holds the raw jiffy counters from one /proc/stat cpu line.
type CPUTimes struct {
	User    uint64 `json:"user"`
	Nice    uint64 `json:"nice"`
	System  uint64 `json:"system"`
	Idle    uint64 `json:"idle"`
	IOWait  uint64 `json:"iowait"`
	IRQ     uint64 `json:"irq"`
	SoftIRQ uint64 `json:"softirq"`
	Steal   uint64 `json:"steal"`
}

// Total is the sum of every counter.
func (t CPUTimes) Total() uint64 {
	return t.User + t.Nice + t.System + t.Idle + t.IOWait + t.IRQ + t.SoftIRQ + t.Steal
}

// Busy is the time not spent idle.
func (t CPUTimes) Busy() uint64 {
	return t.Total() - t.Idle - t.IOWait
}

// CPUStats is a CPU usage sample.
type CPUStats struct {
	// UsagePercent is the busy share since the previous sample. It is zero on
	// the very first sample, when there is no interval to compare against.
	UsagePercent float64 `json:"usage_percent"`
	// UserPercent and friends break the interval down. They are zero on the
	// first sample for the same reason.
	UserPercent   float64 `json:"user_percent"`
	SystemPercent float64 `json:"system_percent"`
	IOWaitPercent float64 `json:"iowait_percent"`
	IdlePercent   float64 `json:"idle_percent"`
	// Cores is the number of logical CPUs.
	Cores int `json:"cores"`
	// SampleWindow is how long the reported interval covers.
	SampleWindow string `json:"sample_window"`
	// Times is the cumulative counter, exposed so a caller can compute its own
	// deltas over a longer window.
	Times CPUTimes `json:"times"`
}

// cpuSample is a stored reading used to compute the next delta.
type cpuSample struct {
	times CPUTimes
	at    time.Time
}

// CPU returns CPU usage since the previous call.
//
// /proc/stat exposes cumulative counters, not a rate, so a usage percentage
// only exists relative to an earlier reading. The Collector keeps the last
// sample; the first call after start reports counters with zero percentages
// rather than inventing a number by comparing against boot, which would report
// a machine's lifetime average and look wrong to anyone watching a graph.
func (c *Collector) CPU() (CPUStats, error) {
	path, err := c.procPath("stat")
	if err != nil {
		return CPUStats{}, err
	}

	lines, err := readLines(path)
	if err != nil {
		return CPUStats{}, fmt.Errorf("%w: /proc/stat is unreadable", ErrUnsupported)
	}

	var (
		aggregate CPUTimes
		found     bool
		cores     int
	)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) == 0 || !strings.HasPrefix(fields[0], "cpu") {
			continue
		}
		if fields[0] == "cpu" {
			aggregate, err = parseCPUTimes(fields)
			if err != nil {
				return CPUStats{}, err
			}
			found = true
			continue
		}
		// "cpu0", "cpu1", ... are the per-core lines.
		cores++
	}

	if !found {
		return CPUStats{}, fmt.Errorf("%w: no aggregate cpu line in /proc/stat", ErrMalformed)
	}

	now := c.now()
	stats := CPUStats{Cores: cores, Times: aggregate}

	c.mu.Lock()
	previous := c.lastCPU
	c.lastCPU = &cpuSample{times: aggregate, at: now}
	c.mu.Unlock()

	if previous == nil {
		stats.SampleWindow = "0s"
		return stats, nil
	}

	totalDelta := aggregate.Total() - previous.times.Total()
	if aggregate.Total() < previous.times.Total() || totalDelta == 0 {
		// Counters went backwards (a reset or a fixture replay) or no time has
		// passed. Reporting zero is honest; dividing would produce nonsense.
		stats.SampleWindow = now.Sub(previous.at).Truncate(time.Millisecond).String()
		return stats, nil
	}

	busyDelta := aggregate.Busy() - previous.times.Busy()
	stats.UsagePercent = percent(busyDelta, totalDelta)
	stats.UserPercent = percent(aggregate.User-previous.times.User, totalDelta)
	stats.SystemPercent = percent(aggregate.System-previous.times.System, totalDelta)
	stats.IOWaitPercent = percent(aggregate.IOWait-previous.times.IOWait, totalDelta)
	stats.IdlePercent = percent(aggregate.Idle-previous.times.Idle, totalDelta)
	stats.SampleWindow = now.Sub(previous.at).Truncate(time.Millisecond).String()

	return stats, nil
}

// parseCPUTimes reads the counters from a /proc/stat cpu line.
//
// Older kernels emit fewer fields, so trailing counters are optional rather
// than required; refusing to parse a short line would break on real hosts.
func parseCPUTimes(fields []string) (CPUTimes, error) {
	if len(fields) < 5 {
		return CPUTimes{}, fmt.Errorf("%w: cpu line has %d fields", ErrMalformed, len(fields))
	}

	values := make([]uint64, 0, 8)
	for _, field := range fields[1:] {
		if len(values) == 8 {
			break
		}
		value, err := parseUint(field)
		if err != nil {
			return CPUTimes{}, err
		}
		values = append(values, value)
	}
	for len(values) < 8 {
		values = append(values, 0)
	}

	return CPUTimes{
		User:    values[0],
		Nice:    values[1],
		System:  values[2],
		Idle:    values[3],
		IOWait:  values[4],
		IRQ:     values[5],
		SoftIRQ: values[6],
		Steal:   values[7],
	}, nil
}

// LoadStats is the system load average.
type LoadStats struct {
	Load1  float64 `json:"load_1"`
	Load5  float64 `json:"load_5"`
	Load15 float64 `json:"load_15"`
	// RunningProcs and TotalProcs come from the fourth field of /proc/loadavg.
	RunningProcs int `json:"running_processes"`
	TotalProcs   int `json:"total_processes"`
	Cores        int `json:"cores"`
	// LoadPerCore normalises Load1 by core count, which is what makes a load
	// figure comparable between machines.
	LoadPerCore float64 `json:"load_per_core"`
}

// Load reads /proc/loadavg.
func (c *Collector) Load() (LoadStats, error) {
	path, err := c.procPath("loadavg")
	if err != nil {
		return LoadStats{}, err
	}

	data, err := readFile(path)
	if err != nil {
		return LoadStats{}, fmt.Errorf("%w: /proc/loadavg is unreadable", ErrUnsupported)
	}

	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return LoadStats{}, fmt.Errorf("%w: /proc/loadavg has %d fields", ErrMalformed, len(fields))
	}

	var stats LoadStats
	if stats.Load1, err = parseFloat(fields[0]); err != nil {
		return LoadStats{}, err
	}
	if stats.Load5, err = parseFloat(fields[1]); err != nil {
		return LoadStats{}, err
	}
	if stats.Load15, err = parseFloat(fields[2]); err != nil {
		return LoadStats{}, err
	}

	// The fourth field is "running/total"; it is informational and absent on
	// some kernels, so a parse failure there is not fatal.
	if len(fields) >= 4 {
		if running, total, ok := strings.Cut(fields[3], "/"); ok {
			if v, err := parseUint(running); err == nil {
				stats.RunningProcs = int(v)
			}
			if v, err := parseUint(total); err == nil {
				stats.TotalProcs = int(v)
			}
		}
	}

	stats.Cores = c.coreCount()
	if stats.Cores > 0 {
		stats.LoadPerCore = round2(stats.Load1 / float64(stats.Cores))
	}
	return stats, nil
}

// coreCount counts the per-core lines in /proc/stat.
func (c *Collector) coreCount() int {
	path, err := c.procPath("stat")
	if err != nil {
		return 0
	}
	lines, err := readLines(path)
	if err != nil {
		return 0
	}

	cores := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "cpu") && fields[0] != "cpu" {
			cores++
		}
	}
	return cores
}
