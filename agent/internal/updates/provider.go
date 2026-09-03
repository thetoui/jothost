package updates

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Provider reads and applies this host's package updates.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	// manager is the detected package manager, or "" when none was found.
	manager string
	// worldPath is Alpine's list of explicitly installed packages, where a
	// version pin lives.
	worldPath string
	// rebootFlag is the file a Debian host touches when a restart is needed.
	rebootFlag string
	now        func() time.Time
}

// Options configure a Provider.
type Options struct {
	Runner *command.Runner
	Log    *slog.Logger
	// WorldPath and RebootFlag default to the distributions' own locations.
	WorldPath  string
	RebootFlag string
	Now        func() time.Time
}

// Default locations. Constants here or configuration on the Agent; never taken
// from a request.
const (
	DefaultWorldPath  = "/etc/apk/world"
	DefaultRebootFlag = "/var/run/reboot-required"
)

// NewProvider builds a Provider over whichever package manager is present.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	if opts.WorldPath == "" {
		opts.WorldPath = DefaultWorldPath
	}
	if opts.RebootFlag == "" {
		opts.RebootFlag = DefaultRebootFlag
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return &Provider{
		runner:     opts.Runner,
		log:        log,
		manager:    detectManager(opts.Runner),
		worldPath:  opts.WorldPath,
		rebootFlag: opts.RebootFlag,
		now:        opts.Now,
	}
}

// detectManager reports which package manager this host uses.
//
// The same order and the same reasoning as agent/internal/php: an Alpine host
// has no apt-get and a Debian host has no apk, so the order only matters on a
// system carrying both.
func detectManager(runner *command.Runner) string {
	if runner == nil {
		return ""
	}
	if runner.Available(CommandAPK) {
		return ManagerAPK
	}
	if runner.Available(CommandAPT) {
		return ManagerAPT
	}
	return ""
}

// Available reports whether this host has a package manager the panel drives.
func (p *Provider) Available() bool { return p != nil && p.manager != "" }

// Manager returns the detected package manager, or "".
func (p *Provider) Manager() string {
	if p == nil {
		return ""
	}
	return p.manager
}

// SecurityKnown reports whether this host can distinguish security updates.
//
// apt can, from the origin it prints with each candidate. apk cannot: Alpine
// publishes its security fixes as ordinary package versions and apk has no
// security channel to ask about. Saying so is the whole point — a panel that
// reported "0 security updates" on an Alpine host would be answering a question
// it never asked.
func (p *Provider) SecurityKnown() bool {
	return p.Manager() == ManagerAPT
}

// Check reports what this host has waiting.
//
// The index is refreshed first, because "check for updates" means exactly that,
// and a check against a stale index is the thing this package exists to avoid.
func (p *Provider) Check(ctx context.Context) (Report, error) {
	report := Report{
		Manager:       p.Manager(),
		Available:     p.Available(),
		SecurityKnown: p.SecurityKnown(),
		Packages:      []Package{},
		Held:          []Held{},
		CheckedAt:     p.now().UTC(),
	}
	if !p.Available() {
		report.Reason = ErrUnavailable.Error()
		return report, nil
	}

	unavailable, stale, err := p.refresh(ctx)
	report.Unavailable = unavailable
	report.Stale = stale
	if err != nil {
		// Reported, not returned as a failure. The host is fine; what is
		// unknown is what it needs, and that distinction is what the page has
		// to show.
		report.Reason = err.Error()
		report.RebootRequired = p.rebootRequired()
		return report, nil
	}
	if unavailable > 0 {
		report.Reason = fmt.Sprintf(
			"%d of this host's package repositories could not be reached, so what is "+
				"outstanding is not known", unavailable)
		report.RebootRequired = p.rebootRequired()
		return report, nil
	}

	switch p.manager {
	case ManagerAPK:
		err = p.checkAPK(ctx, &report)
	case ManagerAPT:
		err = p.checkAPT(ctx, &report)
	}
	if err != nil {
		report.Reason = err.Error()
		return report, nil
	}

	report.Checked = true
	report.RebootRequired = p.rebootRequired()
	return report, nil
}

