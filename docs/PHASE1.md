# Phase 1 — Authentication

**Status:** Complete
**Scope:** TASKS.md Phase 1, DATABASE.md tables 1–7 and 25, API_SPEC.md section 2

Phase 1 turns the Phase 0 skeleton into something with a security boundary:
accounts, passwords, sessions, roles, two-factor authentication, and an
append-only audit trail.

---

## 1. What was built

### 1.1 Schema and migrations

A minimal migration runner (`api/internal/db/migrate`) applies paired up/down
SQL files under a Postgres advisory lock, one transaction per migration.

| Migration | Contents |
|---|---|
| `0001_auth` | users, roles, permissions, user_roles, role_permissions, sessions, two_factor_auth, audit_logs |
| `0002_seed_rbac` | 16 permissions, the `admin`/`operator`/`viewer` roles, and their grants |
| `0003_session_token_history` | retired refresh-token hashes, for reuse detection |

Migrations run automatically at startup (`AUTO_MIGRATE`, default on). With it
off, the API verifies the schema instead and refuses to serve a database it
does not recognise, rather than failing later at query time.

### 1.2 Identity and credentials

| Concern | Implementation |
|---|---|
| Password hashing | Argon2id, 46 MiB / t=1 / p=1, PHC-encoded so parameters travel with the hash |
| Parameter upgrades | `NeedsRehash` + transparent rehash on next successful login |
| Password policy | 12–1024 characters; length only, no composition rules |
| Username rules | Enforced in Go *and* as a CHECK constraint, normalised to lowercase |
| First admin | `jothost-api create-admin`, credentials read from the environment |

### 1.3 Sessions

- **Access token**: opaque 32-byte random string, stored in Redis as a SHA-256
  hash keyed to the caller's claims. 15-minute TTL.
- **Refresh token**: opaque 32-byte random string, stored in Postgres as a
  SHA-256 hash. 7-day TTL, rotated on every use.
- **Logout**: revokes the session row and drops every access token issued for
  it, so it takes effect on the next request rather than at expiry.

### 1.4 Two-factor authentication

RFC 6238 TOTP over stdlib HMAC-SHA1: 6 digits, 30-second step, ±1 step of
drift. Verified against the RFC's published test vectors. Secrets are encrypted
with AES-256-GCM before storage, bound to the owning user as additional
authenticated data.

Login becomes two steps when 2FA is enabled: a correct password returns a
short-lived `mfa_token` (not an access token), which `POST /auth/2fa/verify`
exchanges for a real session.

### 1.5 RBAC

Three seeded roles over 16 permissions. `Service.RequirePermission` gates
handlers; the frontend mirrors the permission list to hide controls, but the
API is what enforces access.

### 1.6 Audit

Every authentication event writes an `audit_logs` row: successful and failed
logins, rate-limit hits, logout, refresh, refresh reuse, and every 2FA state
change.

---

## 2. Security properties, and the tests that pin them

| Property | Mechanism | Test |
|---|---|---|
| No username enumeration | Identical error for a wrong password and an unknown user; a dummy Argon2 verify equalises timing | `TestLoginErrorIsIdenticalForUnknownUsers`, integration check |
| Passwords unrecoverable | Argon2id, per-password salt, never logged | `TestHashIsSaltedPerCall`, `TestSecretsNeverReachTheLog` |
| Tokens unusable from a database dump | Only SHA-256 hashes are stored, both in Redis and Postgres | `TestAccessTokenIsNotStoredInPlaintext` |
| Stolen refresh token is single-use | Rotation on every refresh | `TestRefreshRotatesBothTokens` |
| Stolen refresh token is detected | Retired hashes recorded; replay revokes the whole session set | `TestRefreshReuseRevokesTheWholeSession` |
| Logout is immediate | Access tokens dropped from Redis on revoke | `TestLogoutRevokesImmediately` |
| Disabling an account ends its sessions | Status re-checked on refresh and on every request | `TestRefreshRejectsInactiveAccount` |
| Brute force is throttled | Redis limiter per account *and* per IP, failing closed | `TestLoginRateLimitLocksAfterRepeatedFailures` |
| 2FA secrets unreadable at rest | AES-256-GCM with per-user AAD | `TestTwoFactorSecretIsEncryptedAtRest` |
| A password alone is not a session | Challenge token lives under a separate key prefix | `TestChallengeTokenIsNotAnAccessToken` |
| A second factor cannot be silently swapped | Setup refuses while enabled; disable requires the password | `TestSetupTwoFactorRefusesWhenAlreadyEnabled`, `TestDisableTwoFactorRequiresThePassword` |
| Audit history is immutable | Database triggers reject UPDATE and DELETE | `TestAuditLogIsAppendOnly` |
| Tokens stay out of URLs and logs | Bearer header only; query strings never parsed for tokens | `TestTokenIsNotAcceptedFromQueryString` |
| Permissions match exactly | No prefix or wildcard matching | `TestHasRequiresAnExactMatch` |

---

## 3. Decisions and their rationale

### 3.1 Opaque access tokens instead of JWTs

ARCHITECTURE.md section 11 lists `JWT_SECRET`, implying signed tokens. Phase 1
uses opaque random tokens resolved against Redis instead.

*Why:*

1. **Revocation is the whole point of logout.** A JWT stays valid until it
   expires; making logout immediate requires a server-side denylist, which is
   the same lookup an opaque token needs — without the signature verification.
2. **An entire bug class disappears.** No `alg` confusion, no `none` algorithm,
   no key-confusion, no clock-skew claim validation to get wrong.
