package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/command"
)

// fakeSystemctl writes a stand-in for systemctl.
//
// The real binary is not used: a test that depends on the build machine's init
// system would be unrunnable in a container and would not let the parsing be
// checked against known output.
func fakeSystemctl(t *testing.T, body string) *Provider {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("command execution tests require a Unix host")
	}

	path := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("write fake systemctl: %v", err)
	}

	runner, err := command.NewRunner(command.Spec{Name: CommandName, Path: path})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return NewProvider(runner)
}

// showOutput is what `systemctl show` prints for a running unit.
const showOutput = `printf '%s\n' \
	"Id=nginx.service" \
	"Description=A high performance web server" \
	"LoadState=loaded" \
	"ActiveState=active" \
	"SubState=running" \
	"UnitFileState=enabled" \
	"MainPID=1234"`

func TestStatusParsesSystemdOutput(t *testing.T) {
	provider := fakeSystemctl(t, showOutput)

	status, err := provider.Status(context.Background(), "nginx")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if status.Name != "nginx.service" {
		t.Fatalf("name = %q, want nginx.service", status.Name)
	}
	if status.Description != "A high performance web server" {
		t.Fatalf("description = %q", status.Description)
	}
	if status.ActiveState != "active" || status.SubState != "running" {
		t.Fatalf("unexpected state: %+v", status)
	}
	if !status.Running {
		t.Fatal("active+running must report Running")
	}
	if status.Enabled == nil || !*status.Enabled {
		t.Fatalf("enabled = %v, want true", status.Enabled)
	}
	if status.MainPID != 1234 {
		t.Fatalf("main pid = %d, want 1234", status.MainPID)
	}
}

func TestStatusDistinguishesStoppedFromRunning(t *testing.T) {
	provider := fakeSystemctl(t, `printf '%s\n' \
		"Id=nginx.service" "LoadState=loaded" "ActiveState=inactive" \
		"SubState=dead" "UnitFileState=disabled" "MainPID=0"`)

	status, err := provider.Status(context.Background(), "nginx")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running {
		t.Fatal("an inactive unit must not report Running")
	}
	if status.Enabled == nil || *status.Enabled {
		t.Fatalf("enabled = %v, want false", status.Enabled)
	}
	if status.MainPID != 0 {
		t.Fatalf("a stopped service must have no main pid, got %d", status.MainPID)
	}
}

func TestUnknownUnitFileStateIsUnknownNotDisabled(t *testing.T) {
	// A transient unit has an empty UnitFileState. Reporting that as
	// "disabled" would be a different claim than "we do not know".
	provider := fakeSystemctl(t, `printf '%s\n' \
		"Id=x.service" "LoadState=loaded" "ActiveState=active" "SubState=running" "UnitFileState=" "MainPID=1"`)

	status, err := provider.Status(context.Background(), "x")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Enabled != nil {
		t.Fatalf("enabled must be nil when systemd cannot answer, got %v", *status.Enabled)
	}
}

