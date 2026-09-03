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

**Extended in migration 0011 (Phase 4.5):**

```sql
webserver_mode VARCHAR(20) NOT NULL DEFAULT 'nginx'

CHECK (webserver_mode IN ('nginx', 'hybrid'))
```

Which web server arrangement this host runs. It is a property of the host and
not of a site: both servers are one process tree serving every site on the
machine, so a panel that let one site opt in would be running Apache for that
site and charging every other site the memory. Changing it rewrites every
site's configuration.
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

**Extended in migration 0011 (Phase 4.5).** The hybrid arrangement:

```sql
apache_port INTEGER
allow_override BOOLEAN NOT NULL DEFAULT TRUE

CHECK (apache_port IS NULL OR apache_port BETWEEN 7080 AND 7979)
UNIQUE(server_id, apache_port) WHERE apache_port IS NOT NULL
```

`apache_port` is the loopback port Apache serves this site on. It is **kept**
when the host goes back to nginx alone rather than cleared: the number is this
site's for as long as it exists, so switching arrangement twice does not
renumber every backend and rewrite every configuration file for no reason.

Unique per server, because a backend port is a host-wide resource: two sites on
one port means one Apache virtual host silently serving the other's traffic —
the first `VirtualHost` on a port answers for every name that matches no other.

The range overlaps `node_apps.port` (1024-32767), and two tables cannot be
constrained against each other without a trigger on both. The check is in Go,
in both directions, and names what holds a port when it refuses. See
docs/PHASE4.5.md section 2.6.

`allow_override` is whether Apache reads `.htaccess` for this site. It defaults
to on, because `.htaccess` is what hybrid mode is turned on for; it is per-site
because reading the file in every directory of every request is not free.

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

Implemented by Phase 13 (migration 0014), with three additions and one
substitution, all deliberate.

