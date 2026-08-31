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
```

Remaining, in build order:

```text
 1.  17   SSH Security           — needs 12 (sshd reload)
 2.  18   Fail2Ban               — reads logs (11), writes firewall rules (16)
 3.  7.1  FTP Manager            — passive ports need 16; transfers need 11
 4.  13   DNS & Local Name Server— needs 12 (BIND) and 16 (port 53)
 5.  21   System Updates         — scheduled updates need 10
 6.  19   Monitoring             — service monitoring needs 12
 7.  14   Backup                 — scheduling needs 10
 8.  15   Security Center        — scans 16, 17, 21, 6, 7: follows all of them
 9.  20   Notifications          — delivers alerts raised by 19, 14, 15, 6, 12
10.  26   Mail Server Ecosystem  — DKIM/SPF/DMARC need 13; ports need 16
11.  27   Git & Webhook Actions  — deployment logs need 11
12.  22   Multi-Tenant           — quota dimensions must exist first, mail included
13.  23   Production Installer   — installs everything, so everything must exist
14.  24   Production Hardening   — tests the finished system
15.  25   Release                — last by definition
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

**Build order: 7 of 17.** Depends on 12 (start and reload the daemon), 16 (the
passive port range has to be opened), 11 (connection and transfer logs), and 6
(FTPS binds a certificate the SSL phase already issues).

- [ ] Pure-FTPd / ProFTPD provider & daemon configuration engine
- [ ] FTP user database schema (Virtual FTP users tied to system accounts)
- [ ] Additional FTP users per website/subscription
- [ ] Strict Directory Chroot (chroot jail) to prevent path traversal outside home/document root
- [ ] Granular permission assignment per FTP user (Read-only / Full access)
- [ ] FTPS (FTP over TLS/SSL) enforcement & certificate binding
- [ ] Passive port range configuration (PassivePortRange) & automatic firewall sync
- [ ] Custom home directory mapping (e.g., restrict to specific subfolder /var/www/vhosts/example.com/httpdocs/assets)
- [ ] FTP quota enforcement (Disk space limits per FTP user)
- [ ] FTP active session monitor & disconnect user API
- [ ] FTP connection & transfer logs tracking
- [ ] FTP management UI tab in Website Manager (Plesk-style additional FTP accounts card)

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

**Build order: 8 of 17.** Depends on 12 (BIND or PowerDNS is a service) and 16
(port 53 has to be open).
**Blocks** 26 (DKIM, SPF and DMARC are published as records).

This phase also pays off Phase 4.1's deferred item: automatic DNS record
injection into the parent domain's zone, which was left until a zone existed at
all. See docs/PHASE4.1.md section 7.

- [ ] BIND9 / PowerDNS provider
- [ ] Zone file generator (Forward & Reverse zones)
- [ ] Default DNS SOA and Name Server templates
- [ ] DNS Master / Slave zone replication setup
- [ ] DNSSEC automatic signing & key rollover
- [ ] Local DNS zone editor UI
- [ ] Cloudflare provider (Remote sync option)
- [ ] A, AAAA, CNAME, MX, TXT, CAA, SRV record management

---

# PHASE 14 — Backup

**Build order: 11 of 17.** Depends on 10 (a schedule is a cron job), 12, and the
website and database phases already built.
**Blocks** 20 (backup alerts) and 24 (the restore and disaster-recovery tests).

- [ ] Website backup
- [ ] Database backup
- [ ] Full server backup
- [ ] Backup jobs
- [ ] Schedule
- [ ] Retention
- [ ] Restore
- [ ] Verify backup
- [ ] Local storage
- [ ] S3
- [ ] SFTP

Critical tests:

- [ ] Backup integrity
- [ ] Restore integrity
- [ ] Interrupted backup
- [ ] Disk full
- [ ] Permission failure

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

**Build order: 5 of 17.** Depends on 12 (changing sshd's configuration means
reloading it).
**Blocks** 15 (the SSH scanner).

- [ ] SSH configuration reader
- [ ] Root login status
- [ ] Password authentication status
- [ ] SSH port
- [ ] Authorized keys
- [ ] Add key
- [ ] Remove key
- [ ] Security recommendations

---

# PHASE 18 — Fail2Ban

**Build order: 6 of 17.** Depends on 11 (it decides bans by reading logs), 16 (a
ban is a firewall rule) and 12.

- [ ] Detection
- [ ] Install
- [ ] Enable
- [ ] Disable
- [ ] Jail list
- [ ] Banned IP
- [ ] Unban
- [ ] Logs

---

# PHASE 19 — Monitoring

**Build order: 10 of 17.** Depends on 12 (service monitoring) and on the metric
collection built in Phase 3.
**Blocks** 20 (the alert engine is what notifications deliver) and 22 (disk usage
is a quota dimension).

- [ ] Metrics storage
- [ ] CPU history
- [ ] RAM history
- [ ] Disk history
- [ ] Network history
- [ ] Load history
- [ ] Service monitoring
- [ ] Alert engine
- [ ] Alert rules

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

**Build order: 9 of 17.** Depends on 10 for the scheduled half.
**Blocks** 15 (the package update scanner).

Initial version:

- [ ] Package update detection
- [ ] Security update detection
- [ ] PHP update detection
- [ ] Node update detection

Later:

- [ ] Automatic updates
- [ ] Scheduled updates
- [ ] Rollback

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