package mail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jothost/panel/agent/internal/testsupport"
)

// A domain with everything on it, used by most of the tests below.
func fixture() Desired {
	return Desired{
		Settings: Settings{
			Enabled:         true,
			Hostname:        "mail.example.com",
			TLSCertificate:  "/etc/jothost/ssl/example.com/fullchain.pem",
			TLSKey:          "/etc/jothost/ssl/example.com/privkey.pem",
			RequireTLS:      true,
			SpamEnabled:     true,
			SpamRejectScore: 15,
			MaxMessageMB:    25,
		},
		Domains: []Domain{{
			Name:         "example.com",
			Active:       true,
			DKIMSelector: "jh202609",
			Mailboxes: []Mailbox{
				{
					LocalPart:    "sales",
					PasswordHash: "{SHA512-CRYPT}$6$rounds=25000$abcdefghijklmnop$hash",
					QuotaMB:      2048,
					Active:       true,
				},
				{
					LocalPart:    "old",
					PasswordHash: "{SHA512-CRYPT}$6$rounds=25000$qrstuvwxyzabcdef$hash",
					Active:       false,
				},
			},
			Aliases: []Alias{
				{Source: "info", Destination: "sales@example.com", Active: true},
				{Source: "gone", Destination: "nobody@example.net", Active: false},
			},
		}},
	}
}

func TestTheDomainTableHoldsOnlyActiveDomains(t *testing.T) {
	desired := fixture()
	desired.Domains = append(desired.Domains, Domain{Name: "paused.example", Active: false})

	provider := &Provider{paths: DefaultPaths()}
	domains, _, _, err := provider.buildMaps(desired)
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}

	if !strings.Contains(string(domains), "example.com") {
		t.Error("the active domain is missing from the domain table")
	}
	if strings.Contains(string(domains), "paused.example") {
		// An inactive domain in the table is a domain the server accepts mail
		// for and has nowhere to put it.
		t.Error("an inactive domain was written into the domain table")
	}
}

func TestASuspendedMailboxIsAbsentFromEveryTable(t *testing.T) {
	provider := &Provider{paths: DefaultPaths()}
	_, mailboxes, _, err := provider.buildMaps(fixture())
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}
	if strings.Contains(string(mailboxes), "old@example.com") {
		t.Error("a suspended mailbox is still in the mailbox table, so it still receives")
	}

	passwd, err := buildPasswdFile(fixture(), 5000, 5000, "/var/mail/vhosts")
	if err != nil {
		t.Fatalf("buildPasswdFile: %v", err)
	}
	if strings.Contains(string(passwd), "old@example.com") {
		t.Error("a suspended mailbox is still in the passwd file, so it can still log in")
	}
	if !strings.Contains(string(passwd), "sales@example.com") {
		t.Error("an active mailbox is missing from the passwd file")
	}
}

func TestAnInactiveAliasDoesNotForward(t *testing.T) {
	provider := &Provider{paths: DefaultPaths()}
	_, _, aliases, err := provider.buildMaps(fixture())
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}
	if !strings.Contains(string(aliases), "info@example.com sales@example.com") {
		t.Errorf("the active forwarder is missing:\n%s", aliases)
	}
	if strings.Contains(string(aliases), "gone@example.com") {
		t.Error("an inactive forwarder is still forwarding")
	}
}

func TestTheCatchAllIsWrittenInPostfixSyntax(t *testing.T) {
	desired := fixture()
	desired.Domains[0].CatchAll = "sales@example.com"

	provider := &Provider{paths: DefaultPaths()}
	_, _, aliases, err := provider.buildMaps(desired)
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}
	if !strings.Contains(string(aliases), "@example.com sales@example.com") {
		t.Errorf("the catch-all is not in the table:\n%s", aliases)
	}
}