// refresh updates the package index and reports what it could not reach.
func (p *Provider) refresh(ctx context.Context) (int, int, error) {
	ctx, cancel := context.WithTimeout(ctx, refreshTimeout)
	defer cancel()

	switch p.manager {
	case ManagerAPK:
		result, err := p.runner.Run(ctx, CommandAPK, "update")
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %s", ErrRefreshFailed, err.Error())
		}
		// apk writes the summary to stderr, and exits zero whatever happened.
		unavailable, stale, found := parseAPKRefresh(result.Stderr + "\n" + result.Stdout)
		if !found {
			if !result.Succeeded() {
				return 0, 0, fmt.Errorf("%w: %s", ErrRefreshFailed,
					firstLine(result.Stderr, result.Stdout))
			}
			// No summary and a clean exit is apk saying something this parser
			// does not understand. Treated as "cannot say" rather than as
			// success, because the cost of being wrong here is a panel telling
			// somebody they are up to date when it has no idea.
			return 0, 0, fmt.Errorf(
				"%w: the package manager did not report what it refreshed", ErrRefreshFailed)
		}
		return unavailable, stale, nil

	case ManagerAPT:
		result, err := p.runner.Run(ctx, CommandAPT, "update", "-qq")
		if err != nil {
			return 0, 0, fmt.Errorf("%w: %s", ErrRefreshFailed, err.Error())
		}
		failures := parseAPTRefresh(result.Stderr + "\n" + result.Stdout)
		if !result.Succeeded() && failures == 0 {
			return 0, 0, fmt.Errorf("%w: %s", ErrRefreshFailed,
				firstLine(result.Stderr, result.Stdout))
		}
		return failures, 0, nil
	}
	return 0, 0, ErrUnavailable
}

// checkAPK fills in an Alpine host's pending and held lists.
func (p *Provider) checkAPK(ctx context.Context, report *Report) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	// What apk says it will do. This is the pending list.
	simulate, err := p.runner.Run(ctx, CommandAPK, "upgrade", "--simulate")
	if err != nil {
		return fmt.Errorf("ask apk what it would upgrade: %w", err)
	}
	if !simulate.Succeeded() {
		return fmt.Errorf("%w: %s", ErrUpgradeFailed, firstLine(simulate.Stderr, simulate.Stdout))
	}
	report.Packages = parseAPKUpgrade(simulate.Stdout + "\n" + simulate.Stderr)

	// What has something newer available. The difference between this and the
	// list above is what apk is refusing to move, which is a pin somebody set.
	versions, err := p.runner.Run(ctx, CommandAPK, "version", "-l", "<")
	if err != nil {
		// Not fatal: the pending list is already known, and this only adds the
		// held ones.
		p.log.Warn("could not read which packages have newer versions", "error", err.Error())
		return nil
	}

	pending := make(map[string]bool, len(report.Packages))
	for _, pkg := range report.Packages {
		pending[pkg.Name] = true
	}
	pins := p.worldPins()

	for _, candidate := range parseAPKVersions(versions.Stdout) {
		if pending[candidate.Name] {
			continue
		}
		reason := "held back by this host's package manager"
		if pin, ok := pins[candidate.Name]; ok {
			reason = fmt.Sprintf("pinned to %s in %s", pin, p.worldPath)
		}
		report.Held = append(report.Held, Held{
			Name:      candidate.Name,
			Installed: candidate.Installed,
			Available: candidate.Available,
			Reason:    reason,
		})
	}
	sortHeld(report.Held)
	return nil
}

// worldPins reads Alpine's version pins, or nothing when the file is not there.
func (p *Provider) worldPins() map[string]string {
	data, err := os.ReadFile(p.worldPath)
	if err != nil {
		return map[string]string{}
	}
	return parseAPKWorld(string(data))
}

// checkAPT fills in a Debian host's pending and held lists.
func (p *Provider) checkAPT(ctx context.Context, report *Report) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	simulate, err := p.runner.Run(ctx, CommandAPT, "-s", "upgrade")
	if err != nil {
		return fmt.Errorf("ask apt what it would upgrade: %w", err)
	}
	if !simulate.Succeeded() {
		return fmt.Errorf("%w: %s", ErrUpgradeFailed, firstLine(simulate.Stderr, simulate.Stdout))
	}
	report.Packages = parseAPTUpgrade(simulate.Stdout)

	if !p.runner.Available(CommandAPTMark) {
		return nil
	}
	holds, err := p.runner.Run(ctx, CommandAPTMark, "showhold")
	if err != nil || !holds.Succeeded() {
		p.log.Warn("could not read which packages are held")
		return nil
	}
	for _, name := range parseAPTHolds(holds.Stdout) {
		report.Held = append(report.Held, Held{
			Name:   name,
			Reason: "held with apt-mark on this host",
		})
	}
	sortHeld(report.Held)
	return nil
}

