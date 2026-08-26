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
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/dashboard"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/jobs"
	"github.com/jothost/panel/api/internal/metrics"
	"github.com/jothost/panel/api/internal/middleware"
	"github.com/jothost/panel/api/internal/ratelimit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
	"github.com/jothost/panel/api/internal/websites"
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

	websites *websites.Handler
	jobs     *jobs.Handler
	// worker realises queued jobs against the Agent. It is nil when no server
	// is registered, because there is no host to provision against.
	worker *jobs.Worker

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
		MonitoredUnits: cfg.Dashboard.MonitoredServices,
		// Postgres and Redis are checked by the API itself: it holds the
		// pools, so its own probe is a better answer than asking the Agent
		// whether a unit happens to be running.
		Dependencies:    []string{"postgres", "redis"},
		CheckDependency: s.checkDependencyHealth,
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

	websiteService := websites.NewService(websites.ServiceOptions{
		Repository: websiteRepo,
		Jobs:       jobRepo,
		Audit:      auditRecorder,
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

	if opts.LocalServerID != "" {
		// The worker reconciles websites through the service, so a finished
		// job moves the site to active or failed rather than leaving it in
		// "creating" forever.
		s.worker = jobs.NewWorker(jobs.Options{
			Repository: jobRepo,
			Dispatcher: agent,
			Observer:   websiteService,
			Log:        log,
			JobTimeout: cfg.AgentTimeout * 10,
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
	s.jobs.Routes(mux)

	// Anything unmatched returns the standard error envelope rather than the
	// net/http plain-text default.
	mux.HandleFunc("/", s.handleNotFound)

	return middleware.Chain(mux,
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

	// The job worker is what turns a queued website into a provisioned one.
	// Without it every site would sit in "creating" indefinitely.
	if s.worker != nil {
		go s.worker.Run(ctx)
	} else {
		s.log.Warn("job processing is disabled: no server is registered")
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