```sql
id UUID PRIMARY KEY
zone_id UUID REFERENCES dns_zones(id)
name VARCHAR(253) NOT NULL DEFAULT '@'
type VARCHAR(10) NOT NULL
ttl INTEGER NOT NULL DEFAULT 0
value TEXT NOT NULL
priority INTEGER NOT NULL DEFAULT 0
weight INTEGER NOT NULL DEFAULT 0
port INTEGER NOT NULL DEFAULT 0
flags INTEGER NOT NULL DEFAULT 0
tag VARCHAR(20) NOT NULL DEFAULT ''
provider VARCHAR(50) NOT NULL DEFAULT 'local'
external_id VARCHAR(255) NOT NULL DEFAULT ''
managed BOOLEAN NOT NULL DEFAULT FALSE
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

`zone_id` replaces `domain`. A record belongs to a zone rather than to a name: a
reverse zone is not a domain anybody owns, and a record whose zone was
identified by a string could be written into a zone that does not exist.

`weight`, `port`, `flags` and `tag` are the addition. An SRV record's weight,
port and target in a single text column can only be written to a zone file by
parsing operator text at the moment of writing — which is the one thing this
panel avoids everywhere else. As columns they are checked as numbers, by the
database as well as by Go.

`managed` marks a record the panel maintains for itself: the address record a
subdomain needs in its parent's zone. It is shown and not editable, because
editing one by hand would leave the panel and the zone disagreeing about a name
the panel is responsible for.

`name` is stored **relative** to its zone, with `@` for the apex. That is what a
zone file holds, and an absolute name in the owner column is how a record ends
up in a zone it was not meant for.

A unique index covers (zone, name, type, value, priority, weight, port, tag).
named loads a duplicate and serves it once, so a second row would be invisible
except as a row nobody can account for.

---

# 20.1 dns_zones

Added by Phase 13 (migration 0014).

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
website_id UUID REFERENCES websites(id) ON DELETE SET NULL
name VARCHAR(253) NOT NULL
kind VARCHAR(10) NOT NULL DEFAULT 'master'
reverse_network CIDR
primary_ns VARCHAR(253) NOT NULL
hostmaster VARCHAR(253) NOT NULL
serial BIGINT NOT NULL
refresh, retry, expire, minimum, ttl INTEGER NOT NULL
nameservers TEXT[] NOT NULL
dnssec BOOLEAN NOT NULL DEFAULT FALSE
allow_transfer, also_notify, masters TEXT[] NOT NULL
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

`website_id` is `ON DELETE SET NULL`, not cascade. Deleting a website must not
delete its zone: the names in it may point at other hosts, mail included, and a
panel that silently unpublished a customer's MX records because a vhost was
removed would take their mail down with the site.

`serial` is BIGINT because the wire format's field is unsigned 32-bit and
PostgreSQL's INTEGER is signed. It advances to the greater of "one more than
this" and the current unix time — monotonic, meaningful to a human reading a
`dig` output, and it cannot run out within a day the way YYYYMMDDnn does at the
hundredth edit.

It is **not** the serial the server is answering with. With inline signing named
keeps a second serial on the signed copy and it runs ahead; that one is read
from the host and never stored, because storing it would invite a comparison
that reports every signed zone as drifting.

There are no DNSSEC keys here, and there will not be. They are named's, in a
directory on the host; a private key in this table would be a private key in
every backup of it.

The CHECK constraints mirror the rules enforced in Go: a zone name is a domain
name, a secondary names at least one primary, and the SOA timers are in range
*and* coherent with each other — a retry longer than the refresh is a zone that
heals more slowly the more it breaks, and each value is in range on its own.

---

# 20.2 dns_settings

One row per host: the addresses named answers on, the default transfer list, the
`dnssec-policy` name, and the defaults a new zone starts from — name servers,
TTL and hostmaster. The name servers are here rather than on each zone because
they are the same for every zone a host serves, and asking once is the
difference between setting up DNS and setting it up repeatedly.

`dnssec_policy` is a policy *name*, not a set of key knobs. Inventing a key
policy is how a zone becomes unresolvable for the length of its longest TTL, and
BIND's own default is a single ECDSA key it rolls on its own schedule.

---

# 20.3 dns_providers and dns_zone_providers

Credentials for a DNS service somewhere else, and which zones are pushed to it.

The API token is encrypted with the panel's `ENCRYPTION_KEY` against the
provider row's own id — section 30's arrangement, for exactly this case. It
cannot be hashed the way a password is, because it has to be sent to the
provider on every call, and it is a credential that can rewrite every DNS record
in somebody's account. No endpoint returns it.

`last_sync_at`, `last_sync_status` and `last_sync_error` are stored so that a
provider which has been failing quietly for a month is visible.

---

# 20.4 update_checks, update_runs and update_settings

Added by Phase 21 (migration 0015).

`update_checks` is a **cache**, which is unusual here: everywhere else in this
panel the rows are intent and the host is made to match. The host's package
manager is the authority on what is outstanding, and these rows are what it last
said — kept because a check refreshes the package index and reaches the network.

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
manager VARCHAR(20) NOT NULL
succeeded BOOLEAN NOT NULL DEFAULT FALSE
reason TEXT NOT NULL DEFAULT ''
security_known BOOLEAN NOT NULL DEFAULT FALSE
package_count, security_count, held_count INTEGER NOT NULL
unavailable_repositories, stale_repositories INTEGER NOT NULL
reboot_required BOOLEAN NOT NULL DEFAULT FALSE
packages JSONB NOT NULL DEFAULT '[]'
held JSONB NOT NULL DEFAULT '[]'
checked_at TIMESTAMPTZ NOT NULL
```

`succeeded` is why the table has this shape. A check that could not reach the
repositories produces an empty package list that is indistinguishable from a
host with nothing to do — and "up to date" is what an operator reads to decide
they are safe. A row with `succeeded` false is *not known*, and the page says so
rather than showing zero.

`packages` and `held` are JSONB rather than child tables: this is a snapshot of
somebody else's data, replaced wholesale at the next check, never queried by
package and never joined to. A child table would be a delete-and-insert of a few
hundred rows every time, for a list only ever read whole.

