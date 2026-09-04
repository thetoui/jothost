package services

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// The catalogue is the set of services this panel is willing to touch.
//
// It exists because "manage services" and "run systemctl on anything the caller
// names" are different features, and only the first one is wanted. A request
// carries a key from this table; the Agent maps that key to a unit name it
// chose itself. Nothing a client sends ever becomes a unit name.
//
// What that prevents is worth being concrete about: stopping systemd-logind,
// masking the audit daemon, disabling the very unit the Agent runs as. Those
// are one careless request away in a panel that forwards a name, and no amount
// of pattern-matching on the name makes them safe.
//
// The catalogue grows with the phases that manage new daemons — FTP in 7.1,
// BIND in 13, Fail2Ban in 18, Postfix and Dovecot in 26 — and each addition is
// a reviewable change to this file rather than a permission granted at runtime.

// Definition describes one service the panel knows about.
type Definition struct {
	// Key is the stable identifier the API uses. It never reaches a command
	// line: it selects a Definition, and the Definition supplies the unit.
	Key string `json:"key"`
	// Label is what an operator reads.
	Label string `json:"label"`
	// Role groups the list into the sections a panel shows.
	Role string `json:"role"`
	// Summary says what the service does, for someone who is not sure whether
	// stopping it matters.
	Summary string `json:"summary"`
	// Units are candidate systemd unit names in preference order.
	// Distributions disagree: Alpine and RHEL call it httpd, Debian apache2.
	Units []string `json:"units"`
	// Processes are the kernel process names that mean it is running. They are
	// how the panel answers "is this up" on a host with no systemd, which is
	// the difference between a status page and a blank one.
	Processes []string `json:"processes"`
	// Binaries are absolute paths whose presence means the service is
	// installed here. Probing the filesystem executes nothing.
	Binaries []string `json:"-"`
	// Protected refuses stop and disable.
	Protected bool `json:"protected"`
	// SelfManaged means this daemon's lifecycle belongs to the panel itself
	// rather than to the init system.
	//
	// PHP-FPM is started by the PHP manager, which writes the pools and signals
	// the master to reload them. Apache is started by the hybrid engine, which
	// has to work on hosts with no init system at all. Offering to stop either
	// from here would be two owners fighting over one process: the init system
	// would report success, the component that actually started it would carry
	// on, and the panel would show a state that matched neither.
	//
	// The state is still shown — it is read from the process table, and is
	// true. Only the controls are withheld, with the reason given.
	SelfManaged bool `json:"self_managed"`
	// SelfManagedBy names what owns it, for a panel that has to explain why a
	// button is not there.
	SelfManagedBy string `json:"self_managed_by,omitempty"`
	// Essential means the websites on this host stop working while it is down,
	// so a dashboard raises an alert about it.
	//
	// Not every service earns this. Cron being stopped is normal on a host with
	// no scheduled work, and Apache is *deliberately* stopped whenever the host
	// serves everything from nginx — alerting on either would fire permanently
	// on a healthy machine, and an alert that is always on is one nobody reads.
	Essential bool `json:"essential"`
}

// Service roles, which are the sections the panel groups the list into.
const (
	RoleWeb      = "web"
	RoleRuntime  = "runtime"
	RoleDatabase = "database"
	RoleCache    = "cache"
	RoleSystem   = "system"
)

