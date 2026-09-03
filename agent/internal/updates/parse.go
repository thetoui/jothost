package updates

import (
	"regexp"
	"strconv"
	"strings"
)

// The parsers, and where their samples come from.
//
// Every pattern here was written against output copied from a running package
// manager — apk 2.14 on Alpine 3.21, apt 2.6 on Debian 12 — and the samples are
// frozen in the tests. These commands print for people, so the parser is the
// risk; a stub printing what I imagined would test my imagination.

// apk update prints one of two summaries, and only one of them mentions
// failure. Both are needed, and both were measured:
//
//	# every repository reachable
//	v3.21.7-230-gc031f07a9b5 [https://dl-cdn.alpinelinux.org/alpine/v3.21/main]
//	OK: 25398 distinct packages available
//
//	# two unreachable — note the exit status is still zero
//	WARNING: updating and opening https://…/main: DNS lookup error
//	2 unavailable, 0 stale; 198 distinct packages available
//
// Knowing only the second is how this package spent its first real run
// reporting "not known" on a perfectly healthy host — which was the
// conservative default doing its job, and is why the default is conservative.
var (
	apkRefreshDegraded = regexp.MustCompile(`(\d+)\s+unavailable,\s*(\d+)\s+stale`)
	apkRefreshOKLine   = regexp.MustCompile(`^OK:\s+\d+\s+distinct packages available`)
)

// apkUpgradeLine matches a line of apk upgrade --simulate:
//
//	(1/4) Upgrading git (2.45.4-r0 -> 2.47.3-r0)
var apkUpgradeLine = regexp.MustCompile(
	`^\(\d+/\d+\)\s+Upgrading\s+(\S+)\s+\((\S+)\s+->\s+(\S+)\)`)

// apkVersionLine matches a line of apk version -l '<':
//
//	git-2.45.4-r0                           < 2.47.3-r0
//
// The name and version are joined by a dash and the version contains one, so
// the split is anchored on Alpine's "-rN" revision suffix rather than on the
// last dash — "git-init-template-2.45.4-r0" is one package, not three.
var apkVersionLine = regexp.MustCompile(`^(\S+)-([^-\s]+-r\d+)\s+<\s+(\S+)`)

// parseAPKRefresh reads apk update's summary.
//
// Returns how many repositories were unavailable and stale, and whether a
// summary was found at all. No summary means apk said something this parser
// does not understand, which is treated as "cannot say" rather than as success.
func parseAPKRefresh(output string) (unavailable, stale int, found bool) {
	for _, raw := range strings.Split(output, "\n") {
		line := strings.TrimSpace(raw)
		if match := apkRefreshDegraded.FindStringSubmatch(line); match != nil {
			unavailable, _ = strconv.Atoi(match[1])
			stale, _ = strconv.Atoi(match[2])
			return unavailable, stale, true
		}
		if apkRefreshOKLine.MatchString(line) {
			// apk only counts failures when there are some; "OK" is how it says
			// there were none.
			return 0, 0, true
		}
	}
	return 0, 0, false
}

// parseAPKUpgrade reads what apk upgrade --simulate says it would do.
func parseAPKUpgrade(output string) []Package {
	packages := make([]Package, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		match := apkUpgradeLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		packages = append(packages, Package{
			Name:      match[1],
			Installed: match[2],
			Available: match[3],
		})
	}
	return packages
}

// parseAPKVersions reads apk version -l '<'.
//
// This is not the pending list. It is every package with something newer
// available, which includes the ones apk will never move — see the package
// comment.
func parseAPKVersions(output string) []Package {
	packages := make([]Package, 0, 8)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "Installed:") ||
			strings.HasPrefix(trimmed, "WARNING:") {
			continue
		}
		match := apkVersionLine.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		packages = append(packages, Package{
			Name:      match[1],
			Installed: match[2],
			Available: match[3],
		})
	}
	return packages
}

// apkVersionedName splits an "apk info -v" entry such as "git-2.47.3-r0"
// into its name and version, anchored on Alpine's revision suffix for the same
// reason apkVersionLine is.
var apkVersionedName = regexp.MustCompile(`^(\S+)-([^-\s]+-r\d+)$`)

// parseAPKWorld reads the version pins from Alpine's world file.
//
// A line is a package name, optionally with "=version" or another constraint.
// The constrained ones are why a package can sit in the version list forever
// while apk correctly refuses to upgrade it.
func parseAPKWorld(content string) map[string]string {
	pins := make(map[string]string, 4)
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// "=", "<", ">", "~" are all constraints apk accepts.
		index := strings.IndexAny(trimmed, "=<>~")
		if index <= 0 {
			continue
		}
		pins[trimmed[:index]] = strings.TrimLeft(trimmed[index:], "=<>~")
	}
	return pins
}

