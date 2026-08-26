# JotHost Panel — API Specification

Base URL:

```text
/api/v1
```

Format:

```text
JSON
```

Authentication:

```text
Bearer Access Token
```

---

# 1. Standard Response

Success:

```json
{
  "success": true,
  "data": {},
  "request_id": "req_123"
}
```

Error:

```json
{
  "success": false,
  "error": {
    "code": "RESOURCE_NOT_FOUND",
    "message": "Resource not found"
  },
  "request_id": "req_123"
}
```

---

# 2. Authentication

## Login

```http
POST /auth/login
```

Request:

```json
{
  "username": "admin",
  "password": "password"
}
```

Response:

```json
{
  "success": true,
  "data": {
    "access_token": "...",
    "refresh_token": "...",
    "token_type": "Bearer",
    "expires_in": 900
  }
}
```

When the account has two-factor authentication enabled, no tokens are issued.
The response instead carries a short-lived challenge to be completed at
`POST /auth/2fa/verify`:

```json
{
  "success": true,
  "data": {
    "mfa_required": true,
    "mfa_token": "..."
  }
}
```

Failures return `401 UNAUTHORIZED` with an identical body for a wrong password
and for an unknown username, so the endpoint cannot be used to enumerate
accounts. Repeated failures return `429 RATE_LIMITED`.

### Token semantics

Access tokens are **opaque**, not JWTs: they are random strings resolved
server-side against Redis. This makes logout and revocation take effect
immediately rather than at expiry. See docs/PHASE1.md section 3.1.

Refresh tokens are single-use. Every refresh returns a new refresh token and
invalidates the old one; presenting a superseded token is treated as theft and
revokes the user's entire session set.

---

## Logout

```http
POST /auth/logout
```

---

## Refresh

```http
POST /auth/refresh
```

Request:

```json
{ "refresh_token": "..." }
```

Returns a new token pair. The submitted refresh token is invalidated.

---

## Current User

```http
GET /auth/me
```

Returns the caller's profile, including their effective roles and permissions:

```json
{
  "id": "...",
  "username": "admin",
  "email": null,
  "status": "active",
  "roles": ["admin"],
  "permissions": ["server.view", "..."],
  "two_factor_enabled": false,
  "last_login_at": "2026-08-25T05:30:00Z",
  "created_at": "2026-08-25T05:00:00Z"
}
```

---

## 2FA

```http
POST /auth/2fa/setup     (authenticated)  start enrolment, returns the secret
POST /auth/2fa/enable    (authenticated)  confirm a code and activate
POST /auth/2fa/verify    (anonymous)      complete a login that requires 2FA
POST /auth/2fa/disable   (authenticated)  requires the current password
```

`setup` and `verify` were split into three endpoints during Phase 1 because
they serve two different callers. `enable` is used by a signed-in user
finishing enrolment; `verify` is used by a caller who has passed the password
step but holds no session yet, so it cannot require a bearer token.

### Setup

Returns the shared secret once. It is never retrievable again.

```json
{
  "secret": "JBSWY3DPEHPK3PXP",
  "otpauth_uri": "otpauth://totp/JotHost%20Panel:admin?..."
}
```

### Enable

```json
{ "code": "123456" }
```

### Verify

Completes a login. The `mfa_token` comes from the login response.

```json
{ "mfa_token": "...", "code": "123456" }
```

Responds with the standard token pair.

### Disable

The current password is required: an unlocked browser session must not be
enough to remove a second factor.

```json
{ "password": "..." }
```

---

# 3. Dashboard

```http
GET /dashboard
```

Requires `server.view`. Accepts an optional `server_id`; without one the local
host is used.

Each panel is wrapped in a widget carrying its availability, so one failing
probe does not fail the page:

```json
{
  "server":   { "id": "...", "hostname": "web01", "status": "online" },
  "system":   { "available": true, "data": { "kernel_version": "...", "uptime_seconds": 86400 } },
  "cpu":      { "available": true, "data": { "usage_percent": 12.5, "cores": 4 } },
  "memory":   { "available": true, "data": { "used_percent": 40.0, "total_bytes": 16000000000 } },
  "disk":     { "available": true, "data": { "filesystems": [] } },
  "network":  { "available": true, "data": { "interfaces": [] } },
  "load":     { "available": true, "data": { "load_1": 0.5, "load_per_core": 0.125 } },
  "services": { "available": true, "data": [] },
  "alerts":   [{ "severity": "critical", "category": "disk", "message": "Disk /var is 95% full" }],
  "generated_at": "2026-08-25T10:00:00Z"
}
```

A widget reports one of three states, which callers must not conflate:

```text
available: true        data is present
available: false       could not be collected; "error" says why
unsupported: true      the host cannot provide this at all
```

Website, database, and SSL counts are not yet included: their tables arrive in
Phases 4, 8, and 6, and reporting zero before then would be inaccurate. See
docs/PHASE3.md section 5.

---

# 4. Servers

```http
GET /servers
GET /servers/:id
```

Both require `server.view`.

The mutating endpoints below manage a fleet, and multi-server clustering is an
explicit non-goal in PRD.md section 3. They are not implemented; the single
managed host is registered automatically from what the Agent reports.

```text
POST /servers            not implemented
PATCH /servers/:id       not implemented
DELETE /servers/:id      not implemented
```

---

# 5. Server Metrics

```http
GET /servers/:id/metrics
```

Requires `server.view`.

Query:

```text
range=1h     1 minute buckets
range=24h    15 minute buckets
range=7d     1 hour buckets
range=30d    6 hour buckets
```

`range` defaults to `1h`. Any other value returns `400`, rather than silently
falling back — a typo returning the wrong window looks like a working graph.

Readings are averaged within each bucket, so a response is 60-200 points
regardless of range:

```json
{
  "range": "1h",
  "bucket": "1m0s",
  "from": "2026-08-25T09:00:00Z",
  "to": "2026-08-25T10:00:00Z",
  "points": [
    {
      "timestamp": "2026-08-25T09:00:00Z",
      "cpu_percent": 12.5,
      "memory_percent": 40.0,
      "disk_percent": 45.0,
      "load_1": 0.5,
      "network_rx_per_second": 1024.0,
      "network_tx_per_second": 512.0
    }
  ]
}
```

Any field may be `null`, meaning that metric was not collected for that bucket.
A chart should break its line rather than interpolating across the gap.

Network is reported as a rate derived from the stored cumulative counters, so
the same underlying data is consistent across every range.

---

# 6. Websites

```http
GET /websites
GET /websites/:id
POST /websites
PATCH /websites/:id
DELETE /websites/:id
```

Create:

```json
{
  "domain": "example.com",
  "php_version": "8.4",
  "document_root": "/var/www/example.com/httpdocs",
  "ssl_enabled": true,
  "https_redirect": true
}
```

**As implemented in Phase 4.** The request body is `domain` and an optional
`name`. The other fields are deliberately not accepted yet:

- `document_root` is **computed**, not accepted. A path from a client is a path
  the Agent would resolve as root; it is derived from the validated domain as
  `/var/www/<domain>/public`.
- `php_version` is Phase 5. `ssl_enabled` and `https_redirect` are Phase 6 and
  are **refused with 400** rather than accepted and ignored — a user who
  believes their site is encrypted when it is not is worse off than one told
  the feature is unavailable.

Unknown fields are rejected, so a misspelled key is reported rather than
silently dropped.

Creation is asynchronous. `POST /websites` returns **201** with both the
website (in status `creating`) and the job realising it:

```json
{
  "website": { "id": "…", "status": "creating", "system_user": "web_example_a1b2c3", "…": "…" },
  "job": { "id": "…", "type": "website.create", "status": "PENDING", "progress": 0 }
}
```

`DELETE /websites/:id` returns **202** with the job; the record survives until
the Agent confirms the site is gone from the host.

---

# 7. Domains