// sortHeld puts the held list in a stable order.
func sortHeld(held []Held) {
	sort.SliceStable(held, func(i, j int) bool { return held[i].Name < held[j].Name })
}

// rebootRequired reports whether the host says a restart is needed.
//
// Debian and Ubuntu touch a file; Alpine has no such convention and the answer
// there is always false, which is honest rather than helpful — a kernel upgrade
// on Alpine needs a reboot too and nothing on the host says so.
func (p *Provider) rebootRequired() bool {
	if p.rebootFlag == "" {
		return false
	}
	_, err := os.Stat(p.rebootFlag)
	return err == nil
}

// Apply installs updates.
//
// With no names it applies everything the package manager offers. With names it
// applies exactly those — which is what makes a security-only policy possible
// on a host that can tell them apart.
//
// The changes are read back from the host afterwards rather than assumed from
// what was asked: a package manager resolves dependencies, so applying one
// update routinely moves several, and the list an operator needs is the one
// that describes their machine.
func (p *Provider) Apply(ctx context.Context, names []string,
	report func(int, string),
) (ApplyResult, error) {
	result := ApplyResult{Changed: []Change{}, Requested: names}
	if !p.Available() {
		return result, ErrUnavailable
	}
	for _, name := range names {
		if err := validate.PackageName(name); err != nil {
			return result, err
		}
	}

	before, err := p.installedVersions(ctx)
	if err != nil {
		return result, err
	}

	progress(report, 20, "Applying updates")
	output, err := p.runUpgrade(ctx, names)
	result.Output = output
	if err != nil {
		return result, err
	}

	progress(report, 80, "Checking what changed")
	after, err := p.installedVersions(ctx)
	if err != nil {
		return result, err
	}
	result.Changed = diffVersions(before, after)
	result.RebootRequired = p.rebootRequired()
	return result, nil
}

// runUpgrade executes the upgrade and returns the manager's transcript.
func (p *Provider) runUpgrade(ctx context.Context, names []string) (string, error) {
	var args []string
	switch p.manager {
	case ManagerAPK:
		// `apk upgrade` with names upgrades exactly those; without, everything.
		args = append([]string{"upgrade", "--no-progress"}, names...)
	case ManagerAPT:
		if len(names) == 0 {
			args = []string{"-y", "-o", "Dpkg::Options::=--force-confold", "upgrade"}
		} else {
			// --only-upgrade so a name that is not installed cannot be used to
			// *add* software to the host through an update.
			args = append([]string{
				"-y", "-o", "Dpkg::Options::=--force-confold",
				"install", "--only-upgrade",
			}, names...)
		}
	default:
		return "", ErrUnavailable
	}

	result, err := p.runner.Run(ctx, managerCommand(p.manager), args...)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrUpgradeFailed, err.Error())
	}
	transcript := strings.TrimSpace(result.Stdout + "\n" + result.Stderr)
	if !result.Succeeded() {
		return transcript, fmt.Errorf("%w: %s", ErrUpgradeFailed,
			firstLine(result.Stderr, result.Stdout))
	}
	return transcript, nil
}

// installedVersions reads every package installed on the host, with its
// version.
//
// Every package, not just the ones being asked for. A package manager resolves
// dependencies, so applying one update routinely moves several, and a
// before-and-after comparison narrowed to the requested names would report a
// smaller change than actually happened — which is the opposite of what a
// history is for.
func (p *Provider) installedVersions(ctx context.Context) (map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	versions := make(map[string]string, 256)
	switch p.manager {
	case ManagerAPK:
		result, err := p.runner.Run(ctx, CommandAPK, "info", "-v")
		if err != nil {
			return nil, fmt.Errorf("read the installed packages: %w", err)
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			name, version, ok := splitAPKPackage(strings.TrimSpace(line))
			if ok {
				versions[name] = version
			}
		}
	case ManagerAPT:
		// dpkg-query rather than apt-cache: apt-cache answers about the
		// archive, and the question here is what is on the disk. It is
		// read-only, and the output format is given on the command line, so
		// there is nothing to parse loosely.
		result, err := p.runner.Run(ctx, CommandDpkgQuery, "-W", "-f=${Package} ${Version}\n")
		if err != nil {
			return nil, fmt.Errorf("read the installed packages: %w", err)
		}
		for _, line := range strings.Split(result.Stdout, "\n") {
			name, version, found := strings.Cut(strings.TrimSpace(line), " ")
			if found && name != "" {
				versions[name] = version
			}
		}
	}
	return versions, nil
}

