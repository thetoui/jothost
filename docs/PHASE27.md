# Phase 27 — Git & Webhook Deployment Actions

A website's source comes from a repository; the panel checks it out and runs the
steps that turn it into a working site. Everything runs as the website's own
unprivileged account, never as root.

---

## 1. The rule this phase has to answer for

CLAUDE.md section 4 says: **never execute arbitrary user-provided shell
commands**, and names `exec.Command("sh", "-c", userInput)` as the thing not to
do. A deployment executes code by definition — `composer install` runs a
package's install scripts, `npm run build` runs whatever the project's build
script says — so the reasoning is set out here rather than left implicit.

It is the same reasoning `agent/internal/cron/run.go` already set out for Phase
10, and it holds more strongly here.

The rule exists to stop the panel becoming a remote shell: a caller who should
not have command execution getting it by passing a string through an API. A
caller who can configure a deployment **already has command execution as that
account** — they can create a scheduled job, which is a command line run by
crond as this same account on any schedule they choose. And the website's own
application code already runs as that account, on every request, through
PHP-FPM. A deployment step therefore grants no authority that was not already
granted: it changes *when* code runs, not who it runs as or what it can reach.

What the phase does with that is not "so anything goes". It is arranged so that
**almost nothing is a string at all**:

- Six of the seven steps are typed actions from a closed set, and each becomes a
  fixed argv the Agent builds. Nothing from a request appears in any of those
  command lines. This is the path the UI offers first and it covers nearly every
  real deployment.
- The seventh is the deployment script, and it is bounded rather than trusted:
  never as root, in the site's own directory as the site's own account,
  timeout-protected, output captured and capped, and — the detail worth stating
  twice — **written to a file and run as `sh <file>`, never as `sh -c <text>`.**

That last one is not stylistic. The process table on a Linux host is
world-readable, so a script passed on a command line is visible to every account
on the machine for as long as it runs, and a deployment script is exactly the
kind of text that contains a token, because that is what people put in them. The
file is 0600, owned by the site account, and removed afterwards.

The panel also refuses to grow a shell endpoint. There is no route anywhere in
this phase that takes a command and runs it: the script is stored configuration,
edited deliberately, shown on a page, and audited when it changes.

---

## 2. The most dangerous string in the phase is the repository URL

git's remote is not an address. It is a small language, and three of its
dialects run programs:

- `ext::sh -c whoami` is remote code execution spelled as a URL.
- A remote beginning with a hyphen is not a remote at all but an **option** —
  `--upload-pack=/tmp/evil` as a "URL" hands git a program to run, and it looks
  exactly like a typo.
- `file://` and a bare path read a repository on this host, including one
  somebody uploaded through the file manager.

So `validate.GitRemote` is an **allowlist**, not a list of refusals. Two forms
are accepted, which are the two forms anybody actually uses:

```
https://host/owner/repo.git
git@host:owner/repo.git      (and ssh://git@host/owner/repo.git)
```

Everything else is refused by not being one of them — which is the property that
matters, because a transport added to a future git is refused here without
anybody having to remember to refuse it. The shape is mirrored as a CHECK
constraint, because that column becomes an argument to a program.

Branches and commits get the same treatment for the same reason: a branch
starting with a hyphen is an option, `main:refs/heads` is half a refspec,
`HEAD@{1}` is a revision expression, and a commit is hexadecimal or it is not a
commit.

---

## 3. The webhook is the panel's only unauthenticated route that runs code

It is worth being exact about what protects it.

**The token in the URL does not.** It is an address: it selects which repository
a push is about, so the panel knows which secret to verify against. Anybody who
can read a forge's settings page can read it.

**The signature does.** Every accepted request carries an HMAC-SHA256 over the
exact bytes of the body, computed with a secret the operator set on both sides.
The comparison is constant-time — a byte-by-byte comparison of an HMAC leaks,
through timing, how much of a guess was right — the body is read under a hard
cap and verified *before* it is parsed, and every way of failing to authenticate
answers identically, including a token that matches no repository. Telling an
unauthenticated caller which tokens exist is telling them what to attack.