`update_runs` is one application of updates, and it is this phase's answer to
"rollback". Neither apk nor apt keeps the package it replaced, so what the panel
can do is record exactly what moved, from which version to which:

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
trigger VARCHAR(20) NOT NULL      -- manual, scheduled, revert
status VARCHAR(20) NOT NULL       -- running, succeeded, failed
requested TEXT[] NOT NULL
changes JSONB NOT NULL
output, error TEXT NOT NULL
reboot_required BOOLEAN NOT NULL
started_at TIMESTAMPTZ NOT NULL
finished_at TIMESTAMPTZ
requested_by UUID REFERENCES users(id) ON DELETE SET NULL
```

The row is written **before** the work rather than after, so a panel restarted
mid-upgrade leaves a run stuck in `running` — which is a true and useful thing
to see. A row written only on success would leave no trace of the upgrade that
took the machine down.

`changes` is routinely longer than `requested`: a package manager resolves
dependencies, so applying one update moves several, and it is read back from the
host rather than assumed. `requested_by` is NULL for the schedule, which has no
user behind it — inventing one in an audit trail would be worse than a record
that plainly says the schedule did it.

`update_settings` is one row per host: the automatic policy (`off` by default,
because applying updates restarts daemons), how often to check, the day and time
to apply in, and the packages never to apply automatically. That exclusion list
is the panel's own and is not a pin — the host's package manager is never told
about it, so nothing there changes what a person can do at a shell.

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

# 26.1 metric_rollups, service_states, alert_rules and alerts

Added by Phase 19 (migration 0016).

`metric_rollups` summarises completed hours of `system_metrics` and is kept far
longer than the samples are — the PRD's "aggregated metrics: longer retention".
Each bucket carries an **average and a maximum**: the average is what a graph
plots, and the maximum is what stops an hour of aggregation hiding the
five-minute spike that filled a disk. `sample_count` goes with them because a
bucket built from two readings is not the same evidence as one built from a
hundred. Primary key `(server_id, bucket_start)`, which is what makes the
summarising idempotent.

`service_states` records **transitions, not samples**. A row per service per poll
would be tens of thousands a day saying "still running", and the question an
operator asks — "when did it go down, and for how long" — is answered by the
changes alone. `ended_at` is NULL while a state is current, and a unique partial
index enforces one open stretch per service: two would make every duration
ambiguous.

`alert_rules` is what the panel watches for:

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
name VARCHAR(100) NOT NULL
metric VARCHAR(30) NOT NULL     -- cpu, memory, disk, swap, load, network_*, service
target VARCHAR(255) NOT NULL    -- a mount point or service key; empty means any
comparison VARCHAR(10) NOT NULL -- above or below
threshold NUMERIC(12,2) NOT NULL
for_seconds INTEGER NOT NULL DEFAULT 300
severity VARCHAR(20) NOT NULL
enabled BOOLEAN NOT NULL DEFAULT TRUE
```

Thresholds are rows rather than constants because the right number is a property
of the machine, not of this software: 85% memory is alarming on a web server and
ordinary on a database host told to cache aggressively. A panel with hardcoded
thresholds gets muted, and a muted panel is worse than none.

`for_seconds` is the field the phase turns on. A rule that fires on a single
reading turns one backup job into a page at three in the morning; the same rule
with five minutes of sustained breach fires when something is actually wrong.
Zero means "on the first reading", which is right for a service being down.

A unique index on `(server_id, metric, target, severity)` stops two rules
watching one thing: both would fire, and the operator would get two alerts about
one problem.

