# JotHost Panel

A self-hosted Linux hosting control panel: websites, PHP and Node.js applications,
databases, SSL, files, cron, backups, monitoring, and security from one web interface.

**Status:** Phases 0–15 complete, plus 4.1 (subdomains), 4.5
(the Apache hybrid arrangement), 7.1 (FTP), 16 (the firewall), 17 (SSH security),
18 (intrusion prevention), 19 (monitoring), 20 (notifications), 21 (system
updates), 26 (the mail server), 27 (git deployment), 22 (multi-tenancy), 23
(the production installer) and 24 (production hardening): foundation, authentication, Host Agent, dashboard,
websites, subdomains, the nginx +
Apache engine, PHP, SSL, files, the code editor, databases, Node.js
applications, scheduled jobs, the log viewer, host services, FTP accounts, DNS
and the local name server, the packet filter, the SSH server's settings,
fail2ban, package updates, the alert engine, backups that are read back before
they are called backups, a security score that never hides how many checks it
is built from, notifications that keep the record of
every one they failed to deliver, a mail server that shows what it is
configured to do next to what the world can actually verify, and deployments
that run a customer's build as the customer's own account and never as root,
quotas that are charged to whoever owns the website rather than to whoever
pressed the button, and an installer that turns an empty Linux machine into a
working panel from one command and a domain, and an audit that attacks all of
it — where every group of refusals is preceded by a control, because a refusal
proves nothing unless the request reached the code that refused it.

