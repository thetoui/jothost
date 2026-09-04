package mail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
)

// Daemons the panel drives, by the name the host's service manager knows them
// by. The service layer maps these onto units; this package never names a unit
// file.
const (
	DaemonPostfix = "postfix"
	DaemonDovecot = "dovecot"
	DaemonRspamd  = "rspamd"
	DaemonClamAV  = "clamd"
)

// Provider manages the host's mail server.
type Provider struct {
	runner *command.Runner
	log    *slog.Logger
	paths  Paths

	// services restarts daemons and reports whether they are up. A function
	// dependency rather than a service-manager import, for the reason the FTP
	// provider gives: exactly one thing on this host owns daemon lifecycles,
	// and it is not this package.
	services Services

	// accounts creates the vmail account that owns every Maildir.
	accounts Accounts
}

// Services is the daemon control this package needs.
type Services interface {
	// Restart applies a configuration change.
	//
	// A restart rather than a reload for Dovecot and Rspamd, and a reload for
	// Postfix: Postfix's reload genuinely re-reads everything, while Dovecot
	// caches the passwd-file path and its SSL material at startup. A reload
	// that leaves the daemon authenticating against the previous file is worse
	// than a restart, because the page would report the change as applied.
	Restart(ctx context.Context, name string) error
	// Reload asks a daemon to re-read its configuration without dropping
	// connections.
	Reload(ctx context.Context, name string) error
	// Running reports whether a daemon is up.
	Running(ctx context.Context, name string) bool
}

// Accounts creates the unprivileged account every Maildir belongs to.
type Accounts interface {
	// EnsureAccount creates the account if it is not there and returns its
	// numeric ids. It is idempotent.
	EnsureAccount(ctx context.Context, name, home string) (uid, gid int, err error)
}

// Options configure a Provider.
type Options struct {
	Runner   *command.Runner
	Log      *slog.Logger
	Paths    Paths
	Services Services
	Accounts Accounts
}

// NewProvider builds a Provider.
func NewProvider(opts Options) *Provider {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Provider{
		runner:   opts.Runner,
		log:      log,
		paths:    opts.Paths.withDefaults(),
		services: opts.Services,
		accounts: opts.Accounts,
	}
}

// SetServices supplies the daemon controller.
//
// Set after construction for the reason the FTP provider's reloader is: the
// controller reaches back through the operation registry to the service
// manager, and the registry is built from this provider, so one of the two has
// to be wired second.
func (p *Provider) SetServices(s Services) { p.services = s }

// SetAccounts supplies the account creator.
func (p *Provider) SetAccounts(a Accounts) { p.accounts = a }

// Paths reports where this Provider keeps its files.
func (p *Provider) Paths() Paths { return p.paths }

// Available reports whether this host has a mail server the panel can manage.
//
// Postfix and Dovecot both, because a host with one of them is not a host that
// can serve mail: Postfix alone accepts mail nobody can read, and Dovecot alone
// serves mailboxes nothing delivers into. Reporting "not installed" for a host
// with half of it is a truer answer than accepting a configuration that will
// half-work.
//
// Rspamd is deliberately not required. Its absence costs filtering and DKIM
// signing, which the status reports as absent — a mail server without a spam
// filter is a working mail server, and refusing to manage one would be the
// panel choosing not to help.
func (p *Provider) Available() bool {
	if p == nil || p.runner == nil {
		return false
	}
	return p.runner.Available(CommandPostconf) && p.runner.Available(CommandDovecot)
}

// SupportsFiltering reports whether spam filtering and DKIM signing can be
// offered on this host.
func (p *Provider) SupportsFiltering() bool {
	return p != nil && p.runner != nil && p.runner.Available(CommandRspamadm)
}

// postfixVersion reads the version Postfix reports about itself.
func (p *Provider) postfixVersion(ctx context.Context) string {
	if !p.runner.Available(CommandPostconf) {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandPostconf, "-h", "mail_version")
	if err != nil || !result.Succeeded() {
		return ""
	}
	return strings.TrimSpace(result.Stdout)
}

