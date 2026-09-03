# Phase 15 — Security Center

What this phase adds, the rule the whole thing turns on, and — stated plainly —
what it will not do.

---

## 1. The rule everything else follows from

**A check that did not run is not a check that passed.**

TASKS.md says as much in this phase's dependency note: it depends on 16, 17, 21,
6 and 7 "or the score is computed from blanks". That is the failure this phase
is most able to commit, and the reason it is dangerous is that a score computed
from blanks looks *identical* to a good one — reassuring, green, and wrong.

So every scanner returns one of three things rather than an error:

| Outcome | What happens to the score | What happens to its old findings |
|---|---|---|
| Ran, found problems | they cost score | updated in place |
| Ran, found nothing | costs nothing | **resolved** |
| **Could not run** | **neither** | **left exactly as they were** |

The third row is the whole design. An unreadable firewall must never resolve
"the firewall is disabled", and a host whose SSL could not be read is not a host
with good certificates. And the number of checks that answered travels with the
score everywhere it appears — in the API, in the stored scan, and on the page,
which says outright that a partial score is "incomplete rather than reassuring".

It is the same shape as Phase 21's "not known is not up to date" and Phase 19's
"an unavailable reading is neither a breach nor a recovery". Three phases, one
lesson: the absence of an answer is its own answer, and collapsing it into the
good one is how monitoring lies.

---

## 2. Why five of the seven scanners probe nothing

The phase list names seven scanners. Five of them — SSH, firewall, SSL, updates,
fail2ban — ask the phase that manages the thing rather than looking at the host
again.

That is Phase 19's threshold lesson applied to facts instead of numbers: one
definition, several views. A Security Center that ran its own `sshd -T` would
eventually disagree with the SSH page about whether root login is permitted, and
a panel whose two pages contradict each other about a security setting is worse
than a panel with one page.

The SSH scanner goes furthest: Phase 17 already derives findings from the
*effective* configuration, and this maps them rather than re-deriving. What it
adds is the one thing Phase 17 has no reason to say — that a correctly
configured server is not running.

Two scanners are genuinely new, because nothing else in this panel can answer
them, and both live in the Agent because both need the host.

---

## 3. What is listening

Read from `/proc/net/tcp`, `tcp6`, `udp` and `udp6`, with the socket-to-process
mapping made by walking `/proc/<pid>/fd` — which is what `ss` and `lsof` do.

**No program is executed.** There is no `ss`, no `netstat`, no `lsof` and no
`find` anywhere in this phase. Parsing the kernel's tables is fifty lines;
shelling out would mean allowlisting a program whose output format varies by
version, on a host where the answer matters most when something is already
wrong.

The distinction the scanner exists for is **loopback versus everything else**. A
database on `127.0.0.1` is reachable only from the machine; the same database on
`0.0.0.0` is the single most common way a hosting box is taken over — and it is
invisible from every other page in this panel. The firewall page shows what is
*blocked*; the services page shows what is *running*. Only this shows that
MariaDB is running, bound to every interface, behind a firewall somebody
disabled last year.

Two details worth naming:

- **The byte order is the thing that goes wrong.** `/proc/net/tcp` writes each
  32-bit word of an address in host order, so an IPv4 address comes out reversed
  and an IPv6 address is reversed *within* each word but not across them. Get it
  wrong and every address is plausible and none is right. It has a test against
  captured kernel output for that reason.
- **Sockets are reported even when their owner cannot be identified.** Walking
  other processes' file descriptors needs root, and an Agent that has been
  dropped to a lesser account still reports the sockets, without names —
  `processes_resolved: false`. A socket with no name is still a listening
  socket, and "we could not tell you what it is" is a far better answer than
  silence.

A short closed list of ports is expected (HTTP, HTTPS, SSH, FTP, DNS) and
everything else public is reported. A list of exceptions long enough to cover
every service would cover the wide-open Redis too.

---

## 4. What is writable

A filesystem walk over the directories this panel put things in, looking for
four things:

- **World-writable files under a site.** Any account on the box — including one
  belonging to another customer's site — can replace the code that runs as the
  first customer. It is the usual way one compromised site on a shared host
  becomes all of them.
- **setuid or setgid executables under a site root.** A web application has no
  use for one. Its presence is a mistake or the second stage of an intrusion.
- **`.env`, `.git` and their neighbours inside a *document root*.** Both are
  served to anyone who asks by name; `.env` holds the database password by
  convention and `.git` holds every version of the source, including the
  credentials somebody committed and then removed.
- **Private keys readable by more than their owner.**

Three deliberate refusals:

- **A sticky directory is not a finding.** `/tmp` and the per-site temp
  directories PHP needs are meant to be world-writable, and the sticky bit is
  what stops one account deleting another's files. Reporting them would bury the
  real ones.
- **A `.env` one level *above* the document root is not a finding.** That is the
  correct place for it, and reporting it would teach people to ignore the
  scanner.
