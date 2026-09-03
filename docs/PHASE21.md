# Phase 21 — System Updates

What this phase adds, the sentence it exists to avoid saying, and — stated
plainly — what it will not do.

---

## 1. The sentence this phase exists to avoid

> This host is up to date.

That is what an operator reads before deciding they are safe, and both package
managers make it trivially easy to say when it is not true. Measured, on this
project's own container:

```console
$ apk update            # every repository unreachable
WARNING: updating and opening https://…/main: DNS lookup error
2 unavailable, 0 stale; 198 distinct packages available
$ echo $?
0
$ apk version -l '<'
Installed:                                Available:
$ echo $?
0
```

Exit status zero, an empty list, and no way to tell that apart from a host with
genuinely nothing to do. apt sets the same trap in a different shape: it prints
`Err:` for a repository it cannot reach and carries on.

So this phase reports three states rather than two — **outstanding**, **nothing
outstanding**, and **not known** — and the third is a first-class answer with a
reason attached. Everything else here follows from that.

---

## 2. Where the pending list comes from

From what the package manager says it will **do**, not from a version
comparison.

The two disagree, and the disagreement is not academic:

```console
$ apk version -l '<'
git-2.45.4-r0                           < 2.47.3-r0
$ apk upgrade --simulate
OK: 479 MiB in 205 packages
```

That package is pinned in Alpine's world file. It will appear in the version
comparison forever and apk will correctly never touch it. A panel listing it as
pending would show an operator a queue that never empties, however many times
they clicked Apply.

The version comparison is still read — it is exactly how the **held back**
packages are found. They get their own section, with the pin named, because they
are somebody's deliberate decision rather than outstanding work.

---

## 3. What this phase got wrong first

### The parser knew only half of what apk prints

apk prints one summary when the refresh worked and a different one when it did
not, and only the second mentions failure:

```console
# everything reachable
OK: 25398 distinct packages available

# two unreachable
2 unavailable, 0 stale; 198 distinct packages available
```

The first parser knew only the second shape. On its first run against a real,
healthy host it found no summary at all — and reported **"not known"**, which is
precisely what it was built to do when it cannot tell. The conservative default
turned a parser gap into a cautious answer instead of a false reassurance, which
is the whole argument for writing it that way.

Both shapes are now understood, and both are frozen in the tests.

### The version helper in the test asked the wrong question

`apk info -v <name>` prints a description; `apk info -v` with no argument prints
`name-version` for everything installed. The first version of the integration
test's helper silently returned an empty string, so the check that the host was
*actually* upgraded compared nothing to nothing.

It failed loudly rather than passing quietly, which is the only reason it was
noticed — a helper that returned a plausible wrong answer would not have been.

---

## 4. What is applied, and what is read back

An apply hands the package manager a list, or nothing at all for "everything".
What goes into the history is **read back from the host afterwards**, not taken
from the request.

That is not tidiness. Asking for one package routinely moves several — in this
project's own test, upgrading `git` moves `git-init-template`, `git-perl` and
`perl-git` with it — and the list an operator needs at three in the morning is
the one that describes their machine.

---

## 5. Rollback, and why there isn't one

The task list says "Rollback". This phase does not implement one, and the reason
is that the hosts cannot do it:

- **apk** keeps no copy of the package it replaced, and Alpine's repositories
  carry only the current version of anything.
- **apt** is the same in practice: a Debian security update's predecessor is
  normally gone from the archive the moment it is superseded.

A button promising to undo an update would therefore be a button that takes a
service down and then fails. What is offered instead is two real things:

- **A revert that asks first.** The Agent asks the package manager whether that
  exact version can still be installed — `apk policy`, `apt-cache policy` — and
  refuses with a plain message when it cannot. On most hosts most of the time,
  that refusal *is* the answer, and having it before anything happens is worth
  more than a rollback that half-works.
- **A history with exact versions.** Every run records what moved, from which
  version to which. Nothing on the host holds that, and it is what makes a
  manual recovery possible at all.

The task item is left unticked rather than quietly claimed.

---

## 6. Security updates, and the honest answer on Alpine

apt can identify them, from the origin it prints with each candidate:

```text
Inst libgcrypt20 [1.10.1-3] (1.10.1-3+deb12u1 Debian:12.15/oldstable, Debian-Security:12/oldstable-security [amd64])
```

apk cannot. Alpine publishes security fixes as ordinary package versions and apk
has no security channel to ask about.

So the panel reports **whether the host can tell**, and on Alpine it says so
rather than reporting "0 security updates" — a sentence that answers a question
nothing asked and reads as "nothing urgent". A security-only automatic policy on
such a host would apply nothing at all, and the settings page says that too,
where somebody is about to choose it.

---

## 7. Automatic updates, and why they are off by default

Applying updates restarts daemons. An operator who has not asked for that should
not discover it from their monitoring at three in the morning, so the default
policy is `off` — the panel reports and applies nothing.

Three policies, and no more:

- **off** — report only.
- **security** — the setting somebody can leave on without a major version of
  something changing under a running site.
- **all**.

The schedule is a day and a time rather than a cron expression. The panel
already has a cron page for a customer's own jobs; this is the panel updating
the machine it runs on, and the useful question is "which quiet hour", not
"express an arbitrary schedule". A narrower control is also one whose next run
the panel can state plainly, which matters for something that restarts daemons.

