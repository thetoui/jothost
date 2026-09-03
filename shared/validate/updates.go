package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by update validation.
var (
	// ErrInvalidPackage covers a package name this panel will not hand to a
	// package manager.
	ErrInvalidPackage = errors.New("invalid package name")
	// ErrInvalidPackageVersion covers a version string.
	ErrInvalidPackageVersion = errors.New("invalid package version")
	// ErrInvalidUpdatePolicy covers an unknown automatic-update setting.
	ErrInvalidUpdatePolicy = errors.New("invalid update policy")
	// ErrInvalidUpdateWindow covers a schedule the panel will not accept.
	ErrInvalidUpdateWindow = errors.New("invalid update window")
)

// Automatic update policies.
//
// Three, and no more. "Security only" is the one that earns its place: it is
// the setting an operator can leave on without being woken by a major version
// of something changing under a running site.
const (
	// UpdatesOff means the panel reports updates and applies none.
	UpdatesOff = "off"
	// UpdatesSecurity applies only the updates the host marks as security
	// fixes — which not every package manager can identify. A host that cannot
	// tell them apart applies nothing under this policy, and says so.
	UpdatesSecurity = "security"
	// UpdatesAll applies everything the package manager offers.
	UpdatesAll = "all"
)

// UpdatePolicies is the supported set, in the order a UI should offer them.
var UpdatePolicies = []string{UpdatesOff, UpdatesSecurity, UpdatesAll}

// EveryDay is the day-of-week value meaning "every day".
const EveryDay = -1

// MaxPackageNameLength bounds a package name.
const MaxPackageNameLength = 128

// PackageName checks a name before it becomes an argument to a package manager
// running as root.
//
// The leading-dash rule is the important one and is not a formality: a package
// called "--allow-untrusted" is not a package, it is a flag, and a panel that
// passed it through would be letting a request change how the package manager
// behaves rather than what it operates on. Everything here goes into an
// argument vector — never a shell — so quoting is not the risk; being read as
// an option is.
func PackageName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: a package name is required", ErrInvalidPackage)
	}
	if len(name) > MaxPackageNameLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidPackage, MaxPackageNameLength)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("%w: a name starting with %q would be read as an option",
			ErrInvalidPackage, "-")
	}
	// A version pin belongs in its own field. Accepting "name=version" here
	// would mean two different places in this panel deciding what version is
	// being asked for, and only one of them checking it.
	if strings.ContainsAny(name, "=<>") {
		return fmt.Errorf("%w: a version belongs in its own field, not in the name",
			ErrInvalidPackage)
	}

	for _, char := range name {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9':
		case char == '.' || char == '-' || char == '_' || char == '+':
		default:
			return fmt.Errorf("%w: %q may not appear in a package name",
				ErrInvalidPackage, string(char))
		}
	}
	return nil
}

// PackageVersion checks a version string.
//
// Both package managers this panel drives have their own version grammars —
// Alpine's "1.2.3-r4", Debian's "1:2.38.1-5+deb12u3" — and neither is worth
// reimplementing here. What this refuses is anything that could be read as an
// option or as a second argument; the package manager itself is the judge of
// whether the version exists, and it is asked before anything is installed.
func PackageVersion(version string) error {
	if version == "" {
		return fmt.Errorf("%w: a version is required", ErrInvalidPackageVersion)
	}
	if len(version) > MaxPackageNameLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidPackageVersion, MaxPackageNameLength)
	}
	if strings.HasPrefix(version, "-") {
		return fmt.Errorf("%w: a version starting with %q would be read as an option",
			ErrInvalidPackageVersion, "-")
	}

	for _, char := range version {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z',
			char >= '0' && char <= '9':
		case char == '.' || char == '-' || char == '_' || char == '+' ||
			char == '~' || char == ':':
		default:
			return fmt.Errorf("%w: %q may not appear in a version",
				ErrInvalidPackageVersion, string(char))
		}
	}
	return nil
}

// UpdatePolicy checks an automatic-update setting.
func UpdatePolicy(policy string) error {
	for _, supported := range UpdatePolicies {
		if policy == supported {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidUpdatePolicy, policy, strings.Join(UpdatePolicies, ", "))
}

// UpdateWindow checks when automatic updates may run.
//
// A day and an hour rather than a cron expression. The panel already has a
// cron page for a customer's own jobs; this is the panel updating the machine
// it runs on, and the useful question is "which quiet hour", not "express an
// arbitrary schedule". A narrower control is also one whose next run the panel
// can state plainly, which matters for something that restarts daemons.
func UpdateWindow(dayOfWeek, hour, minute int) error {
	if dayOfWeek != EveryDay && (dayOfWeek < 0 || dayOfWeek > 6) {
		return fmt.Errorf("%w: the day must be 0 (Sunday) to 6 (Saturday), or %d for every day",
			ErrInvalidUpdateWindow, EveryDay)
	}
	if hour < 0 || hour > 23 {
		return fmt.Errorf("%w: the hour must be between 0 and 23", ErrInvalidUpdateWindow)
	}
	if minute < 0 || minute > 59 {
		return fmt.Errorf("%w: the minute must be between 0 and 59", ErrInvalidUpdateWindow)
	}
	return nil
}
