//go:build linux

package ftp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// The parsers here are tested against output copied from a live ProFTPD
// 1.3.8b, not invented.
//
// That is the point of these tests rather than a detail of them. ftpwho and
// ftpquota print columns meant for a person, and they are the only interface
// proftpd offers for "who is connected" and "how much has this account used" —
// so the parser is the risk, and a stub printing what I imagined the output
// looks like would test my imagination. The samples below are pasted from a
// real server: ftpquota reports a raw byte count even when told --units=Mb, and
// ftpwho writes the field name as "Uploaded bytes" with a lower-case b. Both
// were surprises, and both are why this is done this way.

func init() {
	timeNow = func() time.Time { return time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC) }
}

// stubReloader stands in for the service manager.
type stubReloader struct {
	reloads int
	running bool
	fail    error
}

func (s *stubReloader) Reload(context.Context) error {
	s.reloads++
	return s.fail
}
func (s *stubReloader) Running(context.Context) bool { return s.running }

// recording writes stubs for every tool this package drives and returns a
// Provider using them, plus a function returning the calls they saw.
func recording(t *testing.T, scripts map[string]string) (*Provider, *stubReloader, func() []string) {
	t.Helper()

	dir := t.TempDir()
	callLog := filepath.Join(dir, "calls.log")

	// The stubs live in their own directory: one of them is called "proftpd",
	// and so is the configuration root.
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	specs := make([]command.Spec, 0, 4)
	for _, name := range []string{CommandProftpd, CommandFtpasswd, CommandFtpwho, CommandFtpquota} {
		stub := filepath.Join(binDir, name)
		body := "#!/bin/sh\nprintf '%s %s\\n' " + name + " \"$*\" >> " + callLog + "\n"
		if name == CommandFtpasswd {
			// The real ftpasswd creates the password file, mode 0644, which is
			// what the code then has to tighten. A stub that did not create it
			// would make that step untestable.
			body += ftpasswdCreatesTheFile
		}
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

	reloader := &stubReloader{running: true}
	provider := NewProvider(Options{
		Runner: runner,
		Paths: Paths{
			ConfigDir: filepath.Join(dir, "proftpd"),
			RunDir:    filepath.Join(dir, "run"),
			LogDir:    filepath.Join(dir, "log"),
		},
		Reload: reloader,
	})

	calls := func() []string {
		content, err := os.ReadFile(callLog)
		if err != nil {
			return nil
		}
		return strings.Split(strings.TrimSpace(string(content)), "\n")
	}
	return provider, reloader, calls
}

// ftpasswdCreatesTheFile emulates the one side effect the real tool has that
// this package depends on.
const ftpasswdCreatesTheFile = `for arg in "$@"; do
  case "$arg" in
    --file=*) touch "${arg#--file=}" && chmod 644 "${arg#--file=}" ;;
  esac
done
`

func settings() Settings {
	return Settings{PassiveFrom: 30000, PassiveTo: 30100}
}

func user(name string) User {
	return User{Name: name, UID: 1000, GID: 1000, Home: "/var/www/example.com/public"}
}

// ---------------------------------------------------------------- rendering

func TestRenderConfinesEverySessionAndNamesTheVirtualUserFile(t *testing.T) {
	p := DefaultPaths()
	out := Render(p, settings(), []User{user("alice")})

	for _, want := range []string{
		"DefaultRoot ~",
		"AuthUserFile " + p.Passwd(),
		"AuthOrder mod_auth_file.c",
		"ScoreboardFile " + p.Scoreboard(),
		"PassivePorts 30000 30100",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the configuration is missing %q\n%s", want, out)
		}
	}
}

// AuthOrder naming only mod_auth_file is what stops an FTP password being a way
// to try a system account's credentials.
func TestRenderDoesNotFallBackToSystemAccounts(t *testing.T) {
	out := Render(DefaultPaths(), settings(), []User{user("alice")})
	if strings.Contains(out, "mod_auth_unix") || strings.Contains(out, "mod_auth_pam") {
		t.Fatalf("the configuration allows system accounts to log in\n%s", out)
	}
}

