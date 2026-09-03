//go:build linux

package updates

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

// Every sample below was copied from a running package manager: apk 2.14 on
// Alpine 3.21 in this project's own agent container, and apt 2.6 on Debian 12
// in a throwaway container started for the purpose.
//
// That matters more than usual here, because two of these samples are the
// reason this package is shaped the way it is:
//
//   - apk update and apk version both exit 0 when every repository is
//     unreachable, and print an empty list. "Up to date" and "could not check"
//     are indistinguishable by exit status.
//   - a version-pinned package appears in apk version -l '<' forever while apk
//     upgrade correctly refuses to touch it. Listing that as pending would show
//     an operator a queue that never empties.
//
// Both were found by running the commands, not by reading about them.

const (
	// A refresh that reached everything. Note it says nothing about failures:
	// apk only counts those when there are some, so a parser that knew only
	// the degraded shape would find no summary here — which is exactly what
	// happened on this package's first run against a real host.
	apkRefreshOK = "fetch https://dl-cdn.alpinelinux.org/alpine/v3.21/main/x86_64/APKINDEX.tar.gz\n" +
		"v3.21.7-230-gc031f07a9b5 [https://dl-cdn.alpinelinux.org/alpine/v3.21/main]\n" +
		"OK: 25398 distinct packages available\n"

	// The older degraded shape, kept because it is the one that reports a
	// failure and the one a partly-broken host prints.
	apkRefreshPartial = "WARNING: opening from cache https://example.invalid/community: No such file\n" +
		"1 unavailable, 0 stale; 198 distinct packages available\n"

	// A refresh that reached nothing. Note the exit status is still zero.
	apkRefreshUnreachable = "fetch https://dl-cdn.alpinelinux.invalid/alpine/v3.21/main/x86_64/APKINDEX.tar.gz\n" +
		"WARNING: updating and opening https://dl-cdn.alpinelinux.invalid/alpine/v3.21/main: DNS lookup error\n" +
		"2 unavailable, 0 stale; 198 distinct packages available\n"

	// What apk says it will actually do.
	apkSimulateOutput = "(1/4) Upgrading git (2.45.4-r0 -> 2.47.3-r0)\n" +
		"(2/4) Upgrading git-init-template (2.45.4-r0 -> 2.47.3-r0)\n" +
		"(3/4) Upgrading perl-git (2.45.4-r0 -> 2.47.3-r0)\n" +
		"(4/4) Upgrading git-perl (2.45.4-r0 -> 2.47.3-r0)\n" +
		"OK: 479 MiB in 205 packages\n"

	// What has something newer available, which is not the same question.
	apkVersionOutput = "Installed:                                Available:\n" +
		"git-2.45.4-r0                           < 2.47.3-r0 \n" +
		"git-init-template-2.45.4-r0             < 2.47.3-r0 \n" +
		"git-perl-2.45.4-r0                      < 2.47.3-r0 \n" +
		"perl-git-2.45.4-r0                      < 2.47.3-r0 \n"

	// apk policy for a package whose installed version is in no repository.
	apkPolicyOutput = "jq policy:\n" +
		"  1.7.1-r0:\n" +
		"    lib/apk/db/installed\n" +
		"    https://dl-cdn.alpinelinux.org/alpine/v3.21/main\n" +
		"  1.6.0-r0:\n" +
		"    lib/apk/db/installed\n"

	// apt-get -s upgrade on debian:12.5, including a real security line.
	aptSimulateOutput = "Reading package lists...\n" +
		"Building dependency tree...\n" +
		"Calculating upgrade...\n" +
		"41 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n" +
		"Inst base-files [12.4+deb12u5] (12.4+deb12u15 Debian:12.15/oldstable [amd64])\n" +
		"Conf base-files (12.4+deb12u15 Debian:12.15/oldstable [amd64])\n" +
		"Inst libgcrypt20 [1.10.1-3] (1.10.1-3+deb12u1 Debian:12.15/oldstable, " +
		"Debian-Security:12/oldstable-security [amd64])\n" +
		"Conf libgcrypt20 (1.10.1-3+deb12u1 Debian:12.15/oldstable, " +
		"Debian-Security:12/oldstable-security [amd64])\n"

	// apt-cache policy for one package.
	aptPolicyOutput = "libgcrypt20:\n" +
		"  Installed: 1.10.1-3\n" +
		"  Candidate: 1.10.1-3+deb12u1\n" +
		"  Version table:\n" +
		"     1.10.1-3+deb12u1 500\n" +
		"        500 http://deb.debian.org/debian-security bookworm-security/main amd64 Packages\n" +
		" *** 1.10.1-3 100\n" +
		"        100 /var/lib/dpkg/status\n"
)

