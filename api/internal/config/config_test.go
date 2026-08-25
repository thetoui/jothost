package config

import (
	"strings"
	"testing"
	"time"
)

// setValid populates the minimum environment required by Load.
func setValid(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://jothost:pw@postgres:5432/jothost?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://redis:6379/0")
}

func TestLoadDefaults(t *testing.T) {
	setValid(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Environment != EnvDevelopment {
		t.Fatalf("expected development environment, got %q", cfg.Environment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Fatalf("expected default addr :8080, got %q", cfg.HTTPAddr)
	}
	if cfg.AgentSocket != "/run/jothost/agent.sock" {
		t.Fatalf("unexpected default agent socket %q", cfg.AgentSocket)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Fatalf("unexpected default shutdown timeout %v", cfg.ShutdownTimeout)
	}
	if cfg.IsProduction() {
		t.Fatal("development config must not report production")
	}
}

func TestLoadRequiresDatastoreURLs(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("REDIS_URL", "")

	_, err := Load()
	if err == nil {
		t.Fatal("expected missing datastore URLs to fail validation")
	}
	for _, want := range []string{"DATABASE_URL is required", "REDIS_URL is required"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must mention %q", err, want)
		}
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	setValid(t)
	t.Setenv("JOTHOST_ENV", "staging")
	t.Setenv("AGENT_SOCKET", "run/jothost/agent.sock")

	_, err := Load()
	if err == nil {
		t.Fatal("expected invalid environment and relative socket path to fail")
	}
	if !strings.Contains(err.Error(), "JOTHOST_ENV") {
		t.Fatalf("error must mention JOTHOST_ENV: %v", err)
	}
	if !strings.Contains(err.Error(), "AGENT_SOCKET must be an absolute path") {
		t.Fatalf("error must reject relative socket path: %v", err)
	}
}

func TestGetDurationAcceptsDurationsAndSeconds(t *testing.T) {
	setValid(t)
	t.Setenv("AGENT_TIMEOUT", "45s")
	t.Setenv("API_READ_TIMEOUT", "20")
	t.Setenv("API_WRITE_TIMEOUT", "not-a-duration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.AgentTimeout != 45*time.Second {
		t.Fatalf("expected 45s agent timeout, got %v", cfg.AgentTimeout)
	}
	if cfg.ReadTimeout != 20*time.Second {
		t.Fatalf("bare seconds must be accepted, got %v", cfg.ReadTimeout)
	}
	if cfg.WriteTimeout != 30*time.Second {
		t.Fatalf("unparsable duration must fall back to the default, got %v", cfg.WriteTimeout)
	}
}

func TestProductionEnvironment(t *testing.T) {
	setValid(t)
	t.Setenv("JOTHOST_ENV", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if !cfg.IsProduction() {
		t.Fatal("production environment must report IsProduction")
	}
}