`alerts` is a condition that has been true long enough to matter:

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
rule_id UUID REFERENCES alert_rules(id) ON DELETE SET NULL
metric, target, severity, threshold   -- copied from the rule
status VARCHAR(20) NOT NULL           -- open or resolved
message TEXT NOT NULL
value, worst, last_value NUMERIC(12,2)
opened_at, last_seen_at, resolved_at TIMESTAMPTZ
acknowledged_at TIMESTAMPTZ
acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL
```

The rule's details are **copied** rather than only referenced, so an alert still
describes itself after its rule is edited or deleted — otherwise an alert whose
threshold changed after the fact would silently rewrite its own history. That is
also why `rule_id` is `ON DELETE SET NULL`: deleting a rule must not erase the
record of what it caught.

A unique partial index on `(server_id, metric, target, severity) WHERE status =
'open'` is what keeps a flapping disk to one alert instead of four hundred rows.

`acknowledged_at` is separate from `status`, and there is no way to set `status`
to resolved from outside: acknowledging says "I know", and whether a condition
has cleared is the machine's to decide. A panel where a person can mark a full
disk as fine is a panel that will one day say a full disk is fine.

---

# 22. backups

Built by migration 0017. The columns below are the specification's, plus the
ones Phase 14 found it could not do without.

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
website_id UUID REFERENCES websites(id)   -- ON DELETE SET NULL
database_id UUID REFERENCES databases(id) -- ON DELETE SET NULL
subject VARCHAR(255) NOT NULL             -- what it was called at the time
type VARCHAR(30) NOT NULL
destination_id UUID REFERENCES backup_destinations(id)
destination VARCHAR(255) NOT NULL         -- its name at the time
path TEXT                                 -- the object key, not a host path
size_bytes BIGINT
checksum VARCHAR(64)
status VARCHAR(30) NOT NULL
verified_at TIMESTAMPTZ
verify_detail TEXT
manifest JSONB
error TEXT
job_id UUID REFERENCES jobs(id)
schedule_id UUID REFERENCES backup_schedules(id)
created_by UUID REFERENCES users(id)
started_at TIMESTAMPTZ
completed_at TIMESTAMPTZ
created_at TIMESTAMPTZ NOT NULL
```

**`verified_at` is separate from `status` and carries the phase.** "The upload
returned success" and "the bytes are there and correct" are different claims,
and only the second is a backup. A backup that finished and could not be read
back is stored as `failed` with the reason, because a listing where "completed"
sometimes means "probably" cannot be used to decide whether anybody is safe.

**Every reference is ON DELETE SET NULL, and the details are copied.** The
moment somebody most needs last night's copy of a site is immediately after
deleting the site, and a backup that can no longer say what it is a backup of is
unrestorable in practice. So `subject` holds the domain or database name as it
was, and `destination` the destination's name as it was.

**`path` is an object key, not a filesystem path.** For S3 and SFTP there is no
host path, and for a local destination the key is joined to the directory by the
Agent rather than trusted from the panel.

A CHECK enforces that a `completed` row has a path, a size and a checksum: a
completed backup that cannot say where it is or how big it is is not a backup,
it is a row.

`manifest` holds what went into the archive, read from the archive's own
manifest after it was written. The per-file list is dropped first: an archive of
a WordPress site has forty thousand entries, and keeping them per backup would
make this table larger than the data it describes. The digests stay in the
archive, where a verify reads them.

---

# 22.1 backup_destinations

Added by migration 0017, and a divergence from section 23 below.

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
name VARCHAR(100) NOT NULL
kind VARCHAR(20) NOT NULL          -- local, s3, sftp
config JSONB NOT NULL
credentials_encrypted TEXT         -- NULL for local
last_check_at TIMESTAMPTZ
last_check_ok BOOLEAN
last_check_detail TEXT
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

Section 23 puts `destination_type` and `destination_config_encrypted` on the
schedule. That is one table fewer and it is wrong in three ways, so it is
deliberately not what was built:

* A **manual** backup needs a destination too, and the spec's shape gives it
  nowhere to come from but a copy of a schedule's.
* Credentials would be duplicated per schedule. Rotating an S3 key would mean
  editing every schedule that used it, and missing one is a backup that silently
  stops working.
* A backup row could name a destination that no longer describes anything,
  because there would be nothing to reference.

