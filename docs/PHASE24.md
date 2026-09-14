# Phase 24 — Production Hardening

The security audit, and the suites that keep it true.

Every other phase built something. This one attacks what the others built, and
its deliverable is four suites that go on attacking it: a route that loses its
guard next month has to fail a test rather than wait to be noticed.

```bash
make docker-test-hardening
```

---

## 1. The rule this phase is built on

**A refusal proves nothing unless the request reached the code that refused
it.**

That is not a maxim; it is what the first day of writing this produced. A set
of path-traversal probes came back refused, one after another, and every one of
them was a lie — `curl --data-urlencode` never delivered the parameter, so the
answer was *"you gave me no path"* dressed up as *"I will not read that path"*.
Eleven green ticks, testing nothing.

So every section of every suite begins with a **control**: the same endpoint,
the same shape of request, with a value that must be *accepted*. A failed
control is reported differently from a failed refusal, because they mean
opposite things — a failed refusal is a hole in the panel, a failed control is
a hole in the test — and the summary says how many of each there were.

The discipline caught four more hollow checks after that one, all in this
phase's own code, and section 6 lists them. That is the honest reason to write
it down: the technique is not clever, it is necessary, and the people who most
need it are the ones already sure their tests work.

---

## 2. What was examined

The audit is in four parts, by what an attacker actually has.

**From outside, with nothing** — `tests/integration/phase24_security.sh`. The
unauthenticated surface, authentication, the RBAC boundary, path traversal,
command injection, privilege escalation, rate limiting, and what the panel
returns about itself.

**From inside the machine** — `tests/integration/phase24_agent.sh`. The
privilege boundary itself: the socket between the unprivileged API and the
privileged Agent, and what is readable on disk by whom.

**When things break** — `tests/recovery/phase24_recovery.sh`. The three ways
the panel can be lost, and whether the hosting goes with it.

**Under load** — `tests/integration/phase24_load.sh`. Not a benchmark:
correctness when several people use it at once.

Two of this phase's thirteen bullets are drilled by suites that already
existed, and repeating them here would have been theatre rather than coverage:

| Bullet | Where it is actually tested |
|---|---|
| Backup restore test | `phase14_backup.sh` — backs up, destroys the file *and* the row, restores both, and refuses an archive whose checksum changed |
| Firewall recovery test | `phase16_firewall.sh` — applies a rule provisionally, never confirms it, and watches the host put itself back |

---

## 3. The RBAC sweep, and why it reads the source

The strongest check in the suite is also the simplest: **every route registered
in the source is called with an account that holds no permissions at all, and
must refuse.**

Two decisions make it worth having.

The adversary is a *customer* account — a tier the panel creates with no roles
whatsoever. Not "a user with fewer permissions": one with none. Its profile
comes back `"permissions":[]`, which is asserted before the sweep runs.

And the route list is read out of `api/internal/**.go` at run time rather than
kept in the test. A list in the test drifts the first week; a list read from
the source means a route added next month without a guard fails this suite the
first time anybody runs it. 259 routes were swept; 259 refused.

The two exceptions are deliberate and are checked individually rather than
skipped:

```text
GET    /api/v1/tenancy/impersonation
DELETE /api/v1/tenancy/impersonation
```

An impersonated session does not carry `tenant.impersonate` — that is the point
of impersonation, and section 7 of Phase 22 explains it — so a session stripped
of the permission must still be able to *stop being one*. Both were checked for
what they actually do: reading from an ordinary session answers
`"impersonation":null` and names nobody, and ending one that is not an
impersonation is refused.

---

## 4. What the audit confirmed

Each of these is a property the panel claims elsewhere in its documentation.
None of them had been checked from outside before.

**The unauthenticated surface is eight routes**, and the suite enumerates it
from the source rather than from memory: login, refresh, the two-factor
verification, the deploy webhook, and four health endpoints. Anything else
appearing there is a route somebody forgot to guard.

**Login does not enumerate users.** An unknown username and a wrong password
produce byte-identical responses. The timing is equalised in code — Phase 1
verifies a dummy hash for an account that does not exist — and the messages are
now checked to match as well.

**A token is only a token in the Authorization header.** Query string, cookie
and Basic are all refused. A token accepted from a query string is a token in
the access log, in the referrer of every outbound link, and in browser history.

**Revocation is immediate.** Logout stops the access token that instant, not at
expiry — which is why they are opaque and server-side rather than signed.

