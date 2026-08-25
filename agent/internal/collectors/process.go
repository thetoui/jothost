package collectors

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Process is one running process.
type Process struct {
	PID  int    `json:"pid"`
	PPID int    `json:"ppid"`
	Name string `json:"name"`
	// State is the single-letter kernel state: R, S, D, Z, T.
	State string `json:"state"`
	// User is the account name when it can be resolved, otherwise the numeric
	// UID rendered as a string.
	User string `json:"user"`
	UID  int    `json:"uid"`

	MemoryRSSBytes uint64  `json:"memory_rss_bytes"`
	MemoryPercent  float64 `json:"memory_percent"`
	// CPUTimeSeconds is cumulative CPU time, not a rate: a per-process usage
	// percentage needs two samples, which a one-shot listing cannot provide.
	CPUTimeSeconds float64 `json:"cpu_time_seconds"`
	Threads        int     `json:"threads"`
	// Command is the full command line, truncated. It is host data and is
	// returned verbatim, so callers must treat it as untrusted when rendering.
	Command string `json:"command"`
}

// ProcessListOptions filters a process listing.
type ProcessListOptions struct {
	// Limit caps how many processes are returned. Zero means DefaultProcessLimit.
	Limit int
	// SortBy is "memory" (default) or "cpu".
	SortBy string
}

// Process listing bounds. A host can have tens of thousands of processes, and
// serialising all of them would produce a response large enough to be a
// resource-exhaustion vector in itself.
const (
	DefaultProcessLimit = 50
	MaxProcessLimit     = 500
	maxCommandLength    = 512
)

// clockTicks is the kernel's USER_HZ. It is 100 on every supported Linux
// architecture; the syscall to read it needs cgo, which the Agent avoids.
const clockTicks = 100

// ProcessList returns the running processes, ordered by resource use.
func (c *Collector) ProcessList(opts ProcessListOptions) ([]Process, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultProcessLimit
	}
	if limit > MaxProcessLimit {
		limit = MaxProcessLimit
	}

	entries, err := os.ReadDir(c.procRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: /proc is unreadable", ErrUnsupported)
	}

	// Total memory is needed to express RSS as a percentage.
	var totalMemory uint64
	if memory, err := c.Memory(); err == nil {
		totalMemory = memory.TotalBytes
	}

	processes := make([]Process, 0, 128)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			// Non-numeric entries are the kernel's own /proc files.
			continue
		}

		process, err := c.readProcess(pid, totalMemory)
		if err != nil {
			// Processes exit while the directory is being walked; a vanished
			// PID is normal and must not fail the listing.
			continue
		}
		processes = append(processes, process)
	}

	sortProcesses(processes, opts.SortBy)
	if len(processes) > limit {
		processes = processes[:limit]
	}
	return processes, nil
}

// Process returns a single process by PID.
func (c *Collector) Process(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, fmt.Errorf("%w: pid must be positive", ErrNoSuchProcess)
	}

	var totalMemory uint64
	if memory, err := c.Memory(); err == nil {
		totalMemory = memory.TotalBytes
	}

	process, err := c.readProcess(pid, totalMemory)
	if err != nil {
		return Process{}, fmt.Errorf("%w: pid %d", ErrNoSuchProcess, pid)
	}
	return process, nil
}

