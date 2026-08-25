# Phase 3 — Dashboard

**Status:** Complete
**Scope:** TASKS.md Phase 3, PRD.md section 7, API_SPEC.md sections 3–5,
DATABASE.md tables 8 and 26

Phase 2 made the Agent able to read the host. Phase 3 makes that readable by a
person: a live server view, a metric history to plot, and alerts that say what
needs attention.

---

## 1. What was built

### 1.1 Two data paths

The distinction matters, and conflating them is the trap this phase is built
around:

| Path | Source | Failure mode |
|---|---|---|
| **Live values** — current CPU, RAM, services | Agent, per request, in parallel | Each widget degrades on its own |
| **History** — the metric graph | `system_metrics`, written by a sampler | A gap in the line, not a broken page |

The Agent deliberately keeps no history (docs/PHASE2.md §3.1), so the graph
only exists because something persists readings on a cadence.

### 1.2 Schema and registration

Migration `0004_servers_and_metrics` adds `servers` (DATABASE.md §8) and
`system_metrics` (§26). The local host is registered at API startup from the
Agent's `system.info`, as an upsert keyed on hostname so a restart refreshes
the row rather than accumulating one per boot.

### 1.3 Sampler

A goroutine tied to the API lifecycle polls the Agent every
`METRIC_SAMPLE_INTERVAL` (default 30s), stores what it got, and prunes past
`METRIC_RETENTION` (default 30 days). A failed sample is logged and skipped —
an Agent restart leaves a gap in the graph rather than stopping collection
until someone restarts the API.

### 1.4 Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/dashboard` | Live snapshot: system, CPU, memory, disk, network, load, services, alerts |
| `GET /api/v1/servers` | Managed servers |
| `GET /api/v1/servers/{id}` | One server |
| `GET /api/v1/servers/{id}/metrics?range=` | Bucketed history for `1h`, `24h`, `7d`, `30d` |

All behind `RequireAuth` and `server.view`.

### 1.5 Alerts

Derived from the reading that produced them rather than stored, so an alert
cannot disagree with the panel beside it. Disk is checked per filesystem,
memory includes swap pressure, load is normalised per core, and a stopped
service or an unreachable Agent is stated plainly.

### 1.6 Frontend

Widgets for each panel, a range-switchable metric chart drawn as inline SVG,
and an alert list. Every gauge exposes `progressbar` semantics and prints its
value as text, so colour is never the only signal.

---

## 2. Properties, and the tests that pin them

| Property | Mechanism | Test |
|---|---|---|
| The dashboard survives a dead Agent | Widgets fail independently | `TestSnapshotSurvivesAnUnreachableAgent`, `TestDashboardRespondsWithoutAnAgent` |
| One broken probe does not blank the page | Parallel collection, per-widget state | `TestOneFailingWidgetDoesNotAffectTheOthers` |
| "Unsupported" is not "broken" | Distinct widget flag | `TestUnsupportedMetricIsDistinctFromAFailure` |
| A systemd-less host still shows dependencies | Units and dependencies collected separately | `TestServicesDegradeWithoutSystemd` |
| Widget errors leak nothing | Fixed summaries, never the Agent's message | `TestWidgetErrorsCarryNoInternalDetail` |
| Disk alerts catch a full `/var` | Per filesystem, not the aggregate | `TestDiskAlertsArePerFilesystem` |
| Load alerts work on any machine size | Per core | `TestLoadAlertIsPerCore` |
| An absent optional unit does not alert forever | `not installed` excluded | `TestNotInstalledServiceDoesNotAlert` |
| Registration is idempotent | Upsert on hostname | `TestRegisterIsIdempotent` |
| A known address is not erased | `COALESCE` on upsert | `TestRegisterKeepsAKnownAddressWhenTheNewOneIsMissing` |
| Unknown facts stay unknown | NULL, not `""` | `TestRegisterStoresUnknownFactsAsNull` |
| An unreadable metric is absent, not zero | Nullable sample fields | `TestInsertAcceptsAPartialSample` |
| A host's readings never appear on another's graph | Query scoped by server | `TestHistoryIsolatesServers` |
| A bad range is refused, not defaulted | `ParseRange` allowlist | `TestParseRange`, `TestServerMetricsValidatesRange` |
| A counter reset is not plotted as a spike | Negative deltas dropped | `TestHistoryIgnoresACounterReset` |
| Metrics die with their server | `ON DELETE CASCADE` | `TestSamplesAreDeletedWithTheirServer` |
| Only authorised callers see host data | `RequireAuth` + `server.view` | `TestDashboardRoutesRejectAnonymousCallers`, `TestDashboardRoutesRequireServerView` |
| A gap in collection is visible | Chart breaks the line | `MetricChart` "breaks the line across a gap" |
| Bars and alerts agree | Shared thresholds | `usageTone` "mirrors the server alert thresholds" |

---

## 3. Decisions and their rationale

### 3.1 Widgets degrade independently

Each panel carries `available`, `unsupported`, and `error` rather than the
endpoint failing when the Agent does.

*Why:* a dashboard is what an operator opens when something is already wrong.
An endpoint that returns 503 because one probe timed out is useless at exactly
the moment it is needed. The three states are distinct on purpose —
"unsupported" tells an operator not to go looking for a fault.

