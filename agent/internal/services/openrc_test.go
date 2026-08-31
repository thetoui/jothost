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

// The OpenRC backend, tested the same way as the systemd one: against a stub
// that records its arguments, with output copied from a live Alpine host rather
// than invented.
//
// The output matters more here than it does for systemd. systemd answers `show`
// with key=value pairs that are hard to misread; OpenRC answers `status` with a
// sentence — " * status: started" — whose vocabulary is not the vocabulary the
// panel shows. Every word it can print is checked below, because the mapping
// from its words to the panel's is the whole of this file's risk.

// recordingOpenRC writes stubs for rc-service and rc-update and returns a
// Provider driving them, plus a function returning the calls they saw.
//
// state is what `rc-service NAME status` will report.
func recordingOpenRC(t *testing.T, state string) (*Provider, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")

	// An init directory with one script in it, because the provider asks the
	// filesystem whether a service exists before running anything.
	initDir := filepath.Join(dir, "init.d")
	runlevelDir := filepath.Join(dir, "runlevels")
	if err := os.MkdirAll(filepath.Join(runlevelDir, openrcRunlevel), 0o755); err != nil {
		t.Fatalf("make runlevel dir: %v", err)
	}
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("make init dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(initDir, "nginx"), []byte("#!/sbin/openrc-run\n"), 0o755); err != nil {
		t.Fatalf("write init script: %v", err)
	}

	rcService := filepath.Join(dir, "rc-service")
	script := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
case "$2" in
  status) printf ' * status: %s\n' ` + state + ` ;;
esac
exit 0
`
	if err := os.WriteFile(rcService, []byte(script), 0o700); err != nil {
		t.Fatalf("write rc-service stub: %v", err)
	}

	rcUpdate := filepath.Join(dir, "rc-update")
	update := `#!/bin/sh
