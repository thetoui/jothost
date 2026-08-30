# JotHost Panel — Database Design

Database:

```text
PostgreSQL
```

---

# 1. users

```sql
id UUID PRIMARY KEY
username VARCHAR(100) UNIQUE NOT NULL
email VARCHAR(255) UNIQUE
password_hash TEXT NOT NULL
status VARCHAR(30) NOT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
last_login_at TIMESTAMPTZ
```

Constraints applied in migration 0001:

```text
status IN ('active', 'disabled', 'locked')
username matches ^[a-z0-9][a-z0-9_.-]{2,99}$
```

`password_hash` holds an Argon2id PHC string. `email` is nullable, and an
absent address is stored as NULL so several accounts may omit one.

---

# 2. roles

```sql
id UUID PRIMARY KEY
name VARCHAR(50) UNIQUE NOT NULL
description TEXT
created_at TIMESTAMPTZ NOT NULL
```

---

# 3. user_roles

```sql
user_id UUID REFERENCES users(id)
role_id UUID REFERENCES roles(id)

PRIMARY KEY(user_id, role_id)
```

---

# 4. permissions

```sql
id UUID PRIMARY KEY
name VARCHAR(100) UNIQUE NOT NULL
description TEXT
```

Examples:

```text
server.view
server.manage
website.view
website.create
website.update
website.delete
database.manage
ssl.manage
firewall.manage
backup.manage
user.manage
```

---

# 5. role_permissions

```sql
role_id UUID REFERENCES roles(id)
permission_id UUID REFERENCES permissions(id)

PRIMARY KEY(role_id, permission_id)
```

---

# 6. sessions

```sql
id UUID PRIMARY KEY
user_id UUID REFERENCES users(id)
token_hash TEXT NOT NULL
ip_address INET
user_agent TEXT
expires_at TIMESTAMPTZ NOT NULL
created_at TIMESTAMPTZ NOT NULL
revoked_at TIMESTAMPTZ
```

---

# 6.1 session_token_history

Added in Phase 1 to make refresh-token reuse detectable.

```sql
token_hash TEXT PRIMARY KEY
session_id UUID REFERENCES sessions(id) ON DELETE CASCADE
rotated_at TIMESTAMPTZ NOT NULL
```

Rotation overwrites `sessions.token_hash`, which would leave a replayed older
token with no row to match and no way to distinguish theft from an invalid
token. Retired hashes are recorded here instead, so presenting one is
unambiguous evidence the token leaked. See docs/PHASE1.md section 3.2.

---

# 7. two_factor_auth

```sql
id UUID PRIMARY KEY
user_id UUID UNIQUE REFERENCES users(id)
secret_encrypted TEXT NOT NULL
enabled BOOLEAN NOT NULL DEFAULT FALSE
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

---

# 8. servers

```sql
id UUID PRIMARY KEY
hostname VARCHAR(255) NOT NULL
os_name VARCHAR(100)
os_version VARCHAR(100)
kernel VARCHAR(255)
architecture VARCHAR(50)
ipv4 INET
ipv6 INET
status VARCHAR(30)
agent_version VARCHAR(50)
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

Constraints applied in migration 0004:

```text
status IN ('online', 'offline', 'unknown')
hostname UNIQUE
```

The unique hostname is what makes registration an idempotent upsert: the API
refreshes the existing row at startup rather than adding one per restart.
Facts the Agent could not determine are stored as NULL, so "unknown" and
"empty" stay distinguishable.

---

# 9. websites

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
name VARCHAR(255)
primary_domain VARCHAR(255) UNIQUE NOT NULL
document_root TEXT NOT NULL
system_username VARCHAR(100) NOT NULL
php_version VARCHAR(20)
status VARCHAR(30) NOT NULL
ssl_enabled BOOLEAN DEFAULT FALSE
https_redirect BOOLEAN DEFAULT FALSE
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