// aptInstLine matches a line of apt-get -s upgrade:
//
//	Inst base-files [12.4+deb12u5] (12.4+deb12u15 Debian:12.15/oldstable [amd64])
//	Inst libgcrypt20 [1.10.1-3] (1.10.1-3+deb12u1 Debian:12.15/oldstable, Debian-Security:12/oldstable-security [amd64])
//
// The installed version is in brackets and absent for a package being added as
// a dependency; the candidate and its origins are in parentheses.
var aptInstLine = regexp.MustCompile(
	`^Inst\s+(\S+)\s+(?:\[([^\]]*)\]\s+)?\(([^\s)]+)\s+([^)]*)\)`)

// parseAPTUpgrade reads what apt-get -s upgrade says it would do.
//
// The security judgement comes from the origin list apt prints with each
// candidate. Debian writes "Debian-Security:12/oldstable-security" and Ubuntu
// "Ubuntu:22.04/jammy-security"; matching "security" case-insensitively covers
// both, and a derivative that names its security suite something else is
// reported as an ordinary update rather than guessed at.
func parseAPTUpgrade(output string) []Package {
	packages := make([]Package, 0, 16)
	for _, line := range strings.Split(output, "\n") {
		match := aptInstLine.FindStringSubmatch(strings.TrimSpace(line))
		if match == nil {
			continue
		}
		origin := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(match[4]), "[amd64]"))
		origin = strings.TrimSpace(strings.TrimSuffix(origin, "]"))
		if index := strings.LastIndex(origin, "["); index > 0 {
			origin = strings.TrimSpace(origin[:index])
		}

		packages = append(packages, Package{
			Name:      match[1],
			Installed: match[2],
			Available: match[3],
			Security:  strings.Contains(strings.ToLower(match[4]), "security"),
			Origin:    origin,
		})
	}
	return packages
}

// aptErrLine matches apt-get update's failure lines.
//
// apt reports an unreachable repository with "Err:" and carries on, so the
// exit status of an update that reached nothing can still be zero — the same
// trap apk sets, in a different shape.
var aptErrLine = regexp.MustCompile(`^(?:Err|E):`)

// parseAPTRefresh counts the repositories apt could not reach.
func parseAPTRefresh(output string) int {
	failures := 0
	for _, line := range strings.Split(output, "\n") {
		if aptErrLine.MatchString(strings.TrimSpace(line)) {
			failures++
		}
	}
	return failures
}

// parseAPTHolds reads apt-mark showhold, which prints one name per line.
func parseAPTHolds(output string) []string {
	names := make([]string, 0, 4)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		names = append(names, trimmed)
	}
	return names
}

// aptPolicyVersion matches a version line of apt-cache policy <pkg>:
//
//	1.10.1-3+deb12u1 500
//
// It is how "can this exact version still be installed" is answered, which is
// the only honest basis for offering a revert.
var aptPolicyVersion = regexp.MustCompile(`^(\S+)\s+\d+\s*$`)

// parseAPTPolicyVersions lists the versions apt-cache policy offers.
func parseAPTPolicyVersions(output string) []string {
	versions := make([]string, 0, 4)
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "***") {
			// The installed one, marked. Still a version that exists.
			trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "***"))
		}
		match := aptPolicyVersion.FindStringSubmatch(trimmed)
		if match == nil {
			continue
		}
		versions = append(versions, match[1])
	}
	return versions
}

// parseAPKPolicyVersions lists the versions apk policy offers:
//
//	jq policy:
//	  1.7.1-r0:
//	    lib/apk/db/installed
//	    https://dl-cdn.alpinelinux.org/alpine/v3.21/main
//
// A version whose only source is lib/apk/db/installed is on the host and in no
// repository — which is exactly the case a revert has to refuse, because there
// is nothing left to install it from.
func parseAPKPolicyVersions(output string) []string {
	versions := make([]string, 0, 4)
	current := ""
	installedOnly := false

	flush := func() {
		if current != "" && !installedOnly {
			versions = append(versions, current)
		}
	}

	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasSuffix(trimmed, " policy:") {
			continue
		}
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, "/") {
			flush()
			current = strings.TrimSuffix(trimmed, ":")
			installedOnly = true
			continue
		}
		if trimmed != "lib/apk/db/installed" {
			installedOnly = false
		}
	}
	flush()
	return versions
}
