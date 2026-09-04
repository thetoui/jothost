package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeScript creates an executable shell script for a test to run.
func writeScript(t *testing.T, name, body string) string {
	t.Helper()

	if runtime.GOOS == "windows" {
		t.Skip("command execution tests require a Unix host")
	}

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func newRunner(t *testing.T, specs ...Spec) *Runner {
	t.Helper()

	runner, err := NewRunner(specs...)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	return runner
}

// ------------------------------------------------------------- allowlist

func TestRunRejectsUnallowlistedCommand(t *testing.T) {
	runner := newRunner(t, Spec{Name: "echo", Path: "/bin/echo"})

	// The allowlist is the whole security model: anything not named in it must
	// be unreachable, whatever the caller passes.
	for _, name := range []string{"sh", "bash", "rm", "systemctl", "", "ECHO", "../bin/sh"} {
		if _, err := runner.Run(context.Background(), name, "x"); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("command %q must be refused, got %v", name, err)
		}
	}
}

func TestNewRunnerRequiresAbsolutePaths(t *testing.T) {
	// A relative path would be resolved through PATH, letting a hostile PATH
	// entry substitute the program.
	if _, err := NewRunner(Spec{Name: "echo", Path: "echo"}); !errors.Is(err, ErrNotAbsolute) {
		t.Fatalf("expected ErrNotAbsolute, got %v", err)
	}
	if _, err := NewRunner(Spec{Name: "echo", Path: "./echo"}); !errors.Is(err, ErrNotAbsolute) {
		t.Fatalf("expected ErrNotAbsolute, got %v", err)
	}
}

func TestNewRunnerRejectsBadSpecs(t *testing.T) {
	if _, err := NewRunner(); err == nil {
		t.Fatal("a runner with no specs must be rejected")
	}
	if _, err := NewRunner(Spec{Path: "/bin/echo"}); err == nil {
		t.Fatal("a spec without a name must be rejected")
	}
	if _, err := NewRunner(
		Spec{Name: "echo", Path: "/bin/echo"},
		Spec{Name: "echo", Path: "/usr/bin/echo"},
	); err == nil {
		t.Fatal("duplicate spec names must be rejected")
	}
}

// -------------------------------------------------------- no shell, ever

func TestArgumentsAreNotInterpretedByAShell(t *testing.T) {
	// The canary file must still exist afterwards: if any argument were ever
	// passed through a shell, "; rm -f canary" would delete it.
	dir := t.TempDir()
	canary := filepath.Join(dir, "canary")
	if err := os.WriteFile(canary, []byte("intact"), 0o600); err != nil {
		t.Fatalf("write canary: %v", err)
	}

	script := writeScript(t, "echoargs", `printf '%s\n' "$@"`)
	runner := newRunner(t, Spec{Name: "echoargs", Path: script})

	hostile := []string{
		"; rm -f " + canary,
		"&& rm -f " + canary,
		"| rm -f " + canary,
		"$(rm -f " + canary + ")",
		"`rm -f " + canary + "`",
		"../../etc/passwd",
		"--dangerous-flag",
	}

	result, err := runner.Run(context.Background(), "echoargs", hostile...)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Succeeded() {
		t.Fatalf("expected success, got %+v", result)
	}

	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("the canary was deleted: an argument reached a shell (%v)", err)
	}

	// Each hostile string must come back verbatim as one argument, proving it
	// was data rather than syntax.
	for _, arg := range hostile {
		if !strings.Contains(result.Stdout, arg) {
			t.Fatalf("argument %q was not passed through literally: %q", arg, result.Stdout)
		}
	}
}

func TestArgumentValidation(t *testing.T) {
	script := writeScript(t, "noop", "exit 0")
	runner := newRunner(t, Spec{Name: "noop", Path: script})

	// A null byte truncates the argument inside the kernel; a newline forges
	// log lines. Both are refused regardless of the absent shell.
	if _, err := runner.Run(context.Background(), "noop", "arg\x00hidden"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("a null byte must be rejected, got %v", err)
	}
	if _, err := runner.Run(context.Background(), "noop", "arg\nforged"); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("a newline must be rejected, got %v", err)
	}

	tooMany := make([]string, maxArgs+1)
	for i := range tooMany {
		tooMany[i] = "x"
	}
	if _, err := runner.Run(context.Background(), "noop", tooMany...); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("an oversized argv must be rejected, got %v", err)
	}

	if _, err := runner.Run(context.Background(), "noop", strings.Repeat("a", maxArgLength+1)); !errors.Is(err, ErrInvalidArg) {
		t.Fatalf("an oversized argument must be rejected, got %v", err)
	}
}

// ------------------------------------------------------------ boundaries

func TestTimeoutIsEnforced(t *testing.T) {
	script := writeScript(t, "slow", "sleep 10")
	runner := newRunner(t, Spec{Name: "slow", Path: script, Timeout: 200 * time.Millisecond})

	start := time.Now()
	result, err := runner.Run(context.Background(), "slow")

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("expected ErrTimeout, got %v", err)
	}
	if !result.TimedOut {
		t.Fatal("the result must record the timeout")
	}
	// A command that outlives its deadline would pin an Agent worker forever.
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("the timeout was not enforced: took %v", elapsed)
	}
}

func TestParentContextCancellationStopsTheCommand(t *testing.T) {
	script := writeScript(t, "slow", "sleep 10")
	runner := newRunner(t, Spec{Name: "slow", Path: script, Timeout: 30 * time.Second})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := runner.Run(ctx, "slow"); err == nil {
		t.Fatal("expected the cancelled context to stop the command")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation was not honoured: took %v", elapsed)
	}
}

