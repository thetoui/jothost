package ssh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real key material, generated with ssh-keygen and pasted here.
//
// Invented base64 would test the parser against a shape nothing produces; these
// are what a person actually pastes into the panel, and their fingerprints below
// are what `ssh-keygen -lf` printed for them — so a change that broke the
// fingerprint would be caught against OpenSSH's own answer rather than against
// this package's.
const (
	edKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOgGTTyStHYebiJCAytvGGjNEziXYMcqrKhTlb1anhyr operator@laptop"
	edFP  = "SHA256:TudVThr2HD30XIK5NF3naO+dRlV3nMQ6NbcAPRbziS0"

	secondKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPET4elVkXnrnYM7ZqOVA3PS+5lkpIHu7EZtLDnyuhO second@laptop"

	rsaKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABAQDWEBGTjrvmH4QlO3re/IW0kDlKv49ZPfke2CeT8z16R9SU9v63dfDcnA1w20UgQ7hdle3zIzLlEYd3GZWxOWEepu4zcao6IpAonPy0/ZXuI8+3bKtnHGVSux9m/ieLjH/L9e0qo2OpZUQ/VbALXhqX2eTgNu3EPe2d9bf8Iz6VjD+MhyTqKzjJT2DZ+u+FhB7mU12phXNNeVs7jpxVH748f9G8eNhfTJWhoCbr4VVaGS4+nOK6Mnwo9XbasIB7deIMsb187wyppulmhlS7Kn/0+EL4w2g7ZmCp50kKA5WYjEIWZUzUN7ImaPnDky3lyZukQ3MEt1AAi54BgEGRQF+z old@server"
	rsaFP  = "SHA256:IUTKx3co4okBG8jEkHCZOKSFL8HsfuCWZzFTjP5a4Ps"
)

func init() {
	timeNow = func() time.Time { return time.Date(2026, time.August, 31, 9, 0, 0, 0, time.UTC) }
}

// fakeAccounts stands in for /etc/passwd.
type fakeAccounts struct{ accounts []Account }

func (f fakeAccounts) LoginAccounts() ([]Account, error) { return f.accounts, nil }

// provider builds a Provider over a temporary /etc/ssh and one account whose
// home is also temporary.
func provider(t *testing.T) (*Provider, Account) {
	t.Helper()

	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("make home: %v", err)
	}

	account := Account{Name: "operator", UID: os.Getuid(), GID: os.Getgid(),
		Home: home, Shell: "/bin/sh"}

	p := NewProvider(Options{
		Dir:      dir,
		Accounts: fakeAccounts{accounts: []Account{account}},
	})
	return p, account
}

// ---------------------------------------------------------------- keys

func TestParseKeyReadsAKeyOpenSSHWrote(t *testing.T) {
	key, err := ParseKey(edKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}

	if key.Fingerprint != edFP {
		t.Fatalf("fingerprint = %q, want the one ssh-keygen printed (%s)", key.Fingerprint, edFP)
	}
	if key.Type != "ssh-ed25519" {
		t.Fatalf("type = %q", key.Type)
	}
	if key.Comment != "operator@laptop" {
		t.Fatalf("comment = %q", key.Comment)
	}
	if key.Bits != 256 {
		t.Fatalf("bits = %d, want 256", key.Bits)
	}
}

func TestParseKeyReadsAnRSAKeysSize(t *testing.T) {
	key, err := ParseKey(rsaKey)
	if err != nil {
		t.Fatalf("ParseKey: %v", err)
	}
	if key.Fingerprint != rsaFP {
		t.Fatalf("fingerprint = %q, want %s", key.Fingerprint, rsaFP)
	}
	// The number an operator is looking for when they ask whether a key is
	// strong enough, read out of the modulus rather than guessed from the type.
	if key.Bits != 2048 {
		t.Fatalf("bits = %d, want 2048", key.Bits)
	}
}

