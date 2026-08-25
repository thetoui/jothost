# JotHost Panel — Product Requirements Document

**Version:** 0.1.0  
**Status:** Draft / Development  
**Project Type:** Self-Hosted Hosting Control Panel  
**Primary Goal:** Build a secure, modern hosting/server management panel for personal and internal use, with capabilities comparable to Plesk.

---

## 1. Product Vision

JotHost Panel is a self-hosted Linux hosting control panel designed to manage websites, PHP applications, Node.js applications, databases, SSL certificates, files, scheduled jobs, backups, server services, monitoring, and security from a single web interface.

The system must minimize the need for direct SSH administration.

The product should provide a workflow similar to:

```text
New Linux Server
    ↓
Install JotHost
    ↓
Initialize Server
    ↓
Create Website
    ↓
Select PHP / Node.js
    ↓
Configure Database
    ↓
Issue SSL
    ↓
Configure DNS
    ↓
Deploy Application
    ↓
Monitor
    ↓
Backup
    ↓
Secure
```

---

# 2. Goals

## Primary Goals

1. Manage Linux hosting from a web interface.
2. Manage multiple PHP versions.
3. Manage PHP-FPM pools.
4. Manage Node.js applications.
5. Manage websites and domains.
6. Manage files.
7. Provide an integrated code editor.
8. Manage databases.
9. Manage SSL certificates.
10. Manage cron jobs.
11. Manage Nginx.
12. Manage server services.
13. Manage backups.
14. Provide security monitoring.
15. Provide firewall management.
16. Provide server monitoring.
17. Provide audit logs.
18. Provide a secure privileged Host Agent.
19. Support Docker-based development on Windows.
20. Provide a production installer for Linux.

---

# 3. Non-Goals for Initial Release

The following should not be implemented in the initial MVP:

- Full email hosting server
- Full DNS authoritative server
- Kubernetes management
- Docker orchestration
- Billing
- Subscription management
- Customer marketplace
- Reseller system
- Multi-server clustering
- High availability clustering

These may be added later.

---

# 4. Target Users

## Administrator

Full control over the server.

## Hosting User

Limited access to websites, databases, files, SSL, cron jobs, and logs.

Multi-user support is a later phase.

---

# 5. Technology Stack

## Frontend

- React
- TypeScript
- Vite
- Tailwind CSS
- React Router
- TanStack Query
- Zustand
- Monaco Editor
- Lucide Icons

## Backend

- Go
- REST API
- WebSocket where appropriate
- PostgreSQL
- Redis

## Host Agent

- Go
- Linux systemd service
- Unix socket for local communication
- mTLS for remote communication if required later

## Infrastructure

- Nginx
- PHP-FPM
- Node.js
- MariaDB / MySQL
- PostgreSQL
- Redis
- Certbot / Let's Encrypt
- UFW
- Fail2Ban
- systemd

---

# 6. Core Modules

## Server

- Server information
- CPU
- RAM
- Disk
- Network
- Uptime
- Processes
- Services
- System information

## Websites

- Create website
- Delete website
- Domain
- Subdomain
- Alias
- Redirect
- Document root
- Website status
- Nginx configuration

## PHP Manager

- PHP versions
- PHP-FPM
- PHP-FPM pools
- php.ini
- PHP extensions
- OPcache
- Per-site PHP version

## Node.js Manager

- Node.js versions
- Applications
- Application root
- Startup file
- Port
- Environment variables
- Process management
- Logs
- Auto restart

## File Manager

- Browse
- Upload
- Download
- Copy
- Move
- Rename
- Delete
- Create folder
- Create file
- ZIP
- UnZIP
- Permissions

## Code Editor

- Monaco Editor
- Syntax highlighting
- Search
- Replace
- Tabs
- Auto-save
- File tree
- PHP
- JS
- TS
- HTML
- CSS
- JSON
- YAML
- Markdown
- SQL

## Database

- MySQL
- MariaDB
- PostgreSQL
- Database creation
- Database deletion
- Users
- Permissions
- Password management
- Database size
- Backup

## SSL

- Let's Encrypt
- Issue certificate
- Renew
- Revoke
- Auto renewal
- Force HTTPS
- Certificate status

## DNS

Initial provider:

- Cloudflare API

Supported records:

- A
- AAAA
- CNAME
- MX
- TXT
- NS
- CAA
- SRV

## Cron

