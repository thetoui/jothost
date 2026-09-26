# Changelog

Notable changes to JotHost Panel. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Ask a binary what it is with `jothost-api version` or `jothost-agent version`.

## [Unreleased]

## [0.2.0-rc.1] — 2026-09-26

A release candidate: the first tag to go through the signed-release pipeline,
installed from its published URL by CI on Debian 12 and Ubuntu 22.04 and 24.04.

### Added

- **Signed releases and an installer that checks them.** A `v*` tag builds the
  archive, checks it matches `VERSION`, signs it with Sigstore keyless signing
  bound to this repository's release workflow, publishes it with its SHA-256,
  and installs it from its published URL on every supported OS — with a
  tampered download as a control that must be refused. `get-jothost.sh`
  verifies the checksum and the signature's identity before it runs anything.
  `docs/SUPPORT.md` states the supported platforms and the versioning policy.
- **Webmail from the Mail page.** A Webmail card installs Roundcube into a
  chosen website, says first that the site's document root is replaced, follows
  the install's progress, explains a failure, and removes it again. For the site
  it is on, it says when PHP is off or the site has no certificate — the second
  would send every mailbox password in the clear.
- **Stored backups are re-verified on a schedule** (`BACKUP_VERIFY_INTERVAL`,
  weekly by default). A backup that no longer verifies raises a notification,
  so bit rot is found by the panel rather than by a restore that fails.
- **Log rotation.** The installer configures logrotate for the API, the Agent
  and the Agent's audit log; the Agent reopens its audit log on `SIGHUP`.
- **A Plesk-style layout.** A persistent sidebar, a Tools & Settings page that
  gathers every tool by area, a tool search in the header, and breadcrumbs.
- **The Stage 2 check covers webmail.** `tests/staging/verify.sh --webmail`
  checks from the public internet that webmail is served only over trusted
  HTTPS, that plain HTTP redirects, and that none of its private paths is
  reachable.

- **A capacity harness.** `tests/capacity` (standard library only) drives an
  installed panel through its API and measures the three things a capacity
  baseline needs: how fast a host provisions websites, how the panel behaves
  under concurrent readers (throughput, error rate, p50/p95/p99), and how long
  a full backup takes. Every report states plainly that a number is a supported
  limit only on the reference hardware it was measured on. `make capacity` runs
  it against a throwaway host for same-machine regression checks; `docs/CAPACITY.md`
  explains the method and holds the results table.

- **Rotating the panel's secrets.** `install.sh rotate-key` re-encrypts every
  stored secret — two-factor secrets, database passwords, provider and
  destination credentials — under a new `ENCRYPTION_KEY` in one transaction,
  swaps the key in `api.env` keeping the old file, and undoes the whole rotation
  if the panel does not come back ready. The set of encrypted columns is checked
  against the database's own schema, so a store added without being taught to
  rotation fails the build rather than being silently stranded under the old
  key. `RECOVERY.md` documents rotating the Agent token and the database
  password as well. Proven on an installed host: a stored secret still decrypts
  after a rotation.

- **Recovery codes for two-factor authentication.** Turning two-factor on now
  shows ten one-time codes, and the sign-in page accepts one in place of an
  authenticator code. They can be replaced from Account security with the
  password; only their hashes are stored, and every use is audited. For an
  account with neither its authenticator nor a code, `jothost-api
  reset-two-factor USERNAME` removes two-factor from the host. Before this, an
  administrator who lost their authenticator on a panel with one administrator
  could not get back in: `RECOVERY.md` said another administrator could disable
  it, and no endpoint did.

- **An upgrade test from the previous release.** CI builds `v0.1.0-rc.1` from
  its tag, installs it on Debian 12 and Ubuntu 24.04, gives it a website, a
  cron job, a backup and a stored secret through its own API, and updates it to
  the candidate. The website still serves the same page, the cron job is still
  in the crontab, the backup still verifies, the secret still decrypts, the
  newest migration rolls back and forward on that data, and the Agent's new
  PostgreSQL role is added to the existing install. Ubuntu 22.04 is left out:
  `rc.1` could not sign anybody in there, so no installation of it exists to
  upgrade.

