# Phase 2 — Host Agent

**Status:** Complete
**Scope:** TASKS.md Phase 2, ARCHITECTURE.md sections 6, 7, 9, and 10

Phase 0 gave the Agent a socket, an allowlist, and one no-op operation. Phase 2
fills that shell in: the Agent now reads the host, executes external programs
under constraint, runs work asynchronously, authenticates its callers, and
keeps its own audit trail.

This is the phase where the root-privileged attack surface first exists, so
most of the work is in the boundaries rather than the features.

---

## 1. What was built

### 1.1 Security primitives

Three packages carry the weight for every later phase.

| Package | Enforces |
|---|---|
| `agent/internal/command` | CLAUDE.md §6 — allowlisted binaries by absolute path, argv only, sanitised environment, timeouts, output caps, process-group kill |
| `agent/internal/pathsec` | CLAUDE.md §5 — normalise, resolve against an allowed root, reject `..`, reject symlink escape |
| `agent/internal/socket` (auth) | Kernel-supplied peer credentials plus a shared token |

There is no code path in `command` that reaches a shell. Programs are executed
directly with an argv slice, so a value like `; rm -rf /` arrives at the
program as one literal argument.

### 1.2 Collectors

Six collectors parse `/proc` and `/sys` directly rather than shelling out to
`ps`, `free`, or `df`.

| Collector | Source | Notes |
|---|---|---|
| CPU | `/proc/stat` | Usage is a delta between samples; the first sample reports counters with zero percentages |
| Memory | `/proc/meminfo` | "Used" derives from `MemAvailable`, not `MemFree` |
| Disk | `/proc/mounts` + `statfs(2)` | Excludes virtual filesystems; reports inodes as well as bytes |
| Network | `/proc/net/dev` | Per-interface rates; loopback excluded from totals |
| Load | `/proc/loadavg` | Normalised per core |
| Process | `/proc/<pid>/{status,stat,cmdline}` | Bounded and sorted; handles names containing spaces and parentheses |

The filesystem roots are configurable (`AGENT_PROC_ROOT`, `AGENT_SYS_ROOT`),
which is what makes the parsers testable against fixture trees with exact
expected values.

### 1.3 Services

`systemctl show` with an explicit property list, executed through the command
allowlist. Unit names are validated against a pattern before anything runs.
A host without systemd reports `UNSUPPORTED` rather than an internal error, and
`agent.info` advertises the capability so the API never offers a panel whose
every entry fails.

Phase 2 is **read-only**: start, stop, and enable arrive with Phase 12.

### 1.4 Asynchronous execution

Any allowlisted operation can be submitted with `mode: "async"`, returning a
job ID immediately. `job.status`, `job.cancel`, and `job.list` manage them.
Jobs carry progress, are bounded by concurrency, retention, and timeout limits,
and survive a panicking handler.

### 1.5 Authentication and audit

Two independent caller checks, and an append-only JSON-lines audit trail of
every operation — successful, failed, or refused — naming the calling process
by its kernel-supplied UID, GID, and PID.

### 1.6 Operator tooling

`jothost-agent -call <operation>` sends one operation to a running Agent and
prints the reply. It authenticates like any other caller, so it is a debugging
aid rather than a bypass, and it is what the Phase 2 integration checks drive.

---

## 2. Security properties, and the tests that pin them

