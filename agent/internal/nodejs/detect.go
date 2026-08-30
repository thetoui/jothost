// Package nodejs detects, installs, and runs Node.js applications on the
// managed host.
//
// Two things shape everything here.
//
// First, an application is somebody else's code. It runs under the website's
// own system account, in its own session, with an environment the panel
// composed — never as root, never with the Agent's environment, and never
// through a shell.
//
// Second, the panel cannot assume systemd. It is the right mechanism on a real
// host and the unit is written for it, but containers and minimal images do
// not have it, so the Agent can also supervise a process itself. Both paths
// answer the same operations, so nothing above this package has to know which
// one a host uses.
package nodejs

import (
	"context"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandNode is the allowlist name for the Node.js runtime.
const CommandNode = "node"

// CommandNPM is the allowlist name for npm, used to install dependencies.
const CommandNPM = "npm"

// binaryPaths are the fixed locations Node may live at.
//
// As everywhere else in the Agent the path is pinned rather than resolved
// through PATH: this runs as root and starts a program.
var binaryPaths = []string{
	"/usr/bin/node",
	"/usr/local/bin/node",
	// Debian's historical name, still shipped by some images.
	"/usr/bin/nodejs",
}

var npmPaths = []string{
	"/usr/bin/npm",
	"/usr/local/bin/npm",
}

// detectTimeout bounds asking a binary for its version.
const detectTimeout = 5 * time.Second

// versionPattern matches the output of `node --version`, e.g. "v22.11.0".
var versionPattern = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)`)

// Version is one Node.js runtime found on the host.
type Version struct {
	// Version is the major version, which is how Node is chosen: nobody asks
	// for 22.11.0, they ask for 22.
	Version string `json:"version"`
	// Full is what the binary reported.
	Full       string `json:"full_version"`
	BinaryPath string `json:"binary_path"`
	NPMPath    string `json:"npm_path,omitempty"`
	NPMVersion string `json:"npm_version,omitempty"`
}

// Detector reports which Node.js runtimes the host has.
type Detector struct {
	runner *command.Runner
}

// NewDetector builds a Detector.
func NewDetector(runner *command.Runner) *Detector {
	return &Detector{runner: runner}
}

// Available reports whether any Node.js runtime is present.
func (d *Detector) Available() bool {
	return d.runner != nil && d.runner.Available(CommandNode)
}

// Detect returns the runtimes on this host.
//
// In practice a host has one: the distributions ship a single `node` binary
// and replace it on upgrade, unlike PHP where versions install side by side.
// The result is a slice anyway, because a host using nvm or a vendor repository
// can have several and the caller should not have to care.
func (d *Detector) Detect(ctx context.Context) []Version {
	if d.runner == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, detectTimeout)
	defer cancel()

	// The runner holds one allowlisted node, so there is one to ask. Probing
	// every candidate path would suggest otherwise while running the same
	// binary each time.
	if !d.runner.Available(CommandNode) {
		return nil
	}

	version, err := d.probe(ctx, firstExisting(binaryPaths))
	if err != nil {
		return nil
	}
	return []Version{version}
}

// probe asks one binary what it is.
func (d *Detector) probe(ctx context.Context, path string) (Version, error) {
	result, err := d.runner.Run(ctx, CommandNode, "--version")
	if err != nil {
		return Version{}, err
	}
	if !result.Succeeded() {
		return Version{}, os.ErrNotExist
	}

	raw := strings.TrimSpace(result.Stdout)
	match := versionPattern.FindStringSubmatch(raw)
	if match == nil {
		return Version{}, os.ErrInvalid
	}

	version := Version{
		Version:    match[1],
		Full:       strings.TrimPrefix(raw, "v"),
		BinaryPath: path,
	}

	if d.runner.Available(CommandNPM) {
		version.NPMPath = firstExisting(npmPaths)
		if out, err := d.runner.Run(ctx, CommandNPM, "--version"); err == nil && out.Succeeded() {
			version.NPMVersion = strings.TrimSpace(out.Stdout)
		}
	}
	return version, nil
}

// Lookup returns one installed version.
func (d *Detector) Lookup(ctx context.Context, version string) (Version, error) {
	if err := validate.NodeVersion(version); err != nil {
		return Version{}, err
	}

	major := validate.NodeMajor(version)
	for _, candidate := range d.Detect(ctx) {
		if candidate.Version == major {
			return candidate, nil
		}
	}
	return Version{}, ErrVersionNotInstalled
}

// CommandSpecs builds allowlist entries for the Node tooling.
//
// The path is pinned, but the entry is registered whether or not the binary is
// there yet. That is deliberate and it matters: the allowlist is fixed when the
// Agent starts, so registering only what exists would mean a runtime the panel
// had just installed could not be run until the Agent restarted — and the
// install would report failure for a package that installed perfectly well,
// which is exactly what it did before this changed.
//
// Nothing is weakened by it. The path is still a constant in this file, and
// Runner.Available checks at call time whether it is really there, so an
// operation against a runtime that is not installed is refused rather than
// attempted.
func CommandSpecs() []command.Spec {
	specs := make([]command.Spec, 0, 2)

	{
		path := firstExisting(binaryPaths)
		specs = append(specs, command.Spec{
			Name: CommandNode,
			Path: path,
			// Long enough for `node --version`, which is all this spec is used
			// for synchronously. Starting an application does not wait.
			Timeout: 15 * time.Second,
			// An application's environment is its own configuration — a
			// database URL, an API key, a feature flag — so no fixed list
			// could contain it. The boundary is shared/validate.EnvKey, which
			// every variable here has passed and which refuses the names that
			// change what runs rather than how it behaves.
			AllowAnyEnv: true,
		})
	}

	{
		path := firstExisting(npmPaths)
		specs = append(specs, command.Spec{
			Name: CommandNPM,
			Path: path,
			// Installing dependencies downloads a dependency tree, which is
			// the slowest thing this package does.
			Timeout:    15 * time.Minute,
			AllowedEnv: []string{"HOME", "USER", "npm_config_cache"},
		})
	}
	return specs
}

// firstExisting returns the first path that is there, or the first candidate.
//
// The fallback is what makes an install-then-use flow work: the canonical
// location is registered so a binary that appears later is runnable, rather
// than the Agent having to be restarted to notice it.
func firstExisting(candidates []string) string {
	for _, path := range candidates {
		if isExecutable(path) {
			return path
		}
	}
	return candidates[0]
}

// isExecutable reports whether path is a regular file with an execute bit.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}
