# Phase 19 — Monitoring

What this phase adds, the one decision the whole thing turns on, and — stated
plainly — what it will not do.

---

## 1. The decision everything else follows from

**A rule fires when its condition has held for its whole duration, not when one
reading crossed a line.**

That sentence is the phase. A monitor that pages for a backup job briefly
filling memory gets muted within a week, and a muted monitor is worse than no
monitor at all — because somebody believes it is watching.

So every rule has a `for_seconds`, and the evaluator has three outcomes rather
than two:

| Outcome | What happens |
|---|---|
| Not breached | any open alert is resolved |
| Breached, not yet sustained | **nothing** — this is where a blip dies |
| Sustained | the alert opens |

Collapsing that middle case into either of the others is how a monitor becomes
useless or unbearable. The integration test proves it directly: a rule that is
breaching from the moment it exists and requires an hour of it opens nothing.

---

## 2. Two kinds of alert, and why both stay

Phase 3's dashboard already computed alerts from the reading in front of it.
Those are not replaced, because the two answer different questions:

- **The dashboard's** answer "what is wrong *now*". They are derived from the
  same snapshot they are shown beside, so they cannot go stale.
- **This phase's** answer "what has been wrong, since when, and is it still".
  Only these can be acknowledged, looked back at, or — in Phase 20 — notified
  about. Only these need rows.

What they must never do is disagree about *numbers*. So the thresholds moved
into alert rules, and the dashboard reads them from there: one definition, two
views. Raising the disk threshold on the monitoring page raises it on the
dashboard, without a restart.

---

## 3. Sustained breach across a restart

"How long has this been true" cannot be answered from a single reading, and
keeping it in memory would mean a panel restarted mid-incident forgetting that a
machine has been at 99% for an hour — which is exactly when a restart is most
likely.

So it is asked of the stored samples. `BreachedSince` finds the run of
consecutive breaching samples by taking everything after the most recent sample
that did *not* breach.

Two properties of that are deliberate and tested:

- **A gap in sampling does not break the run.** The panel being off is not
  evidence that a disk emptied, and treating it as such would reset every
  incident's clock on every deploy.
- **A recovered incident does not count towards the next one.** A machine that
  was full last night and is full again now has been full *since now*.

Disk is the exception, and it is stated rather than discovered: the sample table
holds one whole-host disk figure, so a rule about a single filesystem cannot get
its duration from it. Answering with the wrong filesystem's history would be
worse than answering with none, so it answers with none.

---

## 4. One alert per thing being watched

A unique partial index — `(server_id, metric, target, severity) WHERE status =
'open'` — enforces it. Without it a flapping disk opens a new alert every
evaluation, and somebody wakes to four hundred rows describing one filesystem.

Reopening an existing alert refreshes it rather than replacing it:

- `opened_at` does not move — it is the incident's start.
- `worst` is kept with `GREATEST`, because a resolved alert reading "peaked at
  99%" is worth more than one reading "was at 81% when it cleared".
- The message is refreshed, so the current reading is the one shown.

Warning and critical are separate alerts on purpose: a disk crossing one line
and then the other is two things somebody may want to be told about differently.

---

## 5. Acknowledging is not resolving

There is no endpoint that resolves an alert, and that is the point.
Acknowledging says "I know, I am dealing with it". Whether the condition has
cleared is a fact about the machine, and the monitor decides it.

A panel where a person can mark a full disk as fine is a panel that will one day
say a full disk is fine. The page says so out loud, where somebody is about to
look for the button.

---

## 6. Aggregated history

The PRD asks for "raw metrics: configurable, aggregated metrics: longer
retention". Completed hours are now summarised into `metric_rollups`, and two
new ranges — `90d` and `1y` — are served from them.

Each bucket carries an **average and a maximum**. The average is what a graph
plots; the maximum is what stops an hour of aggregation hiding the five-minute
spike that filled a disk, which is exactly the event somebody looks back for.
`sample_count` goes with them, because a bucket built from two samples is not
the same evidence as one built from a hundred.

Three details worth naming:

- **Only completed hours.** An hour still in progress would be summarised from
  half its samples and never revisited — a permanent wrong number.
- **Idempotent.** A panel restarted mid-hour, or catching up after a week off,
  produces the same rows. A catch-up over a window whose raw samples have since
  been pruned will not overwrite a good summary with a worse one; the upsert
  only accepts a bucket built on at least as much evidence.
- **Falls back to raw.** A panel whose first hour has not finished has no
  summaries, and answering "no history" while the raw samples sit right there
  would be a chart that is empty for an hour after installation.

---

## 7. Service history is transitions, not samples

A row per service per poll would be tens of thousands a day saying "still
running". The question an operator actually asks — "when did it go down, and how
long was it down" — is answered by the changes alone.

So `service_states` stores stretches: one row per state, with `ended_at` NULL
while it is current. A unique partial index enforces one open stretch per
service, because two would make every duration ambiguous.

"Stopped", "not installed", "failed" and "starting" are kept apart, the same
words the dashboard uses. A history that rounded them together would say a
service was down when it was coming up. All four are *recorded*; only the
installed ones are *alerted* on, because "not installed" is a configuration and
a rule on it would fire permanently on every host missing an optional unit.

---

## 8. What the panel starts with

Eight default rules, written once on a host that has none. A monitoring page
with no rules monitors nothing, and an operator who has to invent thresholds
before the panel does anything mostly will not bother.

The numbers are the ones Phase 3's dashboard already used, so nothing changes
meaning when a host is upgraded into this phase — with a duration on each, which
is the part Phase 3 had no way to express. CPU's is deliberately generous and
slow: a server at 90% for ten minutes is a server doing its job, and the alert
is for the one that never comes back down.

