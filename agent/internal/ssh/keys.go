package ssh

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Authorized keys.
//
// Two decisions shape this file.
//
// **Which accounts are offered is the Agent's decision, not the caller's.** A
// request names an account from the list below, and the path it becomes —
// ~/.ssh/authorized_keys — is built here. An API that took a path would be an
// arbitrary file writer, and the file it writes is the one that decides who may
// log in.
//
// **A key is parsed before it is written, not after.** authorized_keys is
// line-oriented and unquoted, like a crontab, so a value containing a newline is
// not a long key: it is a second entry. And a key that does not decode is a key
// that will never work, which is better said now than discovered at three in the
// morning by somebody who is locked out.

// Errors returned by key management.
var (
	// ErrUnknownAccount means the account is not one this host offers.
	ErrUnknownAccount = errors.New("that account cannot be given SSH keys on this host")
	// ErrInvalidKey means the key is not a key.
	ErrInvalidKey = errors.New("invalid SSH public key")
	// ErrKeyNotFound means no key with that fingerprint is authorised.
	ErrKeyNotFound = errors.New("no such authorised key")
	// ErrDuplicateKey means the key is already authorised for the account.
	ErrDuplicateKey = errors.New("that key is already authorised for this account")
)

// MaxKeyLength bounds one line of authorized_keys.
//
// A 4096-bit RSA key is about 750 characters; this is comfortably above that
// and far below anything that would make the file unwieldy.
const MaxKeyLength = 8192

// MaxKeys bounds how many keys one account may have, so the file cannot be
// filled from the panel.
const MaxKeys = 100

// keyTypes are the algorithms the panel will write.
//
// An allowlist rather than "whatever decodes", because the type is the first
// field of the line and the first field of the blob, and a value that is not one
// of these is either a mistake or an attempt to put something else in the file.
// ssh-dss is deliberately absent: OpenSSH has refused DSA by default since 7.0
// and removed it in 9.8, so writing one produces a key that cannot be used.
var keyTypes = map[string]bool{
	"ssh-ed25519":                        true,
	"ssh-rsa":                            true,
	"ecdsa-sha2-nistp256":                true,
	"ecdsa-sha2-nistp384":                true,
	"ecdsa-sha2-nistp521":                true,
	"sk-ssh-ed25519@openssh.com":         true,
	"sk-ecdsa-sha2-nistp256@openssh.com": true,
	"rsa-sha2-256":                       true,
	"rsa-sha2-512":                       true,
}

// Account is a host account that can be given SSH keys.
type Account struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	Home string `json:"home"`
	// Shell is what the account logs in with, shown because it is the reason
	// the account is on this list.
	Shell string `json:"shell"`
	// Keys is how many keys are authorised, filled in by List.
	Keys int `json:"keys"`
}

// AccountLister reports which accounts may be given keys.
type AccountLister interface {
	LoginAccounts() ([]Account, error)
}

// passwdAccounts reads /etc/passwd.
type passwdAccounts struct{ path string }

// nonLoginShells are the shells that mean "this account cannot log in".
//
// A website's account has one of these, which is why sites do not appear on
// this list: giving an SSH key to an account whose shell refuses every session
// produces a key that does nothing, and a panel offering it would be offering a
// feature that cannot work.
var nonLoginShells = map[string]bool{
	"/sbin/nologin":     true,
	"/usr/sbin/nologin": true,
	"/bin/false":        true,
	"/usr/bin/false":    true,
	"":                  true,
}

// LoginAccounts returns the accounts that can actually log in.
func (a passwdAccounts) LoginAccounts() ([]Account, error) {
	file, err := os.Open(a.path) //nolint:gosec // a fixed system path
	if err != nil {
		return nil, fmt.Errorf("read the account list: %w", err)
	}
	defer func() { _ = file.Close() }()

	accounts := make([]Account, 0, 8)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) < 7 {
			continue
		}

		shell := fields[6]
		if nonLoginShells[shell] {
			continue
		}

		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		gid, err := strconv.Atoi(fields[3])
		if err != nil {
			continue
		}
		// System accounts other than root are excluded: postgres and mysql have
		// a shell so that `su` works for maintenance, not so that anyone logs
		// in over the network as them.
		if uid != 0 && uid < 1000 {
			continue
		}

		accounts = append(accounts, Account{
			Name: fields[0], UID: uid, GID: gid, Home: fields[5], Shell: shell,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read the account list: %w", err)
	}

	sort.Slice(accounts, func(i, j int) bool { return accounts[i].UID < accounts[j].UID })
	return accounts, nil
}

// Key is one authorised public key.
type Key struct {
	// Fingerprint is the SHA256 form OpenSSH prints, and is what identifies a
	// key in this API. A line number would change when another key is removed;
	// a fingerprint is the key.
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	// Comment is the trailing label, usually user@host. It is what a person
	// recognises a key by.
	Comment string `json:"comment"`
	// Bits is the key size where it can be told from the blob.
	Bits int `json:"bits,omitempty"`
	// Account is who may log in with it.
	Account string `json:"account"`
}

