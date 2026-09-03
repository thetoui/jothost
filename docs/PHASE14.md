# Phase 14 — Backup

What this phase adds, the sentence the whole thing turns on, where it diverges
from the specification and why, and — stated plainly — what it will not do.

---

## 1. The sentence everything else follows from

**A backup that has not been read back is not a backup.**

Every path in this phase ends with the archive being read from where it was
stored — not from the staging copy still warm in the page cache — and its
SHA-256 recomputed. Three failures look exactly like success at the moment of
writing and are only ever caught by reading:

- an upload that returned 200 and wrote nothing,
- a disk that silently dropped the last block,
- a destination pointing at a directory that no longer exists.

So `verified_at` is a column of its own, separate from `status`. "The upload
returned success" and "the bytes are there and correct" are different claims,
and only the second is a backup. A backup that completed and could not be
confirmed is stored as **failed**, with the reason — because a listing where
"completed" sometimes means "probably" is a listing nobody can use to decide
whether they are safe.

That is also why CLAUDE.md section 18's rule — never overwrite a valid backup
before verifying the new one — needed no special case. Retention runs only after
a backup has completed *and* verified, so there is no ordering in which a good
archive is deleted in favour of one that was never checked. A week of failing
backups leaves a week of old ones.

---

## 2. What an archive is

One gzip-compressed tar file:

```text
sites/<domain>/…            the document root, mode and symlinks preserved
databases/<engine>/<name>.sql
manifest.json               last
```

The manifest carries a SHA-256 per member, and the archive has a digest of its
own computed while it is written.

**The manifest is last** because the digests it records are not known until the
members have been written; a manifest written first could only describe what was
*intended*. Reading it costs a scan to the end, which is what verifying does
anyway.

**tar rather than zip**, which is what the file manager uses. The file manager
archives things a person downloads and opens on a laptop, where zip is what
every operating system understands. This archives a Linux host's own filesystem,
where the mode, the ownership and the symlinks are what make a website's
directory work — and zip does not carry them portably.

**The digest is over the compressed bytes.** Digesting the uncompressed stream
would mean a verify had to decompress before it could say anything, and a
truncated archive would fail as "corrupt gzip" rather than as a digest that did
not match.

**Symlinks are archived as links, not followed.** Following them would duplicate
whatever they point at, and a link out of the document root would pull the rest
of the host into a website's backup.

---

## 3. Restoring

**Verified in full before anything on the host changes.** The archive is
downloaded, its digest checked, every member's digest checked against the
manifest — and only then is a file written. Two passes over a local file is a
cheap price for never half-restoring an archive that turns out to be truncated:
a half-restored site is worse than an untouched broken one, because it looks
repaired.

**The document root is moved aside, not written over,** and it is put back if
the restore fails partway. That is what makes the one failure that matters — the
host filling up in the middle of unpacking somebody's site — recoverable. The
displaced copy is deleted on success unless asked for: a complete second copy of
every site on the same disk, left behind after every restore, is how a panel
fills a host.

**Confirming is the backup's own id**, not a boolean. A `confirm: true` is
something a caller sets once and forgets; sending the id of the thing about to
overwrite a live site is a confirmation of *that* restore.

**Only a verified backup can be restored.** Offering to restore an archive the
panel could not read back is offering something it has no reason to believe will
work, at the moment somebody can least afford to find out.

**Where things go is the request's, not the archive's.** Restoring onto a new
host, where the site root differs, is a case this exists for. A site still known
to the panel is restored to where it lives *now*, not where it was when the
archive was written. A site in the archive the request did not ask for is
skipped and named — writing to a path nobody asked about is how a restore of one
site overwrites another.

---

## 4. Where this diverges from DATABASE.md, and why

DATABASE.md section 23 puts `destination_type` and
`destination_config_encrypted` on the schedule itself. This phase builds a
`backup_destinations` table instead, and both schedules and backups reference
it. Three reasons, in the order they bite:

1. **A manual backup needs a destination too**, and the spec's shape gives it
   nowhere to come from but a copy of a schedule's.
2. **Credentials would be duplicated per schedule.** Rotating an S3 key would
   mean editing every schedule that used it, and missing one is a backup that
   silently stops working.
3. **A backup row could name a destination that no longer describes anything**,
   because there would be nothing to reference.

Everything else in sections 22 and 23 is built as specified, with columns added
rather than removed: `checksum`, `verified_at`, `verify_detail`, `manifest`,
`subject`, `keep_last`.

---

## 5. The three destinations

**Local** — a directory on the host, bounded by a configured list of roots.
Without that bound, "back up to /etc/nginx" would be a way to write a file
anywhere on the host as root. The archive is written under a temporary name and
renamed, so a reader never sees a partial archive under a finished one's name.

**S3** — SigV4 written out against the standard library. The Agent has no
third-party dependencies, deliberately: it runs as root on somebody's server,
and the AWS SDK is a great deal of code to gain four requests whose only hard
part is a page of HMAC over a canonical string. The payload digest in the
signature is the archive's own, so a body changed in flight is rejected by the
service.

