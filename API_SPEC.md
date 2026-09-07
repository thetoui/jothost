# JotHost Panel — API Specification

Base URL:

```text
/api/v1
```

Format:

```text
JSON
```

Authentication:

```text
Bearer Access Token
```

---

# 1. Standard Response

Success:

```json
{
  "success": true,
  "data": {},
  "request_id": "req_123"
}
```

Error:

```json
{
  "success": false,
  "error": {
    "code": "RESOURCE_NOT_FOUND",
    "message": "Resource not found"
  },
  "request_id": "req_123"
}
```

---

# 2. Authentication

## Login

```http
POST /auth/login
```

Request:

```json
{
  "username": "admin",
  "password": "password"
}
```

Response:

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "refresh_token": "...",
    "token_type": "Bearer",
    "expires_in": 900
  }
}
```

When the account has two-factor authentication enabled, no tokens are issued.
The response instead carries a short-lived challenge to be completed at
`POST /auth/2fa/verify`:

```json
{
  "success": true,
  "data": {
    "mfa_required": true,
    "mfa_token": "..."
  }
}
```

Failures return `401 UNAUTHORIZED` with an identical body for a wrong password
and for an unknown username, so the endpoint cannot be used to enumerate
accounts. Repeated failures return `429 RATE_LIMITED`.

### Token semantics

Access tokens are **opaque**, not JWTs: they are random strings resolved
server-side against Redis. This makes logout and revocation take effect
immediately rather than at expiry. See docs/PHASE1.md section 3.1.

Refresh tokens are single-use. Every refresh returns a new refresh token and
invalidates the old one; presenting a superseded token is treated as theft and
revokes the user's entire session set.

---

## Logout

```http
POST /auth/logout
```

---

## Refresh

```http
POST /auth/refresh
```

Request:

```json
{ "refresh_token": "..." }
```

Returns a new token pair. The submitted refresh token is invalidated.

---

## Current User

```http
GET /auth/me
```

Returns the caller's profile, including their effective roles and permissions:

```json
{
  "id": "...",
  "username": "admin",
  "email": null,
  "status": "active",
  "roles": ["admin"],
  "permissions": ["server.view", "..."],
  "two_factor_enabled": false,
  "last_login_at": "2026-08-25T05:30:00Z",
  "created_at": "2026-08-25T05:00:00Z"
}
```

---

## 2FA

```http
POST /auth/2fa/setup     (authenticated)  start enrolment, returns the secret
POST /auth/2fa/enable    (authenticated)  confirm a code and activate
POST /auth/2fa/verify    (anonymous)      complete a login that requires 2FA
POST /auth/2fa/disable   (authenticated)  requires the current password
```

`setup` and `verify` were split into three endpoints during Phase 1 because
they serve two different callers. `enable` is used by a signed-in user
finishing enrolment; `verify` is used by a caller who has passed the password
step but holds no session yet, so it cannot require a bearer token.

### Setup

Returns the shared secret once. It is never retrievable again.

```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "otpauth_uri": "otpauth://totp/JotHost%20Panel:admin?..."
}
```

### Enable

```json
{ "code": "123456" }
```

### Verify

Completes a login. The `mfa_token` comes from the login response.

```json
{ "mfa_token": "...", "code": "123456" }
```

Responds with the standard token pair.

### Disable

The current password is required: an unlocked browser session must not be
enough to remove a second factor.

```json
{ "password": "..." }
```

---

# 3. Dashboard

```http
GET /dashboard
```

Requires `server.view`. Accepts an optional `server_id`; without one the local
host is used.

Each panel is wrapped in a widget carrying its availability, so one failing
probe does not fail the page:

```json
{
  "server":   { "id": "...", "hostname": "web01", "status": "online" },
  "system":   { "available": true, "data": { "kernel_version": "...", "uptime_seconds": 86400 } },
  "cpu":      { "available": true, "data": { "usage_percent": 12.5, "cores": 4 } },
  "memory":   { "available": true, "data": { "used_percent": 40.0, "total_bytes": 16000000000 } },
  "disk":     { "available": true, "data": { "filesystems": [] } },
  "network":  { "available": true, "data": { "interfaces": [] } },
  "load":     { "available": true, "data": { "load_1": 0.5, "load_per_core": 0.125 } },
  "services": { "available": true, "data": [] },
  "alerts":   [{ "severity": "critical", "category": "disk", "message": "Disk /var is 95% full" }],
  "generated_at": "2026-08-25T10:00:00Z"
}
```

A widget reports one of three states, which callers must not conflate:

```text
available: true        data is present
available: false       could not be collected; "error" says why
unsupported: true      the host cannot provide this at all
```

Website, database, and SSL counts are not yet included: their tables arrive in
Phases 4, 8, and 6, and reporting zero before then would be inaccurate. See
docs/PHASE3.md section 5.

---

# 4. Servers

```http
GET /servers
GET /servers/:id
```

Both require `server.view`.

The mutating endpoints below manage a fleet, and multi-server clustering is an
explicit non-goal in PRD.md section 3. They are not implemented; the single
managed host is registered automatically from what the Agent reports.

```text
POST /servers            not implemented
PATCH /servers/:id       not implemented
DELETE /servers/:id      not implemented
```

---

# 5. Server Metrics

```http
GET /servers/:id/metrics
```

Requires `server.view`.

Query:

```text
range=1h     1 minute buckets
range=24h    15 minute buckets
range=7d     1 hour buckets
range=30d    6 hour buckets
```

`range` defaults to `1h`. Any other value returns `400`, rather than silently
falling back — a typo returning the wrong window looks like a working graph.

Readings are averaged within each bucket, so a response is 60-200 points
regardless of range:

```json
{
  "range": "1h",
  "bucket": "1m0s",
  "from": "2026-08-25T09:00:00Z",
  "to": "2026-08-25T10:00:00Z",
  "points": [
    {
      "timestamp": "2026-08-25T09:00:00Z",
      "cpu_percent": 12.5,
      "memory_percent": 40.0,
      "disk_percent": 45.0,
      "load_1": 0.5,
      "network_rx_per_second": 1024.0,
      "network_tx_per_second": 512.0
    }
  ]
}
```

Any field may be `null`, meaning that metric was not collected for that bucket.
A chart should break its line rather than interpolating across the gap.

Network is reported as a rate derived from the stored cumulative counters, so
the same underlying data is consistent across every range.

---

# 6. Websites

```http
GET /websites
GET /websites/:id
POST /websites
PATCH /websites/:id
DELETE /websites/:id
```

Create:

```json
{
  "domain": "example.com",
  "php_version": "8.4",
  "document_root": "/var/www/example.com/httpdocs",
  "ssl_enabled": true,
  "https_redirect": true
}
```

**As implemented in Phase 4.** The request body is `domain` and an optional
`name`. The other fields are deliberately not accepted yet:

- `document_root` is **computed**, not accepted. A path from a client is a path
  the Agent would resolve as root; it is derived from the validated domain as
  `/var/www/<domain>/public`.
- `php_version` is Phase 5. `ssl_enabled` and `https_redirect` are Phase 6 and
  are **refused with 400** rather than accepted and ignored — a user who
  believes their site is encrypted when it is not is worse off than one told
  the feature is unavailable.

Unknown fields are rejected, so a misspelled key is reported rather than
silently dropped.

Creation is asynchronous. `POST /websites` returns **201** with both the
website (in status `creating`) and the job realising it:

```json
{
  "website": { "id": "…", "status": "creating", "system_user": "web_example_a1b2c3", "…": "…" },
  "job": { "id": "…", "type": "website.create", "status": "PENDING", "progress": 0 }
}
```

`DELETE /websites/:id` returns **202** with the job; the record survives until
the Agent confirms the site is gone from the host.

The site's **files are kept** unless `?remove_files=true` is passed. Keeping
them is the default because a vhost can be recreated and content cannot, but it
has two consequences worth knowing before choosing it:

- The retained tree is reassigned to root before the site's account is removed,
  so the uid going back into the allocation pool stops owning anything. Without
  that step the directory silently changes hands the next time that number is
  issued.
- Because the files are still there, a later site cannot be provisioned into
  that directory: creation refuses rather than adopting another account's
  content. Recreating a website on the same document root therefore needs the
  old files removed first — with `?remove_files=true` on the delete, or by hand.

The same parameter applies to `DELETE /subdomains/:id`.

---

# 6.1 Audit trail

```http
GET /audit
GET /audit/actions
```

Both need `audit.view`, and nothing else needs it. The trail records who did
what across every customer on the host, which is a different thing to be
trusted with from seeing how much disk is left — so it is not folded into
`server.view`.

`GET /audit` takes `action`, `user_id`, `resource_type`, `resource_id`,
`status` (`SUCCESS` or `FAILURE`), `since` and `until` (RFC 3339), plus `limit`
and `offset`. It answers with the page, the `total` that matched the filter,
and the `limit` actually applied — which is not always the one asked for: a
page is capped at 200, because this table grows for the life of the
installation and an unbounded read is a way to ask the panel to load its own
history into memory.

`GET /audit/actions` lists the action names actually present, for populating a
filter. It is read from the trail rather than from a list in the code: every
feature appends its own names, and a filter offering names nothing ever
recorded is worse than no filter.

There is no way to write, change or remove an entry, and there is not meant to
be. `audit_logs` has carried triggers rejecting UPDATE and DELETE since
migration 0001.

---

# 7. Domains

```http
GET /websites/:id/domains
POST /websites/:id/domains
PATCH /domains/:id
DELETE /domains/:id
```

**As implemented in Phase 4.** `POST` and `DELETE` are implemented and each
returns the job rewriting the vhost. `PATCH /domains/:id` is not implemented.

Type `alias` and `subdomain` are accepted. Type `redirect` returns **400** for
now: the Agent can write a redirect vhost but cannot remove a stale one, so
accepting it would create configuration the panel could not take back. The
primary domain cannot be detached — it is the site's identity and its vhost's
`server_name`.

A domain of type `subdomain` here is an extra name served by *this* site — an
alias whose hostname happens to sit beneath the site's domain. A subdomain in
the Plesk sense, with its own document root and vhost, is section 7.1.

Adding or removing a name rewrites the site's whole vhost from its complete
current state — its PHP socket, its certificate, its application port included.
It used to send only the names, which turned each of those off as a side effect
(docs/PHASE4.1.md §4).

---

# 7.1 Subdomains

```http
GET    /websites/:id/subdomains
POST   /websites/:id/subdomains
DELETE /subdomains/:id
```

**As implemented in Phase 4.1.**

A subdomain is a website in its own right — its own document root, vhost, logs,
certificate, PHP, files, databases and Node application — recorded as a website
row with a parent rather than in a table of its own. Every endpoint that takes
a website id therefore works on one, `GET /websites/:id` included.

`POST` takes a **label**, not a hostname:

```json
{
  "name": "shop",
  "document_root_mode": "nested",
  "php_pool_mode": "inherit",
  "system_user_mode": "inherit"
}
```

The full name is derived from the parent's own record, so a caller cannot
create a site under a domain the parent does not own — it never supplies that
half. The label may contain dots (`dev.shop`), which is how a name several
levels down is created; `*` creates a wildcard that catches every name beneath
the parent no other site claims. All three modes are optional and default to
the values above.

It returns **201** with the website and the job provisioning it, the same shape
as `POST /websites`.

Refusals worth naming:

- a subdomain of a subdomain → **400**. One level is the whole model; a dotted
  label reaches the same hostname under the top-level site.
- `system_user_mode: "dedicated"` with `php_pool_mode: "inherit"` → **409**.
  The parent's pool runs as the parent's user, so PHP would run as one account
  over files owned by another: every write fails and it reads as a broken
  application.
- a parent that is still being created → **409**. There is no directory for a
  nested subdomain to live in yet.
- a name already hosted → **409**, because a name is served by one site.

`DELETE /subdomains/:id` returns **202** with the job. It is separate from
`DELETE /websites/:id` because the two differ on the one question that matters:
whether the system account goes with the site. A subdomain that shares its
parent's account never takes it — doing so would leave the parent's files owned
by a user that no longer exists and the parent serving 403 to every visitor.
Passing a top-level website here returns **400**.

`DELETE /websites/:id` on a site that still has subdomains returns **409** and
names them. The database would cascade the rows, but the rows are not the
sites: each has a vhost on the host and nothing would be queued to remove it.

`GET /websites?include_subdomains=true` adds them to the websites listing,
which leaves them out by default so the sites page shows sites rather than
everything nested under them.

Reading needs `website.view`, creating `website.create`, removing
`website.delete`: a subdomain is a website, so it takes a website's authority.

---

# 7.2 Web server

```http
GET  /webserver
PUT  /webserver
POST /webserver/apache/install
```

**As implemented in Phase 4.5.**

`GET` reports the arrangement the panel has recorded, what Apache is actually
doing on the host — installed, running, version, how many sites are behind it,
and whether it could be installed — and how many websites a mode change would
rewrite. The record and the host are reported separately on purpose: the two
disagreeing is exactly what an operator needs to see.

`PUT` takes `{"mode": "nginx" | "hybrid"}` and returns **202** with one job per
website. The arrangement is a property of the host, not of a site — both
servers are one process tree serving every site on the machine — so changing it
rewrites every site's configuration. Sites keep serving throughout: each is
reconfigured and reloaded in turn, and a site whose reload fails keeps the
configuration it already had.

Refusals: `hybrid` on a host without Apache is **409**, named rather than
discovered by each site's job failing in turn; the arrangement already in use
is **409**; anything that is not one of the two modes is **422**.

`POST /webserver/apache/install` installs Apache and the FastCGI proxy module.
It sends no package name — the Agent holds the only list, because a name from a
request would be an argument to a package manager running as root — and it does
**not** change the arrangement: installing a package and rewriting every site
on the machine are different decisions.

Reading needs `server.view`; both changes need `server.manage`. Authority over
one website is not authority over how the host serves all of them.

Per-site: `PATCH /websites/:id` accepts `allow_override`, which is whether
Apache reads `.htaccess` for that site. It rewrites the site's vhost like any
other configuration change; on a host running nginx alone nothing reads the
file, and the panel does not offer the switch.

---

# 8. PHP

```http
GET /php/versions
POST /php/versions/install
DELETE /php/versions/:version
GET /php/versions/:version
GET /websites/:id/php
PATCH /websites/:id/php
```

Example:

```json
{
  "version": "8.4"
}
```

**As implemented in Phase 5.** All six endpoints exist. Notes:

- A version is `major.minor` only. A patch level (`8.4.19`) is refused: it is a
  property of what happens to be installed, not something a user selects, and
  accepting both spellings would let one version exist as two.
- Install and remove return **202** with the job realising them; the version is
  not on the host until that job succeeds.
- Removing a version websites still run returns **409**. Taking it off the host
  would break every one of those sites.
- `GET /websites/:id/php` returns `{"enabled": false}` for a static site rather
  than 404 — a site without PHP is a valid configuration, not a missing one.
- `PATCH /websites/:id/php` requires the `version` field. An explicit `null`
  turns PHP off and makes the site static again.

Listing versions needs `server.view` and installing needs `server.manage`:
installing changes the whole server, not one site.

---

# 9. PHP Configuration

```http
GET /websites/:id/php/config
PATCH /websites/:id/php/config
```

Example:

```json
{
  "memory_limit": "512M",
  "upload_max_filesize": "100M",
  "max_execution_time": 300,
  "opcache": true
}
```

**As implemented in Phase 5.** Both endpoints exist and use exactly these
field names. `PATCH` returns **202** with a job: a php.ini value that is
written but not reloaded is a setting the panel claims is active and is not.

Values are bounded rather than passed through. They are written verbatim into
an FPM pool file, so each is matched against an anchored pattern — a value
carrying a newline could otherwise close its directive and append another.
`memory_limit` accepts `-1` for PHP's "no limit"; sizes and times are capped
(see `shared/validate`). A rejected value returns **422** and is never stored.

Unset fields keep whatever the pool already has, so changing one setting does
not silently reset the others.

---

# 10. Node.js

```http
GET    /node/versions
POST   /node/versions/install
DELETE /node/versions/:package
```

**As implemented in Phase 9.**

`GET /node/versions` reports the runtimes installed, the release lines this
host could install, whether it can install anything at all, and — importantly —
`managed_by`: `systemd` or `agent`. That last one decides where an
application's output goes, so the panel says which it is rather than leaving
someone to work it out from an empty log.

Install takes a **package**, not a version: the Agent keeps the only table of
package names, and the value sent must be one it offered. There is no free-text
version, because that string would otherwise reach a package manager running as
root.

Removing a runtime is refused while any application still uses it. The Agent
would do as it was told and every one of them would stop at its next restart.

Installing needs `server.manage`: it changes the whole host, not one site.

Applications:

```http
GET    /node/apps
GET    /node/apps/:id
POST   /node/apps
DELETE /node/apps/:id
```

**As implemented in Phase 9.** `PATCH` is not offered: the name is a systemd
unit's identity and the website decides the account, the directory, and the
vhost, so changing either of them is a new application rather than an edit.

`POST` takes `website_id` and `port`, and optionally `name`, `node_version`, and
`startup_file`. The name defaults to one derived from the domain, the version to
the host's only runtime, and the startup file to `server.js`.

It returns **201** with the application **stopped**. Creating and starting are
separate: a deployment that is not ready to serve should be created, looked at,
and started deliberately.

One application per website, and one application per port on a server. A second
on a site would need a second vhost to reach it; a second on a port means one of
them is failing to bind and the panel would not know which.

A site that serves PHP is refused, because a site is served by an application or
by files, never both.

Reading needs `website.view` and every change needs `website.update`: an
application is what a website serves.

---

# 11. Node Process

```http
POST /node/apps/:id/start
POST /node/apps/:id/stop
POST /node/apps/:id/restart
GET  /node/apps/:id/logs
POST /node/apps/:id/dependencies
```

**As implemented in Phase 9.** Synchronous: the response says what actually
happened rather than that the intent was recorded.

Starting points the website's vhost at the application **only once it is
listening** — the site was serving something before the request, and replacing
that with a 502 while a process boots is worse than what was asked for.
Stopping does the reverse first, so a deliberate stop does not look like an
outage.

`GET .../logs` takes `lines` (at most 2000). On a systemd host the output is in
the journal, which this panel does not read; the response says so and names the
`journalctl` command rather than returning an empty list.

`POST .../dependencies` runs `npm install --omit=dev` as the website's own
account, so what it writes into `node_modules` belongs to the account that will
read it. It is the one slow call here and holds the request until it finishes.

Environment:

```http
PUT    /node/apps/:id/environment
DELETE /node/apps/:id/environment/:key
GET    /node/apps/:id/environment
```

`PUT` sets one variable; `DELETE` removes one. Neither restarts the
application — a process reads its environment once, at startup, so the change
takes effect on the next restart, and restarting somebody's application as a
side effect of editing a setting is not the panel's decision.

Names that change *what runs* rather than how it behaves are refused:
`LD_PRELOAD`, `LD_LIBRARY_PATH`, `NODE_OPTIONS`, `PATH`, `BASH_ENV`, and the
names the panel sets itself (`PORT`, `HOME`, `USER`). A value containing a
newline is refused, because it would close a line in a unit file and start a
directive of the caller's choosing.

Values are stored AES-256-GCM encrypted, bound to their own row. A listing names
what is set and never what it is set to. `GET` returns the values and needs
`server.manage`, is audited, and is sent `Cache-Control: no-store`: it hands
back credentials.

---

# 12. Files

```http
GET /files
POST /files/upload
POST /files/folder
POST /files/file
PATCH /files
DELETE /files
POST /files/copy
POST /files/move
GET /files/download
POST /files/zip
POST /files/unzip
```

Every path must be validated server-side.

**Implemented in Phase 7** — see [docs/PHASE7.md](docs/PHASE7.md).

Paths travel as query parameters on the read verbs and in the JSON body on the
write verbs. `POST /files/folder` and `POST /files/file` take a directory
(`path`) and a `name`, which is always a single segment.

`PATCH /files` renames (`name`) or changes permissions (`mode`, optionally
`recursive`). `DELETE /files` needs `recursive=true` for a non-empty directory.

The Agent's socket caps a message at 1 MiB, so uploads and downloads are chunked
between the API and the Agent, and directory listings are paged (`offset`,
`limit`, with `total` and `truncated` in the response). Neither the API nor the
Agent ever holds a whole file.

Reads require `file.read`; every mutation requires `file.write`.

---

# 13. Code Editor

**Implemented in Phase 7.5** — see [docs/PHASE7.5.md](docs/PHASE7.5.md).

The code editor uses the File API.

```http
GET /files/content
PUT /files/content
```

Request:

```json
{
  "path": "/var/www/example.com/httpdocs/index.php",
  "content": "<?php ...",
  "checksum": "sha256 of what was loaded",
  "force": false
}
```

A read returns the content with the file's mode, owner, language, line-ending
style, and a `checksum` of exactly what was loaded. A save sends the checksum
back; if the file changed underneath, the save is refused with **409** rather
than silently overwriting it. `force: true` overwrites deliberately, which the
panel asks about before setting.

A file over 2 MiB, or one containing a NUL byte, is refused: too large to edit is
not too large to download, and editing a binary would corrupt it.

Reads require `file.read`; saves require `file.write`.

---

# 14. Databases

```http
GET    /databases
POST   /databases
GET    /databases/engines
GET    /databases/:id
DELETE /databases/:id
POST   /databases/:id/size
```

**As implemented in Phase 8.** The four endpoints above are joined by
`GET /databases/engines`, which reports what the host actually runs, and
`POST /databases/:id/size`, which re-measures one database on demand.

These endpoints are **synchronous**, unlike websites and certificates. Creating
a database is a single DDL statement that finishes in milliseconds; a job would
add a poll cycle of latency and, worse, leave no response in which to hand back
the generated password. A **201** therefore means the database exists on the
host, not that the intent was recorded.

`POST /databases` takes `name`, and optionally `engine`, `website_id`,
`create_user`, `username`, `host`, and `password`.

`engine` may be omitted on a host running one server, which is most of them.
`create_user` defaults to **true**: a database no account can reach cannot be
used by anything, so creating one alone is the unusual case.

Linking a database to a website is a convenience, not ownership. Deleting the
website leaves the database, and its `website_id` becomes null — the data
outlives the vhost, and dropping it has to be a separate, deliberate decision.

`GET /databases/engines` returns each engine the panel knows about with
`available`, `version`, `supports_host_patterns`, and — when it cannot be used —
a `detail` explaining why. "Not installed" and "installed but not answering" are
reported as different states, because they have different fixes.

Everything here needs `database.manage`, reads included. The split some other
resources use would be a mistake here: a role that can list accounts is one
step from a role that can read their passwords.

`PATCH /databases/:id` changes which website a database is related to; an empty
`website_id` unlinks it. Nothing on the database server changes — the link
exists so the panel can show a database on the site that uses it.

```http
GET    /databases/console
POST   /databases/console
DELETE /databases/console
```

phpMyAdmin. It is **not installed** until somebody asks, and `POST` requires a
`server_name`: there is no default, because a database console reachable on a
name nobody chose is one somebody else finds first. Both `POST` and `DELETE`
return **202** with a job — installation is a package download, a system
account, an FPM pool, and a vhost, and it is the only operation in this section
that does not finish inside the request.

`GET` reports `installed`, `served`, the address, the PHP version behind it,
and — when it cannot be installed — why.

```http
POST   /databases/:id/console-session
```

Opens phpMyAdmin on one database, signed in as an account that can reach it.
The response carries `url`, `database`, `username`, `password` and `host`, and
is sent with `Cache-Control: no-store`.

The password is in the body on purpose. phpMyAdmin authenticates with a
database account; the panel stores these passwords because MySQL and PostgreSQL
keep only a hash, and `GET /database-users/:id/password` already reveals them to
this same permission. A shared administrative account is never used — it would
give everyone who can open a console full access to every database on the host.
Where several accounts can reach the database the one with the most access
wins, and ties break on username so the same database always opens as the same
account.

`url` is the path the panel proxies phpMyAdmin at on its **own origin**, not the
hostname the Agent published it under. Behind that path is the panel's own
nginx and its own PHP-FPM master, separate from the pair that serve customer
websites — see ARCHITECTURE.md — so a website cannot take the console down. That is forced rather than preferred:
phpMyAdmin's login is a POST carrying a CSRF token bound to the session cookie
set on the page the form came from, so a caller must read that page before it
can sign anybody in, and only same-origin JavaScript may read it. Posting blind
to phpMyAdmin's own hostname returns the login page every time.

**POST**, never GET, and the credentials go in a form body. In a query string
the password would land in phpMyAdmin's access log, in the `Referer` of every
link on the page that follows, and in browser history.

**409** when no account the panel holds a password for can reach the database,
and **503** when phpMyAdmin is not served on this host. Each session is audited
as `database.console.session`, recording who opened which database as which
account — and not the password.

---

# 15. Database Users

```http
GET    /databases/:id/users
POST   /databases/:id/users
PATCH  /databases/:id/users/:userId
GET    /database-users
DELETE /database-users/:id
PATCH  /database-users/:id/password
GET    /database-users/:id/password
```

**As implemented in Phase 8.**

`POST /databases/:id/users` creates an account and grants it access in one
call. An omitted `password` asks the host to generate one, which is the normal
path: the password is then never typed, never sent from the browser, and comes
back in this response exactly once.

`PATCH /databases/:id/users/:userId` sets the account's `privilege` on that
database. The three levels are `readonly`, `readwrite`, and `full`, and they are
the only accepted values — a panel that forwarded a privilege string could be
asked for `SUPER` or `FILE`, either of which is server-wide. An **empty**
privilege revokes: "no access" travels the same path as every other level, so
there is one piece of code deciding who can reach a database rather than two
that can disagree.

A grant replaces whatever the account held, rather than adding to it. Lowering
someone from `full` to `readonly` actually takes `DROP` away.

`PATCH /database-users/:id/password` rotates a password, generating one when
the body's `password` is empty. The stored copy is replaced only after the
server accepts the change, so the panel never shows a credential the server
would reject.

`GET /database-users/:id/password` returns a stored password. It is a separate
endpoint rather than a field on any listing, and every call is audited: this is
the one request in the panel that hands back a working credential, and "who
read this, and when" has to remain answerable. The response is sent
`Cache-Control: no-store`.

Passwords are stored AES-256-GCM encrypted, bound to their own row, so a
ciphertext copied from another account fails to decrypt rather than revealing
that account's password. The panel stores them because the servers do not: both
MySQL and PostgreSQL keep only a hash, so a password not captured here could
never be shown to the person who has to put it in a configuration file.

**Account identity differs by engine.** MySQL identifies an account by user
*and* host, so `'app'@'localhost'` and `'app'@'%'` are two accounts; only
`localhost` and `%` are accepted, because a literal address or a wildcard
pattern is how a typo becomes a database exposed to a subnet. PostgreSQL roles
are global, so `host` is empty there and supplying one is refused rather than
silently ignored.

---

# 16. SSL

```http
GET /ssl
GET /websites/:id/ssl
POST /websites/:id/ssl/issue
POST /websites/:id/ssl/renew
POST /websites/:id/ssl/revoke
PATCH /websites/:id/ssl
```

**As implemented in Phase 6.** All six exist, plus `GET /ssl/providers`, which
reports what the host can issue so the panel can hide an option that would
always fail.

Two providers are offered: `letsencrypt` (certbot over ACME, needs public DNS)
and `selfsigned` (always available, for hosts without it). Issue, renew, and
revoke each return **202** with the job realising them — a certificate does not
exist until the host says so.

`PATCH /websites/:id/ssl` takes `auto_renew` and `https_redirect`. Changing the
redirect returns **202** with a job, because the redirect *is* the vhost and the
host has to be rewritten; changing only `auto_renew` returns **200**, because
nothing on the host reads it.

`GET /websites/:id/ssl` returns `{"enabled": false}` for a site on plain HTTP
rather than 404 — HTTP is a valid configuration, not a missing one.

Issuing and revoking need `ssl.manage`; listing needs only `website.view`.

## Issuance puts this host's own DNS in order first

A Let's Encrypt certificate is obtained over the HTTP-01 challenge: the
authority resolves every name on the certificate and fetches a file from
whatever answers. A name resolving nowhere fails, and a failed challenge is
spent — Let's Encrypt allows a small number per hour and then stops looking.

So before the job is queued, each name that falls inside a primary zone this
panel serves gets an address record pointing at this host, and the 202 carries
a report of what happened:

```json
{
  "job": { "id": "…", "type": "ssl.issue" },
  "dns": {
    "address": "203.0.113.10",
    "added": 1,
    "blocked": 0,
    "names": [
      { "name": "example.com", "zone": "example.com", "status": "added",
        "detail": "A record added, pointing at 203.0.113.10" }
    ]
  }
}
```

`status` is one of `ready` (already points here), `added` (the panel wrote the
missing record), `elsewhere` (a record names a different machine), `aliased` (a
CNAME, which may or may not lead here), `not_served` (no zone here covers the
name) or `no_address` (this host's own address is not known). `blocked` counts
the first and last of those, which are the two that fail validation.

Only a missing record is ever written. A record naming a different machine is
reported and left alone: repointing a live domain can take a working site off
the internet, and nobody clicking Issue is asking for it. A failure to reach
DNS does not stop issuance — the panel's zones are one of several places a name
can be served from — and `dns` is then absent, as it is for `selfsigned`, which
no authority ever resolves anything for.

The change is audited as `dns.certificate.align`.

---

# 17. DNS

```http
GET    /dns
POST   /dns/install
PUT    /dns/settings

