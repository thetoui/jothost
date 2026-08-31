//go:build linux

package fail2ban

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/command"
)

// The client is tested against a recording stub whose output is copied from a
// live fail2ban 1.1.0, not invented.
//
// That matters more here than in most phases. fail2ban-client prints a
// pipe-drawn tree meant for a person, and it is the only interface the daemon
// has — so the parser is the risk, and a stub that printed what I imagined the
// tree looks like would test my imagination.

func init() {
	timeNow = func() time.Time { return time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC) }
}

// allLogs says every path exists, which is what a host with logs looks like.
type allLogs struct{}

func (allLogs) LogExists(string) bool { return true }

// noLogs says none do.
type noLogs struct{}

func (noLogs) LogExists(string) bool { return false }

// recording writes a stub fail2ban-client and returns a Provider driving it,
// plus a function returning the calls it saw.
func recording(t *testing.T, script string, logs LogFinder) (*Provider, func() []string) {
	t.Helper()

	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "fail2ban-client")

	body := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> " + log + "\n" + script
	if err := os.WriteFile(stub, []byte(body), 0o700); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	runner, err := command.NewRunner(command.Spec{Name: CommandName, Path: stub})
	if err != nil {
		t.Fatalf("NewRunner: %v", err)
	}

	if logs == nil {
		logs = allLogs{}
	}
	provider := NewProvider(Options{Runner: runner, Dir: dir, Logs: logs})

	calls := func() []string {
		content, err := os.ReadFile(log)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(content)), "\n")
	}
	return provider, calls
}

// answering is a stub that replies to each subcommand the way fail2ban does.
const answering = `case "$1 $2" in
  "ping ") echo "Server replied: pong" ;;
  "--version ") echo "Fail2Ban v1.1.0" ;;
  "status ") cat <<'OUT'
Status
|- Number of jail:	2
` + "`" + `- Jail list:	sshd, sshd-ddos
OUT
    ;;
  "status sshd") cat <<'OUT'
Status for the jail: sshd
|- Filter
|  |- Currently failed:	2
|  |- Total failed:	12
|  ` + "`" + `- File list:	/var/log/messages
` + "`" + `- Actions
   |- Currently banned:	1
   |- Total banned:	3
   ` + "`" + `- Banned IP list:	198.51.100.7
OUT
    ;;
  "status sshd-ddos") cat <<'OUT'
Status for the jail: sshd-ddos
|- Filter
|  |- Currently failed:	0
|  |- Total failed:	0
|  ` + "`" + `- File list:	/var/log/messages
` + "`" + `- Actions
   |- Currently banned:	0
   |- Total banned:	0
   ` + "`" + `- Banned IP list:
OUT
    ;;
  "get nginx-botsearch") case "$3" in
      maxretry) echo 10 ;;
      findtime) echo 600 ;;
      bantime) echo 21600 ;;
    esac ;;
  "get sshd") case "$3" in
      maxretry) echo 5 ;;
      findtime) echo 600 ;;
      bantime) echo 3600 ;;
      ignoreip) printf 'These IP addresses/networks are ignored:\n|- 127.0.0.0/8\n` + "`" + `- ::1\n' ;;
    esac ;;
  "get sshd-ddos") echo 10 ;;
  "set sshd") echo 1 ;;
esac
exit 0
`

func TestJailsAreReadFromTheDaemonsOwnTree(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	jails, err := provider.Jails(context.Background())
	if err != nil {
		t.Fatalf("Jails: %v", err)
	}
	if len(jails) != 2 || jails[0] != "sshd" || jails[1] != "sshd-ddos" {
		t.Fatalf("jails = %q, want [sshd sshd-ddos]", jails)
	}
	if got := calls(); len(got) != 1 || got[0] != "status" {
		t.Fatalf("calls = %q", got)
	}
}

func TestJailStatusReadsTheCountersAndTheBans(t *testing.T) {
	provider, _ := recording(t, answering, nil)

	jail, err := provider.JailStatus(context.Background(), "sshd")
	if err != nil {
		t.Fatalf("JailStatus: %v", err)
	}

	if jail.Failed != 2 || jail.TotalFailed != 12 {
		t.Fatalf("failures = %d/%d, want 2/12", jail.Failed, jail.TotalFailed)
	}
	if jail.Currently != 1 || jail.Total != 3 {
		t.Fatalf("bans = %d/%d, want 1/3", jail.Currently, jail.Total)
	}
	if len(jail.Banned) != 1 || jail.Banned[0] != "198.51.100.7" {
		t.Fatalf("banned = %q", jail.Banned)
	}
	if len(jail.LogPaths) != 1 || jail.LogPaths[0] != "/var/log/messages" {
		t.Fatalf("log paths = %q", jail.LogPaths)
	}
}

