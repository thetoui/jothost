# Phase 16 — Firewall

The strictest requirement in the project, and the reasoning behind each part of
it.

---

## 1. What it does

The panel reads the host's ufw rules, adds and removes them, switches the
firewall on and off, and changes its default policies — and **every one of
those changes is undone automatically unless the panel is still reachable
afterwards**.

---

## 2. Why the ceremony

Every other mistake this panel can make is recoverable by using the panel. A
broken vhost is fixed by writing another one. A stopped service is started
again. A dropped database is restored from a backup.

A firewall mistake takes away the connection the fix would arrive over. There
is no second attempt from the panel, because there is no panel — and on a
rented machine there may be no console either. That asymmetry is why
CLAUDE.md section 19 asks for six steps where every other feature gets one, and
why this phase implements all six rather than the two that would look the same
on a good day.

---

## 3. The six steps, as built

**1. Back up.** ufw's state is three files: the rules, the IPv6 rules, and the
flag saying whether it is switched on. Each change copies them to a directory
of its own under `/var/lib/jothost/firewall/backups`, and nothing is ever
written over (CLAUDE.md section 18). Restoring copies them back and reloads,
which restores the parts the panel does not model as well as the parts it does.

**2. Validate — and refuse a lockout.** Every field is checked before it
becomes a command: the action, the direction, the protocol, the port or range,
the source address or CIDR, and the comment. Then the *guard* asks a different
question: would this change close a port this host is administered through?

The guard is deliberately conservative. It refuses `deny 22` and `reject 22`
and `deny 20:25` and `deny from any` — and also `deny 22 from 203.0.113.7`,
which is probably harmless, because the panel cannot tell whether that address
is the one the operator is sitting at. It refuses removing the last rule that
keeps SSH open, and refuses switching the firewall on when its default policy
would close a guarded port and nothing allows it.

There is no override. An operator who genuinely needs to close SSH does it at
the machine, where being wrong is recoverable.

**3. Apply provisionally.** The change is made — it is a real rule, live
immediately, not a proposal — and a timer is armed to undo it. The default
window is 60 seconds.

**4. Verify.** This is the step it is easiest to lie about, so it is worth
being precise.

The Agent *cannot* test whether the host is reachable from the internet. A
connection it makes to the host's own address is routed over the loopback
interface, meets the INPUT chain as loopback traffic, and is allowed by a rule
ufw installs for exactly that purpose. Such a test passes while the host is
unreachable — which is worse than no test, because it would be believed.

So the check is split. The Agent verifies what it genuinely can: it re-reads
the ruleset ufw actually ended up with and confirms the guarded ports are still
allowed, which catches ufw having done something other than what it was asked.
The other half comes from the caller: **confirming is an ordinary request that
has to cross the network the change governs.** The browser applies the change,
re-reads the firewall, and only then confirms. If the change cut the panel off,
that read never returns, nothing confirms, and step 6 happens.

**5. Commit.** Confirming inside the window stops the timer and keeps the
change.

**6. Roll back automatically.** The window closing restores the backup and
reloads ufw. The timer lives in the Agent, not the API — the failure this has
to survive is precisely "the API can no longer be reached". A marker on disk
carries the deadline, so an Agent that restarts mid-window either re-arms for
the remainder or, if the window has passed, rolls back at once. Without that,
a restart would leave a provisional change standing forever, which is the one
failure the protocol exists to prevent, arriving by the back door.

Only one change may be in flight. Two overlapping windows would each hold a
backup of a state the other had already moved away from, and rolling back would
restore neither.

---

## 4. Other decisions worth stating

**Rules are structured, never a command line.** ufw's syntax is a small
language, and a panel that accepted it as a string would be accepting rules it
cannot read back, cannot render, and cannot check for lockouts. Every field is
validated and the command line is built from them, one argv entry per token.

**Rules are addressed by what they do, not by position.** ufw's `status
numbered` renumbers on every change, so "delete rule 3" means something
different by the time a second request arrives — and what it means is "delete
whichever rule is third *now*". Deletion sends the specification.

**Guarded ports are 22, 80 and 443** by default, plus anything in
`AGENT_FIREWALL_GUARDED_PORTS`. Phase 17 will read the real SSH port from
sshd's own configuration; until then a host with SSH elsewhere says so in
configuration.

---

## 5. What the live checks found

**1. `strings.Contains(line, "active")` matches "inactive".** The first request
made against a real ufw reported a switched-off firewall as enabled, because
every canned fixture in the unit tests said "active" and the substring test had
never met the other case. Everything downstream then reasoned about a ruleset
the host was not enforcing.

**2. A disabled ufw lists no rules at all** — `status` describes what is being
*enforced*, and nothing is. The rules are still there, staged. This was not
cosmetic: the guard that decides whether the firewall may be switched on asks
which rules would apply, saw an empty list, concluded nothing allowed SSH, and
refused. **The firewall could never have been enabled from the panel.** A
disabled firewall is now read from `ufw show added`, whose format is the same
command line the panel builds — so what the panel writes and what it reads back
are provably the same thing (there is a round-trip test).

Both were found within minutes of pointing the code at a real ufw, and neither
was reachable from the unit tests as written.

---

## 6. Verification

```bash
make docker-test-firewall
```

40 checks against a real ufw and a real packet filter. The dangerous half is
testable because the Agent container has its own network namespace and
`NET_ADMIN`: a rule written there filters that container and nothing else, so
connectivity can be broken deliberately and the rollback watched, without
touching the machine running Docker. A real host gives the Agent the same
capability because it runs as root.

The check at the centre of it: a rule is applied with a five-second window and
deliberately not confirmed. The suite watches it appear in `ufw status`, waits,
and watches the host put itself back — with the rules it had before, still
switched on.

Also verified: staged rules are visible while the firewall is off; the guard
refuses each shape of lockout and the refusal names the port; refused rules
never reach ufw; a second change is refused while one is in flight; confirming
keeps a change past its window; rolling back is immediate and leaves the other
rules alone; and the suite leaves the host unfiltered whatever happens, because
every other suite reaches that container over the network.

The Go tests cover the protocol's branches against a scripted ufw — including
the ones a real one is hard to produce on demand — and the parsers against real
captured output.

---

## 7. Known limitations

- **ufw only.** The specs name it (PRD "Infrastructure"). A host running
  firewalld or raw nftables reports no firewall the panel can manage rather
  than guessing.
- **The Agent cannot prove external reachability**, only that the ruleset still
  allows the guarded ports. The browser's confirmation is what closes that gap,
  and it closes it for the panel's own path — not for SSH, which is guarded
  statically instead.
- **No IPv6-specific rules.** ufw writes both families for a rule that names no
  address, and the panel folds the pair into one row. A rule for one family
  only is not expressible here.
- **No routed or interface rules.** `ALLOW FWD` and `on eth0` are shown by ufw
  and skipped by the panel's parser: a rule it cannot express is one it must not
  claim to manage, because the delete it would build would not match.
- **The window is fixed per change, not per session.** An operator making ten
  changes confirms ten times. Batching them into one provisional set is the
  obvious improvement and is not here.
- **Rate limiting is offered but not explained.** `limit` is ufw's
  six-connections-in-thirty-seconds rule; the panel exposes it without letting
  those numbers be changed, because ufw does not either.