GET    /dns/zones
POST   /dns/zones
GET    /dns/zones/:id
PATCH  /dns/zones/:id
DELETE /dns/zones/:id

POST   /dns/zones/:id/records
PATCH  /dns/records/:id
DELETE /dns/records/:id

POST   /dns/providers
DELETE /dns/providers/:id
POST   /dns/zones/:id/sync

GET    /websites/:id/dns
```

**As implemented in Phase 13.** Reading needs `server.view`; every change needs
`dns.manage`, which migration 0002 seeded with the rest of the permission set.

The routes are addressed by zone id rather than by domain, which is the one
deviation from the shape sketched above. A zone is not a domain: a reverse zone
is not a name anybody owns, a zone's name cannot change without it becoming a
different zone, and two hosts can serve zones of the same name. An id says
exactly which row is meant.

`GET /dns` reports what the *host* has joined with what the panel recorded: the
server's version, whether it is running, whether this BIND can sign zones,
whether `named.conf` actually includes the panel's zones, the addresses it
answers on, whether the firewall admits port 53, and the panel's zones. Its
`host_zones` is a separate list — the zones the host is configured for — because
a name there and not in `zones` is a zone the next reconcile removes.

`POST /dns/zones` takes:

```text
name            the apex, for a forward zone
reverse_network given instead of a name, and the zone is named after it
kind            "master" (default) or "slave"
masters         required for a secondary: where it transfers from
nameservers     defaults to the host's configured name servers
primary_ns      defaults to the first of those
hostmaster      an email address; the zone file's dotted form is produced
dnssec          asks named to sign it
seed_records    defaults true: the apex and www pointing at this host
```

A zone whose name servers are *inside it* also gets the address records those
name servers need. That is not decoration: without them `named-checkzone`
refuses the zone, so the server would not load it at all. When the panel does
not know this host's address it refuses the zone and says why, rather than
writing one that cannot be served.

Every change hands the Agent the **complete** set of zones and the Agent makes
the host match, so a delete is performed by the zone's absence from that set.
A record change also advances the zone's serial: the file is being rewritten,
and a secondary compares serials to decide whether to transfer, so a rewritten
zone at an unchanged serial is a change every secondary in the world ignores.

`GET /dns/zones/:id` carries the zone, its records, and what the running server
says about it — including **two serials**. With inline signing named keeps its
own serial on the signed copy and it runs ahead of the file's; they are reported
separately, and a difference between them is not drift. A signed zone also
carries the **DS record** its parent's registrar needs: until that is published,
no resolver knows to check the signatures.

Refused with a **422**: a record type outside the supported nine, an AAAA record
holding an IPv4 address (or the reverse — both parse and neither resolves), a
CNAME sharing a name with another record or sitting at the apex, a misspelt CAA
tag, an SRV record with no port, a TTL under a minute or over a week, SOA timers
that heal more slowly the more the zone breaks, a reverse zone for a network
that cannot have one (a /25 needs an RFC 2317 delegation the address's owner has
to make), a PTR for an address outside its zone, an edit to a secondary zone's
records, and signing on a server with no `dnssec-policy`.

A duplicate zone is a **409**: two zones of one name is a configuration named
refuses to load.

Port 53 is **reported against the firewall, not opened** — the boundary Phases
17 and 7.1 drew, for the reason CLAUDE.md section 19 gives. It is worth
reporting because of how it fails: the panel's checks pass, `dig` on the server
answers, and the zone is invisible from the internet with no error anywhere.

`POST /dns/providers` stores a remote provider's API token, encrypted against
its own row and never returned by any endpoint. `POST /dns/zones/:id/sync`
pushes a zone there. Deleting records the panel does not have is opt-in
(`prune`): a provider's zone usually holds records added in their dashboard, and
removing what the panel does not recognise would break them with no warning.

`dns.zone.create`, `.update`, `.delete`, `dns.record.create`, `.update`,
`.delete`, `dns.configure`, `dns.install`, `dns.provider.add`, `.remove` and
`dns.sync` are audited. A provider token never appears in an audit record.

---

# 18. Cron

```http
GET    /cron
POST   /cron
GET    /cron/:id
PATCH  /cron/:id
DELETE /cron/:id
POST   /cron/:id/run
GET    /cron/:id/logs
```

**As implemented in Phase 10.** Everything here needs `cron.manage`, reads
included: a job's command line describes how a site works, and sometimes carries
a token in a URL.

A job belongs to a website and runs as that website's own unprivileged account.
**There is no field for a user**, and that is the design rather than an
omission — see docs/PHASE10.md section 2.

`POST /cron` and `PATCH /cron/:id` take:

```text
website_id   the site whose account the job runs as (create only)
name         what the operator calls it
job_type     php | url | command (create only; a type change is a new job)
schedule     five fields, or @daily / @hourly / @weekly / @monthly / @yearly
target       a script path, a URL, or a command line, depending on job_type
enabled      whether the entry is written at all
```

For `php` and `url` the panel builds the command line itself — an interpreter it
resolved plus a path inside the site's document root, or a bounded `curl` — so
`target` decides *which* script or address, never which program. `command` is
the free-form case. The rendered command is returned as `command` alongside the
`target` the operator typed, because "what will actually run" is the question
they have when a job does not do what they expected.

Refused, with the reason: a schedule that is not five fields or a known
shorthand, `@reboot` (no five-field equivalent, so the panel cannot say when it
would next run), six fields (the seconds form, which would run a job sixty times
more often than intended), any value containing a newline or `%` (a crontab is
line-oriented and unquoted, so both are ways to write a second entry), a script
path that is absolute or contains `..`, and a URL that is not http(s) or that
carries characters a shell would read as syntax.

The response carries `next_run_at`, computed from the schedule rather than
stored — null for a disabled job, and null for a schedule that can never fire,
which 31 February is.

`POST /cron/:id/run` runs the job now, as its own account, and **holds the
request open until it finishes**: pressing "run now" asks what happens, and a
reply of "started" answers a different question. It returns the exit code,
what the job printed, and how long it took, and records the outcome against the
job.

`GET /cron/:id/logs` returns the log *source* for the job — every run's output,
scheduled and manual, goes to one file per job, which the Phase 11 log viewer
serves at `GET /logs/cron.<job-id>` with its search, filtering and download.

---

# 19. Logs

```http
GET /logs
GET /logs/:key
GET /logs/:key/download
```

**As implemented in Phase 11.** A request names a **source key from the Agent's
catalogue, never a path**. That is the whole security of the feature: a log
viewer that took a path would be an unrestricted file reader, and no amount of
validating the string afterwards would make it safe to have asked.

`GET /logs` lists what this host has. Logs that are catalogued but absent are
listed too, marked `present: false` — "nginx has recorded no errors" and "this
panel does not offer that log" are different answers.

The URLs above are the same thing as the keys, with the dot written as a path
separator, so the routes this specification has always listed work unchanged:

```http
GET /logs/nginx/access      = /logs/nginx.access
GET /logs/nginx/error       = /logs/nginx.error
GET /logs/system            = /logs/system
GET /logs/security          = /logs/security
GET /logs/agent             = /logs/agent
```

Keys only knowable at runtime have a URL for the same reason: `php.8.4` for each
installed PHP version, and `node.<app>.out` / `node.<app>.error` for each Node
application. `/logs/php` and `/logs/node` on their own are **404s**, because
this host has no single PHP or Node log to serve.

Query on `GET /logs/:key`:

```text
limit     lines to return, at most 2000 (default 200)
search    substring, compared without case
level     error | warn | info | debug
after     byte offset from a previous read
```

`after` is what makes following a log cheap: the response carries an `offset`,
and passing it back returns only what was appended since. The offset is the end
of the last *complete* line, so a line still being written arrives whole on the
next read rather than in halves. A response with `rotated: true` means the file
shrank underneath the caller — it was rotated or truncated — and the offsets
from before it refer to a file that no longer exists.

`offset`, `from` and `to` from the original sketch are **not implemented**:
paging backwards through a log is what `search` is for, and filtering by time
means parsing each daemon's own timestamp format, where a filter that silently
returns nothing because it failed to parse a format is worse than one that is
not offered. See docs/PHASE11.md section 6.

`GET /logs/:key/download` streams the file as the daemon wrote it, always as
`application/octet-stream` with an attachment disposition: a log is
attacker-influenced content — anyone who can make a request can write a line
into an access log — and it must never be rendered on the panel's own origin.
Downloads are audited as `log.download`; reading a log on screen is not.

Reading and downloading both require `server.view`. The host's logs are not one
website's business: an access log names every site on the machine, and the
authentication log names the people who administer it.

---

# 20. Services

```http
GET  /services
POST /services/:key/start
POST /services/:key/stop
POST /services/:key/restart
POST /services/:key/enable
POST /services/:key/disable
```

**As implemented in Phase 12.**

The path segment is a **key from the Agent's catalogue, not a unit name**. A
request naming `nginx` is answered; one naming `nginx.service`,
`systemd-logind.service` or anything else a caller invents is a **404**. The
unit that key becomes is chosen by the Agent from a table in the repository, so
nothing sent from a client ever reaches systemctl as the thing being acted on.
`GET /services/:name` is therefore not offered: a per-service read would be a
second place that maps a caller's string to a unit, and the listing already
carries every service's state.

`GET /services` reports what the host actually has — detected, not configured.
Each entry carries its state twice over: `running` and `pid` come from the
process table, which is readable whether or not systemd is present, while
`unit`, `active_state`, `sub_state` and `enabled` come from systemd where it
can answer. `enabled` is `null` when nothing can say, which is a different
answer from `false`. Services that are not installed are left out entirely.

The listing also carries `controllable`, on the response and on each service.
It is `false` on a host with no service manager, where the states above are
still true and nothing can be changed. That is reported once, as a property of
the host, rather than discovered one failed action at a time.

Each verb returns the service's state **after** the action, read from the host
rather than assumed: a start that returns zero and leaves the service down is
exactly what an operator needs to see.

Refusals worth naming:

- a key outside the catalogue → **404**
- `stop` or `disable` on a protected service → **409**. SSH is protected:
  stopping it on a remote host locks the operator out of the machine they are
  administering, and nothing in the panel can put them back. `restart` stays
  available, because that is how a configuration change is applied.
- any verb on a host with no service manager → **409**, saying so
- a verb the panel does not have (`mask`, `reload`) → **404**: the verb is part
  of the route, so an invented one is not a route.

Reading needs `server.view`; every verb needs `server.manage`. Restarting the
database is not authority over one website. Every action is audited under its
own name — `service.start`, `service.stop` and so on — including the refusals,
because "who tried to stop SSH" is a question worth being able to answer.

---

# 20.1 Firewall

```http
GET    /firewall
POST   /firewall/rules
DELETE /firewall/rules
POST   /firewall/enable
POST   /firewall/disable
PUT    /firewall/default
POST   /firewall/changes/:id/confirm
POST   /firewall/changes/:id/rollback
```

**As implemented in Phase 16.**

Every change returns **202** with a change id and a deadline. The change is live
immediately — it is a real rule, not a proposal — and the Agent has armed a
timer to undo it. Confirming inside the window commits it; letting the window
close restores the rules the host had before (CLAUDE.md section 19).

**Confirming is the connectivity test.** It is an ordinary request, and that is
the point: it has to cross the network the change governs. A panel that has just
firewalled itself off cannot send it, so the change is undone. The Agent cannot
perform that test itself — a connection to the host's own address is routed over
loopback and allowed by a rule ufw installs for that purpose, so it would pass
while the host was unreachable.

`DELETE /firewall/rules` takes a body rather than an id in the path. A rule is
identified by what it does; ufw's position numbers renumber on every change, so
a path naming rule 3 would mean something different by the time it arrived.

`GET /firewall` reports the rules, the default policies, the guarded ports, and
any change waiting to be confirmed. While the firewall is off it reports the
rules that are *staged* — ufw's own listing shows nothing then, because it
describes what is being enforced.

Refusals worth naming:

- a change that would close a guarded port → **409**, naming the port. That
  covers `deny 22`, a range covering it, a rule with no port at all, removing
  the last rule that allows it, and switching the firewall on while its default
  policy would close it.
- a second change while one is unconfirmed → **409**
- a port, address, or comment that could not be part of a rule → **422**
- confirming a change that has expired or does not exist → **404**

Reading needs `server.view`; every change needs `server.manage`. A firewall
governs the whole machine. Every change is audited under `firewall.change`,
`firewall.confirm` or `firewall.rollback` — including the refusals, because
"who tried to close SSH" is a question worth being able to answer.

---

# 21. Processes

```http
GET /processes
POST /processes/:pid/stop
POST /processes/:pid/kill
```

---

# 22. Backups

```http
GET    /backups
POST   /backups
GET    /backups/:id
DELETE /backups/:id
POST   /backups/:id/restore
POST   /backups/:id/verify
```

**As implemented in Phase 14.** Everything here needs `backup.manage`, which
migration 0002 seeded and granted to admin and operator. Reading is not split
out to a lesser permission: a backup listing names every website and database on
the host, and a manifest names every file in them. That is a map of the machine,
and it belongs behind the same permission as the thing it describes.

`GET /backups` carries the host's *capabilities* as well as the backups, the
destinations, the schedules and the counts. A destination kind the host cannot
reach — SFTP where no sftp client is installed — has to be visible before
somebody configures one and finds out at three in the morning.

The field that carries the weight is **`verified_at`**. A backup row with a
`completed` status and a `verified_at` claims four things: the archive is at its
destination, it is this many bytes, it hashes to this, and the panel read it
back and got the same answer. "The upload returned success" and "the bytes are
there and correct" are different claims and only the second is a backup — so a
backup that finished and could not be confirmed is stored as **failed**, with
the reason. A listing where "completed" sometimes means "probably" is a listing
nobody can use to decide whether they are safe.

`POST /backups` takes:

```text
type              website, database or full
website_id        for a website backup
database_id       for a database backup
destination_id    where it goes
include_databases defaults to true on a website backup
```

It answers **201** with the row, which is `pending` — the work goes through the
job queue, because a backup of a busy host moves an unbounded amount of data and
a request that waited for it would time out first.

`include_databases` defaults to *true* because a site's files restored without
the schema its application expects produce a site that is broken in a more
confusing way than one that is simply gone.

`POST /backups/:id/restore` requires **`confirm` to be the backup's own id**.
CLAUDE.md section 18 requires a restore to be confirmed explicitly, and a boolean
would not be one: a `confirm: true` is something a script sets once and forgets.
It answers **202** with the job. A backup that has not been verified is a
**422**: offering to restore an archive the panel could not read back is
offering something it has no reason to believe will work.

`POST /backups/:id/verify` reads the archive back from its destination and
checks it — the whole file against its recorded digest, and every member against
the manifest. It answers **200** with `ok: false` and a reason when the archive
is bad, not an error: the check ran and produced an answer, and an error
envelope has nowhere to put the detail saying which part is wrong.

`DELETE /backups/:id` removes the archive from its destination and then the row.
That order is deliberate: a row removed first would leave an object nothing
knows about, accruing storage charges forever with no way to find it.

`GET /backups/:id/download` is **not implemented**, and docs/PHASE14.md section
12 says why: streaming a multi-gigabyte archive through an API that cannot reach
the destination itself would mean proxying it through the Agent's socket.

---

# 22.1 Backup Destinations

```http
GET    /backup-destinations
POST   /backup-destinations
PATCH  /backup-destinations/:id
DELETE /backup-destinations/:id
POST   /backup-destinations/:id/check
```

A destination is a row of its own rather than fields on a schedule; DATABASE.md
section 26.2 explains the divergence.

A destination **never returns its credential**, only whether one is stored. The
serialised struct has no field for it at all: a field that is only sometimes
cleared is a field that will one day be returned.

`POST /backup-destinations/:id/check` writes a small object, reads it back, and
removes it. It is what turns "somebody typed a bucket name" into "the panel has
written there and read the same bytes out", at the one moment anybody is
watching. It answers **200** with the destination whether or not the check
passed: "we could not reach it, and here is what the storage said" is the
answer, and it belongs on the destination where the page shows it.

Refused with a **422**: a local directory that is relative or contains `..`; an
S3 endpoint carrying a path, a query, or credentials; an S3 endpoint that is
plain http, is not on this machine, and has not been explicitly accepted with
`allow_insecure`; an S3 destination with no secret key; an SFTP destination with
no host key — without one there is no way to tell the intended server from
whatever answers on port 22 — and an SFTP username that would be read as an
option. A name already in use is a **409**, and so is deleting a destination a
schedule still points at.

---

# 23. Backup Schedules

```http
GET    /backup-schedules
POST   /backup-schedules
PATCH  /backup-schedules/:id
DELETE /backup-schedules/:id
POST   /backup-schedules/:id/run
```

A schedule is an hour, a minute and a day in UTC rather than a cron expression,
and the panel's own loop decides when it is due — the same arrangement Phase 21
uses for updates, and for the same two reasons: the privileged half belongs to
the Agent, and a schedule written into a crontab is one the panel can no longer
describe.

`retention_days` and `keep_last` go together, and the second is what makes the
first safe. The count floor is applied *before* the age filter, so a panel that
was off for longer than the retention window comes back and keeps its most
recent backups rather than finding everything expired and deleting all of it.
Only verified backups count towards the floor: keeping three archives the panel
could not read back is keeping nothing.

Retention runs **only after a backup has completed and verified**, which is
where CLAUDE.md section 18's rule lives. A week of failing backups leaves a week
of old ones rather than pruning its way to an empty destination.

`PATCH` cannot change a schedule's type or what it points at. A schedule that
started backing up a different site would leave its own history attached to
backups of something else, and retention would then prune one site's archives on
another's rules.

Deleting a schedule keeps the backups it took. They are the only reason it
existed, and deleting a schedule is a decision about the future.

`backup.create`, `.delete`, `.restore`, `.verify`, and the destination and
schedule changes are all audited.

---

# 24. Firewall

```http
GET /firewall/rules
POST /firewall/rules
PATCH /firewall/rules/:id
DELETE /firewall/rules/:id
POST /firewall/apply
POST /firewall/rollback
```

Firewall API must enforce emergency rollback.

---

# 25. Security

```http
GET   /security/score
GET   /security/findings
POST  /security/scan
PATCH /security/findings/:id
GET   /security/history
```

**As implemented in Phase 15.** Everything needs `security.view`, which
migration 0018 adds and grants to admin and operator. It is deliberately not
`server.view`: a findings list is a list of the ways into this machine, with the
exact port and the exact path, and it is the most sensitive read in the panel.

Reading and accepting share the permission on purpose. Splitting them would mean
the people who can see "we still allow password logins" are not the people who
can record that it is deliberate, and the finding would be re-reported forever
by a panel nobody can quiet.

`GET /security/score` answers with the findings, the accepted risks and the
per-scanner outcomes as well as the number. The score alone would be a number
with no way to act on it, and a page that made two calls would show the number
first and the reasons a moment later — which is the wrong order to read them in.

**The field that carries the phase is `checks_run` against `checks_total`.** A
scanner that could not answer contributes neither a finding nor a clean bill of
health, so a score is never returned without the number of checks behind it. A
100 from two checks is not a 100, and `complete: false` says so; the `summary`
then reads "incomplete rather than reassuring" and names the checks that could
not run. TASKS.md warns about exactly this in the phase's dependency note — a
score computed from blanks looks identical to a good one.

A host that has **never been scanned** gets `last_scan: null` and a grade of
`unknown` rather than a zero. A host nobody has looked at is not a host with
problems and it is not a host without them, and a number there would make one of
those up.

`POST /security/scan` runs all seven scanners. It is a POST because it is not
free: it walks a filesystem and reaches the Agent seven times, and a GET that did
that would be re-run by every retry and every prefetch. It is synchronous rather
than a job, because a scan reads and changes nothing — there is nothing to
reconcile if it fails halfway — and an operator pressing "scan" wants the answer
rather than a job to follow. A second scan while one is running is a **409**.

`PATCH /security/findings/:id` takes `status` of `accepted` or `open`, and
`reason`.

**`status: "resolved"` is refused with a 422.** Whether a weakness still exists
is the scanner's to decide, and a panel where a person can mark an open port as
closed is a panel that will one day say an open port is closed. A finding is
resolved when a scan no longer finds it.

Accepting requires a reason of at least a few characters. An acceptance with no
reason is a mute button, and a mute button on a security page is how a real
problem becomes permanent — somebody will read it in a year and needs to know it
was a decision rather than an oversight. The severity at the moment of
acceptance is stored, and **a rescan that finds the same thing worse re-opens
it**: accepting "SSH listens on port 22" is not accepting "SSH permits root
login with a password".

Accepted findings cost no score and are never hidden: the count sits beside the
score and they have their own section in the response.

`GET /security/findings` filters by `status`, `scanner` and `severity`. An
unknown scanner or severity is a **422** rather than an empty list, because an
empty list reads as "nothing wrong".

`security.finding.accept` and `.reopen` are audited. Running a scan is not: it
changes nothing, and a scheduled one would drown the log it is meant to make
readable.

---

# 26. SSH

```http
GET    /security/ssh
PATCH  /security/ssh
GET    /security/ssh/keys?account=NAME
POST   /security/ssh/keys
DELETE /security/ssh/keys/:fingerprint?account=NAME
```

**As implemented in Phase 17.** Reading needs `server.view`; every change needs
`server.manage`.

`GET /security/ssh` returns the **effective** configuration — what `sshd -T`
resolves, not what is in the file, because a directive that is commented out is
still in force at a default that differs between versions — along with the
accounts that can hold keys and the recommendations.

`PATCH /security/ssh` takes any of:

```text
port                      1-65535
root_login                yes | no | prohibit-password | forced-commands-only
password_authentication   bool
pubkey_authentication     bool
permit_empty_passwords    bool
x11_forwarding            bool
max_auth_tries            1-100
```

An omitted field is left alone; a body that asks for nothing is a **422**. The
change is written to `/etc/ssh/sshd_config.d/10-jothost.conf`, validated with
`sshd -t` against the whole configuration before it is installed, and read back
afterwards — a change the server does not adopt restores the backup and returns
a **409** naming the directive.

Four changes are **refused with a 409**, because they would lock the operator out
of the machine and the panel can tell in advance:

- password authentication off while no account has an authorised key
- public key authentication off while passwords are already off
- a port the firewall would not admit: the panel does not open it as a side
  effect, so the message names the rule to add first
- `root_login: no` where root is the only account that can log in

The message is the point of these, and is meant to be shown to the operator
verbatim.

A host whose `sshd_config` has no `Include` for the drop-in directory reports
`managed: false` with the reason, and every change is refused: writing a file
nothing reads would report success and change nothing.

`GET /security/ssh/keys` lists one account's authorised keys, and `POST` adds
one. The account must be one the host offers — a real login shell, or root — and
is matched against that list rather than turned into a path; anything else is a
**404**. A key is refused unless its declared type matches the type inside the
blob, and options in front of a key (`command="..."` and the rest) are refused
outright: the panel writes plain keys.

`DELETE /security/ssh/keys/:fingerprint` withdraws one. The `:id` of the original
sketch is the key's SHA256 fingerprint, which is the only identifier that
survives another key being removed; it is URL-encoded, and the account is a query
parameter because the same key may be authorised for more than one.

`ssh.change`, `ssh.key.add` and `ssh.key.remove` are audited, refusals included.

---

# 27. Fail2Ban

```http
GET    /security/fail2ban
POST   /security/fail2ban/install
GET    /security/fail2ban/jails
PATCH  /security/fail2ban/jails/:jail
PUT    /security/fail2ban/ignored
GET    /security/fail2ban/banned
POST   /security/fail2ban/ban
POST   /security/fail2ban/unban
```

**As implemented in Phase 18.** Reading needs `server.view`; every change needs
`firewall.manage` — a ban is a firewall rule, and an account that may not open a
port should not be able to close one for everybody either.

`GET /security/fail2ban` reports what the *daemon* is running, not what the panel
last wrote: the counters, the policy in force, and the addresses banned now. A
jail somebody configured by hand is listed with `managed: false` and is not
touched; a jail the panel offers that this host cannot run — because none of the
logs it watches exist — is listed with `available: false` and the reason.

`PATCH /security/fail2ban/jails/:jail` takes any of:

```text
enabled     bool
max_retry   1-100
find_time   10 seconds to a week
ban_time    a minute to a year
```

An omitted field is left alone. The panel writes only policy: `filter` and
`logpath` belong to the distribution, which knows what its own daemons write —
Alpine's sshd jail uses a filter built for BusyBox's syslog prefix, and
overwriting it would produce a jail that matches nothing while reporting itself
enabled. A body carrying `filter` is a **400**.

The change is written to `/etc/fail2ban/jail.d/99-jothost.local`, validated with
`fail2ban-client -t`, reloaded, and then **read back from the daemon**. The name
of that file is load-bearing: fail2ban reads every `.conf` before every `.local`
and the last value of an option wins, so a `10-jothost.conf` loses to the
distribution's own drop-in — silently. A change the daemon does not adopt
restores the backup and returns a **409** naming the numbers it is actually
running.

Refused with a **422**: an address or CIDR block that is not one (host names
included — what gets banned would otherwise depend on DNS), a jail name that is
not one, a ban shorter than the window failures are counted in (the counter never
resets, so the address is banned again the moment it is released), a threshold
nothing would reach, and fail2ban's negative "permanent" ban.

`PUT /security/fail2ban/ignored` sets the addresses no jail may ban. **Loopback
is added whether it was asked for or not**: a host that has banned its own
loopback has broken every local service that talks to another over it.

`POST /security/fail2ban/unban` returns a **404** when the address was not banned
— fail2ban answers "0" and exits zero in that case, which is a success code for a
call that did nothing.

`enable` and `disable` from the original sketch are **409s** pointing at
`POST /api/v1/services/fail2ban/start`. Starting a daemon is the service
manager's job, and two places that start the same thing is how a panel comes to
disagree with itself about whether it is running.

`fail2ban.install`, `.configure`, `.ignore`, `.ban` and `.unban` are audited,
refusals included.

---

# 27.1 FTP

```http
GET    /ftp
POST   /ftp/install
PUT    /ftp/settings
GET    /ftp/users
POST   /ftp/users
PATCH  /ftp/users/:id
DELETE /ftp/users/:id
GET    /ftp/sessions
DELETE /ftp/sessions/:pid
GET    /websites/:id/ftp
```

**As implemented in Phase 7.1.** Reading needs `server.view`; every change needs
`ftp.manage`, which migration 0013 adds. It is its own permission rather than
`website.update`, because an FTP credential reaches a site's files without going
through the panel at all and keeps working after the person holding it stops
being a panel user.

Accounts are **virtual**: they exist in the FTP server's own password file and
nowhere else on the host, and each maps to the system account that owns its
website. An FTP password is therefore never a login to the machine, and an
uploaded file is owned by exactly the account that serves the site.

`GET /ftp` reports what the *host* has, joined with what the panel recorded: the
server's version, whether it is running, which optional modules it has (`mod_tls`
decides whether FTPS can be offered at all), the accounts with the usage the
server has counted for each, who is connected now, and whether the firewall
actually admits the ports FTP needs.

`POST /ftp/users` takes:

```text
website_id   required
username     3-32 characters, starting with a letter
password     optional — omitted, the panel generates one and returns it once
home_subpath relative to the website's document root; empty means the root
access_level "full" or "readonly"
quota_mb     0 for no limit
```

The password is returned **once**, in the create response, and only when the
panel generated it. Nothing stores it: it is written into the server's own hashed
file and the panel does not keep a copy, encrypted or otherwise. "Show me the
password" is answered by setting a new one.

`PATCH /ftp/users/:id` changes the folder, the access level, the quota, whether
the account is suspended, or its password. The name and the website cannot
change: an account confined to a different site is a different account, and
renaming one in the password file is a delete and a create with a password
nobody has.

Every change hands the Agent the **complete** set of accounts and the Agent makes
the host match it, so a delete is performed by the account's absence from that
set. An account the panel has recorded that the host does not have — a rebuilt
machine, a restore — is reported as `missing_on_host` rather than failing the
change: nothing holds its password, so failing would let one unusable account
block every later FTP change permanently.

`DELETE /ftp/sessions/:pid` ends one session. The pid is checked against the live
session list *and* against `/proc` before anything is signalled: pids are reused,
the Agent runs as root, and a signal sent to the wrong one would be delivered
successfully. A pid that is not a current FTP session is a **404**.

Refused with a **422**: a name that could not survive the password file's
colon-separated format, a home directory containing `..` or starting with `/`
(refused rather than trimmed — stripping the slash off `/etc` would silently
give the operator a different directory), a password under 12 characters, a
passive range of fewer than 16 ports (each transfer in progress uses one), and
requiring encryption on a host with no certificate or no TLS module.

A duplicate account name is a **409**: the server's password file has a single
namespace, so two websites cannot each have a "backup" account.

The passive port range is **reported against the firewall, not opened**. A
firewall change is its own deliberate act with its own protocol (CLAUDE.md
section 19), and performing one as a side effect would open ports without the
operator seeing which and without the audit trail recording it. `GET /ftp`
carries `firewall_open` and the reason, and the page says what to open.

`ftp.user.create`, `.update`, `.password`, `.delete`, `ftp.configure`,
`ftp.install` and `ftp.session.disconnect` are audited. The password itself never
appears in an audit record.

# 27.2 System updates

```http
GET  /updates
POST /updates/check
POST /updates/apply
POST /updates/revert
PUT  /updates/settings
GET  /updates/history
```

**As implemented in Phase 21.** Reading needs `server.view` — knowing a host is
behind is not itself a privilege, and hiding it from the people who look after
the sites on it would make the panel worse at the one job this phase has.
Everything that changes the host needs `update.manage`, which migration 0015
adds. It is not `server.manage`: applying an update restarts daemons and can
change the version of PHP a customer's site runs on, which is a different kind
of decision from restarting a service somebody already chose to run.

`GET /updates` is the panel's cached reading, not a live one. Checking refreshes
the host's package index and reaches the network, so asking on every page load
would make the page slow and hammer a distribution's mirrors.

The field that matters most in the response is **`succeeded`**, not the package
list. A check that could not reach the repositories produces an empty list that
is indistinguishable from a host with nothing to do — and both package managers
exit zero either way. `succeeded: false` means the lists are *not known*, and
`reason` says why; `has_check: false` means nobody has looked yet. Neither is
"up to date".

`security_known` says whether this host can tell security updates apart at all.
apt can, from the origin it prints with each candidate; apk cannot, because
Alpine publishes security fixes as ordinary versions. On such a host
`security_count` is not "none" — it is "cannot tell", and the page says so
rather than reporting zero.

`held` is separate from `packages` on purpose. A version-pinned package appears
in a version comparison forever and is deliberately never upgraded, so listing
it as outstanding would show a queue that never empties. The pending list comes
from what the package manager says it will *do* — `apk upgrade --simulate`,
`apt-get -s upgrade` — rather than from a version comparison.

`POST /updates/check` is a POST because it is not free: it refreshes the index.
A GET that did that would be re-run by every retry and every prefetch.

`POST /updates/apply` takes `packages` (empty applies everything outstanding) or
`security_only`. It answers with the **run**, and a run that started and then
failed is answered with the run rather than an error envelope: an upgrade that
failed halfway still moved packages, and the record of which ones is the most
useful thing the caller can be given. The run's `changes` are read back from the
host afterwards rather than taken from the request — a package manager resolves
dependencies, so asking for one package routinely moves several.

`POST /updates/revert` puts one package back, and **is not a rollback**. The
Agent asks the package manager whether that exact version can still be installed
and refuses with a **404** when it cannot, which on a host whose repositories
carry only the current version is the ordinary answer. Neither apk nor apt keeps
what it replaced, so a panel promising to undo an update would be promising
something the host cannot do; see docs/PHASE21.md section 5.

`PUT /updates/settings` sets the automatic policy — `off` (the default),
`security` or `all` — the window it runs in, how often the panel checks, and the
packages it will never apply automatically. The window is a day and a time
rather than a cron expression, and "every day" is `-1`: zero is Sunday, and
confusing the two would quietly turn a nightly schedule into a weekly one. The
exclusion list is the panel's own and does not pin anything on the host.

Refused with a **422**: a package name that would be read as an option (a
leading dash — `--allow-untrusted` is a flag, not a package), a name carrying a
version, an unknown policy, an hour outside the day, a check interval under an
hour or over a week, a security-only apply on a host that cannot identify
security updates, and applying anything at all on the basis of a check that
failed. Two updates at once is a **409**.

`update.apply`, `update.revert` and `update.configure` are audited. A check is
not: reading is not a change, and a six-hourly automatic check would drown the
log it is supposed to make readable.

# 27.3 Monitoring

```http
GET    /monitoring
GET    /monitoring/alerts
POST   /monitoring/alerts/:id/acknowledge