// An empty ban list is the common case, and reading the tree's trailing tab as
// an address would put a row in the panel that unbans nothing.
func TestAnEmptyBanListIsEmpty(t *testing.T) {
	provider, _ := recording(t, answering, nil)

	jail, err := provider.JailStatus(context.Background(), "sshd-ddos")
	if err != nil {
		t.Fatalf("JailStatus: %v", err)
	}
	if len(jail.Banned) != 0 {
		t.Fatalf("banned = %q, want none", jail.Banned)
	}
}

// The numbers the panel shows come from `get`, which returns one value on one
// line — not from the configuration files, because the whole point is to find
// out whether what was written is what the daemon is using.
func TestThePolicyIsAskedOfTheDaemon(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	maxRetry, findTime, banTime, err := provider.Policy(context.Background(), "sshd")
	if err != nil {
		t.Fatalf("Policy: %v", err)
	}
	if maxRetry != 5 || findTime != 600 || banTime != 3600 {
		t.Fatalf("policy = %d/%d/%d", maxRetry, findTime, banTime)
	}

	want := []string{"get sshd maxretry", "get sshd findtime", "get sshd bantime"}
	if got := calls(); len(got) != 3 {
		t.Fatalf("calls = %q, want %q", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("call %d = %q, want %q", i, got[i], want[i])
			}
		}
	}
}

func TestUnbanPassesAValidatedAddress(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	if err := provider.Unban(context.Background(), "sshd", "198.51.100.7"); err != nil {
		t.Fatalf("Unban: %v", err)
	}
	if got := calls(); len(got) != 1 || got[0] != "set sshd unbanip 198.51.100.7" {
		t.Fatalf("calls = %q", got)
	}
}

// fail2ban answers "0" and exits zero when it removed nothing, which is a
// success code for a call that did not do what was asked.
func TestUnbanningSomethingThatIsNotBannedIsAnError(t *testing.T) {
	provider, _ := recording(t, "echo 0\nexit 0\n", nil)

	err := provider.Unban(context.Background(), "sshd", "198.51.100.7")
	if err == nil {
		t.Fatal("unbanning an address that was not banned reported success")
	}
	if !strings.Contains(err.Error(), "not banned") {
		t.Fatalf("error = %q, want it to say the address was not banned", err)
	}
}

func TestAnAddressThatIsNotOneNeverReachesTheDaemon(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	for _, address := range []string{
		"", "not-an-ip", "198.51.100.7; rm -rf /", "evil.example.com",
		"198.51.100.7 198.51.100.8", "fe80::1%eth0",
	} {
		if err := provider.Unban(context.Background(), "sshd", address); err == nil {
			t.Fatalf("%q was accepted as an address", address)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("calls = %q, want none: rejection happens before execution", got)
	}
}

func TestAJailNameThatIsNotOneNeverReachesTheDaemon(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	for _, name := range []string{"", "../../etc/passwd", "sshd; id", "a b", strings.Repeat("x", 80)} {
		if _, err := provider.JailStatus(context.Background(), name); err == nil {
			t.Fatalf("%q was accepted as a jail name", name)
		}
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("calls = %q, want none", got)
	}
}

// ------------------------------------------------------------- the drop-in

// The file name is load-bearing. fail2ban reads jail.conf, then jail.d/*.conf,
// then the .local files, and the last value of an option wins — so a file named
// 10-jothost.conf is read before the distribution's own drop-in and silently
// loses to it. This is not a style preference; it cost an afternoon.
func TestTheDropInIsNamedSoFail2banReadsItLast(t *testing.T) {
	provider, _ := recording(t, answering, nil)

	name := filepath.Base(provider.DropInPath())
	if !strings.HasSuffix(name, ".local") {
		t.Fatalf("drop-in is %q, which a .conf file would override", name)
	}
	if !strings.HasPrefix(name, "99-") {
		t.Fatalf("drop-in is %q, which sorts before other .local files", name)
	}
	if filepath.Base(filepath.Dir(provider.DropInPath())) != "jail.d" {
		t.Fatalf("drop-in is in %q, not jail.d", filepath.Dir(provider.DropInPath()))
	}
}

func TestApplyWritesPolicyAndReadsItBack(t *testing.T) {
	provider, calls := recording(t, answering, nil)

	enabled := true
	result, err := provider.Apply(context.Background(), Change{Jail: "sshd", Enabled: &enabled})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !result.Enabled {
		t.Fatal("the jail was not enabled")
	}

	content, err := os.ReadFile(provider.DropInPath())
	if err != nil {
		t.Fatalf("read the drop-in: %v", err)
	}
	for _, want := range []string{"[sshd]", "enabled = true", "maxretry = 5", "bantime = 3600"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("the drop-in is missing %q:\n%s", want, content)
		}
	}
	// The panel does not write a filter: the distribution's is the one its own
	// log format was written against.
	if strings.Contains(string(content), "filter") {
		t.Fatalf("the panel wrote a filter, which is the distribution's:\n%s", content)
	}

	// It validated before reloading, and read the policy back afterwards.
	joined := strings.Join(calls(), "\n")
	if !strings.Contains(joined, "-t") {
		t.Fatalf("the configuration was not validated: %q", calls())
	}
	if !strings.Contains(joined, "get sshd maxretry") {
		t.Fatalf("the policy was not read back: %q", calls())
	}
}