**SFTP** — the OpenSSH client, through the same allowlisted, argv-only runner as
everything else. An SSH implementation is not something to write out by hand the
way a signature is. `StrictHostKeyChecking` is never turned off and there is no
option to turn it off: a backup sent to whatever answered on port 22 is a copy
of every site on the host handed to a stranger, and "it stopped working after we
rebuilt the backup server" is a far better failure than that. The destination
carries the server's public key and the Agent writes a `known_hosts` from it.

### The one place a line of text is interpreted

`sftp` takes its commands from a batch file. Three things keep that from being
an injection point: every key in a batch line has been through
`validate.BackupKey`, which permits only letters, digits, dots, dashes,
underscores and slashes — no space, quote, newline or backslash can reach one;
every local path is one the Agent itself created; and the file is written by the
Agent, mode 0600, in a private directory, and deleted afterwards.

### Plain http

Refused by default and possible with an explicit acknowledgement. Self-hosted
object storage on a private network is an ordinary arrangement, and a panel that
flatly refused it would be worked around rather than obeyed. So somebody has to
say, in the destination, that they accept a copy of every site on the host
travelling where anyone on the path can read it — and the panel then labels that
destination as insecure wherever it is shown. A loopback address needs no
acknowledgement: a request that never leaves the machine cannot be read on the
way.

---

## 6. Where the credentials are not

**Not in the jobs table.** A job row carries the backup's id and nothing else;
the destination is resolved and decrypted at dispatch, by the
`jobs.PayloadResolver` hook this phase adds. A queue is a table, and a table is
backed up, replicated, and readable by anyone with database access.

**Not in anything a handler returns.** The `Destination` struct has no
credential field at all — a field that is only sometimes cleared is a field that
will one day be returned. The secret is read by a method of its own, at the one
call site that needs it.

**Not in the audit trail, and not in a log.** The one place an S3 error could
leak a signature — a URL in a transport error — is redacted before the message
is stored.

The access key is deliberately *not* encrypted alongside the secret: it is an
identifier, it appears in every request's `Authorization` header anyway, and a
page needs to show which key a destination uses.

---

## 7. Retention

Two numbers, and the second is what makes the first safe.

`retention_days` is the window. `keep_last` is a floor applied **before** the
age filter: the most recent verified backups are excluded whatever their age.
Without that ordering, a panel that had been off for longer than the retention
window would come back, find everything expired, and delete all of it — leaving
nothing at the exact moment somebody is most likely to need something.

Only *verified* backups count towards the floor. Keeping three archives the
panel could not read back is keeping nothing.

An archive that cannot be removed from its destination keeps its row. Deleting
the row first would leave an object nothing knows about, costing money forever
with no way to find it again.

---

## 8. Scheduling

The panel's own loop, not a crontab entry — the same arrangement Phase 21 uses
for updates, for the same two reasons. Scheduled jobs run as a website's own
unprivileged account, and reading every file of every site needs root, which
only the Agent has. And a schedule written into a crontab is a schedule the
panel can no longer describe: "when does this next run" becomes a question about
a file somebody may have edited.

**A missed window is taken late rather than skipped.** A panel that was off at
three in the morning takes the backup when it comes back, because the
alternative is a day with no backup and nothing saying so. The cost is that a
panel restarted at noon takes last night's backup at noon; a late backup is a
backup, and a skipped one is not.

A schedule is marked as run *before* the work is queued. The loop ticks every
minute and queueing takes longer than that on a busy host, so waiting until the
job finished would start the same backup sixty times in an hour.

---

## 9. What the panel refuses

- **A backup of a path somebody typed.** A backup names a thing the panel
  manages, so a restore has somewhere unambiguous to go; a backup of an
  arbitrary path is a restore with nowhere to put it back.
- **A document root outside the site root**, including one that symlinks out of
  it. Without this, "back up the site at /etc" would archive the host's
  configuration — every credential in it — and send it to a destination the same
  request chose.
- **A local destination outside the configured roots.**
- **An object key containing a dot segment, a backslash, a leading dash, or
  anything but filename characters.** One key becomes three different things — a
  path under a directory, a remote path over SFTP, a URL path against S3 — and
  they all go wrong the same way.
- **An archive member that escapes**, checked before a restore touches anything.
  The extractor would not have written it, but an archive restored "successfully"
  with a member quietly dropped is the worst outcome available.
- **An SFTP destination with no host key**, and an SFTP username that would be
  read as an option.
- **A plain-http S3 endpoint** that is not on this machine and has not been
  explicitly accepted.
- **A retention of zero days, or keeping none.**
- **A restore that is not confirmed with the backup's own id**, and a restore of
  a backup that has not been verified.
- **Deleting a destination a schedule still uses.** It would leave the schedule
  unable to run, which is a backup that silently stops happening.

---

## 10. Where it meets the other phases

- **Jobs (4).** Creating and restoring both move an unbounded amount of data, so
  both go through the queue. This phase adds one hook to the worker —
  `PayloadResolver` — for the single reason in section 6.