GET    /monitoring/rules
POST   /monitoring/rules
PATCH  /monitoring/rules/:id
DELETE /monitoring/rules/:id

GET    /monitoring/services/:service
```

**As implemented in Phase 19.** Reading needs `server.view`; changing a rule or
acknowledging an alert needs `monitor.manage`, which migration 0016 adds. It is
deliberately not `server.manage`: tuning a threshold that is crying wolf, or
acknowledging a disk alert at three in the morning, cannot change what the
server does — and the person who looks after the sites is exactly who needs to
do both.

These alerts are **not** the dashboard's. The dashboard's are computed from the
reading in front of them and answer "what is wrong now"; these are rows and
answer "what has been wrong, since when, and is it still". Only the second can
be acknowledged, looked back at, or delivered by Phase 20. The two share their
*thresholds* — the dashboard reads them from these rules — so they cannot
disagree about what "nearly full" means.

A rule fires when its condition has held for **`for_seconds`**, not when one
reading crossed a line. That field is the whole phase: a rule that pages for a
backup job briefly filling memory gets muted within a week, and a muted monitor
is worse than none. Zero fires on the first reading, which is right for a
service being down and wrong for almost everything else.

`GET /monitoring` carries the open alerts, the recent ones including resolved,
the rules, what every watched service is doing and for how long, and the counts
— including how many nobody has acknowledged, which is the number that decides
whether to worry.

`POST /monitoring/rules` takes:

```text
name        what an alert from this rule will be called
metric      cpu, memory, disk, swap, load, network_rx, network_tx or service
target      a mount point, or a service key; empty watches every instance
comparison  "above" or "below"
threshold   the number
for_seconds how long the breach must last
severity    "warning" or "critical"
enabled     defaults to true
```

`PATCH` cannot change `metric` or `target`. A rule that watched something else
would be a different rule, and the alerts it had already opened would be
attributed to a condition it never observed — so that is a **422**.

`POST /monitoring/alerts/:id/acknowledge` records that somebody has seen an
alert. **There is no endpoint that resolves one**, and that is deliberate:
whether a condition has cleared is a fact about the machine, and a panel where a
person can mark a full disk as fine is a panel that will one day say a full disk
is fine. Alerts resolve when the monitor next finds the condition gone, when
their rule is disabled, or when what they watched stops being reported.

One alert exists per `(metric, target, severity)` at a time. A second breach
refreshes it — keeping the original `opened_at`, raising `worst`, updating the
message — rather than opening another; without that a flapping disk would
produce hundreds of rows about one filesystem.

Refused with a **422**: a metric this panel cannot measure, a percentage
threshold above 100 (it could never be reached, so accepting it would let
somebody switch an alert off while believing they had set one), a service rule
that names no service, a blank name, and a duration longer than a day. A second
rule on the same metric, target and severity is a **409**.

`monitor.rule.create`, `.update`, `.delete` and `monitor.alert.acknowledge` are
audited: "why did nobody get told the disk was full" has an answer.

**Metric history** (section 5) gains two ranges, `90d` and `1y`, served from the
hourly summaries rather than the raw samples — which is what lets them outlive
the samples' retention. Their points carry a maximum alongside the average,
because a day's average of 40% hides an hour at 99%, and `sample_count`, because
a bucket built from two readings is not the same evidence as one built from a
hundred.

# 31. Deployments

```http
GET    /deployments
POST   /deployments/repositories
GET    /deployments/repositories/:id
DELETE /deployments/repositories/:id
PUT    /deployments/repositories/:id/actions
POST   /deployments/repositories/:id/key
POST   /deployments/repositories/:id/deploy
POST   /deployments/repositories/:id/rollback

