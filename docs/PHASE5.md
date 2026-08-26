# Phase 5 — PHP Manager

**Status:** Complete
**Scope:** TASKS.md Phase 5, PRD.md section 8, API_SPEC.md sections 8 and 9,
DATABASE.md tables 11 and 12

Phase 4 left a deliberate gap in the vhost template where PHP belongs. Phase 5
fills it: a site can now select a PHP version, run it in its own pool under its
own account, and change that version without going down.

---

## 1. What was built

### 1.1 The shape of a PHP change

A site's pool and its vhost have to change together. A pool nothing passes to
is never reached; a `fastcgi_pass` to a pool that does not exist returns 502 to
every visitor. So both happen in **one Agent operation**, in this order:

```text
write the pool → start/reload FPM → wait for the socket
    → repoint the vhost → remove pools for other versions
```

Every step is placed to avoid a window where the site is broken. The vhost is
only repointed once the new socket exists, and the old version's pool is only
removed once nothing is passing to it.

### 1.2 Schema

Migration `0006_php` adds `php_versions` (DATABASE.md §11) and `php_pools`
(§12). `websites.php_version` already existed from Phase 4.

`php_pools.website_id` is UNIQUE, which is the point: two pools for one site
would race for a socket, and whichever FPM started last would win silently.

### 1.3 Detection, not assertion

A version appears in the panel because a binary was found **and answered a
version query** — never because a row said so. The detector probes the
filesystem for known layouts (Alpine's `/usr/sbin/php-fpm83`, Debian's
`/usr/sbin/php-fpm8.3`), then asks each binary what it is.

That probe runs before the Agent's command allowlist is built and executes
nothing, which is what lets the allowlist be built *from* it: every PHP binary
the Agent can ever run is an absolute path resolved at startup, never one
assembled from a request.

The API refreshes its table from the Agent at startup and every five minutes,
because PHP can be installed or removed outside the panel.

### 1.4 Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/php/versions` | Versions on this host, with usage counts |
| `GET /api/v1/php/versions/{version}` | One version |
| `POST /api/v1/php/versions/install` | Queue an install (202) |
| `DELETE /api/v1/php/versions/{version}` | Queue a removal (202) |
| `GET /api/v1/websites/{id}/php` | Whether a site runs PHP, and on what |
| `PATCH /api/v1/websites/{id}/php` | Change the version; `null` turns it off |
| `GET /api/v1/websites/{id}/php/config` | The site's php.ini values |
| `PATCH /api/v1/websites/{id}/php/config` | Change them |

Installing a version changes the whole server, so it needs `server.manage`.
Selecting one for a site needs `website.update`.

### 1.5 UI

`/php` lists versions and installs or removes them. Each website's detail page
gains a PHP panel: a version selector offering only installed versions, and the
php.ini values once PHP is on.

---

## 2. Security

This is the phase where a mistake becomes remote code execution, so the
reasoning is recorded rather than assumed.

### 2.1 The FastCGI execution guard

The obvious `location ~ \.php$ { fastcgi_pass ... }` is a **remote code
execution vulnerability by default**. Upload `avatar.jpg` containing PHP,
request `/uploads/avatar.jpg/x.php`, and nginx passes it to FPM, which walks
back to the real file and executes it.

Two things prevent it, and neither is optional:

- `try_files $uri =404` **before** `fastcgi_pass`, so a request only reaches
  PHP if the `.php` file it names actually exists.
- `SCRIPT_FILENAME` built from `$document_root$fastcgi_script_name`, never from
  `$request_filename` or unvalidated `PATH_INFO`.

`fastcgi_split_path_info` is deliberately absent: nothing here needs
`PATH_INFO`, and enabling it reintroduces exactly the parsing the attack uses.

`agent/internal/nginx/template_test.go` asserts all three properties, including
that `try_files` comes *before* `fastcgi_pass` — a guard placed after the pass
never runs.

### 2.2 Per-site isolation

Each pool runs as the site's own account, with:

- `open_basedir` confined to the site's own directories, so a path traversal in
  application code cannot read another site;
- a per-site session directory at mode `0700`, so one site cannot read or forge
  another's sessions out of `/tmp`;
- `disable_functions` covering `exec`, `shell_exec`, `system`, `proc_open` and
  friends — a panel that leaves these on gives every site shell access to the
  host.

The limits use `php_admin_value`, not `php_value`. Admin values cannot be
overridden by `ini_set()` from inside the application, so a compromised script
cannot raise its own memory ceiling or escape `open_basedir`.

### 2.3 The socket

The pool socket is owned by the site and group-owned by the web server, mode
`0660`. This is the Phase 4 permission lesson in a new place: get it wrong and
the pool starts, nginx cannot connect, and every request returns 502 with
nothing obviously broken.

### 2.4 A static site does not serve PHP source

Turning PHP off rewrites the vhost to **deny** `.php` rather than serve it as
text. A site that had PHP and was switched off still has its `config.php` with
the database password in it; serving it as plain text would hand that over.
Same class as the Phase 4 dotfile rule.

### 2.5 Untrusted input

Values like `memory_limit` are written verbatim into an FPM pool file, which is
line-oriented and section-based. A newline would close a directive and start
another; a bracket could open a whole new pool section defining workers that
run as any user it names. Every value is validated against an anchored pattern
in `shared/validate`, imported by both the API and the Agent so they cannot
drift, and re-validated at render time.

