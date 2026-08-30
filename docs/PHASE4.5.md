# Phase 4.5 — Apache Hybrid Engine

nginx in front, Apache behind. What it buys, what it costs, and what the live
checks found.

---

## 1. What it does

A host runs one of two arrangements, chosen in the panel:

- **nginx only** — nginx serves every site itself, static files and PHP alike.
  This is what every phase before 4.5 built, and it stays the default.
- **nginx + Apache** — nginx holds ports 80 and 443 and proxies each site to
  Apache on the loopback. Apache serves the files and the PHP, reads
  `.htaccess`, and sees the real visitor address.

Switching rewrites every site's configuration on the host. Sites keep serving
throughout.

---

## 2. Design decisions

### 2.1 The arrangement is a property of the host, not of a site

Both servers are one process tree serving every site on the machine. A panel
that let one site opt in would be running Apache for that site and charging
every other site the memory, while making "why does my `.htaccess` work and
theirs not" a question with a per-site answer.

So it is one setting, and changing it queues one rewrite per website — each
built from the panel's record, so a job that runs or is retried after the
switch writes the configuration the panel now describes.

### 2.2 nginx proxies everything, rather than serving static files itself

The tempting optimisation is to let nginx serve images and CSS directly and
send only the rest to Apache. It is also how `.htaccess` gets quietly bypassed:
a `Require all denied` on a directory of PDFs, or a rewrite that maps
`/assets/…` somewhere else, lives in a file only Apache reads. nginx would
answer first and none of it would apply.

Two servers both serving the same files is two answers to the question "may
this be served". This phase keeps one.

### 2.3 PHP runs through FPM, never in the Apache process

`SetHandler "proxy:unix:…|fcgi://localhost"`, not `mod_php`. mod_php would run
every site's code inside Apache under one shared account — the arrangement
per-site pools exist to avoid — and it rules out the event MPM, because it is
not thread-safe.

The path-info hole has an Apache form as well as an nginx one: without a guard,
a request for `/uploads/avatar.jpg/x.php` reaches FPM, which walks back to the
real file and executes an uploaded image. The handler is therefore attached
only to files that exist, inside the document root's own `<Directory>`.

### 2.4 Apache runs as the web server's group

A site's files are owned by the site's own account and group-owned by the web
server's group, which is how nginx reads them without being able to write them.
Apache's own account is in none of those groups, so the panel's base
configuration sets `Group` to the same one. Without it every request in hybrid
mode is a 403 on a site that looks perfectly configured.

### 2.5 mod_remoteip, with a trusted-proxy list

Every request arrives from 127.0.0.1. Without `mod_remoteip` the access log,
any IP rule in a `.htaccess`, and everything an application reads from
`REMOTE_ADDR` all see the proxy instead of the visitor.

The trusted-proxy list is what makes believing `X-Forwarded-For` safe: it is a
header a client can send, and honouring it from anywhere would let anyone claim
any address they liked — including one a `.htaccess` grants access to.

### 2.6 A backend port per site, from a range of its own

7080–7979, which is where Plesk puts its Apache backend, so an operator who has
seen one hosting panel does not have to learn a second set of numbers. It is
well clear of the ephemeral range (32768 and up), which is what stops a backend
binding fine on most days and failing on the day something else got there
first.

Ports are **kept** when the host goes back to nginx alone: switching twice
should not renumber every backend and rewrite every file for no reason.

The range overlaps the one a Node application may ask for, and two tables
cannot be constrained against each other in SQL without a trigger on both. The
allocator therefore skips ports held by applications, application creation
refuses ports held by backends, and the refusal names what holds it — "port in
use" on a host the user believes is idle is not an answer.

### 2.7 Two edits to httpd.conf, marked and reversible

Everything the panel configures lives in a file of its own in Apache's include
directory, because a distribution owns `httpd.conf` and will replace it on
upgrade. Two things cannot: a second `Listen 80` is still a `Listen 80`, and
only one MPM may be loaded — an included `LoadModule` for a second is a fatal
error, not an override.

So the panel comments out the public `Listen` (nginx owns port 80) and swaps
prefork for event. Each edit is line-scoped, marked with a comment saying who
made it and why, idempotent, and the original file is kept beside it as
`httpd.conf.jothost-original`.

### 2.8 An application-served site skips Apache entirely

