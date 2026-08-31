package ftp

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// The virtual-user password file.
//
// It is a passwd(5)-format file — name:hash:uid:gid:gecos:home:shell — that
// only proftpd reads. It is maintained with ftpasswd rather than written here
// because ftpasswd owns the hash format, and a panel that reimplemented that
// would be one release away from a file nobody can log in against.
//
// It is read directly, though. Listing accounts by shelling out would be a
// process per page load to parse a file this package already knows the shape
// of, and there is no ftpasswd verb that lists.

// hashAlgorithm is what the passwords are stored under.
//
// SHA-512, not ftpasswd's default. Its default is MD5-crypt, which is a hash a
// modern GPU tries at a rate measured in billions per second — and this is a
// file of password hashes for accounts that reach customer files.
const hashAlgorithm = "--sha512"

// nologinShell is the shell every virtual account carries.
//
// It is not a real shell and it does not need to be: nothing on this host
// authenticates against this file except proftpd, and proftpd is told not to
// check (RequireValidShell off). Writing a real shell here would be writing
// something that looks like a login where none exists.
const nologinShell = "/sbin/nologin"

// Account is one entry in the password file, as read back from it.
//
// It carries no password: the panel never has one after it is written, and a
// hash is not something to hand to the API layer.
type Account struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"home"`
	// Locked reports an account ftpasswd has disabled. A locked account keeps
	// its home and its mapping, so unlocking restores exactly what was there.
	Locked bool `json:"locked"`
}

// Accounts reads the password file.
//
// A missing file is not an error: a host that has never had an FTP account has
// no file, and that is an empty list rather than a failure.
func (p *Provider) Accounts() ([]Account, error) {
	data, err := os.ReadFile(p.paths.Passwd())
	if err != nil {
		if os.IsNotExist(err) {
			return []Account{}, nil
		}
		return nil, fmt.Errorf("read the FTP password file: %w", err)
	}

	accounts := make([]Account, 0, 8)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			// A line this parser does not understand is skipped rather than
			// guessed at. Reporting an account with the wrong home directory
			// would be worse than not reporting it.
			p.log.Warn("skipping an unreadable line in the FTP password file")
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil {
			p.log.Warn("skipping an FTP account with a malformed id", "account", fields[0])
			continue
		}
		accounts = append(accounts, Account{
			Name:   fields[0],
			UID:    uid,
			GID:    gid,
			Home:   fields[5],
			Locked: strings.HasPrefix(fields[1], "!"),
		})
	}
	return accounts, nil
}

// Account reads one entry.
func (p *Provider) Account(name string) (Account, error) {
	accounts, err := p.Accounts()
	if err != nil {
		return Account{}, err
	}
	for _, account := range accounts {
		if account.Name == name {
			return account, nil
		}
	}
	return Account{}, fmt.Errorf("%w: %q", ErrUnknownUser, name)
}

// CreateAccount writes a new virtual user.
//
// Creating an account that already exists is refused rather than silently
// replacing somebody's password with a new one: the two calls mean different
// things, and a caller that meant the second one has SetPassword.
func (p *Provider) CreateAccount(ctx context.Context, u User, password string) error {
	if err := p.checkUser(u); err != nil {
		return err
	}
	if err := validate.FTPPassword(password); err != nil {
		return err
	}
	if _, err := p.Account(u.Name); err == nil {
		return fmt.Errorf("%w: %q", ErrUserExists, u.Name)
	}
	if err := p.ensureDirs(); err != nil {
		return err
	}
	if err := p.ensureHome(u); err != nil {
		return err
	}

	args := []string{
		"--passwd",
		hashAlgorithm,
		"--file=" + p.paths.Passwd(),
		"--name=" + u.Name,
		"--uid=" + strconv.Itoa(u.UID),
		"--gid=" + strconv.Itoa(u.GID),
		"--home=" + u.Home,
		"--shell=" + nologinShell,
		"--stdin",
	}
	if err := p.runFtpasswd(ctx, password, args...); err != nil {
		return err
	}
	return p.secretPermissions()
}

// SetPassword changes an account's password and nothing else.
func (p *Provider) SetPassword(ctx context.Context, name, password string) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if err := validate.FTPPassword(password); err != nil {
		return err
	}
	if _, err := p.Account(name); err != nil {
		return err
	}

	// --change-password takes --name and --passwd and nothing else, by design:
	// it is the one verb that cannot move an account somewhere else by
	// accident.
	err := p.runFtpasswd(ctx, password,
		"--passwd", hashAlgorithm, "--file="+p.paths.Passwd(),
		"--name="+name, "--change-password", "--stdin")
	if err != nil {
		return err
	}
	return p.secretPermissions()
}

// SetHome moves an account's confinement.
func (p *Provider) SetHome(ctx context.Context, name, home string) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if !strings.HasPrefix(home, "/") {
		// ftpasswd exits 8 for this, but the message it prints is not one to
		// show an operator.
		return fmt.Errorf("%w: it must be an absolute path", validate.ErrInvalidFTPHome)
	}
	if _, err := p.Account(name); err != nil {
		return err
	}
	return p.runFtpasswd(ctx, "",
		"--passwd", "--file="+p.paths.Passwd(),
		"--name="+name, "--change-home="+home)
}

// DeleteAccount removes an entry.
//
// Deleting an account that is not there succeeds: the caller wanted it gone,
// and it is gone. That is the idempotency rule from CLAUDE.md §17, and it
// matters here because deleting a website removes every account it owns and
// must not fail halfway because one had already been removed by hand.
func (p *Provider) DeleteAccount(ctx context.Context, name string) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if _, err := p.Account(name); err != nil {
		return nil
	}
	return p.runFtpasswd(ctx, "",
		"--passwd", "--file="+p.paths.Passwd(),
		"--name="+name, "--delete-user")
}

// SetLocked disables or re-enables an account without deleting it.
//
// Locking prefixes the hash with "!", which matches no password. It exists so
// an operator who suspects a credential is loose can stop it now and decide
// later, rather than choosing between leaving it alone and destroying it.
func (p *Provider) SetLocked(ctx context.Context, name string, locked bool) error {
	if err := validate.FTPUsername(name); err != nil {
		return err
	}
	if _, err := p.Account(name); err != nil {
		return err
	}
	verb := "--unlock"
	if locked {
		verb = "--lock"
	}
	return p.runFtpasswd(ctx, "",
		"--passwd", "--file="+p.paths.Passwd(), "--name="+name, verb)
}

// checkUser validates everything about an account before any of it is written.
func (p *Provider) checkUser(u User) error {
	if err := validate.FTPUsername(u.Name); err != nil {
		return err
	}
	if u.UID <= 0 || u.GID <= 0 {
		// Not a style rule. uid 0 is root, and an FTP account mapped to root is
		// one whose every upload lands as root inside a chroot that root can
		// leave.
		return fmt.Errorf(
			"%w: an FTP account must map to an unprivileged account, never to root",
			validate.ErrInvalidFTPUser)
	}
	if !strings.HasPrefix(u.Home, "/") {
		return fmt.Errorf("%w: it must be an absolute path", validate.ErrInvalidFTPHome)
	}
	return nil
}

// runFtpasswd runs one ftpasswd verb.
//
// The password is written to the child's standard input, never passed as an
// argument: an argument is visible in /proc to every account on the host for as
// long as the process runs.
func (p *Provider) runFtpasswd(ctx context.Context, password string, args ...string) error {
	stdin := ""
	if password != "" {
		// ftpasswd asks twice, even with --stdin.
		stdin = password + "\n" + password + "\n"
	}
	result, err := p.runner.RunWith(ctx, CommandFtpasswd, command.Options{Stdin: stdin}, args...)
	if err != nil {
		return wrap("write the FTP password file", err)
	}
	if !result.Succeeded() {
		// The message may quote what ftpasswd read, so only its first line is
		// surfaced and the password is never in it.
		return fmt.Errorf("the FTP password file could not be written: %s",
			firstLine(result.Stderr, result.Stdout))
	}
	return nil
}

// ensureHome makes the directory an account is confined to.
//
// It has to exist before the account can be used at all: proftpd chroots to it
// at login, and a chroot to a directory that is not there is a login that fails
// with a message about authentication. An operator who asked for an "uploads"
// folder would be told their password was wrong.
//
// Owned by the website's own account, because a directory root owns is one the
// session cannot write into — the account would log in successfully and be
// unable to upload anything.
func (p *Provider) ensureHome(u User) error {
	info, err := os.Stat(u.Home)
	if err == nil {
		if !info.IsDir() {
			return fmt.Errorf("the FTP home %q is not a directory", u.Home)
		}
		// It already exists — a website's document root, normally. Its
		// ownership is the website's business, not this package's.
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("check the FTP home %q: %w", u.Home, err)
	}

	if err := os.MkdirAll(u.Home, 0o750); err != nil {
		return fmt.Errorf("create the FTP home %q: %w", u.Home, err)
	}
	if err := os.Chown(u.Home, u.UID, u.GID); err != nil {
		return fmt.Errorf("give %q to the website's account: %w", u.Home, err)
	}
	p.log.Info("created an FTP home directory", "path", u.Home, "account", u.Name)
	return nil
}

// secretPermissions keeps the password file readable only by root.
//
// ftpasswd creates it 0644. proftpd reads it as root before dropping
// privileges, so nobody else needs to be able to — and a world-readable file of
// password hashes on a shared host is one every customer can copy and attack
// offline at their leisure.
func (p *Provider) secretPermissions() error {
	if err := os.Chmod(p.paths.Passwd(), 0o600); err != nil {
		return fmt.Errorf("secure the FTP password file: %w", err)
	}
	return nil
}