func TestRenderDeniesWritesToReadOnlyAccountsOnly(t *testing.T) {
	full := user("alice")
	readOnly := user("bob")
	readOnly.ReadOnly = true

	out := Render(DefaultPaths(), settings(), []User{full, readOnly})

	if !strings.Contains(out, "<Limit WRITE>") || !strings.Contains(out, "DenyUser bob") {
		t.Fatalf("bob is not denied writes\n%s", out)
	}
	if strings.Contains(out, "DenyUser alice") || strings.Contains(out, "alice,bob") {
		t.Fatalf("alice should keep write access\n%s", out)
	}
}

func TestRenderOmitsTheLimitBlockWhenEveryAccountCanWrite(t *testing.T) {
	out := Render(DefaultPaths(), settings(), []User{user("alice")})
	if strings.Contains(out, "<Limit WRITE>") {
		t.Fatalf("an empty deny list should not be written\n%s", out)
	}
}

func TestRenderOffersTLSOnlyWithMaterialToPresent(t *testing.T) {
	out := Render(DefaultPaths(), settings(), []User{user("alice")})
	if strings.Contains(out, "TLSEngine") {
		t.Fatalf("TLS was configured with no certificate\n%s", out)
	}

	s := settings()
	s.TLSCertificate = "/etc/jothost/ssl/example.com/fullchain.pem"
	s.TLSKey = "/etc/jothost/ssl/example.com/privkey.pem"
	out = Render(DefaultPaths(), s, []User{user("alice")})

	if !strings.Contains(out, "TLSEngine on") || !strings.Contains(out, s.TLSCertificate) {
		t.Fatalf("TLS was not configured\n%s", out)
	}
	// Offered, not required, unless it was asked for.
	if !strings.Contains(out, "TLSRequired off") {
		t.Fatalf("TLS should be offered rather than required by default\n%s", out)
	}

	s.RequireTLS = true
	out = Render(DefaultPaths(), s, []User{user("alice")})
	if !strings.Contains(out, "TLSRequired on") {
		t.Fatalf("TLS was not required when asked for\n%s", out)
	}
}

// ---------------------------------------------------------------- sessions

// Pasted from a live ProFTPD 1.3.8b during a transfer.
const ftpwhoOutput = `standalone FTP daemon [23688], up for 0 min
23698 demo     [  0m3s] (100%) RETR index.html
	KB/s: inf
	client: localhost [127.0.0.1]
	server: 127.0.0.1:21 (ProFTPD Default Installation)
	protocol: ftps
	location: /uploads

Service class                      -   1 user
`

func TestSessionsReadsWhatIsConnected(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		CommandFtpwho: "cat <<'OUT'\n" + ftpwhoOutput + "OUT\n",
	})

	sessions, err := p.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected one session, got %d: %#v", len(sessions), sessions)
	}

	got := sessions[0]
	if got.PID != 23698 || got.User != "demo" {
		t.Errorf("wrong session identity: %#v", got)
	}
	if got.Client != "localhost [127.0.0.1]" || got.Location != "/uploads" {
		t.Errorf("wrong session detail: %#v", got)
	}
	// The one field that says whether the password crossed the network in
	// clear text.
	if !got.Encrypted() {
		t.Errorf("an ftps session was not reported as encrypted: %#v", got)
	}
	if !strings.Contains(got.Activity, "RETR index.html") {
		t.Errorf("the activity was lost: %#v", got)
	}
}

// The header line and the trailing summary are prose. Reading either as a
// session would put a row on the page for a client that does not exist.
func TestSessionsIgnoresTheHeaderAndTheSummary(t *testing.T) {
	sessions := parseSessions(ftpwhoOutput)
	for _, session := range sessions {
		if session.User == "FTP" || session.User == "class" || session.PID == 23688 {
			t.Fatalf("prose was parsed as a session: %#v", session)
		}
	}
}

// ftpwho right-aligns the pid in a five-column field, so a session served by a
// process with a pid under 10000 begins with a space.
//
// This is pasted from a live server, and it is here because the first version of
// this parser read that leading space as "a detail of the line above" and
// dropped every such session in silence. The sample above has a five-digit pid
// and passed throughout — a stub built from what I imagined the output looked
// like, which is the failure this file's opening comment warns about.
const ftpwhoPaddedPID = `standalone FTP daemon [1808], up for 0 min
 1818 probe    [  0m4s] (100%) RETR slow.bin
	client: localhost [127.0.0.1]
	protocol: ftp
	location: /

Service class                      -   1 user
`

