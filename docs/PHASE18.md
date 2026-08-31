# Phase 18 — Fail2Ban

What this phase adds, the two things it got wrong first, and — stated plainly —
what it will not offer.

---

## 1. What it does

The panel shows which jails this host runs, how strict each is, who is banned
right now, and which addresses are never banned. It switches jails on and off,
changes the thresholds, bans and unbans by hand, and installs fail2ban where it
is missing.

It is the third of the phases that decide who can reach the machine, after the
firewall (16) and SSH (17), and it reads the logs the log viewer (11) already
catalogues.

---

## 2. Policy is the panel's; filters are the distribution's

A jail is three things: a filter (a regular expression), a log to match it
against, and an action. The panel manages **none** of those. What it manages is
policy — which jails run, how many failures are allowed, in what window, for how
long, and who is exempt.

That boundary is not squeamishness about regular expressions. It is that a
filter decides *who gets banned*, and the two ways to get it wrong are both bad:
one bans the wrong person, and the other — far more common — matches nothing at
all while the page reports protection.

The distribution is better placed to get it right, and demonstrably is. Alpine's
sshd jail uses a filter called `alpine-sshd` rather than the standard `sshd`,
because BusyBox's syslog writes a facility prefix the standard filter does not
expect. A panel that wrote `filter = sshd` over the top would produce a jail that
matched nothing, banned nobody, and looked enabled.

So the panel writes a jail's `logpath` only where the distribution has not
configured one, and never writes `filter` at all.

---

## 3. The two things this got wrong first

Both were found by running it, and both would have passed any test that only
read the file the panel wrote.

### fail2ban reads its drop-ins last-wins, and `.local` beats `.conf`

Alpine ships `/etc/fail2ban/jail.d/alpine-ssh.conf` setting `maxretry = 10`.

- `10-jothost.conf` is read *first*, so the distribution's value wins.
- `99-jothost.conf` is still a `.conf`, and every `.conf` is read before every
  `.local`. It loses too.
- `99-jothost.local` wins: `.local` after all the `.conf` files, `99-` last among
  the `.local` files.

Until the file was named correctly the panel wrote a correct configuration,
`fail2ban-client -t` accepted it, the daemon reloaded, and the threshold stayed
at 10. Nothing failed.

**So every change is read back from the daemon.** After a reload the panel asks
`fail2ban-client get <jail> maxretry` and compares it with what was asked for. A
disagreement restores the backup and reports which numbers the daemon is actually
running, because a panel that says a host bans after three attempts when it bans
after ten is worse than one that says it could not make the change.

### A rollback that did not roll back

The write path took a backup and threw the path away; the rollback then rebuilt
the path by convention and tried to read a backup that had never been taken —
because on a first write there is nothing to back up. It logged an error and left
the rejected configuration in place.

The write now returns where the previous file was kept, or an empty string when
there was none, and the rollback removes what it wrote in that case. A unit test
holds it: a change the daemon does not adopt must leave *nothing* behind.

A third bug of the same family turned up in the status reader: pointers taken
into a slice that was still being appended to, so every update through them went
into the array the slice used to have. The symptom was the panel reporting stale
numbers after a change that had actually worked.

---

## 4. Why bans are not ufw rules

fail2ban ships a `ufw` action, and using it would put every ban into the panel's
firewall list. It is deliberately not used.

A ban is transient — minutes to hours, and dozens a day on an exposed host — and
the firewall page is the operator's own rules, with a backup taken before every
change and a timed rollback around it (Phase 16). Filling that with churn would
make both features worse: the firewall page would become unreadable, and every
ban would drag a backup with it.

fail2ban's own iptables chains (`f2b-sshd` and the like) sit in front of ufw's
and do the same job without touching it. The integration suite checks the rule is
really there, and really gone after an unban.

---

## 5. What the panel refuses

- **An address that is not one.** It becomes an argument to `fail2ban-client` and
  then a firewall rule. Host names are refused too, though fail2ban would resolve
  them: what gets banned would otherwise depend on what DNS said at that moment.
