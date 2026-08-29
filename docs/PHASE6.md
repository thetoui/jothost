# Phase 6 — SSL

**Status:** Complete
**Scope:** TASKS.md Phase 6, PRD.md section 8, API_SPEC.md section 16,
DATABASE.md table 19

Phases 4 and 5 both refused `ssl_enabled` and `https_redirect` explicitly
rather than ignoring them. Phase 6 opens those seams: a website can now be
issued a certificate, served over HTTPS, and kept that way without anyone
remembering to renew it.

---

## 1. What was built

### 1.1 Two providers

| Provider | What it is | When it works |
|---|---|---|
| `letsencrypt` | certbot over ACME HTTP-01 | The domain resolves publicly and the challenge path is reachable |
| `selfsigned` | Generated in-process with `crypto/x509` | Always |

The split is deliberate and is the answer to this phase's central constraint,
described in §3.1: a real ACME issuance cannot be tested in a container. The
untestable step is isolated to one small provider, and *everything downstream of
"we now hold a certificate and a key"* — permissions, the HTTPS vhost, the
redirect, expiry tracking, renewal, revocation, deletion cleanup — is exercised
for real against live nginx.

Self-signed is not a test fixture. It is the only option for a host with no
public DNS, which is a real deployment.

### 1.2 The shape of an issuance

```text
API records the certificate (issuing) → job → Agent obtains it
    → key permissions verified → certificate read back and parsed
    → vhost rewritten → nginx reloaded → row marked valid
```

The certificate is obtained **before** the vhost is rewritten. That order is the
whole safety of the operation: a vhost naming a certificate that does not exist
stops nginx from starting at all, which would take down every other site on the
host rather than just this one. Revocation runs the reverse order for the same
reason.

The row only becomes `valid` once the Agent has reported a certificate it could
read and parse, and its expiry comes from the certificate itself rather than
from what was asked for.

### 1.3 Automatic renewal

A sweep runs at startup and every 12 hours. It renews anything within 30 days of
expiry, which is the Let's Encrypt convention: a 90-day certificate renewed at
30 leaves two further chances before anything is actually broken.

A failed renewal is not retried for 6 hours. Without that gap a persistently
failing certificate is retried on every sweep, which for Let's Encrypt means
walking into a rate limit and turning a fixable problem into a blocked one.

### 1.4 Endpoints

| Endpoint | Purpose |
|---|---|
| `GET /api/v1/ssl` | Every certificate, soonest to expire first |
| `GET /api/v1/ssl/providers` | What this host can issue |
| `GET /api/v1/websites/{id}/ssl` | One site's certificate |
| `POST /api/v1/websites/{id}/ssl/issue` | Queue issuance (202) |
| `POST /api/v1/websites/{id}/ssl/renew` | Queue renewal (202) |
| `POST /api/v1/websites/{id}/ssl/revoke` | Queue revocation (202) |
| `PATCH /api/v1/websites/{id}/ssl` | Auto-renewal and the HTTPS redirect |

Issuing and revoking need `ssl.manage`, not a website permission: a certificate
is the site's identity, and that is a different kind of act from editing its
content.

### 1.5 UI

`/ssl` lists every certificate ordered by what runs out first, because that is
what needs attention. Each website's page gains an HTTPS panel that issues,
renews, revokes, and toggles auto-renewal. The dashboard raises an alert for
anything expiring or expired.

---

## 2. Security

### 2.1 The ACME challenge must survive the redirect

This is the most easily broken piece of automated HTTPS, and both ways of
breaking it are silent for two months.

A certificate authority fetches the HTTP-01 challenge over **plain HTTP** and
will not follow a redirect to a certificate it has not issued yet. Two things
would therefore break renewal:

- **Redirecting everything to HTTPS.** The challenge block comes first and the
  redirect covers only `location /`.
- **The dotfile deny rule.** The challenge lives under `/.well-known/`, which
  matches `location ~ /\.`. The challenge block uses the `^~` prefix form, which
  in nginx takes precedence over any regex location, so the deny rule is never
  consulted for that path.

Neither failure appears at issue time. Both appear sixty days later as an
expired certificate and a site that is down for every visitor at once.

The block is emitted for **every** HTTPS site, not only ACME ones — see §4.1 for
why that turned out to matter.

### 2.2 Private keys

A private key is the one file on the host whose disclosure cannot be undone by
any later action: whoever holds it can impersonate the site until the
certificate expires.

- Written mode `0600`, root-owned, in a directory that is mode `0700`.
- Written **with** that mode rather than chmod-ed afterwards: between a
  wide-open create and a later chmod is a window in which any process can read
  it. Replacing a key removes the old file first, because `os.WriteFile` applies
  its mode only on creation and would otherwise keep whatever permissions the
  previous file had.
- Stored under `/etc/jothost/ssl`, deliberately **not** under `/var/www`:
  nothing beneath a document root should ever hold a key, whatever the web
  server config says.
- Permissions are **verified after issuance**, including for certbot's own
  output, which this package does not control. An exposed key fails the
  operation rather than being served.
- Never logged. The Agent's structured errors carry certbot's diagnostics, which
  name the obstacle but never key material.

### 2.3 TLS configuration

TLS 1.2 and 1.3 only. TLS 1.0 and 1.1 are omitted rather than offered and
discouraged — both are deprecated and prohibited for anything handling card
data. The cipher list is forward-secret and AEAD only; anything without both is
absent rather than ordered below the good options.

Session tickets are off: without key rotation they undermine the forward secrecy
the cipher list was chosen for. HSTS is set to one year, without `preload` or
`includeSubDomains`, because both are commitments a site owner has to make
deliberately rather than have a control panel make for them.