func TestSessionsReadsASessionWhosePIDIsPadded(t *testing.T) {
	sessions := parseSessions(ftpwhoPaddedPID)
	if len(sessions) != 1 {
		t.Fatalf("a padded pid dropped the session: %#v", sessions)
	}
	if sessions[0].PID != 1818 || sessions[0].User != "probe" {
		t.Errorf("wrong session: %#v", sessions[0])
	}
	// The details still have to attach to it rather than be swallowed.
	if sessions[0].Protocol != "ftp" || sessions[0].Location != "/" {
		t.Errorf("the session's details were lost: %#v", sessions[0])
	}
}

func TestSessionsIsEmptyWhenNobodyIsConnected(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		CommandFtpwho: "echo 'standalone FTP daemon [100], up for 2 min'\necho 'no users connected'\n",
	})

	sessions, err := p.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %#v", sessions)
	}
}

// A stopped daemon has no scoreboard and ftpwho fails. "Nothing is connected"
// is the true answer, and it is not an error worth failing the page for.
func TestSessionsTreatsAStoppedDaemonAsNobodyConnected(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		CommandFtpwho: "echo 'ftpwho: unable to open scoreboard' >&2\nexit 1\n",
	})

	sessions, err := p.Sessions(context.Background())
	if err != nil {
		t.Fatalf("Sessions: %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("expected no sessions, got %#v", sessions)
	}
}

// The check that matters: this process is root, so a signal sent to the wrong
// pid would be sent successfully.
func TestDisconnectRefusesAPIDThatIsNotAnOpenSession(t *testing.T) {
	p, _, calls := recording(t, map[string]string{
		CommandFtpwho: "cat <<'OUT'\n" + ftpwhoOutput + "OUT\n",
	})

	// A pid that is not in the scoreboard, and the pid of this test process,
	// which is emphatically not a proftpd session.
	for _, pid := range []int{1, 4242, os.Getpid()} {
		if err := p.Disconnect(context.Background(), pid); !errors.Is(err, ErrNoSession) {
			t.Fatalf("disconnecting pid %d: expected ErrNoSession, got %v", pid, err)
		}
	}
	for _, call := range calls() {
		if strings.Contains(call, "kill") {
			t.Fatalf("something was signalled: %q", call)
		}
	}
}

// ---------------------------------------------------------------- quotas

// Pasted from a live ftpquota. Two things here were surprises worth freezing:
// the field is "Uploaded bytes" with a lower-case b, and the value is a raw
// byte count even though the record was written with --units=Mb.
const quotaLimitOutput = `-------------------------------------------
  Name: demo
  Quota Type: User
  Per Session: False
  Limit Type: Hard
    Uploaded bytes:	1048576.00
    Downloaded bytes:	unlimited
    Transferred bytes:	unlimited
    Uploaded files:	unlimited
`

const quotaTallyOutput = `-------------------------------------------
  Name: demo
  Quota Type: User
    Uploaded bytes:	524288.00
    Downloaded bytes:	unlimited
`

func TestQuotasReadsTheLimitAndTheUsage(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		CommandFtpquota: `case "$*" in
  *--type=limit*) cat <<'OUT'
` + quotaLimitOutput + `OUT
  ;;
  *--type=tally*) cat <<'OUT'
` + quotaTallyOutput + `OUT
  ;;
esac
`,
	})
	// Quotas only reads tables that exist.
	writeTables(t, p)

	quotas, err := p.Quotas(context.Background())
	if err != nil {
		t.Fatalf("Quotas: %v", err)
	}
	demo, found := quotas["demo"]
	if !found {
		t.Fatalf("demo has no quota: %#v", quotas)
	}
	if demo.LimitMB != 1 {
		t.Errorf("expected a 1 MB limit, got %d", demo.LimitMB)
	}
	if demo.UsedMB < 0.49 || demo.UsedMB > 0.51 {
		t.Errorf("expected half a megabyte used, got %v", demo.UsedMB)
	}
}