| Property | Mechanism | Test |
|---|---|---|
| No shell, ever | argv-only execution; no `sh -c` anywhere | `TestArgumentsAreNotInterpretedByAShell` (canary file survives) |
| PATH cannot be hijacked | Absolute binary paths; PATH never searched | `TestPathIsNotSearched` |
| Credentials do not leak to children | Fixed environment, not the parent's | `TestChildEnvironmentIsSanitised` |
| A hung child cannot pin a worker | Process-group kill plus `WaitDelay` | `TestTimeoutIsEnforced` |
| Unbounded output cannot exhaust memory | Capped buffers reporting full writes | `TestOutputIsCapped` |
| Path traversal | `..` rejected on the raw input | `TestCleanRejectsTraversal` |
| Symlink escape | Resolution before the root check | `TestResolveRejectsSymlinkEscape` |
| Sibling-prefix confusion | Separator-aware containment | `TestResolveRejectsSiblingPrefix` |
| Null-byte truncation | Rejected before any syscall | `TestCleanRejectsMalformedPaths` |
| Devices are not files | `ResolveFile` requires a regular file | `TestResolveFileRejectsDevices` |
| Caller identity is unforgeable | `SO_PEERCRED`, resolved by the kernel | `TestPeerCredentialsAreReadFromTheKernel` |
| An unauthenticated caller learns nothing | Token checked before dispatch | `TestUnauthenticatedCallerNeverReachesAHandler` |
| Rejections do not disclose which check failed | One uniform message | `TestRejectionMessageDoesNotRevealWhichCheckFailed` |
| The token never leaks | Cleared after auth; absent from replies, logs, audit | `TestTokenIsNotEchoedOrLogged` |
| Command injection via payload | Unit-name pattern, validated before execution | `TestNameIsValidatedBeforeExecution` (systemctl never runs) |
| Silent argument dropping | `DisallowUnknownFields` on every payload | `TestProcessListValidatesPayload` |
| Operation allowlist cannot drift | Registry and allowlist cross-checked both ways | `TestRegisteredOperationsMatchTheAllowlist` |
| Resource abuse | Bounded processes, jobs, concurrency, request size | `TestProcessListCapsTheLimit`, `TestConcurrencyLimit` |
| A panic cannot take down a root daemon | Recovery in the job runner | `TestPanicInAJobDoesNotCrashTheAgent` |
| Job IDs are not enumerable | `crypto/rand` identifiers | `TestJobIDsAreUnguessable` |
| Audit survives a restart | `O_APPEND`, never truncated | `TestRecordsSurviveReopening` |
| Audit records are never partial | Whole-record writes under lock | `TestConcurrentWritesProduceWholeRecords` |
| Internal detail never reaches a caller | Structured codes; causes only logged | `TestHandlerFailuresAreLoggedNotReturned` |

---

## 3. Decisions and their rationale

### 3.1 "Job execution" means agent-side async, not the durable queue

TASKS.md lists "Job execution" under Phase 2. That is implemented as
**asynchronous execution inside the Agent**: submit, poll, cancel, with jobs
held in memory.

The durable side — the `jobs` table from DATABASE.md §24, the `/jobs`
endpoints from API_SPEC.md §28, and the WebSocket — is deferred to Phase 4.

*Why:* Phase 2 has no long-running operation. Building a Redis queue, a
persistence layer, and a WebSocket protocol with no caller would be designing
against imagined requirements, and website creation in Phase 4 is the first
thing that will actually exercise them. The Agent's half is what Phase 2 can
build honestly and test.

*Consequence:* an Agent restart loses its job table. The API must treat a
job ID it can no longer resolve as failed. This is recorded as a limitation.

### 3.2 Metrics parse `/proc` rather than running `ps`, `free`, or `df`

*Why:* those tools' output formats vary by distribution, locale, and version,
and scraping them means executing a program to answer a question the kernel
already answers in a stable format. Parsing is faster, has no process to
sandbox, and cannot be affected by a hostile `PATH`. `statfs(2)` replaces `df`
for the same reason.

### 3.3 CPU usage is zero on the first sample

`/proc/stat` exposes cumulative counters, so a usage percentage only exists
relative to an earlier reading.

*Why not compare against boot?* That reports the machine's lifetime average,
which on a long-running host is a nearly constant number that looks obviously
wrong on a graph. Reporting zero with an explicit `sample_window` of `0s` is
honest about there being nothing to compare yet.

### 3.4 Memory "used" derives from `MemAvailable`

*Why:* `MemFree` excludes reclaimable page cache. Using it reports a healthy
Linux box as nearly out of memory — the single most common way a monitoring
dashboard lies. On kernels predating `MemAvailable`, the estimate the kernel
itself uses is reconstructed rather than falling back to `MemFree`.

### 3.5 Both peer credentials and a token

*Why both:* they fail differently. Peer credentials come from the kernel and
cannot be forged, but only prove which UID connected — and they say nothing if
the socket's permissions are widened by mistake. The token proves knowledge of
a secret and survives a UID change, such as a container rebuild. Requiring both
means one misconfiguration is not a compromise.

Root is always permitted, because the Agent's own health check connects to its
socket as root.

### 3.6 Killing the process group, not just the process

