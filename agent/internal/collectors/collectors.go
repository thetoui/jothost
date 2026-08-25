// Package collectors reads host metrics from the Linux virtual filesystems.
//
// Everything here is read-only and parses /proc and /sys directly rather than
// shelling out to ps, free, or df. Parsing a kernel-stable file format is both
// faster and safer than executing a program and scraping its human-readable
// output, which changes between distributions and locales.
//
// The filesystem roots are configurable so the parsers can be tested against
// fixture trees rather than whatever the build machine happens to look like.
package collectors

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Errors returned by collectors.
var (
	// ErrUnsupported means the host does not expose the data, for example
	// /proc on a non-Linux system.
	ErrUnsupported = errors.New("metric is not available on this host")
	ErrMalformed   = errors.New("kernel file has an unexpected format")
	// ErrNotMounted means the requested mount point is not in the mount table.
	ErrNotMounted = errors.New("path is not a mounted filesystem")
	// ErrNoSuchProcess means the requested PID has no /proc entry.
	ErrNoSuchProcess = errors.New("process does not exist")
)

// maxFileSize bounds a single /proc read. These files are small; a larger one
// means something is wrong and reading it unbounded would risk the daemon.
const maxFileSize = 8 << 20 // 8 MiB

// Collector reads metrics from a set of filesystem roots.
//
// It is safe for concurrent use: the Agent serves several connections at once,
// and the rate calculations below keep state between calls.
type Collector struct {
	procRoot string
	sysRoot  string

	// now is injectable so rate calculations can be tested deterministically.
	now func() time.Time

	// mu guards the previous samples used to turn the kernel's cumulative
	// counters into rates.
	mu          sync.Mutex
	lastCPU     *cpuSample
	lastNetwork *networkSample
	// users caches /etc/passwd so a process listing does not re-read it once
	// per process.
	users map[int]string
}

// Options configures a Collector.
type Options struct {
	// ProcRoot defaults to /proc.
	ProcRoot string
	// SysRoot defaults to /sys.
	SysRoot string
	// Now defaults to time.Now.
	Now func() time.Time
}

// New builds a Collector.
func New(opts Options) *Collector {
	procRoot := opts.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	sysRoot := opts.SysRoot
	if sysRoot == "" {
		sysRoot = "/sys"
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Collector{
		procRoot: filepath.Clean(procRoot),
		sysRoot:  filepath.Clean(sysRoot),
		now:      now,
	}
}

// ProcRoot returns the configured /proc location.
func (c *Collector) ProcRoot() string { return c.procRoot }

// Available reports whether the configured /proc root looks usable, so the
// Agent can refuse to advertise metrics it cannot actually produce.
func (c *Collector) Available() bool {
	info, err := os.Stat(filepath.Join(c.procRoot, "stat"))
	return err == nil && info.Mode().IsRegular()
}

// procPath joins a path under the /proc root.
//
// Components are joined rather than concatenated, and the result is checked to
// still be under the root, so a caller-derived component such as a PID cannot
// walk out of /proc.
func (c *Collector) procPath(parts ...string) (string, error) {
	return safeJoin(c.procRoot, parts...)
}

func (c *Collector) sysPath(parts ...string) (string, error) {
	return safeJoin(c.sysRoot, parts...)
}

func safeJoin(root string, parts ...string) (string, error) {
	for _, part := range parts {
		if part == "" || strings.ContainsAny(part, "/\x00") || part == ".." {
			return "", fmt.Errorf("invalid path component %q", part)
		}
	}
	joined := filepath.Join(append([]string{root}, parts...)...)
	if joined != root && !strings.HasPrefix(joined, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return joined, nil
}

// readFile reads a kernel file with a size cap.
func readFile(path string) ([]byte, error) {
	file, err := os.Open(path) //nolint:gosec // path is built by safeJoin from a fixed root
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	// /proc files report size 0, so the cap is applied by the reader rather
	// than by trusting a stat.
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", filepath.Base(path), err)
	}
	return data, nil
}

// readLines reads a kernel file and splits it into lines.
func readLines(path string) ([]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}

	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", filepath.Base(path), err)
	}
	return lines, nil
}

// parseUint parses an unsigned kernel value.
func parseUint(value string) (uint64, error) {
	n, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not an unsigned integer", ErrMalformed, value)
	}
	return n, nil
}

// parseFloat parses a floating-point kernel value.
func parseFloat(value string) (float64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %q is not a number", ErrMalformed, value)
	}
	return f, nil
}

// percent computes part/whole as a percentage, rounded to two decimals.
//
// A zero denominator returns zero rather than NaN: NaN does not survive JSON
// encoding and would surface as a broken response rather than a sensible one.
func percent(part, whole uint64) float64 {
	if whole == 0 {
		return 0
	}
	return round2(float64(part) / float64(whole) * 100)
}

// round2 rounds to two decimal places.
func round2(value float64) float64 {
	rounded, err := strconv.ParseFloat(strconv.FormatFloat(value, 'f', 2, 64), 64)
	if err != nil {
		return value
	}
	return rounded
}
