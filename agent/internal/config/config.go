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
	MySQLSocket       string
	MySQLAdminUser    string
	MySQLAdminPass    string
	PsqlPath          string
	PostgresHost      string
	PostgresPort      int
	PostgresAdminUser string
	PostgresAdminPass string
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

		ApachePath:        getString("AGENT_APACHE_PATH", "/usr/sbin/httpd"),
		Apache2Path:       getString("AGENT_APACHE2_PATH", "/usr/sbin/apache2"),
		ApacheConfigDir:   getString("AGENT_APACHE_CONF_DIR", "/etc/apache2/conf.d"),
		ApacheMainConf:    getString("AGENT_APACHE_MAIN_CONF", "/etc/apache2/httpd.conf"),
		SiteRoot:          getString("AGENT_SITE_ROOT", "/var/www"),
		LogRoot:           getString("AGENT_LOG_ROOT", "/var/log"),
		Fail2BanPath:      getString("AGENT_FAIL2BAN_PATH", "/usr/bin/fail2ban-client"),
		ProftpdPath:       getString("AGENT_PROFTPD_PATH", "/usr/sbin/proftpd"),
		FtpasswdPath:      getString("AGENT_FTPASSWD_PATH", "/usr/bin/ftpasswd"),
		FtpwhoPath:        getString("AGENT_FTPWHO_PATH", "/usr/bin/ftpwho"),
		FtpquotaPath:      getString("AGENT_FTPQUOTA_PATH", "/usr/bin/ftpquota"),
		FTPConfigDir:      getString("AGENT_FTP_CONFIG_DIR", "/etc/proftpd"),
		FTPRunDir:         getString("AGENT_FTP_RUN_DIR", "/run/proftpd"),
		FTPLogDir:         getString("AGENT_FTP_LOG_DIR", "/var/log/proftpd"),
		Fail2BanConfigDir: getString("AGENT_FAIL2BAN_CONFIG_DIR", "/etc/fail2ban"),
		SSHDPath:          getString("AGENT_SSHD_PATH", "/usr/sbin/sshd"),
		SSHConfigDir:      getString("AGENT_SSH_CONFIG_DIR", "/etc/ssh"),
		ShellPath:         getString("AGENT_SHELL_PATH", "/bin/sh"),
		CronSpoolDir:      getString("AGENT_CRON_SPOOL_DIR", "/var/spool/cron/crontabs"),
		CronLogDir:        getString("AGENT_CRON_LOG_DIR", "/var/log/jothost/cron"),
		UseraddPath:       getString("AGENT_USERADD_PATH", "/usr/sbin/useradd"),
		AdduserPath:       getString("AGENT_ADDUSER_PATH", "/usr/sbin/adduser"),
		UserdelPath:       getString("AGENT_USERDEL_PATH", "/usr/sbin/userdel"),
		DeluserPath:       getString("AGENT_DELUSER_PATH", "/usr/sbin/deluser"),
		WebGroup:          getString("AGENT_WEB_GROUP", ""),

		// The MariaDB client is preferred because a MariaDB host ships it
		// under this name and a MySQL host symlinks the same name to its own.
		MySQLPath:      getString("AGENT_MYSQL_PATH", "/usr/bin/mariadb"),
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
