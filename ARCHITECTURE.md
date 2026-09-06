# JotHost Panel — Architecture

## 1. Architecture Principles

1. Separation of concerns.
2. API must never directly perform privileged operations.
3. Host Agent owns privileged operations.
4. All privileged operations must be typed.
5. No arbitrary shell execution.
6. Infrastructure operations must be idempotent where possible.
7. All sensitive operations must be auditable.
8. Frontend must not contain infrastructure logic.
9. Database must not contain server state as the source of truth.
10. Desired state should be preferred over imperative commands.

---

# 2. System Architecture

```text
                         INTERNET
                            │
                            ▼
                     ┌────────────┐
                     │   NGINX    │
                     └─────┬──────┘
                           │
              ┌────────────┴────────────┐
              ▼                         ▼
       React Frontend              Go API
                                      │
                          ┌───────────┼───────────┐
                          ▼           ▼           ▼
                       Postgres     Redis       WebSocket
                                      │
                                      ▼
                                Job Queue
                                      │
                                      ▼
                                Go Agent
                                      │
              ┌───────────────────────┼───────────────────────┐
              ▼                       ▼                       ▼
           Nginx                   PHP-FPM                 Node.js
              │
        ┌─────┼─────────┬─────────────┬──────────────┐
        ▼     ▼         ▼             ▼              ▼
      SSL   Files    Database       Cron          Firewall
```

---

# 3. Repository Structure

```text
jothost/
│
├── frontend/
│   ├── src/
│   ├── public/
│   ├── tests/
│   ├── package.json
│   └── vite.config.ts
│
├── api/
│   ├── cmd/
│   ├── internal/
│   │   ├── auth/
│   │   ├── users/
│   │   ├── websites/
│   │   ├── domains/
│   │   ├── databases/
│   │   ├── php/
│   │   ├── node/
│   │   ├── ssl/
│   │   ├── dns/
│   │   ├── cron/
│   │   ├── backups/
│   │   ├── jobs/
│   │   ├── audit/
│   │   └── server/
│   └── tests/
│
├── agent/
│   ├── cmd/
│   ├── internal/
│   │   ├── operations/
│   │   ├── nginx/
│   │   ├── php/
│   │   ├── node/
│   │   ├── filesystem/
│   │   ├── ssl/
│   │   ├── firewall/
│   │   ├── cron/
│   │   ├── backup/
│   │   ├── services/
│   │   └── security/
│   └── tests/
│
├── shared/
│   ├── schemas/
│   └── types/
│
├── migrations/
│
├── docker/
│
├── scripts/
│
├── docs/
│
├── tests/
│   ├── integration/
│   └── e2e/
│
├── docker-compose.yml
├── Makefile
├── CLAUDE.md
├── PRD.md
├── ARCHITECTURE.md
├── DATABASE.md
├── API_SPEC.md
└── TASKS.md
```

---

# 4. Frontend Architecture

Use:

```text
React
TypeScript
Tailwind CSS
TanStack Query
Zustand
React Router
Monaco Editor
```

Structure:

```text
src/
├── app/
├── components/
├── layouts/
├── pages/
├── features/
├── hooks/
├── services/
├── stores/
├── types/
├── utils/
└── lib/
```

Feature-oriented architecture is preferred.

Example:

```text
features/
└── websites/
    ├── api.ts
    ├── types.ts
    ├── hooks.ts
    ├── components/
    └── pages/
```

---

# 5. API Architecture

Go API layers:

```text
HTTP Handler
    ↓
Service
    ↓
Repository
    ↓
Database
```

Example:

```text
WebsiteHandler
      ↓
WebsiteService
      ↓
WebsiteRepository
      ↓
PostgreSQL
```

Infrastructure operations:

```text
WebsiteService
      ↓
JobService
      ↓
AgentClient
      ↓
Go Agent
```

---

# 6. Agent Architecture

Agent layers:

```text
Transport
    ↓
Authentication
    ↓
Operation Validator
    ↓
Operation Handler
    ↓
Infrastructure Provider
```

Example:

```text
website.create
      ↓
ValidateDomain()
ValidatePHPVersion()
ValidatePath()
      ↓
WebsiteCreator
      ↓
NginxProvider
PHPProvider
FilesystemProvider
```

---

# 7. Provider Interfaces

Create abstractions:

```go
type WebServerProvider interface {
    CreateSite(...)
    DeleteSite(...)
    Reload(...)
    Validate(...)
}
```

```go
type PHPProvider interface {
    Versions()
    CreatePool(...)
    DeletePool(...)
    Configure(...)
}
```

```go
type FirewallProvider interface {
    Rules()
    AddRule(...)
    RemoveRule(...)
}
```

Providers allow future support for:

```text
Ubuntu
Debian
Rocky Linux
```

---

# 8. Desired State

Resources should have desired state.

Example:

```json
{
  "domain": "example.com",
  "php_version": "8.4",
  "ssl_enabled": true,
  "https_redirect": true
}
```

Agent compares:

```text
Desired State
     ↓
Current State
     ↓
Diff
     ↓
Apply
```

