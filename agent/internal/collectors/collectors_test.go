package collectors

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixture writes a /proc tree and returns a Collector reading it.
//
// Parsers are checked against known bytes with exact expected values. A test
// that only asserts "a number came back" passes against a parser reading the
// wrong column, which is the mistake this whole package is prone to.
type fixture struct {
	root      string
	collector *Collector
	clock     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()

	f := &fixture{root: t.TempDir(), clock: time.Unix(1700000000, 0).UTC()}
	f.collector = New(Options{
		ProcRoot: f.root,
		Now:      func() time.Time { return f.clock },
	})
	return f
}

func (f *fixture) write(t *testing.T, name, content string) {
	t.Helper()

	path := filepath.Join(f.root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func (f *fixture) advance(d time.Duration) { f.clock = f.clock.Add(d) }

// -------------------------------------------------------------------- cpu

func TestCPUFirstSampleReportsNoUsage(t *testing.T) {
	f := newFixture(t)
	f.write(t, "stat", "cpu  1000 100 500 8000 200 0 50 0 0 0\ncpu0 500 50 250 4000 100 0 25 0 0 0\n")

	stats, err := f.collector.CPU()
	if err != nil {
		t.Fatalf("CPU: %v", err)
	}

	// There is no interval to compare against yet. Reporting a number here
	// would be a machine's lifetime average, which looks wrong on a graph.
	if stats.UsagePercent != 0 {
		t.Fatalf("first sample must report 0%% usage, got %v", stats.UsagePercent)
	}
	if stats.Cores != 1 {
		t.Fatalf("expected 1 core, got %d", stats.Cores)
	}
	if stats.Times.User != 1000 || stats.Times.Idle != 8000 {
		t.Fatalf("counters parsed incorrectly: %+v", stats.Times)
	}
	if stats.Times.Total() != 9850 {
		t.Fatalf("total = %d, want 9850", stats.Times.Total())
	}
}

func TestCPUUsageIsComputedFromTheDelta(t *testing.T) {
	f := newFixture(t)
	f.write(t, "stat", "cpu  1000 0 0 9000 0 0 0 0 0 0\ncpu0 1000 0 0 9000 0 0 0 0 0 0\n")

	if _, err := f.collector.CPU(); err != nil {
		t.Fatalf("first CPU sample: %v", err)
	}

	// 500 more busy jiffies and 500 more idle: exactly 50% over the interval.
	f.advance(time.Second)
	f.write(t, "stat", "cpu  1500 0 0 9500 0 0 0 0 0 0\ncpu0 1500 0 0 9500 0 0 0 0 0 0\n")

	stats, err := f.collector.CPU()
	if err != nil {
		t.Fatalf("second CPU sample: %v", err)
	}
	if stats.UsagePercent != 50 {
		t.Fatalf("usage = %v, want 50", stats.UsagePercent)
	}
	if stats.UserPercent != 50 {
		t.Fatalf("user = %v, want 50", stats.UserPercent)
	}
	if stats.IdlePercent != 50 {
		t.Fatalf("idle = %v, want 50", stats.IdlePercent)
	}
	if stats.SampleWindow != "1s" {
		t.Fatalf("sample window = %q, want 1s", stats.SampleWindow)
	}
}

func TestCPUIOWaitCountsAsIdle(t *testing.T) {
	f := newFixture(t)
	f.write(t, "stat", "cpu  0 0 0 0 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\n")
	if _, err := f.collector.CPU(); err != nil {
		t.Fatalf("first sample: %v", err)
	}

	// 100 jiffies of iowait and nothing else: the CPU was waiting, not busy.
	f.advance(time.Second)
	f.write(t, "stat", "cpu  0 0 0 0 100 0 0 0\ncpu0 0 0 0 0 100 0 0 0\n")

	stats, err := f.collector.CPU()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if stats.UsagePercent != 0 {
		t.Fatalf("iowait must not count as busy, got %v", stats.UsagePercent)
	}
	if stats.IOWaitPercent != 100 {
		t.Fatalf("iowait = %v, want 100", stats.IOWaitPercent)
	}
}

func TestCPUHandlesCounterReset(t *testing.T) {
	f := newFixture(t)
	f.write(t, "stat", "cpu  5000 0 0 5000 0 0 0 0\ncpu0 5000 0 0 5000 0 0 0 0\n")
	if _, err := f.collector.CPU(); err != nil {
		t.Fatalf("first sample: %v", err)
	}

	// Counters going backwards means a reset; a naive subtraction would
	// underflow uint64 and report an absurd percentage.
	f.advance(time.Second)
	f.write(t, "stat", "cpu  10 0 0 10 0 0 0 0\ncpu0 10 0 0 10 0 0 0 0\n")

	stats, err := f.collector.CPU()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	if stats.UsagePercent < 0 || stats.UsagePercent > 100 {
		t.Fatalf("usage must stay in range after a reset, got %v", stats.UsagePercent)
	}
}

func TestCPUShortLineIsAccepted(t *testing.T) {
	// Older kernels emit fewer counters; refusing them would break real hosts.
	f := newFixture(t)
	f.write(t, "stat", "cpu  100 10 20 300 40\ncpu0 100 10 20 300 40\n")

	stats, err := f.collector.CPU()
	if err != nil {
		t.Fatalf("CPU: %v", err)
	}
	if stats.Times.Steal != 0 {
		t.Fatalf("missing counters must default to zero, got %d", stats.Times.Steal)
	}
}

func TestCPUMissingProcIsUnsupported(t *testing.T) {
	f := newFixture(t)

	_, err := f.collector.CPU()
	if err == nil {
		t.Fatal("a missing /proc/stat must be reported")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
}

// ----------------------------------------------------------------- memory

func TestMemoryUsesAvailableNotFree(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemTotal:       16000 kB\n"+
		"MemFree:          1000 kB\n"+
		"MemAvailable:    12000 kB\n"+
		"Buffers:           500 kB\n"+
		"Cached:           9000 kB\n"+
		"SwapTotal:        4000 kB\n"+
		"SwapFree:         3000 kB\n")

	stats, err := f.collector.Memory()
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}

	if stats.TotalBytes != 16000*1024 {
		t.Fatalf("total = %d, want %d", stats.TotalBytes, 16000*1024)
	}
	// Used is total minus available (4000 kB), not total minus free (15000 kB).
	// Getting this wrong is what makes dashboards report a healthy Linux box
	// as nearly out of memory.
	if stats.UsedBytes != 4000*1024 {
		t.Fatalf("used = %d, want %d", stats.UsedBytes, 4000*1024)
	}
	if stats.UsedPercent != 25 {
		t.Fatalf("used percent = %v, want 25", stats.UsedPercent)
	}
	if stats.SwapUsedBytes != 1000*1024 {
		t.Fatalf("swap used = %d, want %d", stats.SwapUsedBytes, 1000*1024)
	}
	if stats.SwapUsedPercent != 25 {
		t.Fatalf("swap used percent = %v, want 25", stats.SwapUsedPercent)
	}
}

func TestMemoryFallsBackWithoutMemAvailable(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemTotal:       16000 kB\n"+
		"MemFree:          1000 kB\n"+
		"Buffers:          2000 kB\n"+
		"Cached:           5000 kB\n")

	stats, err := f.collector.Memory()
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	// Free + buffers + cached = 8000 kB available on a pre-3.14 kernel.
	if stats.AvailableBytes != 8000*1024 {
		t.Fatalf("available = %d, want %d", stats.AvailableBytes, 8000*1024)
	}
}