- **A jail name that is not one.** It becomes a section header and an argument.
- **A ban shorter than the window failures are counted in.** The counter never
  resets, so the address is banned again the moment it is released — a permanent
  ban nobody chose.
- **A threshold nothing would reach**, a window too short to be real, and
  fail2ban's negative "permanent" ban.
- **A jail whose log does not exist here.** fail2ban reports it as a failed jail
  and carries on, so the panel would show something enabled and watching nothing.

And one thing it adds rather than refuses: **loopback is always in the ignore
list**, whether it was asked for or not. A host that has banned its own loopback
has broken every local service that talks to another over it, while the panel
reports success.

---

## 6. Where it meets the other phases

- **Services (12).** fail2ban is in the service catalogue, so it is started,
  stopped and enabled at boot from the Services page like every other daemon.
  `POST /security/fail2ban/enable` from the original sketch is answered with a
  409 pointing there: two places that start the same thing is how a panel comes
  to disagree with itself about whether it is running.
- **Logs (11).** `/var/log/fail2ban.log` is a source in the log catalogue, so
  "what did it ban and why" is searched and downloaded by the viewer that already
  exists. This phase built no viewer.
- **Firewall (16).** Changes need `firewall.manage`, not a permission of their
  own: a ban is a firewall rule, and an account that may not open a port should
  not be able to close one for everybody either.
- **SSH (17).** The sshd jail is what protects what that phase configures.

---

## 7. What the tests prove

**Go tests** (`agent/internal/fail2ban`, 20 tests) run against a recording stub
whose output is copied from a live fail2ban 1.1.0. That matters here more than
usual: `fail2ban-client` prints a pipe-drawn tree meant for a person and it is
the only interface the daemon has, so the parser is the risk — and a stub
printing what I imagined the tree looks like would test my imagination. They
cover the tree parsing, the empty ban list, the "0 means nothing was removed"
trap, the drop-in's name, the read-back, the rollback leaving nothing behind,
and every refusal.

**Integration** (`tests/integration/phase18_fail2ban.sh`, 45 checks) runs inside
the Agent's container against a real fail2ban:

- the daemon is running the threshold the panel set — asked of the daemon
- real sshd failure lines are written to the log the jail watches, and the suite
  waits for the daemon to decide to ban the address they came from
- a firewall rule is actually blocking it, and actually gone after an unban
- unbanning something that is not banned is a 404
- the exemption list reaches the daemon, with loopback added
- and every refusal, each with the reason it exists

```bash
make docker-test-fail2ban
```

**Frontend** (`Fail2BanPage.test.tsx`, 10 tests) covers the jail table, the
unmanaged jail shown without controls, a jail this host cannot run explaining
why, the install offer, the ignore list, and the refusal text reaching the page.

---

## 8. Known limitations

- **Three jails.** sshd, nginx-http-auth and nginx-botsearch. Postfix and Dovecot
  belong to Phase 26, and vsftpd to 7.1 — each is a line in the catalogue when
  the thing it protects exists.
- **No filter editing, and no custom jails.** See section 2. A jail somebody adds
  by hand is listed and left alone, which is the supported way to have one the
  panel does not offer.
- **`findtime` is not on the page.** It is in the API and it is validated; the
  page offers the two numbers an operator actually reasons about. A third field
  whose interaction with the second is subtle is how people configure permanent
  bans by accident.
- **Bans are not recorded by the panel.** What is banned comes from the daemon,
  so history is what is in `/var/log/fail2ban.log`. A ban table of the panel's own
  would be a second source of truth about who is blocked.
- **The panel cannot tell you your own address.** The ignore list is where an
  operator puts it, and the field says so — but behind the panel's own reverse
  proxy the API sees the proxy, so guessing would be worse than asking.
- **`recidive` is not offered.** Banning repeat offenders for weeks needs
  fail2ban's own log as a jail input and a much longer ban than this phase's
  bounds allow; it is a deliberate omission rather than an oversight.