Operations should be idempotent.

---

# 9. Job Architecture

```text
API
 ↓
Create Job
 ↓
Redis Queue
 ↓
Worker
 ↓
Agent
 ↓
Result
 ↓
PostgreSQL
 ↓
WebSocket
 ↓
Frontend
```

---

# 10. Security Boundaries

```text
PUBLIC
 │
 ▼
Nginx
 │
 ▼
Frontend / API
 │
 ├── PostgreSQL
 ├── Redis
 │
 └── Agent
       │
       └── ROOT OPERATIONS
```

Agent must not expose a public HTTP endpoint by default.

Use:

```text
Unix Domain Socket
```

for local API → Agent communication.

---

# 11. Secrets

Never store secrets in source code.

Use environment variables:

```text
DATABASE_URL
REDIS_URL
JWT_SECRET
ENCRYPTION_KEY
CLOUDFLARE_API_TOKEN
```

Secrets stored in database must be encrypted at rest where appropriate.

---

# 12. Logging

Use structured JSON logging.

Fields:

```text
timestamp
level
service
request_id
user_id
action
resource
error
duration
```

---

# 13. Error Handling

API response format:

```json
{
  "success": false,
  "error": {
    "code": "WEBSITE_NOT_FOUND",
    "message": "Website not found",
    "request_id": "req_123"
  }
}
```

Never expose:

```text
stack traces
shell commands
passwords
tokens
internal filesystem secrets
```

---

# 14. Observability

Minimum:

```text
Application logs
Agent logs
Audit logs
Job logs
System logs
```

Future:

```text
OpenTelemetry
Prometheus
Grafana
```

---

# 15. Deployment

Production:

```text
systemd
```

Services:

```text
jothost-api.service
jothost-agent.service
```

Frontend should be compiled to static files and served through Nginx.

The installer (`scripts/jothost-installer.sh`, shipped as `install.sh` beside
the artefacts) lays all of that down from one command and a domain. The layout
it produces is the contract between the installer, the service units and the
panel's own configuration:

```text
/opt/jothost/bin/          jothost-api, jothost-agent
/opt/jothost/frontend/     the compiled SPA, served by nginx
/opt/jothost/migrations/   the SQL the API applies at startup
/etc/jothost/api.env       0640 root:jothost
/etc/jothost/agent.env     0600 root:root
/var/lib/jothost/          the Agent's state
/var/log/jothost/          2770 root:jothost
/run/jothost/agent.sock    0660 root:jothost

/etc/jothost/web/          the panel's own web stack: nginx.conf, sites.d,
                           php-fpm.conf, php-fpm.d
/run/jothost-web/          its pid files and PHP socket, 0750 root:<web group>
/var/log/jothost/web/      its logs
```

## The panel's own web stack

The applications the panel installs for itself — phpMyAdmin, and anything that
follows it — are served by a **second nginx and a second PHP-FPM master**,
reading only the directories above. The public nginx proxies to it on loopback
at `/phpmyadmin/`.

This is not tidiness. When the two shared a web server and a PHP master:

- a customer site whose configuration nginx refuses stopped the reload that
  would have published a panel change, and a customer pool that stopped the PHP
  master from starting took the database console down with it — and a database
  console going dark is exactly when an operator most needs it;
- the panel had to recognise its own vhosts by scanning the websites' directory
  for a marker comment, because nothing else distinguished them;
- a website named the same as a panel application collided, and which one won
  depended on filesystem order.

Two consequences worth knowing when working on this:

- `/run/jothost-web` sits **beside** `/run/jothost`, not inside it. That
  directory is `0750 root:jothost` so only the Agent's account reaches the
  privileged socket in it, and nginx is not in that group — a run directory
  underneath it is one the panel's nginx cannot traverse, and the fix is never
  to loosen the socket's directory.
- The panel's instance runs with `-p /etc/jothost/web`, so every relative path
  nginx resolves — its default error log, a bare `include fastcgi_params` —
  resolves inside the panel's tree. Paths written for it must be absolute, and
  `fastcgi_params` is copied in rather than included from `/etc/nginx`.

Neither master is registered with the init system. Both are the Agent's, and
the Agent reconciles them at startup: it starts at boot, so that is what brings
the panel's applications back after a reboot.

The Agent runs as root and the API as the unprivileged `jothost-api`, which is
the boundary in section 2 made concrete on a real host. The installer also sets
`AGENT_ALLOWED_UIDS` to the API account's uid, so the socket's group says who
*can* connect and that says who may even so.

Host conventions differ, and the installer resolves them once and tells both
halves: nginx's vhost directory (`http.d` on Alpine, `conf.d` on Debian), the
cron spool, and the web server's group. A vhost written into the wrong
directory on Alpine stops nginx from starting at all, because `conf.d` is
included at the main level there.

See [docs/PHASE23.md](docs/PHASE23.md).

---

# 16. Docker Development

All Linux-specific behavior must be tested inside Linux containers.

Windows is only the host development OS.

Do not rely on:

```text
Windows filesystem permissions
Windows services
Windows shell
Windows networking behavior
```

for production logic.