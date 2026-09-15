# Security

What JotHost Panel does to keep a host safe, what it deliberately does not do,
and what an operator is still responsible for.

This describes the code as it is. Where something is not implemented it says
so rather than describing an intention.

---

## 1. The privilege boundary

The panel is three processes, and only one of them is privileged.

```text
browser  →  jothost-api (unprivileged)  →  jothost-agent (root)  →  the host
```

The API runs as `jothost-api` and can change nothing on the host directly. It
asks the Agent, over a Unix socket, for one of a fixed set of typed operations.
The Agent is the only thing that runs as root.

This is not a convention — the API has no code path that executes a privileged
command. A vulnerability in the API's HTTP surface reaches the Agent's operation
list and stops there.

**The socket is guarded three ways:**

- **Filesystem.** `/run/jothost/agent.sock` is `0660 root:jothost`, so only
  members of that group can open it at all.
- **Peer credentials.** The kernel reports the connecting process's uid via
  `SO_PEERCRED`; the Agent compares it against `AGENT_ALLOWED_UIDS`, which the
  installer sets to the API account's uid. Group membership says who *can*
  connect; this says who *may*.
- **A shared token.** Held in `/etc/jothost/agent.env` (`0600 root:root`) and
  `/etc/jothost/api.env` (`0640 root:jothost`).

Any one of these failing still leaves two.

## 2. Command execution

The Agent never runs a shell. There is no `sh -c`, no `bash -c`, no `eval`
anywhere in the privileged path.

Every command is:

- **Allowlisted by name.** A command not in the allowlist cannot be run, and
  the list is fixed at startup — including the per-version PHP-FPM binaries,
  which are added from what the host actually has.
- **Executed by absolute path**, so `PATH` cannot redirect it.
- **Passed argv directly.** Arguments are never concatenated into a string, so
  a value containing a quote, a semicolon or a newline is an argument and
  cannot become a command.
- **Timeout-bounded**, per command.
- **Audited** where it changes something.

The operation names themselves are typed constants, not free strings.

## 3. Paths

Every path from a request is normalised, resolved against an allowed root, and
checked for symlink escape before it is used. `../` cannot leave the root, and
neither can a symlink planted inside it — the check is on the resolved path,
not the requested one.

The file manager's roots are explicit (`/var/www` by default). A site's account
owns its own directory and nothing else.

## 4. Sessions and tokens

- Access tokens are **opaque and server-side**, held in Redis. They carry no
  claims, so nothing can be forged offline and there is no signature to get
  wrong.
- Refresh tokens rotate on every use. **A replayed refresh token revokes every
  session for that account** — if an old copy is presented, either it leaked or
  the account is being attacked, and both mean the same response.
- Sign-in is rate-limited per account and per address.
- Passwords are hashed with **Argon2id**; the database holds no recoverable
  password for a panel user.
- **Two-factor authentication** is available per account: TOTP, with the shared
  secret encrypted at rest and bound to the user it belongs to. Enrolment is
  confirmed by a code before it is enabled, so nobody can lock themselves out
  by enrolling.

## 5. What is logged, and what is not

Structured logs, and never these:

```text
password        API token        JWT
private key     database password
session token
```

Sensitive actions produce an audit event: who, what, which resource, and the
outcome. Read them through `GET /api/v1/audit` (permission `audit.view`) or in
the panel's Audit page. See [AUDIT.md](AUDIT.md).

## 6. Stored credentials that must be recoverable

Some passwords cannot be hashed, because something has to present them later:

- **Database accounts.** MySQL and PostgreSQL keep only a hash of their own,
  and a password has to go into a site's configuration file. The panel stores
  these encrypted, and reveals them only to `database.manage`.
- **Remote backup destinations.** An S3 key or an SFTP private key has to be
  usable at backup time.

These are encrypted with a key in `/etc/jothost/api.env`. **Back that file up
separately** — without it the encrypted values cannot be read, and an install
that loses it keeps the ciphertext and not the meaning.

The key can be rotated: `install.sh rotate-key` re-encrypts every stored secret
under a new key in one transaction, and undoes itself if the panel does not come
back. The Agent token and the database password can be rotated too. See
[RECOVERY.md](RECOVERY.md) section 7.

The phpMyAdmin console hands the browser a database account's own password so
phpMyAdmin can sign in with it. That discloses nothing new: the same permission
can already reveal the same password directly. It is posted as a form body,
never a URL, and the response is `Cache-Control: no-store`. A shared
administrative account is never used — it would give everyone who can open a
console full access to every database on the host.

## 7. Dangerous operations

- **Firewall changes** back up the current rules, validate the new ones, apply
  them provisionally, verify connectivity, and roll back automatically if
  anything fails. Locking yourself out requires the verification to succeed.
- **Turning off SSH password authentication** is refused when no account on the
  host has an authorised key, because the alternative is a machine that needs a
  console.
- **Restores** require explicit confirmation, and a new backup is verified
  before a valid one is overwritten.
- **Deleting a website** does not delete its database unless asked; a database
  outlives the vhost.

## 8. What the panel serves, and where

The panel's own applications — phpMyAdmin today — run on a **second nginx and a
second PHP-FPM master**, on loopback, reading only `/etc/jothost/web`. Customer
websites are served by the host's nginx in its own process. A website whose
configuration nginx refuses cannot take the panel's database console down with
it, and the website system cannot see or overwrite the panel's configuration.

Grafana, when installed, listens on loopback and gets a **read-only database
role of its own**. It authenticates its own visitors; the panel does not sign
anybody in to it.

## 9. What this does not do

Stated plainly, because a security document that only lists strengths is
misleading:

- **No recovery codes for two-factor authentication.** TOTP enrolment is
  implemented (`/api/v1/auth/2fa`), with the shared secret stored encrypted —
  but an account that loses its authenticator has no self-service way back in.
  An administrator has to disable two-factor for that user.
- **No rollback of a system update.** Neither apk nor apt keeps enough state to
  do it honestly, so the panel does not pretend to. Take a backup first.
- **The sidebar does not filter by permission.** A user sees links to pages
  they cannot use; the API refuses them. Cosmetic, not a bypass.
- **Dependencies are scanned on every change, not on a schedule.** CI runs
  `govulncheck` over all three Go modules on each push and pull request, and a
  finding fails the build. Nothing re-checks a build that is not changing, so a
  dependency that becomes vulnerable afterwards is found by the next change,
  not by a clock.
- **No visual regression or contrast testing.**
- The panel does not protect a host from its own operator. Anyone with
  `server.manage` can change the firewall, SSH, and services — that is the
  point of the panel, and the audit trail is what makes it accountable.

## 10. Operator responsibilities

- Keep `/etc/jothost/api.env` and `/etc/jothost/agent.env` backed up and
  private. They are `0640` and `0600` for a reason.
- Put the panel behind a certificate. The installer will issue one; a
  self-signed certificate is offered and clearly labelled as untrusted.
- Give panel users the least role that works. The roles are real and enforced
  on every request.
- Read the audit trail after anything unexpected.

## 11. Reporting a vulnerability

Report privately, through a security advisory on the project's repository, and
give the maintainers time to release a fix before disclosing. Please include
the version (`jothost-api version`), what you observed, and the smallest
sequence that reproduces it.