GET    /deployments/runs/:id
GET    /deployments/runs/:id/log

POST   /webhooks/deploy/:token          (unauthenticated — see below)
```

**As implemented in Phase 27.** Git deployment is listed in PRD.md as a future
feature and in TASKS.md as Phase 27; it is not in this specification, so
everything here was designed rather than specified. docs/PHASE27.md records why.

Reading needs `deploy.view`. Every change needs `deploy.manage` — and so does
reading a deployment's **log**, which is the narrower half of the split: a build
prints whatever the build printed, which regularly includes a token in a URL or
an environment variable a script echoed. Seeing that a deployment failed is
support work; reading what it said is not.

## The repository URL is the dangerous field

Refused with a **422**: anything but `https://` and the two ssh forms. git's
remote is not an address but a small language, and three of its dialects run
programs — `ext::sh -c ...` is remote code execution spelled as a URL, and a
remote beginning with a hyphen is an *option* rather than a remote. It is an
allowlist rather than a denylist, so a transport added to a future git is
refused without anybody having to remember to refuse it.

Branches and commits are checked the same way and for the same reason: a branch
beginning with a hyphen is an option, `main:refs/heads` is half a refspec,
`HEAD@{1}` is a revision expression, and a commit is hexadecimal or it is not
a commit.

## Deploying

