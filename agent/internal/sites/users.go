package sites

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strconv"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Command allowlist keys for account management.
//
// Two are needed because distributions disagree: Debian and RHEL ship the
// shadow-utils `useradd`, Alpine ships BusyBox `adduser`, and their flags are
// not compatible. The provider detects which exists rather than assuming.
const (
	CommandUseradd = "useradd"
	CommandAdduser = "adduser"
	CommandUserdel = "userdel"
	CommandDeluser = "deluser"
	// CommandAddgroup and CommandDelgroup exist only for the BusyBox path.
	// shadow-utils makes a private group with --user-group; BusyBox has no
	// such flag, so the group is created and removed as its own step.
	CommandAddgroup = "addgroup"
	CommandDelgroup = "delgroup"
)

// Errors returned by the account provider.
var (
	// ErrNoUserTool means no supported account tool is installed.
	ErrNoUserTool = errors.New("no supported user management tool is available")
	ErrUserExists = errors.New("system user already exists")
)

// nologinShells are tried in order when creating a site account.
//
// A site user must not be able to log in: it exists to own files and run a
// web process, and giving it a shell turns a stolen site password into
// interactive host access.
var nologinShells = []string{
	"/usr/sbin/nologin",
	"/sbin/nologin",
	"/bin/false",
}

