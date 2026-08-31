// Package logs reads the host's log files.
//
// The rule here is the one the service manager follows, for the same reason: a
// request names a **key from this catalogue, never a path**. The path it
// becomes is chosen in this file, from a table in this repository. A log viewer
// that accepted a path would be a file reader with no restrictions at all —
// "show me /etc/shadow" is a log request that happens to name a different file,
// and no amount of validating the string afterwards makes it safe to have
// asked.
//
// The paths themselves are still resolved through pathsec before anything is
// read (see read.go). That is not redundancy for its own sake: log files are
// frequently writable by the daemon that writes them, and a web server running
// as its own account replacing /var/log/nginx/access.log with a symlink to a
// private key would otherwise turn this page into a way to read it.
package logs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// Groups a source belongs to, which is how the page is arranged.
const (
	GroupWeb     = "web"
	GroupRuntime = "runtime"
	GroupSystem  = "system"
	GroupPanel   = "panel"
)

// Formats a log can be in. The format decides how a line's severity is read;
// see format.go.
const (
	FormatNginxAccess = "nginx-access"
	FormatNginxError  = "nginx-error"
	FormatPHP         = "php"
	FormatJSON        = "json"
	FormatAudit       = "audit"
	FormatSyslog      = "syslog"
	FormatPlain       = "plain"
)

// Source is one log this panel knows how to read.
type Source struct {
	// Key is what a request names. It never leaves this repository's
	// vocabulary.
	Key string `json:"key"`
	// Label and Summary are what an operator reads.
	Label   string `json:"label"`
	Summary string `json:"summary"`
	// Group arranges the picker.
	Group string `json:"group"`
	// Format decides how a line's level is read.
	Format string `json:"format"`
	// Paths are the candidates for this log, in order. Distributions disagree
	// about where things live — /var/log/syslog on Debian, /var/log/messages
	// on Alpine and RHEL — so each candidate is tried and the first that
	// exists is used. A table per distribution is a table that is wrong on the
	// distribution nobody tested.
	Paths []string `json:"-"`
	// Path is the candidate that exists on this host. Empty until detection
	// has run, and empty afterwards means this host does not have this log.
	Path string `json:"path,omitempty"`
}

// Errors returned by this package.
var (
	// ErrUnknownSource means the key is not in the catalogue. It is
	// deliberately the same answer for "invented" and "not on this host": a
	// caller learns nothing about the host from a key it guessed.
	ErrUnknownSource = errors.New("unknown log")
	// ErrNotPresent means the source is catalogued but this host has no such
	// file — a panel with no cron daemon has no cron log, which is a fact
	// about the host rather than a failure.
	ErrNotPresent = errors.New("this host does not have that log")
)

// Catalogue is the set of logs this panel reads on any host.
//
// Per-website logs are deliberately absent: a site's own access and error logs
// belong on that site's page, next to the site they describe, and Phase 4
// already serves them there. Adding them here as well would be two routes to
// one file, each with its own idea of who may read it.
func Catalogue() []Source {
	return []Source{
		{
			Key:     "nginx.access",
			Label:   "nginx access",
			Summary: "Every request the web server answered, across all sites.",
			Group:   GroupWeb,
			Format:  FormatNginxAccess,
			Paths:   []string{"/var/log/nginx/access.log"},
		},
		{
			Key:     "nginx.error",
			Label:   "nginx error",
			Summary: "What the web server could not do, and why.",
			Group:   GroupWeb,
			Format:  FormatNginxError,
			Paths:   []string{"/var/log/nginx/error.log"},
		},
		{
			Key:     "apache.error",
			Label:   "Apache error",
			Summary: "The hybrid backend's own errors, where a host runs one.",
			Group:   GroupWeb,
			Format:  FormatNginxError,
			Paths: []string{
				"/var/log/apache2/error.log",
				"/var/log/httpd/error_log",
			},
		},
		{
			Key:     "cron",
			Label:   "Cron",
			Summary: "Scheduled jobs, and what they printed.",
			Group:   GroupSystem,
			Format:  FormatSyslog,
			Paths: []string{
				"/var/log/cron",
				"/var/log/cron.log",
				"/var/log/crond.log",
			},
		},
		{
			Key:     "system",
			Label:   "System",
			Summary: "The machine's own log.",
			Group:   GroupSystem,
			Format:  FormatSyslog,
			Paths: []string{
				"/var/log/syslog",
				"/var/log/messages",
			},
		},
		{
			Key:     "security",
			Label:   "Security",
			Summary: "Authentication: who signed in to this machine, and who failed to.",
			Group:   GroupSystem,
			Format:  FormatSyslog,
			Paths: []string{
				"/var/log/auth.log",
				"/var/log/secure",
			},
		},
		{
			Key:     "agent",
			Label:   "Agent audit",
			Summary: "Every privileged operation the panel performed on this host.",
			Group:   GroupPanel,
			Format:  FormatAudit,
			Paths:   []string{"/var/log/jothost/agent-audit.log"},
		},
	}
}

