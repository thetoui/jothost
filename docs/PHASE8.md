# Phase 8 — Database Manager

What this phase adds, why it is shaped this way, and what the live checks
found that the unit tests could not.

---

## 1. What it does

The panel manages databases and their accounts on the database servers running
on the host: MariaDB, MySQL, and PostgreSQL.

A database is created, sized, and dropped. An account is created with a
generated password, given a level of access to a database, rotated, and
removed. The panel records what it did, and the Agent is the only thing that
touches a server.

---

## 2. Design decisions

### 2.1 Synchronous, not queued

Websites and certificates go through the job queue because provisioning a site
takes seconds and issuing a certificate can take a minute. Creating a database
is one DDL statement that finishes in milliseconds.

Putting it through the queue would add a poll cycle of latency to every
operation and — the deciding reason — leave nowhere to return the generated
password. The account's password exists in exactly one place at exactly one
moment: the response to the request that created it. A job whose result is
fetched later would have to persist it somewhere in between.

So a `201` from `POST /databases` means the database exists on the host. This
is a deliberate departure from the pattern the earlier phases set, and it is
the only one in the panel so far.

### 2.2 One provider for MySQL and MariaDB

The clients speak the same protocol and the statements the panel issues are
identical, so there is one provider. The engine it *reports* is whichever the
server said it was, because an operator needs to know which fork their data is
in — and the two disagree about enough (authentication plugins, JSON
functions, version numbering) that conflating them would eventually mislead
someone.

### 2.3 Three privilege levels, never a privilege string

`readonly`, `readwrite`, `full`. Nothing else is accepted.

A panel that forwards a privilege string is a panel that can be asked to grant
`SUPER` or `FILE`, either of which is server-wide and turns a website's account
into a way to read everything on the machine.

| Level | MySQL / MariaDB | PostgreSQL |
| --- | --- | --- |
| `readonly` | `SELECT` | `USAGE` on schema, `SELECT` on tables, and default privileges for tables added later |
| `readwrite` | `SELECT, INSERT, UPDATE, DELETE` | the above plus write, plus sequence usage, plus matching default privileges |
| `full` | `ALL PRIVILEGES` on that database only | `ALL` on the public schema and its contents, plus default privileges |

`full` on PostgreSQL includes `CREATE` on the schema. Without it an application
cannot run its own migrations, which is the most common way a hosting panel's
"full access" turns out not to be.

The `ALTER DEFAULT PRIVILEGES` half is not optional. Without it, every table the
application creates *after* the grant is invisible to the role that was granted
access — the cause of most "permission denied for table" complaints.

A grant **replaces** what the account held rather than adding to it, so lowering
someone from `full` to `readonly` actually takes `DROP` away.

### 2.4 Secrets never reach argv

The process table on a Linux host is world-readable. `mysql -p<password>`
publishes that password to every account on the machine for as long as the
command runs.

So: every statement, including the one that sets a password, is written to the
client's **standard input**. The Agent's own admin credentials reach the client
through a mode-0600 option file (`--defaults-extra-file`) or `PGPASSFILE`. The
only program in the Agent permitted an environment variable from a caller is
`psql`, and only `PGPASSFILE`.

### 2.5 The password alphabet excludes quotes

SQL has no parameter form for `CREATE USER ... IDENTIFIED BY`, so a password is
necessarily concatenated into a statement inside a string literal. Escaping is
applied, but the generated alphabet also simply excludes `'`, `"`, `` ` `` and
`\`, so a password that could close that literal cannot exist in the first
place. 24 characters from that alphabet is about 149 bits.

### 2.6 Identifiers are the injection boundary

There is no parameter form for an identifier either: `CREATE DATABASE $1` is
not valid on any engine. So names are constrained to `^[a-z][a-z0-9_]*$` in
`shared/validate`, refused if reserved, and **also** quoted at the point of use.
The same rule is repeated as a CHECK constraint in migration 0008, because the
database is the last place a name can be constrained.

### 2.7 The panel stores passwords, because the servers do not

Both engines keep only a hash. A password not captured at creation can never be
shown to the person who has to put it in a configuration file.

They are stored AES-256-GCM encrypted, bound to their own row id as additional
authenticated data, so a ciphertext copied from another account fails to
decrypt rather than handing out that account's password.

Reading one back is a separate, audited endpoint — never a field on a listing —
because it is the one request in the panel that returns a working credential,
and "who read this, and when" has to stay answerable.

---

## 3. What the live checks found

Each of these passed unit tests and failed against a real server. They are
recorded because the pattern is the point: none of them could have been found
without driving actual database servers.

**1. `psql --no-align` separates fields with `|`, not tabs.** Every multi-column
PostgreSQL result parsed as a single field, so the database listing came back
empty while both databases existed. Fixed with an explicit
`--field-separator`.

**2. libpq matches a Unix-socket connection against `localhost`, not the socket
directory.** The pgpass file named `/run/postgresql`, matched nothing, and psql
failed with "no password supplied" — reported by the panel as "PostgreSQL is
not installed".

**3. A MySQL option file treats `#` and `;` as comment starts.** Both are in the
panel's own password alphabet, so roughly one generated admin password in five
would have been silently truncated, authenticating with a prefix. Values are
now quoted.

