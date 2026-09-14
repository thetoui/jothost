# Changelog

Notable changes to JotHost Panel. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Ask a binary what it is with `jothost-api version` or `jothost-agent version`.

## [Unreleased]

### Added

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