A version string reaches a package name and a configuration path. It is matched
against `^[5-9]\.[0-9]{1,2}$` and then mapped to a package name **by a table** —
never concatenated. No part of a request reaches a shell.

---

## 3. Decisions and deviations

### 3.1 Version-specific socket paths

Sockets are `/run/php-fpm/<pool>-<version>.sock` rather than `<pool>.sock`.

With one shared path, switching a site from 8.3 to 8.4 fails: the new FPM
refuses to start with *"Another FPM instance seems to already listen on ..."*.
The only way around it with a shared path is to stop the old pool first, which
takes the site down for the length of the switch. Including the version makes
the switch atomic — the new pool is up before the vhost moves to it.

### 3.2 `GroupName` is not `SystemUser`

`validate.SystemUser` refuses reserved accounts, so a website can never own
`root` or `nginx`. But the pool's `listen.group` must be `nginx` — that is the
whole point of it.

They are different questions, so there are now two functions. Validating the
listen group as a system user rejects the only correct value, at the moment a
pool is written rather than at startup.

### 3.3 Package installation is the one non-hermetic operation

`php.install` runs the host's package manager. It is allowlisted, given an
explicit argument vector, bounded by a 10-minute timeout, and audited — but it
needs a network and a distro-specific package name, so CI does not exercise it.
The integration test uses versions already present in the image.

### 3.4 Extensions come from the FPM binary

`php.extensions` asks the FPM binary (`php-fpm -m`) rather than a PHP CLI. The
CLI is a separate package a host need not have, and what matters is what the
process actually serving requests has loaded.

---

## 4. Bugs found and fixed during this phase

Three were found by running the suite repeatedly rather than once, and all
three were real defects rather than test artifacts.

### 4.1 A deleted website broke PHP for every other site

`website.delete` removed the site's account but left its FPM pool file behind.
FPM validates its **entire** pool directory at once, so one orphaned pool
naming a deleted user made `php-fpm -t` fail permanently — and from then on no
site could enable that PHP version.

Deleting one website quietly broke PHP for all the others. Website deletion now
removes the site's pools from every installed version first, while the account
still exists.

### 4.2 A zombie FPM master was treated as running

The Agent runs as PID 1 in a container and does not reap children, so an FPM
master that exits becomes a zombie — and a zombie still accepts signal `0`.
`FPMRunning` therefore reported a corpse as alive, the next pool change took
the reload path, SIGUSR2 went to a dead process, and the socket never appeared.

Liveness is now read from `/proc/<pid>/status`: a `Z` state is not running, and
the process name is checked too, which closes the same hole from the other side
when a pid is reused.

### 4.3 `null` and absent were indistinguishable

`PATCH /websites/{id}/php` uses `null` to mean "make this static". With a
`*string`, JSON `null` and a missing key both decode to `nil`, so turning PHP
off was rejected as a missing field. The field is now `json.RawMessage`, which
can tell them apart.

---

## 5. Testing

| Layer | Coverage |
|---|---|
| `shared/validate` | Version format, size and time bounds, injection in every setting, `GroupName` vs `SystemUser` |
| `agent/internal/php` | Layout probing for both distributions, numeric version ordering, pool rendering, injection in paths and settings, refusal without a web group |
| `agent/internal/nginx` | The execution guard, guard ordering, `SCRIPT_FILENAME` derivation, absent path-info splitting, `.php` denial on static sites |
| `api/internal/php` | Detection sync, removed versions, settings merge, uninstall refusal while in use, version switching |
| `api/internal/server` | Route authorisation, RBAC per verb, validation at the edge, `null` vs absent |
| `frontend` | Setting validation, status presentation, permission-gated controls |
| `tests/integration/phase5_php.sh` | 51 black-box checks against live FPM |

The integration suite runs **inside the agent container**, which is the managed
host in development: the execution checks write a probe script into a site's
document root the way a deployment would, which a separate container could not
do. The probe reports the version PHP itself is running, so the acceptance
criterion is checked by what executes rather than by what was configured.

It is also idempotent — verified by running it three times in succession, which
is how §4.1 and §4.2 were found.

Run it with:

```bash
make docker-test-php
```

---

## 6. Known limitations

1. **No extension installation.** Extensions are listed, not installed. Adding
   one is another package-manager operation, and the UI does not expose it.
2. **Installation is untested in CI.** See §3.3.
3. **`max_children` is per-site and unbounded in aggregate.** Each site's pool
   is capped, but nothing caps the sum across sites, so many sites at a high
   setting can still exhaust the host.
4. **No per-version php.ini editing.** Settings are per-site, applied through
   the pool. The version's own `php.ini` is detected and reported but not
   editable.
5. **A failed `website.php.unset` leaves the pool.** The site is already static
   and serving by then, so the job succeeds and the idle pool is logged for an
   operator rather than failing a change that did work.
6. **No OPcache statistics.** OPcache can be turned on and off; its hit rate
   and memory use are not surfaced.
7. **The Agent does not reap children.** §4.2 is handled by detecting zombies
   rather than preventing them. A proper fix is a signal handler reaping
   `SIGCHLD`, which is worth doing when the Agent next grows a supervisor.
