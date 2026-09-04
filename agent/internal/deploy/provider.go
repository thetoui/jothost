package deploy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Provider deploys websites from git.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	paths  Paths
}

// Options configure a Provider.
type Options struct {
	Runner *command.Runner
	Log    *slog.Logger
	Paths  Paths
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Provider{runner: opts.Runner, log: log, paths: opts.Paths.withDefaults()}
}

// Paths reports where this Provider keeps its files.
func (p *Provider) Paths() Paths { return p.paths }

// Available reports whether this host can deploy from git.
func (p *Provider) Available() bool {
	return p != nil && p.runner != nil && p.runner.Available(CommandGit)
}

// gitVersion reads the version git reports about itself.
func (p *Provider) gitVersion(ctx context.Context) string {
	if !p.Available() {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandGit, "--version")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "git version 2.47.3".
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// account is a website's own system account.
type account struct {
	Name string
	UID  int
	GID  int
	Home string
}

// lookupAccount resolves the account a deployment runs as.
//
// Refusals here are refusals to deploy, and both of them are deliberate. An
// account that does not exist would leave the checkout owned by whoever the
// Agent is, which is root — so the site could not write its own cache. An
// account that resolves to root would run a customer's build script as root,
// which is the one outcome this whole package is arranged to prevent.
func (p *Provider) lookupAccount(name string) (account, error) {
	if err := validate.SystemUser(name); err != nil {
		return account{}, err
	}
	entry, err := user.Lookup(name)
	if err != nil {
		return account{}, fmt.Errorf("%w: %s", ErrNoAccount, name)
	}
	uid, uidErr := strconv.Atoi(entry.Uid)
	gid, gidErr := strconv.Atoi(entry.Gid)
	if uidErr != nil || gidErr != nil {
		return account{}, fmt.Errorf("%w: %s has no numeric identity", ErrNoAccount, name)
	}
	if uid == 0 || gid == 0 {
		// Refused rather than repaired. An account resolving to root is a
		// misconfiguration somewhere above this, and running a customer's
		// build as root would be the worst possible way to find out.
		return account{}, fmt.Errorf("%w: %s resolves to uid %d", ErrRootRefused, name, uid)
	}
	home := entry.HomeDir
	if home == "" || !isDir(home) {
		home = "/tmp"
	}
	return account{Name: name, UID: uid, GID: gid, Home: home}, nil
}

// ensureDirs creates the directories this package owns.
//
// 0711 and root-owned, which is the unusual mode and the deliberate one:
// **traverse but do not list**.
//
// Everything in here is read by an unprivileged account — git reads the deploy
// key as the website's own user, and the shell reads the deployment script as
// the same — so a 0700 root-owned directory makes every one of those files
// unreachable. That does not fail as a permission error on the file: ssh
// reports "Identity file not accessible", and the deployment fails at
// authentication looking exactly like a key the forge has not been given. It
// was found by watching a real fetch fail.
//
// The execute bit alone is what makes it safe to open. A process can open a
// path it already knows — its own key, whose name is its own account — and
// cannot read the directory to discover anybody else's. The files themselves
// are 0600 and owned by the account that needs them, so knowing another site's
// filename would not help either.
func (p *Provider) ensureDirs() error {
	for _, dir := range []string{p.paths.StateDir, p.paths.KeyDir(), p.paths.ScriptDir()} {
		if err := os.MkdirAll(dir, 0o711); err != nil {
			return fmt.Errorf("create the deployment directory %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o711); err != nil {
			return fmt.Errorf("secure the deployment directory %s: %w", dir, err)
		}
	}
	return nil
}

// Status reports what this host has for one website.
func (p *Provider) Status(ctx context.Context, accountName, root string) Status {
	status := Status{Tools: map[string]bool{}}

	if !p.Available() {
		status.Reason = ErrUnavailable.Error()
		return status
	}
	status.Available = true
	status.GitVersion = p.gitVersion(ctx)

	// Which template actions this host could actually run. Reported up front
	// rather than discovered at the end of a five-minute deployment.
	status.Tools[validate.ActionComposerInstall] = p.runner.Available(CommandComposer)
	status.Tools[validate.ActionNpmCI] = p.runner.Available(CommandNpm)
	status.Tools[validate.ActionNpmInstall] = p.runner.Available(CommandNpm)
	status.Tools[validate.ActionNpmBuild] = p.runner.Available(CommandNpm)
	status.Tools[validate.ActionArtisanMigrate] = p.runner.Available(CommandPHP)
	status.Tools[validate.ActionArtisanOptimise] = p.runner.Available(CommandPHP)
	status.Tools[validate.ActionScript] = p.runner.Available(CommandDeployShell)

	if accountName == "" || root == "" {
		return status
	}

	if info, err := os.Stat(root + "/.git"); err != nil || !info.IsDir() {
		return status
	}
	status.Cloned = true

	owner, err := p.lookupAccount(accountName)
	if err != nil {
		status.Warnings = append(status.Warnings,
			"the website has no usable system account, so nothing can be deployed: "+err.Error())
		return status
	}

	status.Commit = p.gitOutput(ctx, owner, root, "rev-parse", "HEAD")
	status.Branch = p.gitOutput(ctx, owner, root, "rev-parse", "--abbrev-ref", "HEAD")
	status.Message = p.gitOutput(ctx, owner, root, "log", "-1", "--pretty=%s")
	status.Author = p.gitOutput(ctx, owner, root, "log", "-1", "--pretty=%an")
	status.Remote = p.gitOutput(ctx, owner, root, "config", "--get", "remote.origin.url")

	// Uncommitted changes, which a deployment would destroy. Named rather than
	// counted: "dirty" on its own is alarming and not actionable.
	if dirty := p.gitOutput(ctx, owner, root, "status", "--porcelain"); dirty != "" {
		status.Dirty = true
		for i, line := range strings.Split(dirty, "\n") {
			if i >= 10 {
				break
			}
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				status.DirtyFiles = append(status.DirtyFiles, trimmed)
			}
		}
		status.Warnings = append(status.Warnings,
			"this working tree has uncommitted changes; a deployment resets it and they "+
				"would be lost")
	}

	keyPath := p.paths.KeyPath(accountName)
	if info, err := os.Stat(keyPath); err == nil && info.Size() > 0 {
		status.HasKey = true
		status.Fingerprint = p.fingerprint(ctx, keyPath)
	}
	if !status.HasKey && strings.HasPrefix(status.Remote, "git@") {
		status.Warnings = append(status.Warnings,
			"this repository is reached over SSH and this host has no deploy key for it, "+
				"so every deployment will fail to authenticate")
	}

	return status
}

// gitOutput runs a read-only git command and returns its first line.
//
// Read-only: every caller here passes a fixed subcommand. It is run as the site
// account rather than as root, which is not only tidiness — git refuses to
// operate on a repository owned by somebody else, so running these as root
// would report every working tree as broken.
func (p *Provider) gitOutput(ctx context.Context, owner account, root string, args ...string) string {
	result, err := p.git(ctx, owner, root, gitOptions{}, args...)
	if err != nil || !result.Succeeded() {
		return ""
	}
	return strings.TrimRight(result.Stdout, "\n")
}

// fingerprint reads a key's fingerprint, for display.
func (p *Provider) fingerprint(ctx context.Context, keyPath string) string {
	if !p.runner.Available(CommandSSHKeygen) {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandSSHKeygen, "-l", "-f", keyPath)
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "256 SHA256:abc... comment (ED25519)" — the fingerprint field.
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	if len(fields) < 2 {
		return ""
	}
	return fields[1]
}

// isDir reports whether a path is a directory.
func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// firstLine returns the first non-empty line of the first non-empty input.
func firstLine(candidates ...string) string {
	for _, candidate := range candidates {
		for _, line := range strings.Split(candidate, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// wrap adds what was being attempted to an error from the command runner.
func wrap(what string, err error) error {
	if errors.Is(err, command.ErrNotAllowed) || errors.Is(err, command.ErrUnavailable) {
		return fmt.Errorf("%w (%s)", ErrUnavailable, what)
	}
	return fmt.Errorf("%s: %w", what, err)
}
