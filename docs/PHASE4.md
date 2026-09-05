# Phase 4 — Website Manager

**Status:** Complete
**Scope:** TASKS.md Phase 4, PRD.md section 8, API_SPEC.md sections 6–8 and 28,
DATABASE.md tables 9, 10, and 24

Phase 3 made the host readable. Phase 4 makes it writable: the panel now
creates a website that a browser can actually load.

Websites in this phase are static. PHP is Phase 5 and SSL is Phase 6, and both
are refused explicitly rather than accepted and ignored.

---

## 1. What was built

### 1.1 The shape of a change

Provisioning a website takes seconds and touches the filesystem, `/etc/passwd`,
and the web server's configuration. It cannot be done inside a request, so
every change follows one path:

```text
API request → website row (creating) → job row (PENDING)
    → worker claims it → Agent operation → nginx reload
    → job SUCCESS/FAILED → website row (active/failed)
```

The important property is at the end. A website is only ever called `active`
because the Agent reported success. Nothing optimistically marks it working.

### 1.2 Schema

Migration `0005_websites_and_jobs` adds `jobs` (DATABASE.md §24), `websites`
(§9), and `domains` (§10).

Constraints carry the rules the code also enforces, so a future code path that
forgets to validate still cannot write a bad row: a domain must match the
hostname pattern, a document root must be absolute and free of `..`, exactly
one primary domain exists per website, and a system user is unique across all
websites.

### 1.3 The job queue

ARCHITECTURE.md §9 sketches a Redis queue. The `jobs` table is used instead.
`FOR UPDATE SKIP LOCKED` gives the same claim-once semantics, and the table
survives a restart where a Redis list does not — which matters when the queued
work is "finish creating this user's website". The trade-off is recorded in
§3.2 below.

One worker runs per API process, one job at a time. Infrastructure operations
share global state — nginx's config directory, the passwd file, the site root —
so two at once invites one reloading the other's half-written config.
Throughput is not the constraint; a control panel queues a handful of jobs.

A job left `RUNNING` by a killed process is requeued at startup. Without that
it is stuck forever: nothing is executing it and nothing will claim it again.

### 1.4 Agent providers

| Package | Responsibility |
|---|---|
| `agent/internal/nginx` | Renders a vhost from a template, validates with `nginx -t`, reloads, **rolls back on failure** |
| `agent/internal/sites` | Creates the directory tree and the system account, sets ownership |

The rollback is the part that matters. A config is written, validated, and — if
`nginx -t` rejects it — restored byte for byte, including "no file existed
before". A panel that can break the web server for every hosted site with one
bad vhost is not usable.

### 1.5 Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/websites` | List hosted sites |
| `POST /api/v1/websites` | Record a site and queue provisioning (201, returns site + job) |
| `GET /api/v1/websites/{id}` | One site with its domains |
| `PATCH /api/v1/websites/{id}` | Change the display name |
| `DELETE /api/v1/websites/{id}` | Queue removal (202, returns the job) |
| `GET /api/v1/websites/{id}/domains` | A site's hostnames |
| `POST /api/v1/websites/{id}/domains` | Attach an alias, queue a vhost update |
| `DELETE /api/v1/domains/{id}` | Detach a hostname, queue a vhost update |
| `GET /api/v1/jobs` | Job list, filterable by status and resource |
| `GET /api/v1/jobs/{id}` | One job |
| `POST /api/v1/jobs/{id}/cancel` | Cancel a job that has not started |

Each verb carries its own permission: `website.view` to look, `website.create`
to add, `website.update` to change, `website.delete` to remove.

### 1.6 UI

`/websites` lists sites and creates them; `/websites/:id` shows one site, its
domains, and the work run against it. A site with a change in flight is polled
every two seconds; one that is settled is not polled at all.

---

## 2. Security

### 2.1 The permission model that makes a site serve

A site's directories are owned by **the site's own user** and group-owned by
**the web server's group**, mode `0750`.

Group-owning by the site's own group looks tidier and produces a site that
refuses every visitor, because nginx cannot traverse the directory. That
mistake is invisible until the first request — it was made and fixed during
this phase, and `agent/internal/sites/filesystem.go` now carries the reasoning
so it is not made again.

Because the web server group is what makes a site readable, provisioning
**refuses to start** when that group cannot be resolved. The alternative is a
site that provisions cleanly and then returns 403 to every request.

### 2.2 Isolation

Each website gets its own system account, so a compromised site cannot read its
neighbour's files. This is verified, not assumed: the integration test has one
site's user attempt to read another's document root and requires a permission
denial.

### 2.3 Untrusted input

A domain reaches both an nginx `server_name` and a filesystem path. It is
validated once, in `shared/validate`, imported by **both** the API and the
Agent so the two cannot drift. The Agent validates again on arrival rather than
trusting the API — it is the process running as root.

