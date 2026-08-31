# Phase 7.1 — FTP Manager

What this phase adds, the four things it got wrong first, and — stated plainly —
what it will not do.

---

## 1. What it does

The panel gives a website FTP accounts: a name and a password somebody can put
into FileZilla to upload to one site, confined to one directory, optionally
read-only, optionally with a disk limit. It shows who is connected right now and
can cut a session off.

It is the phase that lets somebody who is not an administrator put files on the
server, which is why almost every decision in it is about keeping that as small
a grant as possible.

---

## 2. Virtual users, not system accounts

Every account lives in the FTP server's own password file and **nowhere else on
the host**. It is not in `/etc/passwd`, nothing else on the machine
authenticates against it, and its shell is `/sbin/nologin`.

This is the whole security posture of the phase. A system account can be used to
log in — over SSH, at the console, by anything that authenticates against the
system — and "give the designer upload access to one site" must not hand out a
login to the machine.

What the account *does* carry is a **mapping** to the system account that owns
the website. That is what makes uploads work: a file arriving over FTP is owned
by exactly the account PHP-FPM runs as, so the site can read what was uploaded
to it. Not root, and not some second identity whose files the site cannot open.

The uid is never taken from a request. The API sends the website's account
*name*, and the Agent resolves it against the host's own passwd database — a uid
arriving over the wire would be a way to ask for somebody else's.

---

## 3. Why ProFTPD

Pure-FTPd was tried first and rejected on evidence: Alpine's build reports
`[privsep]` and nothing else, meaning **no TLS at all**. Every password on such a
server crosses the network in clear text, which would have made the FTPS
requirement unimplementable rather than merely unimplemented.

ProFTPD has `mod_tls`, `mod_quotatab`, and the `ftpwho` / `ftpasswd` /
`ftpquota` tools this phase drives, and it includes `conf.d/` last so a panel
drop-in wins over the distribution's defaults.

---

## 4. The four things this got wrong first

All four were found by running it against a real server. Three would have passed
any test that only inspected the file the panel wrote.

### `proftpd -l` does not list the modules that matter

It lists what is *compiled in*. `mod_tls` and `mod_quotatab` are separate
packages loaded at runtime through `mod_dso`, so on a normal host neither appears
there while both work perfectly.

The panel reported `supports_tls: false` on a server whose TLS worked. Worse is
the other direction, which is what makes this dangerous: an `<IfModule>` block
whose module is absent is **skipped in silence**. A panel that assumed mod_tls
would write a valid configuration, restart cleanly, report that FTPS was on, and
serve plain FTP.

Module detection now reads the `LoadModule` directives in `modules.d` as well as
`proftpd -l`, and a reconcile that would need an absent module is refused with a
message naming the package to install.

### ftpwho right-aligns the pid, and the parser dropped every real session

`ftpwho` pads the pid into a five-column field:

```text
standalone FTP daemon [1808], up for 0 min
 1818 probe    [  0m4s] (100%) RETR slow.bin
	client: localhost [127.0.0.1]
	protocol: ftp
```

The session line for pid 1818 **begins with a space**. The parser treated leading
whitespace as "this line is a detail of the session above", so every session
served by a process with a pid under 10000 was silently dropped — on a freshly
booted host, all of them. The session monitor was simply always empty.

The unit test did not catch it because the sample it used had a five-digit pid,
which needs no padding. That test's own opening comment warns against exactly
this — a stub printing what I imagined the output looks like tests my
imagination — and it was still wrong. The sample with the padded pid is now
frozen in a regression test.

### An absolute path was silently turned into a relative one

`FTPHome` trimmed leading slashes, so an operator typing `/etc` got
`<website>/etc` — a different directory, usually one that did not exist, and
never the one they asked for. The same function refused `..` *explicitly* on the
grounds that an operator who typed one should be told rather than quietly given
somewhere else; the leading slash was the same mistake, unnoticed. It is now
refused too.

### A home directory nobody created

An account confined to a folder that does not exist cannot log in — the server
chroots to it and fails, and reports that failure as an authentication problem.
An operator who asked for an `uploads` folder would have been told their password
was wrong. The Agent now creates the directory and gives it to the website's
account, because a directory root owns is one the session cannot write into.

---

## 5. No password, anywhere

There is no password column in the database, and no encrypted one either.

A password is written once into the FTP server's own hashed file (SHA-512, not
ftpasswd's MD5 default) and the panel does not keep it. Storing one — even
encrypted — would mean a copy of every customer's FTP credential in a database
that is backed up, replicated, and read by every part of the panel that touches
that table.

The consequence is deliberate and is surfaced rather than hidden:

- The panel generates the password when the operator does not supply one, and
  shows it **once**. The dialog says so and stays open over it.
- "Show me the password" is answered by setting a new one.
- If the host loses its password file — a rebuilt machine, a restore — the panel
  cannot put those accounts back on its own. It reports each as
  `missing_on_host` and carries on, rather than failing: an error there would let
  one unusable account block every later FTP change on the host, permanently.

Generated passwords leave out the characters people misread — no `O` and `0`, no
`l`, `1` and `I` — and no punctuation, because this is read off a screen and
typed into a client. Twenty-four characters of the remaining alphabet is about
139 bits, so leaving those out costs nothing.

---

## 6. Why the firewall is reported and not changed

The passive port range has to be open or FTP does not work — and it fails in the
most confusing way available: the login succeeds and the first directory listing
hangs until the client gives up.

The panel therefore checks port 21 and the whole passive range against the
firewall on every read, and says exactly what is closed. It does **not** open
them.

