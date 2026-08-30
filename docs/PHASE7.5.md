# Phase 7.5 — Code Editor

**Status:** Complete
**Scope:** TASKS.md Phase 7.5, PRD.md section "Code Editor", API_SPEC.md section 13

Phase 7 gave the panel a file manager that could create an empty file and
download a full one. Phase 7.5 lets someone open a file, change it, and save it
back — which is what a hosting panel is actually used for at 2am.

---

## 1. What was built

### 1.1 Two endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/files/content` | Open a file for editing |
| `PUT /api/v1/files/content` | Save it back |

That is the whole server-side surface. Everything underneath is the Phase 7
plumbing: the same `pathsec` validation, the same chunked transport to the
Agent, the same `file.read` / `file.write` split. A read needs `file.read`; a
save needs `file.write`, so a support account can look at a broken
`wp-config.php` without being able to make it worse.

### 1.2 What a read returns

Not just the text. The response carries the file's mode, owner, language,
line-ending style, and a **checksum** — each of which the editor needs for a
reason:

- **language** drives syntax highlighting, and is derived from the file name
  rather than sniffed from the content. A mode guessed from a first line is
  wrong often enough to be worse than none, and the editor has to choose
  something before the file has finished loading. The table covers the files a
  hosting panel actually meets, including the ones with no extension at all:
  `.htaccess`, `.env`, `nginx.conf`, `Dockerfile`.
- **end_of_line** is reported so a CRLF file does not silently become an LF file
  on its first save, which would show up as every line changed in the customer's
  version control.
- **checksum** is what makes a save safe. See §2.2.

### 1.3 Monaco, bundled

`@monaco-editor/react` loads the editor from a CDN by default. That is
unacceptable here twice over: this panel manages a server as root and must not
execute third-party script delivered at page load, and a control panel is
routinely reached on a private network where the fetch simply fails.

The loader is pointed at a bundled copy and the language workers are wired the
same way, so the editor is served entirely from the panel's own origin and works
offline.

### 1.4 The editor is reached through the file manager

The editor is not a separate destination someone has to find and then navigate
back to the right file in. Clicking a file in the file manager opens an action
menu — **Open in editor**, Download, Rename, Permissions, Delete — and choosing
the editor hands the path across as `?path=`, so the file is already open when
the page arrives.

That replaced the previous behaviour, where clicking a file downloaded it
immediately. Download is one of five things someone might mean by clicking a
file, and the only one that cannot be undone by closing a dialog.

The menu renders in a portal. Inside the table it was clipped by the
`overflow-x-auto` wrapper that lets wide tables scroll, which showed up in a
browser as a menu with only its first item visible — see §5.1.

### 1.5 UI

`/editor` is a file tree beside a Monaco instance with tabs. The tree fetches one
level at a time, because a document root can hold tens of thousands of files and
loading the whole tree to display the top of it costs a request per directory
before anything appears.

Search and replace are Monaco's own find widget (Ctrl+F, Ctrl+H). It already
handles regex, case sensitivity and whole-word; a hand-rolled box beside it
would be a worse version of the same thing. The empty state says so, because a
feature nobody can find is not a feature.

---

## 2. The two decisions that matter

### 2.1 Auto-save is off by default

This editor writes the live configuration of running websites. Saving a
half-typed `wp-config.php` the moment someone stops typing takes the site down
between two keystrokes.

Auto-save exists — TASKS.md asks for it — but it is opt-in per session, and the
delay is three seconds rather than a few hundred milliseconds. It also pauses
entirely while a conflict is unanswered, so a rejected write is never retried on
a timer behind a question the person has not seen yet.

### 2.2 A save cannot silently overwrite someone else's work

Two people editing one file is not exotic on a hosting panel. It is one person
with two tabs open on the same file, or a deploy script writing while someone
reads.

Every read returns a checksum of exactly what was loaded. A save sends it back,
and the API refuses with **409** if the file no longer matches. The panel then
asks, showing both directions and defaulting to neither: overwriting discards
what was written after you opened the file, reloading discards your own edits.

This is a check, not a lock. The file could still change between the check and
the write. What it does is close the window from "however long someone left a tab
open" to "the length of one request" — the difference between losing an
afternoon's work and losing a race that essentially never happens. Recorded here
rather than claimed as atomic, because it is not.

---

## 3. Large file and binary protection

