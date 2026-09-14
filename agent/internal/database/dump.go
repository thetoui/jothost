package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/jothost/panel/agent/internal/command"
	"github.com/jothost/panel/shared/validate"
)

// Dumping and reloading a database.
//
// This lives with the providers rather than with the backup package because the
// credentials do. The admin password — when one exists at all — reaches a client
// through a mode-0600 option file or PGPASSFILE that these providers wrote at
// startup; a backup package that dumped databases itself would need its own copy
// of that arrangement, and two copies of a credential path is one more than can
// be reviewed.
//
// Both directions write to and read from a *file*, never a pipe held in memory.
// A customer's database may be gigabytes, and a dump buffered in the Agent's
// heap is an Agent that dies on the largest database it is asked to protect —
// which is the one most worth protecting.

// Allowlist names for the two dump tools.
const (
	// CommandMysqldump is mysqldump or mariadb-dump; a MariaDB host ships the
	// latter and symlinks the former.
	CommandMysqldump = "mysqldump"
	// CommandPgDump is PostgreSQL's dump tool.
	CommandPgDump = "pg_dump"
)

// Errors dumping and restoring.
var (
	// ErrDumpUnsupported means this engine cannot be dumped on this host,
	// almost always because the tool is not installed.
	ErrDumpUnsupported = errors.New("this host cannot dump that engine")
	// ErrDumpFailed wraps a non-zero exit from a dump or a reload.
	ErrDumpFailed = errors.New("database dump failed")
)

// Dumper is a provider that can write a database out and read it back.
//
// It is a separate interface from Provider rather than more methods on it
// because the two are answered by different programs: a host can have a working
// mysql client and no mysqldump, and a provider that had to claim both would
// have to lie about one.
type Dumper interface {
	// CanDump reports whether this host has the tool.
	CanDump() bool
	// Dump writes database into destination, which must not already exist.
	Dump(ctx context.Context, database, destination string) error
	// Reload replays a dump into database, which must already exist.
	Reload(ctx context.Context, database, source string) error
}

// PanelDumper is implemented by the engine that holds the panel's own
// database. See Postgres.DumpPanel.
type PanelDumper interface {
	DumpPanel(ctx context.Context, database, destination string) error
}

// DumperFor returns the dumper for an engine.
func (m *Manager) DumperFor(engine string) (Dumper, error) {
	provider, err := m.Provider(engine)
	if err != nil {
		return nil, err
	}
	dumper, ok := provider.(Dumper)
	if !ok || !dumper.CanDump() {
		return nil, fmt.Errorf("%w: %s", ErrDumpUnsupported, engine)
	}
	return dumper, nil
}

// checkDumpTarget refuses a destination that is not somewhere the Agent made.
//
// The path always comes from the Agent's own staging directory, never from a
// request, and this is what keeps that true if some future caller forgets.
func checkDumpTarget(destination string) error {
	if !filepath.IsAbs(destination) {
		return fmt.Errorf("%w: a dump destination must be absolute", ErrDumpFailed)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("%w: %s already exists", ErrDumpFailed, destination)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrDumpFailed, err)
	}
	return nil
}

// ---------------------------------------------------------------- MySQL

// CanDump reports whether mysqldump is on this host.
func (m *MySQL) CanDump() bool {
	return m.runner != nil && m.runner.Available(CommandMysqldump)
}

// Dump writes one database to destination with mysqldump.
//
// --single-transaction is what makes the dump internally consistent without
// locking the tables an application is using: it takes one repeatable-read
// snapshot instead of blocking writes for the length of the dump. A backup that
// takes a site offline is a backup nobody schedules.
//
// --routines, --triggers and --events are named because mysqldump does not
// include them by default. A restore missing its stored procedures is the kind
// of failure that is only discovered by the application, weeks later.
func (m *MySQL) Dump(ctx context.Context, database, destination string) error {
	if err := validate.DatabaseName(database); err != nil {
		return err
	}
	if err := checkDumpTarget(destination); err != nil {
		return err
	}
	if !m.CanDump() {
		return fmt.Errorf("%w: mysqldump is not installed", ErrDumpUnsupported)
	}

	args := m.clientAuthArgs()
	args = append(args,
		"--single-transaction",
		"--routines", "--triggers", "--events",
		// The dump is replayed into a database the panel has already created,
		// so it must not carry a CREATE DATABASE or a USE of its own: those
		// would send the restore to whatever the dump was called rather than
		// where the operator asked for it.
		"--no-create-db",
		// Written straight to the file by the client. Redirecting its standard
		// output through the Agent would mean the whole dump passing through
		// memory for no reason.
		"--result-file="+destination,
		database,
	)

	result, err := m.runner.Run(ctx, CommandMysqldump, args...)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("%w: %s", ErrDumpFailed, err)
	}
	if !result.Succeeded() {
		// A failed dump leaves a partial file, and a partial dump that stayed
		// on disk would be archived and later restored as if it were whole.
		_ = os.Remove(destination)
		return fmt.Errorf("%w: %s", ErrDumpFailed, clientError(result.Stderr))
	}
	return nil
}