- **Backups of the panel itself, and a way to rebuild it on another host.** A
  `panel` backup dumps the panel's own database, seals it with a key derived
  from `ENCRYPTION_KEY`, and sends it to a destination like any other backup.
  Only administrators can take one. `install.sh export-key --to FILE` writes the
  key to a 0600 file to keep off the host, and `install.sh restore-panel --from
  ARCHIVE --key-file FILE` replaces an installed panel's database with a
  backup: it checks the archive before changing anything, loads it beside the
  running panel, swaps the databases, keeps the one it replaced, and undoes the
  whole restore if the panel does not come back ready. A CI drill backs up one
  host and restores it onto another. See `docs/PANEL_BACKUP.md`.

- **DNS zone templates.** What a new zone starts with is now a named, editable
  list of records rather than two lines hard-coded in Go. `{domain}` and `{ip}`
  are substituted when a zone is created, and a template is validated by
  rendering it against a sample and checking the record that comes out — so one
  that cannot produce a valid record is refused when it is written, not when
  somebody creates a domain and gets a zone the name server will not load. The
  built-in template reproduces the old behaviour exactly, and can be edited but
  not deleted.
- **Issuing a certificate puts the host's own DNS in order first.** Let's
  Encrypt resolves every name on a certificate and fetches a challenge from
  whatever answers, and a failed challenge is spent. Each name that falls in a
  zone this panel serves now gets its address record before the job is queued,
  and the response says what happened per name. Only a missing record is
  written: a name pointing at another machine is reported and left alone.
- **A document root per domain.** An alias can be served from a directory of
  its own instead of the website's. Clearing it puts the name back on the
  website's root and keeps it there as the site moves, which is not the same as
  typing today's path out.
- **A repair control for the name server's configuration.** The panel already
  reported when `named.conf` did not include its zones; the fix existed as the
  reconcile every zone change runs, and the only way to reach it was to save
  unrelated settings.
- **The virus scanner can be installed after the mail server is.** It was
  offered only while installing the mail server itself, so a host that said no
  once could never change its mind. The panel also tells "no scanner on this
  host" from "a scanner nobody switched on" — states that read identically
  before.

### Changed

- **The panel is dark throughout**, on a design system of shared components and
  tokens; the code editor now uses the dark theme too.
- **Each person sees only the pages they can open.** The sidebar, Tools &
  Settings and search are filtered by permission, and someone without the
  dashboard lands on the first page they can use.
- **A PHP version the panel installs comes with its extensions** — PDO for
  MySQL, PostgreSQL and SQLite, XML, mbstring, intl, gd, zip and the rest, the
  set the installer gives the default PHP. It used to be the FPM package alone,
  which WordPress failed on at its first database call. Installing a version
  already present fills in what is missing.
- **Webmail's install is a queued job**, and webmail is recorded as installed
  only when it succeeds. It was recorded the moment the host accepted the
  request, so a failed download left the panel reporting webmail on a site
  serving nothing.
- **The audit trail's retention is stated**: indefinite, by design, with a
  documented purge procedure (`docs/AUDIT.md`).
- monaco-editor 0.52 to 0.56 and routine frontend updates. Dependabot now holds
  TypeScript's major version until `typescript-eslint` supports the next one.
- The S3 backup suite runs against VersityGW, which checks request signatures
  as AWS does, in place of withdrawn MinIO images.
- **The frontend's major dependencies are current.** React 18 to 19, Tailwind
  CSS 3 to 4, Vite 6 to 8, Vitest 3 to 5, ESLint 9 to 10, react-router 6 to 7,
  jsdom, lucide-react and the testing libraries all move up a major, having been
  held back from Dependabot's grouped bump because the group could not install.
  Tailwind 4 keeps its theme in the existing config through an `@config`
  directive rather than a hand-converted `@theme` block, so no utility silently
  stops being generated; the ESLint and react-hooks majors are adopted without
  their new default rulesets, which are opinions to weigh separately, not part
  of this upgrade. TypeScript stays on 5.x: TypeScript 7 is a preview compiler
  and `typescript-eslint` does not yet accept it.
- The installer is now tested on Debian with systemd as well as Alpine with
  OpenRC. It picks its package manager and its service manager from what it
  finds, and only one of those pairs had ever been exercised.