What the panel deliberately does **not** take from a webhook payload:

- not the repository URL. A push claiming to come from somewhere else changes
  nothing; the panel deploys the remote it has recorded.
- not a commit. The deployment resolves the tip of the configured branch itself,
  so a forged payload cannot pin a site to an arbitrary revision even if it were
  somehow signed.
- not a branch to deploy. The branch in the payload is compared against the
  configured one and is otherwise discarded.

A verified push is a *signal that something changed*. Every fact about what to
deploy comes from the panel's own record.

A webhook cannot exist without a secret: the schema refuses a token with no
secret, and the service refuses to create one. GitLab's scheme — which sends the
secret itself in a header — is supported and is weaker, and it is supported
because refusing GitLab would not make anybody safer, only push them to
something worse.

---

## 4. What rollback does, and what it cannot

Automatic rollback restores the **source**, by resetting the working tree to the
commit the site was on before. It does not undo a build.

That limit is real and it is stated in the log, on the page, and here, rather
than implied away by the word "rollback". The files a build wrote are not in the
repository, so the only way to remove them would be to delete everything git
does not know about — which on a real site is the customer's uploads, their
storage directory and their `.env`. A migration that ran is likewise not undone;
`php artisan migrate` has no inverse the panel can safely call.

A rollback that itself fails is recorded rather than swallowed, because it is the
state an operator most needs to be told about: the site is then on neither
commit, and nothing else in the panel would say so.

### Why the checkout is in place rather than a release directory

The familiar arrangement is a directory per release with a symlink swapped at the
end, which makes a deployment atomic. This panel does not do that, and the reason
is what a document root actually contains: a WordPress site has
`wp-content/uploads` in it, a Laravel site has `storage/`, and neither is in the
repository. **Swapping directories would deploy the code and lose the customer's
files.**

So the working tree is updated in place, with a hard reset — a deployment is not
a collaboration, and a merge that conflicts would stop halfway and leave the site
serving a half-merged tree. The panel reports uncommitted changes before it does
this, because this destroys them.

---

## 5. Where the deploy key lives

On the host that authenticates with it, and nowhere else. The panel records the
public half and the fingerprint, which is what somebody needs in order to paste
it into a forge.

The same decision Phase 26 made about DKIM keys, for a sharper reason: this key
grants read access to a customer's *source code*, and the control-plane database
is backed up, replicated and read by every part of the API.

Ed25519 rather than RSA — smaller, faster, accepted everywhere for years, and no
key-size decision to get wrong for a key the panel generates on somebody's behalf
without asking them anything.

`StrictHostKeyChecking` is **accept-new**, which is weaker than the `yes` Phase
14 chose for SFTP backups, deliberately. Phase 14 could use `yes` because the
operator types in the destination host and can be asked for its key. Here the
host is github.com, and nobody operating a control panel has GitHub's host key to
hand: `yes` would make every first deployment fail with an error there is no way
to act on from a web page, and `no` would accept a different key on every
connection, which is the setting that makes host key checking meaningless.
`accept-new` pins the key on first use and refuses a *change* — which is the
property that actually matters, because a changed key is what an interception
looks like. The `known_hosts` file is per website, so one customer's pinning
cannot decide what another customer's deployment trusts.

---

## 6. Smaller decisions with reasons

- **One repository per website**, enforced by a unique index. Two would be two
  things writing into one document root, and the second deployment would
  silently undo the first.
- **One deployment at a time per repository**, also an index — a unique partial
  index over the rows that are pending or running. Two deployments into one
  working tree is one process rewriting the tree while another builds from it,
  and the result is neither commit. A check written in Go is one that two API
  processes could both pass.
- **A deployment left running by a restart is closed at startup.** Otherwise it
  would hold that index for ever: no further deployment of that website could
  start, and the page would show one in progress that nothing was progressing.
- **Step order is explicit**, not implied by insertion order. "npm ci" after
  "npm run build" is not a deployment, it is a deployment that fails. Reordering
  is one transaction, because applying it as a sequence of updates passes
  through states where two steps share a position.
