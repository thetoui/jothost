# Changelog

Notable changes to JotHost Panel. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Ask a binary what it is with `jothost-api version` or `jothost-agent version`.

## [Unreleased]

Nothing yet.

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