func TestTheTablesAreSortedSoAReconcileThatChangedNothingWritesNothing(t *testing.T) {
	// Not tidiness. The generated file is compared against the one on disk to
	// decide whether the daemons need restarting, so an unsorted file would
	// differ every time and restart the mail server on a timer.
	desired := fixture()
	desired.Domains[0].Mailboxes = append(desired.Domains[0].Mailboxes, Mailbox{
		LocalPart:    "aaa",
		PasswordHash: "{SHA512-CRYPT}$6$rounds=25000$0000000000000000$hash",
		Active:       true,
	})

	provider := &Provider{paths: DefaultPaths()}
	_, first, _, err := provider.buildMaps(desired)
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}

	// Reverse the input order and expect the same file.
	boxes := desired.Domains[0].Mailboxes
	for i, j := 0, len(boxes)-1; i < j; i, j = i+1, j-1 {
		boxes[i], boxes[j] = boxes[j], boxes[i]
	}
	_, second, _, err := provider.buildMaps(desired)
	if err != nil {
		t.Fatalf("buildMaps: %v", err)
	}
	if string(first) != string(second) {
		t.Error("the table depends on the order the mailboxes arrived in, so every " +
			"reconcile would look like a change")
	}
}

func TestAValueThatWouldBreakTheTableFormatIsRefused(t *testing.T) {
	// The last line of defence. Everything reaching here has been validated,
	// and this is the check that would catch a validation that stopped being
	// applied — because the file is read by a process running as root, and a
	// value with a space in it silently becomes a different entry.
	desired := fixture()
	desired.Domains[0].Aliases = []Alias{
		{Source: "info", Destination: "somebody@example.net extra", Active: true},
	}

	provider := &Provider{paths: DefaultPaths()}
	if _, _, _, err := provider.buildMaps(desired); err == nil {
		t.Error("a destination containing a space was written into the alias table")
	}
}

func TestAPasswordHashWithAColonIsRefused(t *testing.T) {
	// The passwd-file is colon-delimited: a colon inside the hash field would
	// end it early and give the *next* field — the uid — a value the panel did
	// not write.
	desired := fixture()
	desired.Domains[0].Mailboxes[0].PasswordHash = "{SHA512-CRYPT}$6$bad:hash"

	if _, err := buildPasswdFile(desired, 5000, 5000, "/var/mail/vhosts"); err == nil {
		t.Error("a password hash containing a colon was written into the passwd file")
	}
}

func TestAQuotaBecomesADovecotRule(t *testing.T) {
	passwd, err := buildPasswdFile(fixture(), 5000, 5000, "/var/mail/vhosts")
	if err != nil {
		t.Fatalf("buildPasswdFile: %v", err)
	}
	if !strings.Contains(string(passwd), "userdb_quota_rule=*:storage=2048M") {
		t.Errorf("the mailbox quota is not in the passwd file:\n%s", passwd)
	}
	if !strings.Contains(string(passwd), "userdb_mail=maildir:/var/mail/vhosts/example.com/sales") {
		t.Errorf("the mailbox location is not in the passwd file:\n%s", passwd)
	}
}

func TestAnUnlimitedMailboxHasNoQuotaRule(t *testing.T) {
	desired := fixture()
	desired.Domains[0].Mailboxes[0].QuotaMB = 0

	passwd, err := buildPasswdFile(desired, 5000, 5000, "/var/mail/vhosts")
	if err != nil {
		t.Fatalf("buildPasswdFile: %v", err)
	}
	if strings.Contains(string(passwd), "quota_rule") {
		t.Error("an unlimited mailbox was given a quota rule, which would be a limit of zero")
	}
}

// ------------------------------------------------------------------ Postfix

