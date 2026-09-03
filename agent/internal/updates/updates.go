// Package updates reports and applies the host's package updates.
//
// # The failure this package is built around
//
// A panel that says "up to date" when it could not check is worse than one that
// says nothing, because that sentence is what an operator reads to decide they
// are safe. Both package managers make it easy to write:
//
//	# every repository unreachable
//	$ apk update
//	WARNING: updating and opening https://…/main: DNS lookup error
//	2 unavailable, 0 stale; 198 distinct packages available
//	$ echo $?
//	0
//	$ apk version -l '<'
//	Installed:                                Available:
//	$ echo $?
//	0
//
// Measured, not assumed. Exit status says nothing, and the empty list is
// indistinguishable from a host with nothing to do. So the refresh is parsed
// for what it actually reports, and a host whose index could not be refreshed
// is reported as *unknown* rather than as current.
//
// # Where the pending list comes from
//
// From what the package manager says it will *do* — `apk upgrade --simulate`,
// `apt-get -s upgrade` — and not from a version comparison.
//
// The two disagree, and the disagreement is not academic. A package pinned to a
// version in Alpine's world file appears in `apk version -l '<'` forever and is
// deliberately never touched by `apk upgrade`:
//
//	$ apk version -l '<'
//	git-2.45.4-r0                           < 2.47.3-r0
//	$ apk upgrade --simulate
//	OK: 479 MiB in 205 packages
//
// A panel listing the first as pending would show an operator a list that never
// empties, however many times they applied it. The version comparison is still
// read — it is exactly how the *held back* packages are found — and they are
// reported as held, with their pin, rather than as work outstanding.
//
// # What it does not do
//
// It does not roll back. Neither apk nor apt keeps the package it replaced, and
// a Debian security update's predecessor is usually gone from the archive the
// moment it is superseded. What is offered instead is a revert that asks the
// package manager whether that exact version can still be installed and refuses
// when it cannot — and a history of every change with exact versions, which is
// what makes a manual recovery possible.
package updates

import (
	"errors"
	"time"
)

// Package manager identifiers, matching agent/internal/php so a host reports
// one name for the manager whatever asked.
const (
	ManagerAPK = "apk"
	ManagerAPT = "apt-get"
)

// Allowlist names for the programs this package runs.
//
// The package managers themselves are already in the Agent's allowlist for
// Phase 5; these are the read-only companions apt needs, because apt-get cannot
// answer "what versions exist" and apt-mark cannot be asked through apt-get.
const (
	CommandAPK      = "apk"
	CommandAPT      = "apt-get"
	CommandAPTCache = "apt-cache"
	CommandAPTMark  = "apt-mark"
	// dpkg-query answers what is installed on the disk, which is what a
	// before-and-after comparison needs; apt-cache answers about the archive.
	CommandDpkgQuery = "dpkg-query"
)

// Errors this package returns.
var (
	// ErrUnavailable means this host has no package manager the panel drives.
	ErrUnavailable = errors.New("no supported package manager was found on this host")
	// ErrRefreshFailed means the package index could not be refreshed, so
	// nothing can be said about what is outstanding.
	ErrRefreshFailed = errors.New("the package index could not be refreshed")
	// ErrUpgradeFailed means the package manager refused to apply an update.
	ErrUpgradeFailed = errors.New("the update could not be applied")
	// ErrVersionUnavailable means a revert was asked for a version the host can
	// no longer install.
	ErrVersionUnavailable = errors.New("that version is no longer available from this host's repositories")
	// ErrSecurityUnsupported means this host cannot tell security updates apart.
	ErrSecurityUnsupported = errors.New("this host's package manager does not mark security updates")
)

// Timeouts. Refreshing an index and applying updates both reach the network.
const (
	refreshTimeout = 5 * time.Minute
	checkTimeout   = 2 * time.Minute
)

// Package is one update the host has waiting.
type Package struct {
	Name string `json:"name"`
	// Installed is the version on the host now; Available is what would be
	// installed.
	Installed string `json:"installed"`
	Available string `json:"available"`
	// Security reports that the host marked this as a security fix.
	//
	// False does not mean "not a security fix" on every host: see
	// SecurityKnown on Report. Alpine's apk has no security channel at all, and
	// reporting zero security updates there would read as "nothing urgent"
	// rather than "cannot tell".
	Security bool `json:"security"`
	// Origin is where the new version comes from, as the package manager names
	// it. It is what the security judgement is made from, so it is shown.
	Origin string `json:"origin,omitempty"`
}

// Held is a package with a newer version available that the host will not
// upgrade.
//
// It is a separate list, and that is the point: an operator pinned it, and a
// panel that listed it as outstanding work would be showing a queue that never
// empties. The panel does not remove pins — somebody set them deliberately.
type Held struct {
	Name      string `json:"name"`
	Installed string `json:"installed"`
	Available string `json:"available"`
	// Reason says how it is held: a version pin in Alpine's world file, or a
	// dpkg hold.
	Reason string `json:"reason"`
}

// Report is everything the panel knows about this host's updates.
type Report struct {
	Available bool   `json:"available"`
	Manager   string `json:"manager"`
	// Checked reports whether the index was refreshed successfully. When it is
	// false the lists below are not "nothing to do" — they are "not known", and
	// Reason says why.
	Checked bool   `json:"checked"`
	Reason  string `json:"reason,omitempty"`
	// SecurityKnown reports whether this host can distinguish security updates
	// at all.
	SecurityKnown bool `json:"security_known"`

	Packages []Package `json:"packages"`
	Held     []Held    `json:"held"`

	// Repositories is how many the refresh could not reach, and how many were
	// left stale. Both are why a check is untrustworthy, so both are shown.
	Unavailable int `json:"unavailable_repositories"`
	Stale       int `json:"stale_repositories"`

	// CheckedAt is when this was read from the host.
	CheckedAt time.Time `json:"checked_at"`
	// RebootRequired reports that the host says a restart is needed to finish
	// applying what is already installed.
	RebootRequired bool `json:"reboot_required"`
}

// SecurityCount returns how many pending updates are security fixes.
func (r Report) SecurityCount() int {
	count := 0
	for _, pkg := range r.Packages {
		if pkg.Security {
			count++
		}
	}
	return count
}

// Names returns the pending package names, in the order reported.
func (r Report) Names() []string {
	names := make([]string, 0, len(r.Packages))
	for _, pkg := range r.Packages {
		names = append(names, pkg.Name)
	}
	return names
}

// SecurityNames returns the pending security-fix package names.
func (r Report) SecurityNames() []string {
	names := make([]string, 0, len(r.Packages))
	for _, pkg := range r.Packages {
		if pkg.Security {
			names = append(names, pkg.Name)
		}
	}
	return names
}

// Change is one package an apply actually moved.
type Change struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// ApplyResult reports what an apply did.
type ApplyResult struct {
	// Changed is what actually moved, read back from the host afterwards
	// rather than taken from what was asked for: a package manager resolves
	// dependencies, so applying one update routinely moves several, and the
	// list an operator needs is the one that describes their machine.
	Changed []Change `json:"changed"`
	// Requested is what the panel asked for, empty when everything was.
	Requested []string `json:"requested,omitempty"`
	// Output is the package manager's own transcript, kept because when this
	// goes wrong the manager's words are the useful ones.
	Output string `json:"output"`
	// RebootRequired reports that the host now wants a restart.
	RebootRequired bool `json:"reboot_required"`
}
