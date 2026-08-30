package database

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandPsql is the allowlist name for the PostgreSQL client.
const CommandPsql = "psql"

// adminDatabase is where server-level statements are run. Every PostgreSQL
// installation has it, and CREATE DATABASE has to be issued from somewhere.
const adminDatabase = "postgres"

// PostgresOptions configure the PostgreSQL provider.
type PostgresOptions struct {
	Runner *command.Runner
	// Host is either a hostname or, when it starts with a slash, the directory
	// holding the server's Unix socket. The socket is preferred: it lets the
	// server authenticate by peer uid, so no password is needed at all.
	Host string
	Port int
	// AdminUser must be a superuser: creating databases and roles requires it.
	AdminUser string
	// AdminPassword is optional. When set it is written to a mode-0600 pgpass
	// file and reaches the client through PGPASSFILE — never through argv or a
	// connection URI, both of which the process table would publish.
	AdminPassword string
	Log           *slog.Logger
}

// Postgres drives a local PostgreSQL server through psql.
type Postgres struct {
	runner    *command.Runner
	host      string
	port      int
	user      string
	version   string
	available bool
	passFile  string
	// probeErr is the startup probe's failure, kept so the panel can say what
	// went wrong rather than only that something did.
	probeErr string
	log      *slog.Logger
}

// NewPostgres builds the provider and probes the server once.
func NewPostgres(ctx context.Context, opts PostgresOptions) *Postgres {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	provider := &Postgres{
		runner: opts.Runner,
		host:   opts.Host,
		port:   opts.Port,
		user:   opts.AdminUser,
		log:    log,
	}
	if provider.user == "" {
		provider.user = "postgres"
	}
	if provider.port == 0 {
		provider.port = 5432
	}
	if provider.runner == nil || !provider.runner.Available(CommandPsql) {
		return provider
	}

	if opts.AdminPassword != "" {
		path, err := writePassFile(provider.port, provider.user, opts.AdminPassword)
		if err != nil {
			// Without the credentials file the client would fall back to peer
			// authentication and appear to work on some hosts and not others.
			// Stopping here makes the misconfiguration visible.
			provider.probeErr = err.Error()
			return provider
		}
		provider.passFile = path
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	version, err := provider.query(probeCtx, adminDatabase, "SELECT version();")
	if err != nil {
		provider.probeErr = firstLine(err.Error())
		log.Warn("postgresql did not answer its version probe", "detail", provider.probeErr)
		return provider
	}

	provider.version = shortPostgresVersion(version)
	provider.available = provider.version != ""
	return provider
}

// Engine always reports postgres: unlike the MySQL client, psql speaks to one
// kind of server.
func (p *Postgres) Engine() string { return validate.EnginePostgres }

// Available reports whether the server answered.
func (p *Postgres) Available() bool { return p.available }

// Unavailable explains why the engine cannot be used, or returns empty.
//
// "Installed but not answering" and "not installed" are different problems
// with different fixes, and reporting the first as the second sends an
// operator looking for a package that is already there.
func (p *Postgres) Unavailable() string {
	if p.available {
		return ""
	}
	if p.runner == nil || !p.runner.Available(CommandPsql) {
		return "PostgreSQL is not installed on this host"
	}
	if p.probeErr != "" {
		return "PostgreSQL is installed but did not answer: " + p.probeErr
	}
	return "PostgreSQL is installed but did not answer"
}

// SupportsHostPatterns is false: a PostgreSQL role is global, and where it may
// connect from is decided by pg_hba.conf, not by the role's name.
func (p *Postgres) SupportsHostPatterns() bool { return false }

// Version returns the server version.
func (p *Postgres) Version(ctx context.Context) (string, error) {
	if p.version != "" {
		return p.version, nil
	}
	version, err := p.query(ctx, adminDatabase, "SELECT version();")
	return shortPostgresVersion(version), err
}

// ListDatabases returns every database the panel may manage.
func (p *Postgres) ListDatabases(ctx context.Context) ([]Database, error) {
	const stmt = `SELECT d.datname, pg_catalog.pg_encoding_to_char(d.encoding), d.datcollate,
		pg_catalog.pg_get_userbyid(d.datdba),
		CASE WHEN pg_catalog.has_database_privilege(d.datname, 'CONNECT')
		     THEN pg_catalog.pg_database_size(d.datname) ELSE 0 END
	FROM pg_catalog.pg_database d
	WHERE NOT d.datistemplate
	ORDER BY d.datname;`

	out, err := p.query(ctx, adminDatabase, stmt)
	if err != nil {
		return nil, err
	}

	databases := make([]Database, 0, 8)
	for _, fields := range rows(out, 5) {
		name := strings.TrimSpace(fields[0])
		// The panel's own control-plane database and the server's defaults are
		// filtered by the same rule that decides what may be created, so the
		// list never offers a delete button for something undeletable.
		if validate.DatabaseName(name) != nil {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
		databases = append(databases, Database{
			Name:      name,
			Engine:    validate.EnginePostgres,
			Charset:   strings.TrimSpace(fields[1]),
			Collation: strings.TrimSpace(fields[2]),
			Owner:     strings.TrimSpace(fields[3]),
			SizeBytes: size,
		})
	}
	return databases, nil
}

// CreateDatabase creates a database if it is not already there.
//
// PostgreSQL has no CREATE DATABASE IF NOT EXISTS and no way to wrap CREATE
// DATABASE in a DO block, because it cannot run inside a transaction. The
// existence check is therefore a separate query. That is a check-then-act race
// in principle; in practice the loser of the race gets "database already
// exists" from the server, which is treated as success — the same answer the
// check would have given it a moment earlier.
func (p *Postgres) CreateDatabase(ctx context.Context, name string) error {
	if err := validate.DatabaseName(name); err != nil {
		return err
	}

	exists, err := p.databaseExists(ctx, name)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	// LC_COLLATE and LC_CTYPE are inherited from template1 deliberately: a
	// database created with a locale the server's template does not have fails
	// to create, and the panel has no business choosing a collation the
	// operator did not ask for.
	stmt := fmt.Sprintf("CREATE DATABASE %s ENCODING 'UTF8';", quotePostgresIdent(name))
	if _, err := p.query(ctx, adminDatabase, stmt); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			return p.isolate(ctx, name)
		}
		return err
	}
	return p.isolate(ctx, name)
}

