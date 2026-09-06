// Command agent is the JotHost Host Agent: the only component permitted to
// perform privileged server operations (PRD.md section 10). It listens on a
// Unix domain socket and never exposes a network port.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jothost/panel/agent/internal/apache"
	"github.com/jothost/panel/agent/internal/audit"
	backuppkg "github.com/jothost/panel/agent/internal/backup"
	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/cron"
	"github.com/jothost/panel/agent/internal/database"
	deploypkg "github.com/jothost/panel/agent/internal/deploy"
	dnspkg "github.com/jothost/panel/agent/internal/dns"
	f2bpkg "github.com/jothost/panel/agent/internal/fail2ban"
	"github.com/jothost/panel/agent/internal/files"
	"github.com/jothost/panel/agent/internal/firewall"
	ftppkg "github.com/jothost/panel/agent/internal/ftp"
	"github.com/jothost/panel/agent/internal/grafana"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/logs"
	mailpkg "github.com/jothost/panel/agent/internal/mail"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/nodejs"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/agent/internal/panelweb"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/pma"
	securitypkg "github.com/jothost/panel/agent/internal/security"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/socket"
	sshpkg "github.com/jothost/panel/agent/internal/ssh"
	"github.com/jothost/panel/agent/internal/ssl"
	tenancypkg "github.com/jothost/panel/agent/internal/tenancy"
	updatespkg "github.com/jothost/panel/agent/internal/updates"
	"github.com/jothost/panel/shared/logger"
	"github.com/jothost/panel/shared/protocol"
	"github.com/jothost/panel/shared/version"
)

