# Phase 26 — Mail Server Ecosystem

Postfix for transport, Dovecot for mailboxes and authentication, Rspamd for
filtering and signing, Roundcube for webmail. Virtual mailboxes throughout: a
mail password is never a login to the machine.

---

## 1. The failure this phase is built around

Every other phase in this panel can tell you whether it worked. A website serves
a page or it does not. A certificate handshakes or it does not. A backup is read
back and verified before it is called a backup.

Mail is the one thing here where the panel is **not** the authority on whether it
works, and where being wrong produces no error at all.

A mail server with a missing SPF record sends mail perfectly. A domain whose DKIM
key was rotated on the host but never republished in DNS signs every message it
sends, correctly, with a key nobody can fetch. In both cases this host's log says
the receiving server accepted the message, because it did — and then it was filed
in a spam folder the recipient never opens. The report arrives weeks later, from
a customer, as *"some people say they never got my email."*

So the panel makes one distinction everywhere it can:

> **what this host is configured to do, and what the world can actually verify.**

The Agent reports the first. The API reads the second out of the DNS this host
actually serves, and the mail page leads with the difference. A domain with a
signing key and no published record is reported as *not signing as far as anybody
else is concerned* — which is worse than not signing at all, because a signature
that fails to verify looks like a forgery.

Where the panel cannot check — a domain whose DNS is somewhere else — it says
**"cannot check"** rather than "not published". Those are different facts: one is
a fault, the other is a limit of what this host can see.

---

## 2. The failure mode that matters more than any feature

A mail server that relays for strangers is on a blocklist within hours and off it
in weeks, and every customer on the host stops being able to send mail to
anybody.

Every configuration this phase writes is built around not being one:

- `mynetworks` is the loopback and nothing else. Not the local network: "the
  server is on a trusted network" is how almost every open relay came to be one,
  because the network stops being trusted the moment one machine on it is
  compromised.
- `smtpd_relay_restrictions` is `permit_mynetworks, permit_sasl_authenticated,
  defer_unauth_destination`. Postfix stops at the first match, so the order *is*
  the policy, and this parameter exists precisely so the relay decision cannot be
  weakened by something later in the recipient restrictions.
- The submission ports refuse to talk to an unauthenticated client at all.
- Port 25 never offers AUTH. A mail server does not authenticate the other mail
  servers of the world, and offering it there invites a password-guessing
  campaign against every mailbox on the host.

None of that is proved by reading a file. The integration suite **connects from
this host's own routable address and asks the server to relay**, and the address
matters: `mynetworks` contains `127.0.0.0/8`, so a probe from the loopback is
permitted by design and would report a correctly configured server as an open
relay. The panel's own status check makes the same connection, for the same
reason, and reports "could not check" rather than a pass when it cannot.

---

## 3. Virtual mailboxes, not system accounts

Every mailbox lives in Dovecot's own passwd-file and maps to a single
unprivileged account, `vmail`, which owns every Maildir on the host.

This is the posture Phase 7.1 took for FTP, and here it matters more. A mail
password is the one credential a customer types into a phone, a laptop and a
webmail page, so it is the one most likely to be reused and the one most likely
to leak. If it were a system account, that leak would be a shell on the host. It
is not: the account is known only to Dovecot, it has no shell, and the only thing
the credential opens is a mailbox.

One `vmail` account rather than one per mailbox, because there is nothing for a
per-mailbox uid to protect — only Dovecot ever opens these files — and thousands
of system accounts is a real cost: every one is a line in `/etc/passwd` that every
name-service lookup on the host walks past.

---

## 4. What the panel writes, and what it does not

**Postfix's configuration is not rewritten.** Postfix ships `postconf(1)` for
exactly this: `postconf -e` for main.cf, `-M` for master.cf services, `-P` for
per-service overrides. Every change goes through it. The distribution keeps
ownership of its own files, an upgrade merges cleanly, and the panel's whole
footprint is expressible as a list of keys — which is what makes drift
detectable, because `postconf -n` says what the server actually believes.

**Dovecot is the other way round**, because Dovecot has no postconf. It includes
`conf.d/*.conf`, so the panel owns exactly one file there and rewrites it whole.