```http
GET /websites/:id/domains
POST /websites/:id/domains
PATCH /domains/:id
DELETE /domains/:id
```

**As implemented in Phase 4.** `POST` and `DELETE` are implemented and each
returns the job rewriting the vhost. `PATCH /domains/:id` is not implemented.

Type `alias` and `subdomain` are accepted. Type `redirect` returns **400** for
now: the Agent can write a redirect vhost but cannot remove a stale one, so
accepting it would create configuration the panel could not take back. The
primary domain cannot be detached — it is the site's identity and its vhost's
`server_name`.

---

# 8. PHP

```http
GET /php/versions
POST /php/versions/install
DELETE /php/versions/:version
GET /php/versions/:version
GET /websites/:id/php
PATCH /websites/:id/php
```

Example:

```json
{
  "version": "8.4"
}
```

**As implemented in Phase 5.** All six endpoints exist. Notes:

- A version is `major.minor` only. A patch level (`8.4.19`) is refused: it is a
  property of what happens to be installed, not something a user selects, and
  accepting both spellings would let one version exist as two.
- Install and remove return **202** with the job realising them; the version is
  not on the host until that job succeeds.
- Removing a version websites still run returns **409**. Taking it off the host
  would break every one of those sites.
- `GET /websites/:id/php` returns `{"enabled": false}` for a static site rather
  than 404 — a site without PHP is a valid configuration, not a missing one.
- `PATCH /websites/:id/php` requires the `version` field. An explicit `null`
  turns PHP off and makes the site static again.

Listing versions needs `server.view` and installing needs `server.manage`:
installing changes the whole server, not one site.

---

# 9. PHP Configuration

```http
GET /websites/:id/php/config
PATCH /websites/:id/php/config
```

Example:

```json
{
  "memory_limit": "512M",
  "upload_max_filesize": "100M",
  "max_execution_time": 300,
  "opcache": true
}
```

**As implemented in Phase 5.** Both endpoints exist and use exactly these
field names. `PATCH` returns **202** with a job: a php.ini value that is
written but not reloaded is a setting the panel claims is active and is not.

Values are bounded rather than passed through. They are written verbatim into
an FPM pool file, so each is matched against an anchored pattern — a value
carrying a newline could otherwise close its directive and append another.
`memory_limit` accepts `-1` for PHP's "no limit"; sizes and times are capped
(see `shared/validate`). A rejected value returns **422** and is never stored.

Unset fields keep whatever the pool already has, so changing one setting does
not silently reset the others.

---

# 10. Node.js

```http
GET /node/versions
POST /node/versions/install
DELETE /node/versions/:version
```

Applications:

```http
GET /node/apps
GET /node/apps/:id
POST /node/apps
PATCH /node/apps/:id
DELETE /node/apps/:id
```

---

# 11. Node Process

```http
POST /node/apps/:id/start
POST /node/apps/:id/stop
POST /node/apps/:id/restart
GET /node/apps/:id/logs
```

---

# 12. Files

```http
GET /files
POST /files/upload
POST /files/folder
POST /files/file
PATCH /files
DELETE /files
POST /files/copy
POST /files/move
GET /files/download
POST /files/zip
POST /files/unzip
```

Every path must be validated server-side.

---

# 13. Code Editor

The code editor uses the File API.

```http
GET /files/content
PUT /files/content
```

Request:

```json
{
  "path": "/var/www/example.com/httpdocs/index.php",
  "content": "<?php ..."
}
```

---

# 14. Databases

```http
GET /databases
POST /databases
GET /databases/:id
DELETE /databases/:id
```

---

# 15. Database Users

```http
GET /databases/:id/users
POST /databases/:id/users
DELETE /database-users/:id
PATCH /database-users/:id/password
```

---

# 16. SSL

```http
GET /ssl
GET /websites/:id/ssl
POST /websites/:id/ssl/issue
POST /websites/:id/ssl/renew
POST /websites/:id/ssl/revoke
PATCH /websites/:id/ssl
```

---

# 17. DNS