func TestTheVirtualDomainsAreNotAlsoLocalDestinations(t *testing.T) {
	// The mistake that most often breaks a virtual mailbox setup. A domain in
	// both mydestination and virtual_mailbox_domains is delivered locally, to a
	// system account that does not exist — so mail bounces with "unknown user"
	// for a mailbox the panel is showing as present.
	provider := &Provider{paths: DefaultPaths()}
	values := provider.mainSettings(fixture(), "lmdb", true)

	if strings.Contains(values["mydestination"], "example.com") {
		t.Errorf("a virtual domain is in mydestination: %s", values["mydestination"])
	}
}

func TestRelayingIsRefusedBeforeAnythingCanPermitIt(t *testing.T) {
	// The single most important setting in the phase. Postfix stops at the
	// first match, so the order *is* the policy.
	provider := &Provider{paths: DefaultPaths()}
	values := provider.mainSettings(fixture(), "lmdb", true)

	restrictions := values["smtpd_relay_restrictions"]
	if !strings.HasSuffix(restrictions, "defer_unauth_destination") {
		t.Errorf("the relay restrictions do not end by refusing: %q", restrictions)
	}
	for _, permit := range []string{"permit_mynetworks", "permit_sasl_authenticated"} {
		if !strings.Contains(restrictions, permit) {
			t.Errorf("the relay restrictions do not permit %s, so nothing can send", permit)
		}
	}
	if values["mynetworks"] != "127.0.0.0/8 [::1]/128" {
		t.Errorf("mynetworks is wider than the loopback: %q", values["mynetworks"])
	}
}

func TestPortTwentyFiveNeverOffersAuthentication(t *testing.T) {
	// A mail server does not authenticate the other mail servers of the world.
	// Offering AUTH on 25 invites a password-guessing campaign against every
	// mailbox on the host.
	provider := &Provider{paths: DefaultPaths()}
	values := provider.mainSettings(fixture(), "lmdb", true)
	if values["smtpd_sasl_auth_enable"] != "no" {
		t.Error("port 25 offers authentication")
	}
}

func TestSubmissionRequiresBothEncryptionAndAuthentication(t *testing.T) {
	services := submissionServices(fixture().Settings)
	if len(services) != 2 {
		t.Fatalf("expected both submission ports, got %d", len(services))
	}
	for _, service := range services {
		if service.overrides["smtpd_tls_security_level"] != "encrypt" &&
			service.overrides["smtpd_tls_wrappermode"] != "yes" {
			t.Errorf("%s does not require encryption", service.name)
		}
		if service.overrides["smtpd_client_restrictions"] != "permit_sasl_authenticated,reject" {
			t.Errorf("%s accepts unauthenticated clients", service.name)
		}
	}
}

func TestWithoutACertificateNoSubmissionPortIsOffered(t *testing.T) {
	// The honest answer rather than a port that would take a password in the
	// clear. The panel reports it as a warning; it does not quietly downgrade.
	settings := fixture().Settings
	settings.TLSCertificate = ""
	settings.TLSKey = ""

	if services := submissionServices(settings); len(services) != 0 {
		t.Errorf("a submission port was offered with no certificate: %d", len(services))
	}
}

func TestTurningTheTLSRequirementOffOffersOnlyTheStartTLSPort(t *testing.T) {
	settings := fixture().Settings
	settings.TLSCertificate = ""
	settings.TLSKey = ""
	settings.RequireTLS = false

	services := submissionServices(settings)
	if len(services) != 1 || services[0].port != 587 {
		t.Fatalf("expected only port 587, got %+v", services)
	}
	if services[0].overrides["smtpd_tls_security_level"] != "none" {
		t.Error("the port claims encryption it cannot provide")
	}
}

func TestTheFilterIsRequiredRatherThanOptionalWhenItIsConfigured(t *testing.T) {
	// tempfail rather than accept: an unsigned message cannot be recalled, and
	// a deferred one is retried for days. See mainSettings.
	provider := &Provider{paths: DefaultPaths(), runner: nil}
	values := provider.mainSettings(fixture(), "lmdb", true)
	// With no runner the provider reports no filtering support, so the milter
	// is off — which is itself the right answer, and is asserted here so the
	// pairing is deliberate rather than accidental.
	if values["smtpd_milters"] != "" {
		t.Error("a milter was configured on a host with no filter installed")
	}
	if values["milter_default_action"] != "accept" {
		t.Error("a host with no milter should not defer mail waiting for one")
	}
}

