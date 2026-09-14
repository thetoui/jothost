package database

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jothost/panel/agent/internal/command"
)

// Both PostgreSQL clients are handed PGPASSFILE by this package, and the
// allowlist must let them take it. pg_dump's entry once did not, and no
// password-authenticated dump could run; this runs each spec for real with the
// variable set, standing /bin/true in for the client.
func TestThePostgresClientsMayBeGivenTheirPasswordFile(t *testing.T) {
	const stand = "/bin/true"
	if _, err := os.Stat(stand); err != nil {
		t.Skip("no /bin/true on this host")
	}

	for _, spec := range []command.Spec{PsqlSpec(stand), PgDumpSpec(stand)} {
		runner, err := command.NewRunner(spec)
		if err != nil {
			t.Fatalf("%s: NewRunner: %v", spec.Name, err)
		}
		_, err = runner.RunWith(context.Background(), spec.Name,
			command.Options{Env: map[string]string{passFileEnv: "/nonexistent/pgpass"}})
		if errors.Is(err, command.ErrNotAllowed) {
			t.Errorf("%s refuses %s: %v", spec.Name, passFileEnv, err)
		} else if err != nil {
			t.Errorf("%s: %v", spec.Name, err)
		}
	}
}

// And nothing else: the password file is the only variable either may take.
func TestThePostgresClientsTakeNoOtherVariable(t *testing.T) {
	for _, spec := range []command.Spec{PsqlSpec("/usr/bin/psql"), PgDumpSpec("/usr/bin/pg_dump")} {
		if len(spec.AllowedEnv) != 1 || spec.AllowedEnv[0] != passFileEnv {
			t.Errorf("%s allows %v, want only %s", spec.Name, spec.AllowedEnv, passFileEnv)
		}
	}
}