func TestMemoryWithoutSwapReportsZeroNotNaN(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")

	stats, err := f.collector.Memory()
	if err != nil {
		t.Fatalf("Memory: %v", err)
	}
	// A NaN would not survive JSON encoding and would break the response.
	if stats.SwapUsedPercent != 0 {
		t.Fatalf("swap percent with no swap = %v, want 0", stats.SwapUsedPercent)
	}
}

func TestMemoryRequiresMemTotal(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemFree: 100 kB\n")

	if _, err := f.collector.Memory(); err == nil {
		t.Fatal("meminfo without MemTotal must be rejected")
	}
}

// ------------------------------------------------------------------- load

func TestLoad(t *testing.T) {
	f := newFixture(t)
	f.write(t, "loadavg", "2.50 1.25 0.75 3/512 9999\n")
	f.write(t, "stat", "cpu 0 0 0 0\ncpu0 0 0 0 0\ncpu1 0 0 0 0\ncpu2 0 0 0 0\ncpu3 0 0 0 0\ncpu4 0 0 0 0\n")

	stats, err := f.collector.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if stats.Load1 != 2.50 || stats.Load5 != 1.25 || stats.Load15 != 0.75 {
		t.Fatalf("unexpected load values: %+v", stats)
	}
	if stats.RunningProcs != 3 || stats.TotalProcs != 512 {
		t.Fatalf("process counts parsed incorrectly: %+v", stats)
	}
	if stats.Cores != 5 {
		t.Fatalf("cores = %d, want 5", stats.Cores)
	}
	// Load per core is what makes a figure comparable between machines.
	if stats.LoadPerCore != 0.5 {
		t.Fatalf("load per core = %v, want 0.5", stats.LoadPerCore)
	}
}

