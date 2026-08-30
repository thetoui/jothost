# Phase 9 — Node.js Manager

What this phase adds, why it is shaped this way, and what the live checks found
that the unit tests could not.

---

## 1. What it does

The panel installs a Node.js runtime, and runs one application per website: its
own directory, its own port, its own environment, under that website's system
account, with nginx reverse-proxying the site's domain to it.

An application is created, started, stopped, restarted, watched, and removed.
Its dependencies are installed with npm. Its output is readable. Its
configuration is stored encrypted and written to the host.

---

## 2. Design decisions

### 2.1 systemd where it exists, the Agent's own supervisor where it does not

A systemd unit is the right mechanism on a real host, and the panel writes one.
But containers and minimal images have no systemd, so the Agent can also
supervise a process itself.

Callers never choose. Which mechanism a host uses is a property of the host, and
every operation answers the same way either way — so nothing above the `nodejs`
package branches on it, and a panel moving between a real machine and a
container behaves the same.

The unit is where the confinement lives, and it is the reason to prefer systemd:

| Directive | What it stops |
| --- | --- |
| `NoNewPrivileges` | gaining privileges through a setuid binary |
| `ProtectSystem=strict` + `ReadWritePaths` | writing anywhere but its own directory and logs |
| `PrivateTmp` | reading what another application left in `/tmp` |
| `ProtectHome` | reading every other account's home directory |
| `RestrictAddressFamilies` | opening a raw socket |
| `StartLimitBurst=5` | a broken deployment restarting forever and burning the host |

The supervisor gives a process its own **session** rather than just its own
process group, so an application keeps serving while the Agent is upgraded or
restarted. The cost is that the Agent cannot `wait()` on it, which is why
liveness is read from the process table — and why the pid file alone is not
trusted: pids are reused, so what is signalled is checked to still be a node
process running *this* application's entry point.

### 2.2 The application runs as the website, never as root

The account is the site's own. An application can therefore reach its own files
and nobody else's, and the credential is applied by the kernel at `exec`, so the
process never runs as root even for an instant.

`npm install` runs as that account too. Installed as root, a deployment ends up
with a `node_modules` it can read and never update.

### 2.3 The environment is the caller's data, and that changes the boundary

Every other program the Agent runs has a fixed allowlist of environment
variables it may be given. Node cannot: an application's configuration is a
database URL, an API key, a feature flag — unbounded by nature.

So the boundary moves rather than disappearing. `shared/validate.EnvKey`
refuses the names that change *what runs* rather than how it behaves —
`LD_PRELOAD`, `LD_LIBRARY_PATH`, `NODE_OPTIONS`, `PATH`, `BASH_ENV` — and the
names the panel sets itself, so one value cannot contradict another. Values are
refused if they contain a newline, which would close a line in a unit file or an
env file and begin a directive of the caller's choosing.

`PORT` is the clearest case: the panel tells the application which port to listen
on and points nginx at the same number. An application that could override it
would listen somewhere nginx is not. `NODE_ENV` is the one default an
application may change, because a staging deployment legitimately wants a
different value and nothing outside the process has to agree with it.

Values are stored AES-256-GCM encrypted, bound to their own row. A listing says
what is set, never what it is set to; reading the values is a separate endpoint
with its own audit record and a stricter permission.

### 2.4 The port is bounded at both ends

Below 1024 needs root, which an application never has. From 32768 up is the
range the kernel hands to outgoing connections, so an application asked to
listen there starts fine most days and fails with "address already in use" on
the day something else got there first — a fault that looks random and is not.

The port is also unique per server in the schema, because two applications on
one port means one of them is failing to bind and the panel would not know
which.

### 2.5 The vhost is changed around the process, not with it

Starting points nginx at the application **only once it is listening**. The site
was serving something before the request; replacing that with a 502 while the
process boots would be a worse outcome than the one the operator asked for.

Stopping does the reverse first: the site goes back to serving its files, *then*
the process is stopped. A deliberate stop should not look like an outage.

### 2.6 The reverse proxy passes what an application needs

`Host`, or the application sees `127.0.0.1` and builds every absolute URL wrong.
`X-Real-IP` and `X-Forwarded-For`, or every request appears to come from the
proxy and rate limiting, logging and abuse handling all see one client.
`X-Forwarded-Proto`, because that is how a framework decides whether to set a
Secure cookie — and it is `$scheme`, so the same block is right in both the HTTP
and HTTPS servers.

`Upgrade` and `Connection` are passed through so WebSockets work. A Node
application that cannot accept a WebSocket has half its use missing, and the two
lines that enable it are easy to leave out and hard to diagnose afterwards.

### 2.7 A site is served by an application or by files, never both

