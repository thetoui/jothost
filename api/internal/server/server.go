// Package server wires configuration, middleware, and routes into an HTTP
// server with graceful shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/auditlog"
	"github.com/jothost/panel/api/internal/auth"
	backuppkg "github.com/jothost/panel/api/internal/backup"
	"github.com/jothost/panel/api/internal/config"
	cronpkg "github.com/jothost/panel/api/internal/cron"
	"github.com/jothost/panel/api/internal/dashboard"
	databasespkg "github.com/jothost/panel/api/internal/databases"
	deploypkg "github.com/jothost/panel/api/internal/deploy"
	dnspkg "github.com/jothost/panel/api/internal/dns"
	f2bpkg "github.com/jothost/panel/api/internal/fail2ban"
	filespkg "github.com/jothost/panel/api/internal/files"
	firewallpkg "github.com/jothost/panel/api/internal/firewall"
	ftppkg "github.com/jothost/panel/api/internal/ftp"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/jobs"
	logspkg "github.com/jothost/panel/api/internal/logs"
	mailpkg "github.com/jothost/panel/api/internal/mail"
	"github.com/jothost/panel/api/internal/metrics"
	"github.com/jothost/panel/api/internal/middleware"
	monitoringpkg "github.com/jothost/panel/api/internal/monitoring"
	nodepkg "github.com/jothost/panel/api/internal/node"
	notificationspkg "github.com/jothost/panel/api/internal/notifications"
	phppkg "github.com/jothost/panel/api/internal/php"
	"github.com/jothost/panel/api/internal/ratelimit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	securitypkg "github.com/jothost/panel/api/internal/security"
	"github.com/jothost/panel/api/internal/servers"
	servicespkg "github.com/jothost/panel/api/internal/services"
	"github.com/jothost/panel/api/internal/sessions"
	sshpkg "github.com/jothost/panel/api/internal/ssh"
	sslpkg "github.com/jothost/panel/api/internal/ssl"
	tenancypkg "github.com/jothost/panel/api/internal/tenancy"
	"github.com/jothost/panel/api/internal/twofactor"
	updatespkg "github.com/jothost/panel/api/internal/updates"
	"github.com/jothost/panel/api/internal/users"
	webserverpkg "github.com/jothost/panel/api/internal/webserver"
	"github.com/jothost/panel/api/internal/websites"
	"github.com/jothost/panel/shared/validate"
	"github.com/jothost/panel/shared/version"
)

// Server owns the API HTTP listener and its dependencies.
type Server struct {
	cfg   config.Config
	log   *slog.Logger
	agent *agentclient.Client
	pool  *pgxpool.Pool
	redis *redis.Client
	auth  *auth.Service
	http  *http.Server

	servers   *servers.Repository
	metrics   *metrics.Repository
	dashboard *dashboard.Handler
	sampler   *metrics.Sampler

	tenancy        *tenancypkg.Handler
	tenancyService *tenancypkg.Service
	quotaGuard     *tenancypkg.Guard

	websites      *websites.Handler
	webserver     *webserverpkg.Handler
	services      *servicespkg.Handler
	logs          *logspkg.Handler
	cron          *cronpkg.Handler
	ssh           *sshpkg.Handler
	fail2ban      *f2bpkg.Handler
	ftp           *ftppkg.Handler
	mail          *mailpkg.Handler
	deploy        *deploypkg.Handler
	deployService *deploypkg.Service
	dns           *dnspkg.Handler
	updates       *updatespkg.Handler
	// auditReader serves the trail every other feature writes to. It has no
	// service layer: reading an append-only table has no business rules.
	auditReader   *auditlog.Handler
	backup        *backuppkg.Handler
	security      *securitypkg.Handler
	notifications *notificationspkg.Handler
	// dispatcher delivers what the other phases raised. It is a loop of its
	// own because the alternative is the monitor waiting on somebody's SMTP
	// server while alerts queue behind it.
	dispatcher *notificationspkg.Dispatcher
	// backupScheduler takes the backups that are due. Like the update
	// scheduler, it is the panel's own loop rather than a crontab entry:
	// reading every file of every site needs root, which only the Agent has.
	backupScheduler *backuppkg.Scheduler
	monitoring      *monitoringpkg.Handler
	// monitor takes readings on a cadence and drives the alert engine. It is
	// a loop of its own beside the sampler: the sampler records what the host
	// said, and this decides what it means.
	monitor *monitoringpkg.Monitor
	// updateScheduler checks for updates on a cadence and applies them in
	// their window. It is the panel's own loop rather than a cron entry: the
	// privileged half belongs to the Agent, and a schedule in a crontab is one
	// the panel can no longer describe.
	updateScheduler *updatespkg.Scheduler
	firewall        *firewallpkg.Handler
	jobs            *jobs.Handler
	php             *phppkg.Handler
	ssl             *sslpkg.Handler
	files           *filespkg.Handler
	databases       *databasespkg.Handler
	node            *nodepkg.Handler
	// worker realises queued jobs against the Agent. It is nil when no server
	// is registered, because there is no host to provision against.
	worker *jobs.Worker
	// phpSync keeps the version table matching what the host actually has.
	phpSync *phppkg.Syncer
	// renewer keeps certificates from expiring. Nil when no server is
	// registered, like the worker.
	renewer *sslpkg.Renewer
	sslRepo *sslpkg.Repository

	// Router is exported so tests can exercise the full middleware chain
	// without binding a port.
	Router http.Handler
}