Both are refusals at the API, before anything can be corrupted:

- **Over 2 MiB** is refused with a message that says to download it instead. The
  limit mirrors `EditableFileLimit` in the Agent, so the panel never offers an
  edit button on a file the save would then reject.
- **A NUL byte or invalid UTF-8** is refused as "not text". Opening a binary in a
  text editor and saving it back corrupts it, and the editor shows mojibake in
  the meantime. Saving content containing a NUL is refused for the same reason,
  which is checked separately: a file can be text on disk and have a null pasted
  into it in the browser.

A file too large to edit is still downloadable. Those are different questions.

---

## 4. Decisions and deviations

### 4.1 The editor does not create files

`POST /files/file` from Phase 7 does that. A save with no checksum will create
one, which is what makes "save as" possible later, but the panel's own flow
creates through the file manager and opens through the editor.

### 4.2 No diff view

The conflict dialog says what each choice loses but does not show the difference.
Showing it needs a diff editor and the other version loaded beside the current
one, which is a meaningful amount of UI for a case that should be rare. It is
listed as a limitation rather than pretended away.

### 4.3 Tab state is not persisted

Open tabs live in a Zustand store and are gone on reload. Persisting them would
mean storing file contents in `localStorage`, which CLAUDE.md §10 warns about,
and unsaved edits surviving a browser restart is its own kind of surprise.

---

## 5. Found by using it

Two things were only visible from a browser, with every unit test green.

### 5.1 The action menu was clipped to one item

All five menu items were in the DOM and every test that asserted on them passed,
because jsdom has no layout: it does not clip anything. On screen the menu was
cut off 134px short by the table's horizontal-scroll container, so a real person
saw "Open in editor" and nothing else.

It now renders in a portal, positioned against the row it belongs to, flipping
upward near the bottom of the window and closing on scroll — a menu positioned
against a row cannot follow it.

### 5.2 Monaco went into the main bundle

Imported directly, Monaco landed in the main chunk: 3.7 MB, 982 KB gzipped, on
every page including the login screen. The editor route is now lazy, which puts
Monaco in its own chunk and returns the rest of the panel to 391 KB (115 KB
gzipped) — where it was before this phase.

Neither of these is the kind of thing a passing test suite says anything about.

---

## 6. Testing

| Layer | Coverage |
|---|---|
| `api/internal/files` | Language mapping for extensions, bare names, and exact-name precedence; the editable limit matching the Agent's; path validation on the content routes |
| `api/internal/server` | Anonymous refusal, `file.read` vs `file.write` per verb, path validation, unknown fields |
| `frontend` (store) | Tab open/focus/close semantics, dirty tracking, checksum updates on save, neighbour focus on close, auto-save defaulting off |
| `frontend` (page) | Opening from the tree, dirty marker, saving, read-only without `file.write`, the conflict dialog, the unsaved-close warning, large-file and binary refusals |
| `tests/integration/phase75_editor.sh` | 47 black-box checks against the live API, each verified on disk |
| browser | The whole flow driven by hand: sign in, browse, open the menu, open in editor, edit, save, verify on disk, force a conflict, reload, rename |

The integration suite is where this phase is actually proven. It checks that a
save writes exactly the bytes sent — including quotes, backslashes, tabs and
non-ASCII — that a shorter save truncates rather than leaving the old tail, that
an empty save empties the file, that mode and owner survive a save, that a stale
checksum is refused **and the other person's work is still on disk afterwards**,
and that a file spanning several transport chunks reassembles byte for byte.

Run it with:

```bash
make docker-test-editor
```

---

## 7. Known limitations

1. **No diff view on conflict.** See §4.2.
2. **The stale-write check is a check, not a lock.** See §2.2.
3. **No "save as" in the UI.** The endpoint would allow it; the panel does not
   offer it.
4. **Tabs do not survive a reload.** See §4.3.
5. **No undo history across sessions.** Monaco's undo stack is per open editor.
6. **2 MiB ceiling.** A large log or SQL dump cannot be edited, only downloaded.
7. **No syntax validation before saving.** The panel will happily write a PHP
   file with a syntax error, and the site will 500. Checking would mean running
   a linter on the host, which is a bigger decision than this phase.
8. **No per-file permission check before opening.** The editor opens anything the
   Agent will read; whether that file *should* be edited by hand is left to the
   person.
