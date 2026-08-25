// Package config loads API configuration from the environment. Secrets are
// never hard-coded (ARCHITECTURE.md section 11) and never logged.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
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
	DatabaseURL string
	RedisURL    string

	// AgentSocket is the Unix socket path exposed by the Host Agent.
	AgentSocket string
	// AgentTimeout bounds every agent operation (CLAUDE.md section 6).
	AgentTimeout time.Duration
}

// IsProduction reports whether the API runs with production guarantees.
func (c Config) IsProduction() bool { return c.Environment == EnvProduction }

// Load reads configuration from the process environment and validates it.
// It returns every validation problem at once so a misconfigured deployment
// does not require repeated restarts to discover all issues.
func Load() (Config, error) {
	cfg := Config{
		Environment:     Environment(getString("JOTHOST_ENV", string(EnvDevelopment))),
		HTTPAddr:        getString("API_HTTP_ADDR", ":8080"),
		LogLevel:        getString("LOG_LEVEL", "info"),
		ShutdownTimeout: getDuration("API_SHUTDOWN_TIMEOUT", 15*time.Second),
		ReadTimeout:     getDuration("API_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:    getDuration("API_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:     getDuration("API_IDLE_TIMEOUT", 60*time.Second),
		DatabaseURL:     getString("DATABASE_URL", ""),
		RedisURL:        getString("REDIS_URL", ""),
		AgentSocket:     getString("AGENT_SOCKET", "/run/jothost/agent.sock"),
		AgentTimeout:    getDuration("AGENT_TIMEOUT", 30*time.Second),
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
	if !strings.HasPrefix(cfg.AgentSocket, "/") {
		problems = append(problems, "AGENT_SOCKET must be an absolute path")
	}
	if cfg.AgentTimeout <= 0 {
		problems = append(problems, "AGENT_TIMEOUT must be greater than zero")
	}
	if cfg.ShutdownTimeout <= 0 {
		problems = append(problems, "API_SHUTDOWN_TIMEOUT must be greater than zero")
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("invalid configuration: %s", strings.Join(problems, "; "))
	}
	return cfg, nil
}

func getString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
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
