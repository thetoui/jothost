# Phase 0 — Project Foundation

**Status:** Complete
**Scope:** TASKS.md sections 0.1 through 0.5

Phase 0 builds the skeleton every later phase hangs off: repository structure,
a containerised development environment, two Go services with configuration,
logging, error handling, request IDs and graceful shutdown, a React shell, and
a test and CI baseline.

It deliberately implements **no** product features. There is no authentication,
no database schema, and no privileged operation. Those are Phases 1, 1, and 2
respectively.

---

## 1. What was built

### 1.1 Repository (TASKS 0.1)

Monorepo with `api/`, `agent/`, `shared/`, `frontend/`, `docker/`,
`migrations/`, `scripts/`, `tests/`, `docs/`, matching ARCHITECTURE.md
section 3. Git initialised; README added; the five specification documents were
already present.

### 1.2 Docker (TASKS 0.2)

`docker-compose.yml` defines six services — `postgres`, `redis`, `agent`,
`api`, `frontend`, `nginx` — each with a health check and ordered startup via
`depends_on: condition: service_healthy`.

Two design points are load-bearing:

- **The Agent publishes no port.** API and Agent share a named volume mounted at
  `/run/jothost`; the socket inside it is the only channel between them.
- **The Agent's health check speaks its own protocol.** `jothost-agent -ping`
  connects to the socket and issues `agent.ping`, so a "healthy" Agent means a
  reachable Agent, not merely a running process.

### 1.3 Go services (TASKS 0.3)

Three modules: `shared`, `api`, `agent`. `api` and `agent` consume `shared`
through a local `replace` directive.

| Concern | Implementation |
|---|---|
| Configuration | `config.Load()` in each service; reports *all* validation problems at once |
| Logging | `shared/logger` — `log/slog` JSON, ARCHITECTURE.md §12 field names |
| Secret redaction | Sensitive attribute keys are replaced with `[REDACTED]` at every nesting depth |
| Errors | `httpx.APIError` with a stable code, HTTP status, and a non-serialised internal cause |
| Request ID | `crypto/rand` token, `req_` prefix, echoed in the body and the `X-Request-ID` header |
| Graceful shutdown | SIGINT/SIGTERM drain in-flight work within a configured timeout, then force-close |

### 1.4 React shell (TASKS 0.4)

Vite + TypeScript (strict, with `noUncheckedIndexedAccess` and
`exactOptionalPropertyTypes`) + Tailwind + React Router + TanStack Query +
Zustand.

The layering rule from CLAUDE.md §10 is enforced structurally: components call
feature hooks, feature hooks call services, only `services/apiClient.ts` calls
`fetch`. An ESLint rule rejects `fetch` used as a bare global elsewhere.

Sidebar entries for future-phase modules render as disabled text rather than
links, so the navigation shows the intended information architecture without
offering dead ends.

### 1.5 Testing and CI (TASKS 0.5)

| Suite | Location | Count |
|---|---|---|
| Go unit and security tests | `*_test.go` across the three modules | 67 |
| Frontend tests | `frontend/src/**/__tests__` | 17 |
| Docker integration | `tests/integration/phase0_smoke.sh` | 23 checks |

CI (`.github/workflows/ci.yml`) runs Go vet/gofmt/tests, frontend
lint/typecheck/test/build, and the containerised integration suite.

---

## 2. Security boundaries already enforced

Phase 0 has no privileged operations, but the machinery that will constrain them
exists and is tested now, while it is cheap to get right.

| Boundary | Enforcement | Test |
|---|---|---|
| No arbitrary operations | `protocol.Validate()` checks an allowlist before dispatch | `protocol_test.go`, `registry_test.go` |
| Allowlist cannot drift | `mustRegister` panics at startup if a handler is not allowlisted | `TestRegisterRejectsNonAllowlistedOperation` |
| Agent not network-reachable | Unix socket only; no `EXPOSE`, no published port | `TestAgentDoesNotListenOnTCP`, integration check |
| Socket access restricted | `chmod 0660` applied after bind, overriding umask; access granted by group, never by widening the mode | `TestSocketPermissionsRestrictAccess`, `TestSocketDirectoryIsNotWorldAccessible` |
| No destructive socket setup | `Listen` refuses to unlink a non-socket file | `TestListenRefusesToDeleteNonSocketFile` |
| Resource abuse | 1 MiB request cap, bounded concurrency, per-operation timeout | `TestOversizedRequestIsRejected`, `TestConcurrentRequests` |
| No internal disclosure | Unknown errors collapse to `INTERNAL_ERROR` | `TestErrorHidesInternalDetail` |
| No secrets in logs | Key-based redaction in the shared logger | `TestSensitiveFieldsAreRedacted` |
| No log injection | Client-supplied `X-Request-ID` must match `^[A-Za-z0-9_.-]{8,64}$` | `TestRequestIDRejectsUnsafeClientValue` |
| No credential leakage in probes | Readiness reports generic reasons, never the connection URL | `TestReadinessFailsWhenDependenciesDown` |
| Path traversal in config | `AGENT_SOCKET` must be absolute and free of `..` | `TestLoadRejectsTraversalInSocketPath` |

