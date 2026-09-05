// Package config loads Host Agent configuration from the environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the validated Agent configuration.
type Config struct {
	// SocketPath is the Unix domain socket the Agent listens on. The Agent
	// never opens a TCP listener (ARCHITECTURE.md section 10).
	SocketPath string
	// SocketMode restricts who may talk to the privileged Agent.
	SocketMode os.FileMode
	// SocketGroup owns the socket group bit. The Agent runs as root while the
	// API runs unprivileged, so group ownership is what grants the API access
	// without widening the socket to the world. Empty disables the chown.
	SocketGroup string
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// OperationTimeout bounds every operation (CLAUDE.md section 6).
	OperationTimeout time.Duration
	// ShutdownTimeout bounds draining in-flight operations on SIGTERM.
	ShutdownTimeout time.Duration
	// MaxConcurrent limits simultaneous operations to prevent resource abuse.
	MaxConcurrent int

	// Token is the shared secret callers must present. It must never be
	// logged. Empty disables the check, which is only safe when the socket's
	// permissions are the sole boundary.
	Token string
	// AllowedUIDs lists the UIDs permitted to call the Agent, checked against
	// kernel-supplied peer credentials. Empty disables the check.
	AllowedUIDs []int

	// ProcRoot and SysRoot locate the kernel's virtual filesystems. They are
	// configurable so the Agent can read a host's /proc when running in a
	// container, and so collectors can be tested against fixtures.
	ProcRoot string
	SysRoot  string

	// AuditLogPath is the Agent's own append-only audit trail. Empty sends
	// audit records only to the structured log.
	AuditLogPath string

	// Job execution bounds.
	MaxJobs           int
	MaxConcurrentJobs int
	JobTimeout        time.Duration
	JobRetention      time.Duration

	// SystemctlPath is the absolute path to systemctl. It is configuration
	// rather than a PATH lookup so a hostile PATH cannot substitute a program.
	SystemctlPath string

	// Website provisioning paths and tools. Each is an absolute path rather
	// than a name resolved through PATH, for the same reason.
	NginxPath     string
	NginxSitesDir string
	// Apache is the backend in hybrid mode. Two binary names because
	// distributions disagree: Alpine and RHEL ship "httpd", Debian ships
	// "apache2". Both are allowlisted; whichever exists is used.
	// RCServicePath and RCUpdatePath drive OpenRC, which is what Alpine and
	// Gentoo run instead of systemd. Absent means this host is not managed by
	// OpenRC, which is the usual case.
	RCServicePath string
	RCUpdatePath  string

	// UFWPath is the firewall front end. Absent means the panel reports that
	// this host has no firewall it can manage, rather than failing requests.
	UFWPath string
	// FirewallStateDir holds rule backups and the pending-change marker, which
	// is what lets an Agent restart finish a rollback it had armed.
	FirewallStateDir string
	// FirewallGuardedPorts are extra ports a change may never close, beyond
	// the built-in 22, 80 and 443. A host with SSH elsewhere says so here.
	FirewallGuardedPorts string

	ApachePath      string
	Apache2Path     string
	ApacheConfigDir string
	ApacheMainConf  string
	SiteRoot        string
	// Fail2BanPath is the client the panel drives, and Fail2BanConfigDir is
	// where its configuration lives.
	Fail2BanPath      string
	Fail2BanConfigDir string
	// The FTP server and its tools. ProftpdPath is used to read the version and
	// to validate a configuration — never to start the daemon, which is the
	// service manager's job. FTPConfigDir is where its configuration lives, and
	// FTPRunDir is where the scoreboard of live sessions goes.
	ProftpdPath  string
	FtpasswdPath string
	FtpwhoPath   string
	FtpquotaPath string
	FTPConfigDir string
	FTPRunDir    string
	FTPLogDir    string
	// The mail server's programs and where its configuration lives.
	//
	// postconf is how the panel configures Postfix — it is Postfix's own
	// interface for exactly that, so the distribution keeps ownership of its
	// files. The daemon binary is used only to check a configuration and read a
	// version; starting and stopping go through the service manager, so one
	// thing on this host owns each daemon's lifecycle.
	PostconfPath  string
	PostmapPath   string
	PostaliasPath string
	PostfixPath   string
	PostqueuePath string
	PostsuperPath string
	DoveadmPath   string
	DovecotPath   string
	RspamadmPath  string
	RspamcPath    string
	SievecPath    string
	// MailConfigDir, DovecotConfigDir and RspamdConfigDir are the three
	// configuration roots; MailRoot is where Maildirs live and MailStateDir is
	// where the panel keeps what it generates — the lookup tables, the passwd
	// file, the Sieve scripts and the DKIM keys.
	MailConfigDir    string
	DovecotConfigDir string
	RspamdConfigDir  string
	MailRoot         string
	MailStateDir     string
	// Deployment. GitPath is the only one that reaches the network; the rest
	// are the build tools the template actions drive, and each may be absent,
	// which the panel reports as the action being unavailable rather than as a
	// deployment that failed obscurely. DeployStateDir holds the deploy keys.
	GitPath        string
	SSHKeygenPath  string
	ComposerPath   string
	NpmToolPath    string
	PHPToolPath    string
	DeployShell    string
	DeployStateDir string
	// Tenancy. DuPath measures a subscription's disk; TenantUnitDir is where
	// its systemd slice is written. du rather than a walk in Go because this
	// panel's own backups make hard links, and a walk would count one file
	// once per name and report a customer using several times what they have.
	DuPath        string
	TenantUnitDir string
	// The read-only companions the update reporter needs on a Debian host.
	// AptCachePath answers what versions exist, AptMarkPath which packages are
	// held, and DpkgQueryPath what is installed on the disk.
	AptCachePath  string
	AptMarkPath   string
	DpkgQueryPath string
	// APKWorldPath is Alpine's list of explicitly installed packages, where a
	// version pin lives — which is how a package that apk will never upgrade is
	// told apart from one that is simply outstanding. RebootFlagPath is the
	// file Debian touches when a restart is needed.
	APKWorldPath   string
	RebootFlagPath string

	// The name server and its checkers. NamedPath is used to read a version,
	// never to start the daemon. DNSConfigDir is where BIND's configuration
	// lives, and DNSStateDir is where the panel's zone files, the journals
	// named keeps beside them, and the DNSSEC keys it generates go — it is a
	// directory of the panel's own rather than BIND's, because named writes
	// into it and a directory named can write to should not be the one holding
	// the configuration it reads.
	NamedPath          string
	NamedCheckconfPath string
	NamedCheckzonePath string
	RndcPath           string
	DNSSECFromKeyPath  string
	DNSConfigDir       string
	DNSStateDir        string
	// SSHDPath is the SSH server binary, used to read and validate the
	// configuration — never to start it. SSHConfigDir is where its
	// configuration lives.
	SSHDPath     string
	SSHConfigDir string
	// ShellPath is the shell a scheduled job's "run now" uses. It is cron's own
	// shell, because a manual run that behaved differently from the scheduled
	// one would be worse than no manual run at all.
	ShellPath string
	// CronSpoolDir is where per-user crontabs live, and CronLogDir is where
	// each job's output is collected.
	CronSpoolDir string
	CronLogDir   string
	// LogRoot is where the host's own logs live. It bounds what the log viewer
	// can resolve to, alongside the site root: the catalogue decides which
	// files are offered, and these decide where those files may actually be.
	LogRoot     string
	UseraddPath string
	AdduserPath string
	UserdelPath string
	DeluserPath string
	// The group tools exist for the BusyBox path only: shadow-utils makes a
	// site's private group with useradd --user-group, and BusyBox has no
	// equivalent, so the group is created and removed as its own step.
	AddgroupPath string
	DelgroupPath string
	// WebGroup is the group the web server runs as. Site directories are
	// group-owned by it so the server can read what it serves. Empty probes
	// the conventional names.
	WebGroup string

	// Database management. Both admin passwords are optional and normally
	// empty: on a default installation the Agent runs as root and both servers
	// authenticate a local connection by the peer's uid, so no secret has to
	// exist at all. When one is set it reaches the client through a mode-0600
	// file, never through a command-line argument.
	MySQLPath         string
	MysqldumpPath     string
	PgDumpPath        string
	MySQLSocket       string
	MySQLAdminUser    string
	MySQLAdminPass    string
	PsqlPath          string
	PostgresHost      string
	PostgresPort      int
	PostgresAdminUser string
	PostgresAdminPass string

	// Backups. BackupWorkDir is where an archive is staged before it is sent
	// anywhere and where it is downloaded to before a restore; it holds a
	// complete copy of whatever is being backed up, so it needs the room.
	//
	// BackupLocalRoots bounds where a *local* destination may write. It is a
	// list rather than one directory because a host commonly has a second disk
	// mounted for backups, and it exists at all because without it "back up to
	// /etc/nginx" would be a way to write a file anywhere as root.
	//
	// SFTPPath is the OpenSSH client an off-host destination uses.
	BackupWorkDir    string
	BackupLocalRoots []string
	SFTPPath         string
}