- Every integration suite is discovered rather than listed, by CI and by
  `make docker-test` alike. Six existed without either running them.
- Mail installation is a queued job. It ran inside the request that asked for
  it, and the HTTP server closes a connection after `API_WRITE_TIMEOUT`
  whatever the handler is doing — so installing a virus scanner reported a
  failure and then succeeded.
- phpMyAdmin no longer asks for an address to serve it on. It is reached
  through the panel's own `/phpmyadmin/`, so the name that field took could
  never appear in a request that arrived anywhere.
- The log source picker scrolls inside its own card. Every website contributes
  two sources, so the list grew the page instead.

### Fixed

- **Webmail never worked, and would have published its private files once it
  did.** The installer's PHP on Debian and Alpine had no SQLite driver, so
  Roundcube answered every request with a 500. With that fixed, its error log,
  temp files, database and every script in `vendor/` would have been served,
  because the whole release sat in the web root and nginx ignores Roundcube's
  `.htaccess`. Only what Roundcube publishes is now readable by the web server,
  the database lives in a private directory, and reinstalling keeps it.
- **Long operations were cut off at the connection's write deadline** whatever
  their own timeout, and large database imports through nginx could fail on its
  body and proxy limits. Long requests now extend their own deadlines, and the
  installer's nginx streams imports.
- **Removing a mailbox, setting its password and every other action that
  answers 204 was reported as a failure after it had succeeded.**
- **Certificate renewal skipped the DNS alignment issuance does**, so it could
  fail a challenge issuance would have repaired. A Let's Encrypt rate limit is
  now reported as one, with the time it lifts, and a failed renewal carries
  certbot's explanation rather than "The certificate operation failed".
- The DNS integration suite could fail after every check had passed, on a race
  in its own cleanup.
- **The Agent could not reach PostgreSQL on any installed host.** It connected
  as `postgres` over the Unix socket, peer authentication refused root, and the
  Agent reported PostgreSQL unavailable: customers could not create PostgreSQL
  databases, and panel backups failed. The installer now creates a
  `jothost_agent` role with a generated password for the Agent to connect as
  over the loopback. `update` and `repair` add it to an existing install.

- **HTTPS did not work on Debian, and the installer stopped before it
  finished.** nginx moved the HTTP/2 switch in 1.25.1 — before that it is a
  parameter on `listen`, after it a directive of its own — and the panel only
  ever emitted the newer spelling. Debian 12 ships nginx 1.22, which rejects
  it, so the installer aborted at the vhost step and every HTTPS website would
  have been refused the same way. Both the installer and the Agent now emit
  what the installed nginx accepts.
- An agent call capped at `AGENT_TIMEOUT` even when the caller had set a longer
  deadline of its own, which made every longer timeout in the API dead code.
- A PHP version that is not installed no longer offers to be removed.
- The name server card reported "Answers on: any, any", which is BIND's IPv4
  and IPv6 settings both saying "everything".
- A per-website log route answered 500 rather than 404 for a website that does
  not exist.
- **Every new website answered 403.** The Agent runs with umask 0077, which
  narrows the mode given to every file and directory it creates, so the
  placeholder page was written 0600 and nginx - which reads site content
  through its group - could not open it. Everything else the Agent creates
  for someone other than root to read now states its mode rather than
  requesting it:
  - files and folders created, uploaded or extracted in the file manager, which
    were served as 403 as well;
  - a restored site, whose files came back 0600;
  - the PHP socket directory, which would come back 0700 after a reboot and
    leave every PHP site answering 502;
  - the ACME challenge directory, where nginx could not serve a token, so
    every Let's Encrypt validation would have failed;
  - the cron log directory: every scheduled job fired on time and did
    nothing, because its account could not open its own log.
- **A fresh installation had no built-in DNS template**, so new zones started
  empty. The migration seeded one per existing server, and on a fresh database
  the server is registered after the migrations run. It is now ensured when the
  server registers, which also repairs installations already affected.
- Release builds from a detached checkout - a pull request in CI, or a release
  cut from a tag - were stamped "commit unknown", and `make dist` could not
  set the binaries' mode on a Linux host.

### Security