func TestParseKeyRefusesWhatIsNotAUsableKey(t *testing.T) {
	cases := []struct{ entry, reason string }{
		{"", "an empty line"},
		{"# a comment", "a comment"},
		{"ssh-ed25519", "a type with no body"},
		{"ssh-ed25519 not-base64!!", "a body that is not base64"},
		// The type appears twice — in the line and inside the blob — and a
		// truncated paste rarely gets both right. This is the check that
		// catches one.
		{"ssh-rsa AAAAC3NzaC1lZDI1NTE5AAAAIOgGTTyStHYebiJCAytvGGjNEziXYMcqrKhTlb1anhyr wrong",
			"a type that disagrees with the blob"},
		{"ssh-dss AAAAB3NzaC1kc3MAAACBAP1 old", "an algorithm OpenSSH has removed"},
		// authorized_keys is one key per line, so a line break is not a long
		// key: it is a second entry, authorising somebody else.
		{edKey + "\n" + secondKey, "two keys on one line"},
		{edKey + "\x00", "a null byte"},
		// Options in front of a key are legitimate OpenSSH syntax and are how a
		// key becomes a forced command or a port forward. The panel writes
		// plain keys.
		{`command="/bin/sh" ` + edKey, "a forced command"},
		{`no-pty,permitopen="10.0.0.1:22" ` + edKey, "an options prefix"},
		{"ssh-ed25519 " + strings.Repeat("A", MaxKeyLength), "an oversized line"},
	}

	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			if _, err := ParseKey(tc.entry); err == nil {
				t.Fatalf("accepted: %s", tc.reason)
			}
		})
	}
}

