package database

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// CommandMySQL is the allowlist name for the MySQL/MariaDB client.
//
// One entry covers both: the MariaDB client is a fork of the MySQL client and
// speaks the same protocol, and distributions ship one or the other under
// either name. Which server is actually behind it is decided by asking, not by
// which binary was found.
const CommandMySQL = "mysql"

// probeTimeout bounds the version probe run at startup. It is short
// because the answer only decides what the panel offers, and a database server
// that cannot answer SELECT VERSION() in a few seconds is not usable anyway.
const probeTimeout = 5 * time.Second

// systemSchemas are never listed as manageable databases and never dropped.
var systemSchemas = map[string]struct{}{
	"mysql": {}, "information_schema": {}, "performance_schema": {}, "sys": {},
}

// MySQLOptions configure the MySQL/MariaDB provider.
type MySQLOptions struct {
	Runner *command.Runner
	// Socket is the server's Unix socket. A local socket is preferred over TCP
	// because it needs no password on a normal installation: the server
	// authenticates root by the connecting process's uid.
	Socket string
	// AdminUser is the account the Agent administers the server as.
	AdminUser string
	// AdminPassword is optional. When empty, socket authentication is used and
	// no credentials file is written at all.
	AdminPassword string
	// FallbackEngine is what to report when the server cannot be probed, so an
	// unreachable server is still named in the panel.
	FallbackEngine string
	Log            *slog.Logger
}

// MySQL drives a local MySQL or MariaDB server through its client.
type MySQL struct {
	runner    *command.Runner
	socket    string
	user      string
	engine    string
	version   string
	available bool
	// optionFile holds the admin password when one is configured. It is
	// created mode 0600 inside a mode-0700 directory, and its path is passed
	// to the client with --defaults-extra-file so the password never appears
	// in argv, where the process table would publish it.
	optionFile string
	// probeErr is the startup probe's failure, kept so the panel can say what
	// went wrong rather than only that something did.
	probeErr string
	log      *slog.Logger
}

// NewMySQL builds the provider and probes the server once.
//
// The probe happens here rather than on first use so the Agent can report at
// startup what this host can do, which is the same contract every other
// provider in the Agent follows.
func NewMySQL(ctx context.Context, opts MySQLOptions) *MySQL {
	engine := opts.FallbackEngine
	if engine == "" {
		engine = validate.EngineMariaDB
	}

	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	provider := &MySQL{
		runner: opts.Runner,
		socket: opts.Socket,
		user:   opts.AdminUser,
		engine: engine,
		log:    log,
	}
	if provider.user == "" {
		provider.user = "root"
	}
	if provider.runner == nil || !provider.runner.Available(CommandMySQL) {
		return provider
	}

	if opts.AdminPassword != "" {
		path, err := writeOptionFile(provider.user, opts.AdminPassword, provider.socket)
		if err != nil {
			// Without the credentials file the client would fall back to
			// socket authentication and appear to work on some hosts and not
			// others. Stopping here makes the misconfiguration visible.
			provider.probeErr = err.Error()
			return provider
		}
		provider.optionFile = path
	}

	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	version, err := provider.query(probeCtx, "SELECT VERSION();")
	if err != nil {
		provider.probeErr = firstLine(err.Error())
		log.Warn("mysql did not answer its version probe", "detail", provider.probeErr)
		return provider
	}

	provider.version = strings.TrimSpace(version)
	provider.available = provider.version != ""
	if strings.Contains(strings.ToLower(provider.version), "mariadb") {
		provider.engine = validate.EngineMariaDB
	} else if provider.version != "" {
		provider.engine = validate.EngineMySQL
	}
	return provider
}

// Engine reports which server was actually found.
func (m *MySQL) Engine() string { return m.engine }

// Available reports whether the server answered.
func (m *MySQL) Available() bool { return m.available }

