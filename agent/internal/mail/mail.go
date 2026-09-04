// Package mail manages the host's mail server: Postfix for transport, Dovecot
// for mailboxes and authentication, Rspamd for filtering and signing.
//
// # The failure this phase is built around
//
// Every other phase in this panel can tell you whether it worked. A website
// either serves a page or it does not; a backup is read back and verified; a
// certificate handshakes or it does not. Mail is the one thing here where the
// panel is *not* the authority on whether it works, and where being wrong
// produces no error at all.
//
// A mail server with a broken SPF record sends mail perfectly. A domain whose
// DKIM key was rotated on the host but never republished in DNS signs every
// message it sends, correctly, with a key nobody can fetch. In both cases the
// host's logs say the message was accepted by the receiving server, because it
// was — and then it was filed in a spam folder the recipient never opens. The
// customer's report, weeks later, is "some people say they never got my email".
//
// So this package makes one distinction everywhere it can: **what this host is
// configured to do, and what the world can actually verify.** The Agent reports
// both. The API compares them against the DNS that is really published and says
// where they differ. An unpublished DKIM key is reported as "not signing as far
// as anybody else is concerned", not as "configured".
//
// # Virtual mailboxes, not system accounts
//
// Every mailbox lives in Dovecot's own passwd-file and maps to a single
// unprivileged account, vmail, which owns every Maildir on the host. A mailbox
// is not a system account.
//
// This is the same posture as the FTP accounts in Phase 7.1 and it is here for
// a stronger reason. A mail password is the one credential a customer types
// into a phone, a laptop, and a webmail page, so it is the one most likely to
// be reused and the one most likely to leak. If it were a system account, that
// leak would be a shell on the host. It is not: it is known only to Dovecot,
// it has no shell, and the only thing it opens is a mailbox.
//
// # What the panel writes, and what it does not
//
// Postfix's configuration is not rewritten by this package. Postfix ships
// postconf(1) precisely so that configuration can be changed programmatically —
// `postconf -e` for main.cf, `postconf -M` for master.cf services, `postconf -P`
// for per-service overrides — and every change this package makes goes through
// it. The distribution keeps ownership of its own files, an upgrade merges
// cleanly, and the panel's whole footprint is expressible as a list of keys,
// which is what makes drift detectable: `postconf -n` says what the server
// actually believes, and the panel compares it against what it set.
//
// Dovecot is the other way round, because Dovecot has no postconf. It includes
// conf.d/*.conf, so the panel owns exactly one file there and rewrites it whole.
// The name is 99-jothost.conf, and the number is the opposite of the one the FTP
// drop-in uses on purpose: proftpd takes the *first* value for a repeated
// directive and Dovecot takes the *last*, so on both servers the panel's file
// is the one that wins. Both numbers were measured rather than assumed — a
// panel that guessed would have a page reporting settings the daemon is not
// running.
//
// # The failure mode that matters more than any feature
//
// A mail server that relays for strangers is on a blocklist within hours and
// off it in weeks, and every customer on the host stops being able to send
// mail. Every configuration this package writes is built around not being one:
// mynetworks is the loopback and nothing else, relaying requires
// authentication, and the restriction that refuses everything else comes
// *before* any rule that could permit it. The integration suite proves it from
// outside, by trying.
package mail

import (
	"errors"
	"path/filepath"
)

