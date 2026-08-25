# Development Guide

## Prerequisites

Docker Desktop with Compose v2. Nothing else is required on the host — Go and
Node.js run inside containers, so a Windows host never executes Linux-specific
logic (ARCHITECTURE.md section 16).

Optional for faster local iteration: Node.js 22+ to run the frontend outside
Docker, and Go 1.23+ to run Go tests without the container round-trip.

---

## Starting the stack

```bash
make dev
```

Then create the first administrator. Credentials come from the environment so
they never land in the process list or shell history:

```bash
JOTHOST_ADMIN_USERNAME=admin JOTHOST_ADMIN_PASSWORD='a-long-password' make create-admin
```

Sign in at http://localhost:8081.

Verify:

```bash
make ps
```

Every service should report `healthy`. If `api` is unhealthy, check whether the
Agent came up first:

```bash
docker compose logs agent
docker compose logs api
```

The API's readiness endpoint reports each dependency separately:

```bash
curl http://localhost:8080/readyz
```

---

## Ports

| Service | Host port | Purpose |
|---|---|---|
| nginx | 8081 | The panel — use this |
| api | 8080 | Direct API access for debugging |
| frontend | 5173 | Vite dev server, direct |
| postgres | 5432 | psql from the host |
| redis | 6379 | redis-cli from the host |

Override any of them in `.env`.

---

## Working on the Go services

```bash
make go-test     # unit and security tests
make go-lint     # go vet plus a gofmt check
make go-fmt      # format in place
make go-build    # compile everything
```

Compose runs the `runtime` image target, so Go changes need a rebuild:

```bash
docker compose up -d --build api agent
```

For a tighter loop, switch the service's `target:` to `dev` in
`docker-compose.yml` and bind-mount the source; the `dev` stage runs `go run`.

### Module layout

```text
shared/   logger, protocol, version   — imported by both services
api/      HTTP handlers, services, agent client — unprivileged
agent/    socket transport, operation registry — privileged in production
```

`api` and `agent` reference `shared` through a `replace` directive, so a change
to `shared` is picked up without publishing anything.

---

## Working on the frontend

Inside Docker, Vite hot-reloads from the bind mount. On the host:

```bash
cd frontend
npm install
npm run dev
```

```bash
npm run test        # vitest
npm run lint        # eslint
npm run typecheck   # tsc
npm run build       # production build
```

### Layering

```text
page  →  feature hook  →  API service  →  fetch
```

Components must not call `fetch` directly and must not contain business logic
(CLAUDE.md section 10). Server state belongs to TanStack Query; Zustand holds
UI-only state.

Adding a feature means adding `frontend/src/features/<name>/` with `api.ts`,
`types.ts`, `hooks.ts`, and `components/`.

---

## Database migrations

```bash
make migrate          # apply pending migrations
make migrate-status   # show what is applied
```

The API applies pending migrations at startup by default (`AUTO_MIGRATE`).
Set it to `false` to manage them by hand; the API then verifies the schema on
boot and refuses to serve one it does not recognise.

Adding a migration means adding a **pair** of files (CLAUDE.md section 8):

```text
migrations/000N_description.up.sql
migrations/000N_description.down.sql
```

Versions must be contiguous from `0001` and every up file needs a down file —
the loader rejects the alternative, so an un-reversible migration fails at
startup rather than during an incident. `TestUpAndDownRoundTrip` applies and
rolls back the whole set on every run, so a down migration that does not
actually reverse its up will fail the build.

Never edit a migration that has been applied anywhere. Add a new one.

---

## Authentication

The panel uses **opaque bearer tokens**, not JWTs: access tokens are random
strings resolved against Redis, which makes logout take effect immediately.
Refresh tokens rotate on every use and reuse is treated as theft. The reasoning
is in [PHASE1.md section 3.1](PHASE1.md).

Guarding a new endpoint:

```go
mux.Handle("GET /api/v1/websites",
    authService.RequireAuth(
        authService.RequirePermission(rbac.PermWebsiteView)(handler)))
```