`POST .../deploy` answers **202** with the deployment row. It does not wait: a
clone, a dependency install and a build take minutes, and the row is how the
page follows it.

A second deployment while one is running answers **409**. That is not a fault —
somebody pressed the button twice, or a push arrived mid-build — and the right
answer is to say so rather than queue a second write into the same working tree.

`POST .../rollback` takes a commit and deploys it. It is a deployment rather than
an undo, which is the honest shape: it checks out an older commit and runs the
same steps. It cannot un-run a migration or remove a file a build wrote, and the
panel says so rather than implying otherwise.

## The webhook

`POST /webhooks/deploy/:token` is the only route in this panel that is reachable
without authentication and causes code to run.

**The token is an address, not a credential.** It selects which repository a push
is about so the panel knows which secret to verify against; anybody who can read
a forge's settings page can read it.

**The signature authenticates.** An HMAC-SHA256 over the exact bytes of the body,
compared in constant time. The body is read under a hard cap and verified before
it is parsed. Every way of failing answers **401** with one message — a wrong
signature, no signature, and a token that matches nothing — because telling an
unauthenticated caller which tokens exist is telling them what to attack.

Nothing in the payload is trusted except the branch, and that only to decide
whether this push is one the website deploys. The repository, the commit and the
branch to check out all come from the panel's own record: a verified push is a
*signal that something changed*, not an instruction about what to do.