- **Symlinks are never followed.** Following one would take the scan outside the
  roots it was given, and would loop on the first link to a parent.

The scan is bounded — entries, depth, and reported examples — and when it hits a
bound it says so, because "we found nothing else" and "we stopped looking" are
not the same answer. The counts are then reported as lower bounds.

---

## 5. The score

It starts at 100 and subtracts a weight per open finding: critical 40, high 20,
medium 10, low 4, info 0.

**Simple beats clever here.** A score nobody can predict is a score nobody
trusts, and the first time it moves for a reason somebody cannot reconstruct
they stop reading it.

The weights are far apart on purpose. One critical costs more than every low
finding a host is likely to have put together, because that is true: a database
on `0.0.0.0` is not four hygiene issues, it is a different kind of problem. A
scale where enough small things added up to one big thing would let a host with
a wide-open Redis score better than one with untidy file modes.

Info costs nothing. It exists so the panel can say things worth knowing without
either inflating them into a problem or leaving them out.

An unrecognised severity costs the same as medium rather than nothing — a
severity this panel does not understand must not be free.

The grade is a word, not a letter: "poor" says what to do with the number, "C"
makes somebody work out the scale first.

**A host that has never been scanned gets no score at all** — not a zero. A host
nobody has looked at is not a host with problems and it is not a host without
them, and a number there would make one of those up. The page says so instead.

---

## 6. Accepting is not resolving

There is no endpoint that resolves a finding, and `PATCH` with
`status: "resolved"` is refused with a message saying why. Whether a weakness
still exists is the scanner's to decide. A panel where a person can mark an open
port as closed is a panel that will one day say an open port is closed.

What a person *can* do is accept a risk, and three rules make that safe to
offer:

1. **A reason is required**, in the schema as well as in Go. An acceptance with
   no reason is a mute button, and a mute button on a security page is how a
   real problem becomes permanent. Somebody will read it in a year and needs to
   know it was a decision rather than an oversight.
2. **Accepted risks are never hidden.** They have their own section on the page
   and the count sits beside the score forever. A score carried by accepted risk
   is a different thing from a clean one, and hiding that would turn accepting
   into a way to reach 100 by writing sentences.
3. **An accepted finding that gets worse comes back.** The severity at the
   moment of acceptance is stored, and a rescan that finds the same thing at a
   higher severity re-opens it and clears the acceptance. Accepting "SSH listens
   on port 22" is not accepting "SSH permits root login with a password", and a
   scan that kept the acceptance across that increase would turn one considered
   decision into permanent blindness.

The reverse does not apply: a finding that gets *better* stays accepted, because
re-opening on an improvement would be the panel arguing with a decision that has
become more conservative.

---

## 7. Findings are rows with a stable identity

Each finding carries a **fingerprint** — the identity of the thing being
reported, not of the row. Without it every scan inserts a fresh copy, and an
operator running a nightly scan accumulates three hundred rows describing one
setting.

With it, a rescan updates in place and `first_seen_at` means something: *how
long this host has been wrong about this*. That is usually the sentence that
gets something fixed.

The unique index is partial on "not resolved", so something fixed in March and
undone in June is two rows. That is the correct answer — collapsing them would
hide the second incident.

Findings are grouped where the fix is one action. Four hundred world-writable
files are one problem with one fix, and four hundred rows would bury every other
finding on the page; the same is true of ten sites with no certificate. What a
grouped finding must never lose is *which* things it covers, so the count is
exact and the first few are named. Expired certificates stay per-site, because
each is individually urgent and individually actionable.

---

## 8. Where this diverges from the specification

**DATABASE.md section 27** specifies `security_findings` with id, server_id,
severity, category, title, description, status, metadata and two timestamps. All
of them are built. Added: `scanner`, `remediation`, `fingerprint`,
`first_seen_at`/`last_seen_at`, and the four acceptance columns. A findings
table that cannot be rescanned doubles in size every hour, and `remediation`
exists because a finding without a next step is a nag.

**`security_scans` is not in DATABASE.md** and is added, because two things are
impossible without it. A score is only meaningful next to when it was taken —
"68" means nothing and "68, an hour ago, down from 91 last week" means a great
deal. And a panel with no scan row cannot distinguish "this host is clean" from
"this host has never been looked at", which a findings table alone renders
identically, as no rows.

**API_SPEC.md section 25** lists four endpoints and all four are built.
`GET /security/score` answers with the findings as well as the number: the score
alone would be a number with no way to act on it, and a page that made two calls
would show the number first and the reasons a moment later, which is the wrong
order to read them in.

---

## 9. What the panel refuses

- **Resolving a finding by hand.** Section 6.
- **Accepting without a reason**, or with one too short to be one.
- **Accepting something that is not open**, or reopening something that is not
  accepted.
- **A severity outside the scale**, in the schema as well as in Go.
- **An unknown scanner or severity in a filter**, so a typo is a 422 rather than
  a silently empty list that reads as "nothing wrong".
- **Two scans at once.** They would each resolve the findings the other had just
  written, and the survivor would be whichever finished last.