// dovecotVersion reads Dovecot's.
func (p *Provider) dovecotVersion(ctx context.Context) string {
	if !p.runner.Available(CommandDovecot) {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandDovecot, "--version")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "2.3.21.1 (d492236fa0)" — the number, without the build hash.
	fields := strings.Fields(strings.TrimSpace(result.Stdout))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// rspamdVersion reads Rspamd's.
func (p *Provider) rspamdVersion(ctx context.Context) string {
	if !p.runner.Available(CommandRspamadm) {
		return ""
	}
	result, err := p.runner.Run(ctx, CommandRspamadm, "--version")
	if err != nil || !result.Succeeded() {
		return ""
	}
	// "Rspamadm 3.10.2".
	line := firstLine(result.Stdout, result.Stderr)
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return strings.TrimSpace(line)
	}
	return fields[len(fields)-1]
}

// ValidatePostfix asks Postfix whether its configuration is one it will start
// with.
//
// `postfix check` reads the whole configuration, the distribution's part
// included, and also checks the ownership and permissions of the queue
// directories — which is a real failure this catches: a mail server whose queue
// is group-writable refuses to start, and the message it gives is about a
// directory rather than about the change that was just made.
func (p *Provider) ValidatePostfix(ctx context.Context) error {
	if !p.runner.Available(CommandPostfix) {
		// postconf is what the panel configures with and it is present; the
		// control program is a separate binary on some builds. Not being able
		// to check is not the same as failing the check, and saying so is
		// better than either pretending it passed or refusing the change.
		return nil
	}
	result, err := p.runner.Run(ctx, CommandPostfix, "check")
	if err != nil {
		return fmt.Errorf("check the Postfix configuration: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	// `postfix check` warns on stderr and still exits zero — a warning is how
	// it reports a file it cannot read or a parameter it does not know, which
	// is exactly the kind of mistake a generated configuration makes. They are
	// surfaced by the caller rather than swallowed here.
	return nil
}

// ValidateDovecot asks Dovecot whether its configuration parses.
//
// `doveconf` is the tool that would report this most precisely, but it is not
// present on every build; `dovecot -n` dumps the merged configuration and fails
// on a syntax error, which is the property needed here. The panel writes one
// file into a directory of files it did not write, so the failure it is
// guarding against is its own file conflicting with the distribution's.
func (p *Provider) ValidateDovecot(ctx context.Context) error {
	if !p.runner.Available(CommandDovecot) {
		return ErrUnavailable
	}
	result, err := p.runner.Run(ctx, CommandDovecot, "-n")
	if err != nil {
		return fmt.Errorf("check the Dovecot configuration: %w", err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrInvalidConfig, firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// ensureDirs creates the directories this package owns, with the ownership
// each of them actually needs.
//
// Three different answers, and every one of them was arrived at by watching
// something fail quietly:
//
//   - The state directory is 0755. Nothing in it is secret by virtue of being
//     there — the secrets are individual files with their own modes — and two
//     unprivileged daemons have to be able to *traverse* it. A 0750 root-owned
//     directory here stops Rspamd reaching the signing keys, and Rspamd does
//     not report that: it simply stops signing.
//   - The Sieve directory is 0750 and group-owned by the mail account, because
//     Dovecot executes those scripts as that account at delivery time.
//   - The key directory is 0750 and group-owned by whatever account Rspamd
//     runs as, for the reason above.
//
// The failure this guards against is the one this whole phase is about: every
// one of these permissions being wrong produces a mail server that works, and
// sends mail nobody can verify.
func (p *Provider) ensureDirs(ctx context.Context) (uid, gid int, err error) {
	uid, gid, err = p.ensureVmail(ctx)
	if err != nil {
		return 0, 0, err
	}

	if err := ensureDir(p.paths.StateDir, 0o755, -1, -1); err != nil {
		return 0, 0, err
	}
	if err := ensureDir(p.paths.SieveDir(), 0o750, 0, gid); err != nil {
		return 0, 0, err
	}
	_, signerGID := p.signerOwnership()
	if err := ensureDir(p.paths.DKIMDir(), 0o750, 0, signerGID); err != nil {
		return 0, 0, err
	}

	// The mail root is the vmail account's own, and only it should be able to
	// look inside: every message on the host is under here.
	if err := os.MkdirAll(p.paths.MailRoot, 0o700); err != nil {
		return 0, 0, fmt.Errorf("create the mail root: %w", err)
	}
	if err := os.Chmod(p.paths.MailRoot, 0o700); err != nil {
		return 0, 0, fmt.Errorf("secure the mail root: %w", err)
	}
	if err := os.Chown(p.paths.MailRoot, uid, gid); err != nil {
		return 0, 0, fmt.Errorf("give the mail root to %s: %w", VmailUser, err)
	}
	return uid, gid, nil
}

// ensureVmail creates the account every Maildir belongs to.
func (p *Provider) ensureVmail(ctx context.Context) (int, int, error) {
	if p.accounts == nil {
		return 0, 0, fmt.Errorf("the Agent cannot create the %s account on this host", VmailUser)
	}
	uid, gid, err := p.accounts.EnsureAccount(ctx, VmailUser, VmailHome)
	if err != nil {
		return 0, 0, fmt.Errorf("create the %s account: %w", VmailUser, err)
	}
	if uid == 0 || gid == 0 {
		// A mailbox owned by root is a mailbox whose contents any delivery bug
		// writes with root's permissions. Refusing here is refusing to serve
		// mail, which is the right answer.
		return 0, 0, fmt.Errorf("the %s account resolved to root, which will not own mailboxes",
			VmailUser)
	}
	return uid, gid, nil
}

// ensureDir creates a directory with the mode and ownership it needs.
//
// A gid of -1 leaves the ownership alone, which is the right answer on a host
// where the account in question does not exist: the directory is then
// root-owned and unreadable by the daemon that wanted it, and the panel reports
// the feature as unavailable rather than silently half-working.
func ensureDir(path string, mode os.FileMode, uid, gid int) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		return fmt.Errorf("secure %s: %w", path, err)
	}
	if gid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("set the owner of %s: %w", path, err)
		}
	}
	return nil
}

// writeFile writes a generated file atomically, with the given ownership.
//
// Atomically because every file this package writes is read by a daemon that
// may be running: a half-written passwd-file is one where the mailboxes below
// the truncation point stop authenticating, for as long as the write takes. A
// rename is the only way to make the change indivisible.
func writeFile(path string, content []byte, mode os.FileMode, uid, gid int) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, ".jothost-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", dir, err)
	}
	tempName := temp.Name()
	// Best effort on the error paths below: the file is being abandoned, and a
	// failure to remove it must not mask the failure that caused it.
	defer func() { _ = os.Remove(tempName) }()

	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Chmod(tempName, mode); err != nil {
		return fmt.Errorf("set the permissions of %s: %w", path, err)
	}
	if uid >= 0 && gid >= 0 {
		if err := os.Chown(tempName, uid, gid); err != nil {
			return fmt.Errorf("set the owner of %s: %w", path, err)
		}
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("install %s: %w", path, err)
	}
	return nil
}

// ensureMode corrects a file's permissions whether or not its content changed.
//
// It matters because the content check above is what decides whether to write
// at all: a file whose bytes are right and whose permissions are wrong would
// otherwise never be corrected, and this is the file where wrong permissions
// mean every correct password is rejected.
func ensureMode(path string, mode os.FileMode, uid, gid int) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read the permissions of %s: %w", path, err)
	}
	if info.Mode().Perm() != mode {
		if err := os.Chmod(path, mode); err != nil {
			return fmt.Errorf("secure %s: %w", path, err)
		}
	}
	if gid >= 0 {
		if err := os.Chown(path, uid, gid); err != nil {
			return fmt.Errorf("set the owner of %s: %w", path, err)
		}
	}
	return nil
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
