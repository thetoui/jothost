# Phase 12 — Service Manager

What this phase adds, the one decision it rests on, and — stated plainly —
what the development environment can and cannot prove about it.

---

## 1. What it does

The panel lists the daemons on the host, says which are running and which start
at boot, and starts, stops, restarts, enables and disables them.

It is the first phase in the new build order, because everything that manages a
daemon waits on it: FTP, DNS, Fail2Ban, sshd and the mail server all need
something to start what they install.

---

## 2. The decision this rests on: a catalogue, not a name

**A request names a key the Agent knows. The unit it becomes is chosen by the
Agent, from a table in this repository.**

"Manage services" and "run systemctl on anything the caller names" are different
features, and only the first one is wanted. A panel that forwards a unit name is
one careless request away from stopping `systemd-logind`, masking the audit
daemon, or disabling the unit the Agent itself runs as — and no amount of
pattern-matching on the name makes that safe, because every one of those is a
perfectly well-formed unit name.

So `POST /services/nginx/restart` is answered and `POST
/services/nginx.service/restart` is a 404. The second is a real unit; it is not
a key, and keys are the only thing this API accepts.

The catalogue grows with the phases that manage new daemons — FTP in 7.1, BIND
in 13, Fail2Ban in 18, Postfix and Dovecot in 26 — and each addition is a
reviewable change to one file rather than a permission granted at runtime.

PHP-FPM is the exception that proves the shape: it is one service per installed
version, and which versions a host has is only knowable at runtime. Those
entries are contributed by the caller that knows — the PHP detector — rather
than written into a static table that would be wrong on every host.

---

## 3. Two sources for one answer

Whether a service is **installed** is a filesystem probe that executes nothing.
Whether it is **running** comes from the process table. Whether it **starts at
boot** comes from the init system, which is the only thing that knows.

Splitting them is what makes the page useful on a host with no init system at all: the
states are still true, and the panel says once — as a property of the host —
that it cannot change them. "nginx is running, and I cannot restart it here" is
a useful answer. A blank page is not, and a page of buttons that fail one at a
time is worse.

Where the two disagree, the init system wins: a unit in `activating` is neither up nor
down, and the process table cannot say so. A panel that rounds `activating` to
"running" tells an operator a service is ready when it is still starting.

---

## 4. Two rules that keep an operator out of trouble

**SSH is protected.** Stop and disable are refused; restart is not. Stopping
sshd on a remote host locks the operator out of the machine they are
administering, and nothing in the panel can put them back. Restart keeps the
listening socket and is how a configuration change is applied.

The rule lives on the service definition, next to the flag it reads, rather than
in the request handler — a second entry point that forgot to check would be a
second way to lock someone out.

**The verb set is closed.** Start, stop, restart, enable, disable. Deliberately
absent: mask, unmask and edit. Masking a unit makes it unstartable in a way that
looks like a broken package to whoever comes next.

---

## 5. What the development environment proves, and what it does not

This is the honest part, and it is why this section exists at all.

**The container has no systemd, so it runs OpenRC.** The specs name systemd
(PRD "Host Agent", ARCHITECTURE section 15) and systemd is what nearly every
production host runs. Alpine — which this Agent image is built on, and which
plenty of small hosts run — has no systemd at all: it is not a package that
exists there. A panel speaking only systemd could report what runs on such a
host and change none of it, which is what the Services page did until OpenRC was
added: it showed accurate states under a notice saying nothing could be started
or stopped.

So there are two backends behind one set of verbs, chosen by what the host has:

| | systemd | OpenRC |
|---|---|---|
| start | `systemctl start nginx.service` | `rc-service nginx start` |
| boot | `systemctl enable nginx.service` | `rc-update add nginx default` |
| status | `systemctl show nginx.service` | `rc-service nginx status` |

Nothing above `agent/internal/services` chooses between them, and callers cannot
tell them apart. The dev container installs OpenRC and its entrypoint starts
nginx, MariaDB and PostgreSQL *through* it, so one thing owns each daemon: a
service started behind the init system's back is one it reports as stopped and
refuses to stop.

What the integration suite therefore proves, for real, against the running
stack: detection finds nginx, MariaDB, PostgreSQL and one PHP-FPM per installed
version without being told they exist; the pid reported is a process that
exists and is the right program; nothing uninstalled is listed; a key outside
the catalogue is refused; the dashboard shows the same detection — and, on this
host, that stopping nginx from the panel **leaves no nginx master process in
`/proc`**, that starting it puts one back, and that enabling it changes what a
fresh listing reports. A check that only read the panel's own reply would pass
against a panel that reported success and did nothing.

