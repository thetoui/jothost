//go:build linux

package firewall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/command"
)

// These tests drive the change protocol against a scripted ufw.
//
// What the script is for: the protocol is a state machine — back up, apply,
// verify, arm, confirm or expire — and each branch needs a ufw that answers in
// a particular way, including ways a real one is hard to produce on demand. The
// script is told what to report; the test asserts on what the *protocol* did
// with that answer, and on the files it left behind.
//
// What it is deliberately not: a model of ufw's behaviour. The status output it
// prints is real output, and the parser that reads it is tested separately
// against more of the same. Whether ufw actually blocks a packet is proved by
// the integration suite, which drives the real thing.

// scriptedUFW writes a stub ufw and returns a provider using it, the directory
// holding its scripted answers, and a function returning the calls it saw.
func scriptedUFW(t *testing.T) (*Provider, string, func() []string) {
	t.Helper()

	dir := t.TempDir()
	configDir := filepath.Join(dir, "etc-ufw")
	stateDir := filepath.Join(dir, "state")
	answers := filepath.Join(dir, "answers")
	for _, path := range []string{configDir, stateDir, answers} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	// The files ufw keeps its state in. Backup and restore copy these, so their
	// contents are how the test tells one snapshot from another.
	writeFile(t, filepath.Join(configDir, "user.rules"), "# rules v1\n")
	writeFile(t, filepath.Join(configDir, "user6.rules"), "# rules6 v1\n")
	writeFile(t, filepath.Join(configDir, "ufw.conf"), "ENABLED=yes\n")

	writeFile(t, filepath.Join(answers, "verbose"),
		"Status: active\nDefault: deny (incoming), allow (outgoing), deny (routed)\n")
	writeFile(t, filepath.Join(answers, "numbered"),
		"Status: active\n\n[ 1] 22/tcp                     ALLOW IN    Anywhere\n"+
			"[ 2] 80/tcp                     ALLOW IN    Anywhere\n"+
			"[ 3] 443/tcp                    ALLOW IN    Anywhere\n")

	log := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "ufw")
	writeFile(t, stub, `#!/bin/sh
printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "status verbose") cat `+answers+`/verbose ;;
  "status numbered") cat `+answers+`/numbered ;;
esac
if [ -f `+answers+`/fail ]; then
  echo "ERROR: scripted failure" >&2
  exit 1
fi
exit 0
`)
	if err := os.Chmod(stub, 0o700); err != nil {
		t.Fatalf("chmod stub: %v", err)
	}

	runner, err := command.NewRunner(command.Spec{Name: CommandName, Path: stub})
	if err != nil {
		t.Fatalf("build runner: %v", err)
	}

	provider := NewProvider(Options{
		Runner:    runner,
		StateDir:  stateDir,
		ConfigDir: configDir,
	})

	return provider, answers, func() []string {
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

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

// CLAUDE.md section 19, steps 1 to 3: the rules are backed up before anything
// is applied, and the change is applied provisionally.
func TestApplyBacksUpBeforeChangingAnything(t *testing.T) {
	provider, _, calls := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if pending.Backup == "" {
		t.Fatal("no backup was recorded")
	}
	if got := readFile(t, filepath.Join(pending.Backup, "user.rules")); got != "# rules v1\n" {
		t.Errorf("the backup does not hold the rules as they were: %q", got)
	}
	// ufw.conf carries ENABLED, which is how a rollback of "enable" knows to
	// switch it off again.
	if got := readFile(t, filepath.Join(pending.Backup, "ufw.conf")); !strings.Contains(got, "ENABLED=yes") {
		t.Errorf("the backup does not hold whether the firewall was on: %q", got)
	}

	// And the rule was actually applied.
	seen := strings.Join(calls(), "\n")
	if !strings.Contains(seen, "allow 3306/tcp") {
		t.Errorf("the rule was not applied:\n%s", seen)
	}
}

// Step 6: a change nobody confirms is undone, without being asked.
func TestAnUnconfirmedChangeIsUndoneWhenTheWindowCloses(t *testing.T) {
	provider, _, calls := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, 150*time.Millisecond, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The rules change under the firewall while the window is open, standing in
	// for whatever else the change did.
	writeFile(t, filepath.Join(provider.ufwDir(), "user.rules"), "# rules v2\n")

	deadline := time.Now().Add(3 * time.Second)
	for provider.PendingChange() != nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if provider.PendingChange() != nil {
		t.Fatal("the change is still pending after its window closed")
	}
	// The rules the host had before are back.
	if got := readFile(t, filepath.Join(provider.ufwDir(), "user.rules")); got != "# rules v1\n" {
		t.Errorf("the previous rules were not restored: %q", got)
	}
	// And ufw was told to re-read them: files on disk are not enforced rules.
	seen := strings.Join(calls(), "\n")
	if !strings.Contains(seen, "--force enable") && !strings.Contains(seen, "--force disable") {
		t.Errorf("the firewall was not reloaded after the restore:\n%s", seen)
	}
	_ = pending
}

// Step 5: confirming inside the window commits, and nothing is undone.
func TestConfirmingKeepsTheChange(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, 2*time.Second, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	writeFile(t, filepath.Join(provider.ufwDir(), "user.rules"), "# rules v2\n")

	if _, err := provider.Confirm(pending.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if provider.PendingChange() != nil {
		t.Fatal("the change is still pending after being confirmed")
	}

	// Long enough that the timer would have fired had it not been stopped.
	time.Sleep(400 * time.Millisecond)
	if got := readFile(t, filepath.Join(provider.ufwDir(), "user.rules")); got != "# rules v2\n" {
		t.Errorf("a confirmed change was undone anyway: %q", got)
	}
}

// A stale identifier must not confirm the change that happens to be pending
// now: that would be confirming something the caller has not seen.
func TestConfirmRefusesAnIdentifierItDoesNotHold(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	if _, err := provider.Confirm("fwc_nothing"); !errors.Is(err, ErrNoPendingChange) {
		t.Errorf("confirming with nothing pending = %v", err)
	}

	if _, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, err := provider.Confirm("fwc_somethingelse"); !errors.Is(err, ErrNoPendingChange) {
		t.Errorf("confirming with a stale id = %v, want ErrNoPendingChange", err)
	}
	if provider.PendingChange() == nil {
		t.Error("the real change was cleared by a confirmation naming another")
	}
}

// One change at a time: two overlapping windows would each hold a backup of a
// state the other had already moved away from.
func TestOnlyOneChangeMayBeInFlight(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	rule := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"}
	if _, err := provider.Apply(ctx, Change{Kind: ChangeAddRule, Rule: rule}, time.Minute, ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	rule.Port = "5432"
	_, err := provider.Apply(ctx, Change{Kind: ChangeAddRule, Rule: rule}, time.Minute, "")
	if !errors.Is(err, ErrChangeInFlight) {
		t.Fatalf("a second change = %v, want ErrChangeInFlight", err)
	}
}

// Step 4, the half the Agent can perform: if the rules ufw ends up with no
// longer allow a guarded port, the change is undone immediately rather than
// waiting for the window.
func TestAChangeThatClosesAGuardedPortIsUndoneAtOnce(t *testing.T) {
	provider, answers, _ := scriptedUFW(t)
	ctx := context.Background()

	// After this change, ufw reports a ruleset with nothing allowing SSH.
	writeFile(t, filepath.Join(answers, "numbered"),
		"Status: active\n\n[ 1] 80/tcp                     ALLOW IN    Anywhere\n")

	_, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test")
	if !errors.Is(err, ErrWouldLockOut) {
		t.Fatalf("Apply = %v, want ErrWouldLockOut", err)
	}
	if provider.PendingChange() != nil {
		t.Error("a change that failed verification was left pending")
	}
	if got := readFile(t, filepath.Join(provider.ufwDir(), "user.rules")); got != "# rules v1\n" {
		t.Errorf("the previous rules were not restored: %q", got)
	}
}

// The timer lives in memory. A restart during the window would otherwise leave
// a provisional change standing forever — the one failure the protocol exists
// to prevent, arriving by the back door.
func TestARestartUndoesAChangeWhoseWindowHasClosed(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	writeFile(t, filepath.Join(provider.ufwDir(), "user.rules"), "# rules v2\n")

	// A new provider over the same directories is an Agent that has restarted,
	// with the marker on disk and no timer in memory. The marker is rewritten
	// with a deadline in the past, which is what a restart after the window
	// would find.
	expired := pending
	expired.Deadline = time.Now().Add(-time.Second)
	restarted := NewProvider(Options{
		Runner:    provider.runner,
		StateDir:  provider.stateDir,
		ConfigDir: provider.configDir,
	})
	if err := restarted.writePending(expired); err != nil {
		t.Fatalf("write the marker: %v", err)
	}

	if err := restarted.RecoverPending(ctx); err != nil {
		t.Fatalf("RecoverPending: %v", err)
	}

	if got := readFile(t, filepath.Join(provider.ufwDir(), "user.rules")); got != "# rules v1\n" {
		t.Errorf("a change abandoned by a restart was not undone: %q", got)
	}
	if restarted.PendingChange() != nil {
		t.Error("the marker was not cleared after the rollback")
	}
}

// A restart *inside* the window re-arms rather than rolling back: the operator
// may still be about to confirm.
func TestARestartInsideTheWindowKeepsWaiting(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	restarted := NewProvider(Options{
		Runner:    provider.runner,
		StateDir:  provider.stateDir,
		ConfigDir: provider.configDir,
	})
	if err := restarted.RecoverPending(ctx); err != nil {
		t.Fatalf("RecoverPending: %v", err)
	}

	recovered := restarted.PendingChange()
	if recovered == nil {
		t.Fatal("the pending change was forgotten across a restart")
	}
	if recovered.ID != pending.ID {
		t.Errorf("recovered %q, want %q", recovered.ID, pending.ID)
	}
	// And it is still confirmable, which is the point of re-arming.
	if _, err := restarted.Confirm(pending.ID); err != nil {
		t.Errorf("Confirm after a restart: %v", err)
	}
}

// Rolling back on request is the same path as the timer, without the wait.
func TestRollbackUndoesTheChangeImmediately(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	pending, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	writeFile(t, filepath.Join(provider.ufwDir(), "user.rules"), "# rules v2\n")

	if _, err := provider.Rollback(ctx, pending.ID); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readFile(t, filepath.Join(provider.ufwDir(), "user.rules")); got != "# rules v1\n" {
		t.Errorf("the rules were not restored: %q", got)
	}
	if provider.PendingChange() != nil {
		t.Error("the change is still pending after a rollback")
	}
}

// A backup is never written over: each change gets its own, so the state
// before any of them is still recoverable (CLAUDE.md section 18).
func TestEachChangeKeepsItsOwnBackup(t *testing.T) {
	provider, _, _ := scriptedUFW(t)
	ctx := context.Background()

	rule := Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"}
	first, err := provider.Apply(ctx, Change{Kind: ChangeAddRule, Rule: rule}, time.Minute, "")
	if err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if _, err := provider.Confirm(first.ID); err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	writeFile(t, filepath.Join(provider.ufwDir(), "user.rules"), "# rules v2\n")

	rule.Port = "5432"
	second, err := provider.Apply(ctx, Change{Kind: ChangeAddRule, Rule: rule}, time.Minute, "")
	if err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	if first.Backup == second.Backup {
		t.Fatal("the second change wrote over the first change's backup")
	}
	if got := readFile(t, filepath.Join(first.Backup, "user.rules")); got != "# rules v1\n" {
		t.Errorf("the first backup was modified: %q", got)
	}
	if got := readFile(t, filepath.Join(second.Backup, "user.rules")); got != "# rules v2\n" {
		t.Errorf("the second backup does not hold what was there: %q", got)
	}
	if len(provider.Backups()) != 2 {
		t.Errorf("expected two backups, got %v", provider.Backups())
	}
}

// A rule the guard refuses never reaches ufw at all.
func TestAGuardedChangeIsNeverApplied(t *testing.T) {
	provider, _, calls := scriptedUFW(t)
	ctx := context.Background()

	_, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "deny", Direction: "in", Protocol: "tcp", Port: "22", Source: "any"},
	}, time.Minute, "")
	if !errors.Is(err, ErrWouldLockOut) {
		t.Fatalf("Apply = %v, want ErrWouldLockOut", err)
	}

	for _, call := range calls() {
		if strings.Contains(call, "deny") {
			t.Errorf("the refused rule reached ufw: %q", call)
		}
	}
	if provider.PendingChange() != nil {
		t.Error("a refused change was left pending")
	}
}

