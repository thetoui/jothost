package validate

import (
	"errors"
	"fmt"
	"strings"
)

// Errors returned by deployment validation.
var (
	// ErrInvalidRemote covers a repository URL the panel will not hand to git.
	ErrInvalidRemote = errors.New("invalid repository")
	// ErrInvalidBranch covers a branch name git would read as something else.
	ErrInvalidBranch = errors.New("invalid branch")
	// ErrInvalidAction covers a deployment step outside the closed set.
	ErrInvalidAction = errors.New("invalid deployment action")
	// ErrInvalidScript covers a deployment script the panel will not store.
	ErrInvalidScript = errors.New("invalid deployment script")
)

// Bounds.
const (
	// MaxRemoteLength bounds a repository URL.
	MaxRemoteLength = 512
	// MaxBranchLength bounds a branch name.
	MaxBranchLength = 255
	// MaxDeployScript bounds a deployment script. Generous: it is a shell
	// script somebody wrote, and truncating one at a size they can see is
	// short would be a validation failure they cannot explain.
	MaxDeployScript = 64 << 10
	// MinScriptTimeout and MaxScriptTimeout bound how long a deployment may
	// run. A build that takes longer than an hour is one that has hung.
	MinScriptTimeout = 30
	MaxScriptTimeout = 3600
)

// GitRemote checks the repository URL the panel will clone from.
//
// This is the most dangerous single string in the phase, and none of the danger
// is obvious from looking at it.
//
// git's remote URL is not merely an address. The `ext::` transport runs a
// command of the caller's choosing; `file://` and a bare path read a repository
// on this host, including one somebody uploaded; and a URL that begins with a
// hyphen is not a URL at all but an *option* — `--upload-pack=...` as a remote
// is remote code execution, and it looks exactly like a typo.
//
// So this is an allowlist rather than a set of refusals. Two forms are
// accepted, which are the two forms anybody actually uses:
//
//	https://host/owner/repo.git
//	git@host:owner/repo.git        (and ssh://git@host/owner/repo.git)
//
// Everything else is refused by not being one of them, which is the property
// that matters: a transport added to a future git is refused here without
// anybody having to remember to refuse it.
func GitRemote(remote string) error {
	trimmed := strings.TrimSpace(remote)
	if trimmed == "" {
		return fmt.Errorf("%w: a repository address is required", ErrInvalidRemote)
	}
	if len(trimmed) > MaxRemoteLength {
		return fmt.Errorf("%w: a repository address may be at most %d characters",
			ErrInvalidRemote, MaxRemoteLength)
	}
	if trimmed != remote {
		return fmt.Errorf("%w: a repository address may not begin or end with a space",
			ErrInvalidRemote)
	}
	// Checked before anything else, because a value starting with "-" is an
	// argument git reads as an option however well-formed the rest of it is.
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: a repository address may not begin with a hyphen",
			ErrInvalidRemote)
	}
	for _, r := range trimmed {
		if r < 0x21 || r == 0x7f {
			return fmt.Errorf("%w: a repository address may not contain spaces or control characters",
				ErrInvalidRemote)
		}
	}

	switch {
	case strings.HasPrefix(trimmed, "https://"):
		return checkRemoteHost(strings.TrimPrefix(trimmed, "https://"))
	case strings.HasPrefix(trimmed, "ssh://"):
		return checkRemoteHost(strings.TrimPrefix(trimmed, "ssh://"))
	case strings.Contains(trimmed, "@") && strings.Contains(trimmed, ":"):
		// The scp-like form, "git@github.com:owner/repo.git". It has no scheme,
		// so it is identified by shape — and the shape is checked rather than
		// assumed, because "x@y:/etc" is the same shape.
		_, rest, _ := strings.Cut(trimmed, "@")
		host, path, found := strings.Cut(rest, ":")
		if !found || host == "" || path == "" {
			return fmt.Errorf("%w: %q is not a repository address", ErrInvalidRemote, trimmed)
		}
		if strings.HasPrefix(path, "/") {
			// An absolute path in the scp form is legal git and is nearly
			// always somebody meaning something else. It is also how a remote
			// reaches outside the account it authenticates as on the far side.
			return fmt.Errorf(
				"%w: use ssh://user@host/path for an absolute path", ErrInvalidRemote)
		}
		return checkRemoteHost(host + "/" + path)
	default:
		return fmt.Errorf(
			"%w: only https:// and ssh (git@host:owner/repo) addresses are supported, "+
				"because every other git transport can name a program to run",
			ErrInvalidRemote)
	}
}

