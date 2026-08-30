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

// These tests run the real command runner against a recording stub, rather
// than faking systemd.
//
// The distinction matters. Whether `systemctl start nginx.service` starts nginx
// is systemd's business and needs no proof from this repository. What does need
// proving is the part written here: that a request naming a catalogue key
// produces exactly that command line and no other — that a key never leaks into
// argv, that the verb is one of five constants, and that the unit is the one
// the Agent chose.
//
// A stub that imitated systemd's behaviour would test my idea of systemd. A
// stub that records its arguments tests my code.

// recordingSystemctl writes a stub at a temporary path and returns a runner
// that treats it as systemctl, plus a function returning the calls it saw.
func recordingSystemctl(t *testing.T, unitState string) (*command.Runner, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "systemctl")

	// The stub answers `show` with real systemd property output — copied from
	// a live host, not invented — so the parser is exercised against the shape
	// it actually meets. Everything else it records and exits zero.
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$1" in
  show)
    cat <<PROPS
Id=nginx.service
Description=nginx - high performance web server
LoadState=loaded
ActiveState=` + unitState + `
SubState=running
UnitFileState=enabled
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

// The whole safety of the arrangement: a request carries a key, and the unit
// it becomes is chosen from the Agent's own table.
func TestActionRunsTheUnitTheAgentChose(t *testing.T) {
	runner, calls := recordingSystemctl(t, "active")
	provider := NewProvider(runner)
	ctx := context.Background()

	definition, unit, err := provider.UnitFor(ctx, "nginx", nil)
	if err != nil {
		t.Fatalf("UnitFor: %v", err)
	}
	if definition.Key != "nginx" {
		t.Fatalf("definition = %q, want nginx", definition.Key)
	}
	if unit != "nginx.service" {
		t.Fatalf("unit = %q, want nginx.service", unit)
	}

	if err := provider.Restart(ctx, unit); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	seen := calls()
	last := seen[len(seen)-1]
	if last != "restart nginx.service" {
		t.Fatalf("ran %q, want %q", last, "restart nginx.service")
	}
}

// Each verb has to reach systemctl as itself. A dispatch table that sent the
// wrong one would be invisible in a status page: stop and disable both leave a
// service down.
func TestEveryVerbReachesSystemctlUnchanged(t *testing.T) {
	runner, calls := recordingSystemctl(t, "active")
	provider := NewProvider(runner)
	ctx := context.Background()

	verbs := []struct {
		name string
		run  func() error
	}{
		{"start", func() error { return provider.Start(ctx, "nginx.service") }},
		{"stop", func() error { return provider.Stop(ctx, "nginx.service") }},
		{"restart", func() error { return provider.Restart(ctx, "nginx.service") }},
		{"enable", func() error { return provider.Enable(ctx, "nginx.service") }},
		{"disable", func() error { return provider.Disable(ctx, "nginx.service") }},
	}

	for _, verb := range verbs {
		if err := verb.run(); err != nil {
			t.Fatalf("%s: %v", verb.name, err)
		}
	}

	seen := calls()
	if len(seen) != len(verbs) {
		t.Fatalf("ran %d commands, want %d: %v", len(seen), len(verbs), seen)
	}
	for i, verb := range verbs {
		want := verb.name + " nginx.service"
		if seen[i] != want {
			t.Errorf("call %d = %q, want %q", i, seen[i], want)
		}
	}
}

// systemd's property output is parsed rather than guessed at, and the state
// after an action is read from it.
func TestStatusIsReadFromSystemdsOwnProperties(t *testing.T) {
	runner, _ := recordingSystemctl(t, "active")
	provider := NewProvider(runner)

	status, err := provider.Status(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if !status.Running {
		t.Error("active + running should read as running")
	}
	if status.MainPID != 4242 {
		t.Errorf("pid = %d, want 4242", status.MainPID)
	}
	if status.Enabled == nil || !*status.Enabled {
		t.Error("UnitFileState=enabled should read as enabled")
	}
	if status.Description == "" {
		t.Error("the description systemd gave was dropped")
	}
}

// "activating" is neither up nor down, and a panel that rounds it to "running"
// tells an operator a service is ready when it is still starting.
func TestActivatingIsNotRunning(t *testing.T) {
	runner, _ := recordingSystemctl(t, "activating")
	provider := NewProvider(runner)

	status, err := provider.Status(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Running {
		t.Error("a unit that is still activating must not report as running")
	}
	if status.ActiveState != "activating" {
		t.Errorf("active state = %q, want activating", status.ActiveState)
	}
}

// Detection consults systemd for what only systemd knows, and the process
// table for what it does not.
func TestDetectPrefersSystemdWhereItCanAnswer(t *testing.T) {
	runner, _ := recordingSystemctl(t, "active")
	provider := NewProvider(runner)

	detected := provider.Detect(context.Background(), nil, fakeProcesses{})

	var nginx *Detected
	for i := range detected {
		if detected[i].Key == "nginx" {
			nginx = &detected[i]
			break
		}
	}
	if nginx == nil {
		t.Fatal("nginx was not detected")
	}

	// Nothing in the process table, but systemd says it is up — and systemd is
	// the better answer, because it knows about a unit the panel's process
	// name list could have got wrong.
	if !nginx.Running {
		t.Error("systemd reported the unit active; detection said it is down")
	}
	if nginx.Unit != "nginx.service" {
		t.Errorf("unit = %q, want nginx.service", nginx.Unit)
	}
	if !nginx.Controllable {
		t.Error("a host with systemd can control its services")
	}
	if nginx.Enabled == nil || !*nginx.Enabled {
		t.Error("the unit's boot state was not carried through")
	}
}