func TestStatusReportsNotFound(t *testing.T) {
	// systemd answers for a unit it has never heard of with LoadState
	// not-found and a zero exit, so absence must be detected from the output.
	provider := fakeSystemctl(t, `printf '%s\n' \
		"Id=ghost.service" "LoadState=not-found" "ActiveState=inactive" "SubState=dead"`)

	_, err := provider.Status(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNameValidation(t *testing.T) {
	valid := []string{"nginx", "nginx.service", "php8.4-fpm", "getty@tty1.service", "a1_b-c.d"}
	for _, name := range valid {
		if err := ValidateName(name); err != nil {
			t.Fatalf("name %q must be accepted: %v", name, err)
		}
	}

	// systemctl arguments are argv entries, so metacharacters are already
	// inert. The pattern exists to stop an option-looking value turning a
	// status query into a different command.
	invalid := []string{
		"",
		"--version",
		"-h",
		"nginx; rm -rf /",
		"nginx && id",
		"$(id)",
		"`id`",
		"nginx\nrm -rf /",
		"../../etc/passwd",
		"/etc/passwd",
		"nginx service",
		strings.Repeat("a", 200),
	}
	for _, name := range invalid {
		if err := ValidateName(name); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("name %q must be rejected, got %v", name, err)
		}
	}
}

func TestNameIsValidatedBeforeExecution(t *testing.T) {
	// The fake would create a marker file if it ever ran; validation must
	// happen first, so it never does.
	dir := t.TempDir()
	marker := filepath.Join(dir, "executed")

	path := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch "+marker+"\n"), 0o700); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	runner, err := command.NewRunner(command.Spec{Name: CommandName, Path: path})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	provider := NewProvider(runner)

	if _, err := provider.Status(context.Background(), "nginx; rm -rf /"); err == nil {
		t.Fatal("an invalid name must be rejected")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("systemctl must not run for an invalid name")
	}
}

func TestUnavailableWithoutSystemd(t *testing.T) {
	runner, err := command.NewRunner(command.Spec{
		Name: CommandName,
		Path: filepath.Join(t.TempDir(), "systemctl"),
	})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	provider := NewProvider(runner)

	if provider.Available() {
		t.Fatal("a missing systemctl must not report available")
	}
	if _, err := provider.Status(context.Background(), "nginx"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
	if _, err := provider.List(context.Background(), []string{"nginx"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestNilRunnerIsUnavailable(t *testing.T) {
	// A provider built without a runner must fail closed rather than panic.
	provider := NewProvider(nil)

	if provider.Available() {
		t.Fatal("a nil runner must not report available")
	}
	if _, err := provider.Status(context.Background(), "nginx"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestListReportsAbsentUnitsWithoutFailing(t *testing.T) {
	// A panel asking about php8.4-fpm on a host without it wants that answer,
	// not an error that hides every other service.
	provider := fakeSystemctl(t, `
case "$2" in
  nginx) printf '%s\n' "Id=nginx.service" "LoadState=loaded" "ActiveState=active" "SubState=running" ;;
  *) printf '%s\n' "Id=$2" "LoadState=not-found" "ActiveState=inactive" ;;
esac`)

	statuses, err := provider.List(context.Background(), []string{"nginx", "absent"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("expected 2 statuses, got %d", len(statuses))
	}

	// Sorted output keeps responses stable.
	if statuses[0].Name != "absent" || statuses[0].LoadState != "not-found" {
		t.Fatalf("absent unit reported incorrectly: %+v", statuses[0])
	}
	if statuses[1].Name != "nginx.service" || !statuses[1].Running {
		t.Fatalf("running unit reported incorrectly: %+v", statuses[1])
	}
}

func TestListDeduplicatesAndValidates(t *testing.T) {
	provider := fakeSystemctl(t, showOutput)

	statuses, err := provider.List(context.Background(), []string{"nginx", "nginx"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("duplicates must collapse, got %d", len(statuses))
	}

	if _, err := provider.List(context.Background(), []string{"nginx", "--version"}); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("an invalid name anywhere in the list must be rejected, got %v", err)
	}
}

func TestListRejectsOversizedRequest(t *testing.T) {
	provider := fakeSystemctl(t, showOutput)

	names := make([]string, maxUnits+1)
	for i := range names {
		names[i] = "unit"
	}
	if _, err := provider.List(context.Background(), names); err == nil {
		t.Fatal("an oversized service list must be rejected")
	}
}

func TestParseProperties(t *testing.T) {
	properties := parseProperties("Id=nginx.service\nDescription=Web server\n\nMainPID=42\ngarbage\n")

	if properties["Id"] != "nginx.service" {
		t.Fatalf("Id = %q", properties["Id"])
	}
	// A value may itself contain '=' and must not be truncated at the first.
	properties = parseProperties("Description=a=b=c\n")
	if properties["Description"] != "a=b=c" {
		t.Fatalf("Description = %q, want a=b=c", properties["Description"])
	}
}
