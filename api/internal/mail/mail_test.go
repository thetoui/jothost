package mail

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jothost/panel/api/internal/servers"
	"github.com/jothost/panel/api/internal/testsupport"
	"github.com/jothost/panel/shared/crypt"
	"github.com/jothost/panel/shared/validate"
)

func setup(t *testing.T) (*Repository, string, context.Context) {
	t.Helper()

	deps := testsupport.Require(t)
	ctx := context.Background()

	server, err := servers.NewRepository(deps.Pool).Register(ctx, servers.RegisterParams{
		Hostname: "mail-test-host",
	})
	if err != nil {
		t.Fatalf("register server: %v", err)
	}
	return NewRepository(deps.Pool), server.ID, ctx
}

func makeDomain(t *testing.T, repo *Repository, ctx context.Context,
	serverID, name string,
) Domain {
	t.Helper()
	domain, err := repo.CreateDomain(ctx, Domain{
		ServerID: serverID, Domain: name, Active: true,
		SPFPolicy: validate.SPFSoft, DMARCPolicy: validate.DMARCNone,
	})
	if err != nil {
		t.Fatalf("create domain %s: %v", name, err)
	}
	return domain
}

func hash(t *testing.T, password string) string {
	t.Helper()
	value, err := crypt.SchemedHash(password)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return value
}

// ------------------------------------------------------------------ settings

func TestTheDefaultsAreTheOnesTheMigrationChose(t *testing.T) {
	// Read through the repository rather than asserted against a zero value,
	// because a caller reading a zero-valued struct would see the opposite of
	// all three: TLS not required, spam filtering off, and a message size limit
	// of nothing.
	repo, serverID, ctx := setup(t)

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.Enabled {
		t.Error("mail is on by default, which is not a thing to switch on by accident")
	}
	if !settings.RequireTLS {
		t.Error("passwords may cross an unencrypted connection by default")
	}
	if !settings.SpamEnabled {
		t.Error("spam filtering is off by default")
	}
	if settings.MaxMessageMB == 0 {
		t.Error("the message size limit defaults to nothing, so every message is refused")
	}
}

func TestSettingsSurviveAReadAfterWrite(t *testing.T) {
	repo, serverID, ctx := setup(t)

	saved, err := repo.SaveSettings(ctx, Settings{
		ServerID: serverID, Enabled: true, Hostname: "mail.example.com",
		RequireTLS: true, SpamEnabled: true, SpamRejectScore: 12,
		MaxMessageMB: 40,
	})
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if saved.Hostname != "mail.example.com" || saved.SpamRejectScore != 12 {
		t.Fatalf("settings did not round-trip: %+v", saved)
	}

	reread, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if reread.Hostname != saved.Hostname || reread.MaxMessageMB != 40 {
		t.Errorf("the second read differs: %+v", reread)
	}
}

func TestMailCannotBeEnabledWithoutAHostname(t *testing.T) {
	// The schema refuses it as well as the service, because a host greeting the
	// world as nothing has its mail refused by most of it — and that failure
	// arrives days later as "some people never got my email".
	repo, serverID, ctx := setup(t)

	if _, err := repo.SaveSettings(ctx, Settings{
		ServerID: serverID, Enabled: true, Hostname: "",
		SpamRejectScore: 15, MaxMessageMB: 25,
	}); err == nil {
		t.Error("mail was switched on with no hostname")
	}
}

func TestAWebmailInstallDoesNotOverwriteTheSettingsForm(t *testing.T) {
	// The two are written by different actors — a person saving a form and a
	// long-running install job — and one clobbering the other is how "I
	// installed webmail and the panel forgot" happens.
	repo, serverID, ctx := setup(t)

	if _, err := repo.SaveSettings(ctx, Settings{
		ServerID: serverID, Enabled: true, Hostname: "mail.example.com",
		SpamRejectScore: 15, MaxMessageMB: 25, RequireTLS: true,
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if err := repo.SaveWebmail(ctx, serverID, "", "1.6.9"); err != nil {
		t.Fatalf("SaveWebmail: %v", err)
	}

	settings, err := repo.Settings(ctx, serverID)
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings.Hostname != "mail.example.com" {
		t.Error("recording the webmail install cleared the hostname")
	}
	if settings.WebmailVersion != "1.6.9" {
		t.Errorf("the webmail version was not recorded: %q", settings.WebmailVersion)
	}
}

// ------------------------------------------------------------------- domains

func TestOneDomainPerHost(t *testing.T) {
	// Two rows for one name would produce two entries in Postfix's domain
	// table, and the second would decide which mailboxes exist.
	repo, serverID, ctx := setup(t)
	makeDomain(t, repo, ctx, serverID, "example.com")

	if _, err := repo.CreateDomain(ctx, Domain{
		ServerID: serverID, Domain: "example.com", Active: true,
		SPFPolicy: validate.SPFSoft, DMARCPolicy: validate.DMARCNone,
	}); err == nil {
		t.Error("the same mail domain was recorded twice")
	}
}

func TestASelectorAndAKeyAreStoredTogetherOrNotAtAll(t *testing.T) {
	// A selector with no key is a record that would be published empty; a key
	// with no selector is one nothing can find. Neither half is useful alone,
	// and the schema refuses either.
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	if _, err := repo.SaveDKIM(ctx, domain.ID, "jh2026090412", ""); err == nil {
		t.Error("a selector was stored with no key")
	}

	updated, err := repo.SaveDKIM(ctx, domain.ID, "jh2026090412", "MIIBIjANBg")
	if err != nil {
		t.Fatalf("SaveDKIM: %v", err)
	}
	if updated.DKIMCreatedAt == nil {
		t.Error("the key has no creation time, so nothing can say how old it is")
	}
	if updated.DKIMPublicKey != "MIIBIjANBg" {
		t.Errorf("the public key did not round-trip: %q", updated.DKIMPublicKey)
	}
}

func TestDeletingADomainTakesItsMailboxesWithIt(t *testing.T) {
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	if _, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "a-long-enough-password")); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}

	if err := repo.DeleteDomain(ctx, domain.ID); err != nil {
		t.Fatalf("DeleteDomain: %v", err)
	}
	boxes, err := repo.ListMailboxes(ctx, domain.ID)
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(boxes) != 0 {
		t.Errorf("%d mailboxes outlived their domain", len(boxes))
	}
}