// Accounts lists the accounts that may hold keys, with their key counts.
func (p *Provider) Accounts() ([]Account, error) {
	accounts, err := p.accounts.LoginAccounts()
	if err != nil {
		return nil, err
	}

	for i := range accounts {
		keys, err := p.readKeys(accounts[i])
		if err != nil {
			// An unreadable file is reported as zero keys rather than failing
			// the whole listing: one broken home directory should not make the
			// page unavailable.
			p.log.Warn("an account's authorized_keys could not be read",
				"account", accounts[i].Name, "error", err.Error())
			continue
		}
		accounts[i].Keys = len(keys)
	}
	return accounts, nil
}

// Keys lists one account's authorised keys.
func (p *Provider) Keys(name string) ([]Key, error) {
	account, err := p.account(name)
	if err != nil {
		return nil, err
	}
	return p.readKeys(account)
}

// AddKey authorises a key for an account.
func (p *Provider) AddKey(name, entry string) (Key, error) {
	account, err := p.account(name)
	if err != nil {
		return Key{}, err
	}

	key, err := ParseKey(entry)
	if err != nil {
		return Key{}, err
	}
	key.Account = account.Name

	existing, err := p.readKeys(account)
	if err != nil {
		return Key{}, err
	}
	if len(existing) >= MaxKeys {
		return Key{}, fmt.Errorf("%w: this account already has %d keys",
			ErrInvalidKey, len(existing))
	}
	for _, held := range existing {
		if held.Fingerprint == key.Fingerprint {
			return Key{}, ErrDuplicateKey
		}
	}

	lines, err := p.readLines(account)
	if err != nil {
		return Key{}, err
	}
	lines = append(lines, normalise(entry))

	if err := p.writeLines(account, lines); err != nil {
		return Key{}, err
	}
	return key, nil
}

// RemoveKey withdraws a key by fingerprint.
func (p *Provider) RemoveKey(name, fingerprint string) (Key, error) {
	account, err := p.account(name)
	if err != nil {
		return Key{}, err
	}

	lines, err := p.readLines(account)
	if err != nil {
		return Key{}, err
	}

	var removed Key
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		key, err := ParseKey(line)
		if err != nil {
			// A line this package cannot parse is a line somebody else wrote,
			// and it is kept exactly as it is. The file is not the panel's to
			// tidy.
			kept = append(kept, line)
			continue
		}
		if key.Fingerprint == fingerprint {
			key.Account = account.Name
			removed = key
			continue
		}
		kept = append(kept, line)
	}

	if removed.Fingerprint == "" {
		return Key{}, ErrKeyNotFound
	}
	if err := p.writeLines(account, kept); err != nil {
		return Key{}, err
	}
	return removed, nil
}

// account resolves a name to one of the accounts this host offers.
func (p *Provider) account(name string) (Account, error) {
	accounts, err := p.accounts.LoginAccounts()
	if err != nil {
		return Account{}, err
	}
	for _, account := range accounts {
		if account.Name == name {
			return account, nil
		}
	}
	return Account{}, fmt.Errorf("%w: %q", ErrUnknownAccount, name)
}

// keyPath is where an account's authorised keys live.
func (p *Provider) keyPath(account Account) string {
	return filepath.Join(account.Home, ".ssh", "authorized_keys")
}

// readLines returns the file as it is, comments and all.
func (p *Provider) readLines(account Account) ([]string, error) {
	content, err := os.ReadFile(p.keyPath(account)) //nolint:gosec // built from the account's own home
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read the authorised keys: %w", err)
	}

	lines := make([]string, 0, 8)
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// readKeys returns the parsed keys, ignoring what it cannot parse.
func (p *Provider) readKeys(account Account) ([]Key, error) {
	lines, err := p.readLines(account)
	if err != nil {
		return nil, err
	}

	keys := make([]Key, 0, len(lines))
	for _, line := range lines {
		key, err := ParseKey(line)
		if err != nil {
			continue
		}
		key.Account = account.Name
		keys = append(keys, key)
	}
	return keys, nil
}

// writeLines installs a new authorized_keys for an account.
//
// Written to a temporary file beside it and renamed, so a reader never sees a
// half-written file — which for this file means a moment during which a valid
// key does not authorise anybody.
func (p *Provider) writeLines(account Account, lines []string) error {
	dir := filepath.Dir(p.keyPath(account))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create the .ssh directory: %w", err)
	}
	// sshd refuses to read a file, or a directory, that anyone but the owner
	// can write — and says so only in its own log. A key that silently does
	// not work is the failure this avoids.
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("set the .ssh directory's mode: %w", err)
	}
	if err := chownIfPossible(dir, account); err != nil {
		return err
	}

	path := p.keyPath(account)
	temp, err := os.CreateTemp(dir, ".jothost-keys-*")
	if err != nil {
		return fmt.Errorf("create a temporary key file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	content := ""
	if len(lines) > 0 {
		content = strings.Join(lines, "\n") + "\n"
	}
	if _, err := temp.WriteString(content); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write the authorised keys: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close the authorised keys: %w", err)
	}

	if err := os.Chmod(tempPath, 0o600); err != nil {
		return fmt.Errorf("set the key file's mode: %w", err)
	}
	if err := chownIfPossible(tempPath, account); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("install the authorised keys: %w", err)
	}
	return nil
}