A firewall change is its own deliberate act with its own protocol — back up,
validate, apply, verify connectivity, commit, roll back on failure (CLAUDE.md
section 19). Doing one as a side effect of saving an FTP setting would open ports
without the operator seeing which were opened, and without the audit trail
recording that a firewall change had happened at all. This is the same boundary
Phase 17 drew for SSH.

The task list calls this "automatic firewall sync". What is automatic is the
*detection*; the change is one click away on the Firewall page, where it is
audited as a firewall change.

---

## 7. What the panel refuses

- **A name that could not survive the password file.** It is a colon-separated
  format; a colon would end the field early and give the next one a value the
  panel did not write.
- **A home containing `..`, or starting with `/`.** Refused, not normalised. The
  first would leave the website; the second would silently mean a different
  directory.
- **A password under twelve characters.** Length rather than a character-class
  rule: a required symbol produces "Password1!" and nothing else, and this
  password is stored in a client profile rather than typed daily.
- **An account mapped to root.** Every upload would land as root inside a chroot
  root can leave.
- **A passive range under sixteen ports.** Each transfer in progress uses one,
  so a smaller range is a server that refuses transfers under any real load with
  an error nobody can interpret.
- **Encryption required with no certificate, or on a server with no TLS module.**
- **A duplicate account name**, across the whole host: the password file has a
  single namespace, so two websites cannot each have a "backup" account.

---

## 8. Where it meets the other phases

- **Services (12).** proftpd is in the service catalogue, so it is started,
  stopped and enabled at boot from the Services page like every other daemon.
  The FTP page changes configuration and asks for a restart; it does not own the
  lifecycle.
- **Logs (11).** The panel writes `SystemLog` and `TransferLog` explicitly rather
  than inheriting the distribution's defaults, and contributes both as sources to
  the log catalogue — so "who connected" and "what moved" are searched and
  downloaded by the viewer that already exists. This phase built no viewer.
- **SSL (6).** FTPS presents a website's certificate, read from the SSL phase's
  record on every reconcile rather than copied. A copy would go stale at the
  first renewal, silently.
- **Firewall (16).** Reported, not changed. See section 6.
- **Websites (4).** An account belongs to a site, is created from that site's own
  FTP tab, and is removed with it: deleting a website cascades the records away
  and the next reconcile removes them from the host.

---

## 9. What the tests prove

**Go tests** (`agent/internal/ftp`, 26 tests) run against recording stubs whose
output is copied from a live ProFTPD 1.3.8d. That matters more than usual here:
`ftpwho` and `ftpquota` print columns meant for a person and are the only
interface the server offers for "who is connected" and "how much has this account
used". Two of the frozen samples exist because reality surprised me — the padded
pid, and `ftpquota` reporting a raw byte count even when told `--units=Mb`.

They cover the rendering, the read-only deny list, TLS only where there is
something to present, the session parser, the refusal to signal a pid that is not
a session, the quota update passing every limit (`--update-record` resets what it
is not given), the rollback leaving nothing behind, the conflict scan, and the
reconcile's handling of an account it cannot recreate.

**API tests** (`api/internal/ftp`) cover home resolution and the generated
passwords, including that a generated one would not be refused by the rule the
API enforces on supplied ones.

**Integration** (`tests/integration/phase71_ftp.sh`, 45 checks) runs inside the
Agent's container against a real server:

- a real FTP client logs in with the password the panel generated
- it uploads a file, and the file lands **owned by the website's own system
  account**
- it is denied when it tries to leave its directory
- a read-only account can list and download and cannot upload
- an upload that would exceed the disk limit is refused, asked of the server by
  exceeding it rather than read back out of the table the panel wrote
- a live session appears in the panel and can be disconnected
- a process that is not an FTP session is refused, and is still running afterwards
- a deleted account can no longer log in
- and every refusal, each with the reason it exists

```bash
make docker-test-ftp
```

**Frontend** (`FtpPage.test.tsx` and `WebsiteFTPPanel.test.tsx`, 20 tests) cover
the account list, the system account it uploads as, quota usage, encrypted versus
unencrypted sessions, both confirmations, the firewall warning, the drop-in
conflict warning, the disabled encryption control where the server cannot do it,
and the generated password being shown once with the warning that nothing stores
it.

---

## 10. Known limitations

- **One FTP server.** ProFTPD. Pure-FTPd is not supported, for the reason in
  section 3 rather than for lack of time.
- **Two access levels**, not a permission matrix. FTP has no meaningful middle
  ground: a client that can write can rename, overwrite and delete, because those
  are the same verbs. Offering more would be offering distinctions the protocol
  does not make.
- **Usage is what the server counted, not a directory size.** `mod_quotatab`
  tallies what it saw uploaded, so files removed through the file manager or by a
  deploy are not subtracted, and an account whose files were deleted by hand can
  sit at its limit. The panel can clear the tally; it does not do so
  automatically, because doing it on a schedule would mean guessing when a number
  the server maintains is wrong.
- **An operator cannot override the panel from their own drop-in.** ProFTPD reads
  `conf.d` in sorted order and the first value wins for these directives —
  measured, not assumed — so the panel's `10-jothost.conf` is authoritative and
  only a `05-*.conf` could beat it. The panel scans for such a file and warns
  rather than pretending it is in control.
- **No FTPS-only per account.** Encryption is required for the whole server or
  not at all, which is what `TLSRequired` offers.
- **No anonymous FTP**, and none is planned. It is a public write endpoint or a
  public read endpoint, and both belong to the web server.
- **Transfer logs are shown, not summarised.** "How much did this account upload
  last month" would need the transfer log parsed and stored, which is a reporting
  feature rather than an FTP one.