// checkRemoteHost checks the host and path of an accepted remote form.
func checkRemoteHost(rest string) error {
	host, path, _ := strings.Cut(rest, "/")
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	if colon := strings.Index(host, ":"); colon >= 0 {
		// A port. Digits only: anything else here is not a port.
		port := host[colon+1:]
		host = host[:colon]
		if port == "" || !isNumeric(port) {
			return fmt.Errorf("%w: %q is not a port", ErrInvalidRemote, port)
		}
	}
	if host == "" {
		return fmt.Errorf("%w: the address names no host", ErrInvalidRemote)
	}
	if err := Domain(host); err != nil {
		// Not a domain: it may still be an address, but a repository host that
		// is neither is not something to hand to git.
		if MatchAddress(host) != nil {
			return fmt.Errorf("%w: %q is not a host name", ErrInvalidRemote, host)
		}
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: the address names no repository", ErrInvalidRemote)
	}
	// ".." in the path of a remote is how a request reaches a repository the
	// operator did not name, on a server that resolves it.
	for _, segment := range strings.Split(path, "/") {
		if segment == ".." {
			return fmt.Errorf("%w: a repository path may not contain '..'", ErrInvalidRemote)
		}
	}
	return nil
}

// GitBranch checks a branch name.
//
// git's own rules, narrowed. What is refused here is what git itself refuses
// (".." , a trailing ".lock", a leading dot) plus what an argv would misread: a
// name starting with a hyphen is an option, and a name containing a colon is
// half of a refspec.
func GitBranch(branch string) error {
	trimmed := strings.TrimSpace(branch)
	if trimmed == "" {
		return fmt.Errorf("%w: a branch is required", ErrInvalidBranch)
	}
	if len(trimmed) > MaxBranchLength {
		return fmt.Errorf("%w: a branch name may be at most %d characters",
			ErrInvalidBranch, MaxBranchLength)
	}
	if strings.HasPrefix(trimmed, "-") {
		return fmt.Errorf("%w: a branch name may not begin with a hyphen", ErrInvalidBranch)
	}
	if strings.HasPrefix(trimmed, ".") || strings.HasSuffix(trimmed, ".") {
		return fmt.Errorf("%w: a branch name may not begin or end with a dot", ErrInvalidBranch)
	}
	if strings.HasSuffix(trimmed, ".lock") || strings.HasSuffix(trimmed, "/") {
		return fmt.Errorf("%w: %q is not a name git will accept", ErrInvalidBranch, trimmed)
	}
	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "@{") {
		return fmt.Errorf("%w: %q is a revision expression, not a branch", ErrInvalidBranch, trimmed)
	}
	for _, r := range trimmed {
		switch {
		case r <= 0x20, r == 0x7f:
			return fmt.Errorf("%w: a branch name may not contain spaces or control characters",
				ErrInvalidBranch)
		case r == '~', r == '^', r == ':', r == '?', r == '*', r == '[', r == '\\':
			return fmt.Errorf("%w: a branch name may not contain %q", ErrInvalidBranch, string(r))
		}
	}
	return nil
}

// CommitSHA checks a commit identifier.
//
// Full or abbreviated, hexadecimal only. It reaches git as a revision, and a
// revision is a small language: "HEAD@{1}", "main^", ":/fix" are all valid
// revisions and none of them is what a caller naming a commit meant.
func CommitSHA(sha string) error {
	trimmed := strings.TrimSpace(sha)
	if len(trimmed) < 7 || len(trimmed) > 40 {
		return fmt.Errorf("%w: a commit is 7 to 40 hexadecimal characters", ErrInvalidAction)
	}
	for _, r := range trimmed {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return fmt.Errorf("%w: %q is not a commit identifier", ErrInvalidAction, trimmed)
		}
	}
	return nil
}