// chownIfPossible gives a path to the account that must read it.
//
// A failure is ignored when the Agent is not root, which is the case in a unit
// test and never the case on a host: chown is a privileged call, and a test
// that could not run because of it would be a test nobody keeps.
func chownIfPossible(path string, account Account) error {
	if os.Geteuid() != 0 {
		return nil
	}
	if err := os.Chown(path, account.UID, account.GID); err != nil {
		return fmt.Errorf("set the owner of %s: %w", path, err)
	}
	return nil
}

// ParseKey reads one authorized_keys line.
//
// What it refuses is as important as what it accepts:
//
//   - a line with options in front of the key. Those are legitimate OpenSSH
//     syntax and they are how a key becomes a forced command, a port forward or
//     an environment override. The panel writes plain keys; anything else is an
//     administrator's deliberate act at a terminal, and the panel leaves the
//     lines it does not understand exactly where they are.
//   - a key whose declared type disagrees with the type inside the blob. That
//     is the check that catches a mangled paste, because the type appears twice
//     and a truncated key rarely gets both right.
func ParseKey(entry string) (Key, error) {
	line := strings.TrimSpace(entry)
	if line == "" {
		return Key{}, fmt.Errorf("%w: the key is empty", ErrInvalidKey)
	}
	if len(line) > MaxKeyLength {
		return Key{}, fmt.Errorf("%w: the key is longer than %d characters",
			ErrInvalidKey, MaxKeyLength)
	}
	if strings.ContainsAny(line, "\n\r\x00") {
		// authorized_keys is one key per line, so a line break is not a long
		// key: it is a second entry, authorising somebody else.
		return Key{}, fmt.Errorf("%w: a key must be a single line", ErrInvalidKey)
	}
	if strings.HasPrefix(line, "#") {
		return Key{}, fmt.Errorf("%w: that is a comment", ErrInvalidKey)
	}

	fields := strings.Fields(line)
	if len(fields) < 2 {
		return Key{}, fmt.Errorf("%w: a key is a type and a body", ErrInvalidKey)
	}

	keyType := fields[0]
	if !keyTypes[keyType] {
		return Key{}, fmt.Errorf(
			"%w: %q is not a key type the panel writes — options in front of a key "+
				"are not accepted either", ErrInvalidKey, truncate(keyType, 40))
	}

	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return Key{}, fmt.Errorf("%w: the key body is not valid base64", ErrInvalidKey)
	}

	inner, rest, err := readString(blob)
	if err != nil {
		return Key{}, fmt.Errorf("%w: the key body is not a key", ErrInvalidKey)
	}
	if inner != keyType {
		return Key{}, fmt.Errorf("%w: it says %q but contains %q, so it is truncated or mangled",
			ErrInvalidKey, keyType, truncate(inner, 40))
	}

	key := Key{
		Fingerprint: fingerprint(blob),
		Type:        keyType,
		Comment:     strings.Join(fields[2:], " "),
		Bits:        bits(keyType, rest),
	}
	return key, nil
}

// fingerprint renders the SHA256 form OpenSSH prints.
func fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

// readString reads one length-prefixed string from an SSH wire-format blob.
func readString(blob []byte) (string, []byte, error) {
	if len(blob) < 4 {
		return "", nil, errors.New("too short")
	}
	length := binary.BigEndian.Uint32(blob[:4])
	if length > uint32(len(blob)-4) {
		return "", nil, errors.New("length runs past the end")
	}
	return string(blob[4 : 4+length]), blob[4+length:], nil
}

// bits reports the key size where the blob says it.
//
// Ed25519 is always 256 and does not carry a size; RSA's modulus length is its
// size, which is the number an operator is looking for when they ask whether a
// key is strong enough.
func bits(keyType string, rest []byte) int {
	switch keyType {
	case "ssh-ed25519", "sk-ssh-ed25519@openssh.com":
		return 256
	case "ecdsa-sha2-nistp256", "sk-ecdsa-sha2-nistp256@openssh.com":
		return 256
	case "ecdsa-sha2-nistp384":
		return 384
	case "ecdsa-sha2-nistp521":
		return 521
	case "ssh-rsa", "rsa-sha2-256", "rsa-sha2-512":
		// e then n; the modulus is the second.
		_, after, err := readString(rest)
		if err != nil {
			return 0
		}
		modulus, _, err := readString(after)
		if err != nil {
			return 0
		}
		trimmed := strings.TrimLeft(modulus, "\x00")
		return len(trimmed) * 8
	}
	return 0
}

// normalise strips a key line down to what will be written.
func normalise(entry string) string { return strings.TrimSpace(entry) }

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
