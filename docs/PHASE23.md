# Phase 23 — Production Installer

One command and a domain. When it finishes, that domain serves the panel over
HTTPS, the administrator exists, every service is enabled at boot, the firewall
is closed to everything but SSH and the web, and the machine can host a website
— with nothing else configured.

```bash
sudo ./install.sh install --domain panel.example.com --email you@example.com
```

---

## 1. What "no configuration beyond the domain" has to mean

It is easy to write an installer that copies files and exits zero. The claim
this phase makes is larger, and each part of it is a place the naive version
fails:

- **The panel is reachable on the name it was given.** Not on localhost, not on
  port 8080 — on the domain, over TLS, with the frontend served and the API
  behind `/api`.
- **Somebody can sign in.** The administrator is created and their password is
  printed once. There is no "now run create-admin" step, because a panel nobody
  can sign in to is not installed.
- **The machine can host a website.** PHP is installed, the Agent knows where
  this distribution keeps its vhosts, the site's account has a group of its
  own, and a site created a minute after the installer finishes serves PHP as
  its own user.
- **It survives a reboot.** The services are enabled, not merely started.

Section 8 is how each of those is checked, and the check is a request over the
network rather than an exit code.

---

## 2. The rules the script keeps

It is run by a human, as root, on a machine they own — a different situation
from CLAUDE.md section 6, which governs commands the *panel* builds from a
request. Nothing here comes from an untrusted caller. What does come from
outside is the handful of options, and each is validated before it reaches a
command line: a domain that is not a domain is refused, not quoted and hoped
for.

Four rules shape the file.

**Every step is idempotent.** The installer reconciles rather than assumes. It
may be run twice, or against a half-finished install, and the second run does
what the first did not. That is also what makes `repair` a real command rather
than a hopeful one: repair is not separate machinery, it is the ordinary steps
run again.

**Secrets are generated once.** Every secret is read from the existing file
when there is one and minted only when there is not. An update that produced a
new `ENCRYPTION_KEY` would leave every stored two-factor secret and every
encrypted credential undecryptable — and nothing would notice until somebody
tried to sign in. The checks compare the key, the Agent token and the database
password across an update and a repair for exactly this reason.

**It refuses rather than guesses.** An unknown distribution, a domain that is
not one, artefacts that are not there: each stops the run and says what it
wanted. And it refuses *before* it changes anything, which the checks confirm
by looking for `/opt/jothost` after each refusal.

**It does not claim success it has not checked.** The last step asks the panel,
over the network, on the name it was given. Until that passes, the install has
not succeeded.

---

## 3. What it installs, and what it deliberately does not

It installs what the **panel itself** needs — nginx, PostgreSQL, Redis, certbot
— and PHP, and stops.

Not MariaDB, BIND, Postfix, Dovecot, Rspamd, or a second PHP version. The panel
installs those itself, on request, through the Agent: that machinery already
exists and is how an operator adds a mail server six months from now. Putting
all of it down here would turn a minutes-long install into an hour and leave
daemons on the machine nobody asked for.

PHP is the exception and is on by default, because a hosting panel whose first
website cannot run an application has not finished the job it was installed to
do. The extension list is what an ordinary application expects to find — gd,
intl, mbstring, curl, zip, the PDO drivers — rather than a minimal interpreter,
because "your first WordPress install fails on a missing extension" is the same
problem moved somewhere the error message is less clear. `--minimal` skips it.

---

## 4. The distribution differences that actually bite

The installer supports Debian/Ubuntu, Alpine and RHEL families. Three
differences between them are not cosmetic, and two of them were found by
running it rather than by reading.

**nginx reads vhosts from different directories.** Alpine's `nginx.conf`
includes `conf.d` at the *top* level and `http.d` inside `http {}`; Debian's
includes `conf.d` inside `http {}`. A vhost written into the wrong one is not
ignored — `server` and `map` are not allowed at the main level, so nginx
refuses to start, and that takes down every site on the host rather than the
one being added. So the installer detects the directory, writes the panel's own
vhost there, **and tells the Agent** through `AGENT_NGINX_SITES_DIR`, so the
panel's customers' vhosts land in the same place. The same is done for the cron
spool directory and the web server's group.