// Load reads and validates Agent configuration.
func Load() (Config, error) {
	cfg := Config{
		SocketPath:       getString("AGENT_SOCKET", "/run/jothost/agent.sock"),
		SocketMode:       0o660,
		SocketGroup:      getString("AGENT_SOCKET_GROUP", "jothost"),
		LogLevel:         getString("LOG_LEVEL", "info"),
		OperationTimeout: getDuration("AGENT_OPERATION_TIMEOUT", 30*time.Second),
		ShutdownTimeout:  getDuration("AGENT_SHUTDOWN_TIMEOUT", 15*time.Second),
		MaxConcurrent:    getInt("AGENT_MAX_CONCURRENT", 16),

		Token:       getString("AGENT_TOKEN", ""),
		AllowedUIDs: getIntList("AGENT_ALLOWED_UIDS"),

		ProcRoot: getString("AGENT_PROC_ROOT", "/proc"),
		SysRoot:  getString("AGENT_SYS_ROOT", "/sys"),

		AuditLogPath: getString("AGENT_AUDIT_LOG", "/var/log/jothost/agent-audit.log"),

		MaxJobs:           getInt("AGENT_MAX_JOBS", 256),
		MaxConcurrentJobs: getInt("AGENT_MAX_CONCURRENT_JOBS", 4),
		JobTimeout:        getDuration("AGENT_JOB_TIMEOUT", 30*time.Minute),
		JobRetention:      getDuration("AGENT_JOB_RETENTION", 15*time.Minute),

		SystemctlPath: getString("AGENT_SYSTEMCTL_PATH", "/usr/bin/systemctl"),

		NginxPath:     getString("AGENT_NGINX_PATH", "/usr/sbin/nginx"),
		NginxSitesDir: getString("AGENT_NGINX_SITES_DIR", "/etc/nginx/conf.d"),

		// OpenRC, for the hosts that have no systemd. Alpine is the one that
		// matters: systemd cannot be installed there at all.
		RCServicePath: getString("AGENT_RC_SERVICE_PATH", "/sbin/rc-service"),
		RCUpdatePath:  getString("AGENT_RC_UPDATE_PATH", "/sbin/rc-update"),

		UFWPath:              getString("AGENT_UFW_PATH", "/usr/sbin/ufw"),
		FirewallStateDir:     getString("AGENT_FIREWALL_STATE_DIR", "/var/lib/jothost/firewall"),
		FirewallGuardedPorts: getString("AGENT_FIREWALL_GUARDED_PORTS", ""),

		ApachePath:         getString("AGENT_APACHE_PATH", "/usr/sbin/httpd"),
		Apache2Path:        getString("AGENT_APACHE2_PATH", "/usr/sbin/apache2"),
		ApacheConfigDir:    getString("AGENT_APACHE_CONF_DIR", "/etc/apache2/conf.d"),
		ApacheMainConf:     getString("AGENT_APACHE_MAIN_CONF", "/etc/apache2/httpd.conf"),
		SiteRoot:           getString("AGENT_SITE_ROOT", "/var/www"),
		LogRoot:            getString("AGENT_LOG_ROOT", "/var/log"),
		Fail2BanPath:       getString("AGENT_FAIL2BAN_PATH", "/usr/bin/fail2ban-client"),
		ProftpdPath:        getString("AGENT_PROFTPD_PATH", "/usr/sbin/proftpd"),
		FtpasswdPath:       getString("AGENT_FTPASSWD_PATH", "/usr/bin/ftpasswd"),
		FtpwhoPath:         getString("AGENT_FTPWHO_PATH", "/usr/bin/ftpwho"),
		FtpquotaPath:       getString("AGENT_FTPQUOTA_PATH", "/usr/bin/ftpquota"),
		FTPConfigDir:       getString("AGENT_FTP_CONFIG_DIR", "/etc/proftpd"),
		FTPRunDir:          getString("AGENT_FTP_RUN_DIR", "/run/proftpd"),
		FTPLogDir:          getString("AGENT_FTP_LOG_DIR", "/var/log/proftpd"),
		Fail2BanConfigDir:  getString("AGENT_FAIL2BAN_CONFIG_DIR", "/etc/fail2ban"),
		PostconfPath:       getString("AGENT_POSTCONF_PATH", "/usr/sbin/postconf"),
		PostmapPath:        getString("AGENT_POSTMAP_PATH", "/usr/sbin/postmap"),
		PostaliasPath:      getString("AGENT_POSTALIAS_PATH", "/usr/sbin/postalias"),
		PostfixPath:        getString("AGENT_POSTFIX_PATH", "/usr/sbin/postfix"),
		PostqueuePath:      getString("AGENT_POSTQUEUE_PATH", "/usr/sbin/postqueue"),
		PostsuperPath:      getString("AGENT_POSTSUPER_PATH", "/usr/sbin/postsuper"),
		DoveadmPath:        getString("AGENT_DOVEADM_PATH", "/usr/bin/doveadm"),
		DovecotPath:        getString("AGENT_DOVECOT_PATH", "/usr/sbin/dovecot"),
		RspamadmPath:       getString("AGENT_RSPAMADM_PATH", "/usr/bin/rspamadm"),
		RspamcPath:         getString("AGENT_RSPAMC_PATH", "/usr/bin/rspamc"),
		SievecPath:         getString("AGENT_SIEVEC_PATH", "/usr/bin/sievec"),
		MailConfigDir:      getString("AGENT_MAIL_CONFIG_DIR", "/etc/postfix"),
		DovecotConfigDir:   getString("AGENT_DOVECOT_CONFIG_DIR", "/etc/dovecot"),
		RspamdConfigDir:    getString("AGENT_RSPAMD_CONFIG_DIR", "/etc/rspamd"),
		MailRoot:           getString("AGENT_MAIL_ROOT", "/var/mail/vhosts"),
		MailStateDir:       getString("AGENT_MAIL_STATE_DIR", "/var/lib/jothost/mail"),
		GitPath:            getString("AGENT_GIT_PATH", "/usr/bin/git"),
		SSHKeygenPath:      getString("AGENT_SSH_KEYGEN_PATH", "/usr/bin/ssh-keygen"),
		ComposerPath:       getString("AGENT_COMPOSER_PATH", "/usr/bin/composer"),
		NpmToolPath:        getString("AGENT_NPM_TOOL_PATH", "/usr/bin/npm"),
		PHPToolPath:        getString("AGENT_PHP_TOOL_PATH", "/usr/bin/php"),
		DeployShell:        getString("AGENT_DEPLOY_SHELL", "/bin/sh"),
		DeployStateDir:     getString("AGENT_DEPLOY_STATE_DIR", "/var/lib/jothost/deploy"),
		DuPath:             getString("AGENT_DU_PATH", "/usr/bin/du"),
		TenantUnitDir:      getString("AGENT_TENANT_UNIT_DIR", "/etc/systemd/system"),
		AptCachePath:       getString("AGENT_APT_CACHE_PATH", "/usr/bin/apt-cache"),
		AptMarkPath:        getString("AGENT_APT_MARK_PATH", "/usr/bin/apt-mark"),
		DpkgQueryPath:      getString("AGENT_DPKG_QUERY_PATH", "/usr/bin/dpkg-query"),
		APKWorldPath:       getString("AGENT_APK_WORLD_PATH", "/etc/apk/world"),
		RebootFlagPath:     getString("AGENT_REBOOT_FLAG_PATH", "/var/run/reboot-required"),
		NamedPath:          getString("AGENT_NAMED_PATH", "/usr/sbin/named"),
		NamedCheckconfPath: getString("AGENT_NAMED_CHECKCONF_PATH", "/usr/bin/named-checkconf"),
		NamedCheckzonePath: getString("AGENT_NAMED_CHECKZONE_PATH", "/usr/bin/named-checkzone"),
		RndcPath:           getString("AGENT_RNDC_PATH", "/usr/sbin/rndc"),
		DNSSECFromKeyPath:  getString("AGENT_DNSSEC_DSFROMKEY_PATH", "/usr/bin/dnssec-dsfromkey"),
		DNSConfigDir:       getString("AGENT_DNS_CONFIG_DIR", "/etc/bind"),
		DNSStateDir:        getString("AGENT_DNS_STATE_DIR", "/var/bind/jothost"),
		SSHDPath:           getString("AGENT_SSHD_PATH", "/usr/sbin/sshd"),
		SSHConfigDir:       getString("AGENT_SSH_CONFIG_DIR", "/etc/ssh"),
		ShellPath:          getString("AGENT_SHELL_PATH", "/bin/sh"),
		CronSpoolDir:       getString("AGENT_CRON_SPOOL_DIR", "/var/spool/cron/crontabs"),
		CronLogDir:         getString("AGENT_CRON_LOG_DIR", "/var/log/jothost/cron"),
		UseraddPath:        getString("AGENT_USERADD_PATH", "/usr/sbin/useradd"),
		AdduserPath:        getString("AGENT_ADDUSER_PATH", "/usr/sbin/adduser"),
		UserdelPath:        getString("AGENT_USERDEL_PATH", "/usr/sbin/userdel"),
		DeluserPath:        getString("AGENT_DELUSER_PATH", "/usr/sbin/deluser"),
		AddgroupPath:       getString("AGENT_ADDGROUP_PATH", "/usr/sbin/addgroup"),
		DelgroupPath:       getString("AGENT_DELGROUP_PATH", "/usr/sbin/delgroup"),
		WebGroup:           getString("AGENT_WEB_GROUP", ""),

		// The MariaDB client is preferred because a MariaDB host ships it
		// under this name and a MySQL host symlinks the same name to its own.
		MySQLPath:      getString("AGENT_MYSQL_PATH", "/usr/bin/mariadb"),
		MysqldumpPath:  getString("AGENT_MYSQLDUMP_PATH", "/usr/bin/mariadb-dump"),
		PgDumpPath:     getString("AGENT_PG_DUMP_PATH", "/usr/bin/pg_dump"),
		MySQLSocket:    getString("AGENT_MYSQL_SOCKET", "/run/mysqld/mysqld.sock"),
		MySQLAdminUser: getString("AGENT_MYSQL_ADMIN_USER", "root"),
		MySQLAdminPass: getString("AGENT_MYSQL_ADMIN_PASSWORD", ""),

		PsqlPath: getString("AGENT_PSQL_PATH", "/usr/bin/psql"),
		// A leading slash makes libpq treat this as a socket directory rather
		// than a hostname, which is what keeps peer authentication working.
		PostgresHost:      getString("AGENT_POSTGRES_HOST", "/run/postgresql"),
		PostgresPort:      getInt("AGENT_POSTGRES_PORT", 5432),
		PostgresAdminUser: getString("AGENT_POSTGRES_ADMIN_USER", "postgres"),
		PostgresAdminPass: getString("AGENT_POSTGRES_ADMIN_PASSWORD", ""),

		BackupWorkDir: getString("AGENT_BACKUP_WORK_DIR", "/var/lib/jothost/backups"),
		// The working directory is itself an allowed local destination, which
		// is what makes a panel work out of the box on a host with one disk.
		// Adding another is a deliberate configuration change.
		BackupLocalRoots: getList("AGENT_BACKUP_LOCAL_ROOTS",
			[]string{"/var/lib/jothost/backups", "/backup", "/backups"}),
		SFTPPath: getString("AGENT_SFTP_PATH", "/usr/bin/sftp"),
	}

	var problems []string
	problems = append(problems, validateAbsolute("AGENT_SOCKET", cfg.SocketPath)...)
	problems = append(problems, validateAbsolute("AGENT_PROC_ROOT", cfg.ProcRoot)...)
	problems = append(problems, validateAbsolute("AGENT_SYS_ROOT", cfg.SysRoot)...)
	problems = append(problems, validateAbsolute("AGENT_SYSTEMCTL_PATH", cfg.SystemctlPath)...)
	problems = append(problems, validateAbsolute("AGENT_NGINX_PATH", cfg.NginxPath)...)
	problems = append(problems, validateAbsolute("AGENT_NGINX_SITES_DIR", cfg.NginxSitesDir)...)
	problems = append(problems, validateAbsolute("AGENT_RC_SERVICE_PATH", cfg.RCServicePath)...)
	problems = append(problems, validateAbsolute("AGENT_RC_UPDATE_PATH", cfg.RCUpdatePath)...)
	problems = append(problems, validateAbsolute("AGENT_UFW_PATH", cfg.UFWPath)...)
	problems = append(problems, validateAbsolute("AGENT_FIREWALL_STATE_DIR", cfg.FirewallStateDir)...)
	problems = append(problems, validateAbsolute("AGENT_APACHE_PATH", cfg.ApachePath)...)
	problems = append(problems, validateAbsolute("AGENT_APACHE2_PATH", cfg.Apache2Path)...)
	problems = append(problems, validateAbsolute("AGENT_APACHE_CONF_DIR", cfg.ApacheConfigDir)...)
	problems = append(problems, validateAbsolute("AGENT_APACHE_MAIN_CONF", cfg.ApacheMainConf)...)
	problems = append(problems, validateAbsolute("AGENT_SITE_ROOT", cfg.SiteRoot)...)
	problems = append(problems, validateAbsolute("AGENT_LOG_ROOT", cfg.LogRoot)...)
	problems = append(problems, validateAbsolute("AGENT_FAIL2BAN_PATH", cfg.Fail2BanPath)...)
	problems = append(problems,
		validateAbsolute("AGENT_FAIL2BAN_CONFIG_DIR", cfg.Fail2BanConfigDir)...)
	problems = append(problems, validateAbsolute("AGENT_SSHD_PATH", cfg.SSHDPath)...)
	problems = append(problems, validateAbsolute("AGENT_SSH_CONFIG_DIR", cfg.SSHConfigDir)...)
	problems = append(problems, validateAbsolute("AGENT_SHELL_PATH", cfg.ShellPath)...)
	problems = append(problems, validateAbsolute("AGENT_CRON_SPOOL_DIR", cfg.CronSpoolDir)...)
	problems = append(problems, validateAbsolute("AGENT_CRON_LOG_DIR", cfg.CronLogDir)...)
	problems = append(problems, validateAbsolute("AGENT_USERADD_PATH", cfg.UseraddPath)...)
	problems = append(problems, validateAbsolute("AGENT_ADDUSER_PATH", cfg.AdduserPath)...)
	problems = append(problems, validateAbsolute("AGENT_MYSQL_PATH", cfg.MySQLPath)...)
	problems = append(problems, validateAbsolute("AGENT_PSQL_PATH", cfg.PsqlPath)...)
	problems = append(problems, validateAbsolute("AGENT_MYSQLDUMP_PATH", cfg.MysqldumpPath)...)
	problems = append(problems, validateAbsolute("AGENT_PG_DUMP_PATH", cfg.PgDumpPath)...)
	problems = append(problems, validateAbsolute("AGENT_SFTP_PATH", cfg.SFTPPath)...)
	problems = append(problems, validateAbsolute("AGENT_BACKUP_WORK_DIR", cfg.BackupWorkDir)...)
	for _, root := range cfg.BackupLocalRoots {
		problems = append(problems, validateAbsolute("AGENT_BACKUP_LOCAL_ROOTS", root)...)
	}

	if cfg.AuditLogPath != "" {
		problems = append(problems, validateAbsolute("AGENT_AUDIT_LOG", cfg.AuditLogPath)...)
	}

	if cfg.OperationTimeout <= 0 {
		problems = append(problems, "AGENT_OPERATION_TIMEOUT must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		problems = append(problems, "AGENT_SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if cfg.MaxConcurrent <= 0 {
		problems = append(problems, "AGENT_MAX_CONCURRENT must be greater than zero")
	}
	if cfg.MaxJobs <= 0 {
		problems = append(problems, "AGENT_MAX_JOBS must be greater than zero")
	}
	if cfg.MaxConcurrentJobs <= 0 {
		problems = append(problems, "AGENT_MAX_CONCURRENT_JOBS must be greater than zero")
	}
	if cfg.JobTimeout <= 0 {
		problems = append(problems, "AGENT_JOB_TIMEOUT must be greater than zero")
	}
	if cfg.JobRetention <= 0 {
		problems = append(problems, "AGENT_JOB_RETENTION must be greater than zero")
	}
	// A token that is present but trivially short gives false confidence; a
	// short one is worse than none because it looks like protection.
	if cfg.Token != "" && len(cfg.Token) < MinTokenLength {
		problems = append(problems,
			fmt.Sprintf("AGENT_TOKEN must be at least %d characters (generate one with: openssl rand -hex 32)", MinTokenLength))
	}
	for _, uid := range cfg.AllowedUIDs {
		if uid < 0 {
			problems = append(problems, "AGENT_ALLOWED_UIDS must contain non-negative user IDs")
			break
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// MinTokenLength is the shortest accepted shared secret.
const MinTokenLength = 32

// Authenticated reports whether at least one caller check is configured.
//
// The Agent warns rather than refuses when neither is set: a development
// container where the socket's permissions are the only boundary is a valid
// configuration, but it should never be a silent one.
func (c Config) Authenticated() bool {
	return c.Token != "" || len(c.AllowedUIDs) > 0
}

// validateAbsolute checks a path is absolute and free of traversal segments.
func validateAbsolute(name, path string) []string {
	var problems []string
	if !filepath.IsAbs(path) {
		problems = append(problems, name+" must be an absolute path")
	}
	if strings.Contains(path, "..") {
		problems = append(problems, name+" must not contain '..'")
	}
	return problems
}

func getString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

// getList parses a comma-separated list of strings, falling back when unset.
//
// An empty entry is dropped rather than kept: a trailing comma is a typo, and a
// list containing "" would be a directory check against the empty path.
func getList(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	values := []string{}
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	if len(values) == 0 {
		return fallback
	}
	return values
}

func getInt(key string, fallback int) int {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return fallback
	}
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
		return v
	}
	return fallback
}

// getIntList parses a comma-separated list of integers. Unparsable entries are
// skipped rather than failing the whole list, so one typo does not lock out
// every configured caller.
func getIntList(key string) []int {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}

	var values []int
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if v, err := strconv.Atoi(part); err == nil {
			values = append(values, v)
		}
	}
	return values
}

func getDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil {
		return d
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