// catalogue is the built-in set.
//
// Every entry is either something an earlier phase installs and configures, or
// a core system service an operator expects a panel to show. Nothing else is
// listed: a service the panel does not manage is one it should not offer to
// stop.
var catalogue = []Definition{
	{
		Key:       "nginx",
		Label:     "nginx",
		Role:      RoleWeb,
		Summary:   "Serves every website on this host, and holds the public ports.",
		Units:     []string{"nginx.service"},
		Processes: []string{"nginx"},
		Binaries:  []string{"/usr/sbin/nginx", "/usr/bin/nginx"},
		// Not protected, but stopping it takes every site offline, which the
		// panel says before it does it — and reports afterwards.
		Essential: true,
	},
	{
		Key:       "apache",
		Label:     "Apache",
		Role:      RoleWeb,
		Summary:   "The backend in the hybrid arrangement, behind nginx.",
		Units:     []string{"httpd.service", "apache2.service"},
		Processes: []string{"httpd", "apache2"},
		Binaries:  []string{"/usr/sbin/httpd", "/usr/sbin/apache2"},
		// The hybrid engine starts and stops Apache as sites move on and off
		// it, and does so on hosts with no init system at all.
		SelfManaged:   true,
		SelfManagedBy: "the web server arrangement",
	},
	{
		Key:       "mariadb",
		Label:     "MariaDB",
		Role:      RoleDatabase,
		Summary:   "The MySQL-compatible database server.",
		Units:     []string{"mariadb.service", "mysqld.service", "mysql.service"},
		Processes: []string{"mariadbd", "mysqld"},
		Binaries: []string{
			"/usr/bin/mariadbd", "/usr/sbin/mariadbd",
			"/usr/sbin/mysqld", "/usr/libexec/mysqld",
		},
		// A database the panel installed is a database sites are using.
		Essential: true,
	},
	{
		Key:     "postgresql",
		Label:   "PostgreSQL",
		Role:    RoleDatabase,
		Summary: "The PostgreSQL database server.",
		// Debian names the unit per cluster; the plain one is an alias that
		// starts them all, which is what a panel means by "PostgreSQL".
		Units:     []string{"postgresql.service", "postgresql@16-main.service"},
		Processes: []string{"postgres"},
		Binaries: []string{
			"/usr/bin/postgres", "/usr/lib/postgresql/16/bin/postgres",
			"/usr/pgsql-16/bin/postgres",
		},
		Essential: true,
	},
	{
		Key:       "redis",
		Label:     "Redis",
		Role:      RoleCache,
		Summary:   "In-memory store used by the panel for sessions and rate limits.",
		Units:     []string{"redis.service", "redis-server.service"},
		Processes: []string{"redis-server"},
		Binaries:  []string{"/usr/bin/redis-server", "/usr/sbin/redis-server"},
	},
	{
		Key:     "sshd",
		Label:   "SSH",
		Role:    RoleSystem,
		Summary: "Remote shell access to this host.",
		Units:   []string{"sshd.service", "ssh.service"},
		// Both, because the daemon is sshd and Debian's unit is ssh.
		Processes: []string{"sshd"},
		Binaries:  []string{"/usr/sbin/sshd"},
		// Stopping sshd on a remote host locks the operator out of the machine
		// they are administering, and the panel cannot put them back. Restart
		// is allowed — it is how a configuration change is applied, and it
		// keeps the listening socket.
		Protected: true,
	},
	{
		Key:       "fail2ban",
		Label:     "Fail2Ban",
		Role:      RoleSystem,
		Summary:   "Bans hosts that repeatedly fail to authenticate.",
		Units:     []string{"fail2ban.service"},
		Processes: []string{"fail2ban-server", "fail2ban"},
		Binaries:  []string{"/usr/bin/fail2ban-server", "/usr/bin/fail2ban-client"},
		// Not essential: a host without it serves every website exactly as well,
		// and an alert for a daemon somebody deliberately switched off is an
		// alert nobody reads.
	},
	{
		Key:     "proftpd",
		Label:   "FTP Server",
		Role:    RoleSystem,
		Summary: "Serves files over FTP and FTPS.",
		Units:   []string{"proftpd.service"},
		// The FTP page changes the configuration and asks for a restart; it is
		// the Services page that starts and stops the daemon, so there is one
		// thing on this host that owns its lifecycle.
		Processes: []string{"proftpd"},
		Binaries:  []string{"/usr/sbin/proftpd", "/usr/bin/proftpd"},
	},
	{
		Key:     "postfix",
		Label:   "Mail Transport",
		Role:    RoleSystem,
		Summary: "Accepts, routes and sends mail.",
		Units:   []string{"postfix.service"},
		// The mail page writes the configuration and asks for a reload; the
		// Services page starts and stops the daemon, so one thing on this host
		// owns its lifecycle.
		Processes: []string{"master"},
		Binaries:  []string{"/usr/sbin/postfix", "/usr/libexec/postfix/master"},
	},
	{
		Key:     "dovecot",
		Label:   "Mailbox Server",
		Role:    RoleSystem,
		Summary: "Serves mailboxes over IMAP and POP3, and authenticates mail logins.",
		Units:   []string{"dovecot.service"},
		// Stopping this does not stop mail arriving — Postfix goes on accepting
		// it and queues it — but it stops every customer reading their mail and
		// stops every one of them sending, because submission authenticates
		// against Dovecot.
		Processes: []string{"dovecot"},
		Binaries:  []string{"/usr/sbin/dovecot"},
	},
	{
		Key:       "rspamd",
		Label:     "Mail Filter",
		Role:      RoleSystem,
		Summary:   "Filters incoming mail for spam and signs outgoing mail with DKIM.",
		Units:     []string{"rspamd.service"},
		Processes: []string{"rspamd"},
		Binaries:  []string{"/usr/sbin/rspamd", "/usr/bin/rspamd"},
	},
	{
		Key:     "clamd",
		Label:   "Virus Scanner",
		Role:    RoleSystem,
		Summary: "Scans mail attachments for malware.",
		// Alpine and Debian both call the daemon clamd; RHEL's unit carries the
		// socket name.
		Units:     []string{"clamd.service", "clamav-daemon.service"},
		Processes: []string{"clamd"},
		Binaries:  []string{"/usr/sbin/clamd"},
	},
	{
		Key:     "named",
		Label:   "DNS Server",
		Role:    RoleSystem,
		Summary: "Answers DNS queries for the zones this host serves.",
		// Alpine and RHEL call the unit named; Debian calls it bind9.
		Units:     []string{"named.service", "bind9.service"},
		Processes: []string{"named"},
		Binaries:  []string{"/usr/sbin/named"},
		// The DNS page writes zones and asks for a reload; the Services page
		// starts and stops the daemon, so one thing on this host owns its
		// lifecycle.
	},
	{
		Key:       "cron",
		Label:     "Cron",
		Role:      RoleSystem,
		Summary:   "Runs scheduled tasks.",
		Units:     []string{"crond.service", "cron.service", "cronie.service"},
		Processes: []string{"crond", "cron"},
		Binaries:  []string{"/usr/sbin/crond", "/usr/sbin/cron", "/usr/bin/crond"},
	},
}