func TestKeysAreAddedListedAndRemovedByFingerprint(t *testing.T) {
	p, account := provider(t)

	added, err := p.AddKey("operator", edKey)
	if err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if added.Fingerprint != edFP {
		t.Fatalf("fingerprint = %q", added.Fingerprint)
	}

	if _, err := p.AddKey("operator", rsaKey); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	keys, err := p.Keys("operator")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("keys = %d, want 2", len(keys))
	}

	// Removed by fingerprint, not by position: a line number changes when
	// another key is removed, and a fingerprint is the key.
	removed, err := p.RemoveKey("operator", edFP)
	if err != nil {
		t.Fatalf("RemoveKey: %v", err)
	}
	if removed.Comment != "operator@laptop" {
		t.Fatalf("removed = %q", removed.Comment)
	}

	keys, err = p.Keys("operator")
	if err != nil {
		t.Fatalf("Keys: %v", err)
	}
	if len(keys) != 1 || keys[0].Fingerprint != rsaFP {
		t.Fatalf("keys = %+v, want only the RSA key", keys)
	}

	// And what is on disk is what OpenSSH will read.
	content, err := os.ReadFile(filepath.Join(account.Home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.HasSuffix(string(content), "\n") {
		t.Fatal("the file does not end with a newline")
	}
	if strings.Contains(string(content), "operator@laptop") {
		t.Fatalf("the removed key is still in the file:\n%s", content)
	}
}

func TestTheSameKeyIsNotAddedTwice(t *testing.T) {
	p, _ := provider(t)

	if _, err := p.AddKey("operator", edKey); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	// The same key with a different comment is the same key: the fingerprint
	// is of the key material, and authorising it twice would show two rows that
	// cannot be told apart.
	if _, err := p.AddKey("operator", "ssh-ed25519 "+strings.Fields(edKey)[1]+" renamed"); err == nil {
		t.Fatal("the same key was authorised twice")
	}
}

// The file belongs to the account, not to the panel. A line this package cannot
// parse was put there by somebody else and stays exactly where it is.
func TestRemovingAKeyLeavesLinesThePanelDoesNotUnderstand(t *testing.T) {
	p, account := provider(t)

	dir := filepath.Join(account.Home, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	handWritten := "# my own key, do not touch\n" +
		`command="/usr/bin/backup" ` + secondKey + "\n" + edKey + "\n"
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(handWritten), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, err := p.RemoveKey("operator", edFP); err != nil {
		t.Fatalf("RemoveKey: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, "authorized_keys"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(content), "# my own key, do not touch") {
		t.Fatalf("a hand-written comment was lost:\n%s", content)
	}
	if !strings.Contains(string(content), `command="/usr/bin/backup"`) {
		t.Fatalf("a hand-written forced-command key was lost:\n%s", content)
	}
	if strings.Contains(string(content), "operator@laptop") {
		t.Fatalf("the key was not removed:\n%s", content)
	}
}

// sshd refuses to read an authorized_keys, or a .ssh directory, that anyone but
// the owner can write — and says so only in its own log. A key that silently
// does not work is the failure these modes avoid.
func TestTheKeyFileAndItsDirectoryArePrivate(t *testing.T) {
	p, account := provider(t)

	if _, err := p.AddKey("operator", edKey); err != nil {
		t.Fatalf("AddKey: %v", err)
	}

	dir, err := os.Stat(filepath.Join(account.Home, ".ssh"))
	if err != nil {
		t.Fatalf("stat .ssh: %v", err)
	}
	if perm := dir.Mode().Perm(); perm != 0o700 {
		t.Fatalf(".ssh mode = %o, want 700", perm)
	}

	file, err := os.Stat(filepath.Join(account.Home, ".ssh", "authorized_keys"))
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if perm := file.Mode().Perm(); perm != 0o600 {
		t.Fatalf("authorized_keys mode = %o, want 600", perm)
	}
}

func TestAnAccountThisHostDoesNotOfferIsRefused(t *testing.T) {
	p, _ := provider(t)

	// A website's account has a nologin shell and is not on the list; ../ is
	// not an account at all. Both get the same answer, because the name is
	// matched against the host's own list rather than turned into a path.
	for _, name := range []string{"web_shop_example", "root", "../../etc", ""} {
		if _, err := p.Keys(name); err == nil {
			t.Fatalf("%q was accepted as an account", name)
		}
	}
}

// ------------------------------------------------------- effective config

// The output `sshd -T` prints, copied from OpenSSH 9.9 rather than invented.
const effectiveOutput = `port 22
addressfamily any
listenaddress [::]:22
logingracetime 120
permitrootlogin without-password
maxauthtries 6
pubkeyauthentication yes
passwordauthentication yes
permitemptypasswords no
x11forwarding no
usepam yes
subsystem sftp /usr/lib/ssh/sftp-server
`

func TestEffectiveConfigIsReadFromWhatTheServerResolved(t *testing.T) {
	var config Config
	applyEffective(&config, effectiveOutput)

	if len(config.Ports) != 1 || config.Ports[0] != 22 {
		t.Fatalf("ports = %v, want [22]", config.Ports)
	}
	// sshd prints "without-password" for what the documentation calls
	// "prohibit-password". Both mean the same thing and the panel maps them.
	if config.RootLogin != "without-password" {
		t.Fatalf("root login = %q", config.RootLogin)
	}
	if normaliseRootLogin(config.RootLogin) != "prohibit-password" {
		t.Fatalf("normalised = %q", normaliseRootLogin(config.RootLogin))
	}
	if !config.PasswordAuthentication || !config.PubkeyAuthentication {
		t.Fatal("authentication methods were misread")
	}
	if config.PermitEmptyPasswords || config.X11Forwarding {
		t.Fatal("a \"no\" was read as a yes")
	}
	if config.MaxAuthTries != 6 || config.LoginGraceTime != 120 {
		t.Fatalf("numbers = %d, %d", config.MaxAuthTries, config.LoginGraceTime)
	}
}

// A drop-in written where nothing includes it is a change that reports success
// and does nothing, which is the worst outcome this package could produce.
func TestADropInIsRefusedWhereNothingWouldIncludeIt(t *testing.T) {
	p, _ := provider(t)

	if err := os.WriteFile(p.ConfigPath(), []byte("Port 22\nPermitRootLogin yes\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if supported, reason := p.dropInSupported(); supported {
		t.Fatal("a config with no Include was reported as writable")
	} else if !strings.Contains(reason, "Include") {
		t.Fatalf("reason = %q, want it to name the missing Include", reason)
	}

	if err := os.WriteFile(p.ConfigPath(),
		[]byte("Include /etc/ssh/sshd_config.d/*.conf\nPort 22\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if supported, reason := p.dropInSupported(); !supported {
		t.Fatalf("a config with an Include was reported as unwritable: %s", reason)
	}
}

// ------------------------------------------------------------ the audit

func TestAuditReportsWhatIsWorthChanging(t *testing.T) {
	config := Config{
		Available:              true,
		Running:                true,
		Ports:                  []int{22},
		RootLogin:              "yes",
		PasswordAuthentication: true,
		PubkeyAuthentication:   true,
		MaxAuthTries:           10,
		X11Forwarding:          true,
		PermitEmptyPasswords:   true,
	}

	found := map[string]Finding{}
	for _, finding := range Audit(config, nil) {
		found[finding.ID] = finding
	}

	for _, id := range []string{
		"ssh.empty-passwords", "ssh.root-login", "ssh.password-authentication",
		"ssh.no-keys", "ssh.max-auth-tries", "ssh.x11-forwarding", "ssh.default-port",
	} {
		if _, ok := found[id]; !ok {
			t.Fatalf("%s was not reported", id)
		}
	}

	if found["ssh.empty-passwords"].Severity != SeverityHigh {
		t.Fatal("empty passwords is not a warning, it is a hole")
	}
	// Moving the port is worth doing and is not a security control. Saying so
	// is the difference between advice and theatre.
	if found["ssh.default-port"].Severity != SeverityInfo {
		t.Fatal("the default port was reported as more than informational")
	}
}

// Telling somebody with no keys to turn off password authentication is telling
// them to lock themselves out. The order matters, so the advice says it.
func TestAuditTellsYouToAddAKeyBeforeTurningPasswordsOff(t *testing.T) {
	config := Config{Available: true, Running: true, Ports: []int{22},
		PasswordAuthentication: true, PubkeyAuthentication: true, RootLogin: "no"}

	for _, finding := range Audit(config, nil) {
		if finding.ID != "ssh.password-authentication" {
			continue
		}
		if !strings.Contains(finding.Action, "Add an SSH key first") {
			t.Fatalf("action = %q, want it to say keys come first", finding.Action)
		}
		return
	}
	t.Fatal("password authentication was not reported")
}

func TestAuditSaysNothingAboutAHostWithNoSSH(t *testing.T) {
	if findings := Audit(Config{Available: false}, nil); len(findings) != 0 {
		t.Fatalf("findings = %+v, want none for a host with no SSH server", findings)
	}
}

// ----------------------------------------------------------- the refusals

func TestTurningOffPasswordsWithNoKeyIsRefused(t *testing.T) {
	p, _ := provider(t)

	before := Config{Available: true, Managed: true, PasswordAuthentication: true,
		PubkeyAuthentication: true, Ports: []int{22}}
	off := false

	err := p.checkLockout(context.Background(), Change{PasswordAuthentication: &off}, before, nil)
	if err == nil {
		t.Fatal("password authentication was turned off on a host with no keys")
	}
	if !strings.Contains(err.Error(), "authorised SSH key") {
		t.Fatalf("error = %q, want it to say why", err)
	}

	// With a key, the same change is allowed: the refusal is about the state of
	// the host, not about the setting.
	if _, err := p.AddKey("operator", edKey); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := p.checkLockout(context.Background(), Change{PasswordAuthentication: &off}, before, nil); err != nil {
		t.Fatalf("refused with a key present: %v", err)
	}
}

func TestTurningOffBothAuthenticationMethodsIsRefused(t *testing.T) {
	p, _ := provider(t)

	before := Config{Available: true, Managed: true, PasswordAuthentication: false,
		PubkeyAuthentication: true, Ports: []int{22}}
	off := false

	if err := p.checkLockout(context.Background(), Change{PubkeyAuthentication: &off}, before, nil); err == nil {
		t.Fatal("both authentication methods were turned off")
	}
}

// closedFirewall refuses every port.
type closedFirewall struct{}

func (closedFirewall) PortReachable(_ context.Context, port int) (bool, string) {
	return false, fmt.Sprintf("the firewall does not allow connections to port %d", port)
}

func TestMovingTheportSomewhereTheFirewallBlocksIsRefused(t *testing.T) {
	p, _ := provider(t)

	before := Config{Available: true, Managed: true, Ports: []int{22},
		PasswordAuthentication: true, PubkeyAuthentication: true}
	port := 2222

	err := p.checkLockout(context.Background(), Change{Port: &port}, before, closedFirewall{})
	if err == nil {
		t.Fatal("the port was moved somewhere nothing could reach")
	}
	// The panel does not open the port as a side effect — a firewall change is
	// its own deliberate act — so the refusal has to say what to do.
	if !strings.Contains(err.Error(), "firewall first") {
		t.Fatalf("error = %q, want it to say what to do first", err)
	}

	// The port it is already on needs no firewall check: nothing is changing.
	same := 22
	if err := p.checkLockout(context.Background(), Change{Port: &same}, before, closedFirewall{}); err != nil {
		t.Fatalf("refused a port that is already in use: %v", err)
	}
}

func TestRefusingRootLoginsOnAHostWithNoOtherAccountIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := NewProvider(Options{
		Dir: dir,
		Accounts: fakeAccounts{accounts: []Account{
			{Name: "root", UID: 0, GID: 0, Home: filepath.Join(dir, "root"), Shell: "/bin/sh"},
		}},
	})

	before := Config{Available: true, Managed: true, Ports: []int{22},
		RootLogin: "yes", PasswordAuthentication: true, PubkeyAuthentication: true}
	no := "no"

	if err := p.checkLockout(context.Background(), Change{RootLogin: &no}, before, nil); err == nil {
		t.Fatal("root logins were refused on a host where root is the only account")
	}
}

// ---------------------------------------------------------- the drop-in

func TestTheDropInHoldsOnlyWhatThePanelManages(t *testing.T) {
	p, _ := provider(t)

	rendered := render(map[string]string{
		"PermitRootLogin":        "prohibit-password",
		"PasswordAuthentication": "no",
	})

	if !strings.Contains(rendered, "PermitRootLogin prohibit-password") {
		t.Fatalf("rendered:\n%s", rendered)
	}
	if !strings.Contains(rendered, "PasswordAuthentication no") {
		t.Fatalf("rendered:\n%s", rendered)
	}
	// Nothing the caller did not ask for: a directive written at a default
	// would pin a value the distribution may later change for good reason.
	if strings.Contains(rendered, "Port ") || strings.Contains(rendered, "X11Forwarding") {
		t.Fatalf("the file sets directives nobody asked for:\n%s", rendered)
	}

	if err := p.install(rendered); err != nil {
		t.Fatalf("install: %v", err)
	}
	info, err := os.Stat(p.DropInPath())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %o, want 600", perm)
	}
}

// A change that sets one directive must not drop another the panel set before:
// the file is rewritten whole, so changing the port would otherwise turn
// password authentication back on.
func TestAChangeKeepsWhatThePanelSetPreviously(t *testing.T) {
	p, _ := provider(t)

	if err := p.install(render(map[string]string{"PasswordAuthentication": "no"})); err != nil {
		t.Fatalf("install: %v", err)
	}

	port := 2222
	merged := p.merge(Change{Port: &port})

	if merged["PasswordAuthentication"] != "no" {
		t.Fatalf("merged = %v, want the previous setting kept", merged)
	}
	if merged["Port"] != "2222" {
		t.Fatalf("merged = %v, want the new port", merged)
	}
}

func TestDisagreementsReportWhatTheServerDidNotAdopt(t *testing.T) {
	desired := map[string]string{
		"PasswordAuthentication": "no",
		"Port":                   "2222",
	}

	// The server adopted neither: something else on the host sets them.
	after := Config{Ports: []int{22}, PasswordAuthentication: true}
	missing := disagreements(desired, after)
	if len(missing) != 2 {
		t.Fatalf("missing = %q, want both reported", missing)
	}

	// And when it did adopt them, nothing is reported.
	after = Config{Ports: []int{2222}, PasswordAuthentication: false}
	if missing := disagreements(desired, after); len(missing) != 0 {
		t.Fatalf("missing = %q, want none", missing)
	}
}
