# Phase 17 — SSH Security

What this phase adds, why its rules are stricter than every phase before it,
and — stated plainly — what it will not do.

---

## 1. What it does

The panel shows how the host accepts SSH — the port, whether root may log in,
whether passwords work, and the settings around them — changes those settings,
manages authorised keys for the accounts that can actually log in, and says what
is worth changing and why.

---

## 2. Why this phase is different

Every other thing this panel changes can be put back from the panel. This one
cannot. A wrong answer here locks the operator out of the machine, and the way
back in is the thing that just broke — on a rented server there may be no
console at all.

So two rules hold throughout, and they are stricter than anything earlier:

**Nothing is written that sshd has not already accepted.** Every change is
validated with `sshd -t` against the whole configuration — includes and all —
before it is installed, and the effective configuration is read back afterwards
to check it took. A change that reports success and did nothing is the worst
outcome available here, because the operator will believe the host is configured
a way it is not and act on that belief.

**A change that would demonstrably lock the operator out is refused.** Not
warned about: refused. A warning is something a person clicks past at the end of
a long day.

Three refusals, each of them a way people actually lose hosts:

| Asked for | Refused when | What the panel says |
|---|---|---|
| Password authentication off | no account has an authorised key | "Add a key first" |
| Public key authentication off | passwords are already off | nothing could authenticate |
| A different port | the firewall does not admit it | "Allow port N in the firewall first" |
| `PermitRootLogin no` | root is the only account that can log in | "Create an administrative account first" |

The port refusal is a cross-phase interlock: the Agent asks Phase 16's firewall
whether a connection to the new port would get through. It will **not** open the
port itself — a firewall change is its own deliberate act with its own protocol
(CLAUDE.md section 19) — so the refusal names the exact thing to do first. A
host with no firewall running admits every port, and refusing there would be
refusing a change that is perfectly safe.

---

## 3. Where the settings are written, and how that is checked

The panel writes **one file it owns**: `/etc/ssh/sshd_config.d/10-jothost.conf`.
It never edits the distribution's `sshd_config`.

That works because of a detail of sshd's parser: **the first setting of a
directive wins**, and modern OpenSSH ships `Include /etc/ssh/sshd_config.d/*.conf`
at the *top* of the main file. A drop-in included there therefore overrides
everything below it. The file is numbered `10-` so it also sorts first among the
drop-ins and wins against those.

The obvious failure of this design is a host whose `sshd_config` has no such
`Include` — the panel would write a correct file that nothing reads, report
success, and change nothing. So the Include is **checked** before any change is
offered, and a host without one is read-only with the reason shown. Belt and
braces, the effective configuration is read back after every change and compared
with what was asked for; a disagreement restores the backup and reports which
directive did not take.

The drop-in holds **only what was asked for**. A directive written at its default
would pin a value the distribution may later change for good reason.

It sets no `Match` blocks, and it is rewritten whole on each change with the
previous panel-set directives carried across — otherwise changing the port would
quietly turn password authentication back on.

---

## 4. Why there is no timed rollback here, unlike the firewall

Phase 16 applies a firewall change provisionally and undoes it unless it is
confirmed, because a bad rule severs the connection immediately and leaves no
way to say so.

SSH is not like that, and copying the pattern would have added a timer that
protects against nothing:

- Existing sessions are separate processes and survive a restart.
- A configuration sshd will not parse is caught by `sshd -t` *before* it is
  installed.

So at every moment either the old configuration is live, or one sshd has already
accepted is. What this phase has instead is a backup of the previous drop-in,
restored on any failure — including the "the server did not adopt it" case above.

---

## 5. Keys

**Which accounts are offered is the Agent's decision.** A request names an
account from the host's own list — read from `/etc/passwd`, filtered to those
with a real login shell and to root — and the path it becomes is built by the
Agent. An API that took a path would be an arbitrary file writer, and the file
it writes is the one that decides who may log in.

Website accounts are absent from that list on purpose: they have a `nologin`
shell, so a key for one would be a key that does nothing, and offering it would
be offering a feature that cannot work.

**A key is parsed before it is written.** `authorized_keys` is line-oriented and
unquoted, like a crontab, so a value containing a newline is not a long key — it
is a second entry authorising somebody else. Beyond that:

- The **type appears twice**, in the line and inside the base64 blob, and they
  must agree. That is the check that catches a truncated paste, which rarely
  gets both right.