// The verbs a service may be given.
//
// Deliberately absent: mask, unmask, and edit. Masking a unit makes it
// unstartable in a way that looks like a broken package to everyone who comes
// later, and none of the three belongs in a panel's service list.
const (
	ActionStart   = "start"
	ActionStop    = "stop"
	ActionRestart = "restart"
	ActionEnable  = "enable"
	ActionDisable = "disable"
)

// ErrProtected means the verb would take away something the panel must not.
var ErrProtected = errors.New("this service cannot be changed from the panel")

// ErrSelfManaged means the daemon's lifecycle belongs to another part of the
// panel, which would go on owning it whatever the init system was told.
var ErrSelfManaged = errors.New("this service is managed elsewhere in the panel")

// Allows reports whether a verb may be applied to this service.
//
// The rule lives here, next to the flag it reads, rather than in the request
// handler: it is a property of the service, and a second entry point that
// forgot to check would be a second way to lock an operator out of their host.
func (d Definition) Allows(action string) error {
	if d.SelfManaged {
		owner := d.SelfManagedBy
		if owner == "" {
			owner = "the panel"
		}
		return fmt.Errorf("%w: %s is started and stopped by %s, not from here",
			ErrSelfManaged, d.Label, owner)
	}

	switch action {
	case ActionStart, ActionRestart, ActionEnable:
		return nil
	case ActionStop, ActionDisable:
		if d.Protected {
			return fmt.Errorf("%w: %s is how this host is administered, and the "+
				"panel cannot put it back", ErrProtected, d.Label)
		}
		return nil
	default:
		return fmt.Errorf("%w: %q is not one of start, stop, restart, enable, disable",
			ErrInvalidName, action)
	}
}

// Catalogue returns the built-in definitions.
func Catalogue() []Definition {
	out := make([]Definition, len(catalogue))
	copy(out, catalogue)
	return out
}

// Lookup finds a definition by key, in the built-in set or the extras.
//
// Extras are contributed by callers that know about services the catalogue
// cannot list statically — PHP-FPM has one unit per installed version, which
// is a fact only the PHP detector holds.
func Lookup(key string, extra []Definition) (Definition, error) {
	for _, definition := range extra {
		if definition.Key == key {
			return definition, nil
		}
	}
	for _, definition := range catalogue {
		if definition.Key == key {
			return definition, nil
		}
	}
	return Definition{}, fmt.Errorf("%w: %q is not a service this panel manages",
		ErrNotFound, key)
}

// keyPattern is what a service key may contain.
//
// It is checked even though a key never becomes a command argument, because a
// key that fails this is a key no Definition can have — so the failure is
// "unknown service", reported without a lookup, rather than a mystery.
func validKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, r := range key {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// PHPFPMDefinitions builds catalogue entries for the installed PHP versions.
//
// PHP-FPM is one service per version, and which versions exist is discovered
// at runtime — so these are contributed by the caller that knows, rather than
// written into a static table that would be wrong on every host.
func PHPFPMDefinitions(versions []string) []Definition {
	definitions := make([]Definition, 0, len(versions))

	for _, version := range versions {
		compact := strings.ReplaceAll(version, ".", "")
		definitions = append(definitions, Definition{
			Key:     "php-fpm" + version,
			Label:   "PHP-FPM " + version,
			Role:    RoleRuntime,
			Summary: "Runs PHP for the websites on version " + version + ".",
			Units: []string{
				"php-fpm" + compact + ".service",
				"php" + version + "-fpm.service",
				"php-fpm" + version + ".service",
			},
			Processes: []string{"php-fpm" + compact, "php-fpm" + version},
			Binaries: []string{
				"/usr/sbin/php-fpm" + compact,
				"/usr/sbin/php-fpm" + version,
				"/usr/bin/php-fpm" + compact,
			},
			// A pool that is down is a 502 on every PHP site using it.
			Essential: true,
			// The PHP manager starts each version's master and signals it to
			// reload when a site's pool changes.
			SelfManaged:   true,
			SelfManagedBy: "the PHP manager",
		})
	}

	sort.Slice(definitions, func(i, j int) bool {
		return definitions[i].Key < definitions[j].Key
	})
	return definitions
}

// installed reports whether any of a definition's binaries is present.
func (d Definition) installed() bool {
	for _, path := range d.Binaries {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

// errUnknownService is the one answer given for a key that is not in the
// catalogue and for a key that is not a valid key at all.
//
// The two are deliberately indistinguishable: a caller probing for which
// services exist learns nothing from the difference, and an operator reading
// the message wants the same next step either way.
func errUnknownService(key string) error {
	return fmt.Errorf("%w: %q is not a service this panel manages", ErrNotFound, key)
}