// ---------------------------------------------------------------- network

func TestNetworkParsesCounters(t *testing.T) {
	f := newFixture(t)
	f.write(t, "net/dev", "Inter-|   Receive                    |  Transmit\n"+
		" face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n"+
		"    lo:  1000  10 0 0 0 0 0 0  1000  10 0 0 0 0 0 0\n"+
		"  eth0: 5000 40 1 2 0 0 0 0  2500 20 3 4 0 0 0 0\n")

	stats, err := f.collector.Network()
	if err != nil {
		t.Fatalf("Network: %v", err)
	}

	if len(stats.Interfaces) != 2 {
		t.Fatalf("expected 2 interfaces, got %d", len(stats.Interfaces))
	}
	// Sorted output keeps responses comparable between calls.
	if stats.Interfaces[0].Name != "eth0" || stats.Interfaces[1].Name != "lo" {
		t.Fatalf("interfaces must be sorted, got %+v", stats.Interfaces)
	}

	eth0 := stats.Interfaces[0]
	if eth0.RxBytes != 5000 || eth0.TxBytes != 2500 {
		t.Fatalf("byte counters parsed incorrectly: %+v", eth0)
	}
	if eth0.RxErrors != 1 || eth0.RxDropped != 2 || eth0.TxErrors != 3 || eth0.TxDropped != 4 {
		t.Fatalf("error counters parsed incorrectly: %+v", eth0)
	}

	// Loopback is excluded from totals or a quiet machine looks busy.
	if stats.TotalRxBytes != 5000 || stats.TotalTxBytes != 2500 {
		t.Fatalf("loopback must be excluded from totals: %+v", stats)
	}
}

func TestNetworkComputesRates(t *testing.T) {
	f := newFixture(t)
	f.write(t, "net/dev", "h1\nh2\n  eth0: 1000 0 0 0 0 0 0 0  500 0 0 0 0 0 0 0\n")
	if _, err := f.collector.Network(); err != nil {
		t.Fatalf("first sample: %v", err)
	}

	f.advance(2 * time.Second)
	f.write(t, "net/dev", "h1\nh2\n  eth0: 3000 0 0 0 0 0 0 0  1500 0 0 0 0 0 0 0\n")

	stats, err := f.collector.Network()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}

	// 2000 bytes over 2 seconds.
	if stats.Interfaces[0].RxBytesPerSecond != 1000 {
		t.Fatalf("rx rate = %v, want 1000", stats.Interfaces[0].RxBytesPerSecond)
	}
	if stats.Interfaces[0].TxBytesPerSecond != 500 {
		t.Fatalf("tx rate = %v, want 500", stats.Interfaces[0].TxBytesPerSecond)
	}
}

func TestNetworkHandlesCounterReset(t *testing.T) {
	f := newFixture(t)
	f.write(t, "net/dev", "h1\nh2\n  eth0: 9000 0 0 0 0 0 0 0  9000 0 0 0 0 0 0 0\n")
	if _, err := f.collector.Network(); err != nil {
		t.Fatalf("first sample: %v", err)
	}

	f.advance(time.Second)
	f.write(t, "net/dev", "h1\nh2\n  eth0: 10 0 0 0 0 0 0 0  10 0 0 0 0 0 0 0\n")

	stats, err := f.collector.Network()
	if err != nil {
		t.Fatalf("second sample: %v", err)
	}
	// A negative delta must report zero, not an underflowed spike.
	if stats.Interfaces[0].RxBytesPerSecond != 0 {
		t.Fatalf("a counter reset must report 0, got %v", stats.Interfaces[0].RxBytesPerSecond)
	}
}