- **Websites (4, 4.1) and Databases (8).** Two small interfaces, `Sites` and
  `Databases`, rather than a dependency on either package: a backup needs a
  domain, a document root, an account and a database name, and nothing else.
  Subdomains are included in a full backup where the websites page excludes
  them, because a page hiding them is making a listing readable and a backup
  hiding them is losing data. A subdomain sharing its parent's document root is
  archived once.
- **The database providers (8).** Dumping lives with them, because the
  credentials do: the admin password reaches a client through a mode-0600 option
  file those providers wrote at startup, and a second copy of that arrangement
  is one more than can be reviewed.
- **The command runner (2).** One capability added: `StdinFile`, so a dump is
  streamed into a client rather than held in the Agent's heap. A customer's
  database may be gigabytes, and an Agent that died on the largest database it
  was asked to protect would be failing at exactly the wrong moment.
- **Notifications (20), which this unblocks.** A failed backup is a thing to be
  told about, and it now has a row, a reason and a time.
- **Production hardening (24), which this unblocks.** The restore and
  disaster-recovery tests need something to restore.

---

## 11. What the tests prove

**Go, no database** (`agent/internal/backup`, 24 tests): that an archive of a
real directory tree is written, uploaded, read back and matched; that a document
root outside the site root — or symlinked out of it — is refused; that a local
destination outside the allowed roots is refused; that a single byte changed in
the middle of an archive is caught while its size still matches; that a restore
of a corrupt archive changes nothing and says so; that the displaced copy is
kept only when asked for; that every shape of tar-slip is refused, including a
symlink pointing out of the destination; that deleting is idempotent; that a
destination check leaves nothing behind; that SigV4 signs the right headers with
the archive's own payload digest and encodes a path the way the algorithm
requires.

**Go, with a database** (`api/internal/backup`, 15 tests): that a destination
never returns its secret; that a backup which could not be verified is recorded
as failed and is not restorable; that retention keeps the most recent whatever
their age, counts only verified backups towards the floor, and leaves anything
inside the window; that deleting a schedule keeps the backups it took; that a
destination a schedule uses cannot be deleted; that a missed window is taken
late; that the same window is not run twice; and that a weekly schedule is due
only on its day.

**Validation** (`shared/validate`, 14 tests): every shape of key that could
escape, the S3 endpoint and bucket rules, an SFTP username that would be read as
an option, and a digest in the wrong case — which would never match and would
report every intact archive as corrupt.

**Frontend** (`BackupsPage.test.tsx`, 17 tests): that a host which cannot take
backups says so with the reason; that a destination nobody has reached is
flagged; that "written" and "confirmed" are shown differently; that an
unconfirmed backup has no restore button; that the restore confirmation is the
backup's id; and that a website backup includes its databases by default.

**Integration** (`tests/integration/phase14_backup.sh`, 59 checks) against the
live stack and the real host:

- a real website with a real database row is backed up, the archive lands at its
  destination under a key a person can find, and the panel confirms it by
  reading it back
- the site's files are deleted and its table dropped, and a restore brings both
  back — the file with its contents, the row with its value
- one byte is changed in the middle of the stored archive: verification catches
  it, and the backup can no longer be restored
- the same backup is written to a **real S3 service** and read back from it, and
  to a **real SSH server** with a real host key check — and a destination whose
  host key does not match is refused
- a destination outside the allowed roots cannot be reached
- a schedule runs, its backup records which schedule took it, and deleting the
  schedule keeps the backup
- deleting a backup removes the archive as well as the row
- and every refusal in section 9

```bash
make docker-test-backup
```

---

## 12. Known limitations

- **No incremental or deduplicated backups.** Every backup is a full copy. That
  is the right first implementation — it has no state to corrupt and no chain to
  break — but it is expensive for a large site backed up nightly.
- **S3 uploads are a single PUT, capped at 5 GiB.** An archive larger than that
  is refused with a message saying so rather than half-uploaded. Multipart is
  not hard, but it introduces an upload that can be abandoned half-done and
  needs a lifecycle rule to clean up after itself, and a backup system whose
  failure mode is "silently accruing charges" is worse than one that says no.
- **No download endpoint.** API_SPEC lists `GET /backups/:id/download`; it is
  not built, because streaming a multi-gigabyte archive through the API — which
  cannot reach the destination itself — would mean proxying it through the Agent
  socket. An operator fetches an archive from its destination directly, which is
  also what they will have to do on the day the panel is what broke.
- **No encryption of the archive itself.** The credentials that reach a
  destination are encrypted; the archive is not. A backup on somebody else's
  storage is readable by them.
- **A full backup is one archive.** A host with fifty sites produces one large
  file, and losing it loses all of them. Per-site schedules are the answer, and
  they exist.
- **Restore is all-or-nothing per site.** There is no "restore this one file",
  which is the request an operator makes most often.
- **The panel's own database is not backed up.** It is not on this host — it is
  the control plane — and backing up the thing that records the backups from
  inside itself is a circularity better solved by the installer.
- **No verification schedule.** A stored backup is checked when it is written
  and whenever somebody asks. Bit rot found six months later is found by a
  person pressing a button.