**PostgreSQL clusters live in different places, and each distribution ships the
supported way of making one.** The installer uses Alpine's `postgresql setup`,
RHEL's `postgresql-setup --initdb`, and Debian's own packaging — rather than
running `initdb` into a path it chose. Guessing the layout produces a cluster
the distribution's init script cannot find, which is a machine with two
PostgreSQL installations and one of them invisible.

**Init systems differ, and may not be running.** The panel's service manager
already speaks systemd and OpenRC, so the installer writes whichever the host
reads. The case worth handling rather than refusing is the third one: a
container or chroot, where the init system is installed and something else is
pid 1. There the units are still written and enabled, the services are started
directly so the run produces a working panel, and the difference is said out
loud — "enabled" and "running" are different claims.

---

## 5. TLS, and the fallback that is the point

Certificates come from **certbot**, because that is the ACME client the panel
already uses for website certificates (Phase 6): one client, one account, one
place certificates live, and renewal an operator only has to understand once. A
deploy hook reloads nginx, because certbot renews on its own timer and writes
the new file — and without the hook nginx serves the old certificate out of
memory until something restarts it, which is usually the day it expires.

The vhost is written **twice**: once plain, so the HTTP-01 challenge can be
answered, and again with the certificate that produced. A vhost naming a
certificate that does not exist stops nginx from starting at all.

The fallback is not a footnote. A machine whose DNS does not yet point at it
cannot pass an HTTP-01 challenge, and that is the ordinary state an hour after
a server is created. Refusing to finish would leave a panel nobody can reach.
So the installer issues a self-signed certificate, **says so in those words**,
and tells the operator to run `repair` once DNS is pointed — at which point the
same step obtains a real certificate. HSTS is sent only with a certificate a
browser trusts: from a self-signed host it would pin visitors to an HTTPS they
cannot accept.

---

## 6. Who runs as what

The split the architecture rests on, made concrete on a real host:

```text
jothost-agent    root                  the only privileged part
jothost-api      jothost-api           the part reachable from the network
socket           0660 root:jothost     plus AGENT_ALLOWED_UIDS
```

The API is the process a request reaches, so what it can do on the host is what
an unauthenticated request could reach if the panel were ever wrong — and what
it can do is ask the Agent, over a socket, for the operations the Agent is
willing to perform. The systemd unit hardens it as far as an application that
talks to a socket and a database can be: `NoNewPrivileges`, `ProtectSystem=strict`,
a private `/tmp`, and two named writable paths. The Agent's unit does none of
that, and cannot: it exists to change the machine.

`AGENT_ALLOWED_UIDS` is the second lock. The socket's group says who *can*
connect; this says who may even so, and the installer sets it to the API
account's uid — a thing a person configuring this by hand would almost
certainly skip.

Permissions worth naming, because two of them were wrong first:

- `/etc/jothost` is `0750 root:jothost`, and `api.env` inside it `0640`. It
  holds the database password, the encryption key and the Agent token.
- `/opt/jothost` is `0755 root:root`. It holds binaries and static files and
  nothing secret, and **nginx has to walk into it** — at `0750` every page is a
  403 while the API answers perfectly, which presents as a blank panel.
- `/var/log/jothost` is `2770 root:jothost`. The API writes here as an
  unprivileged account and the Agent writes here as root; at `0750` the API
  cannot create its own log, and the way *that* fails is the worst kind: the
  supervisor reports the service started and the process is gone before it can
  say why.

---

## 7. update, repair, uninstall

**update** replaces the binaries and the frontend and re-applies migrations,
keeping every secret. The frontend directory is swapped rather than copied
into, because copying in place means a visitor mid-update gets a page whose
assets are half old and half new.

**repair** is the reconciling steps run again — on a machine somebody has
changed by hand, or where an earlier run stopped halfway. It is also how a
self-signed certificate becomes a real one once DNS points at the host.

**uninstall** removes the panel and, by default, nothing else. The customers'
websites, their databases and their mail stay where they are: an uninstall of
the *panel* that deleted what the panel was managing would be the single most
destructive thing this script could do, and nobody typing "uninstall" is asking
for it. `--purge` is the deliberate second answer, and even `--purge` does not
touch `/var/www`.

---

## 8. What running it found

The checks run on a bare `alpine:3.21` container — nothing installed, no nginx,
no PostgreSQL, no panel. That emptiness is the point: an installer tested
against the development image would be tested against a host that already had
everything it was supposed to install. Five defects came out of it, and none
would have failed against a mock.