**A replayed refresh token tears down every session the account has.** The
panel cannot tell which of the two holders is the thief, so it stops trusting
all of them. This is worth stating because it is easy to mistake for
over-reaction, and because it bit this very suite: the replay test was
originally run against the administrator and ended the suite's own session
halfway through, leaving every section below it reporting green while
authenticated as nobody. It now runs against a throwaway account, and the
comment above it says why.

**Paths cannot leave their root** — not through `..`, not through `//`, not
through `/proc`, and *not through a symlink*, which normalisation alone would
miss. The symlink probes are in the Agent suite rather than the API suite,
because they need a real link on a real filesystem: a probe that could not
create the link would have reported "refused" for a file that was never there.

**Values that would close a command are refused by shape.** Domains, database
names, git remotes, firewall ports and cron schedules were each given the
characters that end a shell word, an nginx directive or an SQL statement. All
refused — and the static sweep confirms why: no code path in either binary
hands a shell a string it did not write itself.

**The Agent has three locks and each was tested past the ones before it.** An
account outside the group cannot connect at all; put *into* the group, it is
refused by uid; as root — which defeats both — it is refused for having no
token. Testing only the outermost would have proved the outermost.

**The Agent holds no network port**, refuses an operation outside its
allowlist, refuses a payload past 1 MiB while accepting one below it, and is
still answering after being sent junk.

**A panel outage is not a hosting outage.** With the API stopped, every website
went on being served; with the database stopped, likewise. That claim underpins
the whole architecture and nothing had ever checked it.

**Under load, nothing failed and nothing was answered wrongly.** 2,880
concurrent authenticated requests: every one 200, none throttled, no server
errors, and every response that named a user named the same one. Six
simultaneous attempts to create the same account produced exactly one success
and five clean refusals — uniqueness is the database's answer, not a check in
Go that two processes can both pass.

---

## 5. Findings

Nothing exploitable was found. Four things are worth recording anyway, because
an audit that reports only "all clear" is an audit nobody can check.

### 5.1 `audit.view` is a permission with no endpoint behind it

The panel writes an audit trail — every sensitive action, with actor, address
and outcome — and **there is no way to read it through the API.** The
permission is defined, granted to the admin role, and guards nothing.

This is not a vulnerability; it is a gap with a consequence. An audit log
nobody can read is one nobody reads, and the panel's answer to "who deleted
that website" is currently "connect to PostgreSQL". It is also how this was
found: two checks in this very suite pointed at `/api/v1/audit` and had been
passing on its 404, which is what prompted the stricter `denied()` helper that
refuses to accept 404 as a permission refusal.

Not fixed *here*, under CLAUDE.md section 21: an audit-log endpoint is a
feature, and this phase tests the finished system rather than extending it.

**Since fixed.** `GET /api/v1/audit` and `/api/v1/audit/actions` now stand
behind that permission, with a page in the panel to read them. The finding
above is left as written because it is the record of what the audit found, and
because of how it was found: two checks in this suite were passing on the 404,
which is what prompted the stricter `denied()` helper. The route sweep picked
up both new endpoints on its next run without being told — 259 routes became
261, and both refused an account with no permissions.

### 5.2 Version and readiness are unauthenticated

`/api/v1/version` returns the version, commit and build date to anybody;
`/readyz` names which of PostgreSQL, Redis and the Agent is up.

Deliberate — a load balancer has no token — and bounded: neither discloses a
hostname, a connection string or a credential, which is checked. The residual
risk is ordinary version disclosure, which tells an attacker which
vulnerabilities to try. It is accepted rather than overlooked, and the
alternative (health checks that require a credential) trades a real
operational property for a small one.

### 5.3 Refresh replay revokes every session, not one

Recorded as a decision rather than a defect. It is the right behaviour and it
is surprising: signing in on a second device does not trigger it, but a stolen
token that gets used does, and it logs the legitimate holder out everywhere.
Somebody reading a support ticket about "I was logged out of everything" should
be able to find this sentence.

### 5.4 A stale init state left the development stack silently broken

Found by the recovery drill, and fixed here because it is a defect rather than
a feature: `docker compose stop agent && start agent` left OpenRC's state and
nginx's pidfile in `/run` pointing at processes that no longer existed. A real
host clears `/run` on boot; a restarted container does not.

The result was a stack that looked entirely well — the Agent up, the socket
answering, every operation accepted — and in which **every website operation
failed at its last step** with `site configured but not served: kill(181, 1)
failed`. `docker/agent-entrypoint.sh` now clears what a boot clears, because
running the entrypoint *is* that container's boot.