// ----------------------------------------------------------------- mailboxes

func TestAMailboxRowRefusesAnythingThatIsNotAHash(t *testing.T) {
	// The last place a plaintext password can be caught before it is written
	// into a file the mail server authenticates against.
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	if _, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, "hunter2"); err == nil {
		t.Error("a plaintext password was stored in the password_hash column")
	}
}

func TestOneMailboxPerAddress(t *testing.T) {
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	if _, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "a-long-enough-password")); err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	if _, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "another-long-password")); err == nil {
		t.Error("the same address was created twice")
	}
}

func TestTheHashIsNotSerialisedIntoAnHTTPReply(t *testing.T) {
	// A hash is not a credential, and publishing every mailbox's over an API
	// would still turn one leaked reply into an offline attack against every
	// mailbox on the host. The struct having no JSON tag is the enforcement,
	// and this is what would notice a tag being added.
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	box, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "a-long-enough-password"))
	if err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	if box.PasswordHash() == "" {
		t.Fatal("the hash did not reach the reconcile path, which needs it")
	}

	encoded := mustJSON(t, box)
	if strings.Contains(encoded, "SHA512-CRYPT") || strings.Contains(encoded, "$6$") {
		t.Errorf("a password hash is in the JSON body: %s", encoded)
	}
}

func TestSettingAPasswordReplacesTheHash(t *testing.T) {
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	box, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "the-first-password"))
	if err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}
	before := box.PasswordHash()

	if err := repo.SetPassword(ctx, box.ID, hash(t, "the-second-password")); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	after, err := repo.GetMailbox(ctx, box.ID)
	if err != nil {
		t.Fatalf("GetMailbox: %v", err)
	}
	if after.PasswordHash() == before {
		t.Error("the password did not change")
	}
	if !crypt.IsHash(after.PasswordHash()) {
		t.Errorf("what was stored is not a hash: %q", after.PasswordHash())
	}
}

// ------------------------------------------------------------------- aliases

func TestTheSamePairIsNotRecordedTwice(t *testing.T) {
	// The same source may forward to several addresses — that is a distribution
	// list — but recording the same pair twice would deliver two copies.
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")

	alias := Alias{
		DomainID: domain.ID, Source: "info",
		Destination: "sales@example.com", Active: true,
	}
	if _, err := repo.CreateAlias(ctx, alias); err != nil {
		t.Fatalf("CreateAlias: %v", err)
	}
	if _, err := repo.CreateAlias(ctx, alias); err == nil {
		t.Error("the same forwarder was recorded twice")
	}

	alias.Destination = "support@example.net"
	if _, err := repo.CreateAlias(ctx, alias); err != nil {
		t.Errorf("a second destination for one source was refused: %v", err)
	}
}

// ------------------------------------------------------------ autoresponders

func TestAnAutoresponderNeedsAMessage(t *testing.T) {
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")
	box, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "a-long-enough-password"))
	if err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}

	if _, err := repo.SaveAutoresponder(ctx, Autoresponder{
		MailboxID: box.ID, Subject: "Away", Body: "   ", IntervalDays: 7, Active: true,
	}); err == nil {
		t.Error("an autoresponder with a blank message was accepted")
	}
}

func TestAnAutoresponderNeverRepliesToEveryMessage(t *testing.T) {
	// Zero would mean a reply to every message, including to somebody else's
	// vacation reply — a loop that ends when one of the two mailboxes is full.
	repo, serverID, ctx := setup(t)
	domain := makeDomain(t, repo, ctx, serverID, "example.com")
	box, err := repo.CreateMailbox(ctx, Mailbox{
		DomainID: domain.ID, LocalPart: "sales", QuotaMB: 100, Active: true,
	}, hash(t, "a-long-enough-password"))
	if err != nil {
		t.Fatalf("CreateMailbox: %v", err)
	}

	if _, err := repo.SaveAutoresponder(ctx, Autoresponder{
		MailboxID: box.ID, Subject: "Away", Body: "Back soon.",
		IntervalDays: 0, Active: true,
	}); err == nil {
		t.Error("an autoresponder that replies to every message was accepted")
	}
}

