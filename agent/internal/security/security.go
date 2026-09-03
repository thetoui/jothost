// Package security answers the two questions about a host that nothing else in
// this panel can answer: what is listening, and what is writable.
//
// # Why only two
//
// The Security Center scans seven things. Five of them — the SSH configuration,
// the firewall, the certificates, the outstanding updates, the intrusion
// prevention — are already read by the phases that manage them, and the panel
// asks those rather than looking again. A second probe of the same thing is a
// second answer that can disagree with the first, and a panel whose security
// page contradicts its SSH page is worse than one with no security page.
//
// The two here are genuinely new. Nothing else in this panel enumerates
// listening sockets, and nothing else walks a site's files looking for what is
// world-writable. Both need to run on the host, so both are the Agent's.
//
// # What these read, and what they never do
//
// Everything comes from /proc and from stat(2). No program is executed: there
// is no ss, no lsof, no netstat and no find in this package. That is not
// austerity, it is the point — a scanner that shelled out would be the one
// place in the Agent where a hostile filename reached a command line.
//
// Nothing here changes anything. A scanner that could write is a scanner that
// can be talked into writing.
package security

import (
	"errors"
	"log/slog"
)

// Errors a caller can act on.
var (
	// ErrUnsupported means this host cannot answer, almost always because
	// /proc is not readable.
	ErrUnsupported = errors.New("this host cannot be scanned")
)

// Bounds on what a scan will look at.
//
// A scanner is walking a filesystem somebody else controls. Without a ceiling,
// a site with a million files in it turns a security scan into an outage — and
// the scan is supposed to be the thing that prevents those.
const (
	// MaxScannedEntries bounds how many filesystem entries one scan visits.
	MaxScannedEntries = 400_000
	// MaxReportedIssues bounds how many problems of one kind are listed. The
	// count is still reported in full; only the examples are capped, because
	// an operator reading "48,000 world-writable files" does not need all
	// forty-eight thousand names to know what to do.
	MaxReportedIssues = 50
	// MaxScanDepth bounds directory recursion, which stops a symlink loop or a
	// pathologically nested tree from running until the timeout.
	MaxScanDepth = 24
)

// Options configure a Scanner.
type Options struct {
	Log *slog.Logger
	// ProcRoot is where /proc is mounted. It is injectable so the socket
	// parsers can be tested against captured kernel output rather than against
	// whatever this machine happens to be running.
	ProcRoot string
	// Roots are the directories the permission scan walks. They are the
	// Agent's own configuration, never a value from a request: a scanner that
	// took a path from a caller would be a way to enumerate the filesystem.
	Roots []string
}

// Scanner reads the host's security posture.
type Scanner struct {
	log      *slog.Logger
	procRoot string
	roots    []string
}

// NewScanner builds a Scanner.
func NewScanner(opts Options) *Scanner {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	procRoot := opts.ProcRoot
	if procRoot == "" {
		procRoot = "/proc"
	}
	return &Scanner{
		log:      log,
		procRoot: procRoot,
		roots:    append([]string(nil), opts.Roots...),
	}
}
