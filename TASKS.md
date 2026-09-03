# JotHost Panel — Development Tasks

## Status Legend

```text
[ ] TODO
[-] IN PROGRESS
[x] DONE
[!] BLOCKED
```

---

## Build Order

Phases are built in the order below, which is **not** their numeric order. A
phase number is an identity — it names a document, a migration, a test script —
and renumbering would make every existing reference wrong. The sequence is what
changed.

The rule the order follows: **nothing is built before the thing it reaches
for.** Each phase's own section carries a `Depends on` line saying what it
needs, and the phases already built carry `Blocks` lines saying what waited for
them. Where an earlier phase had to defer part of itself, the phase that pays
it off is named at that point.

Done, in the order they were built:

```text
0  Foundation
1  Authentication
2  Host Agent
3  Dashboard
4  Website Manager
5  PHP Manager
6  SSL
7  File Manager
7.5 Code Editor
8  Database Manager
9  Node.js Manager
4.1 Subdomain Manager
4.5 Apache Hybrid Engine
12  Service Manager
16  Firewall
11  Logs
10  Cron
17  SSH Security
18  Fail2Ban
7.1 FTP Manager
13  DNS & Local Name Server
21  System Updates
19  Monitoring
14  Backup
```

Remaining, in build order:

```text
 1.  15   Security Center        — scans 16, 17, 21, 6, 7: follows all of them
 2.  20   Notifications          — delivers alerts raised by 19, 14, 15, 6, 12
 3.  26   Mail Server Ecosystem  — DKIM/SPF/DMARC need 13; ports need 16
 4.  27   Git & Webhook Actions  — deployment logs need 11
 5.  22   Multi-Tenant           — quota dimensions must exist first, mail included
 6.  23   Production Installer   — installs everything, so everything must exist
 7.  24   Production Hardening   — tests the finished system
 8.  25   Release                — last by definition
```

Why the significant moves, in one line each:

- **12 to the front.** The gap has already been paid for twice: Phase 9 built
  `agent/internal/services/control.go` as "the smallest Phase 12 dependency",
  and Phase 4.5 hand-rolled `httpd -k start/graceful` because no service layer
  existed to ask.
- **16 second.** FTP's passive range, DNS on 53 and mail on 25/465/587/993 all
  need it, Fail2Ban's bans *are* firewall rules, and its rollback requirement
  (CLAUDE.md section 19) is better built once than retrofitted under four
  callers.
- **11 before 10.** The log viewer should exist before the producers, so each
  new one registers a source as it lands rather than the viewer being extended
  five times.
- **15 and 20 late.** Both are aggregators over other phases' work; built early,
  half their content would be dead fields.
- **26 before 22.** Multi-tenant's plan builder counts mailboxes among its quota
  dimensions.

---

# PHASE 0 — Project Foundation

**Status: COMPLETE** — see [docs/PHASE0.md](docs/PHASE0.md) for scope, decisions, and known limitations.

## 0.1 Repository

- [x] Initialize Git repository
- [x] Create monorepo structure
- [x] Create README
- [x] Create CLAUDE.md
- [x] Create PRD.md
- [x] Create ARCHITECTURE.md
- [x] Create DATABASE.md
- [x] Create API_SPEC.md
- [x] Create TASKS.md

## 0.2 Docker

- [x] Docker Compose
- [x] Frontend container
- [x] API container
- [x] Agent container
- [x] PostgreSQL
- [x] Redis
- [x] Nginx
- [x] Development environment
- [x] Health checks

## 0.3 Go

- [x] Initialize API module
- [x] Initialize Agent module
- [x] Configuration package
- [x] Logging
- [x] Error handling
- [x] Request ID
- [x] Graceful shutdown

## 0.4 React

- [x] Vite
- [x] TypeScript
- [x] Tailwind
- [x] Router
- [x] Query client
- [x] Base layout
- [x] Sidebar
- [x] Header
- [x] Error boundary

## 0.5 Testing

- [x] Go unit test framework
- [x] API integration tests
- [x] React test setup
- [x] Docker test profile
- [x] CI pipeline

---

## 0.6 Acceptance

```text
docker compose up -d --build  -> all six services healthy
GET /healthz                  -> 200, success envelope, request_id
GET /readyz                   -> 200, postgres/redis/agent all "up"
API -> Agent over unix socket -> agent.ping succeeds
Agent TCP exposure            -> none
make test                     -> 84 unit tests pass (67 Go, 17 frontend)
make docker-test-integration  -> 23 integration checks pass
make lint                     -> clean (go vet, gofmt, eslint)
frontend build                -> succeeds
```

---

# PHASE 1 — Authentication

**Status: COMPLETE** — see [docs/PHASE1.md](docs/PHASE1.md) for scope, decisions, and known limitations.

- [x] User model
- [x] User repository
- [x] Password hashing
- [x] Login API
- [x] Logout API
- [x] Refresh token
- [x] Session management
- [x] RBAC
- [x] Permissions
- [x] Admin role
- [x] TOTP 2FA
- [x] Login rate limit
- [x] Audit logging

Additionally required by the above:

- [x] Migration runner (up / down / status)
- [x] Schema for users, roles, permissions, sessions, 2FA, audit logs
- [x] PostgreSQL and Redis connection pools
- [x] Secrets-at-rest encryption (AES-256-GCM)
- [x] `create-admin` bootstrap command
- [x] Login, 2FA, and account-security UI

Acceptance:

```text
Admin can securely login.                     verified: browser + integration
Unauthorized users cannot access protected API.  verified: 401 on every guarded route
2FA works.                                    verified: enrol, login, disable in browser
```

---

# PHASE 2 — Host Agent

**Status: COMPLETE** — see [docs/PHASE2.md](docs/PHASE2.md) for scope, decisions, and known limitations.