- **A group-readable log directory the API could not write to.** `0750
  root:jothost` let the API *read* `/var/log/jothost` and not create a file in
  it. start-stop-daemon reported the service started and the process was gone
  before it could say why — no log, because the log was what it could not make.

- **A panel directory nginx could not enter.** `/opt/jothost` at `0750
  root:jothost` made every page a 403 while `/healthz` answered perfectly. The
  health check said "could not be reached", which sent the diagnosis towards
  DNS; it now reports the status code, and names this case by its number.

- **The Agent wrote its vhosts into the directory Alpine includes at the main
  level.** `map` is not allowed there, so nginx refused the whole configuration
  — the panel's own vhost included. The installer now tells the Agent which
  directory this host reads.

- **Every site account on Alpine landed in `nogroup`.** BusyBox's `adduser -S`
  with no `-G` puts an account in the shared `nogroup`, so two customers' sites
  would have been in one group — the per-site isolation the design rests on,
  silently absent. It surfaced as PHP-FPM refusing to start at all: "cannot get
  gid for group". Fixed in `agent/internal/sites/users.go`, which now creates a
  private group on the BusyBox path and passes `--user-group` on the
  shadow-utils path rather than inheriting the answer from `login.defs`. This
  is a Phase 4 fix made here under CLAUDE.md section 21: it is the smallest
  dependency of this phase's claim that the machine can host a website.

- **An env file a shell could not read.** `TOTP_ISSUER=JotHost Panel`
  unquoted is fine for systemd's `EnvironmentFile` and an attempt to run a
  command called `Panel` when the same file is sourced by the OpenRC init
  script. Every value is quoted now, which both readers accept.

Two of these — the log directory and the site group — are the kind that only
appear when an unprivileged account really runs the code on a host that really
lacks the thing being assumed.

---

## 9. Verification

```bash
make dist
make docker-test-installer
```

79 checks on a clean host. The shape of it: a machine with nothing on it, the
refusals, one install command, then everything that install claimed.

Proved rather than asserted:

- the panel answers on its own domain over TLS, serves the frontend, redirects
  plain HTTP, and keeps the ACME challenge path on HTTP so a renewal can be
  answered
- a deep link falls through to the single-page application
- the administrator created by the installer signs in with the printed password,
  and that password is nowhere on disk
- the token works against the API, and the panel reaches the Agent to report on
  the host
- **a website created through the panel a minute later is provisioned, served
  on its own name, owned by its own account, and runs PHP as that account** —
  and refuses to execute `.php` before a version is chosen
- an update and a repair leave the encryption key, the Agent token and the
  database password exactly as they were
- `repair` puts back a vhost somebody deleted
- `uninstall` leaves a customer's website alone, and so does `--purge`

---

## 10. Known limitations

- **No `curl | sh` bootstrap yet.** The installer is run from an unpacked
  artefact directory. The one-line remote installer belongs with the published
  release archive, which is Phase 25.
- **No cross-architecture artefacts.** `make dist` builds for the machine it
  runs on. Publishing amd64 and arm64 builds is Phase 25's.
- **RHEL is written for and not yet exercised.** The package names and the
  `postgresql-setup` path are right by inspection; the checks run on Alpine,
  and Debian is covered only by the same reading. Both deserve a run of the
  suite on their own image.
  **Since fixed for Debian** (2029513): the suite runs in CI on a clean Debian
  12 host. Its first run found HTTPS broken there - nginx 1.22 rejects the
  `http2 on;` directive the panel emitted. RHEL and Ubuntu remain unexercised.
- **systemd is written for and not exercised either.** The container the checks
  run in has no systemd, so the units are pinned by reading rather than by
  being started. What *is* exercised end to end is OpenRC, and the "enabled but
  nothing is supervising" path.
  **Since fixed** (2029513): the Debian host runs systemd as PID 1, so the units
  are installed, enabled and started for real.
- **A reboot is not tested.** The services are enabled; that they come back is
  inferred from the enablement rather than observed.
- **The panel is not a website in the panel.** Its vhost is the installer's and
  nothing else writes it — deliberately, since a site the Agent rewrites is one
  whose vhost could be replaced while the panel is serving the page somebody is
  clicking. The cost is that the panel's own certificate is renewed by certbot's
  timer rather than by the panel's renewal sweep.
- **No unattended-upgrade or backup schedule is set up.** Both are the panel's
  own features and are configured from its pages; the installer does not choose
  a policy on the operator's behalf.