func TestPostconfOutputIsParsedWithItsContinuationLines(t *testing.T) {
	// A long restriction list is wrapped onto the next line by postconf. Read
	// naively, the continuation looks like a key with no name — and the panel
	// would then rewrite the setting on every reconcile because it never
	// matched what it set.
	parsed := parsePostconf(strings.Join([]string{
		"myhostname = mail.example.com",
		"smtpd_relay_restrictions = permit_mynetworks",
		"    permit_sasl_authenticated",
		"    defer_unauth_destination",
		"mynetworks = 127.0.0.0/8",
	}, "\n"))

	if parsed["myhostname"] != "mail.example.com" {
		t.Errorf("myhostname: %q", parsed["myhostname"])
	}
	want := "permit_mynetworks permit_sasl_authenticated defer_unauth_destination"
	if parsed["smtpd_relay_restrictions"] != want {
		t.Errorf("continuation lines were not joined:\n got: %q\nwant: %q",
			parsed["smtpd_relay_restrictions"], want)
	}
	if parsed["mynetworks"] != "127.0.0.0/8" {
		t.Errorf("the key after a continuation was lost: %q", parsed["mynetworks"])
	}
}

// ------------------------------------------------------------------ Dovecot

func TestWithoutACertificatePlaintextAuthenticationIsAllowed(t *testing.T) {
	// Not because it is good. A host with no certificate and plaintext
	// authentication disabled is one where nobody can log in at all, which is
	// a worse answer than a warning. The panel warns loudly instead.
	settings := fixture().Settings
	settings.TLSCertificate = ""
	settings.TLSKey = ""

	conf := string(buildDovecotConf(settings, DefaultPaths(), 5000, 5000))
	if !strings.Contains(conf, "disable_plaintext_auth = no") {
		t.Error("a host with no certificate would refuse every login")
	}
	if !strings.Contains(conf, "ssl = no") {
		t.Error("Dovecot was told to use TLS with no certificate to present")
	}
}

func TestWithACertificatePasswordsMayNotCrossInTheClear(t *testing.T) {
	conf := string(buildDovecotConf(fixture().Settings, DefaultPaths(), 5000, 5000))
	if !strings.Contains(conf, "disable_plaintext_auth = yes") {
		t.Error("passwords may cross an unencrypted connection")
	}
	if !strings.Contains(conf, "ssl_min_protocol = TLSv1.2") {
		t.Error("Dovecot was left accepting obsolete TLS versions")
	}
}

func TestTheUsernameLookupIsCaseInsensitive(t *testing.T) {
	// Without this, everybody who typed their own address with a capital
	// letter cannot log in — and the error they see is "password incorrect".
	conf := string(buildDovecotConf(fixture().Settings, DefaultPaths(), 5000, 5000))
	if !strings.Contains(conf, "username_format=%Lu") {
		t.Error("the passwd-file lookup is case-sensitive")
	}
}

// -------------------------------------------------------------------- Sieve

func TestAVacationMessageCannotEscapeIntoScript(t *testing.T) {
	// The one genuinely dangerous piece of text in this phase. A bare double
	// quote ends a Sieve string, and everything after it is script: an
	// unescaped subject of `x"; discard; #` would be a mailbox that silently
	// deletes its own mail.
	script := string(buildSieve("sales@example.com", Autoresponder{
		Subject:      `Away "until" Monday`,
		Body:         "Back soon.\nBest, \\Sales\"",
		IntervalDays: 7,
		Active:       true,
	}))

	if strings.Contains(script, `"Away "until" Monday"`) {
		t.Error("the subject's quotes were not escaped")
	}
	if !strings.Contains(script, `:subject "Away \"until\" Monday"`) {
		t.Errorf("the subject was not escaped as expected:\n%s", script)
	}
	if !strings.Contains(script, `Best, \\Sales\"`) {
		t.Errorf("the body's backslash and quote were not escaped:\n%s", script)
	}
	if !strings.Contains(script, ":days 7") {
		t.Error("the reply interval is missing, so it would reply to every message")
	}
}

