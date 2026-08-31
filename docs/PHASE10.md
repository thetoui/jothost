# Phase 10 — Cron

What this phase adds, the rule it had to be reconciled with, and — stated
plainly — what was found by running it rather than by reading it.

---

## 1. What it does

The panel schedules work on the host: PHP scripts, URL fetches, and command
lines. Each job belongs to a website, runs as that website's own unprivileged
account, and can be edited, disabled, run on demand, and read back through its
own log.

---

## 2. The rule this phase had to be reconciled with

CLAUDE.md section 4 says: **never execute arbitrary user-provided shell
commands.** A cron job is a user-provided command. So the first thing this phase
had to decide was whether it could exist at all.

It can, because of a distinction that is easy to lose: **the panel does not run
scheduled jobs.** It writes crontab entries, and the host's cron daemon runs
them — as the website's own account, exactly as it would run an entry the
customer wrote themselves with `crontab -e`. The command is *data in a file*.
The panel is not granting a capability; it is offering an interface to one the
account already has.

That reframing decides what actually has to be defended, and it is not what the
command does. The account it runs as can already read and write that site's
files, with or without this panel. What must be defended is **the crontab
file**, which is line-oriented, unquoted, and unescaped:

> A command containing a newline is not a long command. It is two entries, and
> the second one is whatever the caller wrote.

So every value that reaches a crontab line is refused if it contains `\n`, `\r`,
or a null byte — in `shared/validate`, again in the Agent before the write, and
once more as a database CHECK. And `%` is refused outright, because cron treats
an unescaped `%` as "the command ends here, the rest is standard input" with
further `%`s becoming newlines: it is a line break in disguise, and the most
common reason a working command line stops working in a crontab.

### Three job types, so the common cases carry no command at all

| Type | What the operator gives | What is written |
|---|---|---|
| `php` | a script path inside their site | `<interpreter the panel resolved> <path under the document root>` |
| `url` | an http(s) address | `curl -fsS --max-time 300 <address>` |
| `command` | a command line | that command line |

The first two are built by the panel from a fragment that has been checked, so
nothing the caller types decides which program runs. The third is the escape
hatch, and being a separate type makes it an explicit choice rather than the
only way to schedule anything.

### There is no field for a user

A job belongs to a website, and a website supplies the account. There is no
column for a user, no parameter for one, and no code path that could produce
one — and if an account ever resolves to uid 0, "run now" refuses rather than
repairing it. A panel that could schedule work as root would be a panel where
one careless job is a compromised machine.

---

## 3. "Run now", the one place the panel does execute a job

There is no way to implement "run now" that does not run the command, and the
reasoning is written out in `agent/internal/cron/run.go` rather than left
implicit:

A caller who can reach that endpoint can create a scheduled job — a command
line, run by crond, as this same account, on any schedule they choose, including
one minute from now. Running it immediately therefore grants no authority that
was not already granted by the request that created it; it removes a wait of up
to sixty seconds. Refusing would not make the panel safer, only less useful, and
an operator would work around it by scheduling the job a minute ahead — which is
worse, because then there is no record of a deliberate manual run.

What bounds it:

- **Never as root.** The credential drop is mandatory and uid 0 is refused.
- **What is stored, not what was sent** — the command comes from the panel's
  records, having passed validation on the way in.
- **Timeout-protected**, output captured and capped rather than inherited.
- **Audited**, as `cron.run`, with the job named.
- **A dedicated allowlist entry** (`cron-shell`) used by that one function,
  rather than a general `sh` the rest of the Agent could reach for.

It uses a shell because a crontab entry *is* a shell command line, and cron runs
it with `/bin/sh`. A "run now" that executed it any other way — redirections,
pipes and `&&` behaving differently or not at all — would be testing something
other than what the schedule runs, which is worse than not offering it.

---

## 4. The crontab is shared, so the panel owns a block of it

A customer with SSH may have written entries in their crontab years before this
panel existed. The panel owns what lies between two markers and nothing else;
every write preserves the rest byte for byte, and removing the panel's jobs
leaves the customer's untouched.

Three details in that file are not obvious and all three are load-bearing:

- **The block sets no `SHELL`, `PATH` or `MAILTO`.** A crontab environment
  assignment applies to every entry *after* it in the file, so one set inside
  the panel's block would silently change how the customer's own entries behave.
- **A disabled job is absent, not commented out.** A commented entry is one
  `crontab -e` away from running, and the panel would not know it had been
  re-enabled.
- **The file is written to a temporary name and renamed into place.** A crontab
  caught mid-write is a set of jobs that silently stops running.

---

