package deploy

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// gitOptions are the per-invocation extras.
type gitOptions struct {
	// UseKey authenticates with this website's deploy key.
	UseKey bool
	// Timeout overrides the spec's, for the two operations that reach the
	// network.
	Timeout int
}

// git runs one git command in a website's working tree, as that website.
//
// Three things about the invocation matter more than the arguments:
//
//   - It runs as the site account. Not tidiness: git refuses to operate on a
//     repository owned by somebody else, so running as root would report every
//     working tree as broken — and a clone done as root would leave files the
//     site cannot write, so its own cache directory would fail at the first
//     request.
//   - `-C <dir>` rather than a working directory, so the repository is named
//     once and every subcommand acts on the same one.
//   - GIT_TERMINAL_PROMPT=0. Without it, a private repository this host cannot
//     authenticate to does not fail — it *asks for a username*, on a terminal
//     nobody is attached to, and the deployment hangs until its timeout. That
//     is the difference between a deployment that fails in two seconds with a
//     readable message and one that fails in ten minutes with none.
func (p *Provider) git(ctx context.Context, owner account, root string,
	opts gitOptions, args ...string,
) (command.Result, error) {
	environment := map[string]string{
		"HOME":                owner.Home,
		"GIT_TERMINAL_PROMPT": "0",
		// The panel's git is the panel's: a system-wide gitconfig on the host
		// could set core.hooksPath, credential helpers, or url rewrites, and
		// every one of those changes what a deployment does.
		"GIT_CONFIG_NOSYSTEM": "1",
	}
	if opts.UseKey {
		environment["GIT_SSH_COMMAND"] = p.sshCommand(owner.Name)
	}

	full := append([]string{"-C", root}, args...)
	return p.runner.RunWith(ctx, CommandGit, command.Options{
		Env:           environment,
		Dir:           root,
		UID:           owner.UID,
		GID:           owner.GID,
		SetCredential: true,
	}, full...)
}

// Clone puts a repository on the host for the first time.
//
// The document root already exists — a website operation created it, with its
// ownership and permissions — so this clones *into* it rather than creating it.
// git refuses to clone into a directory that is not empty, which is the common
// case here because every new website is created with an index page in it, so
// the repository is initialised and pulled rather than cloned.
//
// That is not a workaround. It is what makes deploying into an existing site
// possible at all, and it is why the first deployment says plainly that it
// overwrites what is there.
func (p *Provider) Clone(ctx context.Context, req Request, report func(string)) error {
	owner, err := p.lookupAccount(req.Account)
	if err != nil {
		return err
	}
	if err := validate.GitRemote(req.Remote); err != nil {
		return err
	}
	if err := validate.GitBranch(req.Branch); err != nil {
		return err
	}
	if !isDir(req.DocumentRoot) {
		return fmt.Errorf("%w: %s is not a directory", ErrNotCloned, req.DocumentRoot)
	}

	if err := p.ensureDirs(); err != nil {
		return err
	}

	report("Preparing the repository")
	// init is idempotent: on a directory that is already a repository it
	// reports the existing one and changes nothing.
	if result, err := p.git(ctx, owner, req.DocumentRoot, gitOptions{}, "init", "-q"); err != nil {
		return wrap("initialise the repository", err)
	} else if !result.Succeeded() {
		return fmt.Errorf("initialise the repository: %s", firstLine(result.Stderr, result.Stdout))
	}

	// The remote, set rather than added, so re-running this against a
	// repository that already has one converges instead of failing.
	if err := p.setRemote(ctx, owner, req.DocumentRoot, req.Remote); err != nil {
		return err
	}

	report("Fetching " + req.Branch)
	return p.fetch(ctx, owner, req)
}