// isolate takes away the access PostgreSQL grants to everyone by default.
//
// A new PostgreSQL database is created with CONNECT granted to PUBLIC, and
// PUBLIC includes every role on the server. On a shared host that means one
// customer's role can connect to another customer's database the moment it is
// created — it cannot read their tables without further privileges, but it can
// connect, enumerate schemas, and see what exists. That is not the isolation a
// hosting panel is expected to provide.
//
// The same is done to the public schema on modern servers, where PUBLIC holds
// CREATE by default up to PostgreSQL 14.
//
// Only databases the panel created are touched. The server's own databases are
// left exactly as the operator's installation made them.
func (p *Postgres) isolate(ctx context.Context, name string) error {
	quoted := quotePostgresIdent(name)

	if _, err := p.query(ctx, adminDatabase,
		fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC;", quoted)); err != nil {
		return err
	}

	_, err := p.query(ctx, name, "REVOKE ALL ON SCHEMA public FROM PUBLIC;")
	return err
}

// DropDatabase removes a database and everything in it.
func (p *Postgres) DropDatabase(ctx context.Context, name string) error {
	if err := validate.DatabaseName(name); err != nil {
		return err
	}

	// Sessions still connected to the database would make DROP fail. They are
	// terminated first: an operator who asked to delete a database has already
	// decided that whatever is connected to it should stop.
	terminate := fmt.Sprintf(
		"SELECT pg_terminate_backend(pid) FROM pg_stat_activity "+
			"WHERE datname = %s AND pid <> pg_backend_pid();", quotePostgresString(name))
	if _, err := p.query(ctx, adminDatabase, terminate); err != nil {
		p.log.Warn("could not terminate sessions before dropping a database",
			"database", name, "detail", err.Error())
	}

	stmt := fmt.Sprintf("DROP DATABASE IF EXISTS %s;", quotePostgresIdent(name))
	_, err := p.query(ctx, adminDatabase, stmt)
	return err
}