A push for another branch answers **200** and says nothing was done, which is the
ordinary outcome of pushing to a feature branch rather than a failure. A verified
push with automatic deployment off answers the same way.

---

# 34. Multi-Tenancy

Accounts, the plans sold to them, the subscriptions those become, and signing in
as somebody else.

Reading needs `tenant.view`. Changing needs `tenant.manage`. Impersonation is
behind `tenant.impersonate`, which is separate from both because signing in as a
customer is a different act from provisioning one.

Everything outside the caller's part of the hierarchy answers **404**, including
things that plainly exist: a reseller walking ids must not be able to tell "no
such subscription" from "somebody else's subscription".

## 34.1 Overview

```text
GET /api/v1/tenancy/overview
```

Returns `accounts`, `plans`, `subscriptions`, `host` and `actor` in one call.

`host` says what this machine can enforce, and reports two facts rather than
one:

```text
isolation_available   whether systemd is here to apply a resource limit
isolation_detail      why not, in words, when it is not
placement             whether anything actually runs inside a slice
```

`placement` is false on every host today. A slice with no processes in it is a
limit on nothing, and the panel must not let availability imply otherwise.

## 34.2 Accounts

```text
GET    /api/v1/tenancy/accounts
POST   /api/v1/tenancy/accounts
PATCH  /api/v1/tenancy/accounts/:id
DELETE /api/v1/tenancy/accounts/:id
```

