package database

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jothost/panel/shared/validate"
)

func TestGeneratePasswordIsLongAndAccepted(t *testing.T) {
	seen := make(map[string]struct{}, 32)
	for i := 0; i < 32; i++ {
		password, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if len(password) != GeneratedPasswordLength {
			t.Fatalf("length = %d, want %d", len(password), GeneratedPasswordLength)
		}
		// A generated password must survive the panel's own validation, or the
		// Agent would create an account it then refuses to update.
		if err := ValidatePassword(password); err != nil {
			t.Fatalf("generated password rejected by ValidatePassword: %v", err)
		}
		if _, repeat := seen[password]; repeat {
			t.Fatalf("GeneratePassword returned a duplicate: %q", password)
		}
		seen[password] = struct{}{}
	}
}

// The alphabet exists to make a quote impossible in a password, because a
// password reaches the server inside a SQL string literal.
func TestGeneratedPasswordsNeverContainQuotes(t *testing.T) {
	for i := 0; i < 200; i++ {
		password, err := GeneratePassword()
		if err != nil {
			t.Fatalf("GeneratePassword: %v", err)
		}
		if strings.ContainsAny(password, "'\"`\\") {
			t.Fatalf("generated password contains a quoting character: %q", password)
		}
	}
}

func TestValidatePasswordRefusesQuotingCharacters(t *testing.T) {
	for _, password := range []string{
		"has'a'quote12345",
		`has"a"quote12345`,
		"has`a`quote12345",
		`has\a\backslash1`,
		"has a space 1234",
		"short",
		strings.Repeat("a", MaxPasswordLength+1),
	} {
		if err := ValidatePassword(password); err == nil {
			t.Errorf("ValidatePassword(%q) = nil, want a refusal", password)
		}
	}
}

func TestValidatePasswordAcceptsAStrongOne(t *testing.T) {
	if err := ValidatePassword("Correct-Horse_9!x"); err != nil {
		t.Fatalf("ValidatePassword: %v", err)
	}
}

func TestQuotingClosesInjectionEvenWithoutValidation(t *testing.T) {
	// Validation would refuse these long before quoting sees them. The check
	// is that quoting is independently correct, so a future caller reaching it
	// by another path is still safe.
	if got := quoteMySQLIdent("a`b"); got != "`a``b`" {
		t.Errorf("quoteMySQLIdent = %q, want %q", got, "`a``b`")
	}
	if got := quoteMySQLString(`a'b`); got != `'a\'b'` {
		t.Errorf("quoteMySQLString = %q, want %q", got, `'a\'b'`)
	}
	if got := quotePostgresIdent(`a"b`); got != `"a""b"` {
		t.Errorf("quotePostgresIdent = %q, want %q", got, `"a""b"`)
	}
	if got := quotePostgresString("a'b"); got != "'a''b'" {
		t.Errorf("quotePostgresString = %q, want %q", got, "'a''b'")
	}
}

// PostgreSQL's standard_conforming_strings makes a backslash an ordinary
// character, so escaping one would change the password that gets stored.
func TestPostgresStringLiteralLeavesBackslashesAlone(t *testing.T) {
	if got := quotePostgresString(`a\b`); got != `'a\b'` {
		t.Fatalf("quotePostgresString = %q, want %q", got, `'a\b'`)
	}
}

func TestMySQLPrivilegesAreAFixedSet(t *testing.T) {
	cases := map[string]string{
		PrivilegeReadOnly:  "SELECT",
		PrivilegeReadWrite: "SELECT, INSERT, UPDATE, DELETE",
		PrivilegeFull:      "ALL PRIVILEGES",
	}
	for level, want := range cases {
		got, err := mysqlPrivileges(level)
		if err != nil {
			t.Fatalf("mysqlPrivileges(%q): %v", level, err)
		}
		if got != want {
			t.Errorf("mysqlPrivileges(%q) = %q, want %q", level, got, want)
		}
	}

	// A caller must never be able to name its own privileges: SUPER and FILE
	// are server-wide and would make a website account a way into every other
	// database on the host.
	for _, level := range []string{"", "SUPER", "ALL PRIVILEGES", "select"} {
		if _, err := mysqlPrivileges(level); err == nil {
			t.Errorf("mysqlPrivileges(%q) = nil error, want a refusal", level)
		}
	}
}

func TestPostgresGrantsIncludeDefaultPrivileges(t *testing.T) {
	// Without ALTER DEFAULT PRIVILEGES, every table the application creates
	// after the grant is invisible to the role that was granted access.
	for _, level := range []string{PrivilegeReadOnly, PrivilegeReadWrite, PrivilegeFull} {
		statements := strings.Join(postgresGrantStatements(level, `"app"`), "\n")
		if !strings.Contains(statements, "ALTER DEFAULT PRIVILEGES") {
			t.Errorf("%s grant has no default privileges:\n%s", level, statements)
		}
	}

	// Only "full" may create objects; readonly and readwrite must not be able
	// to add tables to the schema.
	if strings.Contains(strings.Join(postgresGrantStatements(PrivilegeReadOnly, `"app"`), " "),
		"GRANT ALL ON SCHEMA") {
		t.Error("readonly was granted schema-wide rights")
	}
}