Apache is there for `.htaccess` and PHP. A Node.js application uses neither and
answers every path itself, so in hybrid mode a site with a running application
is still proxied straight to it.

### 2.9 Apache is stopped when no site uses it

Its only listening sockets in this arrangement are the ones its site files
declare. With none, Apache refuses to start at all — so an Apache left
"enabled" with no vhosts would fail its next start for a reason unrelated to
the change being made then.

---

## 3. What the live checks found

**1. A busy Agent was reported as a failed website.** This is the significant
one. The Agent runs a bounded number of jobs at a time and refuses the rest
with `AGENT_BUSY`; the panel's worker treated that as a failure and marked the
job failed. A mode switch queues one job per website at once, so on any host
with more than a handful of sites, switching arrangement would have reported
half of them broken — work that had never been attempted, described as failed.

`AGENT_BUSY` now means "not yet": the job goes back to the queue with its
`started_at` cleared, and the worker stops draining until the next tick. It was
reachable before this phase (several suites running at once hit it); it is
*normal* with this phase, which is why it is fixed here.

**2. `<If "-f %{REQUEST_FILENAME}">` with an `<Else> Require all denied`
returned 403 for every PHP request.** The guard is right; the `<Else>` turned a
condition that did not evaluate as expected into a hard denial. A non-existent
`.php` is a 404 on its own, so the denial branch was removed and the guard kept.

**3. Apache's `Group` had to be set explicitly**, or every request in hybrid
mode was `AH01630: client denied by server configuration` on a correct site.

**4. Wildcard names cannot reach a filename.** The same rule as Phase 4.1: the
vhost for `*.example.com` is written as `jothost-_wildcard.example.com.conf`.

Two were faults in the suite itself, both of which would have made a check pass
while proving nothing:

- The PHP endpoint is `PATCH /websites/:id/php`; the suite called `PUT`, got a
  405 it ignored, and then tested a site that had never had PHP enabled.
- The port-conflict check asserted only the status code. The site it used
  served PHP, so the application was refused *for that reason* — a green check
  for a rule that was never exercised. It now asserts the reason, and the Go
  test in `api/internal/node` covers the rule deterministically.

---

## 4. Verification

```bash
make docker-test-hybrid
```

42 checks against the running stack, from inside the agent container so it can
see the configuration files, the processes and the logs.

The shape of it: one site is created and served by nginx, with a `.htaccess`
that nginx ignores. The host is switched to hybrid. **The same site, the same
files, the same content** — and now the `.htaccess` deny rule returns 403 and
its redirect returns 302. Then the host is switched back, and the rule stops
applying again.

Also verified:

- the backend listens on 127.0.0.1 only, and Apache does not hold port 80
- Apache writes its own access log, recording the real client address
- PHP runs as `fpm-fcgi` behind Apache, not as `apache2handler`
- setting a PHP version keeps the site in Apache — the payload carries the
  backend, so an unrelated change does not undo the arrangement
- the vhost is removed and Apache stopped when the last site leaves
- the backend port is kept for next time
- an application cannot take a backend port, and the refusal says why

Every earlier suite still passes, which is what the worker change above had to
be measured against.

---

## 5. Known limitations

- **No per-site choice of arrangement.** Deliberate (§2.1), and worth stating
  because Plesk offers it: there, a site can be marked to bypass Apache. Here
  the closest equivalent is running an application on it.
- **HTTPS terminates at nginx.** Apache speaks plain HTTP on the loopback and
  learns the scheme from `X-Forwarded-Proto`. An application that insists on
  reading `HTTPS=on` from the server rather than the header will not see it.
- **No Apache-side tuning in the panel.** MaxRequestWorkers and the rest are
  the distribution's defaults; the panel does not expose them yet.
- **`.htaccess` is not validated by the panel.** A syntax error in one returns
  500 for that directory, and Apache's error log is where it says so. Checking
  it would mean parsing Apache's configuration language, and the panel would
  still be wrong about modules it does not know are loaded.
- **The MPM swap is not undone** when the host goes back to nginx alone.
  Apache is stopped, so it changes nothing; the original `httpd.conf` is kept
  beside the edited one for an operator who wants it back.
- **One Listen per site.** A hundred sites means a hundred listening sockets,
  which Apache handles but which is more than the single shared backend port a
  name-based arrangement would use. The per-site port is what the phase's task
  list asked for, and it is what makes a site's backend identifiable in a
  netstat.
