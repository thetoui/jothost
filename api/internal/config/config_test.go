package config

import (
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/testsupport"
)

// setValid populates the minimum environment required by Load.
func setValid(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://jothost:pw@postgres:5432/jothost?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://redis:6379/0")
	t.Setenv("ENCRYPTION_KEY", testsupport.TestEncryptionKey)
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

func TestAuthDefaults(t *testing.T) {
	setValid(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.Auth.AccessTokenTTL != 15*time.Minute {
		t.Fatalf("unexpected access token TTL %v", cfg.Auth.AccessTokenTTL)
	}
	if cfg.Auth.RefreshTokenTTL != 7*24*time.Hour {
		t.Fatalf("unexpected refresh token TTL %v", cfg.Auth.RefreshTokenTTL)
	}
	if cfg.Auth.LoginRateLimit <= 0 || cfg.Auth.LoginIPRateLimit <= 0 {
		t.Fatal("login rate limits must default to positive values")
	}
	if !cfg.AutoMigrate {
		t.Fatal("AUTO_MIGRATE must default to true")
	}
}

func TestEncryptionKeyIsRequired(t *testing.T) {
	setValid(t)
	t.Setenv("ENCRYPTION_KEY", "")

	// Without a key, 2FA secrets could not be decrypted; the API must refuse
	// to start rather than fail later at runtime.
	_, err := Load()
	if err == nil {
		t.Fatal("a missing ENCRYPTION_KEY must fail validation")
	}
	if !strings.Contains(err.Error(), "ENCRYPTION_KEY is required") {
		t.Fatalf("error must name the variable: %v", err)
	}
}

func TestEncryptionKeyMustBeValid(t *testing.T) {
	setValid(t)

	for _, key := range []string{"tooshort", "zz" + testsupport.TestEncryptionKey[2:]} {
		t.Setenv("ENCRYPTION_KEY", key)
		if _, err := Load(); err == nil {
			t.Fatalf("key %q must be rejected", key)
		}
	}
}

func TestRefreshTokenMustOutliveAccessToken(t *testing.T) {
	setValid(t)
	t.Setenv("ACCESS_TOKEN_TTL", "1h")
	t.Setenv("REFRESH_TOKEN_TTL", "30m")

	// A refresh token that expires first makes the pair unusable.
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "REFRESH_TOKEN_TTL must be longer") {
		t.Fatalf("expected a TTL ordering error, got %v", err)
	}
}

func TestRateLimitBoundsMustBePositive(t *testing.T) {
	setValid(t)
	t.Setenv("LOGIN_RATE_LIMIT", "0")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "LOGIN_RATE_LIMIT") {
		t.Fatalf("a zero rate limit must be rejected, got %v", err)
	}
}

func TestAutoMigrateCanBeDisabled(t *testing.T) {
	setValid(t)
	t.Setenv("AUTO_MIGRATE", "false")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.AutoMigrate {
		t.Fatal("AUTO_MIGRATE=false must disable automatic migrations")
	}
}