// Options carries the already-connected infrastructure a Server needs.
type Options struct {
	Config config.Config
	Log    *slog.Logger
	Pool   *pgxpool.Pool
	Redis  *redis.Client
	// LocalServerID is the registered host this API manages. It is empty when
	// registration failed, which leaves the dashboard reporting no server
	// rather than serving a snapshot of nothing.
	LocalServerID string
}

// New builds a Server from validated configuration and live dependencies.
func New(opts Options) (*Server, error) {
	cfg, log := opts.Config, opts.Log

	encrypter, err := secrets.NewEncrypter(cfg.Auth.EncryptionKey)
	if err != nil {
		return nil, err
	}

	userRepo := users.NewRepository(opts.Pool)
	sessionRepo := sessions.NewRepository(opts.Pool)
	rbacRepo := rbac.NewRepository(opts.Pool)
	twoFactorRepo := twofactor.NewRepository(opts.Pool, encrypter)
	auditRecorder := audit.NewRecorder(opts.Pool, log)

	authService := auth.NewService(auth.Config{
		AccessTokenTTL:  cfg.Auth.AccessTokenTTL,
		RefreshTokenTTL: cfg.Auth.RefreshTokenTTL,
		MFAChallengeTTL: cfg.Auth.MFAChallengeTTL,
		TOTPIssuer:      cfg.Auth.TOTPIssuer,
	}, auth.Dependencies{
		Users:     userRepo,
		Sessions:  sessionRepo,
		RBAC:      rbacRepo,
		TwoFactor: twoFactorRepo,
		Tokens:    auth.NewTokenStore(opts.Redis, cfg.Auth.AccessTokenTTL),
		Audit:     auditRecorder,
		UserLimiter: ratelimit.New(opts.Redis, "jothost:rl:login:user:",
			cfg.Auth.LoginRateLimit, cfg.Auth.LoginRateWindow),
		IPLimiter: ratelimit.New(opts.Redis, "jothost:rl:login:ip:",
			cfg.Auth.LoginIPRateLimit, cfg.Auth.LoginRateWindow),
		Log: log,
	})

	agent := agentclient.New(agentclient.Options{
		SocketPath: cfg.AgentSocket,
		Timeout:    cfg.AgentTimeout,
		Token:      cfg.AgentToken,
	})

	serverRepo := servers.NewRepository(opts.Pool)
	metricRepo := metrics.NewRepository(opts.Pool)

	s := &Server{
		cfg:     cfg,
		log:     log,
		agent:   agent,
		pool:    opts.Pool,
		redis:   opts.Redis,
		auth:    authService,
		servers: serverRepo,
		metrics: metricRepo,
	}

	// Monitoring is built before the dashboard because the dashboard's
	// thresholds come from the alert rules this service owns — one definition
	// rather than two that can disagree.
	monitorRepo := monitoringpkg.NewRepository(opts.Pool)
	monitorService := monitoringpkg.NewService(monitoringpkg.ServiceOptions{
		Repo:     monitorRepo,
		Audit:    auditRecorder,
		Log:      log,
		ServerID: opts.LocalServerID,

		// Grafana draws the charts this package's rules are evaluated from.
		// The pool is here to create its read-only role and nothing else; the
		// database URL is read for the host and database only, never for the
		// panel's own credentials.
		Agent:       agent,
		Pool:        opts.Pool,
		DatabaseURL: cfg.DatabaseURL,
		PanelURL:    cfg.PanelURL,
	})
	s.monitoring = monitoringpkg.NewHandler(monitoringpkg.HandlerOptions{
		Service: monitorService,
		Auth:    authService,
	})

	dashboardService := dashboard.NewService(dashboard.Options{
		Agent:   agent,
		Servers: serverRepo,
		Log:     log,
		Thresholds: dashboard.Thresholds{
			DiskWarning:    cfg.Dashboard.DiskWarnPercent,
			DiskCritical:   cfg.Dashboard.DiskCritPercent,
			MemoryWarning:  cfg.Dashboard.MemoryWarnPercent,
			MemoryCritical: cfg.Dashboard.MemoryCritPercent,
			LoadWarning:    cfg.Dashboard.LoadWarnPerCore,
			LoadCritical:   cfg.Dashboard.LoadCritPerCore,
		},
		// The numbers above are the fallback; the live ones come from the alert
		// rules, so raising a threshold raises it on both pages at once.
		ThresholdSource: dashboardThresholds(monitorService),
		// Postgres and Redis are checked by the API itself: it holds the
		// pools, so its own probe is a better answer than asking the Agent
		// whether a unit happens to be running.
		Dependencies:    []string{"postgres", "redis"},
		CheckDependency: s.checkDependencyHealth,
		// Injected as a function so the dashboard package does not depend on
		// ssl, which depends on websites — which would close a cycle.
		ExpiringCertificates: s.expiringCertificates,
	})

	s.dashboard = dashboard.NewHandler(dashboard.HandlerOptions{
		Service:         dashboardService,
		Servers:         serverRepo,
		Metrics:         metricRepo,
		Auth:            authService,
		DefaultServerID: opts.LocalServerID,
	})

	jobRepo := jobs.NewRepository(opts.Pool)
	websiteRepo := websites.NewRepository(opts.Pool)

	// DNS is built before websites because a subdomain publishes itself in its
	// parent's zone as it is created — the item Phase 4.1 deferred until there
	// was a zone to put a record in.
	//
	// The zones are the panel's record; the zone files named reads are what
	// answer queries, and the Agent makes the second match the first on every
	// change.
	dnsService := dnspkg.NewService(dnspkg.ServiceOptions{
		Repo:     dnspkg.NewRepository(opts.Pool, encrypter),
		Websites: dnsWebsites{repo: websiteRepo},
		Agent:    agent,
		Audit:    auditRecorder,
		Remote:   dnspkg.DefaultRemoteFactory,
		Host:     dnsHost{repo: serverRepo, serverID: opts.LocalServerID},
		Log:      log,
		ServerID: opts.LocalServerID,
	})

	websiteService := websites.NewService(websites.ServiceOptions{
		Repository: websiteRepo,
		Jobs:       jobRepo,
		Audit:      auditRecorder,
		DNS:        dnsService,
		Log:        log,
		ServerID:   opts.LocalServerID,
	})

	s.websites = websites.NewHandler(websites.HandlerOptions{
		Service: websiteService,
		Repo:    websiteRepo,
		Auth:    authService,
	})
	s.jobs = jobs.NewHandler(jobs.HandlerOptions{
		Repository: jobRepo,
		Auth:       authService,
	})

	phpRepo := phppkg.NewRepository(opts.Pool)
	phpService := phppkg.NewService(phppkg.ServiceOptions{
		Repository: phpRepo,
		Websites:   websiteRepo,
		Jobs:       jobRepo,
		Audit:      auditRecorder,
		Log:        log,
	})
	s.php = phppkg.NewHandler(phppkg.HandlerOptions{
		Service: phpService,
		Repo:    phpRepo,
		Auth:    authService,
	})
	s.phpSync = phppkg.NewSyncer(phpRepo, agent, log)

	sslRepo := sslpkg.NewRepository(opts.Pool)
	s.sslRepo = sslRepo
	sslService := sslpkg.NewService(sslpkg.ServiceOptions{
		Repository: sslRepo,
		Websites:   websiteRepo,
		PHP:        phpRepo,
		Jobs:       jobRepo,
		Audit:      auditRecorder,
		// Issuance points the certificate's names at this host in the zones
		// the panel serves first: a name that resolves nowhere fails the
		// HTTP-01 challenge, and a failed challenge is spent.
		DNS: dnsService,
		Log: log,
	})
	s.ssl = sslpkg.NewHandler(sslpkg.HandlerOptions{
		Service: sslService,
		Repo:    sslRepo,
		Agent:   agent,
		Auth:    authService,
	})
	s.renewer = sslpkg.NewRenewer(sslRepo, websiteRepo, sslService, log)

	// The file manager holds no state of its own: every operation is a request
	// to the Agent, which owns the disk (CLAUDE.md section 7).
	s.files = filespkg.NewHandler(filespkg.HandlerOptions{
		Service: filespkg.NewService(filespkg.ServiceOptions{
			Agent: agent,
			Audit: auditRecorder,
		}),
		Agent: agent,
		Auth:  authService,
	})

	// Databases are managed synchronously rather than through the job queue:
	// the work is a single DDL statement, and the response has to carry the
	// generated password back to the caller that asked for the account.
	databaseRepo := databasespkg.NewRepository(opts.Pool, encrypter)
	s.databases = databasespkg.NewHandler(databasespkg.HandlerOptions{
		Service: databasespkg.NewService(databasespkg.ServiceOptions{
			Repository: databaseRepo,
			Websites:   websiteRepo,
			Agent:      agent,
			Jobs:       jobRepo,
			Audit:      auditRecorder,
			Log:        log,
			ServerID:   opts.LocalServerID,
		}),
		Repo: databaseRepo,
		Auth: authService,
	})

	// Node.js applications are managed synchronously, like databases: starting
	// a process is fast, and the response should say what happened rather than
	// that the intent was recorded.
	nodeRepo := nodepkg.NewRepository(opts.Pool, encrypter)
	s.node = nodepkg.NewHandler(nodepkg.HandlerOptions{
		Service: nodepkg.NewService(nodepkg.ServiceOptions{
			Repository: nodeRepo,
			Websites:   websiteRepo,
			Agent:      agent,
			Audit:      auditRecorder,
			Log:        log,
			ServerID:   opts.LocalServerID,
		}),
		Repo: nodeRepo,
		Auth: authService,
	})

	// The websites service can now resolve a site's complete vhost: which FPM
	// socket it serves PHP through, which certificate it holds, which
	// application it proxies to. Without this every vhost rewrite would send
	// only the names, turning PHP, HTTPS and the reverse proxy off as a side
	// effect of adding an alias (see websites/serving.go).
	websiteRepo.SetServingSources(servingSources(phpRepo, sslRepo, nodeRepo))

	// The daemons on the host. No state of the panel's own: a service's state
	// is on the host, and the Agent is the only thing that may read or change
	// it.
	s.services = servicespkg.NewHandler(servicespkg.HandlerOptions{
		Service: servicespkg.NewService(servicespkg.ServiceOptions{
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// Scheduled jobs. The rows here are intent; the crontab files on the host
	// are what runs, and after every change the Agent is handed the complete
	// set for the affected account so the two cannot drift apart.
	s.cron = cronpkg.NewHandler(cronpkg.HandlerOptions{
		Service: cronpkg.NewService(cronpkg.ServiceOptions{
			Repo:     cronpkg.NewRepository(opts.Pool),
			Websites: cronWebsites{repo: websiteRepo},
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// Intrusion prevention. No state of the panel's own either: the jails are
	// files on the host and the bans are firewall rules, and both belong to the
	// daemon that maintains them.
	s.fail2ban = f2bpkg.NewHandler(f2bpkg.HandlerOptions{
		Service: f2bpkg.NewService(f2bpkg.ServiceOptions{
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// FTP. The accounts are the panel's record; proftpd's password file is what
	// authenticates, and the Agent makes the second match the first on every
	// change.
	s.ftp = ftppkg.NewHandler(ftppkg.HandlerOptions{
		Service: ftppkg.NewService(ftppkg.ServiceOptions{
			Repo:     ftppkg.NewRepository(opts.Pool),
			Websites: ftpWebsites{repo: websiteRepo, ssl: sslRepo},
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	s.dns = dnspkg.NewHandler(dnspkg.HandlerOptions{Service: dnsService, Auth: authService})

	// Mail. The mailboxes are the panel's record; Dovecot's passwd-file and
	// Postfix's lookup tables are what actually accept and deliver, and the
	// Agent makes the second match the first on every change.
	//
	// The DNS service is handed in because a mail domain's SPF, DKIM and DMARC
	// records are DNS records: this phase decides *what* to publish and Phase
	// 13 decides how to spell it. It is also how the mail page can say whether
	// the world can actually see what the panel has configured, which is the
	// one question a mail server cannot answer about itself.
	// Deployment. The panel records where a website's source comes from; the
	// Agent does the checking out and the building, as the website's own
	// account. Everything long-running goes through the job queue, so the
	// panel follows a deployment rather than holding a request open for the
	// length of a build.
	deployService := deploypkg.NewService(deploypkg.ServiceOptions{
		Repo:     deploypkg.NewRepository(opts.Pool),
		Websites: deployWebsites{repo: websiteRepo},
		Agent:    agent,
		Jobs:     jobRepo,
		Secrets:  encrypter,
		Audit:    auditRecorder,
		Log:      log,
		ServerID: opts.LocalServerID,
	})
	s.deployService = deployService
	s.deploy = deploypkg.NewHandler(deploypkg.HandlerOptions{
		Service: deployService,
		Auth:    authService,
	})
	deploypkg.SetPanelURL(cfg.PanelURL)

	// Tenancy. The hierarchy of accounts, the plans sold to them, and the
	// enforcement of what each subscription may use.
	tenancyRepo := tenancypkg.NewRepository(opts.Pool)
	tenancyService := tenancypkg.NewService(tenancypkg.Dependencies{
		Repo:     tenancyRepo,
		Users:    userRepo,
		RBAC:     rbacRepo,
		Sessions: sessionRepo,
		Agent:    agent,
		Audit:    auditRecorder,
		Log:      log,
	})
	s.tenancyService = tenancyService
	s.tenancy = tenancypkg.NewHandler(tenancypkg.HandlerOptions{
		Service: tenancyService,
		Auth:    authService,
	})

	// The quota-guarded routes, in one table.
	//
	// This is the whole enforcement surface for counted limits, written where
	// somebody can read it rather than spread across six services. Every entry
	// is a route that creates one more of something a plan sells; a feature
	// added later that sells a seventh thing has to be added here, and the
	// list being short and visible is what makes that likely to happen.
	//
	// Deliberately absent are disk and bandwidth. Those are measured on the
	// host after the fact, so there is no request that could be refused to
	// keep one inside its limit — and NewGuard refuses to be wired with one,
	// rather than accepting a promise nothing would keep.
	quotaGuard, err := tenancypkg.NewGuard(tenancyService, authService,
		map[string]tenancypkg.Rule{
			// Creating a website charges the subscription named in the body, or
			// the caller's own. This is the one route where a request can say
			// which subscription it is spending, because the website does not
			// exist yet and so cannot say for itself.
			"POST /api/v1/websites": {
				Dimension: validate.DimensionWebsites,
				Subject:   tenancypkg.SubjectBodySubscription,
			},
			// Everything below charges whoever owns the resource being added
			// to, not whoever is asking. A reseller creating a database inside
			// a customer's site is spending the customer's plan.
			"POST /api/v1/websites/{id}/subdomains": {
				Dimension: validate.DimensionSubdomains,
				Subject:   tenancypkg.SubjectPathWebsite,
			},
			"POST /api/v1/databases": {
				Dimension: validate.DimensionDatabases,
				Subject:   tenancypkg.SubjectBodyWebsite,
			},
			"POST /api/v1/mail/domains/{id}/mailboxes": {
				Dimension: validate.DimensionMailboxes,
				Subject:   tenancypkg.SubjectPathMailDomain,
			},
			"POST /api/v1/ftp/users": {
				Dimension: validate.DimensionFTPUsers,
				Subject:   tenancypkg.SubjectBodyWebsite,
			},
			"POST /api/v1/cron": {
				Dimension: validate.DimensionCronJobs,
				Subject:   tenancypkg.SubjectBodyWebsite,
			},
		})
	if err != nil {
		return nil, err
	}
	s.quotaGuard = quotaGuard
	websiteService.SetSubscriptions(tenancyService)

	s.mail = mailpkg.NewHandler(mailpkg.HandlerOptions{
		Service: mailpkg.NewService(mailpkg.ServiceOptions{
			Repo:     mailpkg.NewRepository(opts.Pool),
			Websites: mailWebsites{repo: websiteRepo, ssl: sslRepo},
			Zones:    mailZones{dns: dnsService},
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// System updates. The host's package manager is the authority on what is
	// outstanding; the panel caches what it last said, because a check
	// refreshes the package index and reaches the network.
	updateRepo := updatespkg.NewRepository(opts.Pool)
	updateService := updatespkg.NewService(updatespkg.ServiceOptions{
		Repo:     updateRepo,
		Agent:    agent,
		Audit:    auditRecorder,
		Runtimes: updateRuntimes{php: phpRepo, node: nodeRepo},
		Log:      log,
		ServerID: opts.LocalServerID,
	})
	s.updates = updatespkg.NewHandler(updatespkg.HandlerOptions{
		Service: updateService,
		Auth:    authService,
	})

	// The audit trail, readable at last. Every phase since the first has been
	// writing to it and nothing could read it back through the panel.
	s.auditReader = auditlog.NewHandler(auditlog.HandlerOptions{
		Reader: audit.NewReader(opts.Pool),
		Auth:   authService,
	})
	if opts.LocalServerID != "" {
		s.updateScheduler = updatespkg.NewScheduler(updatespkg.SchedulerOptions{
			Service:  updateService,
			Repo:     updateRepo,
			Log:      log,
			ServerID: opts.LocalServerID,
		})
	}

	// The SSH server's settings. No state of the panel's own: the configuration
	// is files on the host, and every change is validated by sshd before it is
	// installed and refused outright where it would leave nobody able to log in.
	s.ssh = sshpkg.NewHandler(sshpkg.HandlerOptions{
		Service: sshpkg.NewService(sshpkg.ServiceOptions{
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// The host's own logs. Read-only, and read through the Agent: a request
	// names a source key from the Agent's catalogue, never a path, which is
	// what keeps this from being a file reader with a friendlier name.
	s.logs = logspkg.NewHandler(logspkg.HandlerOptions{
		Service: logspkg.NewService(logspkg.ServiceOptions{
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
		Log:  log,
		// So a website's own logs can be served on the website's own routes,
		// under website.view rather than server.view.
		Websites: websiteRepo,
	})

	// The host's packet filter. No state of the panel's own: the provisional
	// change and the timer that undoes it live in the Agent, which is the only
	// place they survive the API becoming unreachable — which is precisely
	// what a bad firewall rule causes.
	s.firewall = firewallpkg.NewHandler(firewallpkg.HandlerOptions{
		Service: firewallpkg.NewService(firewallpkg.ServiceOptions{
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// Which web server arrangement the host runs. It is a server-wide setting
	// rather than a per-site one, because both servers are one process tree
	// serving every site on the machine.
	s.webserver = webserverpkg.NewHandler(webserverpkg.HandlerOptions{
		Service: webserverpkg.NewService(webserverpkg.ServiceOptions{
			Websites: websiteRepo,
			Jobs:     jobRepo,
			Agent:    agent,
			Audit:    auditRecorder,
			Log:      log,
			ServerID: opts.LocalServerID,
		}),
		Auth: authService,
	})

	// Notifications. Built before the phases that raise events, so each can be
	// given the notifier rather than reaching for it later — and every one of
	// them takes it as an interface it declares itself, so a panel with no
	// channels configured behaves exactly as it did before this phase.
	notificationRepo := notificationspkg.NewRepository(opts.Pool)
	notificationService := notificationspkg.NewService(notificationspkg.ServiceOptions{
		Repository: notificationRepo,
		Audit:      auditRecorder,
		Crypto:     encrypter,
		Log:        log,
		ServerID:   opts.LocalServerID,
		PanelURL:   cfg.PanelURL,
	})
	s.notifications = notificationspkg.NewHandler(notificationspkg.HandlerOptions{
		Service: notificationService,
		Auth:    authService,
	})

	// The renewer is built earlier than this, so it is told afterwards. It is
	// the one source that cannot take the notifier as a constructor argument
	// without reordering half the wiring.
	if s.renewer != nil {
		s.renewer.SetNotifier(notificationService)
	}

	// The Security Center. It is built after the phases it scans, because it
	// asks them rather than probing what they manage a second time — a security
	// page that disagreed with the SSH page would be worse than no security
	// page, and two probes of one thing is exactly how that happens.
	securityService := securitypkg.NewService(securitypkg.ServiceOptions{
		Repository:   securitypkg.NewRepository(opts.Pool),
		Agent:        agent,
		Audit:        auditRecorder,
		Certificates: sslRepo,
		Websites:     securitypkg.NewWebsiteAdapter(websiteRepo),
		Updates:      securitypkg.NewUpdateAdapter(updateRepo, opts.LocalServerID),
		Log:          log,
		Notifier:     notificationService,
		ServerID:     opts.LocalServerID,
	})
	s.security = securitypkg.NewHandler(securitypkg.HandlerOptions{
		Service: securityService,
		Auth:    authService,
	})

	// Backups. The service is built before the worker because the worker needs
	// it twice over: as an observer, to move a backup row to completed or
	// failed, and as the payload resolver that keeps storage credentials out of
	// the jobs table.
	backupRepo := backuppkg.NewRepository(opts.Pool)
	backupService := backuppkg.NewService(backuppkg.ServiceOptions{
		Repository: backupRepo,
		Jobs:       jobRepo,
		Agent:      agent,
		Audit:      auditRecorder,
		Crypto:     encrypter,
		Sites:      backuppkg.NewSiteAdapter(websiteRepo),
		Databases:  backuppkg.NewDatabaseAdapter(databaseRepo),
		Log:        log,
		Notifier:   notificationService,
		ServerID:   opts.LocalServerID,
	})
	s.backup = backuppkg.NewHandler(backuppkg.HandlerOptions{
		Service: backupService,
		Auth:    authService,
	})

	if opts.LocalServerID != "" {
		// The worker reconciles websites through the service, so a finished
		// job moves the site to active or failed rather than leaving it in
		// "creating" forever.
		// Both reconcilers see every job and ignore the ones that are not
		// theirs, so the worker does not have to know which is which.
		s.worker = jobs.NewWorker(jobs.Options{
			Repository: jobRepo,
			Dispatcher: agent,
			Observer:   jobs.Observers{websiteService, s.phpSync, s.renewer, backupService, deployService},
			// The resolver rebuilds a backup job's payload at dispatch, so the
			// queue never stores an S3 secret key or an SSH private key.
			Resolver: jobs.Resolvers{backupService, deployService},
			Log:      log,
			// Installing a PHP package downloads and unpacks it, which takes
			// far longer than any other operation the panel runs.
			JobTimeout: phpInstallTimeout,
		})
	}

	if opts.LocalServerID != "" {
		s.dispatcher = notificationspkg.NewDispatcher(notificationspkg.DispatcherOptions{
			Service: notificationService,
			Repo:    notificationRepo,
			Log:     log,
		})
	}

	if opts.LocalServerID != "" {
		s.backupScheduler = backuppkg.NewScheduler(backuppkg.SchedulerOptions{
			Service:  backupService,
			Repo:     backupRepo,
			Log:      log,
			ServerID: opts.LocalServerID,
		})
	}

	if opts.LocalServerID != "" {
		s.monitor = monitoringpkg.NewMonitor(monitoringpkg.MonitorOptions{
			Repo:     monitorRepo,
			Agent:    agent,
			Metrics:  metricRepo,
			Log:      log,
			Notifier: notificationService,
			ServerID: opts.LocalServerID,
			// No finer than the sampler: evaluating the same sample twice can
			// only reach the same conclusion.
			Interval: maxDuration(cfg.Dashboard.SampleInterval, time.Minute),
		})
	}

	if opts.LocalServerID != "" {
		s.sampler = metrics.NewSampler(metrics.SamplerOptions{
			Agent:     agent,
			Metrics:   metricRepo,
			Servers:   serverRepo,
			Log:       log,
			ServerID:  opts.LocalServerID,
			Interval:  cfg.Dashboard.SampleInterval,
			Retention: cfg.Dashboard.MetricRetention,
		})
	}

	s.Router = s.routes()
	s.http = &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           s.Router,
		ReadTimeout:       cfg.ReadTimeout,
		ReadHeaderTimeout: cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelError),
	}
	return s, nil
}

// expiringCertificates reports certificates worth an operator's attention.
//
// A failure is logged and treated as "nothing expiring": the dashboard is what
// an operator opens when something is already wrong, and it must not fail to
// render because one query did.
func (s *Server) expiringCertificates(ctx context.Context) []dashboard.ExpiringCertificate {
	if s.sslRepo == nil {
		return nil
	}

	certificates, err := s.sslRepo.List(ctx)
	if err != nil {
		s.log.Error("failed to read certificates for the dashboard", "error", err.Error())
		return nil
	}

	now := time.Now()
	expiring := make([]dashboard.ExpiringCertificate, 0, 2)
	for _, certificate := range certificates {
		if certificate.Status != sslpkg.StatusExpiring && certificate.Status != sslpkg.StatusExpired {
			continue
		}
		days := certificate.DaysRemaining(now)
		if days == nil {
			continue
		}
		expiring = append(expiring, dashboard.ExpiringCertificate{
			Domain:        certificate.PrimaryDomain,
			DaysRemaining: *days,
		})
	}
	return expiring
}

// routes builds the middleware chain and route table.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Liveness: process is running. Never touches dependencies.
	mux.HandleFunc("GET /healthz", s.handleLive)
	// Readiness: dependencies reachable.
	mux.HandleFunc("GET /readyz", s.handleReady)

	mux.HandleFunc("GET /api/v1/health", s.handleLive)
	mux.HandleFunc("GET /api/v1/version", s.handleVersion)

	auth.NewHandler(s.auth).Routes(mux)
	s.dashboard.Routes(mux)
	s.websites.Routes(mux)
	s.webserver.Routes(mux)
	s.services.Routes(mux)
	s.logs.Routes(mux)
	s.logs.WebsiteRoutes(mux)
	s.cron.Routes(mux)
	s.ssh.Routes(mux)
	s.fail2ban.Routes(mux)
	s.ftp.Routes(mux)
	s.mail.Routes(mux)
	s.deploy.Routes(mux)
	s.dns.Routes(mux)
	s.dns.TemplateRoutes(mux)
	s.updates.Routes(mux)
	s.auditReader.Routes(mux)
	s.monitoring.Routes(mux)
	s.backup.Routes(mux)
	s.security.Routes(mux)
	s.notifications.Routes(mux)
	s.firewall.Routes(mux)
	s.jobs.Routes(mux)
	s.php.Routes(mux)
	s.ssl.Routes(mux)
	s.files.Routes(mux)
	s.databases.Routes(mux)
	s.node.Routes(mux)
	s.tenancy.Routes(mux)

	// Anything unmatched returns the standard error envelope rather than the
	// net/http plain-text default.
	mux.HandleFunc("/", s.handleNotFound)

	// The quota guard wraps the router rather than sitting inside each
	// feature, so the set of guarded routes is one table above rather than six
	// checks that a seventh feature can forget to add.
	var routed http.Handler = mux
	if s.quotaGuard != nil {
		routed = s.quotaGuard.Middleware(mux)
	}

	return middleware.Chain(routed,
		middleware.RequestID(),
		middleware.Logger(s.log),
		middleware.Recover(s.log),
		middleware.SecurityHeaders(),
	)
}

func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, r, map[string]any{
		"status":  "ok",
		"service": "api",
		"version": version.Current().Version,
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	httpx.OK(w, r, version.Current())
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	httpx.Error(w, r, httpx.NotFound("Resource not found"))
}

// handleReady probes every dependency and reports 503 when any is down, so
// orchestrators do not route traffic to a half-initialised API.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	checks := map[string]checkResult{
		"postgres": s.checkPostgres(ctx),
		"redis":    s.checkRedis(ctx),
		"agent":    s.checkAgent(ctx, httpx.RequestIDFromContext(ctx)),
	}

	ready := true
	for _, result := range checks {
		if result.Status != statusUp {
			ready = false
		}
	}

	status := http.StatusOK
	if !ready {
		status = http.StatusServiceUnavailable
	}

	httpx.WriteJSON(w, r, status, httpx.Envelope{
		Success: ready,
		Data: map[string]any{
			"ready":  ready,
			"checks": checks,
		},
		Error: readinessError(ready),
	})
}

func readinessError(ready bool) *httpx.ErrorDetail {
	if ready {
		return nil
	}
	return &httpx.ErrorDetail{
		Code:    httpx.CodeUnavailable,
		Message: "One or more dependencies are unavailable",
	}
}

// checkAgent verifies the Unix socket path is live by issuing agent.ping.
func (s *Server) checkAgent(ctx context.Context, requestID string) checkResult {
	ctx, cancel := context.WithTimeout(ctx, dependencyTimeout)
	defer cancel()

	if requestID == "" {
		requestID = httpx.NewRequestID()
	}
	if err := s.agent.Ping(ctx, requestID); err != nil {
		s.log.Warn("agent readiness probe failed", "error", err.Error())
		return checkResult{Status: statusDown, Error: "agent unreachable"}
	}
	return checkResult{Status: statusUp}
}

// phpInstallTimeout bounds one job end to end.
//
// It is the longest any operation the panel runs may take: a PHP package has
// to be downloaded and unpacked, which is minutes on a slow link, and a
// shorter bound would fail installs that were going to succeed.
const phpInstallTimeout = 15 * time.Minute

// Run starts the listener and blocks until ctx is cancelled, then drains
// in-flight requests within the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	errCh := make(chan error, 1)

	// The metric sampler is the only reason a history exists to graph, so it
	// runs for the lifetime of the server and stops with it.
	var samplerDone chan struct{}
	if s.sampler != nil {
		samplerDone = make(chan struct{})
		go func() {
			defer close(samplerDone)
			s.sampler.Run(ctx)
		}()
	} else {
		s.log.Warn("metric sampling is disabled: no server is registered")
	}

	// A deployment that was running when the panel stopped is one nothing will
	// finish. Closed at startup rather than left, because the unique index
	// that stops two deployments at once would otherwise hold for ever and no
	// further deployment of that website could start — while the page showed
	// one in progress that nothing was progressing.
	if s.deployService != nil {
		s.deployService.ReleaseStale(ctx)
	}

	// An impersonation left open by a restart is one that never ends: the
	// session went with the process, and nothing would ever write the end
	// time. A panel that showed it as still running would be showing an
	// operator inside a customer's account who is not there.
	if s.tenancyService != nil {
		if closed, err := s.tenancyService.CloseStaleImpersonations(ctx); err != nil {
			s.log.Warn("could not close impersonations left open by a restart",
				"error", err.Error())
		} else if closed > 0 {
			s.log.Info("closed impersonations left open by a restart", "count", closed)
		}
	}

	// The job worker is what turns a queued website into a provisioned one.
	// Without it every site would sit in "creating" indefinitely.
	if s.worker != nil {
		go s.worker.Run(ctx)
	} else {
		s.log.Warn("job processing is disabled: no server is registered")
	}

	// PHP can be installed or removed outside the panel, so the version table
	// is refreshed on an interval rather than only at startup.
	if s.phpSync != nil {
		go s.phpSync.Run(ctx, phppkg.DefaultSyncInterval)
	}

	// An unattended certificate expires in 90 days and the failure is total:
	// every visitor gets a browser warning at once. The sweep is what makes
	// the difference between issuing certificates and keeping sites working.
	if s.renewer != nil {
		go s.renewer.Run(ctx, sslpkg.SweepInterval)
	}

	// A host nobody updates is the one that gets broken into. The loop only
	// applies anything when an operator has asked for it — the default policy
	// is off — but it checks either way, so the panel can say how far behind
	// this machine is without anybody having to ask.
	if s.updateScheduler != nil {
		go s.updateScheduler.Run(ctx)
	}

	// A monitor that has stopped looks exactly like a host with no problems,
	// which is why it is started here rather than lazily and why it logs when
	// it starts.
	if s.monitor != nil {
		go s.monitor.Run(ctx)
	}

	// A notification nobody delivers is a row in a table. This is the loop that
	// turns what the rest of the panel found out into something somebody hears
	// about, and it logs when it starts for the same reason the monitor does:
	// a dispatcher that has stopped looks exactly like a machine with nothing
	// wrong.
	if s.dispatcher != nil {
		go s.dispatcher.Run(ctx)
	}

	// Disk and bandwidth are facts about a host, not rows in this database, so
	// somebody has to go and look. On a timer rather than when a page opens:
	// measuring walks a customer's whole site, and a figure that only exists
	// while somebody is watching cannot show that a customer went over their
	// quota last Tuesday.
	if s.tenancyService != nil {
		go s.tenancyService.StartSampler(ctx, tenancypkg.DefaultSampleInterval)
	}

	// A schedule nobody runs is a backup page that lists intentions. This loop
	// is what turns them into archives, and it takes a missed window late
	// rather than skipping it: a late backup is a backup.
	if s.backupScheduler != nil {
		go s.backupScheduler.Run(ctx)
	}

	go func() {
		s.log.Info("api listening",
			"addr", s.cfg.HTTPAddr,
			"environment", string(s.cfg.Environment),
			"version", version.Current().Version,
		)
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		s.log.Info("shutdown signal received", "timeout", s.cfg.ShutdownTimeout.String())
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	if err := s.http.Shutdown(shutdownCtx); err != nil {
		// Shutdown deadline exceeded: force-close remaining connections so the
		// process can exit rather than hang.
		s.log.Error("graceful shutdown failed, forcing close", "error", err.Error())
		if closeErr := s.http.Close(); closeErr != nil {
			return errors.Join(err, closeErr)
		}
		return err
	}

	// The sampler observes the same cancelled context, so this only waits for
	// an in-flight sample to finish rather than driving the shutdown.
	if samplerDone != nil {
		select {
		case <-samplerDone:
		case <-time.After(s.cfg.ShutdownTimeout):
			s.log.Warn("metric sampler did not stop within the shutdown timeout")
		}
	}

	// A job in flight is changing the host, so the worker is given the same
	// grace period to finish it rather than being abandoned mid-operation.
	if s.worker != nil {
		if err := s.worker.Wait(s.cfg.ShutdownTimeout); err != nil {
			s.log.Warn("job worker did not stop within the shutdown timeout")
		}
	}

	s.log.Info("shutdown complete")
	return nil
}