The name is `99-jothost.conf`, and the number is the opposite of the FTP
drop-in's `10-` on purpose: **proftpd takes the first value for a repeated
directive and Dovecot takes the last**, so on both servers the panel's file is
the one that decides. Both numbers were measured rather than assumed.

**Rspamd** is configured through `local.d`, which it merges into its shipped
configuration rather than replacing — so the distribution keeps its defaults and
the panel states the half-dozen things it cares about.

The generated lookup tables, the passwd-file, the Sieve scripts and the DKIM keys
live under `/var/lib/jothost/mail`. They are *state*, not configuration: every one
is rewritten whole from the panel's own record on each reconcile, and putting them
beside the distribution's files would invite somebody to edit one by hand and have
it silently overwritten.

---

## 5. Where the DKIM private key lives, and why it is not in the database

**The private key is written to the host that signs with it and is stored nowhere
else.** The panel's database holds the public half only.

The control-plane database is backed up, replicated, and read by every part of the
API. A signing key stored there is a key that leaves with any one of those, and it
would be every customer's key at once.

What that costs is honest and small: a host rebuilt from nothing has no keys, so
the panel reports its domains as **not signing** — visibly, on the page — and the
operator generates new ones. That is a minute of work and a DNS change. What the
alternative costs is every customer's domain reputation.

The keys are on a Docker volume in development for the same reason: a rebuild of
the image would otherwise destroy them silently.

Selectors are dated (`jh2026090412`), never fixed. A selector's whole purpose is
to let a domain hold two keys at once during a rotation; a fixed "default" makes
that impossible, because the new key would have to replace the old one at the same
name and every message signed with the old key that is still in flight would fail
to verify. Rotation therefore generates and publishes the new key **before**
deleting the old one, so there is no moment with no key at all.

---

## 6. Decisions worth arguing about

### 6.1 A stopped filter defers mail rather than sending it unsigned

`milter_default_action = tempfail`, not `accept`.

"accept" keeps mail flowing while Rspamd is down. It also sends every outbound
message **unsigned**, and an unsigned message cannot be recalled: it is delivered,
judged against a DKIM policy it does not satisfy, and filed as spam — and the
domain carries that for weeks.

"tempfail" defers instead. The sending server retries for days, so nothing is
lost; inbound mail waits at the sender rather than arriving unfiltered.

The cost is real and is stated plainly: **a stopped Rspamd stops mail.** The panel
reports the daemon as down, and the reconcile *reloads* rather than restarts
Rspamd for the same reason — it compiles ten thousand TLD suffixes at startup,
which is the better part of a minute of deferred mail.

### 6.2 Spam above the threshold is refused, not filed

A rejection at SMTP time is a bounce the **sender** sees. A false positive that
bounces gets a phone call; a false positive filed in a spam folder nobody opens is
silence, and the customer finds out a week later that they lost an order.

### 6.3 A full mailbox is refused, not dropped

Dovecot answers `552 5.2.2 Mailbox is full` at LMTP time, so the sender is told.
Accepting a message and discarding it is the worst thing a mail server can do.

### 6.4 The catch-all is off by default

A catch-all looks helpful and is a spam magnet: every dictionary attack against
the domain lands in it, and because the server can no longer say "no such user" it
has already accepted the message by the time it finds out — so any bounce it then
generates is backscatter sent to a forged sender, which is how a host gets listed.

### 6.5 An unlimited mailbox is not the default

The FTP quota defaults to unlimited and the mailbox quota does not, because of how
the two fail. An unlimited FTP account fills a disk with files somebody uploaded
on purpose. An unlimited mailbox fills it with mail somebody *else* sent — and the
first thing that stops working when the disk is full is every other service on the
host.

### 6.6 Deleting a mailbox does not delete the mail

Deleting a row is a panel action and can be undone by recreating it. Deleting
somebody's correspondence cannot, and a control panel should not do the
irreversible half as a side effect of the reversible one. The Maildir stays; the
panel says so in the confirmation and in the audit record.

### 6.7 Two permissions, not one

`mail.view` and `mail.manage`. Seeing that "sales@example.com exists and is 40%
full" is support work. Being able to set its password is being able to read every
message in it, silently, with no trace the owner will ever see. Those are not the
same act, so they are not the same permission — and the password change is audited
by name.