// --update-record resets every limit it is not given to its default, so a
// partial update would quietly clear the ones it did not mention.
func TestSetQuotaPassesEveryLimitOnAnUpdate(t *testing.T) {
	p, _, calls := recording(t, map[string]string{
		CommandFtpquota: `case "$*" in
  *--show-records*--type=limit*) cat <<'OUT'
` + quotaLimitOutput + `OUT
  ;;
esac
`,
	})
	writeTables(t, p)

	if err := p.SetQuota(context.Background(), "demo", 50); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	update := ""
	for _, call := range calls() {
		if strings.Contains(call, "--update-record") {
			update = call
		}
		if strings.Contains(call, "--add-record") {
			t.Fatalf("an existing record was added again, which would duplicate it: %q", call)
		}
	}
	if update == "" {
		t.Fatalf("no update was made: %#v", calls())
	}
	for _, want := range []string{"--bytes-upload=50", "--units=Mb", "--limit-type=hard"} {
		if !strings.Contains(update, want) {
			t.Errorf("the update is missing %q: %q", want, update)
		}
	}
}

// Creating a table that exists would empty it — every limit and every account's
// usage.
func TestEnsureQuotaTablesDoesNotRecreateOnesThatExist(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})
	writeTables(t, p)

	if err := p.EnsureQuotaTables(context.Background()); err != nil {
		t.Fatalf("EnsureQuotaTables: %v", err)
	}
	for _, call := range calls() {
		if strings.Contains(call, "--create-table") {
			t.Fatalf("an existing table was recreated, emptying it: %q", call)
		}
	}
}

func writeTables(t *testing.T, p *Provider) {
	t.Helper()
	if err := p.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	for _, path := range []string{p.paths.QuotaLimit(), p.paths.QuotaTally()} {
		if err := os.WriteFile(path, []byte(""), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
}

// ---------------------------------------------------------------- accounts

func TestAccountsReadsThePasswordFileWithoutReportingHashes(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})
	writePasswd(t, p,
		"demo:$6$abc$def:1000:1000::/var/www/example.com/public:/sbin/nologin\n"+
			"locked:!$6$abc$def:1001:1001::/var/www/other.com/public:/sbin/nologin\n")

	accounts, err := p.Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("expected two accounts, got %#v", accounts)
	}
	if accounts[0].Name != "demo" || accounts[0].UID != 1000 ||
		accounts[0].Home != "/var/www/example.com/public" {
		t.Errorf("wrong account: %#v", accounts[0])
	}
	if accounts[0].Locked {
		t.Errorf("demo is not locked: %#v", accounts[0])
	}
	if !accounts[1].Locked {
		t.Errorf("the ! prefix means locked: %#v", accounts[1])
	}
}

// A host that has never had an FTP account has no file, and that is an empty
// list rather than a broken page.
func TestAccountsIsEmptyWhenThereIsNoFile(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})
	accounts, err := p.Accounts()
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("expected no accounts, got %#v", accounts)
	}
}

// The password goes to the child's standard input. As an argument it would be
// visible in /proc to every account on the host for as long as the process ran.
func TestCreateAccountNeverPutsThePasswordInAnArgument(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})
	const password = "correct-horse-battery"

	if err := p.CreateAccount(context.Background(), user("demo"), password); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	for _, call := range calls() {
		if strings.Contains(call, password) {
			t.Fatalf("the password was passed as an argument: %q", call)
		}
	}

	created := strings.Join(calls(), "\n")
	for _, want := range []string{"--sha512", "--stdin", "--uid=1000", "--shell=/sbin/nologin"} {
		if !strings.Contains(created, want) {
			t.Errorf("the account was created without %q:\n%s", want, created)
		}
	}
}

// A file of password hashes that every account on a shared host can read is one
// they can all copy and attack offline.
func TestCreateAccountLeavesThePasswordFileUnreadable(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		// ftpasswd creates it 0644.
		CommandFtpasswd: "", // the stub creates nothing, so make the file first
	})
	if err := p.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	if err := os.WriteFile(p.paths.Passwd(), []byte(""), 0o644); err != nil {
		t.Fatalf("write passwd: %v", err)
	}

	if err := p.CreateAccount(context.Background(), user("demo"), "correct-horse-battery"); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	info, err := os.Stat(p.paths.Passwd())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("the password file is mode %o, expected 600", mode)
	}
}

// uid 0 is root, and an FTP account mapped to root is one whose every upload
// lands as root inside a chroot that root can leave.
func TestCreateAccountRefusesToMapToRoot(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})

	rooted := user("demo")
	rooted.UID, rooted.GID = 0, 0

	err := p.CreateAccount(context.Background(), rooted, "correct-horse-battery")
	if !errors.Is(err, validate.ErrInvalidFTPUser) {
		t.Fatalf("expected the root mapping to be refused, got %v", err)
	}
	if len(calls()) != 0 {
		t.Fatalf("something was run for a refused account: %#v", calls())
	}
}

