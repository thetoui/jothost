package collectors

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// SystemInfo describes the host the Agent runs on.
type SystemInfo struct {
	Hostname      string `json:"hostname"`
	OSName        string `json:"os_name"`
	OSVersion     string `json:"os_version"`
	KernelVersion string `json:"kernel_version"`
	Architecture  string `json:"architecture"`
	UptimeSeconds uint64 `json:"uptime_seconds"`
	BootTime      string `json:"boot_time"`
	Cores         int    `json:"cores"`
}

// System reads the host's identity and uptime.
//
// Nothing here shells out to uname or lsb_release: every field comes from a
// kernel file or the Go runtime, so there is no program to execute and no
// output format to break.
func (c *Collector) System() (SystemInfo, error) {
	info := SystemInfo{
		Architecture: runtime.GOARCH,
		Cores:        c.coreCount(),
	}

	if hostname, err := os.Hostname(); err == nil {
		info.Hostname = hostname
	}

	uptime, bootTime, err := c.uptime()
	if err != nil {
		return SystemInfo{}, err
	}
	info.UptimeSeconds = uptime
	info.BootTime = bootTime.UTC().Format(time.RFC3339)

	info.KernelVersion = c.kernelVersion()
	info.OSName, info.OSVersion = c.osRelease()

	return info, nil
}

// uptime reads /proc/uptime and derives the boot time.
func (c *Collector) uptime() (uint64, time.Time, error) {
	path, err := c.procPath("uptime")
	if err != nil {
		return 0, time.Time{}, err
	}

	data, err := readFile(path)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("%w: /proc/uptime is unreadable", ErrUnsupported)
	}

	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, time.Time{}, fmt.Errorf("%w: /proc/uptime is empty", ErrMalformed)
	}

	seconds, err := parseFloat(fields[0])
	if err != nil {
		return 0, time.Time{}, err
	}

	// time.Duration(seconds) would truncate the fractional part to whole
	// nanoseconds before the multiply, discarding it entirely.
	uptime := uint64(seconds)
	return uptime, c.now().Add(-time.Duration(seconds * float64(time.Second))), nil
}

// kernelVersion reads /proc/sys/kernel/osrelease.
func (c *Collector) kernelVersion() string {
	path, err := c.procPath("sys", "kernel", "osrelease")
	if err != nil {
		return ""
	}
	data, err := readFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// osReleasePath is the distribution identity file. It lives outside /proc, so
// it is a package variable rather than a Collector root: tests override it.
var osReleasePath = "/etc/os-release"

// osRelease parses /etc/os-release for the distribution name and version.
func (c *Collector) osRelease() (name, version string) {
	lines, err := readLines(osReleasePath)
	if err != nil {
		return runtime.GOOS, ""
	}

	values := make(map[string]string, 8)
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		// Values may be quoted; the quotes are not part of the value.
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}

	name = values["NAME"]
	if name == "" {
		name = values["ID"]
	}
	if name == "" {
		name = runtime.GOOS
	}

	version = values["VERSION_ID"]
	if version == "" {
		version = values["VERSION"]
	}
	return name, version
}
