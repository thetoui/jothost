package security

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// What is writable, and what is exposed, under the directories this panel owns.
//
// # The four things worth walking a filesystem for
//
// A world-writable file in a document root is a way for any account on the box
// — including one belonging to another customer's site — to replace the code
// that runs as the first customer. It is the single most common way one
// compromised site on a shared host becomes all of them.
//
// A setuid binary under a site root has no legitimate reason to exist. A web
// application does not need one, and one appearing there is either a mistake or
// the second stage of an intrusion.
//
// A .git directory or a .env file inside a *document root* is served to anyone
// who asks for it. .env holds the database password by convention; .git holds
// every version of the source, including the credentials somebody committed
// and then removed.
//
// A private key readable by anyone but its owner is a private key that is no
// longer private.
//
// # What this deliberately does not do
//
// It does not walk the whole filesystem. The roots are the Agent's own
// configuration and are always the directories this panel put things in.
// Scanning /etc or /usr would be a much longer list of things this panel did
// not create and cannot fix, which is a scanner nobody reads.
//
// It executes nothing and changes nothing.

// Issue is one thing found wrong with a file.
type Issue struct {
	// Kind is the machine-readable category, matching the constants below.
	Kind string `json:"kind"`
	Path string `json:"path"`
	// Mode is the permission bits, rendered the way an operator reads them.
	Mode string `json:"mode"`
	// Detail says what specifically is wrong, when the kind alone is not
	// enough to act on.
	Detail string `json:"detail,omitempty"`
}

// The kinds of issue a permission scan reports.
const (
	// IssueWorldWritable is a file or directory anyone on the host may modify.
	IssueWorldWritable = "world_writable"
	// IssueSetuid is a setuid or setgid executable under a site root.
	IssueSetuid = "setuid"
	// IssueExposedVCS is a version control directory inside a document root.
	IssueExposedVCS = "exposed_vcs"
	// IssueExposedSecret is a file holding credentials inside a document root.
	IssueExposedSecret = "exposed_secret"
	// IssueReadableKey is a private key readable beyond its owner.
	IssueReadableKey = "readable_key"
)

// PermissionReport is everything the permission scan found.
type PermissionReport struct {
	Issues []Issue `json:"issues"`
	// Counts holds the full total per kind. Issues is capped for display; these
	// are not, because "48,000 world-writable files" and "3" call for very
	// different responses and a truncated list cannot tell them apart.
	Counts map[string]int `json:"counts"`
	// Scanned is how many filesystem entries were visited.
	Scanned int `json:"scanned"`
	// Complete reports whether the whole tree was walked. False means the scan
	// hit its ceiling, and the counts are lower bounds rather than totals — a
	// distinction that matters, because "we found nothing else" and "we stopped
	// looking" are not the same answer.
	Complete bool     `json:"complete"`
	Roots    []string `json:"roots"`
	Reason   string   `json:"reason,omitempty"`
}

// Names inside a document root that must never be served.
//
// A closed list rather than a pattern: this is what gets *reported*, and a
// pattern that matched too much would produce a scanner people learn to ignore.
var exposedNames = map[string]string{
	".git":               IssueExposedVCS,
	".svn":               IssueExposedVCS,
	".hg":                IssueExposedVCS,
	".env":               IssueExposedSecret,
	".env.local":         IssueExposedSecret,
	".env.production":    IssueExposedSecret,
	"wp-config.php":      "", // handled below: it belongs there, its mode does not
	"id_rsa":             IssueReadableKey,
	"id_ed25519":         IssueReadableKey,
	".htpasswd":          IssueExposedSecret,
	".netrc":             IssueExposedSecret,
	"docker-compose.yml": IssueExposedSecret,
}

// keySuffixes are file extensions that hold private keys.
var keySuffixes = []string{".key", ".pem", ".p12", ".pfx"}

// Permissions walks the configured roots and reports what is wrong.
func (s *Scanner) Permissions() (PermissionReport, error) {
	report := PermissionReport{
		Issues:   []Issue{},
		Counts:   map[string]int{},
		Roots:    append([]string(nil), s.roots...),
		Complete: true,
	}

	if len(s.roots) == 0 {
		return PermissionReport{}, fmt.Errorf(
			"%w: this agent has no directories configured to scan", ErrUnsupported)
	}

	walked := false
	for _, root := range s.roots {
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			// A configured root that is not there is not an error: a host with
			// no sites has no site root yet.
			continue
		}
		walked = true
		if err := s.walkRoot(root, &report); err != nil {
			return PermissionReport{}, err
		}
		if !report.Complete {
			break
		}
	}

	if !walked {
		return PermissionReport{}, fmt.Errorf(
			"%w: none of the configured directories exist", ErrUnsupported)
	}

	if !report.Complete {
		report.Reason = fmt.Sprintf(
			"the scan stopped after %d entries, so these counts are a lower bound",
			MaxScannedEntries)
	}

	sort.Slice(report.Issues, func(i, j int) bool {
		if report.Issues[i].Kind != report.Issues[j].Kind {
			return report.Issues[i].Kind < report.Issues[j].Kind
		}
		return report.Issues[i].Path < report.Issues[j].Path
	})
	return report, nil
}