**Extended in migration 0010 (Phase 4.1).** A subdomain is a website row with
a parent, not a separate table:

```sql
parent_website_id UUID REFERENCES websites(id) ON DELETE CASCADE
document_root_mode VARCHAR(10)
php_pool_mode VARCHAR(10)
system_user_mode VARCHAR(10)

CHECK ((parent_website_id IS NULL) = (document_root_mode IS NULL)
   AND (parent_website_id IS NULL) = (php_pool_mode IS NULL)
   AND (parent_website_id IS NULL) = (system_user_mode IS NULL))
CHECK (document_root_mode IS NULL OR document_root_mode IN ('nested', 'isolated'))
CHECK (php_pool_mode IS NULL OR php_pool_mode IN ('inherit', 'dedicated'))
CHECK (system_user_mode IS NULL OR system_user_mode IN ('inherit', 'dedicated'))
CHECK (parent_website_id IS NULL OR parent_website_id <> id)
CHECK (system_user_mode IS DISTINCT FROM 'dedicated' OR php_pool_mode = 'dedicated')
```

A subdomain needs a document root, a system account, a PHP version, a
certificate, logs, a file manager, a database, possibly a Node application —
which is the list of things a website has. A separate table would duplicate
every one of them, and every feature already built would have to learn that a
site is sometimes one thing and sometimes another. See docs/PHASE4.1.md §2.

`ON DELETE CASCADE` here, unlike a database's `SET NULL`: a subdomain cannot
outlive its parent. It is a backstop rather than the path taken — the API
refuses to delete a website that still has subdomains, because the rows are not
the sites and nothing would be queued to remove their vhosts.

The last check is the one worth reading twice. A dedicated account with an
inherited pool would run PHP as the parent's user over files owned by the
subdomain's: every write fails, and it reads as a broken application rather
than a bad configuration.

The three modes are non-null exactly when there is a parent. A mode on a
top-level row would be a value with no meaning that some later query would read
anyway.

**Shared accounts.** `websites_system_user_idx` became partial:

```sql
CREATE UNIQUE INDEX websites_system_user_idx ON websites (system_username)
    WHERE system_user_mode IS DISTINCT FROM 'inherit';
```

A subdomain that inherits its parent's account shares that account's name. The
index still does its original job — two *independent* sites sharing an account
would let a compromised one read the other's files — by applying to the rows
that own their account; an inheriting row is a copy of a parent row that is
itself unique.

**Wildcard names.** `websites_domain_format` accepts a leading `*.` label, and
only on a subdomain row: on a top-level site it would be a site whose own
identity matches nothing. `domains_format` accepts it too, since a subdomain
carries a primary domain row like any other website.

**Nesting depth.** A subdomain's parent must be a top-level site, enforced by
the `websites_no_nested_subdomains` trigger. This cannot be a CHECK constraint:
the rule is about another row, and a CHECK may not run a subquery. A name
several levels down is still reachable — the label may contain dots, so
`dev.shop.example.com` is one subdomain of `example.com`.

**Note on `system_username`.** This field was specified as `system_user`.
PostgreSQL 16 made `SYSTEM_USER` a reserved keyword (SQL:2023), so that name is
a syntax error unquoted and, in some contexts, silently resolves to the
built-in function instead of failing. The column is therefore
`system_username`, aliased back to `system_user` in every query — the API
contract and the JSON field keep the specified name. Implemented in migration
`0005_websites_and_jobs`; see docs/PHASE4.md §3.1.

---

# 10. domains

```sql
id UUID PRIMARY KEY
website_id UUID REFERENCES websites(id)
domain VARCHAR(255) UNIQUE NOT NULL
type VARCHAR(30) NOT NULL
status VARCHAR(30) NOT NULL
created_at TIMESTAMPTZ NOT NULL
```

Types:

```text
primary
alias
subdomain
redirect
```