// Unavailable explains why the engine cannot be used, or returns empty.
func (m *MySQL) Unavailable() string {
	if m.available {
		return ""
	}
	if m.runner == nil || !m.runner.Available(CommandMySQL) {
		return "no MySQL or MariaDB client is installed on this host"
	}
	if m.probeErr != "" {
		return "the server is installed but did not answer: " + m.probeErr
	}
	return "the server is installed but did not answer"
}

// SupportsHostPatterns is true: a MySQL account is a user and host pair.
func (m *MySQL) SupportsHostPatterns() bool { return true }

// Version returns the server version string.
func (m *MySQL) Version(ctx context.Context) (string, error) {
	if m.version != "" {
		return m.version, nil
	}
	version, err := m.query(ctx, "SELECT VERSION();")
	return strings.TrimSpace(version), err
}

// ListDatabases returns every database the panel may manage.
func (m *MySQL) ListDatabases(ctx context.Context) ([]Database, error) {
	// Size is joined in rather than fetched per database: one query is one
	// round trip, and a host with fifty sites would otherwise pay fifty.
	const stmt = `SELECT s.SCHEMA_NAME, s.DEFAULT_CHARACTER_SET_NAME, s.DEFAULT_COLLATION_NAME,
		COALESCE(SUM(t.DATA_LENGTH + t.INDEX_LENGTH), 0)
	FROM information_schema.SCHEMATA s
	LEFT JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = s.SCHEMA_NAME
	GROUP BY s.SCHEMA_NAME, s.DEFAULT_CHARACTER_SET_NAME, s.DEFAULT_COLLATION_NAME
	ORDER BY s.SCHEMA_NAME;`

	out, err := m.query(ctx, stmt)
	if err != nil {
		return nil, err
	}

	databases := make([]Database, 0, 8)
	for _, fields := range rows(out, 4) {
		name := fields[0]
		if _, system := systemSchemas[name]; system {
			continue
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		databases = append(databases, Database{
			Name:      name,
			Engine:    m.engine,
			Charset:   nullable(fields[1]),
			Collation: nullable(fields[2]),
			SizeBytes: size,
		})
	}
	return databases, nil
}

// CreateDatabase creates a database, or reports that it is already there.
//
// IF NOT EXISTS makes this idempotent (CLAUDE.md section 17): a retried
// operation reconciles rather than failing, which matters because the caller
// may be retrying precisely because it never learned the first attempt
// succeeded.
func (m *MySQL) CreateDatabase(ctx context.Context, name string) error {
	if err := validate.DatabaseName(name); err != nil {
		return err
	}

	// utf8mb4 is not the default on older servers, and utf8 there is a
	// three-byte encoding that silently mangles anything outside the BMP —
	// emoji, most notably. Pinning it means a site created through the panel
	// stores what its users type.
	stmt := fmt.Sprintf(
		"CREATE DATABASE IF NOT EXISTS %s CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;",
		quoteMySQLIdent(name))
	_, err := m.query(ctx, stmt)
	return err
}

// DropDatabase removes a database and everything in it.
func (m *MySQL) DropDatabase(ctx context.Context, name string) error {
	if err := validate.DatabaseName(name); err != nil {
		return err
	}
	if _, system := systemSchemas[name]; system {
		return fmt.Errorf("refusing to drop the server's own schema %q", name)
	}

	stmt := fmt.Sprintf("DROP DATABASE IF EXISTS %s;", quoteMySQLIdent(name))
	_, err := m.query(ctx, stmt)
	return err
}

// DatabaseSize reports the on-disk size of a database in bytes.
func (m *MySQL) DatabaseSize(ctx context.Context, name string) (int64, error) {
	if err := validate.DatabaseName(name); err != nil {
		return 0, err
	}

	stmt := fmt.Sprintf(
		"SELECT COALESCE(SUM(DATA_LENGTH + INDEX_LENGTH), 0) FROM information_schema.TABLES "+
			"WHERE TABLE_SCHEMA = %s;", quoteMySQLString(name))
	out, err := m.query(ctx, stmt)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// ListUsers returns the accounts the panel may manage.
//
// Accounts whose names the panel would refuse to create are excluded rather
// than listed and then rejected: root and the server's own maintenance
// accounts are not something an operator should be offered a delete button
// for.
func (m *MySQL) ListUsers(ctx context.Context) ([]User, error) {
	out, err := m.query(ctx, "SELECT User, Host FROM mysql.user ORDER BY User, Host;")
	if err != nil {
		return nil, err
	}

	users := make([]User, 0, 8)
	for _, fields := range rows(out, 2) {
		if validate.DatabaseUser(fields[0]) != nil {
			continue
		}
		users = append(users, User{Username: fields[0], Host: fields[1], Engine: m.engine})
	}
	return users, nil
}

// CreateUser adds an account with a password, reporting whether it made one.
//
// Existence is checked before the CREATE rather than relying on IF NOT EXISTS
// alone. IF NOT EXISTS makes the operation idempotent, which is what CLAUDE.md
// section 17 asks for, but it also means an existing account keeps the password
// it already had — and a caller that could not tell the difference would return
// the newly generated password to the operator as though it worked. Reporting
// the distinction is what lets the panel refuse to make that claim.
func (m *MySQL) CreateUser(ctx context.Context, user User, password string) (bool, error) {
	account, err := m.account(user)
	if err != nil {
		return false, err
	}
	if err := ValidatePassword(password); err != nil {
		return false, err
	}

	exists, err := m.userExists(ctx, user)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	stmt := fmt.Sprintf("CREATE USER IF NOT EXISTS %s IDENTIFIED BY %s;",
		account, quoteMySQLString(password))
	if _, err := m.query(ctx, stmt); err != nil {
		return false, err
	}
	return true, nil
}

// userExists reports whether an account is already on the server.
func (m *MySQL) userExists(ctx context.Context, user User) (bool, error) {
	host := user.Host
	if host == "" {
		host = "localhost"
	}

	stmt := fmt.Sprintf("SELECT 1 FROM mysql.user WHERE User = %s AND Host = %s;",
		quoteMySQLString(user.Username), quoteMySQLString(host))
	out, err := m.query(ctx, stmt)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

// DropUser removes an account.
func (m *MySQL) DropUser(ctx context.Context, user User) error {
	account, err := m.account(user)
	if err != nil {
		return err
	}

	stmt := fmt.Sprintf("DROP USER IF EXISTS %s;", account)
	_, err = m.query(ctx, stmt)
	return err
}

// SetPassword changes an account's password.
func (m *MySQL) SetPassword(ctx context.Context, user User, password string) error {
	account, err := m.account(user)
	if err != nil {
		return err
	}
	if err := ValidatePassword(password); err != nil {
		return err
	}

	stmt := fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s;", account, quoteMySQLString(password))
	_, err = m.query(ctx, stmt)
	return err
}

// Grant gives an account a privilege level on one database.
//
// Existing privileges are revoked first so a grant is a statement of the whole
// access an account has, not an addition to whatever it had before: lowering
// someone from full to readonly must actually take away DROP, and a bare GRANT
// would leave it in place.
func (m *MySQL) Grant(ctx context.Context, grant Grant) error {
	account, err := m.account(User{Username: grant.Username, Host: grant.Host})
	if err != nil {
		return err
	}
	if err := validate.DatabaseName(grant.Database); err != nil {
		return err
	}
	privileges, err := mysqlPrivileges(grant.Privilege)
	if err != nil {
		return err
	}

	scope := quoteMySQLIdent(grant.Database) + ".*"

	// Whatever the account already holds is taken away first, so a grant is a
	// statement of its whole access rather than an addition to what it had.
	// Without this, lowering someone from full to readonly leaves DROP in
	// place and the panel reports a restriction the server is not applying.
	if err := m.revokeScope(ctx, scope, account); err != nil {
		return err
	}

	stmt := fmt.Sprintf("GRANT %s ON %s TO %s;\nFLUSH PRIVILEGES;", privileges, scope, account)
	_, err = m.query(ctx, stmt)
	return err
}

// revokeScope removes every privilege an account holds on one scope.
//
// The privileges and the grant option are revoked in two statements, because
// the combined `REVOKE ALL PRIVILEGES, GRANT OPTION` form takes no ON clause at
// all — it is the global one. Written with an ON clause it is a syntax error,
// and because the failure was previously logged and discarded, lowering a
// grant silently did nothing while reporting success.
//
// Only one failure is tolerated: "there is no such grant", which is the normal
// answer the first time an account is granted anything. Everything else is
// returned, so a statement this code gets wrong cannot be invisible again.
func (m *MySQL) revokeScope(ctx context.Context, scope, account string) error {
	statements := []string{
		fmt.Sprintf("REVOKE ALL PRIVILEGES ON %s FROM %s;", scope, account),
		fmt.Sprintf("REVOKE GRANT OPTION ON %s FROM %s;", scope, account),
	}

	for _, stmt := range statements {
		if _, err := m.query(ctx, stmt); err != nil {
			if !isNoSuchGrant(err) {
				return err
			}
			m.log.Debug("no existing grant to revoke", "engine", m.engine, "statement", stmt)
		}
	}
	return nil
}

// isNoSuchGrant reports whether a revoke failed only because there was nothing
// to revoke (MySQL and MariaDB error 1141).
func isNoSuchGrant(err error) bool {
	message := err.Error()
	return strings.Contains(message, "1141") || strings.Contains(message, "no such grant")
}

// RevokeAll removes an account's access to one database.
func (m *MySQL) RevokeAll(ctx context.Context, user User, database string) error {
	account, err := m.account(user)
	if err != nil {
		return err
	}
	if err := validate.DatabaseName(database); err != nil {
		return err
	}

	if err := m.revokeScope(ctx, quoteMySQLIdent(database)+".*", account); err != nil {
		return err
	}

	_, err = m.query(ctx, "FLUSH PRIVILEGES;")
	return err
}

// account validates and renders a 'user'@'host' pair.
func (m *MySQL) account(user User) (string, error) {
	if err := validate.DatabaseUser(user.Username); err != nil {
		return "", err
	}
	host := user.Host
	if host == "" {
		host = "localhost"
	}
	if err := validate.DatabaseHostPattern(host); err != nil {
		return "", err
	}
	return quoteMySQLString(user.Username) + "@" + quoteMySQLString(host), nil
}

// query runs SQL against the server and returns its tab-separated output.
//
// The SQL goes on standard input. Passing it with -e would put a CREATE USER
// statement — password and all — into the process table.
func (m *MySQL) query(ctx context.Context, sql string) (string, error) {
	args := []string{
		// --raw stops the client escaping the values it prints, which would
		// otherwise have to be undone here.
		// --batch gives tab-separated, unadorned output and, crucially, makes
		// the client stop and exit non-zero on the first failed statement.
		// Without it a broken grant would be reported as a success.
		"--batch", "--raw", "--skip-column-names",
	}
	if m.optionFile != "" {
		// This must come first: the client reads option files in argument
		// order, and a later --user would be overridden by the file.
		args = append([]string{"--defaults-extra-file=" + m.optionFile}, args...)
	} else {
		args = append(args, "--user="+m.user)
		if m.socket != "" {
			args = append(args, "--socket="+m.socket)
		}
	}

	result, err := m.runner.RunWith(ctx, CommandMySQL, command.Options{Stdin: sql}, args...)
	if err != nil {
		return "", err
	}
	if !result.Succeeded() {
		return "", fmt.Errorf("%w: %s", ErrQueryFailed, clientError(result.Stderr))
	}
	return result.Stdout, nil
}

// writeOptionFile stores admin credentials where only root can read them.
func writeOptionFile(user, password, socket string) (string, error) {
	dir, err := os.MkdirTemp("", "jothost-db-")
	if err != nil {
		return "", fmt.Errorf("create credentials directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure credentials directory: %w", err)
	}

	var builder strings.Builder
	builder.WriteString("[client]\n")
	builder.WriteString("user=" + quoteOptionValue(user) + "\n")
	builder.WriteString("password=" + quoteOptionValue(password) + "\n")
	if socket != "" {
		builder.WriteString("socket=" + quoteOptionValue(socket) + "\n")
	}

	path := filepath.Join(dir, "admin.cnf")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		return "", fmt.Errorf("write credentials file: %w", err)
	}
	return path, nil
}

// quoteOptionValue renders a value for a MySQL option file.
//
// The quoting is not decoration. An option file treats both '#' and ';' as the
// start of a comment and strips trailing whitespace, so an unquoted
// `password=ab#cd` authenticates with "ab" — silently, and only for the
// passwords that happen to contain one. Both characters are in the panel's own
// password alphabet, so roughly one generated password in five would be
// affected.
//
// Inside double quotes the client honours backslash escapes, so the backslash
// and the quote are escaped and everything else is literal.
func quoteOptionValue(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + escaped + `"`
}

// mysqlPrivileges maps a panel privilege level to a fixed GRANT list.
func mysqlPrivileges(level string) (string, error) {
	switch level {
	case PrivilegeReadOnly:
		return "SELECT", nil
	case PrivilegeReadWrite:
		return "SELECT, INSERT, UPDATE, DELETE", nil
	case PrivilegeFull:
		// Scoped to one database by the ON clause, so this is not server-wide
		// superuser: it excludes SUPER, FILE, and PROCESS, which are global.
		return "ALL PRIVILEGES", nil
	default:
		return "", fmt.Errorf("unknown privilege level %q", level)
	}
}

// quoteMySQLIdent renders an identifier.
//
// The name has already been through validate.DatabaseName, which permits only
// [a-z0-9_], so there is nothing here to escape. The doubling is kept anyway:
// this function must be correct on its own terms, so that it stays safe if a
// future caller reaches it by a path that skipped validation.
func quoteMySQLIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

// quoteMySQLString renders a string literal.
func quoteMySQLString(value string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
		"\x00", `\0`,
		"\n", `\n`,
		"\r", `\r`,
	)
	return "'" + replacer.Replace(value) + "'"
}

// rows splits tab-separated client output into fields, skipping short lines.
func rows(out string, fields int) [][]string {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	parsed := make([][]string, 0, len(lines))

	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\t")
		if len(parts) < fields {
			continue
		}
		parsed = append(parsed, parts)
	}
	return parsed
}