// recording writes stubs for the package managers and returns a Provider using
// them, plus a function returning the calls they saw.
func recording(t *testing.T, manager string, scripts map[string]string) (*Provider, func() []string) {
	t.Helper()

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tools := []string{CommandAPK}
	if manager == ManagerAPT {
		tools = []string{CommandAPT, CommandAPTCache, CommandAPTMark, CommandDpkgQuery}
	}

	specs := make([]command.Spec, 0, len(tools))
	for _, name := range tools {
		stub := filepath.Join(binDir, name)
		body := "#!/bin/sh\nprintf '%s %s\\n' " + name + " \"$*\" >> " + callLog + "\n"
		body += scripts[name]
		if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
			t.Fatalf("write %s stub: %v", name, err)
		}
		specs = append(specs, command.Spec{Name: name, Path: stub})
	}

	runner, err := command.NewRunner(specs...)
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	provider := NewProvider(Options{
		Runner:     runner,
		WorldPath:  filepath.Join(dir, "world"),
		RebootFlag: filepath.Join(dir, "reboot-required"),
		Now:        func() time.Time { return time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC) },
	})

	calls := func() []string {
		data, err := os.ReadFile(callLog)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(data)), "\n")
	}
	return provider, calls
}

// apkScripts builds a stub that answers each apk subcommand.
func apkScripts(refresh, simulate, versions, info, policy string) map[string]string {
	return map[string]string{
		CommandAPK: `case "$1" in
  update) cat <<'EOF' >&2
` + refresh + `EOF
    ;;
  upgrade) cat <<'EOF'
` + simulate + `EOF
    ;;
  version) cat <<'EOF'
` + versions + `EOF
    ;;
  info) cat <<'EOF'
` + info + `EOF
    ;;
  policy) cat <<'EOF'
` + policy + `EOF
    ;;
esac
`,
	}
}

func TestBothOfAPKsRefreshSummariesAreUnderstood(t *testing.T) {
	// apk prints one summary when everything worked and a different one when
	// something did not, and only the second mentions failure. Knowing one of
	// them is how a healthy host gets reported as unreadable.
	unavailable, stale, found := parseAPKRefresh(apkRefreshOK)
	if !found {
		t.Fatal("the summary of a successful refresh was not recognised")
	}
	if unavailable != 0 || stale != 0 {
		t.Errorf("a successful refresh reported %d unavailable, %d stale", unavailable, stale)
	}

	unavailable, stale, found = parseAPKRefresh(apkRefreshPartial)
	if !found || unavailable != 1 || stale != 0 {
		t.Errorf("degraded refresh = (%d, %d, %v)", unavailable, stale, found)
	}

	// Anything else is "cannot say" rather than success, which is what turned
	// the gap above into a conservative answer instead of a wrong one.
	if _, _, found := parseAPKRefresh("something apk has never printed"); found {
		t.Error("output this parser does not understand was read as a summary")
	}
}