func TestNetworkSkipsMalformedLines(t *testing.T) {
	f := newFixture(t)
	f.write(t, "net/dev", "h1\nh2\n  eth0: not a number\n  eth1: 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16\n")

	stats, err := f.collector.Network()
	if err != nil {
		t.Fatalf("Network: %v", err)
	}
	if len(stats.Interfaces) != 1 || stats.Interfaces[0].Name != "eth1" {
		t.Fatalf("malformed lines must be skipped, got %+v", stats.Interfaces)
	}
}

// ------------------------------------------------------------------ disk

func TestDiskParsesMountsAndSkipsVirtual(t *testing.T) {
	f := newFixture(t)
	// The temp dir is a real mount point statfs can answer for.
	f.write(t, "mounts", "/dev/sda1 "+f.root+" ext4 rw,relatime 0 0\n"+
		"proc /proc proc rw,nosuid 0 0\n"+
		"sysfs /sys sysfs rw 0 0\n")

	stats, err := f.collector.Disk("")
	if err != nil {
		t.Fatalf("Disk: %v", err)
	}

	for _, fs := range stats.Filesystems {
		if fs.Type == "proc" || fs.Type == "sysfs" {
			t.Fatalf("virtual filesystem %q must be excluded", fs.Type)
		}
	}
	if len(stats.Filesystems) != 1 {
		t.Fatalf("expected exactly the real filesystem, got %+v", stats.Filesystems)
	}
	if stats.Filesystems[0].TotalBytes == 0 {
		t.Fatal("expected a non-zero capacity from statfs")
	}
}

func TestDiskReportsUnmountedPath(t *testing.T) {
	f := newFixture(t)
	f.write(t, "mounts", "/dev/sda1 "+f.root+" ext4 rw 0 0\n")

	_, err := f.collector.Disk("/definitely/not/mounted")
	if err == nil {
		t.Fatal("an unmounted path must be reported")
	}
	if !strings.Contains(err.Error(), "not a mounted filesystem") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDiskReadOnlyFlag(t *testing.T) {
	f := newFixture(t)
	f.write(t, "mounts", "/dev/sda1 "+f.root+" ext4 ro,relatime 0 0\n")

	stats, err := f.collector.Disk("")
	if err != nil {
		t.Fatalf("Disk: %v", err)
	}
	if len(stats.Filesystems) != 1 || !stats.Filesystems[0].ReadOnly {
		t.Fatalf("expected a read-only filesystem, got %+v", stats.Filesystems)
	}
}

func TestUnescapeMountDecodesOctal(t *testing.T) {
	// The kernel escapes spaces as \040; a mount point with a space would
	// otherwise be truncated at the space by field splitting.
	if got := unescapeMount(`/mnt/my\040drive`); got != "/mnt/my drive" {
		t.Fatalf("unescapeMount = %q, want %q", got, "/mnt/my drive")
	}
	if got := unescapeMount("/mnt/plain"); got != "/mnt/plain" {
		t.Fatalf("unescapeMount = %q, want unchanged", got)
	}
	// An invalid escape is left alone rather than mangled.
	if got := unescapeMount(`/mnt/a\99b`); got != `/mnt/a\99b` {
		t.Fatalf("unescapeMount = %q, want unchanged", got)
	}
}

// -------------------------------------------------------------- processes

func writeProcess(t *testing.T, f *fixture, pid, rssKB int, name string) {
	t.Helper()

	f.write(t, pid8(pid)+"/status",
		"Name:\t"+name+"\nState:\tS (sleeping)\nPPid:\t1\nUid:\t1000\t1000\t1000\t1000\n"+
			"VmRSS:\t"+itoa(rssKB)+" kB\nThreads:\t2\n")
	f.write(t, pid8(pid)+"/stat",
		itoa(pid)+" ("+name+") S 1 1 1 0 -1 0 0 0 0 0 200 100 0 0 20 0 2 0 100 0 0 0 0\n")
	f.write(t, pid8(pid)+"/cmdline", name+"\x00--flag\x00")
}

func pid8(pid int) string { return itoa(pid) }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	digits := ""
	for v > 0 {
		digits = string(rune('0'+v%10)) + digits
		v /= 10
	}
	return digits
}