// The drop-in is rewritten whole on every change, so a jail configured earlier
// has to be carried across — otherwise turning on the second jail would turn off
// the first.
func TestAChangeKeepsTheOtherJails(t *testing.T) {
	provider, _ := recording(t, answering, nil)

	enabled := true
	if _, err := provider.Apply(context.Background(), Change{Jail: "sshd", Enabled: &enabled}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := provider.Apply(context.Background(),
		Change{Jail: "nginx-botsearch", Enabled: &enabled}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	content, err := os.ReadFile(provider.DropInPath())
	if err != nil {
		t.Fatalf("read the drop-in: %v", err)
	}
	if !strings.Contains(string(content), "[sshd]") {
		t.Fatalf("the first jail was dropped:\n%s", content)
	}
	if !strings.Contains(string(content), "[nginx-botsearch]") {
		t.Fatalf("the second jail is missing:\n%s", content)
	}
}

// A jail whose log does not exist would be reported by fail2ban as a failed
// jail and skipped, while the panel showed it as enabled and watching.
func TestAJailWithNoLogIsRefused(t *testing.T) {
	provider, _ := recording(t, answering, noLogs{})

	enabled := true
	_, err := provider.Apply(context.Background(), Change{Jail: "sshd", Enabled: &enabled})
	if err == nil {
		t.Fatal("a jail was enabled against a log that does not exist")
	}
	if !strings.Contains(err.Error(), "exist on this host") {
		t.Fatalf("error = %q, want it to say the log is missing", err)
	}
}

// The check the rest of the phase exists for: the daemon is running numbers
// other than the ones written, which means something else configures this jail.
func TestAPolicyTheDaemonDoesNotAdoptIsReportedAndRolledBack(t *testing.T) {
	// This stub answers every `get maxretry` with 10, whatever is written.
	stubborn := strings.Replace(answering, "maxretry) echo 5 ;;", "maxretry) echo 10 ;;", 1)
	provider, _ := recording(t, stubborn, nil)

	enabled := true
	retries := 5
	_, err := provider.Apply(context.Background(),
		Change{Jail: "sshd", Enabled: &enabled, MaxRetry: &retries})
	if err == nil {
		t.Fatal("a change the daemon ignored was reported as a success")
	}
	if !strings.Contains(err.Error(), "something else on this host") {
		t.Fatalf("error = %q, want it to explain what happened", err)
	}
	// And what was there before is back: nothing, since this was the first write.
	if _, statErr := os.Stat(provider.DropInPath()); !os.IsNotExist(statErr) {
		content, _ := os.ReadFile(provider.DropInPath())
		t.Fatalf("the rejected configuration was left in place:\n%s", content)
	}
}

func TestAConfigurationFail2banRefusesIsNotLeftBehind(t *testing.T) {
	// -t fails; everything else succeeds.
	refusing := `case "$1" in
  -t) echo "ERROR: Failed during configuration: Bad value substitution" >&2; exit 255 ;;
  ping) echo "Server replied: pong" ;;
esac
exit 0
`
	provider, _ := recording(t, refusing, nil)

	enabled := true
	_, err := provider.Apply(context.Background(), Change{Jail: "sshd", Enabled: &enabled})
	if err == nil {
		t.Fatal("a configuration fail2ban refused was installed")
	}
	if !strings.Contains(err.Error(), "Bad value substitution") {
		t.Fatalf("error = %q, want fail2ban's own words", err)
	}
	if _, statErr := os.Stat(provider.DropInPath()); !os.IsNotExist(statErr) {
		t.Fatal("the refused configuration was left on disk")
	}
}

// -------------------------------------------------------------- the policy

func TestAPolicyThatWouldBanForeverIsRefused(t *testing.T) {
	// A ban shorter than the window means the counter never resets: the address
	// is banned again the moment it is released.
	policy := Policy{MaxRetry: 5, FindTime: 3600, BanTime: 600}
	if err := policy.Validate(); err == nil {
		t.Fatal("a ban shorter than the counting window was accepted")
	}

	if err := (Policy{MaxRetry: 5, FindTime: 600, BanTime: 3600}).Validate(); err != nil {
		t.Fatalf("an ordinary policy was refused: %v", err)
	}
}

func TestAPolicyOutsideItsBoundsIsRefused(t *testing.T) {
	cases := []struct {
		policy Policy
		reason string
	}{
		{Policy{MaxRetry: 0, FindTime: 600, BanTime: 3600}, "no failures allowed at all"},
		{Policy{MaxRetry: 1000, FindTime: 600, BanTime: 3600}, "a threshold nothing reaches"},
		{Policy{MaxRetry: 5, FindTime: 1, BanTime: 3600}, "a window too short to be real"},
		{Policy{MaxRetry: 5, FindTime: 600, BanTime: 5}, "a ban shorter than a packet round trip"},
		{Policy{MaxRetry: 5, FindTime: 600, BanTime: -1}, "fail2ban's permanent ban"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if err := tc.policy.Validate(); err == nil {
				t.Fatalf("accepted: %s", tc.reason)
			}
		})
	}
}

// -------------------------------------------------------------- the status

// A jail somebody configured by hand is banning people whether the panel knows
// about it or not, so it is listed — and left alone.
func TestStatusListsJailsThePanelDoesNotManage(t *testing.T) {
	provider, _ := recording(t, answering, nil)

	status := provider.Status(context.Background(), false)

	var found *Jail
	for i := range status.Jails {
		if status.Jails[i].Name == "sshd-ddos" {
			found = &status.Jails[i]
		}
	}
	if found == nil {
		t.Fatalf("a jail the panel did not configure was hidden: %+v", status.Jails)
	}
	if found.Managed {
		t.Fatal("a jail the panel does not offer was reported as managed")
	}

	// And the managed ones come first, because they are what the page can act
	// on.
	if !status.Jails[0].Managed {
		t.Fatalf("the list does not lead with what the panel manages: %+v", status.Jails)
	}
}

func TestStatusReportsAHostWithNoFail2ban(t *testing.T) {
	provider := NewProvider(Options{Dir: t.TempDir()})

	status := provider.Status(context.Background(), true)
	if status.Available {
		t.Fatal("a host with no fail2ban reported it as available")
	}
	if !status.CanInstall {
		t.Fatal("a host that could install it was not offered the option")
	}
	if status.Reason == "" {
		t.Fatal("no reason was given")
	}
}

// A jail the panel offers that this host cannot run is listed as unavailable
// with the reason, rather than hidden: "there is no jail for that" and "the
// panel does not do that" are different answers.
func TestAJailWhoseLogIsAbsentIsListedAsUnavailable(t *testing.T) {
	provider, _ := recording(t, `case "$1" in ping) exit 1 ;; esac
exit 0
`, noLogs{})

	status := provider.Status(context.Background(), false)
	if len(status.Jails) == 0 {
		t.Fatal("no jails were listed")
	}
	for _, jail := range status.Jails {
		if jail.Available {
			t.Fatalf("%s was reported available on a host with none of its logs", jail.Name)
		}
		if jail.Reason == "" {
			t.Fatalf("%s was reported unavailable with no reason", jail.Name)
		}
	}
}
