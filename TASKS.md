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

# PHASE 7.5 — Code Editor

- [ ] Monaco integration
- [ ] File tree
- [ ] Open file
- [ ] Save file
- [ ] Tabs
- [ ] Search
- [ ] Replace
- [ ] Syntax highlighting
- [ ] Auto save
- [ ] Large file protection

---

# PHASE 8 — Database Manager

- [ ] MariaDB provider
- [ ] MySQL provider
- [ ] PostgreSQL provider
- [ ] Database creation
- [ ] Database deletion
- [ ] User creation
- [ ] User deletion
- [ ] Password change
- [ ] Permissions
- [ ] Database size
- [ ] Database UI

---

# PHASE 9 — Node.js Manager

- [ ] Node version detection
- [ ] Node version installation
- [ ] Node application model
- [ ] Application creation
- [ ] Application deletion
- [ ] Environment variables
- [ ] systemd service generation
- [ ] Start
- [ ] Stop
- [ ] Restart
- [ ] Logs
- [ ] Port validation
- [ ] Reverse proxy
- [ ] Node UI

Test:

```text
Express
NestJS
Nuxt
```

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

# PHASE 13 — DNS

- [ ] Cloudflare provider
- [ ] API token encryption
- [ ] A records
- [ ] AAAA
- [ ] CNAME
- [ ] MX
- [ ] TXT
- [ ] CAA
- [ ] SRV
- [ ] DNS UI

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

# PHASE 22 — Multi-user

- [ ] Hosting users
- [ ] User websites
- [ ] User databases
- [ ] Resource limits
- [ ] Disk limits
- [ ] Website limits
- [ ] Database limits
- [ ] User dashboard

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