- **Vulnerabilities can be reported privately.** The documentation pointed
  reporters at a private advisory while that route was switched off on the
  repository. It is on, `docs/SECURITY.md` links straight to it, and it states
  the response: acknowledgement within 3 business days, a fix or mitigation for
  critical and high issues within 30 days, coordinated disclosure.
- Go 1.23 to 1.26, and `golang.org/x/text`, `pgx` and `go-redis` updated,
  clearing the 35 vulnerabilities `govulncheck` reported. CI now runs
  `govulncheck` over all three modules on every push and pull request.

## [0.1.0] — 2026-09-06

First release. Everything below is new, so it is listed by what it does rather
than by what changed.

### Hosting

- **Websites** on nginx, each with its own system account, document root and
  logs. Subdomains, wildcard subdomains, and a hybrid mode that puts Apache
  behind nginx for sites that need `.htaccess`.
- **PHP** per site, one FPM pool per site running as the site's own account, on
  whichever versions the host has. A site with no PHP version does not execute
  `.php` at all.
- **Node.js** applications, managed and reverse-proxied.
- **SSL** through Let's Encrypt, or self-signed where there is no public DNS —
  labelled as untrusted rather than presented as a certificate.
- **Databases** on MariaDB/MySQL and PostgreSQL, with per-database accounts and
  grants. phpMyAdmin is installed on request, never by default, and opens on a
  named database already signed in as an account that can reach it.
- **Mail**, with virus scanning and its own log sources.
- **FTP**, a file manager, and a code editor.
- **Scheduled jobs**, and **Git and webhook deployments** over SSH with the
  panel's own deploy key and host-key pinning.

### Operations

- **Backups** to a local path, S3 or SFTP. A restore is confirmed explicitly,
  and a valid backup is never overwritten before the new one verifies.
- **DNS**, with a local name server and Cloudflare synchronisation — pushed by
  default, imported only when asked.
- **Monitoring**, with an alert engine of its own and optional Grafana panels
  embedded from a read-only database role.
- **Notifications** by email, proved against a real SMTP server rather than a
  stub.
- **Logs**, **services**, and **system updates**.
- **Multi-tenancy**: resellers, customers, subscriptions and service plans,
  with four plans provided out of the box.

### Security

- The API runs unprivileged and holds no code path that executes a privileged
  command. Only the Agent, over a Unix socket guarded by file mode, peer uid
  and a shared token, runs as root.
- No shell anywhere in the privileged path. Commands are allowlisted, executed
  by absolute path, passed argv directly, and bounded by a timeout.
- Opaque server-side sessions; a replayed refresh token revokes every session
  for the account. Argon2id password hashing. TOTP two-factor per account.
- **Firewall** changes back up, validate, apply provisionally, verify
  connectivity and roll back on failure.
- **SSH** refuses to turn off password authentication when no account has a
  key.
- An audit trail for every sensitive action, readable through the API and the
  panel rather than only with a database client.
- The panel's own applications run on a **separate nginx and PHP-FPM master**
  from customer websites, so a website that breaks cannot take the database
  console down with it.

See [docs/SECURITY.md](docs/SECURITY.md) for the full account, including what
is deliberately not implemented.

### Installation

- A single installer that brings its own dependencies, with `install`,
  `update`, `repair`, `uninstall`, `uninstall --purge` and `status`.
- `uninstall` and `--purge` remove the panel and **never** a customer's
  websites or databases.
- `update` and `repair` preserve the encryption key, the Agent token and the
  database password.
- Paired up and down migrations, applied at startup in a transaction.
- A release archive with a SHA256 beside it, whose binaries are checked to
  report the version the archive is named for.

### Known limitations

- No recovery codes for two-factor authentication; an administrator has to
  disable it for a locked-out user.
- No rollback of a system update. Neither apk nor apt keeps enough state to do
  it honestly, so the panel does not pretend to.
- The sidebar does not filter by permission — a user sees links to pages the
  API will refuse them.
- One phpMyAdmin session per browser: two tabs open on two databases will
  contend, because phpMyAdmin holds a single signed-in account per browser.
- No `govulncheck` in continuous integration, and no visual-regression or
  contrast testing.
- Subscription resource limits are recorded but not enforced on a host without
  systemd.

[Unreleased]: https://github.com/thetoui/jothost/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/thetoui/jothost/releases/tag/v0.1.0