// readProcess reads one /proc/<pid> entry.
func (c *Collector) readProcess(pid int, totalMemory uint64) (Process, error) {
	// The PID is rendered back to a string rather than used as a caller-
	// supplied component, so nothing but digits can reach the path.
	pidDir := strconv.Itoa(pid)

	statusPath, err := c.procPath(pidDir, "status")
	if err != nil {
		return Process{}, err
	}
	statusLines, err := readLines(statusPath)
	if err != nil {
		return Process{}, err
	}

	process := Process{PID: pid, UID: -1}
	for _, line := range statusLines {
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value := strings.TrimSpace(rest)

		switch key {
		case "Name":
			process.Name = value
		case "State":
			// "S (sleeping)" -> "S"
			if fields := strings.Fields(value); len(fields) > 0 {
				process.State = fields[0]
			}
		case "PPid":
			if v, err := strconv.Atoi(value); err == nil {
				process.PPID = v
			}
		case "Uid":
			// "real effective saved filesystem"; the real UID is first.
			if fields := strings.Fields(value); len(fields) > 0 {
				if v, err := strconv.Atoi(fields[0]); err == nil {
					process.UID = v
				}
			}
		case "VmRSS":
			if fields := strings.Fields(value); len(fields) > 0 {
				if v, err := parseUint(fields[0]); err == nil {
					process.MemoryRSSBytes = v * 1024
				}
			}
		case "Threads":
			if v, err := strconv.Atoi(value); err == nil {
				process.Threads = v
			}
		}
	}

	if process.Name == "" {
		return Process{}, fmt.Errorf("%w: no name for pid %d", ErrMalformed, pid)
	}

	process.MemoryPercent = percent(process.MemoryRSSBytes, totalMemory)
	process.User = c.resolveUser(process.UID)
	process.CPUTimeSeconds = c.readCPUTime(pidDir)
	process.Command = c.readCommand(pidDir, process.Name)

	return process, nil
}

// readCPUTime sums utime and stime from /proc/<pid>/stat.
func (c *Collector) readCPUTime(pidDir string) float64 {
	path, err := c.procPath(pidDir, "stat")
	if err != nil {
		return 0
	}
	data, err := readFile(path)
	if err != nil {
		return 0
	}

	// Field 2 is the executable name in parentheses and may itself contain
	// spaces and parentheses, so fields are counted from the last ')'.
	content := string(data)
	closing := strings.LastIndex(content, ")")
	if closing < 0 || closing+2 >= len(content) {
		return 0
	}

	fields := strings.Fields(content[closing+2:])
	// After the state field, utime is index 11 and stime index 12.
	if len(fields) < 13 {
		return 0
	}

	utime, err := parseUint(fields[11])
	if err != nil {
		return 0
	}
	stime, err := parseUint(fields[12])
	if err != nil {
		return 0
	}
	return round2(float64(utime+stime) / clockTicks)
}

// readCommand reads /proc/<pid>/cmdline, falling back to the process name.
func (c *Collector) readCommand(pidDir, fallback string) string {
	path, err := c.procPath(pidDir, "cmdline")
	if err != nil {
		return fallback
	}
	data, err := readFile(path)
	if err != nil || len(data) == 0 {
		// Kernel threads have an empty cmdline.
		return fallback
	}

	// Arguments are null-separated.
	command := strings.TrimRight(string(data), "\x00")
	command = strings.ReplaceAll(command, "\x00", " ")
	command = strings.TrimSpace(command)
	if command == "" {
		return fallback
	}
	if len(command) > maxCommandLength {
		command = command[:maxCommandLength] + "..."
	}
	return command
}

// resolveUser maps a UID to an account name.
//
// os/user's lookup needs cgo for NSS; parsing /etc/passwd covers the local
// accounts a hosting panel cares about and works in a static binary.
func (c *Collector) resolveUser(uid int) string {
	if uid < 0 {
		return ""
	}

	c.mu.Lock()
	if c.users == nil {
		c.users = loadPasswd()
	}
	name, ok := c.users[uid]
	c.mu.Unlock()

	if ok {
		return name
	}
	return strconv.Itoa(uid)
}

// loadPasswd parses /etc/passwd into a UID-to-name map.
func loadPasswd() map[int]string {
	users := make(map[int]string)

	lines, err := readLines("/etc/passwd")
	if err != nil {
		return users
	}
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		users[uid] = fields[0]
	}
	return users
}

// sortProcesses orders a listing by the requested resource.
func sortProcesses(processes []Process, sortBy string) {
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "cpu":
		sort.Slice(processes, func(i, j int) bool {
			if processes[i].CPUTimeSeconds != processes[j].CPUTimeSeconds {
				return processes[i].CPUTimeSeconds > processes[j].CPUTimeSeconds
			}
			return processes[i].PID < processes[j].PID
		})
	default:
		sort.Slice(processes, func(i, j int) bool {
			if processes[i].MemoryRSSBytes != processes[j].MemoryRSSBytes {
				return processes[i].MemoryRSSBytes > processes[j].MemoryRSSBytes
			}
			return processes[i].PID < processes[j].PID
		})
	}
}
