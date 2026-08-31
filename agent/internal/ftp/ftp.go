// Package ftp manages the host's FTP server.
//
// # Virtual users, not system accounts
//
// Every FTP account the panel creates lives in a password file of its own
// (proftpd's AuthUserFile), and *maps* to the system account that owns the
// website. It is not a system account.
//
// That is the whole security posture of this phase. A system account can be
// used to log in — over SSH, over the console, by anything on the host that
// authenticates against /etc/passwd — and "give the designer FTP access to one
// site" should not hand out a login to the machine. A virtual user is known
// only to proftpd: it authenticates against a file nothing else reads, it has
// /sbin/nologin as its shell, and the only thing it can do with the credential
// is open an FTP session that is chrooted to one directory.
//
// The mapping is what makes uploaded files work. The account carries the
// website's uid and gid, so a file uploaded over FTP is owned by exactly the
// account PHP-FPM runs as — not by root, and not by a second identity whose
// files the site cannot then read.
//
// # Why ProFTPD
//
// Alpine's pure-ftpd is built without TLS (it reports "[privsep]" and nothing
// else), which would leave every password on this server crossing the network
// in clear text. ProFTPD has mod_tls, mod_quotatab and the ftpwho/ftpasswd/
// ftpquota tools, which is the whole of what this phase needs, and it reads
// /etc/proftpd/conf.d/ last so a panel drop-in wins over the distribution's
// defaults.
//
// mod_tls and mod_quotatab are separate packages, loaded at runtime rather than
// compiled in, and the panel checks for them properly — see Provider.Modules.
// An <IfModule> block whose module is missing is skipped in silence, so a panel
// that assumed them would report FTPS was on while serving plain FTP.
//
// # The drop-in, and what the panel does not write
//
// The panel owns exactly one file, conf.d/10-jothost.conf, and rewrites it
// whole from the panel's own state. It does not edit proftpd.conf: the
// distribution owns that, and a panel that rewrites it inherits every future
// upgrade's merge conflict.
//
// The numeric prefix is load-bearing, and in the opposite direction from
// fail2ban's. proftpd includes conf.d in sorted order and, for the directives
// this package writes, the *first* value wins — measured, not assumed: with a
// 20-other.conf setting a second PassivePorts range, the daemon handed clients
// a port from the panel's range. So 10- means the panel's settings are the ones
// the server runs, which is the property a control panel needs: what the page
// shows is what the daemon does.
//
// The cost of that choice is that an operator cannot override the panel from a
// drop-in of their own — a 05-*.conf would be needed, and that is documented
// rather than hidden. What the panel does instead is *notice*: after every
// change it scans the other included files for the directives it owns and
// reports a conflict, because the failure this prevents is silent.
package ftp

import (
	"errors"
	"path/filepath"
)

// Allowlist keys for the tools this package drives.
//
// Every one of them is a fixed path resolved at startup. None of them is ever
// handed a path, a user name or a filter that came out of a request without
// being parsed first.
const (
	// CommandProftpd is the daemon binary, used only to check a candidate
	// configuration (-t) and to read its version (-v). Starting and stopping
	// the daemon goes through the service manager, so one thing on this host
	// owns its lifecycle.
	CommandProftpd = "proftpd"
	// CommandFtpasswd writes the virtual-user password file. It is used rather
	// than writing the file directly because it owns the hash format, and a
	// panel that reimplemented that would be one proftpd release away from a
	// file nobody can log in against.
	CommandFtpasswd = "ftpasswd"
	// CommandFtpwho reads the scoreboard: who is connected right now.
	CommandFtpwho = "ftpwho"
	// CommandFtpquota maintains the quota tables.
	CommandFtpquota = "ftpquota"
)

// Where things live.
const (
	// DefaultConfigDir is proftpd's configuration root.
	DefaultConfigDir = "/etc/proftpd"
	// dropInDir is the directory proftpd.conf includes last.
	dropInDir = "conf.d"
	// modulesDir holds the distribution's LoadModule declarations, one file per
	// optional module package.
	modulesDir = "modules.d"
	// dropInName is the file the panel owns, entirely.
	dropInName = "10-jothost.conf"
	// stateDir holds the things the panel maintains that are not
	// configuration: the password file, the quota tables and the TLS material
	// the panel copied in.
	stateDir = "jothost"
	// passwdName is the AuthUserFile. It holds password hashes, so it is
	// written 0600 and owned by root — proftpd reads it before dropping
	// privileges, and it refuses to use one that is group- or world-readable.
	passwdName = "ftpd.passwd"
	// Quota tables. Two files: what each user is allowed, and what each user
	// has used.
	quotaLimitName = "ftpquota.limit"
	quotaTallyName = "ftpquota.tally"
	// scoreboardName is the file proftpd records live sessions in. Without it
	// ftpwho reports "no users connected" while sessions are in progress, so
	// the session monitor would show an empty table on a busy server.
	scoreboardName = "proftpd.scoreboard"
)