## 5. Two things that were only found by running it

Both would have passed any test that read the file the panel wrote.

**BusyBox crond ignores a crontab that is not owned by root — silently.** Not a
warning at any log level; the file is simply never parsed. The first
implementation wrote spool files owned by the job's account, which is what
`crontab` itself produces and what Vixie cron documents, and every entry was
correct and nothing ever ran. Root ownership satisfies both implementations:
Vixie accepts a spool file owned by root or by the user, BusyBox accepts only
root, and the filename is what decides who a job runs as in either.

**A redirection applies to the last command in a list, not the list.** The
entries were written as `<command> >> <log> 2>&1`, so a job of
`php cron.php; php queue.php` logged only the second half — and the half that is
missing is, reliably, the half that failed. The command is now wrapped in a
subshell: `( <command> ) >> <log> 2>&1`.

Both are checked by the integration suite now, and the suite waits for the
daemon to actually fire a job rather than inspecting the file it would read.

---

## 6. What else this phase touched

**Phase 11's extension point, used as intended.** Each job's output is collected
in a file per job, and those files are contributed to the log catalogue as
sources — so the job's log is searched, filtered, followed and downloaded by the
viewer that already exists. This phase built no viewer of its own, and
`GET /cron/:id/logs` returns the log source key rather than a second reader with
its own bounds and its own bugs. The job names come from a small index the Agent
writes beside the logs, because a picker listing `8f3c2b1a-…` is a picker nobody
can use.

**Deleting a website now removes its crontab and its job logs.** The rows go
with the site through a foreign key, but the file on the host does not: without
this, a deleted site's entries keep running as an account that is removed
moments later, and cron starts failing every minute against a user that is gone.

**The PHP detector reports the CLI path.** A web server needs only FPM, which is
why the command-line interpreter is a separate package on every distribution. A
scheduled PHP job runs the CLI, so detection now reports it — empty where it is
absent, which is what lets the panel refuse the job with a reason instead of
scheduling one that fails every night.

**`NextRun` in `shared/validate`.** The most common cron mistake is an
expression that means something other than what was intended, so the panel shows
when a job will next fire. It implements cron's oldest and least obvious rule:
when both day fields are restricted they are an **or**, not an **and** —
`0 0 13 * 5` is the 13th *or* any Friday. And it reports "never" for a schedule
that can never fire, because 31 February is a valid expression and a job that
looks scheduled and never happens is the worst outcome available here.

---

## 7. What the tests prove

**Go tests.** `shared/validate` covers the schedule grammar, the shorthands, the
or-rule, and the refusals — including six fields (the seconds-resolution form
some crons accept, which would run a job sixty times more often than intended)
and `%`. `agent/internal/cron` writes real files: the customer's entries
surviving, the block being replaced rather than duplicated, a block whose end
marker somebody deleted, the mode and ownership, the subshell redirection, and
the injection attempts. `api/internal/cron` covers what a job *becomes* — the
command line built from each type, and every traversal, separator and
substitution the two structured types refuse.

**Integration** (`tests/integration/phase10_cron.sh`, 51 checks) runs inside the
Agent's container:

- a job scheduled for every minute, and a wait of up to 130 seconds for the
  host's cron daemon to actually run it
- what it printed, in the job's own log, and the manual run appended to the same
  file
- the crontab root-owned and 0600, with no `PATH` or `MAILTO`
- disabling removing the entry entirely rather than commenting it out
- deleting a website taking its crontab and its job logs with it
- and the refusals, each with the reason it exists

```bash
make docker-test-cron
```

**Frontend** (`CronPage.test.tsx`, 11 tests) covers the list, the account being
visible, a job that has never run saying so, a failed run showing its exit code
and output, a schedule that never fires, and the create form.

---

## 8. Known limitations

- **No `@reboot`.** It has no five-field equivalent, so the panel could not say
  when it will next happen — and this panel regenerates crontabs.
- **No seconds.** A six-field expression means different things to different
  crons.
- **`%` is refused rather than escaped.** `date +%Y` in a command has to move
  into a script. Escaping it would work for the panel's own writes and break the
  moment anyone edited the crontab by hand.
- **A scheduled run's outcome is not recorded against the job.** `last_status`
  is set by manual runs; a run by the daemon leaves its output in the log but
  the panel does not parse that back. Watching for it would mean either a daemon
  of the panel's own or a parser for cron's log, and neither belongs in this
  phase.
- **One schedule per job.** Two schedules means two jobs.
- **No concurrency guard.** If a job takes longer than its interval, cron starts
  another. `flock` in the command is the answer, and the panel does not add it
  for you.