`credentials_encrypted` is AES-256-GCM bound to this row's id as additional
authenticated data, so a ciphertext moved from another row fails to decrypt
rather than quietly authenticating somewhere it should not (section 30). It
holds only the halves that grant access — an S3 secret key, an SSH private key.
The *access* key is in `config`: it is an identifier, it appears in every
request's Authorization header anyway, and a page needs to show which key a
destination uses.

A CHECK enforces that a local destination has no credential and the other two
always do. An S3 destination with no secret key is one that fails at the worst
possible moment.

The `last_check_*` columns exist because **a destination that has never been
reached is the most dangerous object in this phase: it looks like protection and
is not.** The panel shows this.

---

# 23. backup_schedules

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
name VARCHAR(100) NOT NULL
type VARCHAR(30) NOT NULL
website_id UUID REFERENCES websites(id)
database_id UUID REFERENCES databases(id)
destination_id UUID NOT NULL REFERENCES backup_destinations(id) ON DELETE RESTRICT
hour SMALLINT NOT NULL
minute SMALLINT NOT NULL
day_of_week SMALLINT NOT NULL   -- 0-6 Sunday first, -1 every day
retention_days INTEGER NOT NULL
keep_last INTEGER NOT NULL
enabled BOOLEAN NOT NULL DEFAULT TRUE
last_run_at TIMESTAMPTZ
last_status VARCHAR(30)
last_backup_id UUID REFERENCES backups(id)
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

An hour, a minute and a day rather than the spec's cron expression. Backing up
needs root and a crontab entry runs as somebody; and a schedule written into a
file the panel does not own is a schedule the panel can no longer answer
questions about. The API's scheduler decides when one is due, the same
arrangement Phase 21 uses for updates.

`keep_last` is the addition that matters. Retention by age alone deletes
everything you have the day after a panel is off for a fortnight; a count floor
is what makes "keep 7 days" fail safe rather than fail empty. CHECKs enforce
that neither can be set to zero: a retention of zero days would delete a backup
the moment it finished, and a schedule that can prune its way to nothing is a
schedule that eventually does.

`ON DELETE RESTRICT` on the destination: deleting one a schedule still uses
would leave the schedule unable to run, which is a backup that silently stops
happening.

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

Built by migration 0018. The columns below are the specification's, plus the
ones a *rescan* needs — a findings table that cannot be rescanned doubles in
size every hour.

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
scanner VARCHAR(30) NOT NULL
severity VARCHAR(20) NOT NULL
category VARCHAR(100) NOT NULL
title VARCHAR(255) NOT NULL
description TEXT
remediation TEXT
fingerprint VARCHAR(200) NOT NULL
status VARCHAR(30) NOT NULL DEFAULT 'open'
metadata JSONB
first_seen_at TIMESTAMPTZ NOT NULL
last_seen_at TIMESTAMPTZ NOT NULL
resolved_at TIMESTAMPTZ
accepted_at TIMESTAMPTZ
accepted_by UUID REFERENCES users(id)
accepted_reason TEXT
accepted_severity VARCHAR(20)
created_at TIMESTAMPTZ NOT NULL
```

**`fingerprint` is the identity of the thing being reported, not of the row.**
Without it every scan inserts a fresh copy, and an operator running a nightly
scan accumulates three hundred rows describing one setting. With it a rescan
updates in place, and `first_seen_at` means something: how long this host has
been wrong about this, which is usually the sentence that gets something fixed.

A unique index on `(server_id, fingerprint)` is **partial on `status <>
'resolved'`**, so something fixed in March and undone in June is two rows. That
is the correct answer: collapsing them would hide the second incident.

**`remediation` exists because a finding without a next step is a nag**, and a
page full of nags is one nobody opens twice.

**The acceptance columns are separate from `status`** for the same reason Phase
19 keeps `acknowledged_at` apart from an alert's status: whether a condition
still holds is the machine's to decide. A CHECK enforces that an accepted
finding always records who and why — an acceptance with no reason is a mute
button. `accepted_severity` is what makes accepting safe: a rescan that finds
the same thing at a higher severity re-opens it and clears the acceptance,
because accepting a medium risk is not accepting the critical version of it.

---

# 27.1 security_scans

Added by migration 0018, and not in this specification.

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
score SMALLINT NOT NULL
checks_run SMALLINT NOT NULL
checks_total SMALLINT NOT NULL
critical, high, medium, low, info INTEGER NOT NULL
accepted, resolved INTEGER NOT NULL
scanners JSONB NOT NULL
duration_ms INTEGER NOT NULL
triggered_by UUID REFERENCES users(id)
created_at TIMESTAMPTZ NOT NULL
```