func TestRowsSkipsBlankAndShortLines(t *testing.T) {
	parsed := rows("a\tb\tc\n\nshort\nd\te\tf\n", 3)
	if len(parsed) != 2 {
		t.Fatalf("rows returned %d, want 2: %v", len(parsed), parsed)
	}
	if parsed[1][2] != "f" {
		t.Errorf("second row = %v", parsed[1])
	}
}

func TestShortPostgresVersionTrimsTheBanner(t *testing.T) {
	full := "PostgreSQL 16.3 on x86_64-pc-linux-musl, compiled by gcc, 64-bit"
	if got := shortPostgresVersion(full); got != "16.3" {
		t.Fatalf("shortPostgresVersion = %q, want 16.3", got)
	}
}

func TestEscapePgpassProtectsTheFieldSeparator(t *testing.T) {
	// A password containing a colon would otherwise split the pgpass line and
	// silently authenticate with a truncated secret.
	if got := escapePgpass("pa:ss"); got != `pa\:ss` {
		t.Fatalf("escapePgpass = %q", got)
	}
}

// --------------------------------------------------------------- the manager

// fakeProvider stands in for a database server in tests that are about
// routing rather than SQL.
type fakeProvider struct {
	engine     string
	available  bool
	hostPairs  bool
	version    string
	versionErr error
	detail     string
}

func (f *fakeProvider) Engine() string  { return f.engine }
func (f *fakeProvider) Available() bool { return f.available }
func (f *fakeProvider) Unavailable() string {
	if f.available {
		return ""
	}
	if f.detail != "" {
		return f.detail
	}
	return "no server of this engine was found on the host"
}
func (f *fakeProvider) SupportsHostPatterns() bool { return f.hostPairs }
func (f *fakeProvider) Version(context.Context) (string, error) {
	return f.version, f.versionErr
}
func (f *fakeProvider) ListDatabases(context.Context) ([]Database, error) { return nil, nil }
func (f *fakeProvider) CreateDatabase(context.Context, string) error      { return nil }
func (f *fakeProvider) DropDatabase(context.Context, string) error        { return nil }
func (f *fakeProvider) DatabaseSize(context.Context, string) (int64, error) {
	return 0, nil
}
func (f *fakeProvider) ListUsers(context.Context) ([]User, error) { return nil, nil }
func (f *fakeProvider) CreateUser(context.Context, User, string) (bool, error) {
	return true, nil
}
func (f *fakeProvider) DropUser(context.Context, User) error            { return nil }
func (f *fakeProvider) SetPassword(context.Context, User, string) error { return nil }
func (f *fakeProvider) Grant(context.Context, Grant) error              { return nil }
func (f *fakeProvider) RevokeAll(context.Context, User, string) error   { return nil }

func TestManagerRefusesAnEngineThatIsNotThere(t *testing.T) {
	manager := NewManager(ManagerOptions{Providers: []Provider{
		&fakeProvider{engine: validate.EngineMariaDB, available: true, version: "11.4"},
		&fakeProvider{engine: validate.EnginePostgres, available: false},
	}})

	if _, err := manager.Provider(validate.EngineMariaDB); err != nil {
		t.Fatalf("mariadb provider: %v", err)
	}
	if _, err := manager.Provider(validate.EnginePostgres); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("postgres provider = %v, want ErrEngineUnavailable", err)
	}
	// An engine name that is not one of ours must be refused as invalid, not
	// reported as merely absent.
	if _, err := manager.Provider("sqlite"); !errors.Is(err, validate.ErrInvalidEngine) {
		t.Fatalf("sqlite provider = %v, want ErrInvalidEngine", err)
	}
}

// A server that is installed but not answering is a third state. Reporting it
// as "not installed" would send an operator looking for a package that is
// already there.
func TestEnginesDistinguishAbsentFromUnreachable(t *testing.T) {
	manager := NewManager(ManagerOptions{Providers: []Provider{
		&fakeProvider{engine: validate.EngineMariaDB, available: true, versionErr: errors.New("connection refused")},
		&fakeProvider{engine: validate.EnginePostgres, available: false},
	}})

	infos := manager.Engines(context.Background())
	if len(infos) != 2 {
		t.Fatalf("got %d engines, want 2", len(infos))
	}

	byName := map[string]EngineInfo{}
	for _, info := range infos {
		byName[info.Engine] = info
	}

	mariadb := byName[validate.EngineMariaDB]
	if mariadb.Available || !strings.Contains(mariadb.Detail, "stopped answering") {
		t.Errorf("unreachable mariadb reported as %+v", mariadb)
	}
	postgres := byName[validate.EnginePostgres]
	if postgres.Available || !strings.Contains(postgres.Detail, "no server") {
		t.Errorf("absent postgres reported as %+v", postgres)
	}
}