// Deleting a website removes every account it owns, and must not fail halfway
// because one had already been removed by hand.
func TestDeleteAccountSucceedsWhenThereIsNothingToDelete(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})
	if err := p.DeleteAccount(context.Background(), "missing"); err != nil {
		t.Fatalf("expected deleting a missing account to succeed, got %v", err)
	}
}

func writePasswd(t *testing.T, p *Provider, content string) {
	t.Helper()
	if err := p.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	if err := os.WriteFile(p.paths.Passwd(), []byte(content), 0o600); err != nil {
		t.Fatalf("write passwd: %v", err)
	}
}

// ---------------------------------------------------------------- applying

func TestApplyValidatesBeforeTheDaemonIsRestarted(t *testing.T) {
	p, reloader, calls := recording(t, map[string]string{})

	if _, err := p.Apply(context.Background(), settings(), []User{user("demo")}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if reloader.reloads != 1 {
		t.Fatalf("expected one restart, got %d", reloader.reloads)
	}

	validated := -1
	for i, call := range calls() {
		if strings.Contains(call, "proftpd -t") {
			validated = i
		}
	}
	if validated < 0 {
		t.Fatalf("the configuration was never validated: %#v", calls())
	}
}

// A rejected configuration must not be left where proftpd will read it at the
// next restart — which is what a reboot would do, taking the daemon down with a
// configuration nobody chose.
func TestApplyLeavesNothingBehindWhenTheConfigurationIsRefused(t *testing.T) {
	p, _, _ := recording(t, map[string]string{
		CommandProftpd: "echo 'unknown directive' >&2\nexit 1\n",
	})

	_, err := p.Apply(context.Background(), settings(), []User{user("demo")})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected the configuration to be refused, got %v", err)
	}
	if _, err := os.Stat(p.paths.DropIn()); !os.IsNotExist(err) {
		content, _ := os.ReadFile(p.paths.DropIn())
		t.Fatalf("the rejected configuration was left in place:\n%s", content)
	}
}

// The Phase 18 lesson, held here: a first write has no backup, and a rollback
// that rebuilds the backup path by convention restores a file that never
// existed and leaves the rejected one in place.
func TestApplyRestoresThePreviousConfigurationWhenTheNewOneIsRefused(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})
	if _, err := p.Apply(context.Background(), settings(), []User{user("demo")}); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	before, err := os.ReadFile(p.paths.DropIn())
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	// Now proftpd refuses everything.
	stub := filepath.Join(filepath.Dir(p.paths.ConfigDir), "bin", CommandProftpd)
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("rewrite stub: %v", err)
	}

	changed := settings()
	changed.PassiveFrom, changed.PassiveTo = 40000, 40100
	if _, err := p.Apply(context.Background(), changed, []User{user("demo")}); err == nil {
		t.Fatalf("expected the change to be refused")
	}

	after, err := os.ReadFile(p.paths.DropIn())
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("the previous configuration was not restored:\n%s", after)
	}
}

// The failure this catches is silent: the panel writes a correct file, the
// validator accepts it, the daemon reloads, and the setting in force is
// somebody else's.
func TestApplyNoticesAnotherFileSettingTheSameDirective(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})
	if err := p.ensureDirs(); err != nil {
		t.Fatalf("ensureDirs: %v", err)
	}
	// Sorts before the panel's file, so proftpd reads it first and it wins.
	other := filepath.Join(p.paths.DropInDir(), "05-operator.conf")
	if err := os.WriteFile(other, []byte("PassivePorts 40000 40100\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := p.Apply(context.Background(), settings(), []User{user("demo")})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("the conflict was not noticed: %#v", result.Conflicts)
	}
	conflict := result.Conflicts[0]
	if conflict.Directive != "PassivePorts" || conflict.File != "05-operator.conf" {
		t.Errorf("wrong conflict: %#v", conflict)
	}
	if conflict.PanelWins {
		t.Errorf("a file read before the panel's wins, and this says otherwise: %#v", conflict)
	}
}

// ---------------------------------------------------------------- reconcile

