# JotHost Panel — Development Tasks

## Status Legend

```text
[ ] TODO
[-] IN PROGRESS
[x] DONE
[!] BLOCKED
```

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

- [ ] Apache2 / httpd detection & provider
- [ ] Nginx reverse proxy template (proxy_pass to Apache backend)
- [ ] Apache mpm_event & PHP-FPM integration
- [ ] Apache VirtualHost generator
- [ ] Webserver mode switcher (Nginx Standalone vs Nginx + Apache Hybrid)
- [ ] .htaccess support and custom rewrite rules handling
- [ ] Real IP restoration via mod_remoteip
- [ ] Backend port assignment and validation engine

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

- [ ] Cron model
- [ ] Cron provider
- [ ] Create
- [ ] Edit
- [ ] Delete
- [ ] Enable
- [ ] Disable
- [ ] Run now
- [ ] Logs
- [ ] UI

---

# PHASE 11 — Logs

- [ ] Nginx access logs
- [ ] Nginx error logs
- [ ] PHP logs
- [ ] Node logs
- [ ] Cron logs
- [ ] Agent logs
- [ ] System logs
- [ ] Search
- [ ] Filter
- [ ] Live logs
- [ ] Download

---

# PHASE 12 — Service Manager

- [ ] Service detection
- [ ] Status
- [ ] Start
- [ ] Stop
- [ ] Restart
- [ ] Enable
- [ ] Disable
- [ ] Service UI

---

# PHASE 13 — DNS & Local Name Server

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

- [ ] UFW provider
- [ ] List rules
- [ ] Add rule
- [ ] Delete rule
- [ ] Enable
- [ ] Disable
- [ ] Rule backup
- [ ] Temporary rule
- [ ] Connectivity test
- [ ] Automatic rollback
- [ ] Audit log

---

# PHASE 17 — SSH Security

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