// setRemote points the working tree at a repository.
func (p *Provider) setRemote(ctx context.Context, owner account, root, remote string) error {
	if err := validate.GitRemote(remote); err != nil {
		return err
	}
	existing, err := p.git(ctx, owner, root, gitOptions{}, "remote", "get-url", "origin")
	if err != nil {
		return wrap("read the repository's remote", err)
	}

	verb := "add"
	if existing.Succeeded() {
		if strings.TrimSpace(existing.Stdout) == remote {
			return nil
		}
		verb = "set-url"
	}
	// "--" so a remote that somehow reached here beginning with a hyphen is
	// still an argument rather than an option. Validation has already refused
	// one; this is the second lock on the same door, on the one argument in
	// this package that is genuinely dangerous.
	result, err := p.git(ctx, owner, root, gitOptions{}, "remote", verb, "origin", "--", remote)
	if err != nil {
		return wrap("set the repository's remote", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("set the repository's remote: %s",
			firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// fetch brings the branch up to date without touching the working tree.
func (p *Provider) fetch(ctx context.Context, owner account, req Request) error {
	result, err := p.git(ctx, owner, req.DocumentRoot,
		gitOptions{UseKey: req.UseDeployKey, Timeout: fetchTimeout},
		"fetch", "--prune", "origin", "--", req.Branch)
	if err != nil {
		return wrap("fetch from the repository", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrRemoteFailed, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// Checkout moves the working tree onto a commit.
//
// A hard reset rather than a merge or a pull. A deployment is not a
// collaboration: the repository is the truth and the working tree is a copy of
// it, so anything that could produce a conflict — and stop halfway, leaving the
// site serving a half-merged tree — is the wrong operation. The panel reports
// uncommitted changes before it does this, because this destroys them.
func (p *Provider) Checkout(ctx context.Context, req Request, target string) (string, error) {
	owner, err := p.lookupAccount(req.Account)
	if err != nil {
		return "", err
	}
	// No "--" before the revision. It reads as a path separator here, and
	// "reset --hard -- <sha>" is refused with "Cannot do hard reset with
	// paths" — which is git telling the truth about what it was asked.
	//
	// What keeps this argument safe is that it is not a caller's string: it is
	// either a commit that has been through validate.CommitSHA, which permits
	// only hexadecimal, or "origin/<branch>" built here from a branch that has
	// been through validate.GitBranch, which refuses a leading hyphen.
	result, err := p.git(ctx, owner, req.DocumentRoot, gitOptions{},
		"reset", "--hard", target)
	if err != nil {
		return "", wrap("check out "+target, err)
	}
	if !result.Succeeded() {
		return "", fmt.Errorf("check out %s: %s", target, firstLine(result.Stderr, result.Stdout))
	}
	return p.gitOutput(ctx, owner, req.DocumentRoot, "rev-parse", "HEAD"), nil
}

// resolve turns what the request asked for into a commit.
//
// A pinned commit is used as given; otherwise the tip of the branch as the
// *remote* has it, which is deliberately not the local branch: after a fetch,
// origin/<branch> is what the repository says and the local branch is whatever
// this host last did.
func (p *Provider) resolve(ctx context.Context, owner account, req Request) (string, error) {
	if req.Commit != "" {
		if err := validate.CommitSHA(req.Commit); err != nil {
			return "", err
		}
		return req.Commit, nil
	}
	target := "origin/" + req.Branch
	sha := p.gitOutput(ctx, owner, req.DocumentRoot, "rev-parse", "--verify", target)
	if sha == "" {
		return "", fmt.Errorf("%w: the repository has no branch called %q",
			ErrRemoteFailed, req.Branch)
	}
	return sha, nil
}

// describe reads the subject and author of a commit, for the record.
func (p *Provider) describe(ctx context.Context, owner account, root, sha string) (string, string) {
	message := p.gitOutput(ctx, owner, root, "log", "-1", "--pretty=%s", sha)
	author := p.gitOutput(ctx, owner, root, "log", "-1", "--pretty=%an", sha)
	const maxMessage = 500
	if len(message) > maxMessage {
		message = message[:maxMessage]
	}
	return message, author
}

// isClean reports whether the working tree has uncommitted changes.
func (p *Provider) isClean(ctx context.Context, owner account, root string) bool {
	return p.gitOutput(ctx, owner, root, "status", "--porcelain") == ""
}

// removeWorkingTree deletes the repository metadata from a document root.
//
// The *files* are left. Disconnecting a site from its repository should stop it
// being deployed, not take it offline — and a panel that deleted a customer's
// document root as a side effect of an unlink would be doing something nobody
// asked for.
func (p *Provider) removeWorkingTree(root string) error {
	if root == "" || !strings.HasPrefix(root, "/") {
		return fmt.Errorf("%w: no working tree to remove", ErrNotCloned)
	}
	gitDir := root + "/.git"
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return nil
	}
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("remove the repository metadata: %w", err)
	}
	return nil
}

// Unlink disconnects a website from its repository.
//
// The repository metadata goes and the files stay: the site keeps serving
// exactly what it was serving a moment ago, and stops being deployable. That is
// what "unlink" should mean, and a panel that deleted a document root as a side
// effect of it would be doing something nobody asked for.
//
// The deploy key goes only if asked. Keeping it is the right default when a
// repository is being repointed rather than abandoned — regenerating one means
// somebody has to go and change the forge.
func (p *Provider) Unlink(accountName, root string, removeKey bool) error {
	if err := validate.SystemUser(accountName); err != nil {
		return err
	}
	if err := p.removeWorkingTree(root); err != nil {
		return err
	}
	if removeKey {
		return p.RemoveKey(accountName)
	}
	return nil
}