// walkRoot walks one configured root.
func (s *Scanner) walkRoot(root string, report *PermissionReport) error {
	base := filepath.Clean(root)

	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			// A directory this Agent may not read is skipped rather than
			// failing the scan. Reporting nothing because one directory was
			// unreadable would be the worst outcome: the scan looks clean.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		report.Scanned++
		if report.Scanned > MaxScannedEntries {
			report.Complete = false
			return filepath.SkipAll
		}

		if depthOf(base, path) > MaxScanDepth {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		// Symlinks are never followed. Following them would take the scan
		// outside the roots it was given, which is the one thing that must not
		// happen — and would loop on the first link to a parent.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		s.inspect(path, entry, info, report)

		if entry.IsDir() {
			return nil
		}
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return fmt.Errorf("%w: %s", ErrUnsupported, err)
	}
	return nil
}

// inspect records every issue one entry has.
func (s *Scanner) inspect(path string, entry fs.DirEntry, info fs.FileInfo,
	report *PermissionReport,
) {
	mode := info.Mode()
	perm := mode.Perm()
	name := entry.Name()

	// World-writable. A sticky directory — /tmp and the per-site temp
	// directories PHP needs — is deliberately world-writable and is not a
	// finding: the sticky bit is what stops one account deleting another's
	// files, and reporting those would bury the real ones.
	if perm&0o002 != 0 && mode&os.ModeSticky == 0 {
		report.add(Issue{
			Kind: IssueWorldWritable,
			Path: path,
			Mode: fmt.Sprintf("%04o", perm),
			Detail: "any account on this host can change it, including one " +
				"belonging to another site",
		})
	}

	if !entry.IsDir() {
		// setuid and setgid. A web application has no use for either, and one
		// appearing under a site root is a mistake or an intrusion.
		if mode&(os.ModeSetuid|os.ModeSetgid) != 0 {
			report.add(Issue{
				Kind:   IssueSetuid,
				Path:   path,
				Mode:   fmt.Sprintf("%04o", perm),
				Detail: "an executable that runs as its owner rather than as its caller",
			})
		}

		// A private key anybody but the owner can read.
		if isKeyFile(name) && perm&0o077 != 0 {
			report.add(Issue{
				Kind:   IssueReadableKey,
				Path:   path,
				Mode:   fmt.Sprintf("%04o", perm),
				Detail: "a private key readable by more than its owner is no longer private",
			})
		}
	}

	// Exposed under a document root. The check is on the directory being a
	// public one, because a .env one level *above* the document root is the
	// correct place for it — reporting that would teach people to ignore this.
	if !underDocumentRoot(path) {
		return
	}

	if kind, listed := exposedNames[name]; listed {
		switch {
		case name == "wp-config.php":
			// It belongs in a document root; the server is configured not to
			// serve it. What is worth reporting is a mode that lets other
			// accounts read the database password out of it.
			if perm&0o044 != 0 {
				report.add(Issue{
					Kind:   IssueExposedSecret,
					Path:   path,
					Mode:   fmt.Sprintf("%04o", perm),
					Detail: "readable by other accounts on this host, and it holds a database password",
				})
			}
		case kind == IssueReadableKey:
			report.add(Issue{
				Kind:   IssueReadableKey,
				Path:   path,
				Mode:   fmt.Sprintf("%04o", perm),
				Detail: "a private key inside a directory the web server serves",
			})
		default:
			report.add(Issue{
				Kind:   kind,
				Path:   path,
				Mode:   fmt.Sprintf("%04o", perm),
				Detail: "inside a directory the web server serves, so anyone can ask for it",
			})
		}
	}
}

// add records an issue, counting all of them and keeping some.
func (r *PermissionReport) add(issue Issue) {
	r.Counts[issue.Kind]++
	if len(r.Issues) < MaxReportedIssues {
		r.Issues = append(r.Issues, issue)
	}
}

// underDocumentRoot reports whether a path is inside a directory the web server
// serves.
//
// The convention this panel creates is <root>/<domain>/public, so a path with a
// "public" segment is inside a document root. It is a convention rather than a
// lookup because the scanner is given directories, not websites, and asking the
// panel which sites exist would make a host probe depend on the control plane.
func underDocumentRoot(path string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(path), "/") {
		if segment == "public" || segment == "public_html" || segment == "htdocs" {
			return true
		}
	}
	return false
}

// isKeyFile reports whether a filename looks like a private key.
func isKeyFile(name string) bool {
	lower := strings.ToLower(name)
	for _, suffix := range keySuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return strings.HasPrefix(lower, "id_rsa") || strings.HasPrefix(lower, "id_ed25519") ||
		strings.HasPrefix(lower, "id_ecdsa")
}

// depthOf counts directory levels between a base and a path.
func depthOf(base, path string) int {
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return 0
	}
	if relative == "." {
		return 0
	}
	return strings.Count(filepath.ToSlash(relative), "/") + 1
}