// The deployment steps the panel knows how to run itself.
//
// A closed set, and each one becomes an argv the Agent builds — nothing from a
// request is ever part of the command line. This is the path the UI offers
// first and the path that covers nearly every real deployment; the custom
// script below is the exception, and it is bounded differently.
const (
	// ActionComposerInstall installs PHP dependencies for production.
	ActionComposerInstall = "composer.install"
	// ActionNpmCI installs Node dependencies from the lockfile.
	ActionNpmCI = "npm.ci"
	// ActionNpmInstall installs Node dependencies without one.
	ActionNpmInstall = "npm.install"
	// ActionNpmBuild runs the project's build script.
	ActionNpmBuild = "npm.build"
	// ActionArtisanMigrate runs Laravel's migrations, non-interactively.
	ActionArtisanMigrate = "artisan.migrate"
	// ActionArtisanOptimise rebuilds Laravel's caches.
	ActionArtisanOptimise = "artisan.optimise"
	// ActionScript runs the deployment script recorded against the repository.
	ActionScript = "script"
)

// DeployActions is the supported set, in the order a UI should offer them —
// which is also the order they are usually wanted in.
var DeployActions = []string{
	ActionComposerInstall,
	ActionNpmCI,
	ActionNpmInstall,
	ActionNpmBuild,
	ActionArtisanMigrate,
	ActionArtisanOptimise,
	ActionScript,
}

// DeployAction checks a deployment step.
func DeployAction(kind string) error {
	for _, known := range DeployActions {
		if kind == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidAction, kind, strings.Join(DeployActions, ", "))
}

// DeployScript checks the script a deployment may run.
//
// Length and control characters only, deliberately. There is no attempt to
// parse it, forbid particular commands, or detect "dangerous" ones: a
// denylist over a shell language is a denylist somebody gets around, and
// pretending otherwise would be worse than not trying — it would suggest a
// safety that is not there.
//
// What actually bounds this script is where and how it runs, not what it says:
// as the website's own unprivileged account, in that website's own directory,
// with a timeout, with its output captured, and written to a file rather than
// passed on a command line. See agent/internal/deploy/script.go.
//
// A NUL is refused because it truncates the file at the C library, so the shell
// would run a prefix of what the operator wrote — which is the one way this
// text could mean something other than it says.
func DeployScript(script string) error {
	if len(script) > MaxDeployScript {
		return fmt.Errorf("%w: a deployment script may be at most %d bytes",
			ErrInvalidScript, MaxDeployScript)
	}
	if strings.ContainsRune(script, 0) {
		return fmt.Errorf("%w: a deployment script may not contain a null byte",
			ErrInvalidScript)
	}
	return nil
}

// ScriptTimeout checks how long a deployment may run.
func ScriptTimeout(seconds int) error {
	if seconds < MinScriptTimeout || seconds > MaxScriptTimeout {
		return fmt.Errorf("%w: a deployment timeout must be between %d and %d seconds",
			ErrInvalidScript, MinScriptTimeout, MaxScriptTimeout)
	}
	return nil
}

// The forges the panel knows how to verify a webhook from.
const (
	// ProviderGitHub signs with HMAC-SHA256 over the body, in
	// X-Hub-Signature-256.
	ProviderGitHub = "github"
	// ProviderGitLab sends the secret itself, in X-Gitlab-Token.
	ProviderGitLab = "gitlab"
	// ProviderGeneric accepts the GitHub scheme without the vendor header
	// names, for anything that can be pointed at a URL.
	ProviderGeneric = "generic"
	// ProviderNone means this repository has no webhook: deployments are
	// started by a person.
	ProviderNone = "none"
)

// DeployProviders is the supported set.
var DeployProviders = []string{
	ProviderNone, ProviderGitHub, ProviderGitLab, ProviderGeneric,
}

// DeployProvider checks a webhook provider.
func DeployProvider(provider string) error {
	for _, known := range DeployProviders {
		if provider == known {
			return nil
		}
	}
	return fmt.Errorf("%w: %q is not one of %s",
		ErrInvalidAction, provider, strings.Join(DeployProviders, ", "))
}

// The ways a deployment can be started.
const (
	TriggerManual   = "manual"
	TriggerWebhook  = "webhook"
	TriggerRollback = "rollback"
)

// DeployTrigger checks how a deployment was started.
func DeployTrigger(trigger string) error {
	switch trigger {
	case TriggerManual, TriggerWebhook, TriggerRollback:
		return nil
	default:
		return fmt.Errorf("%w: %q is not a deployment trigger", ErrInvalidAction, trigger)
	}
}