func TestReconcileRemovesAccountsThePanelDoesNotKnowAbout(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})
	writePasswd(t, p,
		"keep:$6$a:1000:1000::/var/www/example.com/public:/sbin/nologin\n"+
			"stale:$6$b:1001:1001::/var/www/gone.com/public:/sbin/nologin\n")

	result, err := p.Reconcile(context.Background(), Desired{
		Settings: settings(),
		Users:    []User{user("keep")},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != "stale" {
		t.Fatalf("the stale account was not removed: %#v", result.Removed)
	}

	deleted := false
	for _, call := range calls() {
		if strings.Contains(call, "--delete-user") && strings.Contains(call, "--name=stale") {
			deleted = true
		}
		if strings.Contains(call, "--delete-user") && strings.Contains(call, "--name=keep") {
			t.Fatalf("an account the panel knows about was deleted: %q", call)
		}
	}
	if !deleted {
		t.Fatalf("nothing deleted the stale account: %#v", calls())
	}
}

// An account the panel has recorded that the host does not have, with no
// password to recreate it from.
//
// Reported and skipped, not invented and not fatal. Inventing a password would
// create an account nobody can use and nobody knows is unusable; failing would
// mean one such account blocked every later FTP change on the host for good,
// because the panel does not keep passwords and can never supply one itself.
func TestReconcileReportsAnAccountItCannotRecreate(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})

	result, err := p.Reconcile(context.Background(), Desired{
		Settings: settings(),
		Users:    []User{user("new")},
	})
	if err != nil {
		t.Fatalf("one unrecreatable account must not fail the whole change: %v", err)
	}
	if len(result.NeedPassword) != 1 || result.NeedPassword[0] != "new" {
		t.Fatalf("the account was not reported: %#v", result.NeedPassword)
	}
	for _, call := range calls() {
		if strings.Contains(call, "--name=new") {
			t.Fatalf("an account was created with a password nobody chose: %q", call)
		}
	}
}

// The rest of the change still has to happen: the account that can be made is
// made, and the configuration is still written.
func TestReconcileCarriesOnPastAnAccountItCannotRecreate(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})

	result, err := p.Reconcile(context.Background(), Desired{
		Settings:  settings(),
		Users:     []User{user("stranded"), user("fresh")},
		Passwords: map[string]string{"fresh": "correct-horse-battery"},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(result.Created) != 1 || result.Created[0] != "fresh" {
		t.Fatalf("the account that could be made was not: %#v", result.Created)
	}
	if len(result.NeedPassword) != 1 || result.NeedPassword[0] != "stranded" {
		t.Fatalf("the stranded account was not reported: %#v", result.NeedPassword)
	}

	written := strings.Join(calls(), "\n")
	if !strings.Contains(written, "--name=fresh") {
		t.Fatalf("the new account was never written:\n%s", written)
	}
	if _, err := os.Stat(p.paths.DropIn()); err != nil {
		t.Fatalf("the configuration was not written: %v", err)
	}
}

// A set that fails halfway leaves the host in a state that is neither what it
// was nor what was asked for.
func TestReconcileValidatesEverythingBeforeWritingAnything(t *testing.T) {
	p, _, calls := recording(t, map[string]string{})

	bad := user("second")
	bad.UID = 0

	_, err := p.Reconcile(context.Background(), Desired{
		Settings:  settings(),
		Users:     []User{user("first"), bad},
		Passwords: map[string]string{"first": "correct-horse-battery", "second": "correct-horse-battery"},
	})
	if !errors.Is(err, validate.ErrInvalidFTPUser) {
		t.Fatalf("expected the root mapping to be refused, got %v", err)
	}
	if len(calls()) != 0 {
		t.Fatalf("the valid half was written before the invalid half was checked: %#v", calls())
	}
}

func TestReconcileRefusesAPassiveRangeTooSmallToServe(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})

	s := settings()
	s.PassiveFrom, s.PassiveTo = 30000, 30002

	_, err := p.Reconcile(context.Background(), Desired{Settings: s})
	if !errors.Is(err, validate.ErrInvalidPortRange) {
		t.Fatalf("expected the range to be refused, got %v", err)
	}
}

func TestReconcileRefusesToRequireTLSWithNothingToPresent(t *testing.T) {
	p, _, _ := recording(t, map[string]string{})

	s := settings()
	s.RequireTLS = true

	_, err := p.Reconcile(context.Background(), Desired{Settings: s})
	if !errors.Is(err, ErrNoCertificate) {
		t.Fatalf("expected FTPS with no certificate to be refused, got %v", err)
	}
}