Creating an application on a site that serves PHP is refused, and the renderer
refuses a configuration carrying both. The alternative is a vhost that sends
some URLs to PHP and the rest to Node, which is never what anybody meant.

---

## 3. What the live checks found

Each of these passed unit tests and failed against a real host.

**1. A pool file does nothing on its own.** Writing the FPM pool and pointing
nginx at it produced a 502 against a configuration that looked entirely correct:
FPM has to be told to read it, and the socket has to exist first. (Found in the
phpMyAdmin work in Phase 8, and the same shape applies here.)

**2. The Agent's environment allowlist blocked the application's own
variables** — which is the entire feature. It failed as "command is not
allowlisted: node may not set GREETING", which is accurate and useless. The
allowlist now has a documented exception for exactly one spec, with the
validator as the boundary instead.

**3. The command allowlist is fixed at startup**, so a runtime the panel had
just installed could not be run until the Agent restarted — and `node.install`
reported failure for a package that installed perfectly well. The Node spec is
now registered at a pinned path whether or not the binary is there yet;
`Runner.Available` checks at call time.

**4. `WritePlaceholder` skipped an existing `index.html`** and left it owned by
an account that no longer existed, so a site created over a leftover directory
returned 403 from the moment it was made — precisely the outcome that function
exists to prevent. Its ownership is now corrected; its content is not touched.

**5. The lifecycle error message read "could not be startinged"**, built by
appending "ed" to a progress verb.

Three more were bugs in the integration script, each of which made a passing
check meaningless:

- A website's response carries a **domains array whose rows are `"status":
  "active"` from the moment they are written**, so matching the whole body for
  that string succeeded while the website itself was still being created.
- `chown -R owner:owner` on a document root **removed the group nginx reads
  through**, turning a working site into a 403.
- Checks fired **during nginx's graceful reload**, when the old workers are
  still serving the previous configuration. That is correct behaviour — no
  request is dropped — so the checks poll rather than assume.

---

## 4. The development environment

The agent container does **not** ship with Node.js. Installing it is part of the
feature, and a bare host is where a real operator starts, so the integration
suite installs it on every run rather than requiring it.

That container has no systemd, so development exercises the Agent's own
supervisor. The systemd path is covered by unit tests over the rendered unit
(and is what a production host uses); it is the one part of this phase not
driven end to end here, which is stated plainly rather than implied.

The container's `/etc/passwd` is now preserved across image rebuilds. The panel
creates a Unix account per website, and rebuilding the image deleted every one
of them while `/var/www` — a volume — kept every site's files: a host whose
sites exist on disk, are listed in the panel, and cannot be served because the
account that owns them is gone. On a real machine none of this applies.

---

## 5. Verification

```bash
make docker-test-node
```

47 checks against the running stack, from inside the agent container so it can
see the process, its account, and its logs. It deploys a **real Express
application**, not a stub.

What it actually verifies, beyond status codes:

- the panel installs a runtime on a host that has none, and npm with it
- an application is created **stopped**, and the site keeps serving its files
- `npm install` produces a `node_modules` owned by the website's account
- the domain reaches the application through nginx, with the right `Host`,
  `X-Forwarded-Proto` and `X-Forwarded-For`
- the process runs as the website's account and **not** as root
- the port is bound on the loopback only
- the environment the panel set arrives in the process
- a listing names a variable but never carries its value
- variables that would change what runs are refused, as are values containing
  a newline
- restarting keeps it serving; stopping returns the site to its files rather
  than to a 502
- privileged and ephemeral ports, and startup files outside the application,
  are refused
- removing it leaves the customer's code and dependencies exactly where they are

---

## 6. Known limitations

- **The systemd path is not exercised end to end** in development, because the
  container has no systemd. The unit is unit-tested; a production host is where
  it actually runs.
- **One application per website.** A second would need a second vhost to reach
  it, and the site has one domain.
- **No build step.** The panel installs dependencies and starts the entry point;
  it does not run `npm run build`. A framework needing a build expects it to
  have happened before deployment.
- **No zero-downtime deploy.** A restart is a stop and a start, so there is a
  gap of a second or two. Nothing is queued during it.
- **The Agent's supervisor does not restart a crashed process.** systemd does,
  through `Restart=always`; the supervisor reports the application as failed and
  leaves restarting to an operator. Reconciling that automatically is worth
  doing and is not here.
- **Logs under systemd are in the journal**, which this panel does not read. It
  says where they are rather than returning an empty list.
- **The runtime is a release line, not a version.** Alpine ships one `nodejs`
  and one `nodejs-current`; there is no per-major package to choose from, so the
  panel offers the lines the host actually has and reports which major arrived.