Paths are never accepted from a client. The API computes the document root from
the validated domain; the Agent resolves it against an allowed root and refuses
anything outside. `nginx.validatePath` additionally rejects newlines, braces,
and semicolons, because a path containing one could close a directive and
inject configuration.

No user input is ever passed to a shell. Commands are allowlisted by name with
explicit arguments (CLAUDE.md §6), and ownership is set with `os.Chown` rather
than by invoking `chown`.

### 2.4 What the vhost denies

Dotfiles return 403. Application configuration lives in `.env`, and serving it
hands out database passwords and API keys to anyone who guesses the name. A
hostname no site claims returns 404 rather than being served by whichever vhost
loaded first.

### 2.5 Audit

`website.create`, `website.delete`, `domain.create`, and `domain.delete` are
recorded with the actor, the domain, and the job id, so a change on the host
can be traced back to the request that asked for it.

---

## 3. Decisions and deviations

### 3.1 The `system_username` column

PostgreSQL 16 made `SYSTEM_USER` a reserved keyword (SQL:2023). DATABASE.md §9
names the column `system_user`, which is a syntax error unquoted — and worse,
in some contexts resolves to the built-in function instead of failing loudly.

The column is therefore `system_username`, aliased back to `system_user` in
every query. The API contract, the JSON field, and the Go struct all keep the
documented name; only the physical column differs.

### 3.2 Jobs in Postgres rather than Redis

Deviation from ARCHITECTURE.md §9, described in §1.3. The queue must survive an
API restart, and the deployment is single-instance.

### 3.3 REST polling rather than a WebSocket

API_SPEC.md §28 describes `WS /ws/jobs/:id`. Polling `GET /api/v1/jobs/{id}` is
what shipped. Provisioning takes seconds, the UI polls only while something is
actually in flight, and a WebSocket adds a connection lifecycle, an auth
handshake, and a reconnect path for a latency improvement nobody would notice
at this duration. The endpoint remains available to add later without changing
the job model.

### 3.4 nginx in the agent container

In production the Agent runs on a host that already has nginx. In development
the agent container *is* the managed host, so it now runs nginx itself and
serves created sites on port 80. `shadow` is installed for `useradd`, which
BusyBox's `adduser` cannot fully replace for system accounts.

This changed a Phase 0 assertion that the agent container answered on no TCP
port. The property that actually matters — the Agent protocol is reachable only
over its Unix socket — is unchanged and still checked; port 80 is now verified
to be a web server that does not speak the agent protocol.

---

## 4. Testing

| Layer | Coverage |
|---|---|
| `api/internal/jobs` | Claim exclusivity under 8 concurrent workers, ordering, progress clamping, cancel rules, stale requeue, worker success/failure/agent-unavailable |
| `api/internal/websites` | Domain normalisation and rejection, SSL refusal, duplicate domains, per-site users, truncation collisions, delete ordering, reconciliation |
| `api/internal/server` | Route authorisation, RBAC per verb, UUID validation, unknown-field rejection, status codes |
| `agent/internal/nginx`, `sites` | Template rendering, config rollback, path validation |
| `frontend` | Domain validation, status presentation, list/create/permission behaviour |
| `tests/integration/phase4_websites.sh` | 41 black-box checks against the live stack |

The acceptance criterion from TASKS.md — create a website, request it over
HTTP, receive a response — is checked against a real nginx serving a real
directory, not a mock.

Run it with:

```bash
make docker-test-websites
```

---

## 5. Known limitations

1. **Redirect domains are refused.** The Agent can write a redirect vhost but
   has no way to remove a stale one, so accepting the type would create
   configuration the panel could never take back. The schema supports it; the
   API returns 400 until the Agent manages the full lifecycle.
2. **No PHP.** Sites are static. The vhost deliberately contains no FastCGI
   block — Phase 5.
3. **No HTTPS.** `ssl_enabled` and `https_redirect` are refused rather than
   ignored, because a user who believes their site is encrypted when it is not
   is worse off than one who is told it is unavailable. Phase 6.
4. **Website logs are readable but not streamed.** The Agent exposes a bounded
   tail; there is no follow mode.
5. **Single server.** `server_id` exists on every website and the code passes it
   through, but the panel manages one host.
6. **A failed delete leaves the row.** This is deliberate — the files may still
   be on the host, and a vanished row would leave orphaned directories nobody
   knows about — but it means a site can sit in `failed` needing a retry.

   The worry in that sentence turned out to be right, and about the *successful*
   path rather than the failed one. A successful delete kept the files and
   removed the account, which put the uid back in the allocation pool while the
   files still carried it; a few sites later the number was issued again and an
   unrelated customer owned a directory nobody had given them. Deletion now
   reassigns a retained tree to root before the account goes, and provisioning
   refuses a directory that already holds another account's files rather than
   adopting it. `docs/SITE_OWNERSHIP.md` has the whole account.
7. **No quota or resource limits per site.** A single site can fill the disk.