// "inactive" contains "active".
//
// A substring test reports a disabled firewall as enabled, and every guard
// downstream then reasons about a ruleset the host is not enforcing. This was a
// live bug: the first request made against a real ufw hit it, because the
// firewall happened to be switched off and every canned fixture said "active".
func TestStatusDistinguishesInactiveFromActive(t *testing.T) {
	provider, answers, _ := scriptedUFW(t)
	ctx := context.Background()

	// What ufw prints when it is off: one line, and no Default line at all.
	writeFile(t, filepath.Join(answers, "verbose"), "Status: inactive\n")
	writeFile(t, filepath.Join(answers, "numbered"), "Status: inactive\n")

	status, err := provider.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Enabled {
		t.Error("a disabled firewall was reported as enabled")
	}
	if !status.Available {
		t.Error("ufw answered, so it is available even while switched off")
	}

	writeFile(t, filepath.Join(answers, "verbose"),
		"Status: active\nDefault: deny (incoming), allow (outgoing), deny (routed)\n")
	status, err = provider.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Enabled {
		t.Error("an active firewall was reported as disabled")
	}
	if status.DefaultIncoming != "deny" || status.DefaultOutgoing != "allow" {
		t.Errorf("policies = %q in, %q out", status.DefaultIncoming, status.DefaultOutgoing)
	}
}

// A rule may be added to a firewall that is switched off: it filters nothing
// until it is switched on, and the guard that matters then is the one on
// enabling.
func TestARuleMayBeAddedWhileTheFirewallIsOff(t *testing.T) {
	provider, answers, calls := scriptedUFW(t)
	ctx := context.Background()

	writeFile(t, filepath.Join(answers, "verbose"), "Status: inactive\n")
	writeFile(t, filepath.Join(answers, "numbered"), "Status: inactive\n")

	if _, err := provider.Apply(ctx, Change{
		Kind: ChangeAddRule,
		Rule: Rule{Action: "allow", Direction: "in", Protocol: "tcp", Port: "3306", Source: "any"},
	}, time.Minute, "req_test"); err != nil {
		t.Fatalf("Apply on a disabled firewall: %v", err)
	}

	if !strings.Contains(strings.Join(calls(), "\n"), "allow 3306/tcp") {
		t.Error("the rule was not applied")
	}
}
