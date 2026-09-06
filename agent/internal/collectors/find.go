package collectors

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// FindByName reports which of the named processes are running.
//
// It reads only each process's comm — the kernel's own name for it, at most 15
// characters — rather than building a full Process for every pid. A host runs
// hundreds of processes and this is asked for on every service listing.
//
// The lowest pid wins where several match, which for a service with workers is
// the master: nginx and PHP-FPM both fork children that share the parent's
// name, and the master is the one worth reporting.
//
// The panel's own web stack is excluded. It runs a second nginx and a second
// PHP-FPM master, and the kernel calls them "nginx" and "php-fpm84" like any
// other — so without this, stopping the nginx that serves websites left the
// panel reporting nginx as running while every site on the host was down. That
// is the worst kind of wrong answer: it is the one an operator checks first.
func (c *Collector) FindByName(names []string) map[string]int {
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name != "" {
			wanted[name] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return map[string]int{}
	}

	entries, err := os.ReadDir(c.procRoot)
	if err != nil {
		// An unreadable /proc is reported by the caller as "not running",
		// which is the same answer this gives and is honest: the Agent cannot
		// see the process table, so it cannot claim anything is up.
		return map[string]int{}
	}

	ours := c.panelStackPIDs(entries)

	found := make(map[string]int, len(wanted))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		comm, err := os.ReadFile(filepath.Join(c.procRoot, entry.Name(), "comm"))
		if err != nil {
			// The process exited between the listing and the read, which is
			// normal on a busy host.
			continue
		}

		name := strings.TrimSpace(string(comm))
		if _, ok := wanted[name]; !ok {
			continue
		}
		if _, mine := ours[pid]; mine {
			continue
		}
		if existing, seen := found[name]; !seen || pid < existing {
			found[name] = pid
		}
	}
	return found
}

// panelStackMarker appears in the command line of every master the panel starts
// for its own applications.
//
// It has to match the configuration root in agent/internal/panelweb. A trailing
// separator so a directory merely beginning with the same letters is not
// mistaken for it.
const panelStackMarker = "/etc/jothost/web/"

// panelStackPIDs returns the processes belonging to the panel's own web stack.
//
// Masters are found by their command line, which names the configuration file
// they were started with. Their children are found by parentage, because a
// worker's command line says only "nginx: worker process" - there is nothing in
// it to tell one instance's workers from another's, and leaving them in would
// make a stopped nginx look like a running one just as surely as the master
// would.
//
// One generation is enough: nginx workers and PHP-FPM pool processes are both
// direct children of their master.
func (c *Collector) panelStackPIDs(entries []os.DirEntry) map[int]struct{} {
	masters := make(map[int]struct{})

	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(c.procRoot, entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		// Arguments are NUL-separated; a plain contains would miss a marker
		// split across two of them.
		if strings.Contains(strings.ReplaceAll(string(cmdline), "\x00", " "), panelStackMarker) {
			masters[pid] = struct{}{}
		}
	}
	if len(masters) == 0 {
		return masters
	}

	ours := make(map[int]struct{}, len(masters)*4)
	for pid := range masters {
		ours[pid] = struct{}{}
	}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if _, mine := ours[pid]; mine {
			continue
		}
		if parent, ok := parentPID(filepath.Join(c.procRoot, entry.Name(), "stat")); ok {
			if _, fromUs := masters[parent]; fromUs {
				ours[pid] = struct{}{}
			}
		}
	}
	return ours
}

// parentPID reads the parent from a /proc/<pid>/stat line.
//
// The fields are positional and the second is the executable name in
// parentheses, which may itself contain spaces and parentheses - so the scan
// starts after the last ")" rather than splitting the whole line.
func parentPID(statPath string) (int, bool) {
	content, err := os.ReadFile(statPath)
	if err != nil {
		return 0, false
	}
	line := string(content)
	close := strings.LastIndex(line, ")")
	if close < 0 || close+1 >= len(line) {
		return 0, false
	}
	fields := strings.Fields(line[close+1:])
	// After the name come state and then ppid.
	if len(fields) < 2 {
		return 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return parent, true
}
