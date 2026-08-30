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
		if existing, seen := found[name]; !seen || pid < existing {
			found[name] = pid
		}
	}
	return found
}