---

## 6. What running it found in the tests themselves

Five hollow checks, all caught by the controls, all in this phase's own code.
They are listed because the failure mode is the one this phase exists to
prevent and it does not spare the person writing the tests.

- **`curl --data-urlencode` never delivered the parameter.** Eleven traversal
  probes "refused" a path the handler never saw.
- **BusyBox `grep` has no `--include`.** It does not fail when given one — it
  matches nothing, silently. The unauthenticated-surface check compared an
  empty list against its expectations and passed; the RBAC sweep reported *"all
  0 routes refuse"* and called it green. Both now count what they read and
  assert the count before comparing anything.
- **The refresh-replay test ended the suite's own session**, so every section
  after it ran unauthenticated and every refusal was really a 401.
- **`POST /files/file` takes a directory and a name**, not a whole path. The
  control file was never created, so the section's control failed — which is
  precisely what a control is for.
- **`grep -c` prints `0` *and* exits non-zero.** `|| echo 0` then printed a
  second zero, and `"0\n0"` compared unequal to `"0"`, failing a section with
  nothing wrong with it. The same shape as `curl`'s `|| echo 000`, which turned
  every connection failure into `HTTP 000000`.

Two smaller ones: a `case` pattern of `*0` matches `750` as readily as `705`,
which reported forty-five world-readable directories on a filesystem that was
entirely correct; and a 100 KB payload was called oversized against a 1 MiB
limit.

---

## 7. Static review

Three properties CLAUDE.md requires, checked across both binaries rather than
sampled:

- **No shell.** Nothing in `api/` or `agent/` passes a value to `sh -c`,
  `bash -c` or `eval`. Every privileged command is an allowlisted spec with an
  argv the Agent builds.
- **No secret reaches a logger.** No call site hands a logger a field named
  password, token, secret or key.
- **No SQL is concatenated with a value.** Every query is parameterised; the
  only string building is of column lists and filters the code itself writes.

The dependency surface is the other half of a supply chain. The **Agent has no
third-party dependencies at all** — the privileged binary is the standard
library and this repository's own `shared` module, which is why it can be
audited by reading it. The API has five direct: pgx, go-redis, and
`golang.org/x/crypto`, plus their transitive set.

---

## 8. Verification

```bash
make docker-test-hardening
```

| Suite | What it drives | Checks |
|---|---|---|
| `phase24_security.sh` | the API, as an attacker | 84 |
| `phase24_agent.sh` | the privilege boundary, from inside the host | 22 |
| `phase24_recovery.sh` | the deployment, by taking parts away | 22 |
| `phase24_load.sh` | correctness under concurrency | 10 |

The recovery drill runs on the machine that runs the stack rather than in a
container, because a drill that cannot stop a service is a description of a
drill.

---

## 9. Known limitations

- **No audit-log endpoint**, so the audit trail is not reviewable through the
  panel. Section 5.1; the clearest work left.
  **Since fixed** (985c20f): `GET /api/v1/audit`, behind `audit.view`.
- **The "sites keep serving without the Agent" half of drill 2 is not checked
  in development.** In production nginx is its own systemd service and stopping
  `jothost-agent` leaves it running; in the development stack the Agent's
  container *is* the machine it manages, so the two stop together. The claim is
  checked where it is real — on the clean host the installer builds — and the
  drill says so rather than asserting something the topology cannot provide.
- **The load figures are not a benchmark.** 2,880 requests in ~200 seconds on a
  laptop, inside Docker, is a fact about the laptop. Nothing here asserts a
  throughput, and nothing should be quoted as one.
- **No fuzzing.** The injection probes are a fixed list of shapes chosen for
  what they would close. A fuzzer over the validators would be a better use of
  a day than lengthening the list by hand.
- **No dependency-vulnerability scanning in CI.** The dependency set is
  enumerated here and pinned; nothing checks it against an advisory database on
  a schedule. `govulncheck` in the test profile is a small piece of work and is
  not done.
  **Since fixed in part** (2029513): CI runs `govulncheck` over all three
  modules on every push and pull request. Its first run found 35
  vulnerabilities, cleared by moving to Go 1.26. It still does not run on a
  schedule.
- **No penetration test by somebody else.** Everything here was written by the
  same hand that wrote what it attacks, which is the fundamental limit of a
  self-audit and no amount of care removes it.
- **TLS is not exercised in these suites.** They run inside the compose
  network, over plain HTTP, against the API directly. What terminates TLS is
  nginx, and the installer's checks cover its configuration.