### 6.8 SPF is `mx a`, and `+all` cannot be asked for

`v=spf1 mx a ~all` authorises whatever the domain's own MX and A records point at,
which is this host by construction and stays correct through a renumbering. An
explicit `ip4:` would be more precise and would become wrong the day the server
moves — silently, because SPF failure is not an error anybody here would see.

There is deliberately no way to publish `+all`. A record that authorises everybody
is worse than no record: it tells every receiver that the forgery they are looking
at is legitimate.

---

## 7. Passwords

The panel implements **SHA-512 crypt** itself, in `shared/crypt`.

Not because the algorithm is preferred — the panel's own users are hashed with
Argon2id, which is better — but because a mailbox password is verified by
**Dovecot**, so the format is Dovecot's choice. Dovecot supports ARGON2ID only when
built against libsodium, which is a build option rather than a guarantee: a panel
writing ARGON2ID hashes would produce mailboxes that authenticate on one
distribution's package and fail on another's, and the failure appears as "password
incorrect" for a password that is correct.

What the panel does about the weaker algorithm is spend more on it: 25 000 rounds,
five times the format's default, written into every hash so it survives a change
of default.

It is computed rather than shelled out to `doveadm pw`, because the process table
on a Linux host is world-readable and a password passed as an argument is visible
to every account on the machine. The plaintext is hashed at the HTTP boundary and
goes no further: **not into the database, not over the socket to the privileged
Agent, not into a log.**

The implementation is checked against the specification's published vectors in
unit tests, and — the check that actually matters — the integration suite asks
**Dovecot itself** to authenticate a real mailbox against a hash this code
produced.

---

## 8. Autoresponders, and the one genuinely dangerous piece of text

A vacation message is written by a customer and ends up inside a Sieve script,
which the mail server executes for every message that arrives. Sieve quotes
strings exactly as C does: a bare double quote ends the string and everything
after it is script. A subject of

```
Away until Monday"; discard; #
```

would, unescaped, be a mailbox that silently deletes its own mail.

Three layers, all deliberate:

1. Validation refuses the control characters no escaping makes safe.
2. The generator escapes the backslash and the quote, in that order.
3. **Every generated script is compiled before it is installed.** A script that
   does not compile is one Dovecot skips at delivery time in silence, so the
   compile is both a syntax check and the proof that what was generated is what
   was intended — and it is the layer that would catch a mistake in the first two.

A script is removed when its autoresponder is: one left behind for a deleted
mailbox is dormant until somebody creates a mailbox at the same address, at which
point their mail starts auto-replying with a message written by somebody else.

---

## 9. Webmail

Roundcube 1.6.9, pinned with its SHA-256 checksum, both compiled into the Agent.

The Agent is fetching a few megabytes of PHP over the network and about to serve
it from a customer's own domain, as a page that will be handed every mailbox
password on this host. A version taken from a request would be a way to ask the
panel to install anything; an unchecked download would be a way for anybody in
between to replace it. **Upgrading is a code change with a new checksum**,
deliberately — it is the one operation here that should require somebody to look.

The download is verified **before** anything is unpacked into the document root:
an archive unpacked and then checked is one that was executable for the length of
the check. Extraction refuses absolute names, dot segments and backslashes — the
same three checks the Phase 14 restore makes, redundant here because the checksum
already passed, and present anyway because "we verified it" is a property of
today's code and the extractor will outlive it.

Webmail is installed into **a website the panel already created**, not into a
directory. A website is a vhost, a PHP pool, a document root owned by an
unprivileged account and a certificate — all of which Phases 4, 5 and 6 already
build correctly, so this phase does not invent a second, weaker path to the same
thing. Its IMAP and SMTP hosts are always this machine; a configurable one would
make the login page a credential collector pointed wherever somebody typed.

Roundcube's own installer is disabled in the generated configuration. It is a page
that can rewrite that configuration and connect to arbitrary hosts, and leaving it
reachable is how a webmail installation becomes somebody else's.

---

## 10. What running it found

Every one of these was found by driving real daemons, and none of them would have
failed against a mock.

