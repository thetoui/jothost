// Package config loads API configuration from the environment. Secrets are
// never hard-coded (ARCHITECTURE.md section 11) and never logged.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/api/internal/secrets"
)

// Environment names the deployment environment.
type Environment string

// Supported environments.
const (
	EnvDevelopment Environment = "development"
	EnvProduction  Environment = "production"
	EnvTest        Environment = "test"
)

// Config is the fully validated API configuration.
type Config struct {
	Environment     Environment
	HTTPAddr        string
	LogLevel        string
	ShutdownTimeout time.Duration
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration

	// DatabaseURL and RedisURL hold credentials and must never be logged.
	DatabaseURL    string
	RedisURL       string
	DBMaxConns     int32
	ConnectTimeout time.Duration

	// MigrationsDir holds the SQL migration files.
	MigrationsDir string
	// AutoMigrate applies pending migrations at startup.
	AutoMigrate bool

	// AgentSocket is the Unix socket path exposed by the Host Agent.
	AgentSocket string
	// AgentTimeout bounds every agent operation (CLAUDE.md section 6).
	AgentTimeout time.Duration

	// Auth holds the authentication settings.
	Auth AuthConfig
}

// AuthConfig groups the Phase 1 authentication settings.
type AuthConfig struct {
	// EncryptionKey is the hex-encoded AES-256 key protecting secrets at rest.
	// It must never be logged.
	EncryptionKey string

	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	MFAChallengeTTL time.Duration
	TOTPIssuer      string

	// LoginRateLimit bounds attempts per account per window;
	// LoginIPRateLimit bounds attempts per source address, which is higher so
	// a shared office NAT does not lock everyone out at once.
	LoginRateLimit   int
	LoginIPRateLimit int
	LoginRateWindow  time.Duration
}

// IsProduction reports whether the API runs with production guarantees.
func (c Config) IsProduction() bool { return c.Environment == EnvProduction }

// Load reads configuration from the process environment and validates it.
// It returns every validation problem at once so a misconfigured deployment
// does not require repeated restarts to discover all issues.
func Load() (Config, error) {
	environment := Environment(getString("JOTHOST_ENV", string(EnvDevelopment)))

	cfg := Config{
		Environment:     environment,
		HTTPAddr:        getString("API_HTTP_ADDR", ":8080"),
		LogLevel:        getString("LOG_LEVEL", "info"),
		ShutdownTimeout: getDuration("API_SHUTDOWN_TIMEOUT", 15*time.Second),
		ReadTimeout:     getDuration("API_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:    getDuration("API_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:     getDuration("API_IDLE_TIMEOUT", 60*time.Second),
		DatabaseURL:     getString("DATABASE_URL", ""),
		RedisURL:        getString("REDIS_URL", ""),
		DBMaxConns:      int32(getInt("DATABASE_MAX_CONNS", 10)),
		ConnectTimeout:  getDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
		MigrationsDir:   getString("MIGRATIONS_DIR", "/app/migrations"),
		AutoMigrate:     getBool("AUTO_MIGRATE", true),
		AgentSocket:     getString("AGENT_SOCKET", "/run/jothost/agent.sock"),
		AgentTimeout:    getDuration("AGENT_TIMEOUT", 30*time.Second),
		Auth: AuthConfig{
			EncryptionKey:    getString("ENCRYPTION_KEY", ""),
			AccessTokenTTL:   getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
			RefreshTokenTTL:  getDuration("REFRESH_TOKEN_TTL", 7*24*time.Hour),
			MFAChallengeTTL:  getDuration("MFA_CHALLENGE_TTL", 5*time.Minute),
			TOTPIssuer:       getString("TOTP_ISSUER", "JotHost Panel"),
			LoginRateLimit:   getInt("LOGIN_RATE_LIMIT", 5),
			LoginIPRateLimit: getInt("LOGIN_IP_RATE_LIMIT", 20),
			LoginRateWindow:  getDuration("LOGIN_RATE_WINDOW", 15*time.Minute),
		},
	}

	var problems []string
	switch cfg.Environment {
	case EnvDevelopment, EnvProduction, EnvTest:
	default:
		problems = append(problems, fmt.Sprintf("JOTHOST_ENV must be one of development, production, test (got %q)", cfg.Environment))
	}
	if strings.TrimSpace(cfg.HTTPAddr) == "" {
		problems = append(problems, "API_HTTP_ADDR must not be empty")
	}
	if cfg.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}
	if cfg.RedisURL == "" {
		problems = append(problems, "REDIS_URL is required")
	}
	if cfg.DBMaxConns <= 0 {
		problems = append(problems, "DATABASE_MAX_CONNS must be greater than zero")
	}
	if !strings.HasPrefix(cfg.AgentSocket, "/") {
		problems = append(problems, "AGENT_SOCKET must be an absolute path")
	}
	if cfg.AgentTimeout <= 0 {
		problems = append(problems, "AGENT_TIMEOUT must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		problems = append(problems, "API_SHUTDOWN_TIMEOUT must be greater than zero")
	}
	if strings.TrimSpace(cfg.MigrationsDir) == "" {
		problems = append(problems, "MIGRATIONS_DIR must not be empty")
	}

	problems = append(problems, cfg.Auth.validate()...)

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

// validate checks the authentication settings.
func (a AuthConfig) validate() []string {
	var problems []string

	// The key is required rather than auto-generated: a generated key would
	// change on restart and silently orphan every encrypted 2FA secret.
	if a.EncryptionKey == "" {
		problems = append(problems,
			"ENCRYPTION_KEY is required (generate one with: openssl rand -hex 32)")
	} else if _, err := secrets.NewEncrypter(a.EncryptionKey); err != nil {
		problems = append(problems, "ENCRYPTION_KEY "+err.Error())
	}

	if a.AccessTokenTTL <= 0 {
		problems = append(problems, "ACCESS_TOKEN_TTL must be greater than zero")
	}
	if a.RefreshTokenTTL <= a.AccessTokenTTL {
		problems = append(problems, "REFRESH_TOKEN_TTL must be longer than ACCESS_TOKEN_TTL")
	}
	if a.MFAChallengeTTL <= 0 {
		problems = append(problems, "MFA_CHALLENGE_TTL must be greater than zero")
	}
	if a.LoginRateLimit <= 0 {
		problems = append(problems, "LOGIN_RATE_LIMIT must be greater than zero")
	}
	if a.LoginIPRateLimit <= 0 {
		problems = append(problems, "LOGIN_IP_RATE_LIMIT must be greater than zero")
	}
	if a.LoginRateWindow <= 0 {
		problems = append(problems, "LOGIN_RATE_WINDOW must be greater than zero")
	}
	if strings.TrimSpace(a.TOTPIssuer) == "" {
		problems = append(problems, "TOTP_ISSUER must not be empty")
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
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
		return v
	}
	return fallback
}

// getBool accepts the values strconv.ParseBool understands.
func getBool(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	if v, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
		return v
	}
	return fallback
}

// getDuration parses a Go duration string. An unparsable value falls back to
// the default rather than failing silently with a zero timeout.
func getDuration(key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	if d, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil {
		return d
	}
	// Also accept a bare number of seconds for operator convenience.
	if secs, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return fallback
}