Next is Phase 25 (Release). Phases are built in dependency order
rather than numeric order — see [Build Order](TASKS.md#build-order) in TASKS.md
for the sequence and why each phase sits where it does. The per-phase notes in
[docs/](docs/) say what each one does and does not include.

---

## Architecture in one picture

```text
Browser
   │
   ▼
 Nginx ──────────► React frontend (Vite)
   │
   └────────────►  Go API   (unprivileged)
                     │
          ┌──────────┼──────────┐
          ▼          ▼          ▼
      Postgres     Redis    Unix socket
                                │
                                ▼
                          Go Host Agent  (privileged)
```

Three rules define the design:

1. The **API never performs privileged operations**. It sends typed, allowlisted
   operations to the Agent.
2. The **Agent never exposes a network port**. It listens on a Unix domain socket
   with `0660` permissions.
3. **No arbitrary shell execution**, ever. Operations are typed constants
   validated against an allowlist before dispatch.
4. **Sessions are revocable.** Access tokens are opaque and server-side, so
   logout takes effect immediately rather than at expiry.

Full detail: [ARCHITECTURE.md](ARCHITECTURE.md).

---

## Requirements

| Tool | Version | Notes |
|---|---|---|
| Docker Desktop | 24+ with Compose v2 | Required. Everything runs in Linux containers |
| Make | any | Optional; every target has a plain `docker compose` equivalent |

Go and Node.js are **not** required on the host. Windows is supported as a
development host; all Linux-specific behaviour executes inside containers.

---

## Quick start

```bash
make dev
```

That copies `.env.example` to `.env` if needed, builds the images, and starts
Postgres, Redis, the Agent, the API, the frontend, and Nginx.

| Surface | URL |
|---|---|
| Panel (via Nginx) | http://localhost:8081 |
| Frontend (direct) | http://localhost:5173 |
| API liveness | http://localhost:8080/healthz |
| API readiness | http://localhost:8080/readyz |

Create the first administrator — credentials come from the environment so they
never reach the process list or shell history:

```bash
JOTHOST_ADMIN_USERNAME=admin JOTHOST_ADMIN_PASSWORD='a-long-password' make create-admin
```

Then sign in at http://localhost:8081.

Without Make:

```bash
docker compose up -d --build
```

---

## Installing on a server

Everything above is the development stack. To put the panel on a real Linux
machine, build a release and run the installer on the target host:

```bash
make release
```

That produces a versioned archive and a checksum:

```text
release/jothost-0.1.0-linux-amd64.tar.gz
release/jothost-0.1.0-linux-amd64.tar.gz.sha256
```

Copy both to the server and check the archive before running anything as root
— it is the only check available at that point:

```bash
sha256sum -c jothost-0.1.0-linux-amd64.tar.gz.sha256
tar -xzf jothost-0.1.0-linux-amd64.tar.gz
cd jothost-0.1.0-linux-amd64
./bin/jothost-api version      # what you are about to install
```

Then:

```bash
sudo ./install.sh install --domain panel.example.com --email you@example.com
```

That is the whole of it. The installer detects the distribution, installs
nginx, PostgreSQL, Redis, certbot and PHP, creates the panel's accounts and
services, obtains a certificate, creates the administrator and prints their
password once, closes the firewall to everything but SSH and the web, and then
asks the panel over the network whether it is answering before reporting
success.

```bash
sudo ./install.sh status       # what is installed and what is running
sudo ./install.sh update       # new binaries and frontend, same secrets
sudo ./install.sh repair       # reconcile a machine that has drifted
sudo ./install.sh uninstall    # remove the panel; --purge also its data
```

Supported: Debian/Ubuntu, Alpine, and RHEL/Rocky/Alma, on x86_64 and aarch64.
See [docs/PHASE23.md](docs/PHASE23.md) for what it does and does not do, and
[docs/PHASE25.md](docs/PHASE25.md) for how a release is built and checked.

`make dist` still builds the unpacked tree if you would rather copy that.

### Before you rely on it

| | |
|---|---|
| [docs/SECURITY.md](docs/SECURITY.md) | What protects the host, and what deliberately does not. Read the "What this does not do" section. |
| [docs/RECOVERY.md](docs/RECOVERY.md) | What to do when something breaks. Start here at 3am. |
| [CHANGELOG.md](CHANGELOG.md) | What is in this version, and its known limitations. |

---

## Common commands

```bash
make dev                      # build and start the dev stack
make ps                       # service status and health
make logs                     # follow all logs
make down                     # stop the stack
make clean                    # stop and delete volumes (destroys dev data)
```

```bash
make test                     # all unit tests (Go + frontend)
make lint                     # go vet, gofmt, eslint
make docker-test              # full containerised suite incl. integration
make verify                   # everything CI runs
```

```bash
make migrate                  # apply pending database migrations
make migrate-status           # show migration state
make create-admin             # create an administrator
```

Ask the Agent about the host directly:

```bash
docker compose exec agent jothost-agent -call metrics.memory
```

---

## Repository layout

```text
api/        Go HTTP API — unprivileged
agent/      Go Host Agent — privileged, Unix socket only
shared/     Go module shared by api and agent (logger, protocol, version)
frontend/   React + TypeScript + Vite + Tailwind
docker/     Dockerfiles and Nginx configuration
migrations/ SQL migrations
tests/      Integration and end-to-end tests
docs/       Development and phase documentation
```

---

## Configuration

Configuration comes from the environment; nothing is hard-coded and secrets are
never committed. Start from [.env.example](.env.example). Every variable is
validated at startup — the API and Agent refuse to boot on invalid
configuration rather than running with a surprising default.

`ENCRYPTION_KEY` is required and protects secrets at rest (currently 2FA
secrets). Generate one with `openssl rand -hex 32`. Losing or changing it makes
every stored 2FA secret undecryptable, so treat it like a database password.

---

## Documentation

| Document | Contents |
|---|---|
| [PRD.md](PRD.md) | Product requirements |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System design and boundaries |
| [DATABASE.md](DATABASE.md) | Schema design |
| [API_SPEC.md](API_SPEC.md) | Endpoint contract |
| [TASKS.md](TASKS.md) | Phase plan, build order, and status |
| [CLAUDE.md](CLAUDE.md) | Engineering rules |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | Day-to-day workflow |
| [docs/PHASE0.md](docs/PHASE0.md) | Phase 0 scope, decisions, and known limitations |
| [docs/PHASE1.md](docs/PHASE1.md) | Phase 1 scope, decisions, and known limitations |
| [docs/PHASE2.md](docs/PHASE2.md) | Phase 2 scope, decisions, and known limitations |
| [docs/PHASE3.md](docs/PHASE3.md) | Phase 3 scope, decisions, and known limitations |
| [docs/PHASE4.md](docs/PHASE4.md) | Phase 4 scope, decisions, and known limitations |
| [docs/PHASE4.1.md](docs/PHASE4.1.md) | Phase 4.1 scope, decisions, and known limitations |
| [docs/PHASE4.5.md](docs/PHASE4.5.md) | Phase 4.5 scope, decisions, and known limitations |
| [docs/PHASE5.md](docs/PHASE5.md) | Phase 5 scope, decisions, and known limitations |
| [docs/PHASE6.md](docs/PHASE6.md) | Phase 6 scope, decisions, and known limitations |
| [docs/PHASE7.md](docs/PHASE7.md) | Phase 7 scope, decisions, and known limitations |
| [docs/PHASE7.5.md](docs/PHASE7.5.md) | Phase 7.5 scope, decisions, and known limitations |
| [docs/PHASE8.md](docs/PHASE8.md) | Phase 8 scope, decisions, and known limitations |
| [docs/PHASE9.md](docs/PHASE9.md) | Phase 9 scope, decisions, and known limitations |
| [docs/PHASE10.md](docs/PHASE10.md) | Phase 10 scope, decisions, and known limitations |
| [docs/PHASE11.md](docs/PHASE11.md) | Phase 11 scope, decisions, and known limitations |
| [docs/PHASE12.md](docs/PHASE12.md) | Phase 12 scope, decisions, and known limitations |
| [docs/PHASE16.md](docs/PHASE16.md) | Phase 16 scope, decisions, and known limitations |
| [docs/PHASE17.md](docs/PHASE17.md) | Phase 17 scope, decisions, and known limitations |
| [docs/PHASE18.md](docs/PHASE18.md) | Phase 18 scope, decisions, and known limitations |
| [docs/PHASE7.1.md](docs/PHASE7.1.md) | Phase 7.1 scope, decisions, and known limitations |
| [docs/PHASE13.md](docs/PHASE13.md) | Phase 13 scope, decisions, and known limitations |
| [docs/PHASE21.md](docs/PHASE21.md) | Phase 21 scope, decisions, and known limitations |
| [docs/PHASE19.md](docs/PHASE19.md) | Phase 19 scope, decisions, and known limitations |
| [docs/PHASE14.md](docs/PHASE14.md) | Phase 14 scope, decisions, and known limitations |
| [docs/PHASE15.md](docs/PHASE15.md) | Phase 15 scope, decisions, and known limitations |
| [docs/PHASE20.md](docs/PHASE20.md) | Phase 20 scope, decisions, and known limitations |
| [docs/PHASE26.md](docs/PHASE26.md) | Phase 26 scope, decisions, and known limitations |
| [docs/PHASE27.md](docs/PHASE27.md) | Phase 27 scope, decisions, and known limitations |
| [docs/PHASE22.md](docs/PHASE22.md) | Phase 22 scope, decisions, and known limitations |
| [docs/PHASE23.md](docs/PHASE23.md) | Phase 23 scope, decisions, and known limitations |
| [docs/PHASE24.md](docs/PHASE24.md) | Phase 24 security audit, findings, and known limitations |
| [docs/UI.md](docs/UI.md) | Which control to reach for, and what keeps the panel consistent |
| [docs/SITE_OWNERSHIP.md](docs/SITE_OWNERSHIP.md) | Why a deleted site's directory could change hands, and what stops it |
| [docs/AUDIT.md](docs/AUDIT.md) | What the panel records, who may read it, and what it cannot do |

---

## Security

Security is a first-class requirement, not a later phase. If you find a problem,
do not open a public issue — see [docs/PHASE0.md](docs/PHASE0.md) and
[docs/PHASE1.md](docs/PHASE1.md) for the boundaries that are already enforced
and tested.

Currently enforced: Argon2id password hashing, opaque revocable sessions,
single-use refresh tokens with theft detection, TOTP two-factor, RBAC,
per-account and per-IP login throttling, encrypted secrets at rest, and an
append-only audit trail — readable in the panel under Audit trail, behind a
permission of its own ([docs/AUDIT.md](docs/AUDIT.md)).

On the Agent: kernel-verified caller identity plus a shared token, an operation
allowlist that cannot drift from its handlers, argv-only command execution with
no shell anywhere, path validation against traversal and symlink escape, and a
separate append-only audit trail that survives the database being unreachable.

All of that is attacked rather than asserted: [docs/PHASE24.md](docs/PHASE24.md)
is the audit, and `make docker-test-hardening` is the part of it that runs. It
sweeps every route registered in the source with an account holding no
permissions, so a route that loses its guard fails a test rather than waiting to
be noticed. The findings — including one gap left open on purpose — are in that
document.

## License

MIT. See [LICENSE](LICENSE).