- [x] Agent daemon
- [x] Unix socket
- [x] Agent authentication
- [x] Operation protocol
- [x] Operation allowlist
- [x] Operation validation
- [x] CPU collector
- [x] RAM collector
- [x] Disk collector
- [x] Network collector
- [x] Process collector
- [x] Service collector
- [x] Job execution
- [x] Agent logging
- [x] Agent audit

Additionally required by the above:

- [x] Allowlisted command execution (no shell, ever)
- [x] Path validation (traversal and symlink escape)
- [x] Typed payload decoding with unknown-field rejection
- [x] Typed API client wrappers for every operation
- [x] `jothost-agent -call` operator diagnostics

Security tests:

- [x] Path traversal
- [x] Command injection
- [x] Invalid operation
- [x] Privilege escalation
- [x] Timeout
- [x] Resource abuse

Acceptance:

```text
Agent reads real host metrics.        verified: 40 integration checks on a live host
Only allowlisted operations run.      verified: registry/allowlist cross-checked both ways
Callers are authenticated.            verified: peer credentials + token, both enforced
Every operation is audited.           verified: append-only trail names the calling process
```

---

# PHASE 3 — Dashboard

**Status: COMPLETE** — see [docs/PHASE3.md](docs/PHASE3.md) for scope, decisions, and known limitations.

- [x] Dashboard API
- [x] CPU widget
- [x] RAM widget
- [x] Disk widget
- [x] Network widget
- [x] Service status
- [x] Server information
- [x] Alert widget
- [x] Metric graph

Additionally required by the above:

- [x] `servers` and `system_metrics` schema
- [x] Local server registration from the Agent
- [x] Metric sampler with retention pruning
- [x] Bucketed metric history API (1h / 24h / 7d / 30d)
- [x] Server read endpoints

Acceptance:

```text
Dashboard shows live host metrics.     verified: browser + 42 integration checks
Widgets degrade independently.         verified: unreachable agent still renders the page
History is sampled and graphed.        verified: sampler writes, chart plots, ranges switch
Only authorised callers see it.        verified: 401 anonymous, 403 without server.view
```

---

# PHASE 4 — Website Manager

**Status: COMPLETE** — see [docs/PHASE4.md](docs/PHASE4.md) for scope, decisions, and known limitations.

- [x] Website database
- [x] Domain database
- [x] Create website API
- [x] Delete website API
- [x] Update website API
- [x] Nginx provider
- [x] Filesystem provider
- [x] Site user creation
- [x] Permissions
- [x] Nginx template
- [x] Nginx validation
- [x] Nginx reload
- [x] Website UI
- [x] Domain UI
- [x] Website status
- [x] Website logs

Additionally required by the above:

- [x] `websites`, `domains`, and `jobs` schema
- [x] Durable job queue and worker (`FOR UPDATE SKIP LOCKED`)
- [x] Job API and progress reporting
- [x] Shared domain and system-user validation (`shared/validate`)
- [x] nginx running in the agent container so sites are actually served

Integration test:

```text
Create website
→ request HTTP
→ receive response
```

Acceptance:

```text
Create website → HTTP → response.     verified: 41 integration checks, live nginx
Sites are isolated from each other.    verified: cross-site read denied, dotfiles 403
A site is never claimed to work early. verified: "creating" until the agent confirms
Only authorised callers may change it. verified: 401 anonymous, 403 without permission
```

---

# PHASE 4.1 — Subdomain Manager (Plesk Style)

**Status: COMPLETE** — see [docs/PHASE4.1.md](docs/PHASE4.1.md) for scope,
decisions, and known limitations.

- [x] Subdomain database schema & parent website relationship
- [x] Document Root mapping options:
Nested path (e.g., /var/www/example.com/sub.example.com)
Isolated path (e.g., /var/www/sub.example.com)

- [x] Dedicated Nginx vhost generation for subdomains
- [x] PHP-FPM Pool strategy selector:
Inherit parent site pool
Dedicated isolated PHP-FPM pool & socket per subdomain