3. **The trade-off is affordable.** Stateless tokens matter for horizontal
   scale; multi-server clustering is an explicit non-goal in PRD.md section 3,
   and Redis is already a required dependency.

*Consequence:* Redis is now on the critical path for every authenticated
request. It was already required for rate limiting.

`JWT_SECRET` is therefore not used. `ENCRYPTION_KEY` is required instead.

### 3.2 A separate table for retired refresh tokens

Rotation overwrites `sessions.token_hash`. A replayed older token then matches
no row and is indistinguishable from a token that was never issued — so the
theft goes unnoticed. `session_token_history` records retired hashes, making a
replay unambiguous at any generation.

This adds a table to DATABASE.md section 6, documented there as 6.1.

*Alternative considered:* one session row per rotation, linked into a family.
Rejected because it churns session IDs, which the Phase 22 "active sessions"
UI will want to be stable.

### 3.3 Refresh reuse revokes every session, not just the affected one

When a retired token is presented, the legitimate holder's current token is
revoked too.

*Why:* at that moment it is unknowable which party is the attacker. Signing
both out costs one honest user a re-login; leaving the session alive leaves an
intruder inside.

### 3.4 The rate limiter fails closed

A Redis error on the login path returns an error rather than allowing the
attempt.

*Why:* otherwise an attacker who can disrupt Redis can switch off brute-force
protection — turning an availability problem into an authentication bypass.

### 3.5 `X-Forwarded-For` is ignored

The per-IP limiter uses the TCP peer address only.

*Why:* the header is client-controlled. Honouring it without a trusted-proxy
allowlist would let an attacker rotate a header value to get unlimited login
attempts. Behind the bundled Nginx the peer address is correct; a
trusted-proxy configuration belongs with the production installer (Phase 23).

### 3.6 Test suites run against real Postgres and Redis

Repository, session, and auth tests use live engines rather than mocks, and
skip when `TEST_DATABASE_URL` / `TEST_REDIS_URL` are unset.

*Why:* the properties that matter here — the append-only trigger, the atomic
rotate, unique-constraint behaviour, Redis expiry — live in the engines. A mock
would assert my assumptions rather than their behaviour. The suite shares one
database, so packages must run serially (`go test -p 1`); the Makefile and CI
both pass it.

### 3.7 Token storage in the browser

The access token is held in memory only. The refresh token is persisted in
`localStorage`, which CLAUDE.md section 10 requires justifying:

- Without persistence, every page reload is a re-login.
- The XSS exposure is bounded server-side: refresh tokens are single-use, and
  using a stolen one destroys the session and raises an audit event.

The stronger option is an httpOnly `SameSite=Strict` cookie, which JavaScript
cannot read at all. That requires the API to set cookies rather than return
tokens in the body — a change to API_SPEC.md section 2 — and is recorded below
as recommended hardening.

---

## 4. How to verify

```bash
make dev
make create-admin JOTHOST_ADMIN_USERNAME=admin   # password via environment
```

```bash
make verify                   # lint, unit tests, both integration suites
make docker-test-auth         # Phase 1 authentication checks only
```

Manually:

```bash
curl -s -X POST http://localhost:8080/api/v1/auth/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"..."}'
```

```bash
curl -s http://localhost:8080/api/v1/auth/me -H "Authorization: Bearer $ACCESS_TOKEN"
```

---

## 5. Known limitations

Each is deliberate and scoped to a later phase.

1. **No recovery codes.** Losing an authenticator device means an operator must
   clear `two_factor_auth` directly in the database. Recovery codes are
   recommended before any multi-user deployment (Phase 22).
2. **Permissions in an access token are a snapshot.** A role change takes
   effect on the next refresh — at most `ACCESS_TOKEN_TTL` later — rather than
   instantly. `GET /auth/me` always reads live, so the UI updates immediately.
3. **No user-management API.** Accounts are created with `create-admin` only;
   CRUD, password change, and invitations belong to Phase 22.
4. **No `GET /audit-logs` endpoint.** Audit rows are written and queryable in
   the database, but API_SPEC.md section 29's read endpoints are not
   implemented — they belong with a UI to display them.
5. **No session-listing or remote sign-out UI.** The data supports it; the
   screens arrive with Phase 22.
6. **Expired sessions are not swept.** `DeleteExpired` exists and is tested but
   nothing schedules it; it wants the Phase 10 cron infrastructure.
7. **Rate limiting covers login only.** Other endpoints are unthrottled.
   API_SPEC.md section 31 wants it everywhere; that is Phase 24 work.
8. **The password policy is length-only.** No breached-password check, no
   history, no expiry.
9. **`localStorage` refresh tokens.** See 3.7. Moving to httpOnly cookies is
   the recommended Phase 24 hardening.
10. **Redis is a single point of failure for authentication.** If Redis is
    down, no request authenticates. This is the accepted cost of 3.1.

---

## 6. Next recommended task

**Phase 2 — Host Agent.** The auth layer now exists to protect it, and the
Phase 0 operation allowlist is waiting to be filled in:

1. Metric collectors (CPU, RAM, disk, network, processes, services) as typed
   operations registered in `shared/protocol`.
2. Agent-side authentication and per-operation audit, mirroring the API's.
3. The job execution model from ARCHITECTURE.md section 9.
4. The Phase 2 security tests — path traversal, command injection, privilege
   escalation, timeouts, resource abuse — extending
   `agent/internal/socket/server_test.go`.