- **A scanner path from a request.** Neither host probe takes a parameter: one
  that accepted a path would be a way to enumerate the filesystem through an
  endpoint meant to be run often by everyone with a security role.

---

## 10. Where it meets the other phases

- **SSH (17), Firewall (16), SSL (6), Updates (21), Fail2Ban (18).** Asked, not
  re-probed. Section 2.
- **Updates (21) in particular.** Its central distinction is carried through
  unchanged: a check that never succeeded makes the update scanner *unavailable*
  rather than clean, because an empty package list from a failed check reads
  exactly like a host with nothing to do.
- **Websites (4, 4.1).** The SSL scanner reads the sites to find the ones with
  no certificate — the interesting case is the row that is *not* there.
  Subdomains are included, because one served over plain HTTP is exactly as
  exposed as a top-level site.
- **The Agent (2).** Two new operations, both read-only and both parameterless.
- **Notifications (20), which this unblocks.** A finding has a severity, a
  stable identity, a first-seen time and an open/resolved lifecycle, which is
  what there is to deliver.

---

## 11. What the tests prove

**Go, no host** (`agent/internal/security`, 17 tests): that the kernel's byte
order is decoded correctly for IPv4 and IPv6 against captured `/proc/net` output;
that an established connection is not reported as listening; that the same
service bound to loopback and to `0.0.0.0` is one exposure and one
non-exposure; that a host with IPv6 disabled still gets an IPv4 answer; that an
unreadable `/proc` fails rather than returning an empty, clean-looking result;
that a world-writable file, a setuid binary, an exposed `.env` and a readable
private key are each found; that a sticky directory and a correctly placed
`.env` are not; and that the walk does not follow a symlink out of its root.

**Go, no database** (`api/internal/security`, 18 tests): that a check which
could not run changes neither the score up nor down but changes what the panel
*says*; that the summary names the checks that could not run; that one critical
costs more than eight lows; that info costs nothing; that an unknown severity is
not free; that accepted findings cost no score and are still counted; and that a
grouped finding names what it covers without listing everything.

**Go, with a database** (`api/internal/security`, 15 tests): that a rescan
updates rather than duplicating and keeps `first_seen_at`; that resolving one
scanner's findings leaves another's alone; that accepting requires a reason at
the schema level; that an accepted finding which gets worse is re-opened and one
that stays the same or improves is not; and that something fixed and broken
again is two rows.

**Frontend** (`SecurityCenterPage.test.tsx`, 15 tests): that an unscanned host
is told apart from a clean one; that the score never appears without the checks
behind it; that a partial score says "incomplete rather than reassuring"; that
there is no resolve button and the page says why; that accepting asks for a
reason and promises the finding will come back if it worsens; and that accepted
risks stay visible with their reason.

**Integration** (`tests/integration/phase15_security.sh`, 39 checks) against the
live stack and the real host:

- a **real service is bound to `0.0.0.0`** on the host and the scan finds it by
  reading `/proc`, names what the port is for, and grades it critical — while
  the database on `127.0.0.1` in the same container is not reported
- a **real world-writable file and a real `.env`** are created in a real site's
  document root and both are found by a real filesystem walk
- **fixing all three makes the findings disappear by themselves** on the next
  scan, which is the half a page of checkboxes could never do
- a rescan produces the same number of findings rather than duplicating them
- accepting is refused without a reason and refused with one too short to be one
- a finding cannot be marked resolved by hand
- an accepted risk is counted, is no longer outstanding, and can be reopened

```bash
make docker-test-security
```

---

## 12. Known limitations

- **No scheduled scanning.** A scan runs when somebody asks. Phase 20 is where a
  schedule belongs, because a nightly scan whose results nobody is told about is
  a nightly scan nobody reads.
- **The port scanner does not know what *should* be listening.** It has a short
  list of expected ports and reports everything else public at low severity,
  which on a host running something unusual means a finding to accept once. The
  alternative — inferring intent from the running services — would quietly
  approve whatever an intruder installed.
- **The permission scan covers the site root only.** Not `/etc`, not `/usr`, not
  an operator's home directory. Scanning those would produce a long list of
  things this panel did not create and cannot fix, which is a scanner nobody
  reads — but it does mean a world-writable file outside a site is missed.
- **`.htaccess` and server configuration are not checked.** A `.git` blocked by
  an nginx rule is still reported as exposed, because this phase reads the
  filesystem rather than reasoning about the web server's configuration.
- **No CVE matching.** The update scanner reports how many security updates are
  outstanding, not which vulnerabilities they fix. That needs a vulnerability
  database and a feed to keep it current, which is a different kind of feature.
- **No file integrity monitoring.** The permission scan says what is writable,
  not what has changed.
- **The score is not comparable between hosts.** A host with fifty sites has
  more opportunities to score badly than one with two. It is meant to be read
  against its own history, which is what the scan table is for.
- **One server.** Like every phase so far, the Security Center scans the local
  host.
