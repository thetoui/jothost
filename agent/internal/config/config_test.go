package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.SocketPath != "/run/jothost/agent.sock" {
		t.Fatalf("unexpected default socket path %q", cfg.SocketPath)
	}
	if cfg.SocketMode != 0o660 {
		t.Fatalf("socket mode must default to 0660, got %#o", cfg.SocketMode)
	}
	if cfg.OperationTimeout != 30*time.Second {
		t.Fatalf("unexpected default operation timeout %v", cfg.OperationTimeout)
	}
	if cfg.MaxConcurrent <= 0 {
		t.Fatal("MaxConcurrent must default to a positive bound")
	}
}

func TestLoadRejectsRelativeSocketPath(t *testing.T) {
	t.Setenv("AGENT_SOCKET", "run/agent.sock")

	if _, err := Load(); err == nil {
		t.Fatal("relative socket path must be rejected")
	}
}

func TestLoadRejectsTraversalInSocketPath(t *testing.T) {
	// A path containing .. could place the privileged socket outside its
	// intended directory.
	t.Setenv("AGENT_SOCKET", "/run/jothost/../../tmp/agent.sock")

	_, err := Load()
	if err == nil {
		t.Fatal("socket path containing '..' must be rejected")
	}
	if !strings.Contains(err.Error(), "'..'") {
		t.Fatalf("error must explain the traversal rejection: %v", err)
	}
}

func TestLoadRejectsNonPositiveBounds(t *testing.T) {
	t.Setenv("AGENT_MAX_CONCURRENT", "0")
	t.Setenv("AGENT_OPERATION_TIMEOUT", "0s")

	_, err := Load()
	if err == nil {
		t.Fatal("zero timeout must be rejected: operations must always be bounded")
	}
	if !strings.Contains(err.Error(), "AGENT_OPERATION_TIMEOUT") {
		t.Fatalf("error must mention AGENT_OPERATION_TIMEOUT: %v", err)
	}
}

func TestDurationOverrides(t *testing.T) {
	t.Setenv("AGENT_OPERATION_TIMEOUT", "90s")
	t.Setenv("AGENT_SHUTDOWN_TIMEOUT", "45")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}
	if cfg.OperationTimeout != 90*time.Second {
		t.Fatalf("expected 90s, got %v", cfg.OperationTimeout)
	}
	if cfg.ShutdownTimeout != 45*time.Second {
		t.Fatalf("bare seconds must be accepted, got %v", cfg.ShutdownTimeout)
	}
}