func TestCheckReportsUnknownRatherThanUpToDateWhenNothingCouldBeReached(t *testing.T) {
	// The failure this package exists to prevent. Every repository is
	// unreachable, apk exits zero, and the list is empty — a panel that trusted
	// that would tell somebody they were safe.
	provider, _ := recording(t, ManagerAPK,
		apkScripts(apkRefreshUnreachable, apkSimulateOutput, apkVersionOutput, "", ""))

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Checked {
		t.Error("a check against unreachable repositories was reported as successful")
	}
	if report.Unavailable != 2 {
		t.Errorf("unavailable = %d, want 2", report.Unavailable)
	}
	if !strings.Contains(report.Reason, "could not be reached") {
		t.Errorf("reason = %q", report.Reason)
	}
	// And it does not quietly present a pending list built on a stale index.
	if len(report.Packages) != 0 {
		t.Errorf("packages = %d, want none from an unusable check", len(report.Packages))
	}
}

func TestCheckListsWhatTheManagerSaysItWillDo(t *testing.T) {
	provider, calls := recording(t, ManagerAPK,
		apkScripts(apkRefreshOK, apkSimulateOutput, apkVersionOutput, "", ""))

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.Checked {
		t.Fatalf("a good check was reported as failed: %s", report.Reason)
	}
	if len(report.Packages) != 4 {
		t.Fatalf("packages = %d, want 4", len(report.Packages))
	}

	first := report.Packages[0]
	if first.Name != "git" || first.Installed != "2.45.4-r0" || first.Available != "2.47.3-r0" {
		t.Errorf("first package = %+v", first)
	}
	// "git-init-template" is one package, not "git" plus "init-template".
	found := false
	for _, pkg := range report.Packages {
		if pkg.Name == "git-init-template" {
			found = true
		}
	}
	if !found {
		t.Errorf("a package whose name contains dashes was split: %+v", report.Packages)
	}

	joined := strings.Join(calls(), "\n")
	if !strings.Contains(joined, "apk update") {
		t.Errorf("the index was not refreshed:\n%s", joined)
	}
	if !strings.Contains(joined, "apk upgrade --simulate") {
		t.Errorf("the pending list did not come from a simulation:\n%s", joined)
	}
}

func TestCheckReportsAPinnedPackageAsHeldRatherThanPending(t *testing.T) {
	// Measured: apk version lists it forever, apk upgrade never moves it. As a
	// pending update it would be a queue that never empties.
	provider, _ := recording(t, ManagerAPK,
		apkScripts(apkRefreshOK, "OK: 479 MiB in 205 packages\n", apkVersionOutput, "", ""))

	if err := os.WriteFile(provider.worldPath,
		[]byte("nginx\ngit=2.45.4-r0\nbusybox\n"), 0o644); err != nil {
		t.Fatalf("write world: %v", err)
	}

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if len(report.Packages) != 0 {
		t.Errorf("packages = %d, want none: apk said it would upgrade nothing", len(report.Packages))
	}
	if len(report.Held) != 4 {
		t.Fatalf("held = %d, want 4", len(report.Held))
	}

	var git Held
	for _, held := range report.Held {
		if held.Name == "git" {
			git = held
		}
	}
	if !strings.Contains(git.Reason, "pinned to 2.45.4-r0") {
		t.Errorf("the pin was not reported: %+v", git)
	}
}

func TestAlpineSaysItCannotIdentifySecurityUpdates(t *testing.T) {
	// Reporting "0 security updates" here would answer a question apk was never
	// asked, and it would read as "nothing urgent".
	provider, _ := recording(t, ManagerAPK,
		apkScripts(apkRefreshOK, apkSimulateOutput, apkVersionOutput, "", ""))

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.SecurityKnown {
		t.Error("an Alpine host claimed it can identify security updates")
	}
	if report.SecurityCount() != 0 {
		t.Error("security updates were counted on a host that cannot mark them")
	}
}

