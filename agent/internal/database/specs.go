package database

import (
	"time"

	"github.com/jothost/panel/agent/internal/command"
)

// passFileEnv is how the admin password reaches libpq: a path to a mode-0600
// file this package wrote, never the password itself in argv or the
// environment.
const passFileEnv = "PGPASSFILE"

// PsqlSpec is the allowlist entry for the PostgreSQL client.
//
// Defined here, beside the code that sets PGPASSFILE, rather than in the
// Agent's main. The pg_dump entry used to live there without it, and every
// PostgreSQL dump on a host where the Agent authenticates with a password was
// refused by the allowlist - which is every installed host.
func PsqlSpec(path string) command.Spec {
	return command.Spec{
		Name: CommandPsql, Path: path, Timeout: 30 * time.Second,
		AllowedEnv: []string{passFileEnv},
	}
}

// PgDumpSpec is the allowlist entry for pg_dump.
//
// Its timeout is measured in hours because a dump's work is proportional to
// how much data a customer has; one that gave up after thirty seconds would
// work on every test database and on no real one.
func PgDumpSpec(path string) command.Spec {
	return command.Spec{
		Name: CommandPgDump, Path: path, Timeout: 6 * time.Hour,
		AllowedEnv: []string{passFileEnv},
	}
}