- [x] Dedicated SSL certificate issuance (Certbot / Let's Encrypt) per subdomain
- [x] Wildcard subdomain support (*.example.com) & Catch-all routing
- [x] Isolated Access & Error log files per subdomain
- [x] User permission model (Parent site user ownership vs Dedicated system user)
- [x] Subdomain API endpoints & UI management tab (Plesk-style collapsible site card)

Two items belong to phases that do not exist yet, and building them here would
mean building those phases. They are deferred rather than dropped:

- [ ] Apache VirtualHost generation — **Phase 4.5**, which is where the Apache
  provider is. This phase writes the nginx vhost, which is what the host runs
  today; when the hybrid engine lands, a subdomain is a vhost like any other.
- [ ] Automatic DNS record injection (A / AAAA / CNAME) into the parent's zone —
  **Phase 13**, which is where a zone exists at all. A subdomain needs no DNS
  from this panel to work: it is reached through whatever already resolves the
  parent.

A subdomain is a website row with a parent, not a separate table, which is why
SSL, PHP, files, databases and Node apply to one unchanged. See docs/PHASE4.1.md
section 2.

Additionally required by the above:

- [x] One place that assembles a site's complete vhost state, so a rewrite no
  longer turns PHP, HTTPS or a reverse proxy off as a side effect of an
  unrelated change (docs/PHASE4.1.md section 4)
- [x] Wildcard names accepted as an nginx server_name, and kept out of every
  filename
- [x] PHP refused on a site served by a Node application

Test:

```text
Nested subdomain
Isolated subdomain with its own account
Wildcard catch-all
```

# PHASE 4.5 — Apache Hybrid Engine (Nginx + Apache)

**Status: COMPLETE** — see [docs/PHASE4.5.md](docs/PHASE4.5.md) for scope,
decisions, and known limitations.

- [x] Apache2 / httpd detection & provider
- [x] Nginx reverse proxy template (proxy_pass to Apache backend)
- [x] Apache mpm_event & PHP-FPM integration
- [x] Apache VirtualHost generator
- [x] Webserver mode switcher (Nginx Standalone vs Nginx + Apache Hybrid)
- [x] .htaccess support and custom rewrite rules handling
- [x] Real IP restoration via mod_remoteip
- [x] Backend port assignment and validation engine

This also closes the Apache half of Phase 4.1's deferred work: a subdomain is a
website, so it gets an Apache virtual host like any other site.

nginx proxies *everything* to Apache rather than serving static files itself.
Letting nginx answer first is how a `.htaccess` rule gets bypassed — the file
is read by only one of the two servers. See docs/PHASE4.5.md §2.2.

Additionally required by the above:

- [x] `servers.webserver_mode` and `websites.apache_port` schema
- [x] A busy Agent defers a job rather than failing it. A mode switch queues one
  job per website at once, and the Agent runs a bounded number: without this,
  switching arrangement on any host with more than a handful of sites reported
  half of them broken (docs/PHASE4.5.md §3)
- [x] Two-way port checking between Apache backends and Node applications

Test:

```text
A site served by nginx, then by Apache, then by nginx again
.htaccess deny and redirect
PHP through mod_proxy_fcgi
```

# PHASE 5 — PHP Manager

**Status: COMPLETE** — see [docs/PHASE5.md](docs/PHASE5.md) for scope, decisions, and known limitations.

- [x] PHP version detection
- [x] PHP version database
- [x] PHP installer
- [x] PHP-FPM detection
- [x] PHP-FPM provider
- [x] PHP pool creation
- [x] PHP pool deletion
- [x] PHP configuration
- [x] PHP extensions
- [x] OPcache
- [x] Website PHP selection
- [x] PHP UI

Additionally required by the above:

- [x] `php_versions` and `php_pools` schema
- [x] FastCGI in the nginx vhost, with the path-info execution guard
- [x] Per-site pool isolation: own account, own socket, own session directory
- [x] Version-specific socket paths, so a version switch has no downtime
- [x] Pool cleanup on website deletion

Test:

```text
PHP 8.4 website
PHP 8.3 website
PHP 8.2 website
```

Acceptance:

```text
A website per version, each running it.  verified: 51 integration checks, live FPM
An upload is never executed as code.     verified: path-info attack returns 404
A site cannot read its neighbour.        verified: open_basedir denies, per-site accounts
Switching versions keeps the site up.    verified: version-specific sockets, no 502
Only authorised callers may change it.   verified: 401 anonymous, 403 without permission
```

---

# PHASE 6 — SSL

**Status: COMPLETE** — see [docs/PHASE6.md](docs/PHASE6.md) for scope, decisions, and known limitations.

- [x] Certbot provider
- [x] Certificate detection
- [x] Issue certificate
- [x] Renew certificate
- [x] Revoke certificate
- [x] Auto renewal
- [x] HTTPS redirect
- [x] SSL dashboard
- [x] Expiration alerts

Additionally required by the above:

- [x] `ssl_certificates` schema
- [x] A self-signed provider, so the serving path is testable without public DNS
- [x] HTTPS vhost with modern TLS, HSTS, and forward secrecy only
- [x] An ACME challenge path that survives the HTTPS redirect
- [x] Private keys written root-only, verified rather than assumed
- [x] Certificate cleanup when a website is deleted

Acceptance:

```text
An issued site completes a TLS handshake.  verified: 44 integration checks, live nginx
HTTP redirects to HTTPS.                   verified: 301 to the secure origin
Renewal keeps working after the redirect.  verified: challenge served over plain HTTP
A private key is readable only by root.    verified: mode 600, root-owned, outside /var/www
Weak TLS is refused.                       verified: TLS 1.1 rejected, 1.3 negotiated
```

---

# PHASE 7 — File Manager

**Status: COMPLETE** — see [docs/PHASE7.md](docs/PHASE7.md) for scope, decisions, and known limitations.

- [x] Directory browser
- [x] File browser
- [x] Upload
- [x] Download
- [x] Rename
- [x] Delete
- [x] Copy
- [x] Move
- [x] New file
- [x] New folder
- [x] ZIP
- [x] UnZIP
- [x] Permissions
- [x] Search

Security:

- [x] Path traversal test
- [x] Symlink escape test
- [x] Permission test
- [x] Unauthorized access test

---

# PHASE 7.1 — FTP Manager (Plesk Style)

**Status: COMPLETE** — see [docs/PHASE7.1.md](docs/PHASE7.1.md) for scope,
decisions, and known limitations.

**Build order: 7 of 17.** Depends on 12 (start and reload the daemon), 16 (the
passive port range has to be opened), 11 (connection and transfer logs), and 6
(FTPS binds a certificate the SSL phase already issues).

- [x] ProFTPD provider & daemon configuration engine — Pure-FTPd was rejected
  after testing: Alpine's build has no TLS at all, which would put every FTP
  password on the wire in clear text
- [x] FTP user database schema (virtual FTP users tied to system accounts)
- [x] Additional FTP users per website
- [x] Strict directory chroot, proved by a real client being refused the escape
- [x] Read-only / full access per user
- [x] FTPS enforcement & certificate binding — the certificate is read from the
  SSL phase's record rather than copied, so a renewal is picked up
- [x] Passive port range configuration, with the firewall **reported** rather
  than changed as a side effect (see docs/PHASE7.1.md section 6)
- [x] Custom home directory mapping, created and given to the website's account
- [x] FTP quota enforcement, proved by an upload that exceeds it being refused
- [x] Active session monitor & disconnect API
- [x] Connection & transfer logs — contributed to Phase 11's catalogue as two
  sources, with the paths written by the panel rather than guessed at
- [x] FTP tab in the Website Manager, where accounts are created

Additionally required by the above:

- [x] A `ftp.manage` permission of its own: an FTP credential reaches a site's
  files without going through the panel, and keeps working after the person
  holding it stops being a panel user
- [x] No password column anywhere in the panel. One is written to the server's
  hashed file and returned to the operator once
- [x] Module detection that accounts for DSO modules. `proftpd -l` lists only
  what is compiled in, and an `<IfModule>` block for an absent module is skipped
  in silence — so a panel that assumed mod_tls would report FTPS was on while
  serving plain FTP
- [x] A declarative reconcile: the panel hands the Agent the complete set of
  accounts and the Agent makes the host match, so nothing can drift

---

# PHASE 7.5 — Code Editor

**Status: COMPLETE** — see [docs/PHASE7.5.md](docs/PHASE7.5.md) for scope, decisions, and known limitations.

- [x] Monaco integration
- [x] File tree
- [x] Open file
- [x] Save file
- [x] Tabs
- [x] Search
- [x] Replace
- [x] Syntax highlighting
- [x] Auto save
- [x] Large file protection

---

# PHASE 8 — Database Manager

- [x] MariaDB provider
- [x] MySQL provider
- [x] PostgreSQL provider
- [x] Database creation
- [x] Database deletion
- [x] User creation
- [x] User deletion
- [x] Password change
- [x] Permissions
- [x] Database size
- [x] Database UI
- [x] PHPmyadmin

MariaDB and MySQL share one provider: the clients speak the same protocol and
the DDL the panel issues is identical. They stay distinct **engine values**,
because an operator needs to know which server their data is actually in, and
the provider reports whichever the host answered as.

Grants are a fixed set of three levels rather than a privilege string. See
docs/PHASE8.md for what each one means on each engine, and for the defects the
live checks found.

phpMyAdmin is installed on request and never by default, from the host's own
package manager, under its own system account and FPM pool, on one name the
operator chooses. Its configuration holds no credentials: a visitor signs in
with a database account the panel created, and the server's root account cannot
sign in at all.

The Databases screen follows the arrangement of Plesk's own: two tabs, a list
whose rows expand in place into that database's tools, and the site each one
belongs to shown and editable in the list itself.

---

# PHASE 9 — Node.js Manager

- [x] Node version detection
- [x] Node version installation
- [x] Node application model
- [x] Application creation
- [x] Application deletion
- [x] Environment variables
- [x] systemd service generation
- [x] Start
- [x] Stop
- [x] Restart
- [x] Logs
- [x] Port validation
- [x] Reverse proxy
- [x] Node UI

Test:

```text
Express
```

The integration suite deploys a real Express application and checks that the
domain reaches it through nginx, under the website's own account, with the
environment the panel gave it.

NestJS and Nuxt are **not** covered, and the reason is worth stating rather than
quietly dropping: both expect a build step, and this phase installs dependencies
and starts an entry point without running one. A built NestJS or Nuxt
application is an entry point like any other and runs here unchanged; building
it is what the panel does not do. See docs/PHASE9.md section 6.

An application runs under systemd where the host has it and under the Agent's
own supervisor where it does not, and answers the same operations either way.
The unit carries the confinement — NoNewPrivileges, ProtectSystem=strict,
PrivateTmp, a bounded restart limit — which is the reason to prefer it.

---

# PHASE 10 — Cron

**Status: COMPLETE** — see [docs/PHASE10.md](docs/PHASE10.md) for scope,
decisions, and known limitations.

**Build order: 4 of 17.** Depends on 12 (the cron daemon is a service) and 11
(a cron job's output is a log this phase registers as a source).
**Blocks** 14 (backup scheduling) and 21 (scheduled updates).

- [x] Cron model — migration 0012, implementing DATABASE.md table 21
- [x] Cron provider — the Agent writes crontab entries; crond runs them
- [x] Create
- [x] Edit
- [x] Delete
- [x] Enable
- [x] Disable
- [x] Run now
- [x] Logs — a file per job, contributed to Phase 11's catalogue as a source
- [x] UI

Additionally required by the above:

- [x] The panel writes crontab entries and does not run them: the command is
  data in a file, and crond runs it as the website's own account. That is what
  reconciles this phase with CLAUDE.md section 4
- [x] Every value that reaches a crontab line refused if it contains a newline
  or a `%` — a crontab is line-oriented and unquoted, so a newline is a second
  entry and `%` is a newline in disguise
- [x] Three job types, two of which the panel builds itself from a checked
  fragment, so the common cases carry no free-form command at all
- [x] No field for a user anywhere: a job belongs to a website and runs as its
  account, and an account resolving to uid 0 is refused
- [x] A managed block inside a shared file, preserving whatever the customer
  wrote around it, with no environment assignments that would change their
  entries
- [x] Crontabs written root-owned: BusyBox crond ignores anything else, and
  ignores it silently
- [x] Deleting a website removes its crontab and its jobs' logs — the rows
  cascade, the file on the host does not
- [x] A next-run calculation, including cron's day-of-month/day-of-week **or**
  rule, so a schedule that means something other than what was intended is
  caught before the job runs

---

# PHASE 11 — Logs

**Status: COMPLETE** — see [docs/PHASE11.md](docs/PHASE11.md) for scope,
decisions, and known limitations.

**Build order: 3 of 17.** Depends on nothing new: it reads files the Agent
already writes.
**Blocks** 18 (Fail2Ban decides bans by reading logs), 10, 7.1 and 27, each of
which registers a log source rather than building its own viewer.

- [x] Nginx access logs
- [x] Nginx error logs
- [x] PHP logs — one per installed version, contributed at runtime
- [x] Node logs — output and errors per application, found on disk
- [x] Cron logs
- [x] Agent logs — the audit log, whose outcome field is read as its severity
- [x] System logs — and the authentication log alongside it
- [x] Search
- [x] Filter — one severity vocabulary across seven formats
- [x] Live logs
- [x] Download

Additionally required by the above:

- [x] A catalogue: a request names a *key* the Agent knows, and the file is
  chosen from a table in this repository. A log viewer that took a path would
  be an unrestricted file reader
- [x] Every catalogued path resolved through pathsec before it is opened —
  nginx's access log is writable by the account nginx runs as, so a symlink
  planted there must not be followed out of /var/log
- [x] Reads bounded in three directions: how far back into the file, how many
  lines, and how long one line may be
- [x] A read that stops at the last complete line, so a log being written is not
  shown in halves, and a rotated file reported rather than spliced onto the
  previous one

Deliberately not implemented, with reasons in docs/PHASE11.md section 6: the
`from`/`to` time filters (one timestamp parser per daemon, and a filter that
fails to parse looks like an empty log), page-number `offset`, per-website logs
(they are on the website's own page, from Phase 4), and the systemd journal.

---

# PHASE 12 — Service Manager

**Status: COMPLETE** — see [docs/PHASE12.md](docs/PHASE12.md) for scope,
decisions, and known limitations.

**Build order: 1 of 17.** Depends on nothing.
**Blocks** 7.1, 13, 16, 17, 18, 19, 22 and 26 — everything that starts, stops or
reloads a daemon.

Part of it exists already: `agent/internal/services/control.go` was built in
Phase 9 as the smallest dependency that phase needed, and Phase 4.5 drove Apache
with `httpd -k` directly because there was no general layer to ask. Both should
end up calling this.

- [x] Service detection
- [x] Status
- [x] Start
- [x] Stop
- [x] Restart
- [x] Enable
- [x] Disable
- [x] Service UI

Additionally required by the above:

- [x] A catalogue: the panel acts on a *key* it knows, and chooses the unit
  itself. Forwarding a caller's unit name is one careless request away from
  stopping the unit the Agent runs as
- [x] Status from the process table as well as systemd, so a host without an
  init system still gets a truthful page rather than a blank one
- [x] The dashboard reads the same detection instead of a configured list of
  unit names — two sources for "which services exist" is two answers, and the
  configured one named php-fpm on a host running php-fpm83
- [x] Alerts only for the services whose being down breaks websites: cron is
  legitimately stopped, and Apache is deliberately stopped in nginx-only mode
- [x] An OpenRC backend behind the same verbs, because Alpine has no systemd to
  install: without it the page reported accurate states on such a host and could
  change none of them
- [x] Daemons the panel starts itself (PHP-FPM, Apache) show their state and
  withhold their controls, naming the owner — two owners for one process is
  worse than one owner and an explanation

---

# PHASE 13 — DNS & Local Name Server

**Status: COMPLETE** — see [docs/PHASE13.md](docs/PHASE13.md) for scope,
decisions, and known limitations.

**Build order: 8 of 17.** Depends on 12 (BIND or PowerDNS is a service) and 16
(port 53 has to be open).
**Blocks** 26 (DKIM, SPF and DMARC are published as records).

This phase also pays off Phase 4.1's deferred item: automatic DNS record
injection into the parent domain's zone, which was left until a zone existed at
all. See docs/PHASE4.1.md section 7.

- [x] BIND9 provider — PowerDNS was rejected on the grounds in
  docs/PHASE13.md section 2: it keeps zones in a database of its own, which
  would mean a second schema beside the panel's and no file an operator can read
  to see what is actually being served
- [x] Zone file generator, forward and reverse
- [x] Default SOA and name server templates, including the **glue** a zone whose
  name servers are inside it cannot load without
- [x] Master / slave replication — proved by a real transfer between two name
  servers, not by reading a configuration file back
- [x] DNSSEC signing and key rollover, done by named through `dnssec-policy`,
  with the DS record surfaced for the registrar: the one step nothing can
  automate
- [x] Zone editor UI, with the records, the signing state and the transfer
  settings
- [x] Cloudflare provider, one-way and with deletion opt-in
- [x] A, AAAA, CNAME, MX, TXT, NS, CAA, SRV and PTR record management — PTR
  because a reverse zone with no PTR records is an empty zone

Additionally required by the above:

- [x] The host's own address, reported by the Agent and stored by registration.
  The `servers` table has had `ipv4` and `ipv6` since Phase 3 and nothing ever
  filled them; the glue records above cannot be written without one. See
  docs/PHASE13.md section 8
- [x] A declarative reconcile: the panel hands the Agent the complete set of
  zones and the Agent makes the host match, so nothing can drift
- [x] Verification that the server really took a reload. `rndc` reports that a
  command was *accepted*, not that the reload worked — a named that cannot read
  its own configuration goes on serving the old one while everything reports
  success
- [x] The subdomain record injection Phase 4.1 deferred

---

# PHASE 14 — Backup

**Status: COMPLETE** — see [docs/PHASE14.md](docs/PHASE14.md) for scope,
decisions, the divergence from DATABASE.md, and known limitations.

**Build order: 11 of 17.** Depends on 10 (a schedule is a cron job), 12, and the
website and database phases already built.
**Blocks** 20 (backup alerts) and 24 (the restore and disaster-recovery tests).

- [x] Website backup — its files *and* the databases attached to it, in one
  archive by default. A site restored without the schema its application expects
  is broken in a more confusing way than one that is simply gone
- [x] Database backup — dumped by the providers that already hold the
  credentials, straight to a file rather than through the Agent's memory
- [x] Full server backup — every website and every database, subdomains
  included: a page that hides them is making a listing readable, and a backup
  that hides them is losing data
- [x] Backup jobs — through the queue, because both taking and restoring move an
  unbounded amount of data
- [x] Schedule — an hour, a minute and a day the panel owns, not a crontab entry
  it could no longer describe
- [x] Retention — an age **and** a count floor, because retention by age alone
  deletes everything you have the day after a panel is off for a fortnight
- [x] Restore — verified in full before anything on the host changes, with the
  previous files moved aside and put back if it fails partway
- [x] Verify backup — the archive read back from where it was stored and matched
  against the checksum recorded when it was written
- [x] Local storage — bounded by configured roots, so "back up to /etc/nginx"
  is not a way to write a file anywhere as root
- [x] S3 — SigV4 written against the standard library, because the Agent has no
  third-party dependencies and the only hard part is a page of HMAC
- [x] SFTP — the OpenSSH client with `StrictHostKeyChecking` on and no way to
  turn it off: a backup sent to whatever answered on port 22 is every site on
  the host handed to a stranger

Additionally required by the above:

- [x] **A backup that has not been read back is not a backup.** `verified_at` is
  its own column, and a backup that completed and could not be confirmed is
  recorded as failed with the reason
- [x] Retention that runs only after a new backup has verified, which is
  CLAUDE.md section 18 with no special case needed
- [x] A destination as a row of its own rather than fields on a schedule, so a
  manual backup has somewhere to go and a rotated key is edited once
- [x] A destination check that writes and reads back, because a destination
  nobody has reached looks like protection and is not
- [x] Credentials that never reach the jobs table, an audit record, or anything
  a handler returns
- [x] A restore confirmed with the backup's own id, not a boolean somebody sets
  once and forgets

Critical tests:

- [x] Backup integrity — a byte changed in the middle of a stored archive, with
  its size unchanged, is caught
- [x] Restore integrity — a real site's deleted files and a real database's
  dropped row both come back
- [x] Interrupted backup — a corrupt archive is refused before anything on the
  host is touched, and a restore that fails partway puts the original back
- [x] Disk full — bounded members and archives, and a restore that rolls back
  rather than leaving a site half-replaced
- [x] Permission failure — a destination outside the allowed roots, a document
  root outside the site root, and an unreachable destination are all reported
  rather than half-attempted

---

# PHASE 15 — Security Center

**Build order: 12 of 17.** Depends on 16, 17, 21, 6 and 7 — it scans what those
phases manage, so every one of them has to exist first or the score is computed
from blanks.
**Blocks** 20 (security alerts).

- [ ] Security score
- [ ] SSH scanner
- [ ] Firewall scanner
- [ ] Open port scanner
- [ ] File permission scanner
- [ ] SSL scanner
- [ ] Package update scanner
- [ ] Security findings
- [ ] Security UI

---

# PHASE 16 — Firewall

**Status: COMPLETE** — see [docs/PHASE16.md](docs/PHASE16.md) for scope,
decisions, and known limitations.

**Build order: 2 of 17.** Depends on 12.
**Blocks** 7.1 (passive port range), 13 (port 53), 18 (a ban is a rule), 26 (mail
ports), 15 (the firewall scanner) and 23 (an install step).

- [x] UFW provider
- [x] List rules
- [x] Add rule
- [x] Delete rule
- [x] Enable
- [x] Disable
- [x] Rule backup
- [x] Temporary rule
- [x] Connectivity test
- [x] Automatic rollback
- [x] Audit log

The six steps of CLAUDE.md section 19 are one protocol, not six features: back
up, validate, apply provisionally, verify, commit, roll back automatically. See
docs/PHASE16.md section 3.

One of them is easy to overstate, so it is written down plainly. The Agent
cannot prove the host is reachable from the internet — a connection it makes to
the host's own address goes over loopback and is allowed by a rule ufw installs
for that purpose, so such a test passes while the host is unreachable. The Agent
verifies the ruleset still allows the guarded ports; the *browser* supplies the
other half by confirming over the network the change governs.

Additionally required by the above:

- [x] A lockout guard that refuses the change rather than undoing it a minute
  later: most firewall accidents are obvious before they are applied
- [x] Rules addressed by what they do, not by ufw's position numbers, which
  renumber on every change
- [x] Staged rules read from `ufw show added` when the firewall is off, without
  which it could never have been switched on from the panel
- [x] NET_ADMIN for the development Agent, so the dangerous half is testable in
  a network namespace that affects nothing outside it

Test:

```text
A rule applied and never confirmed, undone by the host itself
Every shape of lockout, refused
```

---

# PHASE 17 — SSH Security

**Status: COMPLETE** — see [docs/PHASE17.md](docs/PHASE17.md) for scope,
decisions, and known limitations.

**Build order: 5 of 17.** Depends on 12 (changing sshd's configuration means
reloading it).
**Blocks** 15 (the SSH scanner).

- [x] SSH configuration reader — the *effective* configuration, from `sshd -T`
- [x] Root login status
- [x] Password authentication status
- [x] SSH port
- [x] Authorized keys
- [x] Add key
- [x] Remove key
- [x] Security recommendations

Additionally required by the above:

- [x] Every change validated with `sshd -t` against the whole configuration
  before it is installed, and the effective configuration read back afterwards:
  a change that reports success and did nothing is the worst outcome here
- [x] The four changes that would lock the operator out are refused, not warned
  about — no key and passwords off, both methods off, a port the firewall blocks,
  and root logins off where root is the only account
- [x] A drop-in the panel owns, never an edit to the distribution's sshd_config
  — and a host with no Include for it is read-only, because writing a file
  nothing reads would report success and change nothing
- [x] The firewall interlock: the Agent asks Phase 16 whether the new port would
  be reachable, and refuses with the rule to add rather than opening it itself
- [x] Keys parsed before they are written, identified by SHA256 fingerprint, with
  options prefixes refused and lines the panel does not understand left alone
- [x] The accounts offered are the host's own login accounts; a website's account
  has a nologin shell and is not offered a key it could never use

---

# PHASE 18 — Fail2Ban

**Status: COMPLETE** — see [docs/PHASE18.md](docs/PHASE18.md) for scope,
decisions, and known limitations.

**Build order: 6 of 17.** Depends on 11 (it decides bans by reading logs), 16 (a
ban is a firewall rule) and 12.

- [x] Detection — installed, running, version, and what it is actually running
- [x] Install
- [x] Enable — per jail; the daemon itself is started from the Services page
- [x] Disable
- [x] Jail list
- [x] Banned IP — with the jail that banned each, and banning by hand
- [x] Unban
- [x] Logs — fail2ban's own, contributed to Phase 11's catalogue as a source

Additionally required by the above:

- [x] A catalogue of jails, with the panel writing policy only: a filter decides
  who gets banned, and the distribution's is the one written for its own log
  format — Alpine's sshd filter exists because BusyBox writes a syslog prefix the
  standard one does not expect
- [x] The drop-in named so fail2ban reads it last. It reads every .conf before
  every .local and the last value wins, so a 10-jothost.conf loses to the
  distribution's own file — silently, with everything reporting success
- [x] Every change read back from the daemon after the reload, and rolled back
  when it did not take
- [x] Bans left in fail2ban's own iptables chains rather than written as ufw
  rules: a ban is transient and the firewall page is the operator's own rules,
  with a backup taken before every change
- [x] Loopback always in the ignore list, whether it was asked for or not
- [x] Addresses and jail names parsed before they become arguments, and host
  names refused — what gets banned must not depend on what DNS said

---

# PHASE 19 — Monitoring

**Status: COMPLETE** — see [docs/PHASE19.md](docs/PHASE19.md) for scope,
decisions, and known limitations.

**Build order: 10 of 17.** Depends on 12 (service monitoring) and on the metric
collection built in Phase 3.
**Blocks** 20 (the alert engine is what notifications deliver) and 22 (disk usage
is a quota dimension).

- [x] Metrics storage — Phase 3's samples, plus the **aggregated** half the PRD
  asks for: completed hours summarised and kept far longer than the samples,
  each bucket carrying a maximum as well as an average so an hour of averaging
  cannot hide the spike that filled a disk
- [x] CPU history
- [x] RAM history
- [x] Disk history
- [x] Network history
- [x] Load history — all five as Phase 3 built them, now with `90d` and `1y`
  ranges served from the summaries, which is what lets them outlive the raw
  samples' retention
- [x] Service monitoring — recorded as **transitions**, so "when did it go down
  and for how long" is a subtraction rather than a search, and a row per poll
  saying "still running" never exists
- [x] Alert engine — with a **sustained breach**: a rule fires when its
  condition has held for its whole duration, not when one reading crossed a
  line. That is the difference between a panel somebody keeps and a panel
  somebody mutes
- [x] Alert rules — rows an operator can edit, because the right threshold is a
  property of the machine rather than of this software

Additionally required by the above:

- [x] One definition of a threshold. The dashboard's alerts stay — they answer
  "what is wrong now" and cannot go stale — but their numbers now come from
  these rules, so the two pages cannot disagree
- [x] Sustained breach that survives a restart, asked of the stored samples
  rather than kept in memory: a panel restarted mid-incident must not forget
  that a machine has been at 99% for an hour
- [x] One open alert per thing watched, enforced by a unique partial index —
  without it a flapping disk is four hundred rows about one filesystem
- [x] Acknowledging that is **not** resolving, and no endpoint that resolves by
  hand: whether a condition has cleared is the machine's to decide
- [x] A `monitor.manage` permission of its own, granted to operators too: the
  person who looks after the sites is who needs to silence a false alarm at
  three in the morning, and that is not the same as being able to stop nginx

---

# PHASE 20 — Notifications

**Build order: 13 of 17.** Depends on 19, 14, 15, 6 and 12 — every alert type
listed below is raised by one of them, and built earlier this phase would ship
switches with nothing behind them.

- [ ] Email
- [ ] LINE
- [ ] Telegram
- [ ] Notification settings
- [ ] SSL alerts
- [ ] Backup alerts
- [ ] Disk alerts
- [ ] Service alerts
- [ ] Security alerts

---

# PHASE 21 — System Updates

**Status: COMPLETE**, with one item deliberately not done and written up —
see [docs/PHASE21.md](docs/PHASE21.md) for scope, decisions, and known
limitations.

**Build order: 9 of 17.** Depends on 10 for the scheduled half — though see
below: the schedule is *not* a cron entry, and the reason is in
docs/PHASE21.md section 8.
**Blocks** 15 (the package update scanner).

Initial version:

- [x] Package update detection, from what the package manager says it will
  **do** rather than from a version comparison. The two disagree: a pinned
  package appears in the comparison forever and is never upgraded, so listing it
  as pending would show a queue that never empties
- [x] Security update detection on apt, from the origin it prints with each
  candidate. apk **cannot** identify them, and the panel says so rather than
  reporting "0 security updates" — a sentence that answers a question nothing
  asked and reads as "nothing urgent"
- [x] PHP update detection, as a view over the same list filtered to the
  packages Phase 5 installed
- [x] Node update detection, the same way

Later:

- [x] Automatic updates — off by default, because applying updates restarts
  daemons and an operator who has not asked for that should not learn it from
  their monitoring at three in the morning
- [x] Scheduled updates, in a window the panel owns rather than a crontab
  entry: those run as a website's unprivileged account, and a schedule in a file
  is one the panel can no longer describe
- [ ] **Rollback — not implemented, deliberately.** Neither apk nor apt keeps
  the package it replaced, and a Debian security update's predecessor is
  normally gone from the archive the moment it is superseded, so the button
  would take a service down and then fail. What is there instead: a *revert*
  that asks the package manager whether that exact version can still be
  installed and refuses when it cannot, and a history recording every version
  that moved — which is what makes a manual recovery possible. See
  docs/PHASE21.md section 5

Additionally required by the above:

- [x] Three states rather than two. "Outstanding", "nothing outstanding" and
  **"not known"** — because both package managers exit zero and print an empty
  list when every repository is unreachable, so a panel with two states says
  "up to date" to somebody whose machine it could not read
- [x] An `update.manage` permission of its own. Applying an update restarts
  daemons and can change the version of PHP a customer's site runs on, which is
  a different decision from restarting a service somebody already chose to run
- [x] A scheduler in the panel, alongside the metric sampler, so the privileged
  half stays in the Agent and the schedule stays describable

---

# PHASE 22 — Multi-Tenant Hierarchy & Subscriptions

**Build order: 16 of 17.** Depends on 26 (mailboxes are a quota dimension), 19
(disk usage is another) and 12 (cgroup isolation is a systemd slice).
**Blocks** 23 and 24.

Built before mail, the plan builder would ship a Mailboxes limit that counts
nothing — the retroactive edit this ordering exists to avoid.

- [ ] Tiered account model: Admin -> Reseller -> Customer
- [ ] Service Plan (Package) builder (Disk, Bandwidth, Sites, DBs, Mailboxes)
- [ ] Add-on Plan builder
- [ ] Subscription creation and plan assignment
- [ ] Quota enforcement middleware (Hard & Soft limits)
- [ ] Resource isolation per subscription via cgroups (CPU, RAM, IOPS)
- [ ] Impersonation mechanism (Login-as-Customer / Reseller)
- [ ] Subscription dashboard & resource tracking UI

---

# PHASE 23 — Production Installer

**Build order: 17 of 17.** Depends on everything it installs: 16 for the firewall
step, 12 for the systemd step, and every runtime feature it lays down.

- [ ] OS detection
- [ ] Architecture detection
- [ ] Root detection
- [ ] Dependency installation
- [ ] Database setup
- [ ] API installation
- [ ] Agent installation
- [ ] Nginx configuration
- [ ] SSL
- [ ] Admin creation
- [ ] systemd
- [ ] Firewall
- [ ] Health check

Commands:

```text
install
update
repair
uninstall
```

---

# PHASE 24 — Production Hardening

**Build order: 18 of 17.** Depends on 14 (restore test), 16 (firewall recovery
test) and 22 (the RBAC and tenancy tests).

- [ ] Full security audit
- [ ] API penetration test
- [ ] Agent security test
- [ ] Path traversal test
- [ ] Command injection test
- [ ] Privilege escalation test
- [ ] Authentication test
- [ ] RBAC test
- [ ] Rate limit test
- [ ] Backup restore test
- [ ] Disaster recovery test
- [ ] Firewall recovery test
- [ ] Load test

---

# PHASE 25 — Release

**Build order: 19 of 17.** Last by definition.

- [ ] Versioning
- [ ] Release build
- [ ] Docker image
- [ ] Linux binary
- [ ] Installer
- [ ] Upgrade script
- [ ] Migration system
- [ ] Documentation
- [ ] Changelog
- [ ] Security documentation
- [ ] Recovery documentation

---

# PHASE 26 — Mail Server Ecosystem

**Build order: 14 of 17.** Depends on 13 (DKIM, SPF and DMARC are DNS records),
16 (25, 465, 587, 993), 6 (TLS uses the certificates already issued) and 12
(Postfix and Dovecot are services).
**Blocks** 22 (mailboxes are one of its quota dimensions).

- [ ] Postfix MTA provider
- [ ] Dovecot IMAP/POP3 provider
- [ ] Mailbox schema & CRUD API
- [ ] Automated DKIM key generation & DNS record publishing
- [ ] SPF & DMARC policy auto-configuration
- [ ] Rspamd / SpamAssassin integration
- [ ] ClamAV virus scanning integration
- [ ] Roundcube Webmail provider & automated vhost installer
- [ ] Email forwarders, Catch-all, and Autoresponders
- [ ] Mailbox quota limits & TLS security enforcement
- [ ] Webmail UI integration

# PHASE 27 — Git & Webhook Deployment Actions

**Build order: 15 of 17.** Depends on 11 (deployment log streaming), 12, and the
per-site isolated execution built in Phases 5 and 9.

- [ ] Git repository manager per website
- [ ] SSH deployment key generation
- [ ] Webhook receiver endpoint & secret signature validation
- [ ] Post-deployment script runner engine
- [ ] Pre-built action templates (composer install, npm run build, php artisan migrate)
- [ ] Custom shell script execution isolated per site user
- [ ] Real-time deployment log streaming & execution history
- [ ] Automatic rollback on script failure
- [ ] Branch selection & Push-to-deploy trigger control
- [ ] Deployment UI

# Definition of Done

A task is complete only when:

```text
[x] Code implemented
[x] Unit tests
[x] Integration tests where applicable
[x] Security checks
[x] Error handling
[x] Logging
[x] Documentation
[x] Build successful
[x] Docker test successful
```

A phase is complete only when all acceptance criteria are satisfied.