printf '%s\n' "$*" >> ` + log + `
printf ' * service %s added to runlevel %s\n' "$2" "$3"
exit 0
`
	if err := os.WriteFile(rcUpdate, []byte(update), 0o700); err != nil {
		t.Fatalf("write rc-update stub: %v", err)
	}

	runner, err := command.NewRunner(
		command.Spec{Name: CommandRCService, Path: rcService},
		command.Spec{Name: CommandRCUpdate, Path: rcUpdate},
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	provider := NewProvider(runner)
	provider.openrcInitDir = initDir
	provider.openrcRunlevelDir = runlevelDir

	calls := func() []string {
		data, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return provider, calls
}

func TestOpenRCIsUsedWhenSystemdIsAbsent(t *testing.T) {
	provider, _ := recordingOpenRC(t, "started")

	if provider.Manager() != ManagerOpenRC {
		t.Fatalf("manager = %q, want %q", provider.Manager(), ManagerOpenRC)
	}
	if !provider.Available() {
		t.Fatal("a host with OpenRC and no systemd reports no service manager")
	}
}

func TestOpenRCStatusMapsItsVocabulary(t *testing.T) {
	// The left column is what OpenRC prints; the right is what the panel shows.
	// "stopped" and "inactive" are different words for different things in
	// OpenRC — one has not been started, the other started and did not finish —
	// and both are "not running" to a reader.
	cases := []struct {
		state       string
		activeState string
		subState    string
		running     bool
	}{
		{"started", "active", "running", true},
		{"stopped", "inactive", "dead", false},
		{"starting", "activating", "starting", false},
		{"crashed", "failed", "crashed", false},
		{"inactive", "inactive", "inactive", false},
	}

	for _, tc := range cases {
		t.Run(tc.state, func(t *testing.T) {
			provider, calls := recordingOpenRC(t, tc.state)

			status, err := provider.Status(context.Background(), "nginx.service")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if status.Running != tc.running {
				t.Fatalf("running = %v, want %v", status.Running, tc.running)
			}
			if status.ActiveState != tc.activeState {
				t.Fatalf("active state = %q, want %q", status.ActiveState, tc.activeState)
			}
			if status.SubState != tc.subState {
				t.Fatalf("sub state = %q, want %q", status.SubState, tc.subState)
			}

			// The ".service" suffix is systemd's and must not reach OpenRC.
			if got := calls(); len(got) != 1 || got[0] != "nginx status" {
				t.Fatalf("calls = %q, want [\"nginx status\"]", got)
			}
		})
	}
}

func TestOpenRCReportsAnAbsentServiceAsNotFound(t *testing.T) {
	provider, calls := recordingOpenRC(t, "started")

	// No init script, so this host does not have it.
	if _, err := provider.Status(context.Background(), "postgresql.service"); err == nil {
		t.Fatal("a service with no init script was reported as present")
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("calls = %q, want none: the filesystem answers this", got)
	}
}

func TestOpenRCControlPassesTheVerbAfterTheName(t *testing.T) {
	// rc-service takes the service first and the verb second, which is the
	// opposite of systemctl. Getting it the wrong way round produces a command
	// that runs and does nothing.
	for _, verb := range []string{"start", "stop", "restart"} {
		t.Run(verb, func(t *testing.T) {
			provider, calls := recordingOpenRC(t, "started")

			var err error
			switch verb {
			case "start":
				err = provider.Start(context.Background(), "nginx.service")
			case "stop":
				err = provider.Stop(context.Background(), "nginx.service")
			case "restart":
				err = provider.Restart(context.Background(), "nginx.service")
			}
			if err != nil {
				t.Fatalf("%s: %v", verb, err)
			}

			want := "nginx " + verb
			if got := calls(); len(got) != 1 || got[0] != want {
				t.Fatalf("calls = %q, want [%q]", got, want)
			}
		})
	}
}

func TestOpenRCBootStateUsesTheDefaultRunlevel(t *testing.T) {
	provider, calls := recordingOpenRC(t, "started")

	if err := provider.Enable(context.Background(), "nginx.service"); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := provider.Disable(context.Background(), "nginx.service"); err != nil {
		t.Fatalf("Disable: %v", err)
	}

	want := []string{"add nginx default", "del nginx default"}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestOpenRCReadsEnabledFromTheRunlevel(t *testing.T) {
	provider, _ := recordingOpenRC(t, "started")

	status, err := provider.Status(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Enabled == nil || *status.Enabled {
		t.Fatal("a service with no runlevel symlink was reported as enabled")
	}

	link := filepath.Join(provider.runlevelDir(), openrcRunlevel, "nginx")
	if err := os.Symlink(filepath.Join(provider.initDir(), "nginx"), link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	status, err = provider.Status(context.Background(), "nginx.service")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Enabled == nil || !*status.Enabled {
		t.Fatal("a service in the default runlevel was reported as disabled")
	}
}

func TestOpenRCTreatsAZeroExitWithAnErrorLineAsFailure(t *testing.T) {
	// This is not hypothetical. OpenRC prints " * ERROR: nginx failed to stop"
	// and still exits zero, so a caller reading only the exit code would report
	// success for a service that is still running.
	dir := t.TempDir()
	initDir := filepath.Join(dir, "init.d")
	if err := os.MkdirAll(initDir, 0o755); err != nil {
		t.Fatalf("make init dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(initDir, "nginx"), []byte("#!/sbin/openrc-run\n"), 0o755); err != nil {
		t.Fatalf("write init script: %v", err)
	}

	stub := filepath.Join(dir, "rc-service")
	script := `#!/bin/sh
printf '%s\n' " * Stopping nginx ..."
printf '%s\n' " * start-stop-daemon: 1 process refused to stop"
printf '%s\n' " * ERROR: nginx failed to stop"
exit 0
`
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	update := filepath.Join(dir, "rc-update")
	if err := os.WriteFile(update, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("write rc-update stub: %v", err)
	}

	runner, err := command.NewRunner(
		command.Spec{Name: CommandRCService, Path: stub},
		command.Spec{Name: CommandRCUpdate, Path: update},
	)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	provider := NewProvider(runner)
	provider.openrcInitDir = initDir
	provider.openrcRunlevelDir = filepath.Join(dir, "runlevels")

	err = provider.Stop(context.Background(), "nginx.service")
	if err == nil {
		t.Fatal("a stop that printed ERROR and exited zero was reported as success")
	}
	// The message an operator reads is OpenRC's own sentence, not a wrapper.
	if !strings.Contains(err.Error(), "nginx failed to stop") {
		t.Fatalf("error = %q, want OpenRC's own message", err)
	}
}

func TestOpenRCNameIsValidatedBeforeExecution(t *testing.T) {
	provider, calls := recordingOpenRC(t, "started")

	for _, name := range []string{"nginx; rm -rf /", "../../etc/init.d/nginx", "nginx nginx"} {
		if err := provider.Start(context.Background(), name); err == nil {
			t.Fatalf("%q was accepted as a service name", name)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("calls = %q, want none: rejection happens before execution", got)
	}
}