- Create
- Edit
- Delete
- Enable
- Disable
- Run now
- Logs

## Logs

- Nginx access
- Nginx error
- PHP
- Node.js
- Cron
- System
- Security
- Agent

## Backup

- Website backup
- Database backup
- Full server backup
- Schedule
- Retention
- Restore
- Local destination
- S3-compatible storage
- SFTP

## Security

- Security score
- Firewall
- SSH configuration
- Fail2Ban
- Open ports
- File permissions
- SSL security
- Security scanner
- System updates

---

# 7. Dashboard

Dashboard must display:

```text
CPU
RAM
Disk
Load Average
Network
Uptime
```

Service status:

```text
Nginx
PHP-FPM
Database
Redis
Agent
```

Statistics:

```text
Websites
Domains
Databases
PHP Apps
Node Apps
SSL Certificates
Cron Jobs
```

Alerts:

```text
SSL expiring
Disk usage high
Memory usage high
Service down
Backup failed
Security issue
```

---

# 8. Website Creation Workflow

The administrator should be able to create a website from one form.

Input:

```text
Domain
PHP Version
Document Root
SSL
Database
```

The Agent should perform:

```text
Create system/site user
Create directory
Set permissions
Create Nginx configuration
Create PHP-FPM pool
Create database
Configure SSL
Configure HTTPS
Reload services
Verify configuration
```

All operations must be logged.

---

# 9. Security Requirements

Security is a first-class feature.

The system must:

- Never execute arbitrary shell commands from user input.
- Validate every path.
- Validate every domain.
- Validate every port.
- Use allowlists for privileged operations.
- Separate API from privileged Agent.
- Store secrets securely.
- Use HTTPS in production.
- Support 2FA.
- Implement RBAC.
- Implement rate limiting.
- Implement audit logging.
- Implement request IDs.
- Provide emergency rollback for firewall changes.

---

# 10. Privileged Agent

The Host Agent is the only component allowed to perform privileged server operations.

The API must never directly execute:

```text
systemctl
rm
chmod
chown
nginx
certbot
iptables
ufw
apt
```

The API sends a typed operation to the Agent.

Example:

```json
{
  "operation": "website.create",
  "domain": "example.com",
  "php_version": "8.4"
}
```

The Agent validates the operation and performs the required actions.

---

# 11. Job System

Long-running operations must be asynchronous.

Examples:

- Website creation
- SSL issuance
- Backup
- Restore
- PHP installation
- Node.js installation

Job states:

```text
PENDING
RUNNING
SUCCESS
FAILED
CANCELLED
```

---

# 12. Monitoring

Monitor:

- CPU
- RAM
- Disk
- Load
- Network
- Services
- Processes

Retention:

- Raw metrics: configurable
- Aggregated metrics: longer retention

---

# 13. Audit Log

Every sensitive action must create an audit record.

Example:

```text
User:
admin

Action:
website.delete

Resource:
example.com

IP:
192.168.1.10

Result:
SUCCESS

Timestamp:
2026-08-25 05:30:00
```

Audit logs must not be deletable through the UI.

---

# 14. Development Environment

Development machine:

```text
Windows
```

Runtime:

```text
Docker Desktop
```

All Linux-specific operations must run inside Linux containers.

Required services:

```text
frontend
api
agent
postgres
redis
nginx
```

Optional test services:

```text
php74
php81
php82
php83
php84
node20
node22
mariadb
mysql
```

---

# 15. Production Environment

Target:

```text
Ubuntu / Debian Linux
```

Production services:

```text
jothost-api.service
jothost-agent.service
nginx
postgresql
redis
php-fpm
```

---

# 16. Success Criteria

The MVP is successful when an administrator can:

1. Install JotHost.
2. Login securely.
3. View server health.
4. Create a website.
5. Select PHP version.
6. Create a database.
7. Issue SSL.
8. Browse files.
9. Edit source code.
10. Create a Node.js application.
11. Create a cron job.
12. View logs.
13. Create backups.
14. Manage firewall.
15. Review security status.
16. Restore a backup.
17. Perform all of the above without SSH for normal operations.

---

# 17. Future Features

- Email hosting
- Reseller accounts
- Billing
- Multi-server management
- Server templates
- Docker application manager
- Git deployment
- GitHub/GitLab integration
- WordPress Toolkit
- Cloudflare automation
- Remote backup
- High availability
- Server clustering