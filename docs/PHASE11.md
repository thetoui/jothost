# Phase 11 — Logs

What this phase adds, the decision it rests on, and — stated plainly — what was
left out and why.

---

## 1. What it does

The panel shows the host's logs: nginx's access and error logs, Apache's error
log, one per installed PHP version, one pair per Node application, the machine's
own system, cron and authentication logs, and the Agent's audit log.

Each can be searched, filtered by severity, followed while it is being written,
and downloaded whole.

It comes early in the build order because three later phases read logs rather
than build viewers: Fail2Ban (18) decides bans by reading them, and 10, 7.1 and
27 each register a source rather than growing their own page. The extension
point is a table and a contributor function, not a framework.

---

## 2. The decision this rests on: a catalogue, not a path

**A request names a key the Agent knows. The file it becomes is chosen by the
Agent, from a table in this repository.**

This is the same rule the service manager follows, and here it is the difference
between a feature and a hole. "Show me a log" and "read a file I name" are the
same request with different intentions, and only the first is wanted:
`/etc/shadow` is a perfectly well-formed log request if the API takes a path.
Validating the string afterwards does not help, because the problem is not the
string's shape — it is that the caller got to choose the file at all.

So `GET /logs/nginx.access` is answered and `GET /logs/%2Fvar%2Flog%2Fmessages`
is a 404, even though the second names a real log the panel is perfectly willing
to show under its own key.

### The second lock: where a key may resolve to

The catalogue is what stops a caller naming a file. It does not stop a *file*
from pointing somewhere else, and that is a real path rather than a theoretical
one: `/var/log/nginx/access.log` is writable by the account nginx runs as on
most hosts. Replace it with a symlink to a private key, and a panel that
followed it would read the key back to whoever asked.

So every catalogued path is resolved through `pathsec` before it is opened:
symlinks are followed to their target, the target must be a regular file — a
FIFO where a log is expected blocks a reader forever — and it must lie inside
`/var/log` or `/var/www`. A Provider whose roots could not be resolved reads
nothing rather than everything.

The integration suite proves this end to end: it plants a symlink to
`/etc/shadow` where a log belongs, and checks the panel reports the log absent
and returns a 404 rather than the file.

---

## 3. Reading a file that is being written

A log is not a document; it is a stream that happens to be stored in a file, and
most of the care in `agent/internal/logs/read.go` is about that difference.

**The last line is usually half-written.** The read stops at the end of the last
*complete* line and reports that offset, so the fragment arrives whole on the
next read instead of being shown in two pieces as though they were two events.

**Following is a byte offset, not a stream.** A response carries `offset`;
passing it back returns only what was appended since. A quiet log transfers
nothing at all.

**Rotation is reported, not smoothed over.** When the file is smaller than the
caller's offset, it was rotated or truncated underneath them and their place no
longer exists. The panel says so and shows the new file from its beginning.
Appending the new file to what was already on screen would splice two files
together and present them as one.

**Everything is bounded.** At most 256 KiB of the tail is examined per request,
at most 2000 lines are returned, and a single line is truncated at 4 KiB. A
month of logging must not be loaded into memory to show the last twenty lines,
and one stack trace written without newlines must not be able to fill a
response.

**Control characters are stripped.** Not for tidiness: an escape sequence that
reaches a terminal — an operator piping a download through `cat` — can move the
cursor and rewrite what was already printed there. Tabs survive, because logs
align their fields with them.

---

## 4. One severity filter across seven formats

Every daemon writes its own idea of a level, and the panel offers one filter. It
is only worth having if "errors only" means the same thing on every page, so
each format is parsed for the word it actually uses:

| Format | Where the level is |
|---|---|
| nginx error | `[error]` in the first bracketed group |
| Apache error | `[proxy:error]` in the *second* one, after the timestamp |
| PHP | `WARNING:` after the timestamp, or `PHP Fatal error:` in the message |
| JSON | the `"level"` field, found textually rather than by decoding |
| Agent audit | the `"status"` field: an audit log records an outcome, not a severity |
| nginx access | the response status: 5xx is an error, 4xx a warning |
| syslog | the words daemons write, bounded so `error-node-1` is not a level |

Two of those rows are decisions rather than parsing.

**An access log has no severity**, which makes "show me the errors" unanswerable
on the log an operator most often wants to ask it about. The status code is the
honest answer.