---

## 3. Decisions and their rationale

### 3.1 The Go modules use only the standard library

`api`, `agent`, and `shared` have zero third-party dependencies. Go 1.22's
`net/http` pattern routing removed the last real reason to pull in a router at
this layer.

*Why:* the foundation is where a dependency tree is most expensive to unwind,
and a build that cannot fail on a registry outage is worth more than the small
convenience a router would add. `pgx` and a Redis client arrive in Phase 1, when
a schema exists to justify them.

### 3.2 Readiness probes dependencies at the TCP layer

`/readyz` dials Postgres and Redis rather than issuing `SELECT 1` / `PING`.

*Why:* no driver is present yet (see 3.1). This is a real limitation, recorded
in section 5 below, and Phase 1 replaces it once the connection pools exist.
The Agent check is already a genuine protocol-level probe.

### 3.3 One request per Agent connection

The Agent reads one line, replies, and closes.

*Why:* the Agent is the process that will eventually run as root. A trivial,
stateless connection lifecycle removes a whole class of state-confusion bugs,
and the cost — one socket connect per operation on a local Unix socket — is
negligible.

### 3.4 The API validates operations before dialling the Agent

`agentclient.Do` calls `Validate()` before opening the socket.

*Why:* defence in depth. The Agent validates too, but an invalid operation
should not consume a connection to the privileged process at all.

### 3.5 `agent -ping` doubles as the health check

*Why:* the runtime image needs no extra tooling, and the health check exercises
the same code path the API uses. A health check that tests a different mechanism
than production traffic can pass while production traffic fails.

### 3.6 The socket is shared by group, not by widening its mode

The Agent runs as root and the API runs as uid 10001, so a `0660` root-owned
socket is unreachable by the API. Rather than relaxing the mode to `0666`, the
Agent chowns the socket *and its directory* to the `jothost` group
(`AGENT_SOCKET_GROUP`, GID 10001 in both images).

*Why:* `0666` would let any process on the host talk to the privileged Agent.
Group ownership grants exactly one additional principal — the API user — and
mirrors how the production installer will set this up. A missing group is
logged and left alone rather than treated as fatal, because the fallback
(Agent's own group) is *more* restrictive, not less.

---

## 4. How to verify

```bash
make verify
```

Individually:

```bash
make go-test                  # Go unit and security tests
make fe-test                  # frontend tests
make lint                     # go vet, gofmt, eslint
make docker-test-integration  # black-box checks against the running stack
```

Manual smoke check:

```bash
curl http://localhost:8080/healthz
curl http://localhost:8080/readyz
curl http://localhost:8081/api/v1/version
```

---

## 5. Known limitations

Each of these is deliberate and scoped to a later phase.

1. **Readiness checks are TCP-level for Postgres and Redis.** They detect an
   unreachable dependency, not an unusable one — a Postgres that accepts
   connections but rejects authentication reports `up`. Phase 1 replaces this
   with a real query once the pool exists.
2. **No authentication.** Every endpoint is open. `/healthz`, `/readyz`,
   `/api/v1/health`, and `/api/v1/version` are intentionally public; Phase 1
   adds auth and the middleware to protect everything else.
3. **No database schema and no migrations.** `migrations/` holds only a README.
   Phase 1 introduces the migration runner along with the first tables.
4. **No rate limiting.** Redis is running but unused. Phase 1 adds login rate
   limiting, which is the first place it is actually needed.
5. **The only Agent operation is `agent.ping`.** No collectors, no
   infrastructure providers. Phase 2 adds them.
6. **The Agent container does not run as root** and has no host mounts. It is a
   protocol and lifecycle harness, not a functional Agent. Phase 2 introduces
   the privileged runtime configuration.
7. **No TLS between components in development.** Traffic is plain HTTP on the
   Docker network. Production terminates TLS at Nginx (Phase 23).
8. **Dev Dockerfile stages are defined but unused by compose**, which runs the
   `runtime` targets for API and Agent. Switch the `target:` to `dev` for
   `go run` iteration with a bind mount.
9. **`frontend/node_modules` lives in an anonymous volume.** After changing
   `package.json`, rebuild (`make dev`) rather than restarting, or the container
   keeps the old dependency set.

---

## 6. Next recommended task

**Phase 1 — Authentication.** In dependency order:

1. Migration runner and the `users`, `roles`, `user_roles`, `permissions`,
   `sessions`, and `audit_logs` tables from DATABASE.md.
2. A Postgres pool (`pgx`) and a Redis client; upgrade `/readyz` to real pings
   (limitation 1).
3. Password hashing (Argon2id), the user repository, and the login/refresh/logout
   endpoints from API_SPEC.md section 2.
4. Auth middleware, RBAC, and the audit log — extending the existing middleware
   chain rather than replacing it.
5. Login rate limiting on Redis, and TOTP 2FA.