A `subdomain` row here is **not** a subdomain site. It is an extra name served
by *this* site whose hostname happens to sit beneath its domain — an alias by
another name. A subdomain in the Phase 4.1 sense is its own website row with
its own vhost and document root (table 9).

The two cannot collide: `domain` is unique across the whole table and every
website — subdomains included — writes its primary name into it, so a name can
be an alias of one site or the identity of another, never both. Two server
blocks answering to one name is a configuration nginx resolves by picking one,
which is not a decision the panel should leave to it.

---

# 11. php_versions

```sql
id UUID PRIMARY KEY
version VARCHAR(20) UNIQUE NOT NULL
binary_path TEXT
fpm_service VARCHAR(255)
status VARCHAR(30)
installed BOOLEAN DEFAULT FALSE
created_at TIMESTAMPTZ NOT NULL
```

---

# 12. php_pools

```sql
id UUID PRIMARY KEY
website_id UUID UNIQUE REFERENCES websites(id)
php_version VARCHAR(20) NOT NULL
pool_name VARCHAR(100) NOT NULL
socket_path TEXT NOT NULL
memory_limit VARCHAR(30)
max_children INTEGER
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

**As implemented in Phase 5.** Migration `0006_php` adds three columns this
sketch omits — `upload_max_filesize`, `max_execution_time`, and
`opcache_enabled` — so the settings API_SPEC section 9 documents have somewhere
to live. They are columns rather than a JSON blob so the CHECK constraints
apply: these values are written into a configuration file, and the database is
the last place they can be constrained.

`website_id` is UNIQUE, which is load-bearing: two pools for one site would
race for the same socket path, and whichever FPM started last would win
silently. `pool_name` is unique across the host for the same reason.

`socket_path` includes the PHP version. With one path per site, switching a
site's version fails because the new FPM refuses to start while the old one
still listens there; see docs/PHASE5.md section 3.1.

---

# 13. node_versions

```sql
id UUID PRIMARY KEY
version VARCHAR(30) UNIQUE NOT NULL
binary_path TEXT
installed BOOLEAN DEFAULT FALSE
status VARCHAR(30)
created_at TIMESTAMPTZ NOT NULL
```

---

# 14. node_apps

**As implemented in migration 0009.**

```sql
id UUID PRIMARY KEY
server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE
website_id UUID NOT NULL REFERENCES websites(id) ON DELETE CASCADE
name VARCHAR(40) NOT NULL
node_version VARCHAR(30) NOT NULL
application_root TEXT NOT NULL
startup_file TEXT NOT NULL
port INTEGER NOT NULL
status VARCHAR(30) NOT NULL DEFAULT 'stopped'
systemd_service VARCHAR(255)
autostart BOOLEAN NOT NULL DEFAULT TRUE
last_error TEXT
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL

UNIQUE(website_id)
UNIQUE(server_id, port)
UNIQUE(server_id, name)
CHECK (name ~ '^[a-z][a-z0-9_-]{0,39}$')
CHECK (status IN ('stopped', 'starting', 'running', 'failed'))
CHECK (application_root LIKE '/%' AND application_root NOT LIKE '%..%')
CHECK (startup_file NOT LIKE '/%' AND startup_file NOT LIKE '%..%')
CHECK (port BETWEEN 1024 AND 32767)
```

`website_id` is `ON DELETE CASCADE`, unlike a database's. An application is the
website's own process, not data that outlives it: deleting the site removes the
vhost that reached it, so keeping the record would leave a process nothing
could route to. It is unique, because a second application on one site would
need a second vhost to reach it and the site has one domain.

The port is unique **per server**, not per website. A port is a host-wide
resource, and two applications on one of them means one is failing to bind
while the panel cannot say which.

It is bounded at 1024–32767 for two reasons that are easy to conflate. Below
1024 needs root, which an application never has. From 32768 up is the range the
kernel hands to outgoing connections, so an application asked to listen there
starts fine most days and fails with "address already in use" on the day
something else got there first — a fault that looks random and is not.

`name` is bounded at 40 and constrained to the same character set as
`shared/validate.AppName`, because this value becomes a systemd unit name and
the database is the last place it can be constrained.

`startup_file` must be relative and free of traversal. An absolute path would
let an application be started from outside its own directory, which is the one
thing the application root exists to prevent.

`autostart` is separate from `status` on purpose. "It is stopped" and "it is
meant to be stopped" are different facts, and conflating them is how a crashed
application looks deliberate.

---

# 15. node_environment

**As implemented in migration 0009.**

```sql
id UUID PRIMARY KEY
node_app_id UUID NOT NULL REFERENCES node_apps(id) ON DELETE CASCADE
key VARCHAR(64) NOT NULL
value_encrypted TEXT NOT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL

UNIQUE(node_app_id, key)
CHECK (key ~ '^[A-Z_][A-Z0-9_]{0,63}$')
CHECK (key NOT IN ('LD_PRELOAD', 'LD_LIBRARY_PATH', 'LD_AUDIT', 'NODE_OPTIONS',
                   'PATH', 'IFS', 'SHELL', 'BASH_ENV', 'ENV',
                   'PORT', 'HOME', 'USER', 'PWD'))
```

Values are AES-256-GCM encrypted and bound to their own row's id (section 30),
so a ciphertext copied from another application's row fails to decrypt rather
than revealing its secret. This is where a database URL with a password in it
goes, and an API key, and a signing secret — a panel that stored them in plain
text would make its own database the most valuable thing on the host.

The reserved-key check refuses the names that change *what runs* rather than
how it behaves — `LD_PRELOAD`, `NODE_OPTIONS`, `PATH`, `BASH_ENV` — together
with the names the panel sets itself, so one value cannot contradict another.
`shared/validate.EnvKey` refuses them first; this is the last line, for a row
written by some future path that forgot.

---

# 16. databases

**As implemented in migration 0008.**

```sql
id UUID PRIMARY KEY
server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE
website_id UUID REFERENCES websites(id) ON DELETE SET NULL
name VARCHAR(63) NOT NULL
engine VARCHAR(30) NOT NULL
status VARCHAR(30) NOT NULL DEFAULT 'creating'
charset VARCHAR(64)
collation_name VARCHAR(64)
size_bytes BIGINT
size_checked_at TIMESTAMPTZ
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL

UNIQUE(server_id, engine, name)
CHECK (engine IN ('mysql', 'mariadb', 'postgres'))
CHECK (status IN ('creating', 'active', 'deleting', 'failed'))
CHECK (name ~ '^[a-z][a-z0-9_]{0,62}$')
```

`website_id` is `ON DELETE SET NULL`, not `CASCADE`. Deleting a website must
never silently delete a database: the data outlives the vhost, and dropping it
is a separate decision an operator has to make deliberately.

The name is bounded at 63 — PostgreSQL's limit, and below MySQL's 64 — and
constrained to the same character set as `shared/validate.DatabaseName`. The
constraint is repeated here because the database is the last place a name can
be constrained: a row written by some future code path that skipped the
validator still cannot hold a name that would need escaping in a statement.

`collation_name` rather than `collation`: the bare word is reserved in
PostgreSQL and would need quoting at every use, which works until someone
writes one query without the quotes.

`size_bytes` is nullable on purpose. "Never measured" and "empty" are different
facts, and only one of them is worth an operator's attention.

---

# 17. database_users

**As implemented in migration 0008.**

```sql
id UUID PRIMARY KEY
server_id UUID NOT NULL REFERENCES servers(id) ON DELETE CASCADE
engine VARCHAR(30) NOT NULL
username VARCHAR(32) NOT NULL
host VARCHAR(64) NOT NULL DEFAULT ''
password_encrypted TEXT NOT NULL
password_updated_at TIMESTAMPTZ NOT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL

UNIQUE(server_id, engine, username, host)
CHECK (username ~ '^[a-z][a-z0-9_]{0,31}$')
CHECK (host IN ('', 'localhost', '%'))
CHECK ((engine = 'postgres' AND host = '') OR (engine <> 'postgres' AND host <> ''))
```

Accounts belong to a **server**, not to a database. One account routinely holds
grants on several, which is what `database_permissions` records.

`host` exists because MySQL identifies an account by user and host together.
PostgreSQL roles are global, so it is empty there — and the last CHECK enforces
that, rather than letting a row imply a restriction the server does not apply.

`password_encrypted` is AES-256-GCM, bound to the row's own id as additional
authenticated data (section 30), so a ciphertext moved from another row fails to
decrypt instead of revealing that account's password. The panel stores it
because the servers do not: both keep only a hash, so a password not captured
at creation could never be shown again.

---

# 18. database_permissions

**As implemented in migration 0008.**

```sql
id UUID PRIMARY KEY
database_user_id UUID NOT NULL REFERENCES database_users(id) ON DELETE CASCADE
database_id UUID NOT NULL REFERENCES databases(id) ON DELETE CASCADE
privilege VARCHAR(20) NOT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL

UNIQUE(database_user_id, database_id)
CHECK (privilege IN ('readonly', 'readwrite', 'full'))
```

A privilege **level**, not a JSON blob of individual privileges. The two engines
express the same intent differently — "readwrite" is four MySQL privileges and,
on PostgreSQL, a schema grant plus default privileges — so recording the level
the operator chose is what lets the panel say what it meant and reapply it
identically on either server. Storing a raw privilege list would also mean
accepting one, and a panel that accepts a privilege list can be asked for
`SUPER`.

The UNIQUE pair matters: two rows would make "what access does this account
have" a question with two answers.

---

# 19. ssl_certificates

```sql
id UUID PRIMARY KEY
website_id UUID REFERENCES websites(id)
provider VARCHAR(50) NOT NULL
domains JSONB NOT NULL
certificate_path TEXT
private_key_path TEXT
issued_at TIMESTAMPTZ
expires_at TIMESTAMPTZ
auto_renew BOOLEAN DEFAULT TRUE
status VARCHAR(30)
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

**As implemented in Phase 6.** Migration `0007_ssl` adds three columns this
sketch omits: `fingerprint` and `issuer`, read back from the certificate itself
so a renewal that produced the same file can be told from one that replaced it,
and `last_renewal_attempt`, without which a failing certificate is retried on
every sweep — which for Let's Encrypt means walking into a rate limit.

`website_id` is UNIQUE. Two certificates for one site would make the vhost's
`ssl_certificate` directive ambiguous, and nginx would silently use whichever
was written last.

`status` is constrained to the full lifecycle rather than a boolean: a
certificate is issuing, valid, expiring, expired, revoked, or failed, and
conflating any of those with valid is how a panel reports HTTPS working the day
after it stopped.

---

# 20. dns_records

```sql
id UUID PRIMARY KEY
domain VARCHAR(255) NOT NULL
type VARCHAR(20) NOT NULL
name VARCHAR(255) NOT NULL
value TEXT NOT NULL
ttl INTEGER
priority INTEGER
provider VARCHAR(50)
external_id VARCHAR(255)
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

---

# 21. cron_jobs

```sql
id UUID PRIMARY KEY
website_id UUID REFERENCES websites(id)
schedule VARCHAR(100) NOT NULL
command TEXT NOT NULL
enabled BOOLEAN DEFAULT TRUE
last_run_at TIMESTAMPTZ
last_status VARCHAR(30)
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

---

# 22. backups

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
website_id UUID REFERENCES websites(id)
type VARCHAR(30) NOT NULL
destination VARCHAR(255)
path TEXT
size_bytes BIGINT
status VARCHAR(30)
started_at TIMESTAMPTZ
completed_at TIMESTAMPTZ
created_at TIMESTAMPTZ NOT NULL
```

---

# 23. backup_schedules

```sql
id UUID PRIMARY KEY
website_id UUID REFERENCES websites(id)
schedule VARCHAR(100) NOT NULL
retention_days INTEGER NOT NULL
enabled BOOLEAN DEFAULT TRUE
destination_type VARCHAR(30)
destination_config_encrypted TEXT
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