Create takes `username`, `password`, `tier`, and optionally `email`,
`full_name`, `company` and `parent_id`.

`tier` is `admin`, `reseller` or `customer`, and an account is created
**strictly below** its parent — never at the same level. A reseller creating a
reseller is 403. That rule is also what makes the hierarchy acyclic: every edge
runs from a higher tier to a lower one.

Deleting is refused while anything depends on the account: 409 for a reseller
with customers under it, 403 for an account that still owns a subscription.

## 34.3 Plans and add-ons

```text
GET    /api/v1/tenancy/plans
POST   /api/v1/tenancy/plans
GET    /api/v1/tenancy/plans/:id
PUT    /api/v1/tenancy/plans/:id
DELETE /api/v1/tenancy/plans/:id
```

A plan carries `limits`, `enforcement` and `isolation`:

```text
limits.disk_mb, bandwidth_mb, max_websites, max_databases,
       max_mailboxes, max_ftp_users, max_cron_jobs, max_subdomains
enforcement           hard | soft
isolation.cpu_percent, memory_mb, io_weight
```

**Every limit is nullable, and `null` is not `0`.** `null` means unlimited; `0`
means none at all. They are opposite promises and the API keeps them apart in
both directions. On an add-on (`kind: "addon"`) a number is an *increment*, and
`null` means it adds nothing to that dimension — there is nothing for an
increment of infinity to mean.

`PUT` replaces the limits rather than patching them, because a patch could not
distinguish "unset the mailbox limit" from "leave it alone".

A reseller may *sell* the administrator's published plans and may not *edit*
them; editing one answers 403 with that sentence. Deleting a plan a
subscription is on is 409.

## 34.4 Subscriptions

```text
GET    /api/v1/tenancy/subscriptions
POST   /api/v1/tenancy/subscriptions
GET    /api/v1/tenancy/subscriptions/:id
PATCH  /api/v1/tenancy/subscriptions/:id
DELETE /api/v1/tenancy/subscriptions/:id
POST   /api/v1/tenancy/subscriptions/:id/status
POST   /api/v1/tenancy/subscriptions/:id/addons
DELETE /api/v1/tenancy/subscriptions/:id/addons/:planId
POST   /api/v1/tenancy/subscriptions/:id/websites
DELETE /api/v1/tenancy/subscriptions/:id/websites/:websiteId
POST   /api/v1/tenancy/subscriptions/:id/measure
POST   /api/v1/tenancy/subscriptions/:id/isolation
GET    /api/v1/tenancy/subscriptions/:id/quota/:dimension
```

A subscription returns its plan's limits **with add-ons already folded in**,
what it is using, and what the host did with its resource caps:

```text
isolation_state   none | applied | declared | failed
```

`declared` means the limits are written and this host is not applying them.
That is neither success nor failure and it has a value of its own, so a page
cannot show a tick for a cap nobody enforces.

`/status` takes `{"status": "suspended", "reason": "..."}`. A suspended
subscription cannot grow — every quota-guarded creation is refused by name — and
**it does not take the customer's websites offline**.

`/quota/:dimension` answers whether one more would be allowed, for greying out a
button. Asking about `disk` or `bandwidth` is a 400: those are measured on the
host after the fact, so there is no answer to give before it.

## 34.5 Quota enforcement

Six routes are guarded, and the guard sits around the whole router rather than
inside each feature:

```text
POST /api/v1/websites                       websites
POST /api/v1/websites/:id/subdomains        subdomains
POST /api/v1/databases                      databases
POST /api/v1/mail/domains/:id/mailboxes     mailboxes
POST /api/v1/ftp/users                      ftp_users
POST /api/v1/cron                           cron_jobs
```

**The subscription charged is the one that owns the resource, not the one that
belongs to the caller.** A reseller creating a database inside a customer's
website spends the customer's plan; so does an administrator. A guard that
looked at who was asking would let every limit be walked around by having
somebody senior press the button.

`POST /api/v1/websites` is the exception, because a website that does not exist
yet cannot say who owns it. It accepts an optional `subscription_id`, which is
both where the new site lands and whose plan is charged for it.

A hard limit reached answers **409** — the caller has the right to do this and
it is the state of the account that prevents it, which is what a conflict is.
A soft limit lets the request through and sets a header:

```text
X-JotHost-Quota-Warning: this subscription is over its websites limit (2 of 2)
                         and its plan allows it
```

Every refusal is written to the audit log with the dimension, the count and the
limit.

## 34.6 Measured usage

`usage` carries the counted dimensions as plain numbers and the measured ones as
nullable:

```text
websites, databases, mailboxes, ftp_users, cron_jobs, subdomains   integers
disk_bytes, bandwidth_bytes                       null = not measured
measured_at, period_start                         null = never measured
measure_error                                     why, when it failed
```

**`null` is not zero.** A subscription whose disk could not be read is not one
using no disk, and a client rendering `0` would tell a customer they are inside
a quota nobody checked.

Bandwidth is a total for the calendar month named by `period_start`, accumulated
across log rotations. Disk is a level, not a history.

## 34.7 Impersonation