What it cannot prove: that `systemctl start nginx` starts nginx, because there
is no systemd here.

**What is proved instead, and how.** The Go tests in
`agent/internal/services` run the *real* command runner against a recording
stub, and assert the exact command line produced — that `nginx` becomes
`restart nginx.service`, that each verb reaches systemctl as itself, that
systemd's property output is parsed into the right state. The OpenRC backend is
tested the same way, against output copied from a live Alpine host: that the
`.service` suffix never reaches `rc-service`, that the verb goes *after* the
service name (the opposite of systemctl, and getting it wrong produces a command
that runs and does nothing), that every word OpenRC can print for a state maps
to the one the panel shows, and that a stop which prints `ERROR:` and still
exits zero is reported as the failure it is. That is a claim about
the code in this repository, which is the part that can be wrong.

A stub that imitated systemd's *behaviour* was considered and rejected: it would
test my idea of systemd, and this session has already produced four bugs from
exactly that mistake — psql's field separator, the mariadb client's echo,
`REVOKE ... GRANT OPTION` syntax, and libpq's socket matching. A stub that
records its arguments tests the code; a stub that pretends to be systemd tests
the assumption.

```bash
make docker-test-services
```

44 checks, plus the Go tests above.

---

## 6. What else changed

**The dashboard reads detection.** It used to take a configured list of unit
names (`DASHBOARD_SERVICES`), which is a guess maintained per host: it named
`php-fpm` on a host running `php-fpm83`, and the widget then reported "not
installed" for something that was running. The configuration key is gone.

**Alerts fire only for services whose being down breaks websites.** Switching
the dashboard to detection immediately produced a permanent "Critical: Cron is
stopped" on a healthy host — cron is legitimately down on a machine with no
scheduled work, and Apache is *deliberately* stopped whenever the host serves
everything from nginx. An alert that is always on is one nobody reads, so
services carry an `Essential` flag and only those raise one.

---

## 7. Known limitations

- **systemd and OpenRC only.** Other init systems (runit, s6, SysV without
  OpenRC) get detection and status but no control, and say so. A third backend
  is a contained change to the same interface.
- **A service the init system does not know about cannot be controlled.** Cron
  in this container is an example: the binary is installed and its state is
  read from the process table, but there is no init script, so the panel shows
  the state and withholds the buttons rather than offering one that fails.
- **The catalogue is the whole list.** A daemon the panel does not manage does
  not appear, however much an operator might want to restart it from here. That
  is the point, and it is also a limit.
- **No logs, no journal.** "Why did it fail to start" is answered by the
  service's own log, which Phase 11 is where the panel learns to show.
- **No dependency awareness.** Stopping MariaDB while sites use it is allowed,
  with a warning in the confirmation rather than a refusal. The panel does not
  know which sites use which engine well enough to refuse safely.
- **Two daemons are owned elsewhere in the panel, and say so.** Apache is
  driven by the hybrid engine (`httpd -k`, because it must work on a host with
  no init system) and PHP-FPM by the PHP manager, which writes the pools and
  signals the master. Both appear in the listing with their real state and no
  controls, naming their owner: offering to stop them from here would give one
  process two owners, and the panel would show a state matching neither.
  Routing those lifecycles through this package is the eventual fix.


---

## Surviving a reboot

Everything running now should be running after a restart. That was not true
for most of this panel's life: only Node.js applications ever called `Enable`,
so every daemon installed on demand — Apache for the hybrid arrangement, BIND,
the mail server, fail2ban, the FTP server, each PHP-FPM version — was started
and never made persistent. The host served perfectly until it was rebooted,
came back with nothing running, and the panel reported it healthy until
somebody looked.

`BootAudit` reports which running services would not come back;
`EnsureBootPersistence` enables them. The Agent runs the sweep at every start,
so a host that is already wrong is put right by restarting the Agent rather
than waiting for the reboot that would expose it.

Three rules make it safe:

- **Running, not installed.** Apache is *meant* to be stopped on a host serving
  everything from nginx, and PHP-FPM 8.2 on a host running only 8.4. Enabling
  everything installed would start daemons at boot that somebody deliberately
  turned off.
- **Unknown is not "no".** A unit whose init system will not say whether it
  starts at boot — a systemd `static` unit, for instance — is reported and left
  alone. Acting on it would enable units on a guess.
- **A host with no init system is reported once.** Every running daemon there
  is at risk for the same single reason, and repeating it per service buries
  the one fact that matters.

`make docker-test-services` covers it, and the sweep is asserted to be
idempotent — it runs at every Agent start, so one that did work on a correct
host would rewrite the init configuration on every restart.