// DatabaseSize reports the on-disk size of a database in bytes.
func (p *Postgres) DatabaseSize(ctx context.Context, name string) (int64, error) {
	if err := validate.DatabaseName(name); err != nil {
		return 0, err
	}

	stmt := fmt.Sprintf("SELECT pg_catalog.pg_database_size(%s);", quotePostgresString(name))
	out, err := p.query(ctx, adminDatabase, stmt)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// ListUsers returns the login roles the panel may manage.
func (p *Postgres) ListUsers(ctx context.Context) ([]User, error) {
	out, err := p.query(ctx, adminDatabase,
		"SELECT rolname FROM pg_catalog.pg_roles WHERE rolcanlogin ORDER BY rolname;")
	if err != nil {
		return nil, err
	}

	users := make([]User, 0, 8)
	for _, fields := range rows(out, 1) {
		name := strings.TrimSpace(fields[0])
		if validate.DatabaseUser(name) != nil {
			continue
		}
		users = append(users, User{Username: name, Engine: validate.EnginePostgres})
	}
	return users, nil
}

// CreateUser adds a login role with a password, reporting whether it made one.
func (p *Postgres) CreateUser(ctx context.Context, user User, password string) (bool, error) {
	if err := validate.DatabaseUser(user.Username); err != nil {
		return false, err
	}
	if err := ValidatePassword(password); err != nil {
		return false, err
	}

	exists, err := p.roleExists(ctx, user.Username)
	if err != nil {
		return false, err
	}
	if exists {
		// Idempotent, like the MySQL provider. The password is deliberately not
		// reset here: a repeated create must not silently invalidate
		// credentials an application is already using. The caller is told the
		// role was not created so it does not report the password it supplied
		// as one that works.
		return false, nil
	}

	// NOSUPERUSER NOCREATEDB NOCREATEROLE is stated rather than relied on. The
	// defaults are right today, but a server whose template role was altered
	// would otherwise hand every website account the ability to read every
	// other database on the machine.
	stmt := fmt.Sprintf("CREATE ROLE %s WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE "+
		"NOINHERIT PASSWORD %s;",
		quotePostgresIdent(user.Username), quotePostgresString(password))
	if _, err := p.query(ctx, adminDatabase, stmt); err != nil {
		return false, err
	}
	return true, nil
}

// DropUser removes a login role.
//
// A role that owns anything cannot be dropped, so what it owns inside each
// database is dropped first. That is destructive and deliberate: an operator
// deleting a database user is told, before reaching here, that the objects
// that user owns go with it.
func (p *Postgres) DropUser(ctx context.Context, user User) error {
	if err := validate.DatabaseUser(user.Username); err != nil {
		return err
	}

	exists, err := p.roleExists(ctx, user.Username)
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}

	databases, err := p.ListDatabases(ctx)
	if err != nil {
		return err
	}

	role := quotePostgresIdent(user.Username)
	for _, database := range databases {
		// DROP OWNED BY only affects the database it is run in, so it has to
		// be run in each one. A database this role never touched simply has
		// nothing to drop.
		if _, err := p.query(ctx, database.Name, fmt.Sprintf("DROP OWNED BY %s;", role)); err != nil {
			p.log.Warn("could not release a role's objects before dropping it",
				"role", user.Username, "database", database.Name, "detail", err.Error())
		}
	}

	_, err = p.query(ctx, adminDatabase, fmt.Sprintf("DROP ROLE IF EXISTS %s;", role))
	return err
}

