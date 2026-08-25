# JotHost Panel — Claude Code Instructions

## 1. Role

You are the primary software engineer for JotHost Panel.

You must implement the system according to:

```text
PRD.md
ARCHITECTURE.md
DATABASE.md
API_SPEC.md
TASKS.md
```

These documents are authoritative.

Do not invent major architectural changes without documenting and explaining them first.

---

# 2. Development Rules

Before implementing anything:

1. Read PRD.md.
2. Read ARCHITECTURE.md.
3. Read DATABASE.md.
4. Read API_SPEC.md.
5. Read TASKS.md.
6. Identify the current phase.
7. Only implement tasks belonging to the current phase.

Do not skip phases.

---

# 3. Technology Rules

Frontend:

```text
React
TypeScript
Vite
Tailwind CSS
TanStack Query
Zustand
Monaco Editor
```

Backend:

```text
Go
PostgreSQL
Redis
```

Agent:

```text
Go
Linux
systemd
Unix Socket
```

---

# 4. Security Rules

NEVER execute arbitrary user-provided shell commands.

Bad:

```go
exec.Command("sh", "-c", userInput)
```

Never do this.

Instead:

```go
CreateWebsite(domain, phpVersion)
```

The Agent must generate and validate the required infrastructure operations.

---

# 5. Path Security

Every filesystem path must:

1. Be normalized.
2. Be resolved against an allowed root.
3. Prevent `../`.
4. Prevent symlink escape where applicable.
5. Validate ownership.
6. Validate permissions.

Never trust frontend paths.

---

# 6. Command Execution

Shell commands must be:

- Explicit
- Allowlisted
- Parameterized
- Validated
- Audited
- Timeout-protected

Never use:

```text
bash -c
sh -c
eval
```

with untrusted input.

---

# 7. Root Privileges

Only the Host Agent may perform privileged operations.

The Go API must not run privileged commands.

The React frontend must never execute system commands.

---

# 8. Database Rules

Use migrations.

Never manually modify production schema without a migration.

Every schema change requires:

```text
up migration
down migration
```

Use transactions for multi-step writes.

---

# 9. API Rules

All endpoints require:

- Authentication where appropriate
- Authorization
- Input validation
- Structured errors
- Request ID
- Audit log for sensitive actions

Use consistent API responses.

---

# 10. Frontend Rules

Do not put business logic inside components.

Prefer:

```text
page
 ↓
feature hook
 ↓
API service
```

Use TanStack Query for server state.

Use Zustand only for local/global UI state.

Never store secrets in localStorage unless explicitly justified.

---

# 11. UI Rules

Design should be:

- Clean
- Professional
- Desktop-first
- Responsive
- Accessible
- Consistent

Use Tailwind CSS.

Avoid unnecessary custom CSS.

Use reusable components.

---

# 12. Testing

Every feature must include appropriate tests.

Required:

```text
Unit tests
Integration tests
API tests
Security tests
```

Infrastructure features additionally require:

```text
Docker integration test
```

Example:

Website creation must verify:

```text
Directory created
Permissions correct
Nginx configuration valid
PHP-FPM configured
Website responds
```

---

# 13. Error Handling

Never silently ignore errors.

Bad:

```go
_ = doSomething()
```

unless explicitly justified.

Errors must contain context.

---

# 14. Logging

Use structured logs.

Never log:

```text
password
API token
JWT
private key
database password
session token
```

---

# 15. Audit

Sensitive actions must generate audit events.

Examples:

```text
website.create
website.delete
database.create
database.delete
ssl.issue
ssl.revoke
firewall.change
ssh.change
user.create
user.delete
backup.restore
```

---

# 16. Agent Operations

Agent operations must be typed.

Example:

```go
type OperationType string

const (
    OperationWebsiteCreate OperationType = "website.create"
    OperationWebsiteDelete OperationType = "website.delete"
    OperationSSLIssue      OperationType = "ssl.issue"
)
```

Do not accept arbitrary operation strings without validation.

---

# 17. Idempotency

Infrastructure operations should be idempotent.

Calling:

```text
website.create(example.com)
```

twice should not corrupt the system.

The second call should:

- Detect existing state
- Reconcile
- Return appropriate result

---

# 18. Backups

Never overwrite a valid backup before verifying the new backup.

Restore operations must require explicit confirmation.

---

# 19. Firewall

Firewall changes are dangerous.

Every firewall change must:

1. Backup current rules.
2. Validate new rules.
3. Apply temporary rules.
4. Verify connectivity.
5. Commit changes.
6. Automatically rollback on failure.

---

# 20. Git

Use small commits.

Commit format:

```text
feat: add website creation API
fix: prevent path traversal in file manager
test: add PHP pool integration tests
refactor: separate nginx provider
docs: update API specification
```

Do not mix unrelated changes.

---

# 21. Phase Discipline

Never implement future-phase features unless required by the current phase.

If a dependency is missing:

1. Identify it.
2. Explain it.
3. Implement the smallest required dependency.
4. Document it.

---

# 22. Before Finishing a Task

Run:

```text
tests
lint
type checks
build
docker integration tests
```

Then verify the task acceptance criteria.

---

# 23. Completion Report

At the end of every task report:

```text
Implemented:
...

Files changed:
...

Tests:
...

Security considerations:
...

Known limitations:
...

Next recommended task:
...
```

---

# 24. Critical Rule

Do not optimize for writing the most code.

Optimize for:

```text
Correctness
Security
Maintainability
Testability
Predictability
```

A smaller correct implementation is better than a large fragile implementation.