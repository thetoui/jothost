package collectors

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// NetworkInterface is one interface's counters and rates.
type NetworkInterface struct {
	Name string `json:"name"`

	RxBytes   uint64 `json:"rx_bytes"`
	RxPackets uint64 `json:"rx_packets"`
	RxErrors  uint64 `json:"rx_errors"`
	RxDropped uint64 `json:"rx_dropped"`

	TxBytes   uint64 `json:"tx_bytes"`
	TxPackets uint64 `json:"tx_packets"`
	TxErrors  uint64 `json:"tx_errors"`
	TxDropped uint64 `json:"tx_dropped"`

	// Rates are computed against the previous sample and are zero on the
	// first call, when there is no interval to divide by.
	RxBytesPerSecond float64 `json:"rx_bytes_per_second"`
	TxBytesPerSecond float64 `json:"tx_bytes_per_second"`
}

// NetworkStats is a network sample across all interfaces.
type NetworkStats struct {
	Interfaces []NetworkInterface `json:"interfaces"`
	// Totals exclude loopback, which would otherwise double-count local
	// traffic and make a quiet machine look busy.
	TotalRxBytes uint64 `json:"total_rx_bytes"`
	TotalTxBytes uint64 `json:"total_tx_bytes"`
	SampleWindow string `json:"sample_window"`
}

// networkSample stores counters for rate calculation.
type networkSample struct {
	byInterface map[string]NetworkInterface
	at          time.Time
}

// loopbackPrefix identifies interfaces excluded from totals.
const loopbackPrefix = "lo"

// Network reads /proc/net/dev.
func (c *Collector) Network() (NetworkStats, error) {
	path, err := c.procPath("net", "dev")
	if err != nil {
		return NetworkStats{}, err
	}

	lines, err := readLines(path)
	if err != nil {
		return NetworkStats{}, fmt.Errorf("%w: /proc/net/dev is unreadable", ErrUnsupported)
	}

	current := make(map[string]NetworkInterface, len(lines))
	for _, line := range lines {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			// The first two lines are headers and carry no colon.
			continue
		}

		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		fields := strings.Fields(rest)
		// The kernel emits 8 receive then 8 transmit counters.
		if len(fields) < 16 {
			continue
		}

		values := make([]uint64, 16)
		malformed := false
		for i := 0; i < 16; i++ {
			value, err := parseUint(fields[i])
			if err != nil {
				malformed = true
				break
			}
			values[i] = value
		}
		if malformed {
			continue
		}

		current[name] = NetworkInterface{
			Name:      name,
			RxBytes:   values[0],
			RxPackets: values[1],
			RxErrors:  values[2],
			RxDropped: values[3],
			TxBytes:   values[8],
			TxPackets: values[9],
			TxErrors:  values[10],
			TxDropped: values[11],
		}
	}

	if len(current) == 0 {
		return NetworkStats{}, fmt.Errorf("%w: no interfaces in /proc/net/dev", ErrMalformed)
	}

	now := c.now()

	c.mu.Lock()
	previous := c.lastNetwork
	c.lastNetwork = &networkSample{byInterface: current, at: now}
	c.mu.Unlock()

	stats := NetworkStats{SampleWindow: "0s"}
	var elapsed float64
	if previous != nil {
		elapsed = now.Sub(previous.at).Seconds()
		stats.SampleWindow = now.Sub(previous.at).Truncate(time.Millisecond).String()
	}

	names := make([]string, 0, len(current))
	for name := range current {
		names = append(names, name)
	}
	// Stable ordering keeps responses comparable between calls.
	sort.Strings(names)

	for _, name := range names {
		iface := current[name]

		if previous != nil && elapsed > 0 {
			if before, ok := previous.byInterface[name]; ok {
				// Counters can wrap or reset when an interface is recreated;
				// a negative delta is reported as zero rather than as a
				// nonsensical spike.
				if iface.RxBytes >= before.RxBytes {
					iface.RxBytesPerSecond = round2(float64(iface.RxBytes-before.RxBytes) / elapsed)
				}
				if iface.TxBytes >= before.TxBytes {
					iface.TxBytesPerSecond = round2(float64(iface.TxBytes-before.TxBytes) / elapsed)
				}
			}
		}

		if !isLoopback(name) {
			stats.TotalRxBytes += iface.RxBytes
			stats.TotalTxBytes += iface.TxBytes
		}
		stats.Interfaces = append(stats.Interfaces, iface)
	}

	return stats, nil
}

func isLoopback(name string) bool {
	return name == loopbackPrefix || strings.HasPrefix(name, loopbackPrefix+":")
}