func TestAPTMarksSecurityUpdatesFromTheOriginItPrints(t *testing.T) {
	provider, _ := recording(t, ManagerAPT, map[string]string{
		CommandAPT: `case "$1" in
  update) exit 0 ;;
  -s) cat <<'EOF'
` + aptSimulateOutput + `EOF
    ;;
esac
`,
		CommandAPTMark:   "\n",
		CommandDpkgQuery: "\n",
	})

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.SecurityKnown {
		t.Fatal("a Debian host said it cannot identify security updates")
	}
	if len(report.Packages) != 2 {
		t.Fatalf("packages = %d, want 2", len(report.Packages))
	}

	byName := map[string]Package{}
	for _, pkg := range report.Packages {
		byName[pkg.Name] = pkg
	}
	if byName["base-files"].Security {
		t.Error("an ordinary update was marked as a security fix")
	}
	if !byName["libgcrypt20"].Security {
		t.Error("a security update was not marked")
	}
	if byName["libgcrypt20"].Installed != "1.10.1-3" ||
		byName["libgcrypt20"].Available != "1.10.1-3+deb12u1" {
		t.Errorf("versions = %+v", byName["libgcrypt20"])
	}
	if report.SecurityCount() != 1 {
		t.Errorf("security count = %d, want 1", report.SecurityCount())
	}
}

func TestAPTRefreshFailureIsReportedRatherThanIgnored(t *testing.T) {
	// apt prints "Err:" and carries on, so the exit status of an update that
	// reached nothing can still be zero — the same trap apk sets.
	provider, _ := recording(t, ManagerAPT, map[string]string{
		CommandAPT: `case "$1" in
  update) echo 'Err:1 http://deb.debian.org/debian bookworm InRelease' >&2; exit 0 ;;
esac
`,
		CommandAPTMark:   "\n",
		CommandDpkgQuery: "\n",
	})

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Checked {
		t.Error("a refresh that reached nothing was reported as successful")
	}
	if report.Unavailable != 1 {
		t.Errorf("unavailable = %d, want 1", report.Unavailable)
	}
}

func TestApplyReadsBackWhatActuallyMoved(t *testing.T) {
	// A package manager resolves dependencies, so applying one update routinely
	// moves several. The history has to describe the machine, not the request.
	before := "git-2.45.4-r0\nnginx-1.26.3-r3\nperl-git-2.45.4-r0\n"
	after := "git-2.47.3-r0\nnginx-1.26.3-r3\nperl-git-2.47.3-r0\n"

	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	if err := os.WriteFile(state, []byte(before), 0o644); err != nil {
		t.Fatalf("write state: %v", err)
	}

	provider, calls := recording(t, ManagerAPK, map[string]string{
		CommandAPK: `case "$1" in
  info) cat ` + state + ` ;;
  upgrade) cat <<'EOF' > ` + state + `
` + after + `EOF
    echo 'OK: 479 MiB in 205 packages' ;;
esac
`,
	})

	result, err := provider.Apply(context.Background(), []string{"git"}, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Changed) != 2 {
		t.Fatalf("changed = %+v, want git and perl-git", result.Changed)
	}
	if result.Changed[0].Name != "git" || result.Changed[0].From != "2.45.4-r0" ||
		result.Changed[0].To != "2.47.3-r0" {
		t.Errorf("first change = %+v", result.Changed[0])
	}
	// perl-git was never asked for and moved anyway, which is the point.
	if result.Changed[1].Name != "perl-git" {
		t.Errorf("a dependency that moved was not reported: %+v", result.Changed)
	}
	if !strings.Contains(strings.Join(calls(), "\n"), "apk upgrade --no-progress git") {
		t.Errorf("the named package was not the one upgraded:\n%s", strings.Join(calls(), "\n"))
	}
}

