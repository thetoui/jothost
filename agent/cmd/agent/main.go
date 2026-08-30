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
	"github.com/jothost/panel/agent/internal/collectors"
	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/agent/internal/config"
	"github.com/jothost/panel/agent/internal/database"
	"github.com/jothost/panel/agent/internal/files"
	"github.com/jothost/panel/agent/internal/firewall"
	"github.com/jothost/panel/agent/internal/jobs"
	"github.com/jothost/panel/agent/internal/nginx"
	"github.com/jothost/panel/agent/internal/nodejs"
	"github.com/jothost/panel/agent/internal/operations"
	"github.com/jothost/panel/agent/internal/php"
	"github.com/jothost/panel/agent/internal/pma"
	"github.com/jothost/panel/agent/internal/services"
	"github.com/jothost/panel/agent/internal/sites"
	"github.com/jothost/panel/agent/internal/socket"
	"github.com/jothost/panel/agent/internal/ssl"
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
	flag.Parse()

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
		{Name: sites.CommandUseradd, Path: cfg.UseraddPath, Timeout: 15 * time.Second},
		{Name: sites.CommandAdduser, Path: cfg.AdduserPath, Timeout: 15 * time.Second},
		{Name: sites.CommandUserdel, Path: cfg.UserdelPath, Timeout: 15 * time.Second},
		{Name: sites.CommandDeluser, Path: cfg.DeluserPath, Timeout: 15 * time.Second},
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
	if !serviceProvider.Available() {
		log.Warn("service management is unavailable: systemctl not found",
			"path", cfg.SystemctlPath)
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

	phpDetector := php.NewDetector(php.DetectorOptions{Runner: runner})
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
	phpMyAdmin := pma.NewManager(pma.Options{
		Installer: phpInstaller,
		FPM:       phpInstaller,
		PHP:       phpDetector,
		Pools:     phpPools,
		Nginx:     nginxProvider,
		Users:     sites.NewUserProvider(runner),
		WebGroup:  provisioner.WebGroup(),
		Log:       log,
	})

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
		Node:         nodeManager,
		Files:        fileManager,
	})

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