func TestAVacationReplyCoversTheAddressesMailArrivesThrough(t *testing.T) {
	// Without :addresses, Sieve replies only when the message was addressed to
	// the login address itself — so mail that arrived through a forwarder gets
	// no reply, which is exactly the mail an away message is for.
	script := string(buildSieve("sales@example.com", Autoresponder{
		Subject: "Away", Body: "Back soon.", IntervalDays: 3, Active: true,
	}))
	if !strings.Contains(script, `:addresses ["sales@example.com"]`) {
		t.Errorf("the reply does not cover the mailbox's own address:\n%s", script)
	}
}

// --------------------------------------------------------------------- DKIM

func TestGeneratingAKeyLeavesThePrivateHalfOnTheHostOnly(t *testing.T) {
	testsupport.RequireRoot(t)

	dir := t.TempDir()
	provider := &Provider{
		paths:    Paths{StateDir: dir, MailRoot: filepath.Join(dir, "mail")}.withDefaults(),
		accounts: fakeAccounts{},
	}
	provider.paths.StateDir = dir
	provider.paths.MailRoot = filepath.Join(dir, "mail")

	key, err := provider.GenerateDKIM(context.Background(), "example.com", "jh202609")
	if err != nil {
		t.Fatalf("GenerateDKIM: %v", err)
	}

	if key.PublicKey == "" {
		t.Fatal("no public key was returned, so nothing can be published")
	}
	if strings.Contains(key.PublicKey, "PRIVATE") {
		t.Fatal("the private key was returned to the caller")
	}
	if key.Bits != DKIMBits {
		t.Errorf("key size: got %d, want %d", key.Bits, DKIMBits)
	}

	info, err := os.Stat(key.KeyPath)
	if err != nil {
		t.Fatalf("the private key was not written: %v", err)
	}
	// 0640, not 0644: Rspamd reads it as its own user and nothing else on the
	// host has any business reading a signing key.
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("the private key is mode %o, which is readable by more than the signer", mode)
	}

	contents, err := os.ReadFile(key.KeyPath)
	if err != nil {
		t.Fatalf("read the key: %v", err)
	}
	if !strings.HasPrefix(string(contents), "-----BEGIN PRIVATE KEY-----") {
		t.Error("the key on disk is not a PEM private key")
	}
}

func TestADomainWithNoKeyOnDiskIsNotReportedAsSigning(t *testing.T) {
	// The phase's central distinction, at its smallest. A selector recorded in
	// the panel is a claim; a key on this host is a fact.
	dir := t.TempDir()
	provider := &Provider{paths: Paths{StateDir: dir}.withDefaults()}
	provider.paths.StateDir = dir

	if signing := provider.signingDomains(fixture()); len(signing) != 0 {
		t.Errorf("a domain with no key was reported as signing: %v", signing)
	}
}

func TestTheSelectorMapNamesOnlyDomainsThatHaveOne(t *testing.T) {
	desired := fixture()
	desired.Domains = append(desired.Domains, Domain{Name: "unsigned.example", Active: true})

	rendered := string(buildDKIMMap(desired))
	if !strings.Contains(rendered, "example.com jh202609") {
		t.Errorf("the signing domain is missing:\n%s", rendered)
	}
	if strings.Contains(rendered, "unsigned.example") {
		t.Error("a domain with no selector is in the signing map")
	}
}

// ---------------------------------------------------------------- validation