```http
GET /dns/:domain
POST /dns/:domain/records
PATCH /dns/records/:id
DELETE /dns/records/:id
```

---

# 18. Cron

```http
GET /cron
POST /cron
GET /cron/:id
PATCH /cron/:id
DELETE /cron/:id
POST /cron/:id/run
GET /cron/:id/logs
```

---

# 19. Logs

```http
GET /logs/nginx/access
GET /logs/nginx/error
GET /logs/php
GET /logs/node
GET /logs/system
GET /logs/security
GET /logs/agent
```

Query:

```text
limit
offset
from
to
search
level
```

---

# 20. Services

```http
GET /services
GET /services/:name
POST /services/:name/start
POST /services/:name/stop
POST /services/:name/restart
POST /services/:name/enable
POST /services/:name/disable
```

---

# 21. Processes

```http
GET /processes
POST /processes/:pid/stop
POST /processes/:pid/kill
```

---

# 22. Backups

```http
GET /backups
POST /backups
GET /backups/:id
DELETE /backups/:id
POST /backups/:id/restore
GET /backups/:id/download
```

---

# 23. Backup Schedules

```http
GET /backup-schedules
POST /backup-schedules
PATCH /backup-schedules/:id
DELETE /backup-schedules/:id
```

---

# 24. Firewall

```http
GET /firewall/rules
POST /firewall/rules
PATCH /firewall/rules/:id
DELETE /firewall/rules/:id
POST /firewall/apply
POST /firewall/rollback
```

Firewall API must enforce emergency rollback.

---

# 25. Security

```http
GET /security/score
GET /security/findings
POST /security/scan
PATCH /security/findings/:id
```

---

# 26. SSH

```http
GET /security/ssh
PATCH /security/ssh
GET /security/ssh/keys
POST /security/ssh/keys
DELETE /security/ssh/keys/:id
```

---

# 27. Fail2Ban

```http
GET /security/fail2ban
POST /security/fail2ban/enable
POST /security/fail2ban/disable
GET /security/fail2ban/jails
GET /security/fail2ban/banned
POST /security/fail2ban/unban
```

---

# 28. Jobs

```http
GET /jobs
GET /jobs/:id
POST /jobs/:id/cancel
```

**As implemented in Phase 4.** All three REST endpoints exist. `GET /jobs`
filters on `status`, `resource_type`, `resource_id`, and `limit`. Cancellation
applies only to a job that has not started: once dispatched, the Agent is
changing the host, and reporting it cancelled would be a claim the panel cannot
make good on — that returns **409**.

WebSocket:

```text
/ws/jobs/:id
```

**Not implemented.** Clients poll `GET /jobs/:id`. Provisioning takes seconds
and the UI polls only while work is in flight, so a WebSocket would add a
connection lifecycle, an auth handshake, and a reconnect path for a latency
improvement nobody would notice. See docs/PHASE4.md §3.3.

Events:

```json
{
  "event": "job.progress",
  "job_id": "123",
  "progress": 70,
  "message": "Configuring Nginx"
}
```

---

# 29. Audit Logs

```http
GET /audit-logs
GET /audit-logs/:id
```

No DELETE endpoint.

---

# 30. System Updates

```http
GET /system/updates
POST /system/updates/check
POST /system/updates/install
```

Initially these endpoints may be read-only except for checking updates.

---

# 31. API Security

Every protected endpoint must perform:

```text
Authentication
Authorization
Validation
Rate Limiting
Audit where required
```

Never trust:

```text
user ID
server ID
website ID
file path
domain
port
command
```

from the frontend.

---

# 32. HTTP Status Codes

Use:

```text
200 OK
201 Created
202 Accepted
204 No Content
400 Bad Request
401 Unauthorized
403 Forbidden
404 Not Found
409 Conflict
422 Unprocessable Entity
429 Too Many Requests
500 Internal Server Error
502 Bad Gateway
503 Service Unavailable
```

---

# 33. API Versioning

Current:

```text
/api/v1
```

Breaking changes require:

```text
/api/v2
```

Do not silently break v1 clients.