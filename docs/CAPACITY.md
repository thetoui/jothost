# Capacity

How many websites one host running JotHost Panel can carry, how the panel
behaves while several people use it at once, and how long a full backup takes.

These are the questions an operator asks before they put customers on a box,
and until now nobody had measured them — `PHASE24.md` says as much of its load
test: the figures there "are not a benchmark". This is the tool that produces
real ones, and the discipline for reading them.

## The one rule

**A number is a supported limit only on the hardware it was measured on.**

The harness drives a real panel and times what comes back, so its numbers are
honest about *that machine on that day*. Run it in a container on a laptop and
it measures the laptop; run it on a shared VM and it measures whatever else the
VM was doing. Neither is a promise you can make to an operator. A supported
limit is a figure measured on a named reference machine, on an otherwise idle
host, and written into the table below with that machine named beside it.

The harness prints this rule at the top of every report, so a figure cannot be
lifted out of its context.

## What it measures

`tests/capacity/` is a standard-library Go program with three phases, each
skippable:

1. **Website provisioning** — creates websites one after another and times how
   long each takes to become active (nginx config, system user, PHP pool). The
   question is not how many requests the API accepts but how quickly the host
   turns one into a working site, and whether that slows as the tree fills.
2. **Concurrent panel users** — a set number of concurrent readers hit the
   pages a signed-in operator opens (dashboard, websites, jobs, monitoring,
   backups, audit) for a fixed time, and it reports throughput, error rate and
   the latency distribution (p50/p95/p99).
3. **Backup timing** — takes one panel backup and times it end to end,
   including the read-back verification the panel does before reporting success,
   because that is the time an operator actually waits.

## Running it

Against a real host (the only run whose numbers mean anything):

```bash
go run ./tests/capacity \
  -url https://panel.example.com -user admin -password '…' -insecure \
  -host-spec "Hetzner CPX21, 3 vCPU / 4 GB, NVMe" \
  -sites 200 -users 50 -load-duration 2m \
  -destination-id "<a backup destination id>" \
  -out capacity-report.md
```

Or, from the developer machine, `sh tests/capacity/run.sh --against-target` with
`TARGET_URL`, `TARGET_PASSWORD` and `HOST_SPEC` set.

Against a throwaway container, for catching a regression on the *same* machine
over time (not for supported limits):

```bash
make capacity
```

The provisioning phase creates real websites; pass `-cleanup` to remove them, or
leave them to measure a host that is already loaded.

## Published limits

Filled in from runs on the reference hardware, on an otherwise idle host. Until
a row is measured there, it stays blank rather than guessed.

| Reference host | Sites (active) | Provision, p95 | Concurrent users | Read latency p95 | Full backup |
|---|---|---|---|---|---|
| _(to be measured)_ | | | | | |

The reference hardware and the acceptable thresholds are a decision for the
first real measurement, not something to invent here.