**4. `REVOKE ALL PRIVILEGES, GRANT OPTION ON db.*` is not valid syntax** — that
combined form takes no `ON` clause. Lowering an account from `full` to
`readonly` therefore did nothing at all while the panel reported success. This
was the worst of them: the panel was telling an operator about a restriction
the server was not applying.

**5. The revoke's error was being logged and discarded**, which is why (4) went
unnoticed. Only "there is no such grant" — the expected answer the first time —
is tolerated now; anything else fails the operation.

**6. The mariadb client echoes each statement to stderr between rows of
dashes.** Taking the first line reported `--------------` as the reason a
statement failed: useless to a user, and invisible to the code matching on it,
which is what hid (4). The echoed statement must also never become the error
text, because the statement that sets a password contains that password.

**7. `CREATE USER IF NOT EXISTS` leaves an existing account's password alone.**
The Agent was returning the freshly generated password as though it had been
applied, handing the operator a credential that could not work and failed only
much later. The Agent now reports whether it created the account; the panel
refuses to record a password it did not set.

**8. PostgreSQL grants `CONNECT` to `PUBLIC` on every new database**, and
`PUBLIC` is every role on the server. One customer's role could connect to
another customer's database the moment it was created. The Agent now revokes
that on databases it creates — the server's own databases are left exactly as
the operator's installation made them.

Three more were bugs in the integration script itself, kept here because each
made a passing check meaningless: a greedy `sed` that read the *last* `"id"` in
a response rather than the first; a `not_contains` check whose needle was empty,
which matches everything; and a shell function assigning `user_id` without a
prefix, silently overwriting its caller's variable.

---

## 4. The development environment

The agent container is the managed host, so it now runs **MariaDB and
PostgreSQL** alongside nginx and PHP. Both are installed rather than one,
because the providers differ in exactly the places most likely to be wrong —
PostgreSQL has no `CREATE DATABASE IF NOT EXISTS`, no user/host pairs, and
per-schema rather than per-database privileges — and a provider only ever
exercised against a mock is a provider nobody has run.

Neither listens on a TCP port. Both are reached over their Unix sockets, by the
Agent, on the same host.

PostgreSQL is initialised with `scram-sha-256` for local connections rather
than `trust`. `trust` would be simpler, but it would make every password the
panel sets meaningless — any role could connect as any other without one, so
"the account can connect with its password" would pass no matter what the
password was. It is also what makes the Agent's pgpass path get exercised.

This PostgreSQL is **not** the panel's own. The control-plane database is a
separate container, and dropping a database on the managed host cannot reach
the panel's own tables.

Both data directories are named volumes, for the same reason `/var/www` is:
rebuilding the image would otherwise destroy every database while the panel
went on listing them as active.

---

## 5. Verification

```bash
make docker-test-databases
```

51 checks against the running stack, from inside the agent container so it can
compare the panel's claims against the servers themselves. The suite is
idempotent — it cleans up its own leftovers, including accounts, on entry.

What it actually verifies, beyond status codes:

- a database the panel says it created exists on the server, with `utf8mb4`
- the account connects **with the password the panel returned**
- `full` can create tables; `readwrite` can write but not change the schema;
  `readonly` can read but not write — checked by doing each
- lowering a grant removes the privileges, and raising it restores them
- rotation: the new password works, the old one stops, and the stored copy is
  the one that works
- no listing endpoint ever carries a password
- a role cannot connect to another tenant's database
- injection attempts, reserved names, and unoffered privileges are refused, and
  nothing is created by any of them
- deletion removes the database from the server, not just from the panel

---

## 6. Known limitations

- **No import or export.** Uploading a dump or downloading a backup is not
  here; Phase 14 covers backups.
- **No phpMyAdmin-style browser.** The panel manages databases and access, not
  their contents.
- **Grants are not reconciled.** The panel records the level it applied. A grant
  changed by hand on the server afterwards is not detected, and the panel will
  keep showing what it set.
- **No quotas.** A database can grow until the disk is full.
- **`ON DELETE SET NULL` for websites is one-way.** A database whose website was
  deleted becomes unlinked and cannot be relinked through the panel.
- **MySQL host patterns are limited to `localhost` and `%`.** A specific address
  is a legitimate thing to want, and is not offered — deliberately, since a
  free-text host field is how a typo becomes a database exposed to a subnet.