func TestOutputIsCapped(t *testing.T) {
	// A program emitting unbounded output would otherwise exhaust the Agent's
	// memory one buffer append at a time.
	script := writeScript(t, "flood", `head -c 100000 /dev/zero | tr '\0' 'a'`)
	runner := newRunner(t, Spec{Name: "flood", Path: script, MaxOutput: 1024})

	result, err := runner.Run(context.Background(), "flood")
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got %v", err)
	}
	if len(result.Stdout) > 1024 {
		t.Fatalf("output was not capped: got %d bytes", len(result.Stdout))
	}
}

func TestNonZeroExitIsAnOutcomeNotAnError(t *testing.T) {
	script := writeScript(t, "fail", "exit 3")
	runner := newRunner(t, Spec{Name: "fail", Path: script})

	result, err := runner.Run(context.Background(), "fail")
	// The caller decides whether a non-zero exit matters; systemctl uses exit
	// status to report "inactive", which is an answer rather than a failure.
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error: %v", err)
	}
	if result.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", result.ExitCode)
	}
	if result.Succeeded() {
		t.Fatal("Succeeded must be false for a non-zero exit")
	}
}

func TestMissingBinaryIsReportedAsUnavailable(t *testing.T) {
	runner := newRunner(t, Spec{Name: "ghost", Path: filepath.Join(t.TempDir(), "ghost")})

	if runner.Available("ghost") {
		t.Fatal("a missing binary must not report available")
	}
	if _, err := runner.Run(context.Background(), "ghost"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestAvailableRequiresAnExecutableRegularFile(t *testing.T) {
	dir := t.TempDir()

	notExecutable := filepath.Join(dir, "data")
	if err := os.WriteFile(notExecutable, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	runner := newRunner(t,
		Spec{Name: "data", Path: notExecutable},
		Spec{Name: "dir", Path: dir},
	)

	if runner.Available("data") {
		t.Fatal("a non-executable file must not report available")
	}
	if runner.Available("dir") {
		t.Fatal("a directory must not report available")
	}
	if runner.Available("unknown") {
		t.Fatal("an unallowlisted name must not report available")
	}
}

// ----------------------------------------------------------- environment

func TestChildEnvironmentIsSanitised(t *testing.T) {
	// The Agent runs as root and its environment may hold credentials, so the
	// child gets a fixed environment rather than inheriting the parent's.
	t.Setenv("JOTHOST_SECRET", "super-secret-value")

	script := writeScript(t, "env", "env")
	runner := newRunner(t, Spec{Name: "env", Path: script})

	result, err := runner.Run(context.Background(), "env")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if strings.Contains(result.Stdout, "super-secret-value") {
		t.Fatalf("the parent environment leaked to the child: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "LC_ALL=C") {
		t.Fatalf("a stable locale must be pinned: %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "PATH=") {
		t.Fatalf("PATH must be pinned explicitly: %q", result.Stdout)
	}
}

func TestPathIsNotSearched(t *testing.T) {
	// A hostile PATH entry must not be able to substitute the program: the
	// spec names an absolute path and that is what runs.
	dir := t.TempDir()
	impostor := filepath.Join(dir, "target")
	if err := os.WriteFile(impostor, []byte("#!/bin/sh\necho IMPOSTOR\n"), 0o700); err != nil {
		t.Fatalf("write impostor: %v", err)
	}
	t.Setenv("PATH", dir)

	genuine := writeScript(t, "target", "echo GENUINE")
	runner := newRunner(t, Spec{Name: "target", Path: genuine})

	result, err := runner.Run(context.Background(), "target")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(result.Stdout, "GENUINE") {
		t.Fatalf("the allowlisted binary must run, got %q", result.Stdout)
	}
}

func TestNamesListsTheAllowlist(t *testing.T) {
	runner := newRunner(t,
		Spec{Name: "a", Path: "/bin/true"},
		Spec{Name: "b", Path: "/bin/false"},
	)

	names := runner.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %v", names)
	}
}

func TestCappedBufferReportsFullWrites(t *testing.T) {
	// Short writes would block the child process, so the buffer must claim to
	// have consumed everything even while discarding the excess.
	buf := &cappedBuffer{limit: 4}

	n, err := buf.Write([]byte("abcdefgh"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != 8 {
		t.Fatalf("Write reported %d, want 8", n)
	}
	if buf.String() != "abcd" {
		t.Fatalf("buffer = %q, want abcd", buf.String())
	}
	if !buf.truncated {
		t.Fatal("truncation must be recorded")
	}

	n, err = buf.Write([]byte("more"))
	if err != nil || n != 4 {
		t.Fatalf("a full buffer must still accept writes: n=%d err=%v", n, err)
	}
}

func TestAProgramThatLeavesAChildHoldingItsOutputStillReportsItsExitStatus(t *testing.T) {
	// A real failure, found by starting Dovecot through the panel: an init
	// script exits cleanly and the daemon it started inherits stdout, so the
	// output pipe never closes and Wait gives up after WaitDelay. Reporting
	// that as a failure showed the operator an error for an action that had
	// worked.
	//
	// What is lost is trailing output from a process that is no longer the one
	// being run. The exit status is real.
	script := filepath.Join(t.TempDir(), "leaky.sh")
	body := "#!/bin/sh\nsleep 30 &\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}

	runner, err := NewRunner(Spec{Name: "leaky", Path: "/bin/sh", Timeout: 10 * time.Second})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	result, err := runner.Run(context.Background(), "leaky", script)
	if err != nil {
		t.Fatalf("a program whose child held the pipe was reported as failing: %v", err)
	}
	if !result.Succeeded() {
		t.Errorf("exit code %d, want 0", result.ExitCode)
	}
}