Two things are impossible without it. A score is only meaningful next to when it
was taken — "68" means nothing and "68, an hour ago, down from 91 last week"
means a great deal. And a panel with no scan row cannot distinguish "this host is
clean" from "this host has never been looked at", which are opposite facts that a
findings table alone renders identically, as no rows.

`checks_run` against `checks_total` is the phase's central rule made durable: a
scanner that could not answer counts towards neither a pass nor a failure, and
the score is never stored without the number of checks behind it.

`scanners` records which ran, which could not, and why — so a score that dropped
because the firewall became unreadable can be told apart from one that dropped
because the firewall was switched off.

---

# 27.2 severity_rank()

A small immutable SQL function added by migration 0018, mapping the severity
scale to a sortable integer with an unknown value ranking last.

It exists because the panel orders findings by severity in several places and
re-opens an accepted finding when its severity *rises*, and both have to mean
exactly what `shared/validate.FindingRank` means. Two copies of an ordering are
two things that can drift.

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

# 28.2 ftp_users

Added by Phase 7.1 (migration 0013).

```sql
id UUID PRIMARY KEY
server_id UUID REFERENCES servers(id)
website_id UUID REFERENCES websites(id)
username VARCHAR(32) NOT NULL
home_subpath VARCHAR(255) NOT NULL DEFAULT ''
access_level VARCHAR(20) NOT NULL DEFAULT 'full'
quota_mb INTEGER NOT NULL DEFAULT 0
suspended BOOLEAN NOT NULL DEFAULT FALSE
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

**There is no password column, and no encrypted one.** The panel writes a
password once into the FTP server's own hashed file and does not keep it.
Storing one would put a copy of every customer's FTP credential in a database
that is backed up, replicated, and read by every part of the panel that touches
this table. "Show me the password" is answered by setting a new one.

`home_subpath` is relative to the website's document root, and the CHECK
constraints refuse a leading slash, a backslash and `..` — the three ways a
subpath stops being one. The absolute path is computed, never stored: a stored
copy would be wrong the moment a website's document root moved.

`username` is unique per **server**, not per website: the FTP server's password
file has a single namespace, so two websites cannot each have a "backup"
account.

---

# 28.3 ftp_settings

Added by Phase 7.1 (migration 0013). One row per host.

```sql
server_id UUID PRIMARY KEY REFERENCES servers(id)
passive_from INTEGER NOT NULL DEFAULT 30000
passive_to INTEGER NOT NULL DEFAULT 30100
tls_website_id UUID REFERENCES websites(id) ON DELETE SET NULL
require_tls BOOLEAN NOT NULL DEFAULT FALSE
masquerade_address VARCHAR(255) NOT NULL DEFAULT ''
max_clients INTEGER NOT NULL DEFAULT 0
created_at TIMESTAMPTZ NOT NULL
updated_at TIMESTAMPTZ NOT NULL
```

The passive range is stored rather than left to the daemon because the firewall
has to admit exactly these ports: without a fixed range every passive transfer on
a firewalled host hangs after a successful login.

`tls_website_id` names the site whose certificate FTPS presents. The certificate
itself is not copied here — Phase 6 owns it and knows where it is, and a second
copy would go stale at the first renewal, silently.

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