`RequirePermission` must sit inside `RequireAuth`; used alone it returns 500
rather than silently allowing the request.

Adding a permission means adding it to migration `0002_seed_rbac`'s catalogue
*and* to the constants in `api/internal/rbac` and
`frontend/src/features/auth/permissions.ts`, so a typo is a compile error
rather than a check that silently never passes.

Never log a token, a password, or a 2FA secret. The shared logger redacts known
key names, but the reliable habit is not to pass them.

---

## Adding an Agent operation

Every privileged operation follows the same path. Skipping a step breaks a
security invariant, so all four are required.

1. **Declare the type** in `shared/protocol/protocol.go`:

   ```go
   const OperationWebsiteCreate OperationType = "website.create"
   ```

2. **Add it to the allowlist** in the same file's `allowedOperations` map.
   Without this, dispatch rejects it — that is the point.

3. **Register a handler** in `agent/internal/operations/registry.go` via
   `mustRegister`. Registering an operation that is not allowlisted panics at
   startup, which keeps the two lists in sync.

4. **Validate the payload inside the handler.** Decode into a typed struct.
   Never concatenate a payload value into a command string; never pass one to a
   shell.

Then add tests: a success case, a rejection case for a malformed payload, and a
rejection case for an injection-shaped input.

---

## Testing

```bash
make test                     # Go and frontend unit tests
make docker-test              # everything, in containers
make docker-test-integration  # Phase 0 black-box checks
make docker-test-auth         # Phase 1 authentication checks
make verify                   # what CI runs
```

`tests/integration/phase0_smoke.sh` asserts the response envelope, request IDs,
readiness reporting, the reverse proxy, and that the Agent answers no HTTP
port. `tests/integration/phase1_auth.sh` asserts the login contract, that
protected routes refuse anonymous callers, refresh rotation and reuse
detection, immediate logout, and login throttling.

### Database-backed Go tests

Repository, session, and auth tests run against a real PostgreSQL and Redis
rather than mocks, because the behaviour that matters lives in the engines: the
append-only trigger, atomic token rotation, unique constraints, key expiry.

`make go-test` and `make docker-test` start throwaway instances automatically.
Running `go test` by hand needs them pointed out, and needs `-p 1` because the
suite shares one database:

```bash
export TEST_DATABASE_URL='postgres://jothost_test:jothost_test@localhost:5432/jothost_test?sslmode=disable'
```

```bash
export TEST_REDIS_URL='redis://localhost:6379/0'
```

```bash
cd api && go test -p 1 ./...
```

Without those variables the database-backed tests skip rather than fail, so a
bare checkout still runs green.

---

## Logs

All services emit structured JSON:

```bash
docker compose logs -f api | grep request_id
```

Sensitive keys — passwords, tokens, JWTs, private keys — are redacted by the
shared logger before any handler sees them (CLAUDE.md section 14). Add a new
sensitive key to `redactedKeys` in `shared/logger/logger.go` rather than
remembering not to log it.

---

## Troubleshooting

**`api` is unhealthy but `postgres` is healthy.** Check `/readyz` — it names the
failing dependency. Most often the Agent socket volume did not mount; confirm
both services mount `agent-socket` at `/run/jothost`.

**Frontend shows no changes.** The bind mount does not deliver inotify events on
Windows, which is why `vite.config.ts` enables polling. If it still stalls,
restart the service.

**`npm ci` fails in the container after editing `package.json`.**
`node_modules` lives in an anonymous volume. Rebuild rather than restart:

```bash
docker compose up -d --build frontend
```

**Port already in use.** Change the port in `.env` and run `make dev` again.

**Reset everything.**

```bash
make clean && make dev
```

---

## Commit conventions

```text
feat: add website creation API
fix: prevent path traversal in file manager
test: add PHP pool integration tests
refactor: separate nginx provider
docs: update API specification
```

Small commits, one concern each.

---

## Before finishing a task

```bash
make lint
make test
make docker-test
```

Then check the task's acceptance criteria in TASKS.md and update its checkbox.
