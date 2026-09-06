package monitoring

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GrafanaRole is the PostgreSQL role Grafana connects as.
//
// Its own role, never the panel's. Grafana lets anyone with editor rights write
// SQL and runs it as whoever the datasource says: pointing it at the panel's
// role would make a Grafana editor an administrator of the panel's database.
const GrafanaRole = "jothost_grafana"

// grafanaTables are what the role may read. Two tables, named explicitly
// rather than granted on the schema, so a later table is not readable by
// Grafana because somebody added it.
var grafanaTables = []string{"system_metrics", "servers"}

// GrafanaCredentials are what the Agent needs to write the datasource.
type GrafanaCredentials struct {
	Host     string
	Port     int
	Database string
	User     string
	Password string
	SSLMode  string
}

// EnsureGrafanaRole creates or updates the read-only role and returns its
// credentials.
//
// The password is regenerated on every provision rather than stored. The panel
// has no reason to keep it: it is written into Grafana's datasource file and
// read from there by Grafana, and a copy in the panel's database would be one
// more credential to protect for no purpose. The cost is that provisioning
// twice invalidates the first password, which is correct — the second
// provision rewrites the file that held it.
func EnsureGrafanaRole(ctx context.Context, pool *pgxpool.Pool, databaseURL string) (GrafanaCredentials, error) {
	var creds GrafanaCredentials

	password, err := randomPassword()
	if err != nil {
		return creds, err
	}

	// The role name is a constant in this file and never comes from a caller,
	// which is why it can be in the statement text: PostgreSQL has no
	// parameter placeholder for an identifier, and the only safe version of
	// this is one where the identifier is not user input.
	//
	// The password is passed as a literal because CREATE ROLE has no
	// placeholder either — so it is quoted with PostgreSQL's own quoting
	// function rather than by string concatenation.
	statements := []string{
		fmt.Sprintf(`DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%s') THEN
        EXECUTE format('CREATE ROLE %%I LOGIN PASSWORD %%L', '%s', $password$%s$password$);
    ELSE
        EXECUTE format('ALTER ROLE %%I LOGIN PASSWORD %%L', '%s', $password$%s$password$);
    END IF;
END
$$;`, GrafanaRole, GrafanaRole, password, GrafanaRole, password),
	}

	for _, table := range grafanaTables {
		statements = append(statements,
			fmt.Sprintf(`GRANT SELECT ON TABLE %s TO %s`, table, GrafanaRole))
	}
	// No CREATE, no USAGE beyond what SELECT needs, and explicitly nothing on
	// anything else in the schema.
	statements = append(statements,
		fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, GrafanaRole))

	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			return creds, fmt.Errorf(
				"the panel could not create a read-only database role for Grafana, "+
					"and will not hand it the panel's own credentials instead: %w", err)
		}
	}

	creds, err = credentialsFrom(databaseURL)
	if err != nil {
		return creds, err
	}
	creds.User = GrafanaRole
	creds.Password = password
	return creds, nil
}

// credentialsFrom reads the host, port and database out of the panel's own
// connection string, so the datasource points at the same database without the
// operator typing it again.
//
// The panel's own user and password are deliberately not carried over: they are
// replaced by the caller, and reading them here would make it easy for a later
// change to pass them through by accident.
func credentialsFrom(databaseURL string) (GrafanaCredentials, error) {
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		return GrafanaCredentials{}, fmt.Errorf("read the database URL: %w", err)
	}

	creds := GrafanaCredentials{
		Host:     parsed.Hostname(),
		Port:     5432,
		Database: strings.TrimPrefix(parsed.Path, "/"),
		SSLMode:  parsed.Query().Get("sslmode"),
	}
	if port := parsed.Port(); port != "" {
		if _, err := fmt.Sscanf(port, "%d", &creds.Port); err != nil {
			return creds, fmt.Errorf("the database URL has a port that is not a number: %q", port)
		}
	}
	if creds.SSLMode == "" {
		creds.SSLMode = "disable"
	}
	if creds.Host == "" || creds.Database == "" {
		return creds, fmt.Errorf("the database URL names no host or no database")
	}
	return creds, nil
}

// randomPassword returns a password with enough entropy that it does not
// matter that nothing rate-limits PostgreSQL authentication.
func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a password: %w", err)
	}
	// URL-safe and unpadded: it travels through a connection string and a YAML
	// file, and a "+" or "/" in either is a character somebody has to think
	// about.
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