func TestADomainThatAppearsTwiceIsRefused(t *testing.T) {
	desired := fixture()
	desired.Domains = append(desired.Domains, Domain{Name: "example.com", Active: true})
	if err := checkDesired(desired); err == nil {
		t.Error("a duplicated domain was accepted, so the second would decide the mailboxes")
	}
}

func TestMailCannotBeSwitchedOnWithoutAQualifiedHostname(t *testing.T) {
	// A host greeting the world as "localhost" has its mail refused by most of
	// it, and the refusal arrives days later as "some people never got my
	// email".
	desired := fixture()
	desired.Settings.Hostname = "localhost"
	if err := checkDesired(desired); err == nil {
		t.Error("mail was switched on with an unqualified hostname")
	}
}

func TestTurningMailOffNeedsNoOtherSetting(t *testing.T) {
	// Disabling must never be blocked by a validation failure in the settings
	// being disabled: a half-configured mail server is exactly the one an
	// operator wants to be able to turn off.
	if err := checkDesired(Desired{}); err != nil {
		t.Errorf("mail could not be turned off: %v", err)
	}
}

// ------------------------------------------------------------------- webmail

func TestAWebmailArchiveCannotWriteOutsideItsDirectory(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{
		"../escape",
		"/etc/passwd",
		"a/../../escape",
		"back\\slash",
	} {
		if _, err := memberPath(root, name); err == nil {
			t.Errorf("memberPath accepted %q", name)
		}
	}
	if _, err := memberPath(root, "roundcubemail-1.6.9/index.php"); err != nil {
		t.Errorf("memberPath refused a good name: %v", err)
	}
}

func TestTheWebmailConfigurationDisablesItsOwnInstaller(t *testing.T) {
	// The installer is a page that can rewrite the configuration and connect to
	// arbitrary hosts. Leaving it reachable is how a webmail installation
	// becomes somebody else's.
	config, err := buildWebmailConfig(WebmailRequest{
		Domain: "webmail.example.com", IMAPHost: "localhost", SMTPHost: "localhost",
	})
	if err != nil {
		t.Fatalf("buildWebmailConfig: %v", err)
	}
	if !strings.Contains(string(config), "$config['enable_installer'] = false;") {
		t.Error("the webmail installer is still reachable")
	}
	if !strings.Contains(string(config), "ssl://localhost:993") {
		t.Error("webmail was not pointed at the encrypted IMAP port")
	}
}

func TestAWebmailValueThatWouldEscapeThePHPStringIsRefused(t *testing.T) {
	if _, err := buildWebmailConfig(WebmailRequest{
		Domain: "webmail.example.com'; system('id'); //",
	}); err == nil {
		t.Error("a domain containing a quote was written into a PHP file")
	}
}

// --------------------------------------------------------------------- queue

func TestTheQueueReportsItsOldestMessageAndNotJustItsDepth(t *testing.T) {
	// The count alone cannot tell a busy server from a broken one. Two hundred
	// messages moving through is health; two hundred that have been there since
	// Tuesday is an outage.
	original := nowUnix
	nowUnix = func() int64 { return 1_000_000 }
	defer func() { nowUnix = original }()

	count, oldest := parseQueue(strings.Join([]string{
		`{"queue_name":"deferred","queue_id":"A1","arrival_time":999000}`,
		`{"queue_name":"active","queue_id":"B2","arrival_time":999900}`,
	}, "\n"))

	if count != 2 {
		t.Errorf("queue length: got %d, want 2", count)
	}
	if oldest != 1000 {
		t.Errorf("oldest message age: got %d, want 1000", oldest)
	}
}

func TestAnEmptyQueueHasNoAge(t *testing.T) {
	count, oldest := parseQueue("")
	if count != 0 || oldest != 0 {
		t.Errorf("an empty queue reported %d messages, oldest %d", count, oldest)
	}
}

// --------------------------------------------------------------------- quota