// Allowlist keys for the programs this package drives.
//
// Every one is a fixed absolute path resolved at Agent startup. None of them is
// ever handed a value from a request that has not been parsed first, and none
// of them is ever given a password in argv: the process table is world-readable
// on a normal host.
const (
	// CommandPostconf reads and writes Postfix's configuration. It is the only
	// way this package changes main.cf or master.cf.
	CommandPostconf = "postconf"
	// CommandPostmap compiles a lookup table into the indexed form Postfix
	// reads. Not needed for every map type — see mapType.
	CommandPostmap = "postmap"
	// CommandPostalias compiles the local alias database.
	//
	// It is here for one specific gap: several distributions ship
	// /etc/postfix/aliases and no compiled copy, so every SMTP connection logs
	// "open database aliases.lmdb: No such file or directory". The panel
	// compiles it rather than switching local aliases off, because an operator
	// may be relying on the root alias in it and a panel that quietly disabled
	// that would be redirecting somebody's mail without saying so.
	CommandPostalias = "postalias"
	// CommandPostfix controls the server: check, reload, and report its
	// version. Starting and stopping go through the service manager, so one
	// thing on this host owns daemon lifecycles.
	CommandPostfix = "postfix"
	// CommandPostqueue reads the mail queue.
	CommandPostqueue = "postqueue"
	// CommandPostsuper acts on queued messages.
	CommandPostsuper = "postsuper"
	// CommandDoveadm is Dovecot's administration tool: quota reporting,
	// mailbox creation, and configuration checks.
	CommandDoveadm = "doveadm"
	// CommandDovecot is the daemon, used to validate a configuration and read
	// its version.
	CommandDovecot = "dovecot"
	// CommandRspamadm is Rspamd's administration tool.
	CommandRspamadm = "rspamadm"
	// CommandRspamc is Rspamd's client, used to ask a running instance what it
	// thinks of a message and whether its antivirus module is answering.
	CommandRspamc = "rspamc"
	// CommandSievec compiles a Sieve script. A script that fails to compile is
	// one Dovecot ignores at delivery time, in silence, so the panel compiles
	// every one it writes rather than finding out from a customer that their
	// vacation reply never fired.
	CommandSievec = "sievec"
)

// Where things live.
const (
	// DefaultPostfixDir is Postfix's configuration root.
	DefaultPostfixDir = "/etc/postfix"
	// DefaultDovecotDir is Dovecot's.
	DefaultDovecotDir = "/etc/dovecot"
	// DefaultRspamdDir is Rspamd's.
	DefaultRspamdDir = "/etc/rspamd"
	// DefaultMailRoot is where Maildirs live, one directory per domain.
	DefaultMailRoot = "/var/mail/vhosts"
	// DefaultStateDir holds what the panel maintains that is neither a
	// distribution file nor a customer's mail: the lookup tables, the passwd
	// file, the Sieve scripts and the DKIM keys.
	DefaultStateDir = "/var/lib/jothost/mail"

	// dovecotDropIn is the one file this package owns under Dovecot's conf.d.
	// See the package comment for why the number is 99 and the FTP drop-in's
	// is 10.
	dovecotDropIn = "99-jothost.conf"
	// rspamdLocalDir is where Rspamd reads operator overrides from. Files here
	// are merged into the shipped configuration rather than replacing it,
	// which is exactly the boundary this package wants.
	rspamdLocalDir = "local.d"

	// The lookup tables the panel maintains, all under the state directory.
	//
	// They are here rather than in /etc/postfix because they are generated
	// from the panel's own record on every reconcile — they are state, not
	// configuration, and putting them beside the distribution's files would
	// invite somebody to edit one by hand and have it silently overwritten.
	domainsMap  = "virtual_domains"
	mailboxMap  = "virtual_mailboxes"
	aliasMap    = "virtual_aliases"
	passwdFile  = "dovecot-users"
	sieveDir    = "sieve"
	dkimDir     = "dkim"
	dkimMapName = "dkim_selectors"
)

// The account every Maildir is owned by.
//
// One account for all of them rather than one per domain or per mailbox. That
// is the standard virtual-mailbox arrangement and the reason is that a mailbox
// is not a login to this machine: there is nothing for a per-mailbox uid to
// protect, since only Dovecot ever opens these files, and thousands of system
// accounts is a real cost — every one of them is a line in /etc/passwd that
// every name-service lookup on the host walks past.
const (
	// VmailUser owns every Maildir.
	VmailUser = "vmail"
	// VmailHome is the account's home, which is also the mail root.
	VmailHome = DefaultMailRoot
)