**Excluded packages** are the escape hatch that makes the whole feature usable:
an operator with one package they must upgrade by hand can say so without
turning automatic updates off. It is the panel's own list and is *not* a pin —
the host's package manager is never told about it, so nothing here changes what
a person can do at a shell.

---

## 8. Why the scheduler is not a cron job

Phase 10 gives the panel a cron page, and writing a crontab entry would have
been less code. It would also have been wrong twice:

- Those jobs run as a website's own unprivileged account. Upgrading the machine
  needs root, which only the Agent has.
- A schedule written into a crontab is a schedule the panel can no longer
  describe. "When does this next run" becomes a question about a file somebody
  may have edited.

So the panel keeps the schedule, its own loop decides when it is due, and the
Agent does the privileged part — the same arrangement as the metric sampler, for
the same reasons.

The loop wakes every minute, which is fine enough to hit a window given to the
minute. What stops it acting twice in one window is that a run inside the last
hour counts as "this window already ran".

---

## 9. What the panel refuses

- **A package name that is really an option.** `--allow-untrusted` is not a
  package; it is a flag, and passing it through would let a request change how
  a program running as root *behaves* rather than what it operates on. This is
  the single most important rule in the phase.
- **A name carrying a version.** A pin belongs in its own field, or two
  different places would be deciding what version is meant and only one of them
  checking it.
- **Applying anything on the basis of a check that failed.** There is no list to
  apply, and "applied nothing successfully" is the wrong answer.
- **A security-only apply on a host that cannot identify security updates.**
- **Two updates at once.** Package-manager lock contention at best, a
  half-applied set of packages at worst.
- **A revert to a version the host can no longer install.**
- **An hour outside the day, a day outside the week, a check interval under an
  hour or over a week.**

---

## 10. Where it meets the other phases

- **PHP (5) and Node.js (9).** Their updates are the same package list, filtered
  to the packages those phases installed — a *view*, not a second question. A
  PHP update is a package update, and asking the host twice would be two answers
  that can disagree. What the panel adds is knowing which packages are PHP,
  because it installed them.
- **Cron (10).** Deliberately not used. See section 8.
- **Services (12).** An upgrade restarts daemons, so applying one invalidates
  what the Services page is showing.
- **Security Center (15), which this unblocks.** The package update scanner it
  needs is `GET /api/v1/updates` — including, and especially, the field that
  says whether the answer can be believed.

---

## 11. What the tests prove

**Go tests** (`agent/internal/updates`, 15 tests) run against recording stubs
whose output is copied from a running apk 2.14 on Alpine 3.21 and a running apt
2.6 on Debian 12 — the latter from a throwaway container started for the
purpose, so the apt parser is written against measured output rather than
documentation. Two samples exist only because reality surprised me: apk's two
different refresh summaries, and a pinned package appearing in one command's
output and not the other's.

They cover the unreachable-repository case reporting *unknown*, the pending list
coming from the simulation, a pin becoming a held entry rather than a pending
one, apt's security marking, an apply reading back a dependency nobody asked
for, a package name that is an option being refused before anything runs, and a
revert refusing a version the host cannot install.

**API tests** (`api/internal/updates`) cover the scheduler's decisions — when a
check is due, when a window is due, that a window does not fire twice, and that
"every day" is not Sunday — plus the transcript trimming that keeps both ends.

**Integration** (`tests/integration/phase21_updates.sh`, 34 checks) runs inside
the Agent's container against a real package manager. It creates a genuine
pending update rather than waiting for one — installing a package from the
previous Alpine release, which leaves the host behind by exactly one known
version — and then:

- a healthy host reports a successful check, and is not reported as unreadable
- every repository is made unreachable, and the panel says *not known* rather
  than "up to date", and refuses to apply on that basis
- the pending update is found with the version installed now
- applying it leaves the host **actually** at the newer version, and the record
  includes the three dependencies that moved with it
- the same package pinned appears as held and *not* as outstanding
- a revert to an unavailable version is refused
- a package name that is an option is refused, in an apply and in an exclusion
- the schedule round-trips, including "every day" as -1

```bash
make docker-test-updates
```

**Frontend** (`UpdatesPage.test.tsx`, 14 tests) cover the three summary states
and, specifically, that neither an unchecked host nor a failed check ever
renders "up to date"; the security wording on a host that cannot tell; the held
list; the confirmation that warns dependencies will move; the history with exact
versions; a failed run showing its reason; and the schedule saving -1 rather
than Sunday.

---

## 12. Known limitations

- **Two package managers.** apk and apt, which is what the rest of this panel
  supports. dnf hosts are reported as having no package manager the panel can
  use, rather than half-working.
- **apt is not covered by the integration test.** Its parsers are written and
  unit-tested against output measured from a real Debian 12, but the end-to-end
  test runs on this project's Alpine container, so the *applying* half is proved
  on apk only.
- **No rollback.** Section 5.
- **No reboot.** The panel reports that a host wants restarting — from Debian's
  own flag file — and will not do it. Rebooting the machine the panel runs on is
  not something to trigger from a web page, and Alpine has no such flag, so the
  answer there is always "no" whether or not a kernel changed.
- **No per-package scheduling.** The window applies to everything the policy
  allows; a package that needs its own timing belongs on the exclusion list and
  in somebody's hands.
- **No changelog.** What an update contains is the distribution's to publish and
  is not in the package metadata either manager exposes cheaply.
- **The check is per host.** This panel manages one; the schema keys everything
  by server so a second one needs no migration.
