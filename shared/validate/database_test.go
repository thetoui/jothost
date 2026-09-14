package validate

import (
	"errors"
	"strings"
	"testing"
)

func TestDatabaseNameAcceptsOrdinaryNames(t *testing.T) {
	for _, name := range []string{"app", "app_prod", "wp1", "a", strings.Repeat("a", 63)} {
		if err := DatabaseName(name); err != nil {
			t.Errorf("DatabaseName(%q) = %v, want nil", name, err)
		}
	}
}

// Every one of these is a name that, if it reached a statement, would end the
// identifier and begin something else. They are refused before quoting is
// even considered.
func TestDatabaseNameRejectsInjection(t *testing.T) {
	injections := []string{
		"app; DROP DATABASE other",
		"app`; DROP DATABASE other; --",
		`app"; DROP DATABASE other; --`,
		"app'--",
		"app\\",
		"app other",
		"app\nDROP DATABASE other",
		"app\x00",
		"APP",
		"1app",
		"_app",
		"app-prod",
		"app.prod",
		"",
		strings.Repeat("a", 64),
	}
	for _, name := range injections {
		if err := DatabaseName(name); err == nil {
			t.Errorf("DatabaseName(%q) = nil, want an error", name)
		}
	}
}

func TestDatabaseNameRejectsServerOwnedSchemas(t *testing.T) {
	for _, name := range []string{
		"mysql", "information_schema", "performance_schema", "sys",
		"postgres", "template0", "template1", "jothost", "pg_toast",
	} {
		err := DatabaseName(name)
		if err == nil {
			t.Fatalf("DatabaseName(%q) = nil, want a refusal", name)
		}
		if !errors.Is(err, ErrInvalidDatabaseName) {
			t.Errorf("DatabaseName(%q) = %v, want ErrInvalidDatabaseName", name, err)
		}
	}
}

func TestDatabaseUserRejectsPrivilegedAccounts(t *testing.T) {
	for _, name := range []string{"root", "postgres", "admin", "jothost", "jothost_agent", "mysql"} {
		if err := DatabaseUser(name); err == nil {
			t.Errorf("DatabaseUser(%q) = nil, want a refusal", name)
		}
	}
}

func TestDatabaseUserIsBoundedAtMySQLsLimit(t *testing.T) {
	if err := DatabaseUser(strings.Repeat("u", 32)); err != nil {
		t.Errorf("32-character user rejected: %v", err)
	}
	// MySQL truncates rather than failing, which would silently create an
	// account under a name nobody asked for.
	if err := DatabaseUser(strings.Repeat("u", 33)); err == nil {
		t.Error("33-character user accepted, want a refusal")
	}
}

func TestDatabaseHostPatternAllowsOnlyTheTwoDeliberateChoices(t *testing.T) {
	for _, host := range []string{"localhost", "%"} {
		if err := DatabaseHostPattern(host); err != nil {
			t.Errorf("DatabaseHostPattern(%q) = %v, want nil", host, err)
		}
	}
	for _, host := range []string{"", "127.0.0.1", "10.%", "example.com", "localhost'--"} {
		if err := DatabaseHostPattern(host); err == nil {
			t.Errorf("DatabaseHostPattern(%q) = nil, want a refusal", host)
		}
	}
}

func TestDatabaseEngineIsAllowlisted(t *testing.T) {
	for _, engine := range []string{EngineMySQL, EngineMariaDB, EnginePostgres} {
		if err := DatabaseEngine(engine); err != nil {
			t.Errorf("DatabaseEngine(%q) = %v, want nil", engine, err)
		}
	}
	for _, engine := range []string{"", "sqlite", "MYSQL", "mongo"} {
		if err := DatabaseEngine(engine); err == nil {
			t.Errorf("DatabaseEngine(%q) = nil, want a refusal", engine)
		}
	}
}

// A suggested name must itself pass validation, or the panel offers a default
// it then refuses to accept.
func TestDatabaseNameForProducesAValidName(t *testing.T) {
	for _, domain := range []string{
		"example.com",
		"123.example.com",
		"a-very-long-domain-name-that-goes-well-past-the-identifier-limit.example.com",
		"x.io",
	} {
		name := DatabaseNameFor(domain, "a1b2c3")
		if err := DatabaseName(name); err != nil {
			t.Errorf("DatabaseNameFor(%q) = %q, which is invalid: %v", domain, name, err)
		}
	}
}

func TestDatabaseNameForIsDistinctPerSite(t *testing.T) {
	first := DatabaseNameFor("example.com", "aaaaaa")
	second := DatabaseNameFor("example.com", "bbbbbb")
	if first == second {
		t.Fatalf("two sites produced the same database name: %q", first)
	}
}

func TestTheIdentifierCheckAllowsReservedNamesAndNothingMalformed(t *testing.T) {
	// The panel's own database is reserved against customers and still has
	// to be nameable by the Agent's configuration.
	if err := DatabaseName("jothost"); err == nil {
		t.Fatal("DatabaseName accepted the panel's reserved database")
	}
	if err := DatabaseIdentifier("jothost"); err != nil {
		t.Fatalf("DatabaseIdentifier refused a well-formed reserved name: %v", err)
	}
	// It relaxes the reservation only, not the shape.
	for _, bad := range []string{"", "Jothost", "1db", "db-name", "db;drop", "pg_catalog", strings.Repeat("a", MaxDatabaseNameLength+1)} {
		if err := DatabaseIdentifier(bad); err == nil {
			t.Fatalf("DatabaseIdentifier accepted %q", bad)
		}
	}
}
