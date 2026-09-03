package collectors

import (
	"fmt"
	"net"
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
	// IPv4 and IPv6 are the addresses this host answers on.
	//
	// Added for Phase 13, which needs them for a reason nothing before it did:
	// a DNS zone whose name servers are inside it needs an address record for
	// them, and named refuses to load a zone that has none. The columns for
	// these have existed in the servers table since Phase 3 and nothing ever
	// filled them.
	IPv4 string `json:"ipv4"`
	IPv6 string `json:"ipv6"`
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
	info.IPv4, info.IPv6 = c.addresses()

	return info, nil
}

// addresses reports the host's first global unicast address of each family.
//
// Through the kernel's own interface list rather than by running `ip addr`,
// which is this file's rule: there is no program to execute and no output
// format to break.
//
// "First" is a real limitation and is the honest one. A host with several
// public addresses has no way to say which is canonical — that is a policy
// question, not a fact about the machine — so what is reported is the first the
// kernel lists, and an operator whose zone should name a different one edits
// the record. Loopback and link-local addresses are skipped: a name server
// whose A record is 127.0.0.1 is one nobody outside the machine can use.
func (c *Collector) addresses() (string, string) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", ""
	}

	v4, v6 := "", ""
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			network, ok := addr.(*net.IPNet)
			if !ok || !network.IP.IsGlobalUnicast() {
				continue
			}
			if network.IP.To4() != nil {
				if v4 == "" {
					v4 = network.IP.String()
				}
				continue
			}
			if v6 == "" {
				v6 = network.IP.String()
			}
		}
	}
	return v4, v6
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
