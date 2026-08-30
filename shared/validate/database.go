package validate

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Errors returned by database validation.
var (
	ErrInvalidDatabaseName = errors.New("invalid database name")
	ErrInvalidDatabaseUser = errors.New("invalid database user name")
	ErrInvalidEngine       = errors.New("unknown database engine")
	ErrInvalidHostPattern  = errors.New("invalid database host pattern")
)

// Database engine names. MySQL and MariaDB are kept distinct even though the
// Agent drives both through one provider: an operator needs to know which
// server their data is actually in, and the two disagree about enough
// (authentication plugins, JSON functions, version numbering) that reporting
// one as the other would eventually mislead someone.
const (
	EngineMySQL    = "mysql"
	EngineMariaDB  = "mariadb"
	EnginePostgres = "postgres"
)

// Identifier length limits.
//
// 63 is PostgreSQL's NAMEDATALEN-1 and below MySQL's 64, so a name accepted
// here is valid on every engine. Users are capped at 32, which is MySQL's
// limit and the tightest of the three.
const (
	MaxDatabaseNameLength = 63
	MaxDatabaseUserLength = 32
)

// databaseIdentifierPattern is the whole injection boundary for Phase 8.
//
// Database and user names are identifiers, and SQL has no parameter form for
// an identifier: "CREATE DATABASE $1" is not valid on any engine, so the name
// is necessarily concatenated into a statement. Rather than rely on quoting
// alone, nothing but lowercase letters, digits and underscores is permitted to
// exist in one — a name containing a quote, a backslash, a backtick, a
// semicolon or a space is refused long before any statement is built.
//
// Quoting is still applied at the point of use. This is the first of two
// defences, not the only one.
var databaseIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// reservedDatabases are names the panel must never create, drop, or hand to a
// customer. Dropping any of them ranges from "the panel forgets its own
// users" to "the server no longer starts".
var reservedDatabases = map[string]struct{}{
	// MySQL and MariaDB system schemas.
	"mysql": {}, "information_schema": {}, "performance_schema": {}, "sys": {},
	// PostgreSQL system and template databases.
	"postgres": {}, "template0": {}, "template1": {},
	// The panel's own control-plane database.
	"jothost": {},
}

// reservedDatabaseUsers are accounts that already exist with privileges no
// website should be handed.
var reservedDatabaseUsers = map[string]struct{}{
	"root": {}, "mysql": {}, "mariadb": {}, "postgres": {}, "pgsql": {},
	"admin": {}, "jothost": {}, "rdsadmin": {}, "healthcheck": {},
	// MariaDB 10.4+ ships these; dropping either breaks the server's own
	// maintenance scripts.
	"mariadb.sys": {}, "mysql.sys": {}, "mysql.session": {}, "mysql.infoschema": {},
}

// DatabaseEngine checks an engine name.
func DatabaseEngine(engine string) error {
	switch engine {
	case EngineMySQL, EngineMariaDB, EnginePostgres:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidEngine, engine)
	}
}

// DatabaseName checks a database name.
func DatabaseName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidDatabaseName)
	}
	if len(name) > MaxDatabaseNameLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidDatabaseName, MaxDatabaseNameLength)
	}
	if !databaseIdentifierPattern.MatchString(name) {
		return fmt.Errorf("%w: %q must start with a letter and contain only "+
			"lowercase letters, digits, and underscores", ErrInvalidDatabaseName, name)
	}
	if _, reserved := reservedDatabases[name]; reserved {
		return fmt.Errorf("%w: %q is reserved by the database server", ErrInvalidDatabaseName, name)
	}
	// "pg_" is reserved by PostgreSQL for system use and rejected by CREATE
	// DATABASE there; refusing it on every engine keeps one rule.
	if strings.HasPrefix(name, "pg_") {
		return fmt.Errorf("%w: names beginning with pg_ are reserved", ErrInvalidDatabaseName)
	}
	return nil
}

// DatabaseUser checks a database account name.
func DatabaseUser(name string) error {
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidDatabaseUser)
	}
	if len(name) > MaxDatabaseUserLength {
		return fmt.Errorf("%w: must be at most %d characters",
			ErrInvalidDatabaseUser, MaxDatabaseUserLength)
	}
	if !databaseIdentifierPattern.MatchString(name) {
		return fmt.Errorf("%w: %q must start with a letter and contain only "+
			"lowercase letters, digits, and underscores", ErrInvalidDatabaseUser, name)
	}
	if _, reserved := reservedDatabaseUsers[name]; reserved {
		return fmt.Errorf("%w: %q is reserved by the database server", ErrInvalidDatabaseUser, name)
	}
	if strings.HasPrefix(name, "pg_") {
		return fmt.Errorf("%w: names beginning with pg_ are reserved", ErrInvalidDatabaseUser)
	}
	return nil
}

// DatabaseHostPattern checks the host half of a MySQL account.
//
// MySQL identifies an account by user *and* host, and the difference matters:
// 'app'@'localhost' and 'app'@'%' are two accounts with separate passwords and
// grants. Only two values are permitted. "localhost" is the default and is the
// only one that cannot be reached from another machine; "%" is offered because
// an application container connecting over the network genuinely needs it, and
// refusing it would push operators to edit grants by hand.
//
// Anything else — a literal address, a wildcard pattern — is refused: those
// are the shapes that turn a typo into a database exposed to a subnet.
func DatabaseHostPattern(host string) error {
	switch host {
	case "localhost", "%":
		return nil
	default:
		return fmt.Errorf("%w: %q (only \"localhost\" or \"%%\" are permitted)",
			ErrInvalidHostPattern, host)
	}
}

// DatabaseNameFor suggests a database name for a website.
//
// The domain is not a valid identifier — it has dots, and may start with a
// digit — so it is folded the same way SystemUserFor folds it, and given the
// same short hash suffix so two sites with a shared prefix cannot collide.
func DatabaseNameFor(domain, hash string) string {
	base := strings.NewReplacer(".", "_", "-", "_").Replace(NormalizeDomain(domain))
	base = strings.TrimLeft(base, "0123456789_")
	if base == "" {
		base = "site"
	}

	name := "db_" + base
	suffix := "_" + hash

	limit := MaxDatabaseNameLength - len(suffix)
	if len(name) > limit {
		name = name[:limit]
	}
	return name + suffix
}