// SetPassword changes a role's password.
func (p *Postgres) SetPassword(ctx context.Context, user User, password string) error {
	if err := validate.DatabaseUser(user.Username); err != nil {
		return err
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}

	stmt := fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s;",
		quotePostgresIdent(user.Username), quotePostgresString(password))
	_, err := p.query(ctx, adminDatabase, stmt)
	return err
}

// Grant gives a role a privilege level on one database.
//
// PostgreSQL privileges are per-object, not per-database, so this is three
// grants: CONNECT on the database, rights on the public schema, and rights on
// what is in it — plus default privileges, without which every table created
// afterwards would be invisible to the role. Panels that grant only CONNECT
// are the reason "permission denied for table" is such a common complaint.
func (p *Postgres) Grant(ctx context.Context, grant Grant) error {
	if err := validate.DatabaseUser(grant.Username); err != nil {
		return err
	}
	if err := validate.DatabaseName(grant.Database); err != nil {
		return err
	}
	if !ValidPrivilege(grant.Privilege) {
		return fmt.Errorf("unknown privilege level %q", grant.Privilege)
	}

	role := quotePostgresIdent(grant.Username)
	database := quotePostgresIdent(grant.Database)

	if _, err := p.query(ctx, adminDatabase,
		fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s;", database, role)); err != nil {
		return err
	}

	statements := postgresGrantStatements(grant.Privilege, role)
	_, err := p.query(ctx, grant.Database, strings.Join(statements, "\n"))
	return err
}

// RevokeAll removes a role's access to one database.
func (p *Postgres) RevokeAll(ctx context.Context, user User, database string) error {
	if err := validate.DatabaseUser(user.Username); err != nil {
		return err
	}
	if err := validate.DatabaseName(database); err != nil {
		return err
	}

	role := quotePostgresIdent(user.Username)
	inside := []string{
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM %s;", role),
		fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM %s;", role),
		fmt.Sprintf("REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %s;", role),
		fmt.Sprintf("REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM %s;", role),
		fmt.Sprintf("REVOKE ALL ON SCHEMA public FROM %s;", role),
	}
	if _, err := p.query(ctx, database, strings.Join(inside, "\n")); err != nil {
		return err
	}

	_, err := p.query(ctx, adminDatabase, fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM %s;",
		quotePostgresIdent(database), role))
	return err
}

