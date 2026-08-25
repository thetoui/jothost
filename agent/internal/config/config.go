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
	}

	var problems []string
	if !filepath.IsAbs(cfg.SocketPath) {
		problems = append(problems, "AGENT_SOCKET must be an absolute path")
	}
	if strings.Contains(cfg.SocketPath, "..") {
		problems = append(problems, "AGENT_SOCKET must not contain '..'")
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
