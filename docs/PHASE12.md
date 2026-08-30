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
boot** comes from systemd, which is the only thing that knows.

Splitting them is what makes the page useful on a host without systemd: the
states are still true, and the panel says once — as a property of the host —
that it cannot change them. "nginx is running, and I cannot restart it here" is
a useful answer. A blank page is not, and a page of buttons that fail one at a
time is worse.

Where the two disagree, systemd wins: a unit in `activating` is neither up nor
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

**The container has no systemd.** The specs are explicit that production is
systemd (PRD "Host Agent", ARCHITECTURE section 15), so that is what this phase
implements — and the dev container is Alpine with a hand-rolled entrypoint,
which is neither systemd nor OpenRC.

What the integration suite therefore proves, for real, against the running
stack: detection finds nginx, MariaDB, PostgreSQL and one PHP-FPM per installed
version without being told they exist; the pid reported is a process that
exists and is the right program; nothing uninstalled is listed; the panel
reports that this host cannot control services and refuses actions with a
conflict that says why; a key outside the catalogue is refused; and the
dashboard shows the same detection.

What it cannot prove: that `systemctl start nginx` starts nginx.

**What is proved instead, and how.** The Go tests in
`agent/internal/services` run the *real* command runner against a recording
stub, and assert the exact command line produced — that `nginx` becomes
`restart nginx.service`, that each verb reaches systemctl as itself, that
systemd's property output is parsed into the right state. That is a claim about
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

27 checks, plus the Go tests above.

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

- **systemd only.** OpenRC hosts (Alpine, Gentoo) get detection and status but
  no control. Adding a second backend is a contained change to one interface,
  but it is scope the specs do not ask for.
- **The catalogue is the whole list.** A daemon the panel does not manage does
  not appear, however much an operator might want to restart it from here. That
  is the point, and it is also a limit.
- **No logs, no journal.** "Why did it fail to start" is answered by the
  service's own log, which Phase 11 is where the panel learns to show.
- **No dependency awareness.** Stopping MariaDB while sites use it is allowed,
  with a warning in the confirmation rather than a refusal. The panel does not
  know which sites use which engine well enough to refuse safely.
- **Phase 4.5's Apache control is still its own.** Apache is driven by
  `httpd -k` because it must work on a host with no systemd. It appears in this
  listing, and the service manager can restart it where systemd exists, but the
  hybrid engine does not route its own reloads through this package.