`exec.CommandContext` kills the program it started. A program that spawns
children leaves them running, and they keep the output pipes open, so `Wait`
blocks long past the deadline.

This was found by a test that asserted a 200 ms timeout completed in under
three seconds and instead took the child's full ten. The Agent now puts each
child in its own process group and cancels the group, with `WaitDelay` as a
backstop. Without it, one hung command pins an Agent worker indefinitely.

### 3.7 The Agent keeps its own audit trail

Records go to a JSON-lines file (`AGENT_AUDIT_LOG`) as well as the structured
log, rather than to the API's database.

*Why:* the Agent runs as root and must remain accountable when Postgres is
unreachable, and an attacker who reaches the API should not be able to erase
evidence of what the Agent was asked to do. A file that cannot be opened
degrades to logger-only auditing rather than stopping the daemon — refusing to
start would turn a logging problem into an outage.

### 3.8 The Agent stays dependency-free

`agent` has no third-party dependencies. `SO_PEERCRED` and `statfs` are both in
the standard library.

*Why:* this is the process that runs as root. Every dependency added here is a
supply-chain path into that process, and the standard library covers everything
Phase 2 needs.

---

## 4. How to verify

```bash
make dev
make docker-test-agent
```

Ask the Agent directly:

```bash
docker compose exec agent jothost-agent -call agent.info
```

```bash
docker compose exec agent jothost-agent -call metrics.memory
```

```bash
docker compose exec agent jothost-agent -call process.list -payload '{"limit":5,"sort_by":"cpu"}'
```

Submit a background job:

```bash
docker compose exec agent jothost-agent -call metrics.disk -async
```

Read the Agent's audit trail:

```bash
docker compose exec agent tail -5 /var/log/jothost/agent-audit.log
```

---

## 5. Known limitations

1. **Jobs are lost on Agent restart.** They are in memory by design (3.1). The
   API must treat an unresolvable job ID as failed once it owns durable job
   records in Phase 4.
2. **No API endpoints expose these metrics.** That is Phase 3 (Dashboard).
   Phase 2 stops at the Agent and typed client wrappers.
3. **Service operations are read-only.** Start, stop, restart, enable, and
   disable arrive with Phase 12.
4. **In a container, the Agent sees the container.** `AGENT_PROC_ROOT` defaults
   to `/proc`, which inside Docker is the container's namespace — the process
   list and network interfaces are the container's, not the host's. In
   production the Agent runs on the host, where this is correct. To see host
   metrics in development, mount the host's `/proc` and point
   `AGENT_PROC_ROOT` at it.
5. **`peer_pid` is 0 for cross-namespace callers.** The API runs in a different
   PID namespace, so the kernel cannot translate its PID. The UID and GID are
   still correct and still enforced; only the PID field is unavailable.
6. **The development Agent runs unprivileged and mounts no host paths.** It is
   a functional Agent for read-only metrics, but the privileged runtime
   configuration belongs with the production installer (Phase 23).
7. **`USER_HZ` is assumed to be 100.** Reading it properly needs cgo, which the
   Agent avoids. It is 100 on every supported Linux architecture, but a host
   with a different value would report skewed per-process CPU times.
8. **`pathsec` is not yet used by an operation.** Phase 2 has no operation that
   takes a filesystem path — `metrics.disk` matches against the mount table
   instead. It is built and tested now because Phase 7's file manager is where
   getting it wrong is unrecoverable.
9. **No rate limiting on Agent operations.** The concurrency cap bounds
   parallelism but not request rate. The socket's permissions are the access
   control; a caller that already has them is trusted not to flood.

---

## 6. Next recommended task

**Phase 3 — Dashboard.** Everything it needs now exists on the Agent side:

1. `GET /api/v1/dashboard` aggregating the metric operations behind
   `RequireAuth` and `rbac.PermServerView`.
2. `GET /api/v1/servers/:id/metrics` with the range parameter from
   API_SPEC.md §5, which needs the `system_metrics` table from DATABASE.md §26
   and a sampler writing to it.
3. The CPU, RAM, disk, network, service-status, and alert widgets, replacing
   the Phase 0 placeholder dashboard.
4. Honouring `agent.info` capabilities in the UI, so a host without systemd
   does not show a broken service panel.
