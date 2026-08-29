# Phase 7 — File Manager

**Status:** Complete
**Scope:** TASKS.md Phase 7, PRD.md section "File Manager", API_SPEC.md section 12

Phases 4 to 6 gave a website a directory, a PHP runtime and a certificate.
Phase 7 opens that directory: browse it, upload to it, and change what is in it,
without handing anyone a root shell.

---

## 1. What was built

### 1.1 Operations

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/files` | One page of a directory |
| `GET /api/v1/files/stat` | One entry |
| `GET /api/v1/files/download` | Stream a file to the browser |
| `GET /api/v1/files/search` | Find by name, or by content |
| `POST /api/v1/files/upload` | Stream a file in (multipart) |
| `POST /api/v1/files/folder` | New folder |
| `POST /api/v1/files/file` | New empty file |
| `POST /api/v1/files/copy` | Copy a file or a tree |
| `POST /api/v1/files/move` | Move a file or a tree |
| `POST /api/v1/files/zip` | Create an archive |
| `POST /api/v1/files/unzip` | Extract an archive |
| `PATCH /api/v1/files` | Rename, or change permissions |
| `DELETE /api/v1/files` | Delete, recursively when asked |

Reads need `file.read`, every mutation needs `file.write`. Both permissions were
already seeded in Phase 1, so this phase adds no migration — file operations
hold no state of their own.

### 1.2 The transport is what shapes this phase

The Agent's socket refuses a request or a response over **1 MiB**
(`agent/internal/socket/server.go`). A file manager has to move files far larger
than that, so nothing here sends a whole file in one message:

```text
browser ⇄ API            one HTTP request, whole file
API     ⇄ Agent          256 KiB chunks, base64 in JSON
Agent   ⇄ disk           one chunk at a time
```

The chunk is 256 KiB because base64 costs a third more, which leaves the encoded
chunk near 342 KiB and the rest of the budget for the envelope.

The API streams in both directions: a download is written to the response as
each chunk arrives, and an upload is read from the request body and forwarded as
it goes. Neither side ever holds a whole file, so a 2 GB download costs one
chunk of memory rather than 2 GB.

Directory listings are paged for the same reason. A document root can hold far
more entries than a 1 MiB response can carry, so a listing reports its total and
whether it was truncated.

### 1.3 UI

`/files` is a browser with a breadcrumb, a table showing size, permissions,
owner and modification time, and a toolbar for everything that changes
something. Permissions get a dialog with both the checkbox grid and the octal
value, because hosting documentation is written in octal while the checkboxes
are what make a wrong value obvious.

Every write control is behind `file.write`, so a `file.read` user gets a
browser and nothing else.

---

## 2. Security

The Agent runs as root. Everything below follows from that.

### 2.1 Every path goes through pathsec

`agent/internal/pathsec` was built in Phase 2 and does the work: normalise,
refuse traversal, resolve symlinks, compare against an allowed root. Phase 7
adds no new path logic — it routes every operation through the existing
validator, which is the point of having built it there.

The file manager's root is the site root alone. A file manager that can reach
`/` is a root shell with a friendlier interface.

### 2.2 A root is a boundary, not a thing inside it

`pathsec` accepts a root as being inside itself, which is correct for reading and
for creating things in it — and catastrophic for delete. See §4.1.

### 2.3 Symlinks are shown, never followed

A symlink is reported as a symlink with its target, and whether that target is
somewhere the panel may go. Resolving it into what it points at is how a file
manager gets someone to delete the wrong thing, and following one out of the
root is how it reads `/etc/shadow`.

Recursive chmod skips symlinks rather than following them, and a directory copy
recreates a link as a link rather than duplicating whatever is behind it.

### 2.4 Archives

Extraction is where the most dangerous input in this phase arrives. An archive
entry named `../../../../etc/cron.d/evil` escapes the destination on any
extractor that joins names blindly, and this one extracts as root.

- Every entry name is refused if it is absolute, traverses upward, or contains a
  null byte, then re-resolved through pathsec against both the destination and
  the manager's roots. Two independent reasons an escaping entry is refused.
- **Symlink, device, FIFO and socket entries are refused outright**, not skipped.
  A symlink extracted into a document root is a delayed escape: it lands
  harmlessly and redirects the next write out of the tree.
- The archive's own mode is not applied. An archive can declare `0777` or
  setuid, and honouring it hands the uploader whatever permissions they chose.
- Total size and entry count are bounded, and the copy is bounded independently
  of the declared size. A zip bomb is a few kilobytes that expands to terabytes,
  and filling the disk takes every site on the host offline.

### 2.5 Permissions

Only the nine permission bits can be set. setuid, setgid and the sticky bit are
refused: a setuid binary written into a document root by a web panel is a local
root exploit waiting for someone to run it.

Ownership is deliberately **not** changeable. A panel that can hand any file to
any account is a privilege escalation tool. Instead, everything the panel creates
inherits its parent directory's owner — without that, the panel writes root-owned
files into a customer's document root that PHP cannot write and the customer
cannot fix.

### 2.6 Downloads are always attachments

A download is served as `application/octet-stream`, with
`Content-Disposition: attachment` and `X-Content-Type-Options: nosniff`. Serving
a customer's `.html` back with its own content type would run their markup on the
panel's origin, with the session sitting right there.

The token travels in the `Authorization` header, never in a URL, so a download
does not end up in browser history or the web server's access log.

### 2.7 Search is bounded

Depth, result count and scanned bytes are all capped, symlinks are not followed
(a link to an ancestor is an infinite walk), and the operation's context is
honoured on every entry. An empty query is refused: it matches every file on the
host.

---

## 3. Decisions and deviations

### 3.1 The file content endpoints belong to Phase 7.5

API_SPEC.md lists `GET/PUT /files/content` under **Code Editor**, which is Phase
7.5. They are not implemented here (CLAUDE.md §21). Phase 7's "new file" creates
an empty file; editing its contents is the next phase's job.

The chunked read and write the editor will need already exist in the Agent, so
7.5 is an endpoint and a Monaco integration rather than new plumbing.

### 3.2 A name is always required when creating

`POST /files/folder` and `POST /files/file` take a directory and a name, and the
name cannot be empty. Allowing it to be empty and falling back to the directory
itself was the alternative, and it means an empty form field quietly creates — or
reports having created — the parent instead of the thing the person was naming.

### 3.3 Ownership is inherited, never chosen

See §2.5. The consequence is that the panel cannot repair a file whose owner is
already wrong; that belongs with the website tooling, which knows what a site's
account should be.

### 3.4 Reads are not audited

Every mutation writes an audit event (CLAUDE.md §15). Browsing does not:
recording every directory listing buries the writes that matter.

---

## 4. Bugs found and fixed during this phase

Both were found by the integration suite exercising the live API, not by unit
tests — every unit test was green while both were present.

### 4.1 Deleting the root deleted every website on the host

`DELETE /api/v1/files?path=/var/www&recursive=true` returned **200** and removed
the entire site root.

`pathsec.contains` treats a root as being inside itself, which is right for
listing it and for creating things in it. `Delete` inherited that and called
`os.RemoveAll` on `/var/www`. One request, every site on the host, no
confirmation beyond the one in the browser.

A root is now refused for delete and for move — moving it away is the same loss.
A directory *inside* the root is still removable, which is the file manager doing
its job, and there is a test for that too so the guard cannot quietly turn into
"nothing can be deleted".

### 4.2 Every file audit event failed silently

`audit_logs.resource_id` is a `uuid` column and the file path was passed into it,
so every `file.*` audit write failed on a type cast. The API logged the failure —
the logging added in Phase 6 §4.3 is what made it visible — and the panel carried
on with no audit trail for file operations at all, which CLAUDE.md §15 requires.

A file has no id; it has a path. The path is in the event metadata, where a value
of that shape belongs, and `resource_id` is left empty.

The integration suite now reads the audit table directly and fails if no file
event landed, rather than trusting that the call was made.

---

## 5. Testing

| Layer | Coverage |
|---|---|
| `agent/internal/files` | 34 tests: traversal, symlink escape, permission validation, root protection, chunked read/write, paging, copy/move semantics, zip round trip, zip slip, symlink entries in archives, search bounds and cancellation |
| `agent/internal/operations` | Payload validation and the error mapping that collapses every path failure to "not found" |
| `api/internal/files` | Path and name validation at the edge; an audit event that actually reaches the database |
| `api/internal/server` | Every route refuses an anonymous caller; `file.read` vs `file.write` per route; traversal and name validation |
| `frontend` | Size, mode, breadcrumb and symlink presentation; permission-gated controls |
| `tests/integration/phase7_files.sh` | 99 black-box checks against the live API, each verified on disk |

The integration suite is the one that matters here. Every operation goes through
the API exactly as the panel does, and the result is then checked on disk: an API
reporting 200 is not evidence that a file was written, moved, or removed. It runs
inside the agent container so it can inspect ownership and permissions, and it is
idempotent — verified by running it twice in succession.

Run it with:

```bash
make docker-test-files
```

---

## 6. Known limitations

1. **No file editing.** Reading and writing content is Phase 7.5. See §3.1.
2. **One file per upload.** Multiple selection uploads one at a time.
3. **No resumable uploads.** The transport is chunked, but a dropped connection
   restarts the file rather than continuing it.
4. **No drag and drop, and no move by dragging.** Moving is a dialog.
5. **Ownership cannot be changed.** Deliberate (§2.5), but it means a file whose
   owner is wrong cannot be repaired from here.
6. **Only zip.** No tar, gzip or 7z, in either direction.
7. **Search is name or content, not both**, and content search skips binary
   files and stops at 2 MB per file.
8. **The root is fixed to the site root.** A host storing sites elsewhere needs
   `AGENT_SITE_ROOT` set; there is no per-user root, which is what a multi-tenant
   panel would eventually want.