func TestProcessListSortsAndLimits(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemTotal: 1000000 kB\nMemAvailable: 500000 kB\n")

	writeProcess(t, f, 100, 1000, "small")
	writeProcess(t, f, 200, 5000, "large")
	writeProcess(t, f, 300, 3000, "medium")

	processes, err := f.collector.ProcessList(ProcessListOptions{})
	if err != nil {
		t.Fatalf("ProcessList: %v", err)
	}
	if len(processes) != 3 {
		t.Fatalf("expected 3 processes, got %d", len(processes))
	}
	// Default ordering is by memory, descending.
	if processes[0].Name != "large" || processes[2].Name != "small" {
		t.Fatalf("unexpected ordering: %+v", processes)
	}

	limited, err := f.collector.ProcessList(ProcessListOptions{Limit: 2})
	if err != nil {
		t.Fatalf("ProcessList: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("limit not applied: got %d", len(limited))
	}
}

func TestProcessListCapsTheLimit(t *testing.T) {
	f := newFixture(t)
	writeProcess(t, f, 100, 1000, "one")

	// An unbounded listing would itself be a resource-exhaustion vector.
	processes, err := f.collector.ProcessList(ProcessListOptions{Limit: 10000})
	if err != nil {
		t.Fatalf("ProcessList: %v", err)
	}
	if len(processes) > MaxProcessLimit {
		t.Fatalf("limit must be capped at %d, got %d", MaxProcessLimit, len(processes))
	}
}

func TestProcessFields(t *testing.T) {
	f := newFixture(t)
	f.write(t, "meminfo", "MemTotal: 1000000 kB\nMemAvailable: 500000 kB\n")
	writeProcess(t, f, 4321, 10000, "nginx")

	process, err := f.collector.Process(4321)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if process.PID != 4321 || process.PPID != 1 || process.Name != "nginx" {
		t.Fatalf("unexpected process: %+v", process)
	}
	if process.State != "S" {
		t.Fatalf("state = %q, want S", process.State)
	}
	if process.MemoryRSSBytes != 10000*1024 {
		t.Fatalf("rss = %d, want %d", process.MemoryRSSBytes, 10000*1024)
	}
	if process.MemoryPercent != 1 {
		t.Fatalf("memory percent = %v, want 1", process.MemoryPercent)
	}
	// utime 200 + stime 100 jiffies at 100 Hz = 3 seconds.
	if process.CPUTimeSeconds != 3 {
		t.Fatalf("cpu time = %v, want 3", process.CPUTimeSeconds)
	}
	if process.Threads != 2 {
		t.Fatalf("threads = %d, want 2", process.Threads)
	}
	if process.Command != "nginx --flag" {
		t.Fatalf("command = %q, want %q", process.Command, "nginx --flag")
	}
}

func TestProcessNameWithSpacesIsParsed(t *testing.T) {
	// The comm field can contain spaces and parentheses, which breaks naive
	// field splitting of /proc/<pid>/stat.
	f := newFixture(t)
	f.write(t, "1/status", "Name:\tmy proc\nState:\tR (running)\nPPid:\t0\nUid:\t0\t0\t0\t0\nVmRSS:\t100 kB\nThreads:\t1\n")
	f.write(t, "1/stat", "1 (my (weird) proc) R 0 1 1 0 -1 0 0 0 0 0 500 500 0 0 20 0 1 0 100 0 0 0 0\n")
	f.write(t, "1/cmdline", "")

	process, err := f.collector.Process(1)
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	// utime 500 + stime 500 at 100 Hz = 10 seconds.
	if process.CPUTimeSeconds != 10 {
		t.Fatalf("cpu time = %v, want 10", process.CPUTimeSeconds)
	}
	// An empty cmdline (kernel thread) falls back to the name.
	if process.Command != "my proc" {
		t.Fatalf("command = %q, want the process name", process.Command)
	}
}

func TestProcessRejectsInvalidPID(t *testing.T) {
	f := newFixture(t)

	for _, pid := range []int{0, -1, -9999} {
		if _, err := f.collector.Process(pid); err == nil {
			t.Fatalf("pid %d must be rejected", pid)
		}
	}
	if _, err := f.collector.Process(999999); err == nil {
		t.Fatal("a missing pid must be reported")
	}
}

// ----------------------------------------------------------------- system

func TestSystemInfo(t *testing.T) {
	f := newFixture(t)
	f.write(t, "uptime", "3600.50 1000.00\n")
	f.write(t, "stat", "cpu 0 0 0 0\ncpu0 0 0 0 0\ncpu1 0 0 0 0\n")
	f.write(t, "sys/kernel/osrelease", "6.1.0-18-amd64\n")

	releasePath := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(releasePath, []byte("NAME=\"Debian GNU/Linux\"\nVERSION_ID=\"12\"\nID=debian\n"), 0o600); err != nil {
		t.Fatalf("write os-release: %v", err)
	}
	original := osReleasePath
	osReleasePath = releasePath
	t.Cleanup(func() { osReleasePath = original })

	info, err := f.collector.System()
	if err != nil {
		t.Fatalf("System: %v", err)
	}

	if info.UptimeSeconds != 3600 {
		t.Fatalf("uptime = %d, want 3600", info.UptimeSeconds)
	}
	if info.KernelVersion != "6.1.0-18-amd64" {
		t.Fatalf("kernel = %q", info.KernelVersion)
	}
	// The quotes are not part of the value.
	if info.OSName != "Debian GNU/Linux" || info.OSVersion != "12" {
		t.Fatalf("os = %q %q", info.OSName, info.OSVersion)
	}
	if info.Cores != 2 {
		t.Fatalf("cores = %d, want 2", info.Cores)
	}
	// Boot time is the injected clock (2023-11-14T22:13:20Z) minus the full
	// 3600.5 second uptime, including the fractional part.
	if info.BootTime != "2023-11-14T21:13:19Z" {
		t.Fatalf("boot time = %q, want 2023-11-14T21:13:19Z", info.BootTime)
	}
}

// ------------------------------------------------------------- path safety

func TestSafeJoinRejectsEscapes(t *testing.T) {
	// A PID or interface name reaching a path must never walk out of /proc.
	for _, component := range []string{"..", "../etc", "a/b", "", "a\x00b"} {
		if _, err := safeJoin("/proc", component); err == nil {
			t.Fatalf("component %q must be rejected", component)
		}
	}

	joined, err := safeJoin("/proc", "1234", "status")
	if err != nil {
		t.Fatalf("safeJoin: %v", err)
	}
	if joined != "/proc/1234/status" {
		t.Fatalf("safeJoin = %q", joined)
	}
}

func TestAvailableReportsMissingProc(t *testing.T) {
	f := newFixture(t)
	if f.collector.Available() {
		t.Fatal("an empty /proc must not report available")
	}

	f.write(t, "stat", "cpu 0 0 0 0\n")
	if !f.collector.Available() {
		t.Fatal("a readable /proc/stat must report available")
	}
}

func TestConcurrentCollectionIsSafe(t *testing.T) {
	f := newFixture(t)
	f.write(t, "stat", "cpu 100 0 0 900 0 0 0 0\ncpu0 100 0 0 900 0 0 0 0\n")
	f.write(t, "meminfo", "MemTotal: 1000 kB\nMemAvailable: 500 kB\n")
	f.write(t, "net/dev", "h1\nh2\n  eth0: 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16\n")

	// The Agent serves several connections at once and the rate calculations
	// keep shared state, so this must not race.
	done := make(chan struct{}, 30)
	for i := 0; i < 30; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _ = f.collector.CPU()
			_, _ = f.collector.Memory()
			_, _ = f.collector.Network()
		}()
	}
	for i := 0; i < 30; i++ {
		<-done
	}
}