- **Options in front of a key are refused.** `command="…"`, `permitopen=…` and
  the rest are legitimate OpenSSH syntax and are exactly how a key becomes a
  forced command or a port forward. The panel writes plain keys; anything else
  is an administrator's deliberate act at a terminal. Lines the panel does not
  understand are **left exactly where they are** — the file is not the panel's
  to tidy.
- `ssh-dss` is refused: OpenSSH removed DSA in 9.8, so writing one produces a
  key that cannot be used.

**A key is identified by its SHA256 fingerprint**, computed from the blob and
matching what `ssh-keygen -l` prints. A line number would change when another
key is removed; a fingerprint is the key. Adding the same key with a different
comment is a duplicate, because the fingerprint is of the material.

The file is written 0600 inside a 0700 `.ssh`, owned by the account. sshd refuses
to read either if anyone else can write, and says so only in its own log — a key
that silently does not work is the failure those modes avoid.

---

## 6. Recommendations, not a score

A number between 0 and 100 invites an operator to chase the number; the findings
are the whole value. Each is a fact about this host, why it matters, and what to
do — and the advice is ordered so that following it in order is safe: a host with
no keys is told to *add a key first*, not to turn passwords off.

Two of them are deliberately honest about their own worth:

- **The default port** is `info`, not a warning. Moving SSH off 22 removes noise
  from the authentication log. It stops nobody who is looking for this host in
  particular, and the finding says so rather than selling obscurity as security.
- **Root login** is high only when it is `yes` (password permitted).
  `prohibit-password` is a reasonable setting and is not reported as a problem.

Phase 15 builds the security centre; this contributes findings to it rather than
a rating.

---

## 7. What the tests prove

**Go tests** (`agent/internal/ssh`, 20 tests) run against real key material
generated with `ssh-keygen`, and check the fingerprints against what
`ssh-keygen -lf` printed for the same keys — so a change that broke the
fingerprint would be caught against OpenSSH's answer rather than this package's.
They cover the RSA modulus size read from the blob, every refusal above, the
`sshd -T` parsing (against output copied from OpenSSH 9.9, including
`without-password` for what the documentation calls `prohibit-password`), the
missing-`Include` detection, hand-written lines surviving a removal, and the
disagreement check. `agent/internal/operations` covers reading ufw's port field —
ranges, lists, and the empty field that means every port.

**Integration** (`tests/integration/phase17_ssh.sh`, 51 checks) runs inside the
Agent's container against a **real OpenSSH server**, and compares the panel's
answers with `sshd -T`:

- the port and root-login setting the panel reports are the ones sshd resolved
- turning passwords off with no key is refused, **and nothing is written on the
  way to refusing**
- with a key present the same change is accepted, and sshd then refuses
  passwords
- a second change keeps the first, and the main `sshd_config` is never edited
- the port moves and sshd listens on the new one, then moves back
- a key's fingerprint matches `ssh-keygen`, its file has the modes sshd
  requires, and a forced-command key is refused
- an account the host does not offer — including `../../root` — is a 404

```bash
make docker-test-ssh
```

**Frontend** (`SSHPage.test.tsx`, 10 tests) covers the settings shown, the two
spellings of the root setting mapped onto one, the refusal text reaching the
page, only the changed setting being sent, the findings ordered worst first, and
the read-only and no-server states.

The dev container runs sshd on a private network with no published port, which
is what makes it safe for the suite to break the configuration on purpose.

---

## 8. Known limitations

- **A host with no `Include` is read-only.** The panel will not edit a
  distribution's `sshd_config` in place, and refuses rather than writing a file
  nothing reads. That covers OpenSSH before 8.2 and a few hand-built
  configurations.
- **Seven directives are managed.** Port, PermitRootLogin,
  PasswordAuthentication, PubkeyAuthentication, PermitEmptyPasswords,
  X11Forwarding, MaxAuthTries. `AllowUsers`, `Match` blocks, ciphers and MACs
  are not — each is a way to lock somebody out that needs its own thinking, and
  a text box for arbitrary directives would be the file editor this design
  exists to avoid.
- **One port.** sshd can listen on several; the panel reports them all and sets
  one.
- **No key generation.** The panel authorises a public key; it never sees or
  creates a private one. A key the server generated is a key the server has.
- **The refusals reason about this host only.** They cannot know that the
  operator's only key is on a laptop that is about to be wiped, or that the
  firewall rule allowing the new port is source-restricted to an address they
  are not at. They catch the predictable mistakes, not every mistake.
- **Restart, not reload.** The service manager has five verbs and restart is
  one; sessions survive it, and adding a sixth verb for one caller was a wider
  change than this phase needed.
