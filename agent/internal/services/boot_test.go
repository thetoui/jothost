//go:build linux

package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/command"
)

// bootStub is recordingSystemctl with the unit's boot state made a parameter.
//
// The state is the whole subject here: a unit that is running and
// "UnitFileState=disabled" is the defect, and one that is running and
// "enabled" is the fixed host. Both have to be produced to tell them apart.
func bootStub(t *testing.T, unitFileState string) (*command.Runner, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "systemctl")

	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$1" in
  show)
    cat <<PROPS
Id=nginx.service
Description=nginx - high performance web server
LoadState=loaded
ActiveState=active
SubState=running
UnitFileState=` + unitFileState + `
MainPID=4242
PROPS
    ;;
esac
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	runner, err := command.NewRunner(command.Spec{Name: CommandName, Path: stub})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	return runner, func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		var calls []string
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				calls = append(calls, line)
			}
		}
		return calls
	}
}

// runningProcesses reports every asked-for name as running, which is what a
// host looks like when the daemons are up.
type runningProcesses struct{}

func (runningProcesses) FindByName(names []string) map[string]int {
	found := make(map[string]int, len(names))
	for i, name := range names {
		found[name] = 1000 + i
	}
	return found
}

// noProcesses is a host where nothing catalogued is running.
type noProcesses struct{}

func (noProcesses) FindByName([]string) map[string]int { return map[string]int{} }

func findingFor(report BootReport, key string) (BootFinding, bool) {
	for _, finding := range report.Findings {
		if finding.Key == key {
			return finding, true
		}
	}
	return BootFinding{}, false
}

// TestBootAuditFlagsARunningServiceThatWillNotComeBack is the defect itself:
// a daemon the panel started, serving now, with nothing to start it at boot.
func TestBootAuditFlagsARunningServiceThatWillNotComeBack(t *testing.T) {
	runner, _ := bootStub(t, "disabled")
	provider := NewProvider(runner)

	report := provider.BootAudit(context.Background(), nil, runningProcesses{})

	finding, ok := findingFor(report, "nginx")
	if !ok {
		t.Fatalf("nginx is missing from the audit: %+v", report.Findings)
	}
	if !finding.AtRisk {
		t.Fatalf("a running, disabled service must be at risk: %+v", finding)
	}
	if finding.Reason == "" {
		t.Fatal("an at-risk finding must say why")
	}
}

func TestBootAuditPassesAServiceThatIsAlreadyPersistent(t *testing.T) {
	runner, _ := bootStub(t, "enabled")
	provider := NewProvider(runner)

	report := provider.BootAudit(context.Background(), nil, runningProcesses{})

	for _, finding := range report.AtRisk() {
		t.Errorf("%s was reported at risk on a correctly configured host: %s",
			finding.Key, finding.Reason)
	}
}

// TestBootAuditIgnoresWhatIsNotRunning is the rule that keeps this safe.
//
// Apache is *meant* to be stopped on a host serving everything from nginx, and
// PHP-FPM 8.2 is meant to be stopped on a host running only 8.4. Enabling
// everything installed would start daemons at boot that somebody deliberately
// turned off.
func TestBootAuditIgnoresWhatIsNotRunning(t *testing.T) {
	runner, _ := bootStub(t, "disabled")
	provider := NewProvider(runner)

	report := provider.BootAudit(context.Background(), nil, noProcesses{})

	for _, finding := range report.Findings {
		if !finding.Running {
			t.Errorf("%s is not running and should not be in the audit at all", finding.Key)
		}
	}
}

// TestEnsureBootPersistenceEnablesTheUnitTheAgentChose is the same safety
// property the rest of this package rests on: a catalogue key never becomes
// argv, and the unit enabled is the one the Agent resolved.
func TestEnsureBootPersistenceEnablesTheUnitTheAgentChose(t *testing.T) {
	runner, calls := bootStub(t, "disabled")
	provider := NewProvider(runner)

	report := provider.EnsureBootPersistence(context.Background(), nil, runningProcesses{})

	if len(report.Enabled) == 0 {
		t.Fatalf("nothing was made persistent: %+v", report)
	}

	var enabled []string
	for _, call := range calls() {
		if strings.HasPrefix(call, "enable ") {
			enabled = append(enabled, call)
		}
	}
	if len(enabled) == 0 {
		t.Fatalf("no enable was issued; calls were %v", calls())
	}
	for _, call := range enabled {
		// The unit, not the key. "enable nginx" would be a different thing
		// from "enable nginx.service" on a host with both.
		if !strings.HasSuffix(call, ".service") {
			t.Errorf("enable was given something that is not a unit: %q", call)
		}
	}
}

// TestEnsureBootPersistenceIsRepeatable matters because this runs at every
// Agent start. A sweep that did work on a host already in the right state
// would rewrite the init configuration on every restart.
func TestEnsureBootPersistenceIsRepeatable(t *testing.T) {
	runner, calls := bootStub(t, "enabled")
	provider := NewProvider(runner)

	report := provider.EnsureBootPersistence(context.Background(), nil, runningProcesses{})

	if len(report.Enabled) != 0 {
		t.Fatalf("a host already correct was changed: %v", report.Enabled)
	}
	for _, call := range calls() {
		if strings.HasPrefix(call, "enable ") {
			t.Errorf("an already-enabled unit was enabled again: %q", call)
		}
	}
}

// TestBootAuditSeparatesUnknownFromNo guards a distinction the Status type
// goes out of its way to keep: systemd not answering is not the same as
// systemd saying no, and acting on the first would enable units on a guess.
func TestBootAuditSeparatesUnknownFromNo(t *testing.T) {
	// "static" units have no enablement to report, which is exactly the case
	// where the panel must not conclude "disabled".
	runner, calls := bootStub(t, "static")
	provider := NewProvider(runner)

	report := provider.EnsureBootPersistence(context.Background(), nil, runningProcesses{})

	for _, call := range calls() {
		if strings.HasPrefix(call, "enable ") {
			t.Errorf("a unit whose state was not 'disabled' was enabled anyway: %q", call)
		}
	}
	if len(report.Enabled) != 0 {
		t.Errorf("reported enabling something on an unknown state: %v", report.Enabled)
	}
}

// TestBootAuditReportsAHostWithNoInitSystemOnce.
//
// On a machine with nothing supervising it, every running daemon is at risk
// for the same single reason. Reporting it per service would bury the one fact
// that matters under a dozen copies of itself.
func TestBootAuditReportsAHostWithNoInitSystemOnce(t *testing.T) {
	// A provider with no working systemctl and no rc-service.
	provider := NewProvider(nil)
	if provider.Available() {
		t.Skip("this host has a service manager, so the no-init path cannot be exercised")
	}

	report := provider.BootAudit(context.Background(), nil, runningProcesses{})

	if report.Unmanaged == 0 {
		t.Fatal("a host with no init system must report its running services as unmanaged")
	}
	for _, finding := range report.AtRisk() {
		if finding.Reason == "" {
			t.Errorf("%s is at risk with no reason given", finding.Key)
		}
	}

	// And the sweep must not claim to have fixed anything it could not.
	fixed := provider.EnsureBootPersistence(context.Background(), nil, runningProcesses{})
	if len(fixed.Enabled) != 0 {
		t.Errorf("claimed to enable services with no service manager: %v", fixed.Enabled)
	}
}