```text
POST   /api/v1/tenancy/impersonation
GET    /api/v1/tenancy/impersonation
DELETE /api/v1/tenancy/impersonation
GET    /api/v1/tenancy/impersonation/history
```

`POST` takes `{"user_id": "...", "reason": "..."}` and returns a token pair for
that account. The subject must be strictly below the caller.

The session it issues is an ordinary one with two differences, both refusals: it
does not carry `tenant.view`, `tenant.manage`, `tenant.impersonate` or
`user.manage`, and it lasts at most an hour. So an impersonated session cannot
impersonate — a second hop's record would name the first hop's subject as the
actor — and cannot change the account's password.

`DELETE` needs no permission, deliberately: a session stripped of
`tenant.impersonate` still has to be able to stop being one. It takes the
session from the caller's own claims rather than from a body, because a body
would make it an endpoint for logging somebody else out. It revokes the session
*and* drops its access tokens, so stopping stops it now rather than at the end
of a token's life.

`GET` says whose account this session is using and who opened it, so a banner
can name both.

---

# 30. Mail

```http
GET    /mail
PUT    /mail/settings
POST   /mail/install

GET    /mail/domains
POST   /mail/domains
PATCH  /mail/domains/:id
DELETE /mail/domains/:id
POST   /mail/domains/:id/dkim

GET    /mail/domains/:id/mailboxes
POST   /mail/domains/:id/mailboxes
PATCH  /mail/mailboxes/:id
DELETE /mail/mailboxes/:id
PUT    /mail/mailboxes/:id/password
PUT    /mail/mailboxes/:id/autoresponder
DELETE /mail/mailboxes/:id/autoresponder

GET    /mail/domains/:id/aliases
POST   /mail/domains/:id/aliases
DELETE /mail/aliases/:id

POST   /mail/webmail
DELETE /mail/webmail
```

**As implemented in Phase 26.** Mail is not in this specification or in
DATABASE.md — PRD.md lists it as a future feature and TASKS.md as Phase 26 —
so everything here was designed rather than specified. docs/PHASE26.md records
why.

Reading needs `mail.view`; every change needs `mail.manage`. Two permissions
rather than one, and the split is the point: seeing that a mailbox exists and is
40% full is support work, while being able to set its password is being able to
read every message in it, silently, with no trace the owner will ever see.
`mail.manage` is granted to admin and operator; `mail.view` additionally to
viewer.

## What GET /mail is really for

It carries the settings, the host's live status, and every domain — and against
each domain, **both** halves of a comparison this panel makes nowhere else:

  * what the panel has configured (a signing key, an SPF policy, a DMARC policy),
  * and what DNS is actually serving.

That is the whole point of the phase. A mail server with a missing SPF record
sends mail perfectly and has it filed as spam; a domain whose DKIM key was never
published signs every message with a key nobody can fetch, which is worse than
not signing because a signature that fails to verify looks like a forgery. The
reply names each disagreement in words, in `problems`.

Where this host does not serve the domain's zone the reply says **it cannot
check**, rather than reporting the records as missing. Those are different facts:
one is a fault, the other is a limit of what this host can see.

`status` additionally carries `open_relay`, which is the answer to the only
question that can take every customer on the host off the internet at once. It is
obtained by *connecting* — from this host's own routable address, because
`mynetworks` contains the loopback and a probe from there is permitted by design.
`checked: false` means the panel could not ask, which is reported as such rather
than as a pass.

## Mailboxes

A mailbox is created with a password of at least 12 characters — longer than
this panel's own minimum for a login, because it is exposed to the whole internet
on ports the panel does not rate-limit and is typed into devices that remember it
forever.

The password is hashed at this boundary and the plaintext goes no further: not
into the database, not over the socket to the privileged Agent, not into a log.
**No reply ever contains a password hash.**

`PUT /mail/mailboxes/:id/password` answers **204 with no body**. There is nothing
useful to return, and returning the mailbox would put its row — and one day, by
accident, its hash — in the reply to a request that carried a password. It is
audited by address: this is the one action in the panel that grants the ability to
read somebody's correspondence without them ever seeing a trace.

Deleting a mailbox or a domain **leaves the messages on the disk**. Deleting a row
can be undone by recreating it; deleting somebody's correspondence cannot, and a
control panel should not do the irreversible half as a side effect of the
reversible one.

## Domains

Creating a domain generates a signing key as part of the same act, because a
domain whose key has to be *remembered* about is one whose mail goes out unsigned
for however long it takes somebody to remember. The reply carries the public half;
the private half is written to the host and is stored nowhere else.

`POST /mail/domains/:id/dkim` rotates. The new key is generated, recorded and
published **before** the old one is deleted, so there is no moment when the domain
has no key at all.

Refused with a **422**: an SPF or DMARC policy outside the closed sets (there is
deliberately no way to publish "+all", which is worse than publishing nothing); a
mail hostname that is not fully qualified, because a host greeting the world as
localhost has its mail refused by most of it; a local part outside `[a-z0-9._-]`,
because it is about to be written into two colon-delimited, line-oriented files; a
forwarder pointing at itself; an autoresponder with no message, or one that would
reply to every message.

Every change reconciles the whole host afterwards — desired state, not a delta,
as the FTP and DNS phases do — so a reply that succeeds means the daemons have
the change, not that a row was written.

---

# 29. Notifications

```http
GET    /notifications
GET    /notifications/deliveries

GET    /notification-channels
POST   /notification-channels
PATCH  /notification-channels/:id
DELETE /notification-channels/:id
POST   /notification-channels/:id/test
```

**As implemented in Phase 20.** Notifications are not in this specification or
in DATABASE.md; they are in TASKS.md and in PRD.md's alert list, so everything
here was designed rather than specified. docs/PHASE20.md records why.

Everything needs `notification.manage`, which migration 0019 adds and grants to
**admin only**. It is not `server.manage`: a channel holds an SMTP password or a
bot token, and changing where the panel sends its alerts is how somebody quietly
stops them arriving. Unlike `monitor.manage`, operators do not get it —
silencing a false alarm at three in the morning is their job, and silencing
every alert on the machine is not.

## What this endpoint set is really for

`GET /notifications/deliveries` is the most important read in the phase, because
of the one failure a notification system cannot report on its own terms: **when
delivery is broken, the message saying so does not arrive.** The operator's
experience is silence, which is exactly what a healthy machine produces. So
every attempt is a row, and this is the only place a failed notification is
visible.

`GET /notifications` carries the channels, the recent deliveries, the recent
events and the counts — including `broken_channels` and `untested_channels`,
which the page leads with. A channel that has **never** succeeded is reported as
untested rather than as working: "we have never got a message through this" and
"this is fine" are different facts.

## Channels

Three kinds: `email`, `telegram`, `line`. **There is deliberately no webhook
kind and no free-form URL anywhere in this phase.** A notification channel that
accepts a URL is a request forger sitting inside the panel, pointed at whatever
an admin account can be talked into typing — from a machine that sits on the
private network beside every site it hosts. Telegram and LINE each publish one
API host and those are compiled in. Email is the exception that proves the rule:
its host is the operator's own mail server.

A channel takes a **severity floor** (`critical`, `high`, `warning`, `info`)
rather than a set of checkboxes, because the question an operator has is "how
bad does it have to be before you wake me" — and checkboxes invite the answer
"all of them" followed by a filter rule in their mail client. An empty `kinds`
list means every kind, which is the right default: a channel that silently
excluded a category would be one somebody believes is watching something it is
not.

A channel **never returns its credential**. The serialised struct has no field
for one.

`POST /notification-channels/:id/test` sends a real message now, while somebody
is watching. It is the same idea as Phase 14's destination check and exists for
the same reason: a channel nobody has ever delivered through looks like
protection and is not. It answers **200 with the channel** whether or not the
message got through — "we could not reach it, and here is what the server
said" is the answer, and it belongs on the channel where the page shows it.

Refused with a **422**: a channel kind this panel does not send through; plain
SMTP to a server that is not on this machine without an explicit
`allow_insecure`; a **password over an unencrypted connection under any
circumstances**, which is a different and indefensible concession; an address or
subject carrying a line break, which is how a message gains a header its author
did not write; and a severity or event kind outside the closed sets, so a typo
is an error rather than a filter that silently matches nothing.

`notification.channel.create`, `.update`, `.delete` and `.test` are audited: the
audit trail is the only record of a channel being changed that does not depend
on the channel itself.

## What is sent

Five kinds — `alert.opened`, `alert.resolved`, `backup.failed`,
`ssl.expiring`, `security.finding` — and the test of what belongs is not "is it
interesting" but "would somebody want to be interrupted by it". A successful
backup is not news. A channel that reported those is a channel somebody mutes,
and a muted channel does not deliver the one message that mattered.

**One event per thing that happened**, enforced by a unique index on a dedupe
key. A disk sitting above its threshold for a week is one email or ten thousand,
and which one it is depends on a constraint rather than on every caller
remembering.

---

# 28. Jobs

```http
GET /jobs
GET /jobs/:id
POST /jobs/:id/cancel
```

**As implemented in Phase 4.** All three REST endpoints exist. `GET /jobs`
filters on `status`, `resource_type`, `resource_id`, and `limit`. Cancellation
applies only to a job that has not started: once dispatched, the Agent is
changing the host, and reporting it cancelled would be a claim the panel cannot
make good on — that returns **409**.

WebSocket:

```text
/ws/jobs/:id
```

**Not implemented.** Clients poll `GET /jobs/:id`. Provisioning takes seconds
and the UI polls only while work is in flight, so a WebSocket would add a
connection lifecycle, an auth handshake, and a reconnect path for a latency
improvement nobody would notice. See docs/PHASE4.md §3.3.

Events:

```json
{
  "event": "job.progress",
  "job_id": "123",
  "progress": 70,
  "message": "Configuring Nginx"
}
```

---

# 29. Audit Logs

```http
GET /audit-logs
GET /audit-logs/:id
```

No DELETE endpoint.

---

# 30. System Updates

```http
GET /system/updates
POST /system/updates/check
POST /system/updates/install
```

Initially these endpoints may be read-only except for checking updates.

---

# 31. API Security

Every protected endpoint must perform:

```text
Authentication
Authorization
Validation
Rate Limiting
Audit where required
```

Never trust:

```text
user ID
server ID
website ID
file path
domain
port
command
```

from the frontend.

---

# 32. HTTP Status Codes

Use:

```text
200 OK
201 Created
202 Accepted
204 No Content
400 Bad Request
401 Unauthorized
403 Forbidden
404 Not Found
409 Conflict
422 Unprocessable Entity
429 Too Many Requests
500 Internal Server Error
502 Bad Gateway
503 Service Unavailable
```

---

# 33. API Versioning

Current:

```text
/api/v1
```

Breaking changes require:

```text
/api/v2
```

Do not silently break v1 clients.