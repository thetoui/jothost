# JotHost Panel

A self-hosted Linux hosting control panel: websites, PHP and Node.js applications,
databases, SSL, files, cron, backups, monitoring, and security from one web interface.

**Status:** Phase 0 (project foundation) complete. See [TASKS.md](TASKS.md) for the
phase plan and [docs/PHASE0.md](docs/PHASE0.md) for what this foundation does and
does not include.

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

Without Make:

```bash
docker compose up -d --build
```

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

---

## Repository layout

```text
api/        Go HTTP API — unprivileged
agent/      Go Host Agent — privileged, Unix socket only
shared/     Go module shared by api and agent (logger, protocol, version)
frontend/   React + TypeScript + Vite + Tailwind
docker/     Dockerfiles and Nginx configuration
migrations/ SQL migrations (Phase 1 onward)
tests/      Integration and end-to-end tests
docs/       Development and phase documentation
```

---

## Configuration

Configuration comes from the environment; nothing is hard-coded and secrets are
never committed. Start from [.env.example](.env.example). Every variable is
validated at startup — the API and Agent refuse to boot on invalid
configuration rather than running with a surprising default.

---

## Documentation

| Document | Contents |
|---|---|
| [PRD.md](PRD.md) | Product requirements |
| [ARCHITECTURE.md](ARCHITECTURE.md) | System design and boundaries |
| [DATABASE.md](DATABASE.md) | Schema design |
| [API_SPEC.md](API_SPEC.md) | Endpoint contract |
| [TASKS.md](TASKS.md) | Phase plan and status |
| [CLAUDE.md](CLAUDE.md) | Engineering rules |
| [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | Day-to-day workflow |
| [docs/PHASE0.md](docs/PHASE0.md) | Phase 0 scope, decisions, and known limitations |

---

## Security

Security is a first-class requirement, not a later phase. If you find a problem,
do not open a public issue — see [docs/PHASE0.md](docs/PHASE0.md) for the
boundaries that are already enforced and tested.