// Reload replays a dump into an existing database.
func (m *MySQL) Reload(ctx context.Context, database, source string) error {
	if err := validate.DatabaseName(database); err != nil {
		return err
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("%w: a dump source must be absolute", ErrDumpFailed)
	}
	if m.runner == nil || !m.runner.Available(CommandMySQL) {
		return fmt.Errorf("%w: the mysql client is not installed", ErrDumpUnsupported)
	}

	args := m.clientAuthArgs()
	args = append(args,
		// Stop on the first failed statement. Without it a dump that fails
		// halfway exits zero and the restore reports success over a database
		// that is now half one thing and half another.
		"--batch", "--force=FALSE",
		"--database="+database,
	)

	result, err := m.runner.RunWith(ctx, CommandMySQL,
		command.Options{StdinFile: source}, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDumpFailed, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrDumpFailed, clientError(result.Stderr))
	}
	return nil
}

// clientAuthArgs returns the credential arguments shared by the client and the
// dump tool, which read the same option file.
func (m *MySQL) clientAuthArgs() []string {
	if m.optionFile != "" {
		// First, as in query(): the client reads option files in argument
		// order and a later --user would override the file.
		return []string{"--defaults-extra-file=" + m.optionFile}
	}
	args := []string{"--user=" + m.user}
	if m.socket != "" {
		args = append(args, "--socket="+m.socket)
	}
	return args
}

// ------------------------------------------------------------ PostgreSQL

// CanDump reports whether pg_dump is on this host.
func (p *Postgres) CanDump() bool {
	return p.runner != nil && p.runner.Available(CommandPgDump)
}

// Dump writes one database to destination with pg_dump.
//
// The plain SQL format is used rather than pg_dump's custom archive, because a
// plain dump can be replayed by psql — which this host already has and has
// already been given credentials — while a custom archive needs pg_restore,
// a third tool to allowlist and a second credential path to maintain. The
// trade-off is that a plain dump cannot be restored selectively, which is not
// something this panel offers.
func (p *Postgres) Dump(ctx context.Context, database, destination string) error {
	if err := validate.DatabaseName(database); err != nil {
		return err
	}
	return p.dump(ctx, database, destination)
}

// DumpPanel writes the panel's own database to destination.
//
// Dump refuses it, and must: it is reached from a database download as well as
// a backup, and the panel's database is reserved so that neither can be pointed
// at it. A panel backup is the one caller that has to dump exactly that
// database, and it names it from the Agent's configuration, never a request —
// so the exemption is a separate method rather than a relaxed check on the
// shared one.
func (p *Postgres) DumpPanel(ctx context.Context, database, destination string) error {
	if err := validate.DatabaseIdentifier(database); err != nil {
		return err
	}
	return p.dump(ctx, database, destination)
}

func (p *Postgres) dump(ctx context.Context, database, destination string) error {
	if err := checkDumpTarget(destination); err != nil {
		return err
	}
	if !p.CanDump() {
		return fmt.Errorf("%w: pg_dump is not installed", ErrDumpUnsupported)
	}

	args := []string{
		"--host=" + p.host,
		"--port=" + strconv.Itoa(p.port),
		"--username=" + p.user,
		"--dbname=" + database,
		// Never prompt: a password prompt on a daemon's stdin hangs until the
		// operation times out.
		"--no-password",
		// Ownership and privileges are the panel's to set when it creates a
		// database, and a dump that re-asserts them fails on a host where the
		// roles are named differently — which is every restore onto a new
		// machine, the case this exists for.
		"--no-owner", "--no-privileges",
		"--file=" + destination,
	}

	opts := command.Options{}
	if p.passFile != "" {
		opts.Env = map[string]string{passFileEnv: p.passFile}
	}

	result, err := p.runner.RunWith(ctx, CommandPgDump, opts, args...)
	if err != nil {
		_ = os.Remove(destination)
		return fmt.Errorf("%w: %s", ErrDumpFailed, err)
	}
	if !result.Succeeded() {
		_ = os.Remove(destination)
		return fmt.Errorf("%w: %s", ErrDumpFailed, clientError(result.Stderr))
	}
	return nil
}

// Reload replays a dump into an existing database.
func (p *Postgres) Reload(ctx context.Context, database, source string) error {
	if err := validate.DatabaseName(database); err != nil {
		return err
	}
	if !filepath.IsAbs(source) {
		return fmt.Errorf("%w: a dump source must be absolute", ErrDumpFailed)
	}
	if p.runner == nil || !p.runner.Available(CommandPsql) {
		return fmt.Errorf("%w: the psql client is not installed", ErrDumpUnsupported)
	}

	args := []string{
		"--no-psqlrc",
		// Stop on the first error. psql's default is to carry on and exit
		// zero, which would report a half-applied dump as a successful restore.
		"--set=ON_ERROR_STOP=1",
		"--quiet",
		"--no-password",
		"--host=" + p.host,
		"--port=" + strconv.Itoa(p.port),
		"--username=" + p.user,
		"--dbname=" + database,
		"--file=" + source,
	}

	opts := command.Options{}
	if p.passFile != "" {
		opts.Env = map[string]string{passFileEnv: p.passFile}
	}

	result, err := p.runner.RunWith(ctx, CommandPsql, opts, args...)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDumpFailed, err)
	}
	if !result.Succeeded() {
		return fmt.Errorf("%w: %s", ErrDumpFailed, clientError(result.Stderr))
	}
	return nil
}