They are written **only when there are none**. A host whose operator deleted a
rule they did not want must not have it put back on the next restart — that is
how a panel teaches people to ignore it.

---

## 9. Why the monitor is its own loop

It runs beside the metric sampler, not inside it. The sampler's job is to record
what the host said; the monitor's is to decide what that means. Keeping them
apart is what lets an operator change a threshold without touching collection,
and what stops a slow evaluation delaying a sample.

An unavailable reading is neither a breach nor a recovery. A disk probe that
timed out must not resolve a real disk alert, and must not open one — silence is
its own answer, and it is the third state the evaluator handles.

---

## 10. What the panel refuses

- **A metric it cannot measure.** A rule naming one would sit in the table
  looking like protection and never fire, which is worse than not having it.
- **A percentage threshold above 100.** It could never be reached, so accepting
  it would let somebody switch off an alert while believing they had set one.
- **A service rule that does not name its service.** "Alert when a service is
  down" is not something the evaluator can act on.
- **Two rules watching the same metric, target and severity.** Both would fire,
  and the operator would get two alerts about one problem.
- **Pointing an existing rule at something else.** That would be a different
  rule, and the alerts it had already opened would be attributed to a condition
  it never observed.
- **A duration longer than a day**, a blank name, and a negative threshold.

---

## 11. Where it meets the other phases

- **Metrics (3).** The sample table and the sampler are Phase 3's; this phase
  adds the summaries, the long ranges, and the question `BreachedSince` answers.
  Two of Phase 3's tests changed, because they asserted the documented range set
  and this phase extends it.
- **Services (12).** The service list is the same probe the dashboard uses,
  taken once per evaluation and turned into both the history and the readings —
  asking twice would let the alert and the history disagree about what the host
  said.
- **Dashboard (3).** Its thresholds now come from the rules. See section 2.
- **Notifications (20), which this unblocks.** An alert with an open/resolved
  lifecycle and a stable identity is the thing there is to deliver; a computed
  one could only be re-sent every thirty seconds.
- **Multi-tenant (22), which this also unblocks.** Disk usage is a quota
  dimension, and it is now recorded per filesystem over time rather than only
  read live.

---

## 12. What the tests prove

**Go tests, no database** (`api/internal/monitoring`, 12 tests): the three-way
outcome, in particular that a two-minute breach of a five-minute rule opens
nothing and that an unknown duration errs towards silence; that an unavailable
reading neither opens nor resolves; that the message names the reading, the
threshold and how long it has held, and does *not* say "for 0 minutes"; and that
the defaults carry Phase 3's numbers, all enabled, none firing on a single
reading, none colliding with each other.

**Go tests, with a database** (`api/internal/monitoring`, 9 tests): that a second
breach refreshes one alert rather than opening a second, keeping the original
start and the worst reading; that warning and critical are separate; that
resolving closes it and a later recurrence is a *new* incident; that
acknowledging leaves it open; that deleting a rule keeps the alerts it caught;
that an unchanged service writes no row and a changed one closes the previous
stretch at the moment it changed; and that pruning does not touch what is open.

**Metrics tests** (`api/internal/metrics`, 8 new): that only completed hours are
summarised, that the worst reading survives aggregation, that rolling up three
times produces one bucket, that a long range falls back to raw when there are no
summaries, and the four `BreachedSince` properties in section 3.

**Integration** (`tests/integration/phase19_monitoring.sh`, 35 checks) runs
against the live stack and the real host:

- a fresh host starts with rules, none of which fire on a single reading
- services are recorded with how long they have been in their state
- a rule pointed at a filesystem **discovered from the host** — not hardcoded,
  because this container reports `/run/jothost` and `/tests` and no `/` at all —
  opens a real alert carrying the real reading
- acknowledging records it and leaves it open, and there is no way to resolve
  one by hand
- a still-breaching condition on the next evaluation does not open a second
  alert
- disabling the rule resolves its alert, and the resolved alert is kept with its
  worst reading
- **a breach shorter than its rule's duration opens nothing**
- and every refusal in section 10

```bash
make docker-test-monitoring
```

**Frontend** (`MonitoringPage.test.tsx`, 14 tests): that "nothing is wrong" says
it has looked; that an open alert shows what opened it and its worst; that there
is no resolve button and the page says why; that acknowledging does not resolve;
that a rule is described as a sentence including its duration; that an existing
rule's metric and target are disabled and not sent; and that a resolved alert
shows how long it lasted.

---

## 13. Known limitations

- **Disk duration is per host, not per filesystem.** Section 3. A per-filesystem
  rule with a duration is judged from the live reading, so the first sustained
  evaluation opens it rather than a run of samples proving it.
- **No notification.** Alerts are recorded and shown; delivering them is Phase
  20, which this exists to make possible.
- **No network or process rules in practice.** `network_rx` and `network_tx` are
  accepted as metrics and the sampler stores the counters, but the monitor takes
  no live network reading yet, so a rule on them will not fire. Processes are
  not watched at all. Both are named here rather than left to be discovered.
- **One rule per metric, target and severity.** Section 10. Somebody wanting two
  thresholds on one filesystem uses warning and critical, which is what they are
  for.
- **No maintenance windows.** An operator who is about to fill a disk on purpose
  can disable the rule, which resolves its alert; there is no "silence until
  Tuesday".
- **Rollups are hourly and per host.** Fine for a panel managing one machine; a
  fleet would want the bucket configurable.
- **The evaluation interval is the sampler's, at least a minute.** A condition
  that appears and clears inside one interval is never seen — which is the
  correct trade for something whose whole purpose is to ignore blips.
