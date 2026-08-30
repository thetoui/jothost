package nodejs

import (
	"context"
	"errors"
	"fmt"

	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/shared/validate"
)

// Errors returned by the installer.
var (
	// ErrVersionNotInstalled means the host does not have that Node.js.
	ErrVersionNotInstalled = errors.New("that Node.js version is not installed")
	// ErrNoPackageManager means nothing on this host can install one.
	ErrNoPackageManager = errors.New("no supported package manager was found on this host")
	// ErrVersionUnavailable means the host's packages do not offer it.
	ErrVersionUnavailable = errors.New("that Node.js version is not offered by this host's packages")
)

// PackageInstaller installs a named package through the host's package manager.
//
// The same narrow interface the phpMyAdmin installer uses, and for the same
// reason: the package-manager detection already exists and should not be
// written twice.
type PackageInstaller interface {
	Available() bool
	Manager() string
	InstallPackage(ctx context.Context, pkg string, report func(int, string)) error
	RemovePackage(ctx context.Context, pkg string, report func(int, string)) error
}

// Offer is a Node.js version this host could install.
type Offer struct {
	// Version is the major version the package provides.
	Version string `json:"version"`
	// Package is what would be installed.
	Package string `json:"package"`
	// Label describes the release line, because "22" means nothing on its own
	// to somebody choosing.
	Label string `json:"label"`
}

// apkOffers are what Alpine provides.
//
// Alpine does not carry one package per major version the way Debian's vendor
// repositories do; it carries the LTS line and the current line, and which
// major each is depends on the release. The version is therefore discovered
// after installation rather than asserted here, and the offer names the line.
var apkOffers = []Offer{
	{Package: "nodejs", Label: "Long-term support"},
	{Package: "nodejs-current", Label: "Current release"},
}

// aptOffers are what Debian and Ubuntu provide from their own archives.
var aptOffers = []Offer{
	{Package: "nodejs", Label: "The distribution's Node.js"},
}

// Installer adds and removes Node.js through the host's package manager.
type Installer struct {
	packages PackageInstaller
	detector *Detector
}

// NewInstaller builds an Installer.
func NewInstaller(packages PackageInstaller, detector *Detector) *Installer {
	return &Installer{packages: packages, detector: detector}
}

// Available reports whether this host can install Node.js at all.
func (i *Installer) Available() bool {
	return i.packages != nil && i.packages.Available()
}

// Manager returns the detected package manager name, or "".
func (i *Installer) Manager() string {
	if i.packages == nil {
		return ""
	}
	return i.packages.Manager()
}

// Offers reports the Node.js versions this host could install.
//
// A version already installed is still listed: reinstalling is how an operator
// moves from the LTS line to the current one, and hiding the option would make
// that look impossible.
func (i *Installer) Offers() []Offer {
	if !i.Available() {
		return nil
	}

	switch i.packages.Manager() {
	case php.ManagerAPK:
		return apkOffers
	case php.ManagerAPT:
		return aptOffers
	default:
		return nil
	}
}

// Install adds a Node.js release line to the host.
//
// The package is chosen from the table above by the label the caller asked
// for, never built from the request: a version string reaching a package
// manager running as root is exactly the thing the allowlist exists to stop.
//
// npm comes with it. A Node.js installation that cannot install dependencies
// is not one anybody can deploy to, and on Alpine npm is a separate package.
func (i *Installer) Install(ctx context.Context, pkg string, report func(int, string)) (Version, error) {
	if !i.Available() {
		return Version{}, ErrNoPackageManager
	}

	offer, ok := i.offerFor(pkg)
	if !ok {
		return Version{}, fmt.Errorf("%w: %q", ErrVersionUnavailable, pkg)
	}

	progress(report, 20, "Installing "+offer.Package)
	if err := i.packages.InstallPackage(ctx, offer.Package, report); err != nil {
		return Version{}, err
	}

	if i.packages.Manager() == php.ManagerAPK {
		progress(report, 60, "Installing npm")
		if err := i.packages.InstallPackage(ctx, "npm", report); err != nil {
			return Version{}, fmt.Errorf("install npm: %w", err)
		}
	}

	progress(report, 90, "Checking what was installed")
	// What is actually there is read back rather than assumed. The package
	// names a release line, and which major that is changes over time.
	versions := i.detector.Detect(ctx)
	if len(versions) == 0 {
		return Version{}, fmt.Errorf("%w: the package installed but no runtime was found",
			ErrVersionNotInstalled)
	}
	return versions[0], nil
}

// Remove takes Node.js off the host.
func (i *Installer) Remove(ctx context.Context, pkg string, report func(int, string)) error {
	if !i.Available() {
		return ErrNoPackageManager
	}

	offer, ok := i.offerFor(pkg)
	if !ok {
		return fmt.Errorf("%w: %q", ErrVersionUnavailable, pkg)
	}

	progress(report, 30, "Removing "+offer.Package)
	return i.packages.RemovePackage(ctx, offer.Package, report)
}

// offerFor finds an offer by package name.
func (i *Installer) offerFor(pkg string) (Offer, bool) {
	for _, offer := range i.Offers() {
		if offer.Package == pkg {
			return offer, true
		}
	}
	return Offer{}, false
}

// FormatVersions renders versions for a protocol response.
func FormatVersions(versions []Version) []map[string]any {
	out := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		out = append(out, map[string]any{
			"version":      version.Version,
			"full_version": version.Full,
			"binary_path":  version.BinaryPath,
			"npm_path":     version.NPMPath,
			"npm_version":  version.NPMVersion,
		})
	}
	return out
}

// RequireVersion checks a caller-supplied version against what is installed.
func (i *Installer) RequireVersion(ctx context.Context, version string) (Version, error) {
	if err := validate.NodeVersion(version); err != nil {
		return Version{}, err
	}
	return i.detector.Lookup(ctx, version)
}

func progress(report func(int, string), percent int, message string) {
	if report != nil {
		report(percent, message)
	}
}