// PHPSources contributes one source per installed PHP version.
//
// Which versions a host has is only knowable at runtime, so these are
// contributed by the caller that knows rather than written into the table
// above — the same arrangement the service catalogue uses for FPM units.
func PHPSources(versions []string) []Source {
	sources := make([]Source, 0, len(versions))
	for _, version := range versions {
		if validate.PHPVersion(version) != nil {
			continue
		}
		compact := validate.PHPVersionCompact(version)
		sources = append(sources, Source{
			Key:     "php." + version,
			Label:   "PHP-FPM " + version,
			Summary: "The FPM master for " + version + ": pool failures and worker crashes.",
			Group:   GroupRuntime,
			Format:  FormatPHP,
			Paths: []string{
				"/var/log/php" + compact + "/error.log",
				"/var/log/php-fpm" + compact + ".log",
				"/var/log/php/" + version + "-fpm.log",
				"/var/log/php" + version + "-fpm.log",
			},
		})
	}
	return sources
}

// NodeLogRoot is where the Agent's supervisor writes application output.
const NodeLogRoot = "/var/log/jothost/node"

// NodeSources contributes one pair of sources per Node application.
//
// The applications are found by reading the directory the Agent itself writes
// into, rather than being passed in from the panel's database. The difference
// matters when the two disagree: a directory here is output that exists on this
// host and can be shown, and a record in the database whose files were removed
// is a menu entry that leads to nothing.
func NodeSources() []Source {
	entries, err := os.ReadDir(NodeLogRoot)
	if err != nil {
		return nil
	}

	sources := make([]Source, 0, len(entries)*2)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// The name became a directory because it passed this check when the
		// application was deployed. It is checked again on the way out, because
		// what is on disk now is not necessarily what this Agent wrote.
		if err := validate.AppName(name); err != nil {
			continue
		}

		dir := filepath.Join(NodeLogRoot, name)
		sources = append(sources,
			Source{
				Key:     "node." + name + ".out",
				Label:   name + " (output)",
				Summary: "What the " + name + " application printed.",
				Group:   GroupRuntime,
				Format:  FormatPlain,
				Paths:   []string{filepath.Join(dir, "out.log")},
			},
			Source{
				Key:     "node." + name + ".error",
				Label:   name + " (errors)",
				Summary: "What the " + name + " application printed to standard error.",
				Group:   GroupRuntime,
				Format:  FormatPlain,
				Paths:   []string{filepath.Join(dir, "error.log")},
			},
		)
	}
	return sources
}

// CronLogRoot is where the Agent collects each scheduled job's output.
const CronLogRoot = "/var/log/jothost/cron"

// CronSources contributes one source per scheduled job.
//
// names maps a job's identifier to the name an operator gave it, because a
// picker listing "8f3c2b1a-4d5e-…" is a picker nobody can use. A job with no
// name in the map is one whose record the panel no longer has: its output is
// still shown, under its identifier, rather than hidden — a log with no job is
// how you find out a job was deleted while it was still failing.
//
// This is the extension point Phase 11 was built around: a later phase
// contributes sources rather than growing its own viewer.
func CronSources(names map[string]string) []Source {
	entries, err := os.ReadDir(CronLogRoot)
	if err != nil {
		return nil
	}

	sources := make([]Source, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		file := entry.Name()
		id := strings.TrimSuffix(file, ".log")
		if id == file {
			continue
		}
		// The identifier became a filename because it passed this check when
		// the job was written. It is checked again on the way out, because what
		// is on disk now is not necessarily what this Agent put there.
		if validate.UUID(id) != nil {
			continue
		}

		label := names[id]
		if label == "" {
			label = "Job " + id[:8]
		}
		sources = append(sources, Source{
			Key:     "cron." + id,
			Label:   label,
			Summary: "What this scheduled job printed, on each run.",
			Group:   GroupSystem,
			Format:  FormatPlain,
			Paths:   []string{filepath.Join(CronLogRoot, file)},
		})
	}
	return sources
}

// Lookup finds one source by key.
func Lookup(key string, extra []Source) (Source, error) {
	if !validKey(key) {
		return Source{}, fmt.Errorf("%w: %q", ErrUnknownSource, key)
	}
	for _, source := range append(Catalogue(), extra...) {
		if source.Key == key {
			return source, nil
		}
	}
	return Source{}, fmt.Errorf("%w: %q", ErrUnknownSource, key)
}

// validKey rejects anything that is not shaped like one of our keys.
//
// The lookup below would reject it anyway. This runs first so that a key which
// is a path traversal attempt, or is long enough to be an attack on something
// downstream, never reaches a comparison, a log line or an error message.
func validKey(key string) bool {
	if key == "" || len(key) > 96 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
		default:
			return false
		}
	}
	// A key is dot-separated segments, and an empty segment — the shape of
	// ".." and of "a..b" — is not one of ours.
	for _, segment := range strings.Split(key, ".") {
		if segment == "" {
			return false
		}
	}
	return true
}

// sortSources puts the list in the order an operator reads it: what serves the
// sites first, what runs their code next, then the machine, then the panel.
func sortSources(sources []Source) {
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].Group != sources[j].Group {
			return groupOrder(sources[i].Group) < groupOrder(sources[j].Group)
		}
		return sources[i].Key < sources[j].Key
	})
}

func groupOrder(group string) int {
	switch group {
	case GroupWeb:
		return 0
	case GroupRuntime:
		return 1
	case GroupSystem:
		return 2
	case GroupPanel:
		return 3
	default:
		return 4
	}
}
