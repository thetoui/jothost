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
    "expires_in": 900
  }
}
```

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

---

## Current User

```http
GET /auth/me
```

---

## 2FA

```http
POST /auth/2fa/setup
POST /auth/2fa/verify
POST /auth/2fa/disable
```

---

# 3. Dashboard

```http
GET /dashboard
```

Returns:

```text
CPU
RAM
Disk
Network
Services
Website count
Database count
SSL alerts
Security alerts
```

---

# 4. Servers

```http
GET /servers
GET /servers/:id
POST /servers
PATCH /servers/:id
DELETE /servers/:id
```

---

# 5. Server Metrics

```http
GET /servers/:id/metrics
```

Query:

```text
range=1h
range=24h
range=7d
range=30d
```

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

---

# 7. Domains

```http
GET /websites/:id/domains
POST /websites/:id/domains
PATCH /domains/:id
DELETE /domains/:id
```

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

WebSocket:

```text
/ws/jobs/:id
```

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