// nullable turns the client's literal NULL into an empty string.
func nullable(value string) string {
	if value == "NULL" {
		return ""
	}
	return value
}

// clientError extracts the meaningful failure from a database client's stderr.
//
// Taking the first line is not good enough, for two separate reasons.
//
// The mariadb client, reading statements from standard input, echoes each one
// to stderr between rows of dashes. So the first line is "--------------": a
// useless message for a user, and — worse — invisible to any code matching on
// it, which is exactly how a REVOKE with invalid syntax went unnoticed while
// the panel reported the grant as changed.
//
// The echoed statement itself must never be returned either. The statement
// that sets a password contains that password, and an error message is
// something the panel shows to a user and writes to a log (CLAUDE.md section
// 14). The echo is therefore skipped as a block, not merely as a first line.
func clientError(stderr string) string {
	lines := strings.Split(stderr, "\n")

	// The line that names the error. "ERROR" is MySQL's prefix; psql writes
	// "psql: error:".
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if strings.HasPrefix(line, "ERROR") || strings.Contains(line, "error:") {
			return line
		}
	}

	// Nothing named itself an error. Fall back to the first line that is
	// neither decoration nor inside an echoed statement block.
	echoing := false
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if isDashRule(line) {
			echoing = !echoing
			continue
		}
		if echoing || line == "" {
			continue
		}
		return line
	}
	return "the database server rejected the statement"
}

// isDashRule reports whether a line is one of the client's echo separators.
func isDashRule(line string) bool {
	if len(line) < 3 {
		return false
	}
	return strings.Trim(line, "-") == ""
}

// firstLine returns the first line of an error string.
func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "the database server rejected the statement"
	}
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	return value
}