### 3.2 Network counters are stored, rates are computed

`system_metrics` holds the cumulative `network_rx`/`network_tx` counters, not a
rate.

*Why:* a stored rate is locked to the interval it was sampled at. Storing the
counter lets a rate be derived over any window, which is what makes the 1h and
30d graphs consistent with each other. A counter reset produces a negative
delta, which is dropped rather than plotted as an enormous spike.

### 3.3 Aggregation happens in Postgres

`date_bin` buckets server-side; the API sends 60–200 points regardless of range.

*Why:* 30 days at a 30-second interval is 86,400 rows. Sending those to a
browser to average in JavaScript would be a multi-megabyte response for a chart
600 pixels wide.

### 3.4 The first CPU reading is recorded as absent, not zero

The Agent's first CPU sample after a restart has no earlier reading to compare
against and reports a zero window.

*Why:* storing that as `0%` is a fabricated measurement, and it plots as a dip
to the floor every time the Agent restarts. This was visible in the first
end-to-end run — the graph opened at zero — and recording the reading as absent
is both honest and what the nullable columns exist for.

### 3.5 tmpfs is excluded from disk reporting

*Why:* it is memory-backed, so its consumption already shows in the memory
figures, and a typical host mounts half a dozen of them. Listing `/dev`,
`/dev/shm`, `/run`, and several cgroup paths buries the one or two filesystems
an operator is looking for. The trade-off, stated plainly: a filling `/run`
surfaces as memory pressure rather than as a disk warning.

### 3.6 Alerts are computed, never stored

*Why:* a stored alert can go stale and contradict the panel beside it. Deriving
them from the same reading that fills the widgets makes disagreement
impossible. It also means alert history does not exist yet — that arrives with
Phase 19's alert engine, which has somewhere to put it.

### 3.7 The chart is inline SVG, not a charting library

*Why:* the requirement is a polyline and two axes. A charting library is
several hundred kilobytes for that, and every one of them brings its own
opinions about theming and responsiveness. If the panel later needs brushing,
zooming, or stacked areas, that is the point to reconsider — not before.

Gaps are drawn as breaks in the line rather than interpolated across, so an
outage stays visible instead of being smoothed into a plausible curve.

### 3.8 Server mutations are not implemented

API_SPEC.md §4 lists `POST`, `PATCH`, and `DELETE /servers`. Phase 3 ships only
the read endpoints.

*Why:* those manage a fleet, and multi-server clustering is an explicit
non-goal in PRD.md §3. The one managed host is registered from what the Agent
reports, which is more reliable than anything a form could collect. When
multi-server arrives, so do these.

---

## 4. How to verify

```bash
make dev
make docker-test-dashboard
```

Sign in at http://localhost:8081 and open the dashboard.

Directly:

```bash
curl -s http://localhost:8080/api/v1/dashboard -H "Authorization: Bearer $ACCESS_TOKEN"
```

```bash
curl -s "http://localhost:8080/api/v1/servers/$SERVER_ID/metrics?range=24h" -H "Authorization: Bearer $ACCESS_TOKEN"
```

---

## 5. Known limitations

1. **No website, database, or SSL counts.** PRD.md §7 and API_SPEC.md §3 list
   them. Their tables arrive in Phases 4, 8, and 6; reporting `0` today would
   be a lie, so the fields are omitted until there is something to count.
2. **No SSL-expiry, backup-failure, or security-finding alerts.** Same reason —
   they need Phases 6, 14, and 15.
3. **Raw samples only, no rollups.** PRD.md §12 wants aggregated metrics kept
   longer than raw ones. At one sample per 30 seconds, 30 days is roughly
   86,000 rows per server, which Postgres handles comfortably. Rollups become
   worthwhile when retention grows past a year or a fleet appears.
4. **The sampler is single-instance.** Two API processes against one database
   would both sample and double the rows. There is no leader election, because
   the deployment model is one API per host.
5. **Alerts have no history and no notification.** They exist only in the
   current snapshot. Phase 19 adds the alert engine and Phase 20 delivery.
6. **No WebSocket; the dashboard polls.** Every 30 seconds for the live view,
   matching the sampling interval. Live push arrives with the job system in
   Phase 4.
7. **Thresholds are global, not per server or per filesystem.** One host, one
   set of numbers. Per-resource overrides want the alert-rule model in
   Phase 19.
8. **In a container the dashboard shows the container.** The Agent reads its
   own namespace (docs/PHASE2.md §5.4), so filesystems and interfaces are the
   container's. Correct in production, where the Agent runs on the host.
9. **A restart gaps the graph.** Metrics are only recorded while the API runs;
   nothing backfills the window it was down.

---

## 6. Next recommended task

**Phase 4 — Website Manager.** This is where the deferred pieces come due:

1. The `websites` and `domains` tables (DATABASE.md §9–10), which finally give
   the dashboard something to count.
2. The durable job system deferred in Phase 2 — the `jobs` table (§24), the
   `/jobs` endpoints (API_SPEC.md §28), and the WebSocket — with website
   creation as its first real caller.
3. Nginx and filesystem providers on the Agent, registered as allowlisted
   operations, using the `pathsec` validator built in Phase 2.
4. The end-to-end integration test named in TASKS.md: create a website, request
   it over HTTP, receive a response.