// postgresGrantStatements renders the in-database half of a grant.
func postgresGrantStatements(level, role string) []string {
	switch level {
	case PrivilegeReadOnly:
		return []string{
			fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s;", role),
			fmt.Sprintf("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s;", role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO %s;", role),
		}
	case PrivilegeReadWrite:
		return []string{
			fmt.Sprintf("GRANT USAGE ON SCHEMA public TO %s;", role),
			fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %s;", role),
			fmt.Sprintf("GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %s;", role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public "+
				"GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %s;", role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public "+
				"GRANT USAGE, SELECT ON SEQUENCES TO %s;", role),
		}
	default: // PrivilegeFull
		return []string{
			// CREATE on the schema is what lets an application run its own
			// migrations, which is the whole point of "full" for a website.
			fmt.Sprintf("GRANT ALL ON SCHEMA public TO %s;", role),
			fmt.Sprintf("GRANT ALL ON ALL TABLES IN SCHEMA public TO %s;", role),
			fmt.Sprintf("GRANT ALL ON ALL SEQUENCES IN SCHEMA public TO %s;", role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON TABLES TO %s;", role),
			fmt.Sprintf("ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT ALL ON SEQUENCES TO %s;", role),
		}
	}
}

func (p *Postgres) databaseExists(ctx context.Context, name string) (bool, error) {
	out, err := p.query(ctx, adminDatabase, fmt.Sprintf(
		"SELECT 1 FROM pg_catalog.pg_database WHERE datname = %s;", quotePostgresString(name)))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

func (p *Postgres) roleExists(ctx context.Context, name string) (bool, error) {
	out, err := p.query(ctx, adminDatabase, fmt.Sprintf(
		"SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = %s;", quotePostgresString(name)))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// query runs SQL in one database and returns psql's unaligned output.
func (p *Postgres) query(ctx context.Context, database, sql string) (string, error) {
	// "postgres" is the maintenance database every statement that cannot run
	// inside a target database goes to. It is on the reserved list precisely
	// so nobody can create or drop it, which is why it is named here rather
	// than passed through the validator.
	if database != adminDatabase {
		if err := validate.DatabaseName(database); err != nil {
			return "", err
		}
	}

	args := []string{
		// No psqlrc: an operator's ~/.psqlrc could set output formatting that
		// changes what is parsed here, or run statements of its own.
		"--no-psqlrc",
		// Fail on the first error rather than continuing and exiting zero.
		"--set=ON_ERROR_STOP=1",
		"--quiet", "--tuples-only", "--no-align",
		// psql's unaligned output separates fields with "|", not a tab. Without
		// this every multi-column result parses as a single field and the
		// listing silently comes back empty — which is exactly what it did
		// before this line existed.
		"--field-separator=\t",
		// Never prompt: a password prompt on a daemon's stdin would hang until
		// the operation timed out.
		"--no-password",
		"--host=" + p.host,
		"--port=" + strconv.Itoa(p.port),
		"--username=" + p.user,
		"--dbname=" + database,
		// Read the statements from standard input, so nothing here — least of
		// all a password — appears in the process table.
		"--file=-",
	}

	opts := command.Options{Stdin: sql}
	if p.passFile != "" {
		opts.Env = map[string]string{"PGPASSFILE": p.passFile}
	}

	result, err := p.runner.RunWith(ctx, CommandPsql, opts, args...)
	if err != nil {
		return "", err
	}
	if !result.Succeeded() {
		return "", fmt.Errorf("%w: %s", ErrQueryFailed, clientError(result.Stderr))
	}
	return result.Stdout, nil
}

// writePassFile stores the admin password in libpq's pgpass format.
func writePassFile(port int, user, password string) (string, error) {
	dir, err := os.MkdirTemp("", "jothost-pg-")
	if err != nil {
		return "", fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure credentials directory: %w", err)
	}

	// The host field is a wildcard rather than the configured host.
	//
	// libpq matches a Unix-socket connection against the literal "localhost",
	// not against the socket directory, so a line naming /run/postgresql
	// matches nothing and psql fails with "no password supplied" — which is
	// exactly what it did before this comment existed. A wildcard is safe
	// here: the file is written for one server and lives only as long as the
	// process.
	//
	// libpq also refuses a pgpass file that is group- or world-readable, which
	// is behaviour relied on here rather than merely tolerated.
	line := fmt.Sprintf("*:%d:*:%s:%s\n", port, escapePgpass(user), escapePgpass(password))

	path := filepath.Join(dir, "pgpass")
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		return "", fmt.Errorf("write credentials file: %w", err)
	}
	return path, nil
}

// escapePgpass escapes the field separator and the escape character itself.
func escapePgpass(value string) string {
	return strings.NewReplacer(`\`, `\\`, `:`, `\:`).Replace(value)
}

// quotePostgresIdent renders an identifier.
//
// Double quotes, with any embedded double quote doubled. As on MySQL the name
// has already been validated to [a-z0-9_], so this is the second of two
// defences rather than the only one.
func quotePostgresIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quotePostgresString renders a string literal.
//
// Standard SQL doubling rather than backslash escapes: PostgreSQL's
// standard_conforming_strings has been on by default since 9.1, which makes a
// backslash an ordinary character, so escaping with one would be wrong.
func quotePostgresString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

// shortPostgresVersion trims "PostgreSQL 16.3 on x86_64-pc-linux-musl..." down
// to something a panel can put in a table cell.
func shortPostgresVersion(full string) string {
	fields := strings.Fields(strings.TrimSpace(full))
	if len(fields) >= 2 && fields[0] == "PostgreSQL" {
		return fields[1]
	}
	if len(fields) > 0 {
		return fields[0]
	}
	return ""
}