func TestAResponderOutsideItsDatesIsNotAppliedToTheHost(t *testing.T) {
	// Evaluated in the panel rather than on the host, because the host would
	// need a scheduled job to notice one expiring — and a reply still firing
	// three weeks after somebody came back is the failure everybody has seen.
	past := time.Now().Add(-48 * time.Hour)
	ended := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(24 * time.Hour)

	if activeNow(Autoresponder{Active: true, StartsAt: &past, EndsAt: &ended}) {
		t.Error("an autoresponder that ended yesterday is still replying")
	}
	if activeNow(Autoresponder{Active: true, StartsAt: &future}) {
		t.Error("an autoresponder that starts tomorrow is already replying")
	}
	if !activeNow(Autoresponder{Active: true, StartsAt: &past, EndsAt: &future}) {
		t.Error("an autoresponder inside its dates is not replying")
	}
	if !activeNow(Autoresponder{Active: true}) {
		t.Error("an autoresponder with no dates should be on until it is turned off")
	}
	if activeNow(Autoresponder{Active: false}) {
		t.Error("a switched-off autoresponder is replying")
	}
}

// -------------------------------------------------------------- DNS records

func TestThereIsNoWayToPublishAnSPFRecordThatAuthorisesEverybody(t *testing.T) {
	// "+all" is worse than no record: it tells every receiver that the forgery
	// they are looking at is legitimate. The closed set is what makes it
	// unaskable-for.
	for _, policy := range validate.SPFPolicies {
		record := spfRecord(policy)
		if strings.Contains(record, "+all") {
			t.Errorf("policy %q produced %q", policy, record)
		}
	}
	if spfRecord(validate.SPFSoft) != "v=spf1 mx a ~all" {
		t.Errorf("soft policy: %q", spfRecord(validate.SPFSoft))
	}
	if spfRecord(validate.SPFStrict) != "v=spf1 mx a -all" {
		t.Errorf("strict policy: %q", spfRecord(validate.SPFStrict))
	}
	if spfRecord(validate.SPFNone) != "" {
		t.Error("the 'none' policy published a record")
	}
}

func TestTheDKIMRecordNamesItsAlgorithm(t *testing.T) {
	// Left unstated, the default differs between implementations — and an
	// unstated algorithm is one some verifier guesses wrong, which reads as a
	// forged signature rather than as a missing tag.
	record := dkimRecord("MIIBIjANBgkq")
	if !strings.HasPrefix(record, "v=DKIM1; k=rsa; p=") {
		t.Errorf("the DKIM record is not in the expected form: %q", record)
	}
	if !strings.HasSuffix(record, "MIIBIjANBgkq") {
		t.Errorf("the key is not in the record: %q", record)
	}
}

func TestTurningDMARCOffPublishesNothing(t *testing.T) {
	if dmarcRecord(validate.DMARCOff, "reports@example.com") != "" {
		t.Error("a record was published for a domain with DMARC off")
	}
	record := dmarcRecord(validate.DMARCReject, "reports@example.com")
	if record != "v=DMARC1; p=reject; rua=mailto:reports@example.com" {
		t.Errorf("the DMARC record is not in the expected form: %q", record)
	}
	if got := dmarcRecord(validate.DMARCNone, ""); got != "v=DMARC1; p=none" {
		t.Errorf("a record with no reporting address: %q", got)
	}
}

// ------------------------------------------------------------------ password

func TestAShortMailboxPasswordIsRefused(t *testing.T) {
	// A mailbox password is exposed to the whole internet on ports the panel
	// does not rate-limit, and it is the single credential that unlocks a
	// customer's correspondence.
	if err := checkPassword("short"); err == nil {
		t.Error("a five-character mailbox password was accepted")
	}
	if err := checkPassword(strings.Repeat("a", MinPasswordLength)); err != nil {
		t.Errorf("a password of the minimum length was refused: %v", err)
	}
}

func TestASelectorIsNewOnEveryRotation(t *testing.T) {
	// A selector's whole purpose is to let a domain hold two keys at once
	// during a rotation. A fixed name makes that impossible: the new key would
	// replace the old one at the same name, and every message signed with the
	// old key that is still in flight would fail to verify.
	first := selectorFor(time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC))
	second := selectorFor(time.Date(2026, 9, 4, 11, 0, 0, 0, time.UTC))
	if first == second {
		t.Errorf("two rotations produced the same selector: %s", first)
	}
	if err := validate.DKIMSelector(first); err != nil {
		t.Errorf("the generated selector is not a valid one: %v", err)
	}
}

// mustJSON encodes a value the way an HTTP reply would.
func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}
