// Package server wires configuration, middleware, and routes into an HTTP
// server with graceful shutdown.
package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/jothost/panel/api/internal/agentclient"
	"github.com/jothost/panel/api/internal/audit"
	"github.com/jothost/panel/api/internal/auth"
	"github.com/jothost/panel/api/internal/config"
	"github.com/jothost/panel/api/internal/httpx"
	"github.com/jothost/panel/api/internal/middleware"
	"github.com/jothost/panel/api/internal/ratelimit"
	"github.com/jothost/panel/api/internal/rbac"
	"github.com/jothost/panel/api/internal/secrets"
	"github.com/jothost/panel/api/internal/sessions"
	"github.com/jothost/panel/api/internal/twofactor"
	"github.com/jothost/panel/api/internal/users"
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

	s := &Server{
		cfg:   cfg,
		log:   log,
		agent: agentclient.New(cfg.AgentSocket, cfg.AgentTimeout),
		pool:  opts.Pool,
		redis: opts.Redis,
		auth:  authService,
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

	s.log.Info("shutdown complete")
	return nil
}
