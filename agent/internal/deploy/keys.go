package deploy

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jothost/panel/shared/validate"
)

// GenerateKey creates a deploy key for one website and returns its public half.
//
// Ed25519 rather than RSA. It is smaller, it is faster, every forge has
// accepted it for years, and there is no key-size decision to get wrong — which
// matters for a key a panel generates on somebody's behalf without asking them
// anything.
//
// The private half is written to this host and **is not returned**. It is the
// same decision Phase 26 made about DKIM keys and it is sharper here: this key
// grants read access to a customer's source code, and the control-plane
// database is backed up, replicated, and read by every part of the API. What
// the panel records is the public half and the fingerprint, which is what
// somebody needs to add it to a forge.
func (p *Provider) GenerateKey(ctx context.Context, accountName string) (Key, error) {
	owner, err := p.lookupAccount(accountName)
	if err != nil {
		return Key{}, err
	}
	if !p.runner.Available(CommandSSHKeygen) {
		return Key{}, fmt.Errorf("%w: this host has no ssh-keygen", ErrUnavailable)
	}
	if err := p.ensureDirs(); err != nil {
		return Key{}, err
	}

	path := p.paths.KeyPath(owner.Name)
	// ssh-keygen refuses to overwrite, and asks — on a terminal nobody is
	// attached to. Removing first is what makes regenerating a key work at all.
	for _, stale := range []string{path, path + ".pub"} {
		if err := os.Remove(stale); err != nil && !os.IsNotExist(err) {
			return Key{}, fmt.Errorf("remove the previous deploy key: %w", err)
		}
	}

	// -N "" is an empty passphrase, which is the only useful kind for a key a
	// daemon authenticates with unattended: a passphrase this panel stored
	// somewhere in order to type it in would be a passphrase protecting
	// nothing, with an extra place to leak from.
	//
	// The comment names the host and the site, because a forge's deploy key
	// list shows comments and nothing else — an operator with thirty sites
	// needs to know which key this is.
	comment := "jothost-" + owner.Name
	result, err := p.runner.Run(ctx, CommandSSHKeygen,
		"-t", "ed25519", "-N", "", "-C", comment, "-f", path)
	if err != nil {
		return Key{}, wrap("generate a deploy key", err)
	}
	if !result.Succeeded() {
		return Key{}, fmt.Errorf("generate a deploy key: %s",
			firstLine(result.Stderr, result.Stdout))
	}

	// 0600 and owned by the site account. git runs as that account, so a
	// root-owned key is one it cannot read — and SSH refuses a key whose
	// permissions are wider than 0600, which it reports as "bad permissions"
	// rather than as a failure to authenticate.
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return Key{}, fmt.Errorf("give the deploy key to %s: %w", owner.Name, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return Key{}, fmt.Errorf("secure the deploy key: %w", err)
	}
	if err := os.Chown(path+".pub", owner.UID, owner.GID); err != nil {
		return Key{}, fmt.Errorf("give the deploy key to %s: %w", owner.Name, err)
	}

	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		return Key{}, fmt.Errorf("read the public key: %w", err)
	}

	if err := p.writeSSHConfig(owner); err != nil {
		return Key{}, err
	}

	return Key{
		PublicKey:   strings.TrimSpace(string(public)),
		Fingerprint: p.fingerprint(ctx, path),
		Path:        path,
		Type:        "ed25519",
	}, nil
}

// RemoveKey deletes a website's deploy key from this host.
func (p *Provider) RemoveKey(accountName string) error {
	if err := validate.SystemUser(accountName); err != nil {
		return err
	}
	for _, path := range []string{
		p.paths.KeyPath(accountName),
		p.paths.KeyPath(accountName) + ".pub",
		p.paths.SSHConfigPath(accountName),
		p.knownHostsPath(accountName),
	} {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}

// knownHostsPath is where this website's pinned host keys go.
func (p *Provider) knownHostsPath(accountName string) string {
	return p.paths.KeyPath(accountName) + ".known_hosts"
}

// sshCommand is what git authenticates with.
//
// Assembled from paths this package derived from a validated account name, so
// nothing here comes from a request. It is passed through GIT_SSH_COMMAND,
// which git splits with shell quoting rules — the paths contain no spaces by
// construction, and the account name has been through validate.SystemUser,
// which permits only [a-z0-9_-].
//
// StrictHostKeyChecking is **accept-new**, and that is a weaker setting than
// Phase 14 chose for its SFTP backups, deliberately and for a reason.
//
// Phase 14 could use "yes" because the operator types in the destination host
// and can be asked for its key. Here the host is github.com, and nobody
// operating a control panel has GitHub's host key to hand. "yes" would make
// every first deployment fail with an error there is no way to act on from a web
// page; "no" would accept a different key on every connection, which is the
// setting that makes host key checking meaningless. "accept-new" pins the key
// the first time and refuses a *change* afterwards — which is the property that
// actually matters, because a changed key is what an interception looks like.
//
// The known_hosts file is per website, so one customer's pinning cannot decide
// what another customer's deployment trusts.
func (p *Provider) sshCommand(accountName string) string {
	return strings.Join([]string{
		"ssh",
		"-i", p.paths.KeyPath(accountName),
		"-o", "IdentitiesOnly=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + p.knownHostsPath(accountName),
		// Without this, ssh reads the *Agent's* ~/.ssh/config — root's — and a
		// Host * entry there would decide how a customer's deployment
		// authenticates.
		"-F", "/dev/null",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
	}, " ")
}

// writeSSHConfig creates the per-website known_hosts file.
//
// Created empty rather than left absent, with the right ownership: ssh writes
// the pinned key into it on the first connection, as the site account, and a
// file it cannot create is one where accept-new silently degrades to trusting
// everything for the length of the session and pinning nothing.
func (p *Provider) writeSSHConfig(owner account) error {
	path := p.knownHostsPath(owner.Name)
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create the known hosts file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("create the known hosts file: %w", err)
	}
	if err := os.Chown(path, owner.UID, owner.GID); err != nil {
		return fmt.Errorf("give the known hosts file to %s: %w", owner.Name, err)
	}
	return nil
}

// PublicKey reads the public half of a website's deploy key, if it has one.
func (p *Provider) PublicKey(ctx context.Context, accountName string) (Key, bool, error) {
	if err := validate.SystemUser(accountName); err != nil {
		return Key{}, false, err
	}
	path := p.paths.KeyPath(accountName)
	public, err := os.ReadFile(path + ".pub")
	if err != nil {
		if os.IsNotExist(err) {
			return Key{}, false, nil
		}
		return Key{}, false, fmt.Errorf("read the public key: %w", err)
	}
	return Key{
		PublicKey:   strings.TrimSpace(string(public)),
		Fingerprint: p.fingerprint(ctx, path),
		Path:        path,
		Type:        "ed25519",
	}, true, nil
}