- **Rspamd refused every configuration.** Writing `clamav { enabled = false; }`
  to turn virus scanning off produces "cannot add AV rule" — a configuration
  *error*, not a warning. The panel would have refused every mail change on any
  host with Rspamd and virus scanning off, which is the default. The module is now
  disabled at the module level. The same investigation showed that a file which
  failed validation stayed on disk and the *next* reconcile skipped the check
  because the file was unchanged, so Rspamd is now validated on every reconcile.
- **Dovecot would not start.** The Postfix sockets were named relative to
  Dovecot's base directory rather than absolutely, so it tried to bind them
  somewhere that does not exist.
- **Dovecot rejected every correct password.** Its authentication process drops
  from root to its own internal user, so a passwd-file only root can read produces
  "auth failed" for a password that is right — with nothing anywhere saying the
  problem is a permission.
- **Mail was accepted and never delivered.** Dovecot refuses to open a mailbox for
  a uid below `first_valid_uid`, which defaults to 500 — and the mail account is a
  system account, so its uid is below that on every distribution. The panel now
  pins both ends of the range to the mail account, which is stricter than the
  default as well as correct.
- **Rspamd could not read its own signing keys**, because the key directory was
  root-owned. Rspamd does not report that; it simply stops signing.
- **Every SMTP connection logged an error**, because several distributions ship
  `/etc/postfix/aliases` with no compiled copy. The panel now compiles it rather
  than switching local aliases off, because an operator may be relying on the root
  alias in it.
- **The panel could not start Dovecot through its own Services page.** The Agent's
  command runner reported "WaitDelay expired before I/O complete" for an init
  script that had succeeded: the daemon it started inherited stdout, so the output
  pipe never closed. A program that exited is no longer a failure when only a
  grandchild is still holding the pipe. That fix is in
  `agent/internal/command`, with a regression test.

---

## 11. What is in the database and what is not

`mail_settings`, `mail_domains`, `mailboxes`, `mail_aliases`,
`mail_autoresponders` — migration 0021.

The mailbox row holds the **password hash** and not the password. That diverges
from `ftp_users`, which stores nothing at all, and the reason is the reconcile
model: the panel rebuilds Dovecot's passwd-file from this table, and without the
hash it could only *preserve* whatever the host already had — so a reinstalled
host would come back with every mailbox present and no login working, and the
panel would have no way to know. A hash is not a credential. Storing one is what
makes the host reconstructible from the panel's own record.

Deleting a website sets `mail_domains.website_id` to NULL rather than cascading.
It is the one place in this schema where that choice is not obvious, and it is
deliberate: deleting a website must not silently delete everybody's mailboxes and
every message in them.

---

## 12. Known limitations

- **No IMAP or POP3 proxying, no shared folders, no aliases across domains.**
- **Autoresponder dates are evaluated when the panel next reconciles**, not by a
  timer on the host. An autoresponder that ended overnight stops at the next mail
  change rather than at midnight. A timer would be a Phase 10 dependency this
  phase does not need; the boundary is soft and it is stated rather than hidden.
- **Webmail uses SQLite** for its own contacts and preferences. That is officially
  supported and right for a single host; a large installation would want MySQL,
  and this panel does not offer it.
- **Mail is not backed up by Phase 14.** Maildirs are not in the backup subjects,
  so the messages on this host are protected by nothing the panel does. This is
  the most significant gap in the phase.
- **The DKIM public key is compared against the panel's own DNS only.** For a
  domain whose zone is elsewhere the panel says it cannot check, which is honest
  and is not the same as checking. Nothing here resolves a record over the network.
- **No DMARC report ingestion.** The panel publishes `rua` and never reads what
  comes back.
- **Nothing alerts on any of this.** A domain that stops signing, a queue that
  stops moving, a certificate that expires on the mail server: all are visible on
  the page and none of them raises a Phase 20 notification. That is the natural
  next piece of work.
- **ClamAV is not installed by default** — its signature database is several
  hundred megabytes — so virus scanning is off until an operator asks for it.
- **Greylisting, rate limiting and outbound spam controls are Rspamd's defaults**,
  not the panel's, and there is no page for them.