func TestApplyRefusesANameThatWouldBeReadAsAnOption(t *testing.T) {
	// The package manager runs as root. "--allow-untrusted" is not a package.
	provider, calls := recording(t, ManagerAPK, apkScripts(apkRefreshOK, "", "", "", ""))

	if _, err := provider.Apply(context.Background(),
		[]string{"--allow-untrusted"}, nil); err == nil {
		t.Fatal("a name that is an option was accepted")
	}
	if len(calls()) != 0 {
		t.Errorf("the package manager was run anyway: %v", calls())
	}
}

func TestRevertRefusesAVersionTheHostCanNoLongerInstall(t *testing.T) {
	// The refusal is the feature. Neither manager keeps the package it
	// replaced, so a revert offered without this check is a button that fails
	// after taking the service down.
	provider, calls := recording(t, ManagerAPK,
		apkScripts(apkRefreshOK, "", "", "", apkPolicyOutput))

	_, err := provider.Revert(context.Background(), "jq", "1.6.0-r0", nil)
	if !errors.Is(err, ErrVersionUnavailable) {
		t.Fatalf("error = %v, want the version being unavailable", err)
	}
	// It asked, and it did not install anything.
	joined := strings.Join(calls(), "\n")
	if !strings.Contains(joined, "apk policy jq") {
		t.Errorf("the package manager was not asked:\n%s", joined)
	}
	if strings.Contains(joined, "apk add") {
		t.Errorf("something was installed anyway:\n%s", joined)
	}
}

func TestRevertProceedsWhenTheVersionIsStillInARepository(t *testing.T) {
	provider, calls := recording(t, ManagerAPK, map[string]string{
		CommandAPK: `case "$1" in
  policy) cat <<'EOF'
` + apkPolicyOutput + `EOF
    ;;
  info) echo 'jq-1.7.1-r0' ;;
  add) echo 'OK: 479 MiB in 205 packages' ;;
esac
`,
	})

	if _, err := provider.Revert(context.Background(), "jq", "1.7.1-r0", nil); err != nil {
		t.Fatalf("Revert: %v", err)
	}
	if !strings.Contains(strings.Join(calls(), "\n"), "apk add --no-progress jq=1.7.1-r0") {
		t.Errorf("the version was not pinned on the way in:\n%s", strings.Join(calls(), "\n"))
	}
}

func TestRebootRequiredIsReadFromTheHostsOwnFlag(t *testing.T) {
	provider, _ := recording(t, ManagerAPK,
		apkScripts(apkRefreshOK, "OK: nothing\n", "", "", ""))

	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.RebootRequired {
		t.Error("a reboot was reported with no flag file")
	}

	if err := os.WriteFile(provider.rebootFlag, []byte("*** System restart required ***\n"), 0o644); err != nil {
		t.Fatalf("write flag: %v", err)
	}
	report, err = provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !report.RebootRequired {
		t.Error("the host's own restart flag was ignored")
	}
}

func TestUnavailableWithoutAPackageManager(t *testing.T) {
	// A runner that knows a program, just not a package manager: the Agent
	// always has some commands allowlisted, so an empty one would be testing a
	// situation that cannot arise.
	dir := t.TempDir()
	stub := filepath.Join(dir, "true")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	runner, err := command.NewRunner(command.Spec{Name: "true", Path: stub})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}
	provider := NewProvider(Options{Runner: runner})

	if provider.Available() {
		t.Fatal("a host with no package manager was reported as usable")
	}
	report, err := provider.Check(context.Background())
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if report.Checked || report.Reason == "" {
		t.Errorf("report = %+v", report)
	}
	if _, err := provider.Apply(context.Background(), nil, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}

func TestParseAPTPolicyReadsTheVersionTable(t *testing.T) {
	versions := parseAPTPolicyVersions(aptPolicyOutput)
	if len(versions) != 2 {
		t.Fatalf("versions = %v, want two", versions)
	}
	if !contains(versions, "1.10.1-3+deb12u1") || !contains(versions, "1.10.1-3") {
		t.Errorf("versions = %v", versions)
	}
}