---

# 24. jobs

```sql
id UUID PRIMARY KEY
type VARCHAR(100) NOT NULL
status VARCHAR(30) NOT NULL
payload JSONB
result JSONB
error TEXT
progress INTEGER DEFAULT 0
created_by UUID REFERENCES users(id)
created_at TIMESTAMPTZ NOT NULL
started_at TIMESTAMPTZ
completed_at TIMESTAMPTZ
```

---

# 25. audit_logs

```sql
id UUID PRIMARY KEY
user_id UUID REFERENCES users(id)
action VARCHAR(100) NOT NULL
resource_type VARCHAR(100)
resource_id UUID
ip_address INET
user_agent TEXT
status VARCHAR(30)
metadata JSONB
created_at TIMESTAMPTZ NOT NULL
```

Audit logs should be append-only.

Migration 0001 enforces this with `BEFORE UPDATE` and `BEFORE DELETE` triggers
that raise an exception, so history cannot be rewritten through the API or by
anything else holding a database connection.

---

# 26. system_metrics

```sql
id BIGSERIAL PRIMARY KEY
server_id UUID REFERENCES servers(id)
timestamp TIMESTAMPTZ NOT NULL
cpu_percent NUMERIC
memory_percent NUMERIC
disk_percent NUMERIC
load_1 NUMERIC
load_5 NUMERIC
load_15 NUMERIC
network_rx BIGINT
network_tx BIGINT
```

Use indexes and retention policies.

Migration 0004 adds a composite `(server_id, timestamp DESC)` index for history
queries and a `timestamp` index for retention pruning, and cascades deletes
from `servers` so metrics cannot outlive the host they describe.

`network_rx` and `network_tx` store the kernel's **cumulative** counters rather
than a rate. A stored rate would be locked to the sampling interval it was
taken at; the counter lets a rate be derived over any window, which is what
keeps the 1h and 30d graphs consistent. See docs/PHASE3.md section 3.2.

Every metric column is nullable. A partially available reading is normal — a
host may answer for memory while its disk probe times out — and recording a
zero would be indistinguishable from a genuinely idle machine.

---

# 27. security_findings

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
severity VARCHAR(20) NOT NULL
category VARCHAR(100) NOT NULL
title VARCHAR(255) NOT NULL
description TEXT
status VARCHAR(30)
metadata JSONB
created_at TIMESTAMPTZ NOT NULL
resolved_at TIMESTAMPTZ
```

---

# 28. firewall_rules

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
action VARCHAR(20) NOT NULL
protocol VARCHAR(20)
port_start INTEGER
port_end INTEGER
source_cidr CIDR
description TEXT
enabled BOOLEAN DEFAULT TRUE
created_at TIMESTAMPTZ NOT NULL
```

---

# 28.1 schema_migrations

Created and maintained by the migration runner
(`api/internal/db/migrate`), not by a migration file.

```sql
version    INTEGER PRIMARY KEY
name       TEXT NOT NULL
applied_at TIMESTAMPTZ NOT NULL
```

---

# 29. indexes

Important indexes:

```text
users.username
users.email
websites.primary_domain
domains.domain
jobs.status
jobs.created_at
audit_logs.created_at
audit_logs.user_id
ssl_certificates.expires_at
system_metrics.server_id + timestamp
security_findings.server_id
```

---

# 30. Encryption

Sensitive database values must be encrypted:

```text
Cloudflare tokens
Database passwords
Node environment secrets
2FA secrets
Backup credentials
S3 credentials
```

Passwords must use one-way password hashing.

Use Argon2id or bcrypt as appropriate.