// Where the server records what it does.
//
// Both are named by the panel rather than left to the distribution: proftpd
// sends SystemLog to syslog by default and puts TransferLog somewhere that
// differs between distributions, and a log viewer that has to guess is one that
// shows an empty page on half of them.
const (
	// DefaultLogDir is where a normal host keeps them.
	DefaultLogDir = "/var/log/proftpd"
	// systemLogName records logins, refusals and errors.
	systemLogName = "proftpd.log"
	// transferLogName records every file that moved, in the xferlog format
	// every FTP server has written since wu-ftpd.
	transferLogName = "xferlog"
)

// Errors returned by this package.
var (
	// ErrUnavailable means proftpd is not installed on this host.
	ErrUnavailable = errors.New("an FTP server is not installed on this host")
	// ErrNotRunning means the daemon is down, so nothing can be asked of it.
	ErrNotRunning = errors.New("the FTP server is not running")
	// ErrInvalidConfig means proftpd refused the configuration.
	ErrInvalidConfig = errors.New("the FTP server refused the configuration")
	// ErrUnknownUser means no such FTP account exists.
	ErrUnknownUser = errors.New("that FTP account does not exist")
	// ErrUserExists means the name is taken.
	ErrUserExists = errors.New("an FTP account with that name already exists")
	// ErrNoSession means the session is not open any more. Between listing the
	// sessions and disconnecting one, a client can hang up on its own.
	ErrNoSession = errors.New("that FTP session is no longer open")
	// ErrNoCertificate means FTPS was asked for without a certificate to
	// present.
	ErrNoCertificate = errors.New("FTPS needs a certificate to present")
)

// Paths resolves the files this package owns beneath a configuration root.
type Paths struct {
	// ConfigDir is proftpd's root, /etc/proftpd on every distribution that
	// ships it. It is a field so tests can point the whole package at a
	// temporary directory rather than at the host's real one.
	ConfigDir string
	// RunDir is where the scoreboard lives. It is on a tmpfs on a real host,
	// which is correct: a scoreboard that survived a reboot would describe
	// sessions that ended when the machine went down.
	RunDir string
	// LogDir is where the server records what it does.
	LogDir string
}

// DefaultPaths returns the layout of a normal host.
func DefaultPaths() Paths {
	return Paths{ConfigDir: DefaultConfigDir, RunDir: "/run/proftpd", LogDir: DefaultLogDir}
}

// LogDirOrDefault is where the logs go.
func (p Paths) LogDirOrDefault() string {
	if p.LogDir == "" {
		return DefaultLogDir
	}
	return p.LogDir
}

// SystemLog records logins, refusals and errors.
func (p Paths) SystemLog() string { return filepath.Join(p.LogDirOrDefault(), systemLogName) }

// TransferLog records every file that moved.
func (p Paths) TransferLog() string { return filepath.Join(p.LogDirOrDefault(), transferLogName) }

func (p Paths) root() string {
	if p.ConfigDir == "" {
		return DefaultConfigDir
	}
	return p.ConfigDir
}

// DropIn is the one configuration file the panel writes.
func (p Paths) DropIn() string { return filepath.Join(p.root(), dropInDir, dropInName) }

// DropInDir is the directory that file lives in.
func (p Paths) DropInDir() string { return filepath.Join(p.root(), dropInDir) }

// State is where the panel keeps the password file and the quota tables.
func (p Paths) State() string { return filepath.Join(p.root(), stateDir) }

// Passwd is the AuthUserFile.
func (p Paths) Passwd() string { return filepath.Join(p.State(), passwdName) }

// QuotaLimit is the table of what each account is allowed.
func (p Paths) QuotaLimit() string { return filepath.Join(p.State(), quotaLimitName) }

// QuotaTally is the table of what each account has used.
func (p Paths) QuotaTally() string { return filepath.Join(p.State(), quotaTallyName) }

// Scoreboard is where proftpd records live sessions.
func (p Paths) Scoreboard() string {
	dir := p.RunDir
	if dir == "" {
		dir = "/run/proftpd"
	}
	return filepath.Join(dir, scoreboardName)
}

// ScoreboardDir is the directory that file lives in. proftpd will not create
// it, and without it ftpwho reports an empty server.
func (p Paths) ScoreboardDir() string {
	if p.RunDir == "" {
		return "/run/proftpd"
	}
	return p.RunDir
}