- **A build never sees the Agent's environment**, which holds the token the API
  authenticates with, the database password and the encryption key. A build
  script that printed its environment would otherwise print all three into a log
  the panel stores and shows.
- **`deploy.view` is separate from `deploy.manage`**, and the split is narrower
  than Phase 26's: the manage half is powerful because deploying runs code, and
  the read half is separated because a *build log* prints whatever the build
  printed, which regularly includes a token in a URL. Seeing that a deployment
  failed is support work; reading what it said is not.
- **Automatic deployment is off by default and audited when it is switched on.**
  Turning it on is the moment a person with write access to the repository gains
  the ability to run code on this host without touching the panel. That is the
  point of the feature and it is worth deciding rather than inheriting.
- **Disconnecting a repository leaves the files.** It should stop a site being
  deployed, not take it offline.
- **The log keeps the end, not the beginning.** A build that prints a hundred
  megabytes is a build with a loop in it, and the part worth keeping is what it
  said just before it stopped.

---

## 7. What running it found

Every one of these came from driving a real repository over a real SSH
connection, and none would have failed against a mock.

- **The Agent would not start.** `npm`, `php` and `composer` are already
  allowlisted by the Node.js and PHP phases, and the Agent refuses to build a
  command allowlist with two specs of one name. The deployment tools are now
  prefixed — which is also right on its own terms: changing how long a build may
  run must not change how a customer's Node.js application is started.
- **Every deployment failed at authentication.** The key directory was 0700 and
  root-owned, so the site account could not reach the key inside it. That does
  not surface as a permission error on the file: ssh reports "Identity file not
  accessible" and the failure looks exactly like a key the forge never received.
  The directories are now **0711** — traverse but do not list — so a process can
  open the path it already knows and cannot enumerate anybody else's.
- **The checkout was refused by git.** `reset --hard -- <sha>` reads the `--` as
  a path separator, and git answers "Cannot do hard reset with paths". The
  separator was removed and the comment now says what actually keeps that
  argument safe: it is either validated hexadecimal or a string this package
  built.
- **A failed build recorded nothing.** Returning an error from the Agent
  operation returns no data with it, so the panel recorded the deployment as
  failed and had no log, no failed step and no record that the source had been
  rolled back — all of which were in the result. A build that fails is an
  *outcome*, not a failure of the operation, which is the same distinction cron
  draws about a job that exits non-zero. Operational failures — a repository that
  cannot be reached, an account that resolves to root — are still errors.
- **A route conflict.** `/deployments/{id}` and
  `/deployments/repositories/{id}` both match `/deployments/repositories`, and Go's
  router refuses to guess. Deployment runs moved under `/deployments/runs/{id}`.

---

## 8. Known limitations

- **No release directories, so no atomic swap.** A deployment is visible to
  visitors while it runs. See §4 for why, and the reason is a good one: the
  alternative deletes customers' uploads.
- **Rollback restores the source only.** Not the build output, not a migration.
- **No deployment log streaming.** The page polls while a deployment runs, at a
  few seconds. A true stream would be a new transport for one feature.
- **No per-branch environments, no review apps, no build artefact cache.**
- **The webhook does not verify a forge's source addresses.** A signature is the
  whole of the authentication; an attacker holding the secret can deploy from
  anywhere. The secret is what needs protecting, and the panel says so.
- **`npm run build` gets the site account's environment and nothing else.** A
  build that needs an API key has to read it from the site's own files; there is
  no place in the panel to put a build variable. That is the most likely thing
  somebody will want next.
- **Nothing alerts on a failed deployment.** It is visible on the page, and no
  Phase 20 notification is raised. Along with the mail-server gaps from Phase 26,
  that is the clearest piece of work left.
- **Deployments are not in Phase 14's backup subjects.** The source is in the
  repository, so little is lost; the *deploy keys* are on the host and are not
  backed up, and a rebuilt host needs new ones added to each forge.