### 2.4 Untrusted input

A certificate path reaches an `ssl_certificate` directive read by a root
process. Paths are rejected if they are relative, traverse upward, or contain a
character that could close a directive. Domains are validated by
`shared/validate` before becoming a directory name or a certbot argument, and
nothing reaches a shell — certbot is executed with an argument vector.

Self-signed certificates are generated in-process rather than by invoking
`openssl`, which removes the last place a domain name would have been passed to
another program as an argument.

---

## 3. Decisions and deviations

### 3.1 A real ACME issuance is not tested

Let's Encrypt must reach `http://<domain>/.well-known/acme-challenge/` over
public DNS. A container on a private network has neither, so no test in this
repository issues a publicly trusted certificate.

What *is* tested is everything the two providers share, which is all of it
except the ACME exchange itself: key handling, the vhost, the redirect, the
challenge path, expiry, renewal, revocation, and cleanup. certbot is installed
in the agent image so its code path compiles and runs as far as the network,
and the argument construction is unit-tested.

This is the same class of gap as Phase 5's package installation, and it is
recorded here for the same reason: the honest position is that one step is
verified by construction and review rather than by execution.

### 3.2 Certificates are read from certbot's own paths

A certbot certificate is referenced at `/etc/letsencrypt/live/<domain>/` rather
than copied into the panel's store. certbot renews in place through a symlink
under `live/`, and a copy would go stale the first time it renewed without the
panel noticing.

### 3.3 The webroot plugin, not the nginx plugin

certbot's nginx plugin rewrites the server's configuration itself, and this
panel owns those files. Two writers editing one vhost is how a configuration
ends up in a state neither of them expects.

### 3.4 Revoking a self-signed certificate deletes it

There is no authority to revoke a self-signed certificate with. The panel
deletes it instead and reports "revoked", which is honest at the panel's level:
either way the certificate stops being used and cannot come back.

---

## 4. Bugs found and fixed during this phase

### 4.1 The challenge block was only emitted for ACME sites

Originally the challenge path was exposed only when the provider was
`letsencrypt`. That is wrong, and the reason is a sequencing one: issuance
happens while the *previous* configuration is still live. A self-signed site
with the redirect on would therefore send the authority's challenge fetch to
HTTPS — so switching that site to Let's Encrypt could never succeed.

The block is now emitted for every HTTPS site. It costs one location and serves
nothing but transient tokens.

### 4.2 A handler panic killed the whole Agent

A nil progress reporter in the revoke handler panicked, and the panic took the
Agent process down with it — leaving every site on the host unmanageable until
someone noticed and restarted it.

The nil call was mine and is fixed, but the larger problem was that nothing
stopped a panic anywhere in any handler from doing the same. Operation dispatch
now recovers, logs the panic with its stack, and returns a generic internal
error. One failed operation is a far better outcome than a dead daemon.

### 4.3 Internal errors were logged nowhere

`httpx.Internal` preserved a cause "for logging" that nothing ever logged. A 500
therefore told the client nothing by design and the operator nothing by
accident, leaving a fault with no way in at all.

`httpx.Error` now logs the cause for any 5xx. It found the next bug within a
minute of being added.

### 4.4 A `RETURNING` clause used table-qualified columns

The certificate column list was written with a `c.` prefix for the queries that
join `websites`, then reused in an `INSERT ... RETURNING`, which has no alias to
qualify against. There are now two lists, named for which is which.

### 4.5 The store's test root leaked into stored paths

`Write` returned root-prefixed paths while `Load` prefixed them again. In
production the root is empty so it never showed — which is exactly what the test
seam exists to catch. Paths are now logical everywhere, and the root is applied
only at the syscall boundary.

---

## 5. Testing

| Layer | Coverage |
|---|---|
| `agent/internal/ssl` | Self-signed generation, name coverage, backdating, key permissions on write and rewrite, exposed-key detection, traversal in domains, parsing, renewal window |
| `agent/internal/nginx` | The challenge block's position and prefix form, TLS protocol and cipher policy, HSTS, certificate path injection, no HTTPS block without a certificate |
| `api/internal/ssl` | Alias coverage, status derived from expiry, the renewal sweep and its retry gap, reconciliation from the Agent's result, revocation, cascade on website deletion, list ordering |
| `api/internal/server` | Route authorisation, `ssl.manage` gating, validation, the job-vs-no-job split on configure |
| `frontend` | Status and expiry presentation, permission-gated controls |
| `tests/integration/phase6_ssl.sh` | 44 black-box checks against live nginx |

The integration suite runs inside the agent container so it can inspect key
permissions on disk, and it is idempotent — verified by running it twice in
succession.

Run it with:

```bash
make docker-test-ssl
```

---

## 6. Known limitations

1. **No real ACME issuance is tested.** See §3.1.
2. **HTTP-01 only.** No DNS-01, so wildcard certificates cannot be issued.
3. **No OCSP stapling.** It needs a resolver configured and a trusted chain on
   disk; a self-signed certificate has neither, and enabling it half-way
   produces warnings rather than benefit.
4. **No certificate upload.** A certificate bought elsewhere cannot be installed
   through the panel.
5. **The renewal sweep is in-process.** It runs in the API, so a panel that is
   down for a month does not renew. The 30-day window makes that survivable, but
   it is not the same as a system timer.
6. **Expiry alerts are dashboard-only.** Nothing emails or pages anyone.
7. **HSTS is not configurable per site.** It is on for every HTTPS site at one
   year, which is right for most and cannot currently be turned off for the
   site where it is not.