func TestQuotaIsReadFromTheStorageRowAndNotTheMessageCount(t *testing.T) {
	// Reading the MESSAGE row as a size would report a mailbox holding three
	// large messages as three kilobytes.
	used, limit, err := parseQuota(strings.Join([]string{
		"Quota name\tType\tValue\tLimit\t%",
		"User quota\tSTORAGE\t2048\t102400\t2",
		"User quota\tMESSAGE\t3\t-\t0",
	}, "\n"))
	if err != nil {
		t.Fatalf("parseQuota: %v", err)
	}
	if used != 2 {
		t.Errorf("used: got %d MB, want 2", used)
	}
	if limit != 100 {
		t.Errorf("limit: got %d MB, want 100", limit)
	}
}

func TestAnUnlimitedMailboxReportsNoLimit(t *testing.T) {
	_, limit, err := parseQuota("User quota\tSTORAGE\t2048\t-\t0")
	if err != nil {
		t.Fatalf("parseQuota: %v", err)
	}
	if limit != 0 {
		t.Errorf("an unlimited mailbox reported a limit of %d", limit)
	}
}

// fakeAccounts stands in for the host's account tool.
type fakeAccounts struct{}

func (fakeAccounts) EnsureAccount(context.Context, string, string) (int, int, error) {
	return 5000, 5000, nil
}

func TestTheSocketsPostfixReadsAreNamedAbsolutely(t *testing.T) {
	// Found by starting the daemon rather than by reading the manual. A
	// relative unix_listener path is resolved against *Dovecot's* base
	// directory, not Postfix's queue — so the socket lands somewhere Postfix
	// cannot see, and Dovecot then refuses to start at all because the
	// directory does not exist.
	conf := string(buildDovecotConf(fixture().Settings, DefaultPaths(), 5000, 5000))

	for _, socket := range []string{
		"unix_listener /var/spool/postfix/private/auth {",
		"unix_listener /var/spool/postfix/private/dovecot-lmtp {",
	} {
		if !strings.Contains(conf, socket) {
			t.Errorf("missing an absolute socket path:\n%s\nin:\n%s", socket, conf)
		}
	}
}

func TestVirusScanningIsSwitchedOffAtTheModuleAndNotTheRule(t *testing.T) {
	// Rspamd refuses to load an antivirus rule that names no servers — "cannot
	// add AV rule" — and that is a configuration *error*, not a warning. It
	// fails the config check, so a panel writing a disabled rule would refuse
	// every mail change on any host with Rspamd and virus scanning off, which
	// is the default. Found by running Rspamd's own validator.
	off := string(buildAntivirus(false))
	if strings.Contains(off, "clamav {") {
		t.Errorf("a disabled rule was written instead of disabling the module:\n%s", off)
	}
	if !strings.Contains(off, "enabled = false;") {
		t.Errorf("the module was not disabled:\n%s", off)
	}

	on := string(buildAntivirus(true))
	if !strings.Contains(on, "servers = ") {
		t.Errorf("an enabled rule names no servers, which Rspamd refuses to load:\n%s", on)
	}
}

func TestDovecotIsToldWhichAccountMayHoldMail(t *testing.T) {
	// Dovecot refuses to open a mailbox for a uid below first_valid_uid, which
	// defaults to 500 — and the mail account is a system account, so its uid is
	// below that everywhere. The symptom is delivery deferred with "Mail access
	// for users with UID n not permitted", which reads as a problem with the
	// Maildir's permissions and is not one. Found by delivering a real message.
	conf := string(buildDovecotConf(fixture().Settings, DefaultPaths(), 105, 105))

	for _, setting := range []string{
		"first_valid_uid = 105",
		"last_valid_uid = 105",
		"first_valid_gid = 105",
		"last_valid_gid = 105",
	} {
		if !strings.Contains(conf, setting) {
			t.Errorf("missing %q — mail would be deferred, not delivered", setting)
		}
	}
}