// splitAPKPackage splits "git-2.47.3-r0" into its name and version.
func splitAPKPackage(entry string) (string, string, bool) {
	match := apkVersionedName.FindStringSubmatch(entry)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// diffVersions reports what moved between two readings.
func diffVersions(before, after map[string]string) []Change {
	changes := make([]Change, 0, 8)
	for name, now := range after {
		was, existed := before[name]
		if !existed {
			changes = append(changes, Change{Name: name, From: "", To: now})
			continue
		}
		if was != now {
			changes = append(changes, Change{Name: name, From: was, To: now})
		}
	}
	for name, was := range before {
		if _, still := after[name]; !still {
			changes = append(changes, Change{Name: name, From: was, To: ""})
		}
	}
	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Name < changes[j].Name })
	return changes
}

// CanRevert reports whether an exact version can still be installed.
//
// It is asked of the package manager rather than assumed, because the answer is
// usually no: neither apk nor apt keeps the package it replaced, and a Debian
// security update's predecessor is normally gone from the archive the moment it
// is superseded. A revert offered without this check would be a button that
// fails after taking the service down.
func (p *Provider) CanRevert(ctx context.Context, name, version string) (bool, error) {
	if !p.Available() {
		return false, ErrUnavailable
	}
	if err := validate.PackageName(name); err != nil {
		return false, err
	}
	if err := validate.PackageVersion(version); err != nil {
		return false, err
	}

	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()

	switch p.manager {
	case ManagerAPK:
		result, err := p.runner.Run(ctx, CommandAPK, "policy", name)
		if err != nil || !result.Succeeded() {
			return false, nil
		}
		return contains(parseAPKPolicyVersions(result.Stdout), version), nil
	case ManagerAPT:
		if !p.runner.Available(CommandAPTCache) {
			return false, nil
		}
		result, err := p.runner.Run(ctx, CommandAPTCache, "policy", name)
		if err != nil || !result.Succeeded() {
			return false, nil
		}
		return contains(parseAPTPolicyVersions(result.Stdout), version), nil
	}
	return false, ErrUnavailable
}

// Revert installs an exact earlier version of one package.
//
// It refuses unless the package manager confirms that version is still
// installable. That refusal is the feature: "the update cannot be undone on
// this host" said before anything happens is worth more than a rollback that
// half-works.
func (p *Provider) Revert(ctx context.Context, name, version string,
	report func(int, string),
) (ApplyResult, error) {
	result := ApplyResult{Changed: []Change{}, Requested: []string{name}}

	possible, err := p.CanRevert(ctx, name, version)
	if err != nil {
		return result, err
	}
	if !possible {
		return result, fmt.Errorf("%w: %s %s", ErrVersionUnavailable, name, version)
	}

	before, err := p.installedVersions(ctx)
	if err != nil {
		return result, err
	}

	progress(report, 30, "Installing "+name+" "+version)
	var args []string
	switch p.manager {
	case ManagerAPK:
		// The version is a separate concept in apk's grammar and is joined to
		// the name here, after both have been validated separately.
		args = []string{"add", "--no-progress", name + "=" + version}
	case ManagerAPT:
		args = []string{"-y", "-o", "Dpkg::Options::=--force-confold",
			"install", "--allow-downgrades", name + "=" + version}
	default:
		return result, ErrUnavailable
	}

	run, err := p.runner.Run(ctx, managerCommand(p.manager), args...)
	if err != nil {
		return result, fmt.Errorf("%w: %s", ErrUpgradeFailed, err.Error())
	}
	result.Output = strings.TrimSpace(run.Stdout + "\n" + run.Stderr)
	if !run.Succeeded() {
		return result, fmt.Errorf("%w: %s", ErrUpgradeFailed, firstLine(run.Stderr, run.Stdout))
	}

	after, err := p.installedVersions(ctx)
	if err != nil {
		return result, err
	}
	result.Changed = diffVersions(before, after)
	result.RebootRequired = p.rebootRequired()
	return result, nil
}

// managerCommand maps a manager to its allowlist name.
func managerCommand(manager string) string {
	switch manager {
	case ManagerAPK:
		return CommandAPK
	case ManagerAPT:
		return CommandAPT
	default:
		return ""
	}
}

// contains reports whether a slice holds a value.
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

// progress reports a step when a reporter was given.
func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}

// firstLine returns the first non-empty line of the candidates given.
func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed != "" && !strings.HasPrefix(trimmed, "WARNING:") {
				return trimmed
			}
		}
	}
	return ""
}