// Account is a provisioned system user.
type Account struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"home"`
	// Created reports whether this call created the account, as opposed to
	// finding one already there.
	Created bool `json:"created"`
}

// UserProvider creates and removes site accounts.
type UserProvider struct {
	runner *command.Runner
}

// NewUserProvider builds a UserProvider.
func NewUserProvider(runner *command.Runner) *UserProvider {
	return &UserProvider{runner: runner}
}

// Available reports whether accounts can be managed on this host.
func (p *UserProvider) Available() bool {
	return p.runner != nil &&
		(p.runner.Available(CommandUseradd) || p.runner.Available(CommandAdduser))
}

// Lookup finds an existing account.
func (p *UserProvider) Lookup(name string) (Account, bool, error) {
	if err := validate.SystemUser(name); err != nil {
		return Account{}, false, err
	}

	found, err := user.Lookup(name)
	if err != nil {
		var unknown user.UnknownUserError
		if errors.As(err, &unknown) {
			return Account{}, false, nil
		}
		return Account{}, false, fmt.Errorf("look up user %s: %w", name, err)
	}

	uid, err := strconv.Atoi(found.Uid)
	if err != nil {
		return Account{}, false, fmt.Errorf("parse uid for %s: %w", name, err)
	}
	gid, err := strconv.Atoi(found.Gid)
	if err != nil {
		return Account{}, false, fmt.Errorf("parse gid for %s: %w", name, err)
	}

	return Account{Name: name, UID: uid, GID: gid, Home: found.HomeDir}, true, nil
}

// Ensure creates a site account if it does not already exist.
//
// It is idempotent so a retried job converges. The account is created as a
// system user with no login shell and no password, which means it cannot
// authenticate anywhere — it exists purely to own files.
func (p *UserProvider) Ensure(ctx context.Context, name, home string) (Account, error) {
	if err := validate.SystemUser(name); err != nil {
		return Account{}, err
	}
	if !p.Available() {
		return Account{}, ErrNoUserTool
	}

	if existing, found, err := p.Lookup(name); err != nil {
		return Account{}, err
	} else if found {
		return existing, nil
	}

	if err := p.create(ctx, name, home); err != nil {
		return Account{}, err
	}

	account, found, err := p.Lookup(name)
	if err != nil {
		return Account{}, err
	}
	if !found {
		// The tool reported success but the account is not resolvable. Better
		// to fail loudly than to hand back a UID of -1 and chown nothing.
		return Account{}, fmt.Errorf("user %s was not created", name)
	}

	account.Created = true
	return account, nil
}

// create runs the host's account tool.
//
// **Every site account gets a group of its own, named after it**, and that is
// not tidiness. It is the difference between per-site isolation and none:
//
//   - BusyBox `adduser -S` with no -G puts the account in *nogroup*, which is
//     shared by every service account on an Alpine host. Two customers' sites
//     would then be in one group, and anything either of them made
//     group-readable would be readable by the other.
//   - shadow-utils decides from USERGROUPS_ENAB in login.defs, so whether a
//     private group appears depends on a setting the panel does not control.
//     `--user-group` says it outright.
//   - PHP-FPM names the group in every pool it writes. Without one it refuses
//     to start — "cannot get gid for group" — which takes down PHP for every
//     site on the host, not only the new one. That is how this was found.
func (p *UserProvider) create(ctx context.Context, name, home string) error {
	shell := p.pickShell()

	// Arguments are argv entries. The name is already validated to be
	// [a-z_][a-z0-9_-]*, so it cannot be read as an option.
	if p.runner.Available(CommandUseradd) {
		// shadow-utils: --system makes it a service account, --no-create-home
		// skips home creation because the site directory is provisioned
		// separately with its own permissions, and --user-group makes the
		// private group explicit rather than inherited from login.defs.
		result, err := p.runner.Run(ctx, CommandUseradd,
			"--system", "--user-group", "--no-create-home",
			"--shell", shell, "--home-dir", home, name)
		if err != nil {
			return fmt.Errorf("create user %s: %w", name, err)
		}
		if !result.Succeeded() {
			return fmt.Errorf("useradd failed for %s: %s", name, summarizeOutput(result.Stderr))
		}
		return nil
	}

	// BusyBox has no equivalent of --user-group, so the group is created first
	// and the account is put in it. An existing group is not an error: this
	// runs again on a retried provision, and the account it belongs to may
	// have been removed while the group survived.
	if p.runner.Available(CommandAddgroup) {
		result, err := p.runner.Run(ctx, CommandAddgroup, "-S", name)
		if err != nil {
			return fmt.Errorf("create group %s: %w", name, err)
		}
		if !result.Succeeded() && !groupExists(name) {
			return fmt.Errorf("addgroup failed for %s: %s", name, summarizeOutput(result.Stderr))
		}
	}

	// BusyBox adduser: -S system, -D no password, -H no home creation.
	args := []string{"-S", "-D", "-H", "-s", shell, "-h", home}
	if groupExists(name) {
		args = append(args, "-G", name)
	}
	args = append(args, name)

	result, err := p.runner.Run(ctx, CommandAdduser, args...)
	if err != nil {
		return fmt.Errorf("create user %s: %w", name, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("adduser failed for %s: %s", name, summarizeOutput(result.Stderr))
	}
	return nil
}

// groupExists reports whether a group of this name is resolvable.
func groupExists(name string) bool {
	_, err := user.LookupGroup(name)
	return err == nil
}

// Remove deletes a site account.
//
// An absent account is not an error: deletion is idempotent so a retried
// teardown converges rather than failing on its second attempt.
func (p *UserProvider) Remove(ctx context.Context, name string) (bool, error) {
	if err := validate.SystemUser(name); err != nil {
		return false, err
	}

	if _, found, err := p.Lookup(name); err != nil {
		return false, err
	} else if !found {
		return false, nil
	}

	switch {
	case p.runner.Available(CommandUserdel):
		result, err := p.runner.Run(ctx, CommandUserdel, name)
		if err != nil {
			return false, fmt.Errorf("remove user %s: %w", name, err)
		}
		if !result.Succeeded() {
			return false, fmt.Errorf("userdel failed for %s: %s", name, summarizeOutput(result.Stderr))
		}
	case p.runner.Available(CommandDeluser):
		result, err := p.runner.Run(ctx, CommandDeluser, name)
		if err != nil {
			return false, fmt.Errorf("remove user %s: %w", name, err)
		}
		if !result.Succeeded() {
			return false, fmt.Errorf("deluser failed for %s: %s", name, summarizeOutput(result.Stderr))
		}
		// BusyBox's deluser leaves the private group behind, and a group with
		// no members is a gid that will be handed to the next account created.
		// Removing it is not fatal if it fails: the account is already gone,
		// which is what was asked for.
		if groupExists(name) && p.runner.Available(CommandDelgroup) {
			if _, err := p.runner.Run(ctx, CommandDelgroup, name); err != nil {
				return true, nil
			}
		}
	default:
		return false, ErrNoUserTool
	}

	return true, nil
}

// pickShell returns the first non-login shell present on the host.
func (p *UserProvider) pickShell() string {
	for _, shell := range nologinShells {
		if isRegularFile(shell) {
			return shell
		}
	}
	// /bin/false exists on every supported distribution; naming it here rather
	// than falling back to a real shell keeps the account non-interactive even
	// if the probe is wrong.
	return "/bin/false"
}

// maxOutputLength bounds tool output travelling in an error.
const maxOutputLength = 256

func summarizeOutput(output string) string {
	trimmed := trimSpace(output)
	if trimmed == "" {
		return "no output"
	}
	if len(trimmed) > maxOutputLength {
		return trimmed[:maxOutputLength] + "…"
	}
	return trimmed
}