**An audit log records an outcome.** Every line is a privileged operation that
was attempted, and the ones worth finding are the ones that failed. Without this
row, "errors only" on the panel's own audit log would return nothing, which
reads as "nothing has ever gone wrong".

And one rule holds everywhere: **a line whose level cannot be read is never
filtered out.** Hiding a line because this code failed to recognise its shape is
how an operator concludes the panel is lying to them.

---

## 5. Following is polling, and that is deliberate

The obvious implementation of "live logs" is a server-sent event stream. It was
not chosen, for a reason worth writing down.

`EventSource` cannot send an `Authorization` header. The alternatives were
putting the access token in the URL — where it would be written into every proxy
log between the browser and the panel, including the very access log this page
displays — or building a second authentication path used by one feature.

Polling a byte offset costs one small request every three seconds and needs
neither. After the first read, each poll asks only for what was appended, so
following an idle log is a few hundred bytes a minute. The page keeps at most
5000 lines on screen, because a log being written to quickly is exactly the one
an operator leaves open for an hour while they wait for a problem to recur.

---

## 6. What is not implemented, and why

`API_SPEC.md` section 19 sketched `offset`, `from` and `to` alongside `limit`,
`search` and `level`. The first three are not implemented.

**`from` and `to` would need a timestamp parser per format.** nginx writes
`2026/08/31 00:34:01`, Apache writes `[Sun Aug 31 00:34:01 2026]`, PHP writes
`[31-Aug-2026 00:34:01]`, syslog writes `Aug 31 00:34:01` with no year at all,
and JSON writes RFC 3339. A time filter that silently returns nothing because it
failed to parse one of them is worse than a filter that is not offered: the
operator concludes the log is empty. `search` answers the same question
imprecisely and never lies about it.

**`offset` as a page number** is not how anyone reads a log. The end is what
matters, `search` is how you find something further back, and `after` — a byte
offset, present and doing real work — is how a follower keeps its place.

Also absent:

- **Per-website logs.** A site's own access and error logs belong on that site's
  page, next to the site they describe, and Phase 4 already serves them there.
  Two routes to one file, each with its own idea of who may read it, is how the
  two drift apart.
- **The journal.** A host running systemd keeps most of this in `journalctl`,
  which the Agent does not allowlist. Where a file exists it is read; where one
  does not, the source is listed as absent rather than fabricated.
- **Rotated files.** `access.log.1` and `access.log.2.gz` are not offered. The
  current file is what a panel is for; the archive is what `ssh` is for.

---

## 7. What the tests prove

**Go tests** (`agent/internal/logs`) run against real files in a temporary root
rather than a filesystem abstraction, because what can go wrong here is about
actual files. They cover the half-written last line, following from an offset,
rotation, the scan bound, line truncation, control-character stripping, the
level mapping for every format above — including the two negative syslog cases,
`error-node-1` and "terrorist" — and the symlink refusal.

**Integration** (`tests/integration/phase11_logs.sh`, 49 checks) runs inside the
Agent's container, so what the panel returns is compared with the files on disk:

- a log created at runtime appears without the panel being told
- the size reported is the file's own, counted with `wc -c`
- searching returns the matching line and nothing else, and says how many it hid
- following returns only what was appended, a half-written line is withheld and
  then arrives whole, and a rotated file is reported as rotated
- a download is `cmp`-identical to the file on disk, served as an attachment
- a path, an encoded path, a null byte and a wrong-case key are all 404s
- a log symlinked to `/etc/shadow` is reported absent and returns nothing

```bash
make docker-test-logs
```

**Frontend** (`LogsPage.test.tsx`, 7 tests) covers the picker showing absent
logs as absent, filters being sent to the host rather than applied in the
browser, the debounce, the rotation notice, and following asking only for what
is new.

---

## 8. Known limitations

- **The scan window is the end of the file.** A log larger than 256 KiB is shown
  from where that window starts, and the page says so. Searching further back
  than that needs the download.
- **Search is a substring, not a pattern.** No regular expressions: a caller
  supplying one would be handing the Agent unbounded work per line.
- **`level` filters what a format declares.** A daemon that writes its errors
  without saying so is not detected as an error, and its lines are shown rather
  than hidden.
- **No aggregation.** One host, one file at a time. Reading every site's error
  log at once is a different feature and a much larger one.
- **The audit log grows without bound in development.** Nothing here rotates it;
  the dev container's is already 18 MB, which is what made the scan bound worth
  testing against a real file.