func TestEnginesReportHostPatternSupport(t *testing.T) {
	manager := NewManager(ManagerOptions{Providers: []Provider{
		&fakeProvider{engine: validate.EngineMariaDB, available: true, hostPairs: true, version: "11.4"},
		&fakeProvider{engine: validate.EnginePostgres, available: true, version: "16.3"},
	}})

	for _, info := range manager.Engines(context.Background()) {
		want := info.Engine == validate.EngineMariaDB
		if info.SupportsHostPatterns != want {
			t.Errorf("%s SupportsHostPatterns = %v, want %v",
				info.Engine, info.SupportsHostPatterns, want)
		}
	}
}

// A MySQL option file treats '#' and ';' as the start of a comment, and both
// are in the panel's own password alphabet. An unquoted value would truncate
// silently, authenticating with a prefix of the real password — a failure that
// only shows up for some passwords and never in a way that says why.
func TestOptionFileValuesAreQuoted(t *testing.T) {
	cases := map[string]string{
		"ab#cd":      `"ab#cd"`,
		"ab;cd":      `"ab;cd"`,
		"trailing ":  `"trailing "`,
		`with"quote`: `"with\"quote"`,
		`back\slash`: `"back\\slash"`,
	}
	for input, want := range cases {
		if got := quoteOptionValue(input); got != want {
			t.Errorf("quoteOptionValue(%q) = %s, want %s", input, got, want)
		}
	}
}

// The generated alphabet really does contain the comment characters, so the
// quoting above is load-bearing rather than theoretical.
func TestPasswordAlphabetContainsOptionFileCommentCharacters(t *testing.T) {
	for _, r := range []rune{'#', ';'} {
		if !strings.ContainsRune(passwordAlphabet, r) {
			t.Errorf("alphabet no longer contains %q; the option-file quoting test is now vacuous "+
				"and should be reconsidered", r)
		}
	}
}

// The mariadb client echoes each statement to stderr between rows of dashes
// when reading standard input. Taking the first line therefore reported
// "--------------" as the reason a statement failed — useless to a user, and
// invisible to any code matching on the message, which is how a broken REVOKE
// went unnoticed.
func TestClientErrorFindsTheErrorLineNotTheDecoration(t *testing.T) {
	stderr := "--------------\nREVOKE ALL PRIVILEGES ON `app`.* FROM 'u'@'localhost'\n" +
		"--------------\n\nERROR 1141 (42000) at line 1: There is no such grant defined for user 'u'\n"

	got := clientError(stderr)
	if !strings.Contains(got, "1141") {
		t.Fatalf("clientError = %q, want the ERROR line", got)
	}
	// And the check that decides whether a revoke may be ignored must agree.
	if !isNoSuchGrant(errors.New(got)) {
		t.Errorf("isNoSuchGrant did not recognise %q", got)
	}
}

// Anything that is not the expected "nothing to revoke" must be fatal, or a
// statement this code gets wrong disappears again.
func TestIsNoSuchGrantIsNarrow(t *testing.T) {
	for _, message := range []string{
		"--------------",
		"ERROR 1064 (42000) at line 1: You have an error in your SQL syntax",
		"ERROR 1045 (28000): Access denied for user 'root'@'localhost'",
		"",
	} {
		if isNoSuchGrant(errors.New(message)) {
			t.Errorf("isNoSuchGrant(%q) = true, want false", message)
		}
	}
}

func TestClientErrorFallsBackToTheFirstLine(t *testing.T) {
	if got := clientError("could not connect to server\nsecond line\n"); got != "could not connect to server" {
		t.Fatalf("clientError = %q", got)
	}
}

// An error message is shown to a user and written to a log. The statement the
// client echoes back can be the one that sets a password, so it must never
// become the error text (CLAUDE.md section 14).
func TestClientErrorNeverReturnsTheEchoedStatement(t *testing.T) {
	stderr := "--------------\n" +
		"CREATE USER 'app'@'localhost' IDENTIFIED BY 'Sup3r-Secret_Pw'\n" +
		"--------------\n\n"

	got := clientError(stderr)
	if strings.Contains(got, "Sup3r-Secret_Pw") || strings.Contains(got, "CREATE USER") {
		t.Fatalf("clientError leaked the statement: %q", got)
	}
	if got == "" {
		t.Fatal("clientError returned nothing at all")
	}
}