func main() {
	// -ping runs the binary as a client instead of a daemon. Container and
	// systemd health checks use it so no extra tooling is required in the
	// runtime image.
	pingMode := flag.Bool("ping", false, "probe a running agent over its socket and exit")
	// -call is an operator diagnostic: it asks a running Agent for one
	// operation and prints the reply. It authenticates like any other caller.
	callOp := flag.String("call", "", "send one operation to a running agent and print the response")
	callPayload := flag.String("payload", "", "JSON payload for -call")
	callAsync := flag.Bool("async", false, "submit -call as a background job")
	// -repair-site-ownership is a maintenance mode for a host that already has
	// abandoned site directories. It reassigns the ones no account owns and
	// reports the rest, then exits without starting the Agent.
	repairOwnership := flag.Bool("repair-site-ownership", false,
		"reassign abandoned site directories to root, report ambiguous ones, and exit")
	showVersion := flag.Bool("version", false, "print the build stamp and exit")
	flag.Parse()

	// Answered before the configuration is loaded, deliberately. An operator
	// asking a binary they have just downloaded what it is has no
	// configuration yet, and refusing until they write one is refusing the one
	// question worth asking before installing anything.
	//
	// `version` without a dash is accepted too: the API binary takes
	// subcommands and this one takes flags, and nobody should have to remember
	// which is which to ask the same question.
	if *showVersion || (flag.NArg() > 0 && flag.Arg(0) == "version") {
		info := version.Current()
		fmt.Printf("jothost-agent %s (commit %s, built %s)\n",
			info.Version, info.Commit, info.BuildDate)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent: fatal: %v\n", err)
		os.Exit(1)
	}

	if *pingMode {
		if err := ping(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "agent: ping failed: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("ok")
		return
	}

	// Before the Agent starts, and without it. This walks the site root and
	// changes ownership, which is not something to do behind a running server's
	// back while it is provisioning sites into the same directories.
	if *repairOwnership {
		os.Exit(repairSiteOwnership(cfg))
	}

	if *callOp != "" {
		if err := call(cfg, *callOp, *callPayload, *callAsync); err != nil {
			fmt.Fprintf(os.Stderr, "agent: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "agent: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run(cfg config.Config) error {
	log := logger.New(logger.Options{
		Service: "agent",
		Level:   cfg.LogLevel,
		Output:  os.Stdout,
	})
	log.Info("starting jothost agent",
		"version", version.Current().Version,
		"socket", cfg.SocketPath,
		"proc_root", cfg.ProcRoot,
	)

	// An Agent with no caller checks is a valid development configuration but
	// never an acceptable production one, so it is called out loudly rather
	// than accepted in silence.
	if !cfg.Authenticated() {
		log.Warn("agent is running without caller authentication",
			"detail", "set AGENT_TOKEN and AGENT_ALLOWED_UIDS; socket permissions are the only boundary")
	}

	auditWriter, err := audit.NewWriter(audit.Options{Path: cfg.AuditLogPath, Log: log})
	if err != nil {
		// A missing audit file degrades to logger-only auditing rather than
		// stopping the daemon; the failure is reported, not hidden.
		log.Error("audit log unavailable, falling back to structured logs only",
			logger.KeyError, err.Error())
	}
	defer func() {
		if err := auditWriter.Close(); err != nil {
			log.Error("failed to close audit log", logger.KeyError, err.Error())
		}
	}()

	registry, jobRunner, err := buildRegistry(cfg, log)
	if err != nil {
		return err
	}

	srv := socket.New(socket.Options{
		Config:   cfg,
		Log:      log,
		Registry: registry,
		Auth: socket.NewAuthenticator(socket.AuthOptions{
			AllowedUIDs: cfg.AllowedUIDs,
			Token:       cfg.Token,
		}),
		Audit: auditWriter,
	})

	if err := srv.Listen(); err != nil {
		log.Error("failed to bind agent socket", logger.KeyError, err.Error())
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := srv.Serve(ctx)

	// Running jobs are cancelled and drained after the listener stops, so a
	// shutdown does not leave a half-finished infrastructure change behind
	// without recording that it was interrupted.
	if err := jobRunner.Shutdown(cfg.ShutdownTimeout); err != nil {
		log.Error("job runner did not drain cleanly", logger.KeyError, err.Error())
	}

	if serveErr != nil {
		log.Error("agent terminated with error", logger.KeyError, serveErr.Error())
		return serveErr
	}
	return nil
}

// buildRegistry assembles the operation registry and its collaborators.
func buildRegistry(cfg config.Config, log *slog.Logger) (*operations.Registry, *jobs.Runner, error) {
	collector := collectors.New(collectors.Options{
		ProcRoot: cfg.ProcRoot,
		SysRoot:  cfg.SysRoot,
	})
	if !collector.Available() {
		// Metrics are advertised through agent.info capabilities, so an
		// unavailable /proc produces honest "unsupported" answers rather than
		// confusing internal errors.
		log.Warn("metrics are unavailable: /proc is not readable", "proc_root", cfg.ProcRoot)
	}

	// The command allowlist is the complete set of programs this Agent may
	// ever execute. Adding one is a deliberate, reviewable change.
	//
	// Both account tools are listed because distributions disagree: Debian and
	// RHEL ship shadow-utils useradd, Alpine ships BusyBox adduser. The
	// provider uses whichever is actually installed.
	specs := []command.Spec{
		{Name: services.CommandName, Path: cfg.SystemctlPath, Timeout: 10 * time.Second},
		// The other init system. Both are allowlisted on every host and the
		// provider uses whichever is actually installed — the same arrangement
		// as useradd and adduser, and for the same reason: distributions
		// disagree, and the Agent must not resolve a path from a request.
		{Name: services.CommandRCService, Path: cfg.RCServicePath, Timeout: 30 * time.Second},
		// fail2ban-client, the daemon's only interface. Every argument it is
		// given here is a jail name from the Agent's catalogue or an address
		// that has been parsed.
		{Name: f2bpkg.CommandName, Path: cfg.Fail2BanPath, Timeout: 30 * time.Second},
		// The FTP server and its tools. proftpd itself is here only to read a
		// version and to validate a configuration (-t); the daemon is started
		// and stopped through the service manager, so one thing on this host
		// owns its lifecycle.
		//
		// ftpasswd is the only one of these that is ever given a secret, and it
		// is given it on standard input rather than as an argument — an
		// argument is visible in /proc to every account on the host.
		{Name: ftppkg.CommandProftpd, Path: cfg.ProftpdPath, Timeout: 20 * time.Second},
		{Name: ftppkg.CommandFtpasswd, Path: cfg.FtpasswdPath, Timeout: 15 * time.Second},
		{Name: ftppkg.CommandFtpwho, Path: cfg.FtpwhoPath, Timeout: 10 * time.Second},
		{Name: ftppkg.CommandFtpquota, Path: cfg.FtpquotaPath, Timeout: 15 * time.Second},
		// The mail server.
		//
		// postconf is the one that matters: it is how Postfix's own
		// configuration is changed, which is why the panel never writes
		// main.cf or master.cf itself. The daemon binaries are here only to
		// check a configuration and read a version — both servers are started
		// and stopped through the service manager, so one thing on this host
		// owns each daemon's lifecycle.
		//
		// None of these is ever given a password. Mailbox passwords are hashed
		// by the API before they reach this process, so the Agent has no
		// plaintext to leak into a process table that every account on the host
		// can read.
		{Name: mailpkg.CommandPostconf, Path: cfg.PostconfPath, Timeout: 20 * time.Second},
		{Name: mailpkg.CommandPostmap, Path: cfg.PostmapPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandPostalias, Path: cfg.PostaliasPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandPostfix, Path: cfg.PostfixPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandPostqueue, Path: cfg.PostqueuePath, Timeout: 20 * time.Second},
		{Name: mailpkg.CommandPostsuper, Path: cfg.PostsuperPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandDoveadm, Path: cfg.DoveadmPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandDovecot, Path: cfg.DovecotPath, Timeout: 20 * time.Second},
		{Name: mailpkg.CommandRspamadm, Path: cfg.RspamadmPath, Timeout: 30 * time.Second},
		{Name: mailpkg.CommandRspamc, Path: cfg.RspamcPath, Timeout: 30 * time.Second},
		// The signature database is hundreds of megabytes and this is the one
		// command in the panel whose honest timeout is measured in minutes.
		{Name: mailpkg.CommandFreshclam, Path: cfg.FreshclamPath, Timeout: 20 * time.Minute},
		{Name: mailpkg.CommandSievec, Path: cfg.SievecPath, Timeout: 20 * time.Second},
		// Deployment.
		//
		// git is the only one of these that reaches the network, and its
		// timeout is generous because a first clone of a large repository is
		// genuinely slow. What keeps it bounded is not the timeout but the
		// remote: validate.GitRemote refuses everything but https and ssh,
		// because git's "ext::" transport runs a command of the caller's
		// choosing and a remote beginning with a hyphen is an option.
		//
		// The deployment shell is its own allowlist entry rather than a shared
		// "sh", for the reason cron's is: it makes the set of places in this
		// Agent that can reach a shell a list somebody can read.
		{Name: deploypkg.CommandGit, Path: cfg.GitPath, Timeout: 15 * time.Minute,
			AllowedEnv: []string{
				"HOME", "GIT_TERMINAL_PROMPT", "GIT_CONFIG_NOSYSTEM", "GIT_SSH_COMMAND",
			}},
		{Name: deploypkg.CommandSSHKeygen, Path: cfg.SSHKeygenPath, Timeout: 30 * time.Second},
		{Name: deploypkg.CommandComposer, Path: cfg.ComposerPath, Timeout: 15 * time.Minute,
			AllowedEnv: []string{
				"HOME", "USER", "LOGNAME", "CI", "COMPOSER_HOME", "COMPOSER_NO_INTERACTION",
				"NPM_CONFIG_CACHE", "NPM_CONFIG_UPDATE_NOTIFIER",
			}},
		{Name: deploypkg.CommandNpm, Path: cfg.NpmToolPath, Timeout: 15 * time.Minute,
			AllowedEnv: []string{
				"HOME", "USER", "LOGNAME", "CI", "COMPOSER_HOME", "COMPOSER_NO_INTERACTION",
				"NPM_CONFIG_CACHE", "NPM_CONFIG_UPDATE_NOTIFIER",
			}},
		{Name: deploypkg.CommandPHP, Path: cfg.PHPToolPath, Timeout: 15 * time.Minute,
			AllowedEnv: []string{
				"HOME", "USER", "LOGNAME", "CI", "COMPOSER_HOME", "COMPOSER_NO_INTERACTION",
				"NPM_CONFIG_CACHE", "NPM_CONFIG_UPDATE_NOTIFIER",
			}},
		{Name: deploypkg.CommandDeployShell, Path: cfg.DeployShell, Timeout: 60 * time.Minute,
			AllowedEnv: []string{
				"HOME", "USER", "LOGNAME", "CI", "COMPOSER_HOME", "COMPOSER_NO_INTERACTION",
				"NPM_CONFIG_CACHE", "NPM_CONFIG_UPDATE_NOTIFIER",
			}},
		// Tenancy. du measures a subscription's disk usage.
		//
		// It gets a name of its own rather than a shared "du" for the reason
		// the deployment tools do: a name is owned by whichever feature
		// claimed it first, and changing how long a quota measurement may run
		// must not change anything else. Five minutes because a customer with
		// a hundred thousand small files is slow to walk and is not a fault.
		{Name: tenancypkg.CommandDu, Path: cfg.DuPath, Timeout: 5 * time.Minute},
		// sshd, used only to *read* the effective configuration (-T) and to
		// validate a candidate one (-t). The panel never starts the server with
		// it: that goes through the service manager, so there is one thing on
		// this host that owns the daemon's lifecycle.
		// The read-only companions apt needs. apt-get itself is already
		// allowlisted for Phase 5; these answer questions it cannot: which
		// packages are held, which versions still exist in the archive, and
		// what is actually installed on the disk.
		//
		// They are registered whether or not the binaries are there, the same
		// arrangement Node's runtime uses: Runner.Available checks at call
		// time, so on an Alpine host they are simply never usable.
		{Name: updatespkg.CommandAPTCache, Path: cfg.AptCachePath, Timeout: 60 * time.Second},
		{Name: updatespkg.CommandAPTMark, Path: cfg.AptMarkPath, Timeout: 30 * time.Second},
		{Name: updatespkg.CommandDpkgQuery, Path: cfg.DpkgQueryPath, Timeout: 60 * time.Second},
		// BIND and its checkers. named is here only to read a version — the
		// daemon is started through the service manager, so one thing on this
		// host owns its lifecycle — and named-checkconf and named-checkzone are
		// what stop the panel installing a zone that would keep the server from
		// starting at all.
		//
		// rndc is how a running server is told to re-read what the panel wrote.
		// Every argument it is given is a zone name that has been checked to be
		// a domain name.
		{Name: dnspkg.CommandNamed, Path: cfg.NamedPath, Timeout: 15 * time.Second},
		{Name: dnspkg.CommandNamedCheckconf, Path: cfg.NamedCheckconfPath, Timeout: 30 * time.Second},
		{Name: dnspkg.CommandNamedCheckzone, Path: cfg.NamedCheckzonePath, Timeout: 30 * time.Second},
		{Name: dnspkg.CommandRndc, Path: cfg.RndcPath, Timeout: 60 * time.Second},
		{Name: dnspkg.CommandDNSSECFromKey, Path: cfg.DNSSECFromKeyPath, Timeout: 15 * time.Second},
		{Name: sshpkg.CommandName, Path: cfg.SSHDPath, Timeout: 15 * time.Second},
		// The shell a scheduled job's "run now" uses, and the only entry here
		// whose argument is a command line rather than a parameter.
		//
		// It is a dedicated entry rather than a general "sh" the rest of the
		// Agent could reach for, so that every use of it is this one function
		// and shows up in a grep for the name. What makes it acceptable at all
		// is written out in agent/internal/cron/run.go: the caller could
		// schedule the same command a minute from now, the run drops to the
		// website's own account and refuses to run as root, and it is bounded
		// and audited.
		//
		// The timeout is generous because a real job — a database dump, a
		// sitemap rebuild — takes minutes, and a manual run that gave up before
		// the scheduled one would tell an operator nothing useful.
		{
			Name: cron.CommandShell, Path: cfg.ShellPath,
			Timeout:    10 * time.Minute,
			AllowedEnv: []string{"HOME", "USER", "LOGNAME"},
		},
		{Name: services.CommandRCUpdate, Path: cfg.RCUpdatePath, Timeout: 10 * time.Second},
		{Name: nginx.CommandName, Path: cfg.NginxPath, Timeout: 15 * time.Second},
		// Both Apache names, allowlisted whether or not the binary is there:
		// hybrid mode can be enabled on a host that installs Apache later, and
		// what stops any other program running in its place is that the path
		// is fixed here rather than resolved from a request.
		// ufw is allowlisted whether or not it is installed: a host may gain a
		// firewall later, and what stops any other program running in its place
		// is that the path is fixed here rather than resolved from a request.
		{Name: firewall.CommandName, Path: cfg.UFWPath, Timeout: 30 * time.Second},
		{Name: apache.CommandHTTPD, Path: cfg.ApachePath, Timeout: 20 * time.Second},
		{Name: apache.CommandApache2, Path: cfg.Apache2Path, Timeout: 20 * time.Second},
		// The two dump tools, and the sftp client an off-host backup needs.
		//
		// All three are allowlisted whether or not the binary is there, the
		// same arrangement the database clients use: an absent binary is
		// reported by Runner.Available, which is how the panel learns it cannot
		// dump that engine or reach an SFTP destination — and registering the
		// path here is what stops any other program ever running in its place.
		//
		// The timeouts are the longest in this allowlist by a wide margin,
		// because these are the only entries whose work is proportional to how
		// much data a customer has rather than to anything this panel controls.
		// A dump that gave up after thirty seconds would work on every test
		// database and on no real one.
		{
			Name: database.CommandMysqldump, Path: cfg.MysqldumpPath,
			Timeout: 6 * time.Hour,
		},
		{
			Name: database.CommandPgDump, Path: cfg.PgDumpPath,
			Timeout: 6 * time.Hour,
		},
		{
			Name: backuppkg.CommandSFTP, Path: cfg.SFTPPath,
			Timeout: 6 * time.Hour,
		},
		{Name: sites.CommandUseradd, Path: cfg.UseraddPath, Timeout: 15 * time.Second},
		{Name: sites.CommandAdduser, Path: cfg.AdduserPath, Timeout: 15 * time.Second},
		{Name: sites.CommandUserdel, Path: cfg.UserdelPath, Timeout: 15 * time.Second},
		{Name: sites.CommandDeluser, Path: cfg.DeluserPath, Timeout: 15 * time.Second},
		{Name: sites.CommandAddgroup, Path: cfg.AddgroupPath, Timeout: 15 * time.Second},
		{Name: sites.CommandDelgroup, Path: cfg.DelgroupPath, Timeout: 15 * time.Second},
	}

	// PHP contributes one allowlist entry per version actually present, found
	// by a filesystem probe that executes nothing. Every PHP binary the Agent
	// can run is therefore fixed at startup, and a request naming a version can
	// only ever reach a path resolved here — never one built from the request.
	specs = append(specs, php.CommandSpecs("")...)
	specs = append(specs, php.ManagerSpecs()...)
	specs = append(specs, ssl.CertbotSpec()...)

	// Node contributes an entry per tool actually present, found by a
	// filesystem probe that executes nothing. An application the panel starts
	// can therefore only ever be run by a binary resolved here.
	specs = append(specs, nodejs.CommandSpecs()...)

	// The two database clients. They are allowlisted unconditionally: an
	// absent binary is reported by Runner.Available, which is how the panel
	// learns the engine is not installed, and registering the path here is
	// what stops any other program ever being run in its place.
	specs = append(specs,
		command.Spec{
			Name: database.CommandMySQL, Path: cfg.MySQLPath, Timeout: 30 * time.Second,
		},
		command.Spec{
			Name: database.CommandPsql, Path: cfg.PsqlPath, Timeout: 30 * time.Second,
			// psql is the only program in the Agent permitted an environment
			// variable from a caller, and only this one: it is how the admin
			// password reaches libpq without passing through argv.
			AllowedEnv: []string{"PGPASSFILE"},
		},
	)

	runner, err := command.NewRunner(specs...)
	if err != nil {
		return nil, nil, fmt.Errorf("build command allowlist: %w", err)
	}

	serviceProvider := services.NewProvider(runner)
	if serviceProvider.Available() {
		log.Info("service management is available", "manager", serviceProvider.Manager())
	} else {
		log.Warn("service management is unavailable: neither systemd nor OpenRC was found",
			"systemctl", cfg.SystemctlPath, "rc_service", cfg.RCServicePath)
	}

	nginxProvider := nginx.NewProvider(nginx.Options{
		Runner:   runner,
		SitesDir: cfg.NginxSitesDir,
	})

	// A host without nginx cannot serve websites. That is advertised through
	// agent.info rather than discovered one failed request at a time.
	provisioner, err := sites.NewProvisioner(cfg.SiteRoot, cfg.WebGroup)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare site root: %w", err)
	}

	apacheProvider := apache.NewProvider(apache.Options{
		Runner:     runner,
		SitesDir:   cfg.ApacheConfigDir,
		MainConfig: cfg.ApacheMainConf,
		// Apache reads site content through the same group nginx does. Without
		// it every request in hybrid mode would be a 403 on a site that looks
		// perfectly configured.
		WebGroup: provisioner.WebGroup(),
	})

	firewallProvider := firewall.NewProvider(firewall.Options{
		Runner:       runner,
		StateDir:     cfg.FirewallStateDir,
		GuardedPorts: firewall.GuardedPortsFromEnv(cfg.FirewallGuardedPorts),
		Log:          log,
	})

	// A firewall change is undone unless it is confirmed, and the timer that
	// undoes it lives in memory. An Agent that restarted mid-window would
	// otherwise leave the change standing forever — which is the one failure
	// the whole protocol exists to prevent, arriving by the back door.
	if err := firewallProvider.RecoverPending(context.Background()); err != nil {
		log.Error("failed to recover a pending firewall change",
			logger.KeyError, err.Error())
	}

	siteManager := sites.NewManager(sites.ManagerOptions{
		Filesystem: provisioner,
		Users:      sites.NewUserProvider(runner),
		Nginx:      nginxProvider,
		Apache:     apacheProvider,
		Log:        log,
	})

	capabilities := siteManager.Capabilities()
	if !capabilities.Nginx {
		log.Warn("website management is unavailable: nginx not found", "path", cfg.NginxPath)
	}
	if !capabilities.Users {
		log.Warn("website management is unavailable: no user management tool found")
	}
	if !capabilities.Apache {
		// Not a warning about something broken: most hosts run nginx alone,
		// and this is what the panel reports when hybrid mode is asked for.
		log.Info("the hybrid web server arrangement is unavailable: apache not found",
			"httpd", cfg.ApachePath, "apache2", cfg.Apache2Path)
	}
	if !provisioner.HasWebGroup() {
		// A site provisioned without this is created successfully and then
		// refuses every visitor, which is far harder to diagnose than a
		// refusal at creation time.
		log.Warn("website provisioning will fail: no web server group found",
			"detail", "set AGENT_WEB_GROUP to the group the web server runs as")
	} else {
		log.Info("website provisioning ready", "web_group", provisioner.WebGroup())
	}

	auditSiteOwnership(provisioner, log)

	phpDetector := php.NewDetector(php.DetectorOptions{Runner: runner})

	// Everything running now should be running after a reboot. Only Node.js
	// applications ever enabled themselves, so every daemon the panel installs
	// on demand — Apache for the hybrid arrangement, BIND, the mail server,
	// fail2ban, the FTP server, each PHP-FPM version — was started and left to
	// disappear at the next restart. Doing this at startup means a host that is
	// already wrong is put right by restarting the Agent, rather than staying
	// wrong until somebody reboots and finds out.
	persistServiceBoot(serviceProvider, collector, phpDetector, log)
	phpPools := php.NewProvider(php.ProviderOptions{Detector: phpDetector})
	phpInstaller := php.NewInstaller(runner)

	if !phpDetector.Available() {
		log.Warn("no PHP version was found: websites can only serve static content",
			"detail", "install a php-fpm package, or use php.install if a package manager is present")
	} else {
		installed := phpDetector.Detect(context.Background())
		names := make([]string, 0, len(installed))
		for _, version := range installed {
			names = append(names, version.Version)
		}
		log.Info("php versions detected", "versions", names,
			"package_manager", phpInstaller.Manager())
	}

	sslManager := ssl.NewManager(ssl.ManagerOptions{
		Store:   ssl.NewStore(""),
		Certbot: ssl.NewCertbot(runner, ""),
		Log:     log,
	})

	sslCapabilities := sslManager.Capabilities()
	if !sslCapabilities.LetsEncrypt {
		// Not an error: a host with no public DNS cannot use ACME at all, and
		// self-signed certificates still work. The panel hides the option
		// rather than offering one that always fails.
		log.Info("certbot is not installed: only self-signed certificates are available",
			"detail", "install certbot to issue publicly trusted certificates")
	}

	// The file manager is confined to the site root. A panel whose file
	// manager can reach / is a root shell with a friendlier interface, so the
	// root is the one thing here that is not configurable per operation.
	//
	// The logs directory inside each site is excluded from writes: it is
	// written by nginx as it serves, and letting the file manager rewrite a
	// live log is how an audit trail stops being one.
	fileManager := files.NewManager(files.ManagerOptions{
		Roots: []string{cfg.SiteRoot},
	})
	if !fileManager.Available() {
		log.Warn("file management is unavailable: the site root could not be resolved",
			"site_root", cfg.SiteRoot)
	} else {
		log.Info("file management ready", "roots", fileManager.Roots())
	}

	// The log reader is confined to where logs live, and to the files this
	// panel's own catalogue names. Both locks matter: the catalogue is what
	// stops a request naming a path, and the roots are what stop a *symlinked*
	// log — /var/log/nginx/access.log is writable by the account nginx runs as
	// on many hosts — from reading something outside them.
	logProvider := logs.NewProvider(log, cfg.LogRoot, cfg.SiteRoot)
	if !logProvider.Available() {
		log.Warn("log reading is unavailable: the log roots could not be resolved",
			"log_root", cfg.LogRoot, "site_root", cfg.SiteRoot)
	}

	// Intrusion prevention. The provider writes policy — which jails run, how
	// many failures are allowed, for how long — and leaves filters and log
	// formats to the distribution, which knows what its own daemons write.
	fail2banProvider := f2bpkg.NewProvider(f2bpkg.Options{
		Runner: runner,
		Log:    log,
		Dir:    cfg.Fail2BanConfigDir,
	})
	if !fail2banProvider.Available() {
		log.Info("fail2ban is not installed; the panel will offer to install it")
	}

	// The FTP server. Accounts are virtual — they live in a password file only
	// proftpd reads and map to the system account that owns the website — so an
	// FTP credential is never a login to the machine.
	ftpProvider := ftppkg.NewProvider(ftppkg.Options{
		Runner: runner,
		Log:    log,
		Paths: ftppkg.Paths{
			ConfigDir: cfg.FTPConfigDir,
			RunDir:    cfg.FTPRunDir,
			LogDir:    cfg.FTPLogDir,
		},
	})
	if !ftpProvider.Available() {
		log.Info("an FTP server is not installed; the panel will offer to install it")
	}

	// The mail server. Mailboxes are virtual — they live in a passwd-file only
	// Dovecot reads and map to a single unprivileged account that owns every
	// Maildir — so a mail password is never a login to the machine.
	mailProvider := mailpkg.NewProvider(mailpkg.Options{
		Runner: runner,
		Log:    log,
		Paths: mailpkg.Paths{
			PostfixDir: cfg.MailConfigDir,
			DovecotDir: cfg.DovecotConfigDir,
			RspamdDir:  cfg.RspamdConfigDir,
			MailRoot:   cfg.MailRoot,
			StateDir:   cfg.MailStateDir,
		},
		Accounts: operations.MailAccountsFor(sites.NewUserProvider(runner)),
	})
	if !mailProvider.Available() {
		log.Info("a mail server is not installed; the panel will offer to install it")
	}

	// Deployment. Everything a deployment runs — the checkout, the dependency
	// install, the build, the script — runs as the website's own account, and
	// an account that resolves to root is refused rather than repaired.
	deployProvider := deploypkg.NewProvider(deploypkg.Options{
		Runner: runner,
		Log:    log,
		Paths:  deploypkg.Paths{StateDir: cfg.DeployStateDir},
	})
	if !deployProvider.Available() {
		log.Info("git is not installed; websites cannot be deployed from a repository")
	}

	// Tenancy. Two jobs: measuring what a subscription uses, and writing the
	// systemd slice for the limits it was sold.
	//
	// The provisioner is passed in rather than a root path, so a document root
	// arriving from the panel is resolved by the same code that created it —
	// one rule about what is inside the allowed root, in one place.
	tenancyProvider := tenancypkg.NewProvider(tenancypkg.Options{
		Runner:   runner,
		Sites:    provisioner,
		Services: serviceProvider,
		Log:      log,
		UnitDir:  cfg.TenantUnitDir,
	})
	if available, detail := tenancyProvider.IsolationStatus(); !available {
		log.Info("subscription resource limits will be recorded but not enforced",
			"reason", detail)
	}

	// The host's package updates. The provider detects apk or apt-get itself,
	// through the same runner Phase 5 installs packages with.
	updatesProvider := updatespkg.NewProvider(updatespkg.Options{
		Runner:     runner,
		Log:        log,
		WorldPath:  cfg.APKWorldPath,
		RebootFlag: cfg.RebootFlagPath,
	})
	if !updatesProvider.Available() {
		log.Info("no supported package manager was found; updates cannot be reported")
	}

	// The authoritative name server. The panel owns the zone files and one
	// include of zone statements; it does not own named.conf, which it writes
	// only when the host has none and otherwise extends by a single line.
	dnsProvider := dnspkg.NewProvider(dnspkg.Options{
		Runner: runner,
		Log:    log,
		Paths: dnspkg.Paths{
			ConfigDir: cfg.DNSConfigDir,
			StateDir:  cfg.DNSStateDir,
		},
	})
	if !dnsProvider.Available() {
		log.Info("a DNS server is not installed; the panel will offer to install it")
	}

	// The SSH server's configuration. The provider reads it through sshd itself
	// rather than by parsing the file, because a directive that is commented out
	// is still in force at its default — and the defaults differ between
	// versions, so the file is not the answer.
	sshProvider := sshpkg.NewProvider(sshpkg.Options{
		Runner: runner,
		Log:    log,
		Dir:    cfg.SSHConfigDir,
	})
	if !sshProvider.Available() {
		log.Info("no SSH server on this host; its settings will be reported as unavailable")
	}

	// Scheduled jobs. The provider writes crontab entries; the host's cron
	// daemon is what runs them.
	cronProvider := cron.NewProvider(cron.Options{
		Runner:   runner,
		Users:    sites.NewUserProvider(runner),
		Log:      log,
		SpoolDir: cfg.CronSpoolDir,
		LogDir:   cfg.CronLogDir,
	})
	if !cronProvider.Available() {
		log.Warn("scheduling is unavailable: this host has no cron spool directory",
			"spool_dir", cfg.CronSpoolDir)
	}

	// Database providers probe their servers here, at startup, so agent.info
	// can report what this host runs rather than each operation discovering it
	// separately.
	databaseManager := database.NewManager(database.ManagerOptions{
		Providers: []database.Provider{
			database.NewMySQL(context.Background(), database.MySQLOptions{
				Runner:        runner,
				Socket:        cfg.MySQLSocket,
				AdminUser:     cfg.MySQLAdminUser,
				AdminPassword: cfg.MySQLAdminPass,
				Log:           log,
			}),
			database.NewPostgres(context.Background(), database.PostgresOptions{
				Runner:        runner,
				Host:          cfg.PostgresHost,
				Port:          cfg.PostgresPort,
				AdminUser:     cfg.PostgresAdminUser,
				AdminPassword: cfg.PostgresAdminPass,
				Log:           log,
			}),
		},
		Log: log,
	})

	for _, engine := range databaseManager.Engines(context.Background()) {
		if engine.Available {
			log.Info("database engine ready", "engine", engine.Engine, "version", engine.Version)
			continue
		}
		// Not an error: most hosts run one engine, and the panel hides what
		// is not there rather than offering a button that always fails.
		log.Info("database engine unavailable", "engine", engine.Engine, "detail", engine.Detail)
	}

	// phpMyAdmin is not installed here, only made installable. It stays absent
	// until an operator asks for it, because a database console reachable by
	// default is a database console someone else finds first.
	// Grafana draws the panel's metrics. It is served the way phpMyAdmin is —
	// on a hostname an operator names, authenticating its own visitors — and
	// the alert engine is untouched by it.
	grafanaManager := grafana.NewManager(grafana.Options{
		Installer: phpInstaller,
		Log:       log,
	})

	// The panel's own web stack: a private nginx on loopback and a PHP-FPM
	// master of its own, both reading only the panel's directories. It is what
	// stops a customer website taking the database console down with it, and
	// what stops the website system ever seeing the panel's own configuration.
	panelStack := panelweb.NewManager(panelweb.Options{
		Runner:        runner,
		PHP:           phpDetector,
		WebGroup:      provisioner.WebGroup(),
		ListenAddress: cfg.PanelWebListen,
		Log:           log,
	})

	phpMyAdmin := pma.NewManager(pma.Options{
		Installer: phpInstaller,
		Panel:     panelStack,
		PHP:       phpDetector,
		Users:     sites.NewUserProvider(runner),
		WebGroup:  provisioner.WebGroup(),
		Log:       log,
	})

	// Brought back up if it was installed and is not running. The Agent starts
	// at boot, so this is what makes the panel's own applications survive a
	// restart: there is no init unit to enable, because this nginx and this
	// PHP master are the Agent's own.
	reconcilePanelStack(panelStack, phpMyAdmin, log)

	// Reverse-proxied sites need one map defined in the http block. Written
	// here rather than per site, because nginx refuses to start with a
	// duplicate — and because the first proxied site failing validation for a
	// missing variable would look like a problem with that site.
	if nginxProvider.Available() {
		if err := nginxProvider.EnsureProxyMap(); err != nil {
			log.Warn("could not write the reverse-proxy map; Node.js sites will not validate",
				logger.KeyError, err.Error())
		}
	}

	nodeDetector := nodejs.NewDetector(runner)
	nodeInstaller := nodejs.NewInstaller(phpInstaller, nodeDetector)
	nodeUsers := sites.NewUserProvider(runner)

	nodeManager := nodejs.NewManager(nodejs.ManagerOptions{
		Detector:   nodeDetector,
		Installer:  nodeInstaller,
		Systemd:    nodejs.NewSystemd(serviceProvider),
		Supervisor: nodejs.NewSupervisor(runner, nodeUsers, log),
		Runner:     runner,
		Users:      nodeUsers,
		Log:        log,
	})

	if versions := nodeDetector.Detect(context.Background()); len(versions) > 0 {
		names := make([]string, 0, len(versions))
		for _, version := range versions {
			names = append(names, version.Full)
		}
		log.Info("node.js detected", "versions", names, "managed_by", nodeManager.Runtime())
	} else {
		// Not an error: a host serving only PHP and static sites needs no
		// Node, and the panel offers to install one rather than hiding the
		// feature.
		log.Info("no node.js runtime was found",
			"detail", "install one through the panel, or with the host's package manager")
	}

	// The security probes. Read-only, and given the directories to walk rather
	// than accepting them from a request — the site root and the web server's
	// configuration are what this panel put things in, and scanning /usr would
	// produce a long list of things it did not create and cannot fix.
	securityScanner := securitypkg.NewScanner(securitypkg.Options{
		Log:      log,
		ProcRoot: cfg.ProcRoot,
		Roots:    []string{cfg.SiteRoot},
	})

	// Backups. The working directory is the Agent's own staging area, and the
	// local roots are the only directories a local destination may write into
	// — without that bound, "back up to /etc/nginx" would be a way to write a
	// file anywhere on the host as root.
	backupProvider := backuppkg.NewProvider(backuppkg.Options{
		Runner:     runner,
		Databases:  databaseManager,
		Log:        log,
		WorkDir:    cfg.BackupWorkDir,
		SiteRoot:   cfg.SiteRoot,
		LocalRoots: cfg.BackupLocalRoots,
	})
	if capabilities := backupProvider.Capabilities(); !capabilities.Available {
		log.Warn("backups are unavailable on this host", "detail", capabilities.Reason)
	} else {
		log.Info("backups ready",
			"work_dir", capabilities.WorkDir,
			"sftp", capabilities.SFTP,
			"engines", capabilities.Engines)
	}

	jobRunner := jobs.NewRunner(jobs.Options{
		MaxConcurrent: cfg.MaxConcurrentJobs,
		MaxJobs:       cfg.MaxJobs,
		Timeout:       cfg.JobTimeout,
		Retention:     cfg.JobRetention,
		Log:           log,
	})

	registry := operations.NewRegistry(operations.Dependencies{
		Collector:    collector,
		Services:     serviceProvider,
		Sites:        siteManager,
		Nginx:        nginxProvider,
		Apache:       apacheProvider,
		Firewall:     firewallProvider,
		Jobs:         jobRunner,
		Log:          log,
		PHP:          phpDetector,
		PHPPools:     phpPools,
		PHPInstaller: phpInstaller,
		WebGroup:     provisioner.WebGroup(),
		SSL:          sslManager,
		Databases:    databaseManager,
		PHPMyAdmin:   phpMyAdmin,
		Grafana:      grafanaManager,
		Node:         nodeManager,
		Files:        fileManager,
		Logs:         logProvider,
		Cron:         cronProvider,
		SSH:          sshProvider,
		Fail2Ban:     fail2banProvider,
		FTP:          ftpProvider,
		Mail:         mailProvider,
		Deploy:       deployProvider,
		DNS:          dnsProvider,
		Updates:      updatesProvider,
		Backup:       backupProvider,
		Security:     securityScanner,
		Tenancy:      tenancyProvider,
	})

	// Wired after the registry, because restarting the FTP daemon goes through
	// the service manager the registry owns. See ftp.Provider.SetReloader.
	ftpProvider.SetReloader(operations.FTPReloaderFor(registry))
	dnsProvider.SetService(operations.DNSServiceFor(registry))
	mailProvider.SetServices(operations.MailServicesFor(registry))
	grafanaManager.SetServices(operations.GrafanaServicesFor(registry))

	return registry, jobRunner, nil
}

// ping issues a single agent.ping over the socket and reports the outcome.
func ping(cfg config.Config) error {
	conn, err := net.DialTimeout("unix", cfg.SocketPath, 5*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}

	req, err := json.Marshal(protocol.Request{
		Operation: protocol.OperationPing,
		RequestID: "req_healthcheck",
		// The health check authenticates like any other caller. It connects as
		// root, so the peer check passes, but the token must still be right.
		Token: cfg.Token,
	})
	if err != nil {
		return err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil && n == 0 {
		return err
	}

	var resp protocol.Response
	if err := json.Unmarshal(trimNewline(buf[:n]), &resp); err != nil {
		return fmt.Errorf("invalid agent response: %w", err)
	}
	if resp.Status != protocol.StatusSuccess {
		return fmt.Errorf("agent reported status %s", resp.Status)
	}
	return nil
}

func trimNewline(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}

// auditSiteOwnership reports abandoned site directories at startup.
//
// It reports and does not act. A directory owned by an account that no longer
// exists is a uid waiting to be handed to the next site created, at which
// point somebody else's files quietly become that site's — and on a host
// upgraded from a build that left them behind, that has already been true for
// a while. Saying so at every start is how an operator finds out; the panel
// cannot fix it for them, for the reason AuditOwnership sets out.
func auditSiteOwnership(provisioner *sites.Provisioner, log *slog.Logger) {
	findings, err := provisioner.AuditOwnership()
	if err != nil {
		log.Warn("site directory ownership could not be audited",
			logger.KeyError, err.Error())
		return
	}

	var orphaned, misowned int
	for _, finding := range findings {
		if finding.Orphaned {
			orphaned++
		} else {
			misowned++
		}
	}
	if orphaned == 0 && misowned == 0 {
		return
	}

	if orphaned > 0 {
		log.Warn("site directories are owned by accounts that no longer exist",
			"count", orphaned,
			"detail", "their uids will be reused by the next sites created; "+
				"run the agent with -repair-site-ownership to reassign them to root")
	}
	if misowned > 0 {
		// Reported separately and never acted on: this is also what a
		// subdomain sharing its parent's account looks like.
		log.Warn("site directories are owned by another site's account",
			"count", misowned,
			"detail", "expected for a subdomain that shares its parent's account, "+
				"and a recycled uid otherwise; run -repair-site-ownership to list them")
	}
}

// repairSiteOwnership is the -repair-site-ownership mode. It returns an exit
// code.
func repairSiteOwnership(cfg config.Config) int {
	log := logger.New(logger.Options{
		Service: "agent",
		Level:   cfg.LogLevel,
		Output:  os.Stdout,
	})

	provisioner, err := sites.NewProvisioner(cfg.SiteRoot, cfg.WebGroup)
	if err != nil {
		log.Error("the site root could not be opened", logger.KeyError, err.Error())
		return 1
	}

	findings, err := provisioner.AuditOwnership()
	if err != nil {
		log.Error("site directory ownership could not be audited", logger.KeyError, err.Error())
		return 1
	}

	for _, finding := range findings {
		if finding.Orphaned {
			continue
		}
		// Listed, not touched. The operator has to decide, because the Agent
		// cannot tell a recycled uid from a subdomain sharing an account.
		log.Warn("site directory is owned by another site's account",
			"path", finding.Path, "owner", finding.Owner, "owner_home", finding.OwnerHome,
			"detail", "expected if this is a subdomain of the owner's site; "+
				"otherwise the uid was recycled and this needs reassigning by hand")
	}

	repaired, err := provisioner.RepairOrphans()
	for _, path := range repaired {
		log.Info("reassigned an abandoned site directory to root", "path", path)
	}
	if err != nil {
		log.Error("the sweep stopped early", "reassigned", len(repaired),
			logger.KeyError, err.Error())
		return 1
	}

	log.Info("site directory ownership repaired",
		"reassigned", len(repaired), "needing_review", len(findings)-len(repaired))
	return 0
}

// reconcilePanelStack brings the panel's own web stack back up after a restart.
//
// The panel's nginx and PHP master are started by the Agent rather than by the
// init system, so nothing else will do this. Before it existed, a restart left
// phpMyAdmin installed, published, and answering 502 to every request — with
// the package and the vhost both exactly where they should be, which is the
// hardest kind of failure to look at and understand.
//
// Only if phpMyAdmin is installed. A host that never asked for it should not
// get a second nginx running on it.
func reconcilePanelStack(panel *panelweb.Manager, console *pma.Manager, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	status := console.Status(ctx)
	if !status.Installed {
		return
	}

	if err := panel.EnsureLayout(); err != nil {
		log.Warn("the panel's web stack could not be prepared",
			logger.KeyError, err.Error())
		return
	}

	if status.PHPVersion != "" && !panel.FPMRunning() {
		if err := panel.StartFPM(ctx, status.PHPVersion); err != nil {
			log.Warn("the panel's PHP master did not start: phpMyAdmin will answer 502",
				"version", status.PHPVersion, logger.KeyError, err.Error())
		} else {
			log.Info("the panel's PHP master is running", "version", status.PHPVersion)
		}
	}

	if !panel.NginxRunning() {
		if err := panel.StartNginx(ctx); err != nil {
			log.Warn("the panel's nginx did not start: its own applications are unreachable",
				logger.KeyError, err.Error())
		} else {
			log.Info("the panel's nginx is running", "port", panel.Port())
		}
	}
}

// persistServiceBoot makes sure what is running now comes back after a reboot.
//
// Reported at every start, whether or not anything needed changing: an
// operator who has just restarted the Agent to fix this should see that it
// found nothing to fix, rather than silence they cannot tell apart from the
// sweep never having run.
func persistServiceBoot(provider *services.Provider, collector *collectors.Collector,
	detector *php.Detector, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// PHP-FPM units are per-version and are not in the static catalogue, so
	// they have to be described before they can be swept — which is the whole
	// point: a host running three PHP versions has three units to persist.
	var extra []services.Definition
	if detector != nil && detector.Available() {
		versions := detector.Detect(ctx)
		names := make([]string, 0, len(versions))
		for _, version := range versions {
			names = append(names, version.Version)
		}
		extra = services.PHPFPMDefinitions(names)
	}

	report := provider.EnsureBootPersistence(ctx, extra, collector)

	if len(report.Enabled) > 0 {
		log.Info("services were not set to start at boot and now are",
			"services", report.Enabled)
	}
	for key, reason := range report.Failed {
		log.Warn("a running service could not be made to start at boot",
			"service", key, logger.KeyError, reason)
	}
	if report.Unmanaged > 0 {
		// One line for the host, not one per service: on a machine with no
		// init system every running daemon is in this state, and it is a fact
		// about the machine rather than about any of them.
		log.Warn("services are running that nothing will start at boot",
			"count", report.Unmanaged,
			"detail", "this host has no init system, so a reboot leaves it serving nothing")
	}
	if len(report.Enabled) == 0 && len(report.Failed) == 0 && report.Unmanaged == 0 {
		log.Info("every running service is set to start at boot",
			"checked", len(report.Findings))
	}
}