// Errors returned by this package.
var (
	// ErrUnavailable means this host has no mail server the panel can manage.
	ErrUnavailable = errors.New("this host has no mail server the panel can manage")
	// ErrNotRunning means the server is installed and stopped.
	ErrNotRunning = errors.New("the mail server is not running")
	// ErrInvalidConfig means the daemon rejected a configuration.
	ErrInvalidConfig = errors.New("the mail server rejected this configuration")
	// ErrNoCertificate means TLS was asked for with nothing to present.
	ErrNoCertificate = errors.New("this host has no certificate for the mail server to present")
	// ErrNoHostname means mail was switched on without a name for the server.
	ErrNoHostname = errors.New("the mail server needs a fully qualified hostname")
	// ErrUnknownDomain means an operation named a domain this host does not
	// carry.
	ErrUnknownDomain = errors.New("this host does not accept mail for that domain")
	// ErrWebmailUnavailable means webmail cannot be installed here.
	ErrWebmailUnavailable = errors.New("webmail cannot be installed on this host")
	// ErrWebmailChecksum means a downloaded webmail archive was not the one
	// the panel expected. It is deliberately its own error: everything else
	// here is a misconfiguration, and this one is a compromised download.
	ErrWebmailChecksum = errors.New("the webmail download did not match its expected checksum")
)

// Paths tells the provider where this host keeps its mail configuration.
type Paths struct {
	// PostfixDir is Postfix's configuration root.
	PostfixDir string
	// DovecotDir is Dovecot's.
	DovecotDir string
	// RspamdDir is Rspamd's.
	RspamdDir string
	// MailRoot is where Maildirs live.
	MailRoot string
	// StateDir is where the panel keeps what it generates.
	StateDir string
}

// DefaultPaths returns the standard layout.
func DefaultPaths() Paths {
	return Paths{
		PostfixDir: DefaultPostfixDir,
		DovecotDir: DefaultDovecotDir,
		RspamdDir:  DefaultRspamdDir,
		MailRoot:   DefaultMailRoot,
		StateDir:   DefaultStateDir,
	}
}

// withDefaults fills in anything the caller left blank.
func (p Paths) withDefaults() Paths {
	defaults := DefaultPaths()
	if p.PostfixDir == "" {
		p.PostfixDir = defaults.PostfixDir
	}
	if p.DovecotDir == "" {
		p.DovecotDir = defaults.DovecotDir
	}
	if p.RspamdDir == "" {
		p.RspamdDir = defaults.RspamdDir
	}
	if p.MailRoot == "" {
		p.MailRoot = defaults.MailRoot
	}
	if p.StateDir == "" {
		p.StateDir = defaults.StateDir
	}
	return p
}

// DovecotConf is the one Dovecot file the panel owns.
func (p Paths) DovecotConf() string {
	return filepath.Join(p.DovecotDir, "conf.d", dovecotDropIn)
}

// RspamdLocal is where an Rspamd module override goes.
func (p Paths) RspamdLocal(name string) string {
	return filepath.Join(p.RspamdDir, rspamdLocalDir, name)
}

// The generated lookup tables and state files.
func (p Paths) DomainsMap() string { return filepath.Join(p.StateDir, domainsMap) }
func (p Paths) MailboxMap() string { return filepath.Join(p.StateDir, mailboxMap) }
func (p Paths) AliasMap() string   { return filepath.Join(p.StateDir, aliasMap) }
func (p Paths) PasswdFile() string { return filepath.Join(p.StateDir, passwdFile) }
func (p Paths) SieveDir() string   { return filepath.Join(p.StateDir, sieveDir) }
func (p Paths) DKIMDir() string    { return filepath.Join(p.StateDir, dkimDir) }
func (p Paths) DKIMMap() string    { return filepath.Join(p.StateDir, dkimMapName) }

// DomainRoot is where one domain's Maildirs live.
//
// The domain has been through validate.MailDomain before it reaches here, so it
// holds no separator and cannot climb out of the mail root. That is the check
// that matters; this function does not repeat it, it depends on it, and every
// caller is inside this package.
func (p Paths) DomainRoot(domain string) string {
	return filepath.Join(p.MailRoot, domain)
}

// MailboxRoot is where one mailbox's Maildir lives.
func (p Paths) MailboxRoot(domain, local string) string {
	return filepath.Join(p.MailRoot, domain, local)
}

// SieveScript is where one mailbox's vacation script lives.
//
// The file is named for the address with the "@" kept, which is legal in a
// filename and makes the directory readable by a human debugging a delivery.
func (p Paths) SieveScript(domain, local string) string {
	return filepath.Join(p.SieveDir(), local+"@"+domain+".sieve")
}

// DKIMKeyPath is where one domain's private signing key lives.
func (p Paths) DKIMKeyPath(domain, selector string) string {
	return filepath.Join(p.DKIMDir(), domain+"."+selector+".key")
}
