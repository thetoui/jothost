# Site directory ownership

Why a deleted website's directory could end up belonging to a different
customer, and what now stops it.

```bash
make docker-test-site-ownership
```

---

## 1. The defect

Four steps, none of them wrong on its own:

1. A website is deleted. **Its files are kept** — deliberate, and documented
   since Phase 4: a vhost can be recreated, content cannot.
2. Its system account is removed. That is also deliberate, and it puts the
   account's uid back into the allocation pool.
3. The files still carry that uid. Nothing on the host records that the number
   used to mean something.
4. Sites keep being created. The pool comes round. One of them is issued the
   number written on the old files, and from that moment the new site's account
   owns the previous customer's directory.

Nothing announces any of this. The panel's records are correct throughout — the
old website is gone, the new one is fine — because the panel never looks at who
owns a directory it did not just create. On the development host it had already
happened to **52 directories**, and `/var/www/p10-t.test` belonged to
`web_ftpdemo_test_cb26a2`, an account for an entirely different site.

It surfaced from the far end. Phase 7.1's FTP checks failed on an assertion
about who owned an uploaded file — a test that had nothing to do with website
deletion, reporting a fact that could only be explained by it.

### How bad

Bounded, and real. It needs a website deleted and enough accounts created
afterwards to recycle the uid, so it is not reachable on demand by an attacker.
But the outcome is a customer holding read and write access to another
customer's document root — including whatever was in `config.php` — with no
event anywhere saying so.

---

## 2. What the code actually said

Both halves were written down as decisions. Neither was wrong by itself, and
together they were the bug.

The Agent's delete request:

> `RemoveFiles` … defaults to false because a deleted vhost can be recreated,
> whereas deleted content cannot.

And provisioning:

> It is idempotent: an existing directory is re-owned and re-permissioned
> rather than treated as an error, so a retried job converges instead of
> failing on its second attempt.

Idempotency for a retry is a good property. "Re-owned rather than treated as an
error" is also how a leftover directory is adopted by whoever asks next.

The clearest evidence that this was understood and mis-diagnosed is in
`WritePlaceholder`, which had a comment explaining the symptom as a reason to
make it worse:

> A directory left behind by a previous site holds files owned by an account
> that no longer exists, and nginx cannot read them — so the new site returns
> 403 from the moment it is created, which is precisely the outcome this
> function exists to prevent.

That is the bug, described accurately, and answered by chowning the leftover to
the new owner.

---

## 3. The fix

### 3.1 A retained tree stops belonging to the account

`Provisioner.Neutralize` gives the whole tree to **root** and closes it to
everyone else, and `Manager.Delete` calls it whenever the files are kept and
the account is not. Root because root is the one uid the system will never
issue again; every other number is in a pool.

The files survive, which is the entire point of keeping them. What does not
survive is the claim on them.

Two details that matter more than they look:

- **It runs before the account is removed**, and a failure is fatal to the
  delete. Carrying on would free the uid anyway, which is the exact outcome
  this exists to prevent. A website that needs a retry is a far smaller problem
  than a directory that quietly changes hands.
- **`Lchown`, and no symlink is followed.** This walks a tree the customer
  controlled, as root. A recursive chown that followed links would hand
  `/etc/shadow` to whatever it was reassigning. There is a test that does
  exactly that and checks the file outside is untouched.

This is the same rule the delete path already followed for everything else —
the FPM pools, the crontab, the certificate are all dealt with "while its
account still exists". The filesystem was the one place it was not.

### 3.2 Provisioning refuses to adopt

`Provision` now refuses a directory that already holds files belonging to
another account, rather than taking it over.

The refusal is narrow on purpose, because the obvious version of it breaks the
retry path that made `Provision` idempotent:

| Directory | Result |
|---|---|
| Does not exist | Created, as before |
| Exists, empty | Adopted — there is no content to hand over |
| Exists, owned by the account being provisioned | Adopted — this is a retry converging |
| Exists, holds another account's files | **Refused**, naming the path and the owning uid |

Each of the three directories is judged on its own contents. The site root is
judged without counting `public` and `logs`, which it holds by definition — so
a refusal names the directory that actually holds somebody's files rather than
the one above it.

### 3.3 Deleting the files is now possible

The first two changes together create a dead end: files are kept, and
provisioning refuses them, so a domain could never be recreated through the
panel at all.

`DELETE /websites/:id?remove_files=true` closes it. The default is unchanged —
files are kept, because that default was deliberate and remains right — but the
choice is now offered instead of being made silently in one direction. The
Agent has always supported it; only the API never asked.

---

## 4. Hosts that already have the problem

The fix prevents new orphans. It does nothing for a machine that already has
them, and a host upgraded into this build will still be carrying whatever it
accumulated. So the Agent says so.

**At every start**, it audits the site root and warns:

```
site directories are owned by accounts that no longer exist  count=52
site directories are owned by another site's account         count=35
```

**`jothost-agent -repair-site-ownership`** reassigns the first kind and lists
the second. It runs before the Agent starts and without it: this changes
ownership under the site root, which is not something to do behind a running
server's back while it is provisioning into the same directories.

### Why only the first kind is repaired

The two findings are not equally certain, and the difference decides what the
panel is allowed to do on its own.

A directory owned by a uid **no account holds** is unambiguous. Nothing
explains it and nothing can justify it.

A directory owned by an account whose home is a *different* site's root is
usually the same thing one step later — the uid was already recycled. But it is
also exactly what **a subdomain that shares its parent's account** looks like,
which is legitimate and common. From the disk alone those two are identical.
Only the panel knows which sites are live and which of them share an account.

So the misowned ones are listed and left alone. Guessing would mean reassigning
a live subdomain's directory and taking a working site off the air — trading a
bounded, conditional disclosure for a certain outage.

On the development host the sweep repaired 52 and left 35 for review, touching
neither the live sites nor the four root-owned directories nginx's own package
installs under `/var/www`.

---

## 5. Found on the way

**Every failed job reported "Operation failed".** Verifying that the refusal
told an operator something useful showed that it did not: the Agent's
synchronous path passed a handler's structured message straight through, and
the asynchronous one replaced it with a generic string. Every website
operation goes through a job, so *every* job failure in the panel's history has
been unexplained — the reason was in the Agent's log and nowhere a user could
reach.

The generic message was deliberate ("the caller receives a structured code
only"), and the intent was right: an unexpected error's text can carry paths
and configuration. But a handler's own `Fail(code, message, cause)` is written
for the person who asked, carries no internal detail, and was already trusted
on the other path. Now both paths agree: a structured error is passed through,
anything else is still flattened and its cause logged.

---

## 6. Tests

`agent/internal/sites/filesystem_test.go` — the package had no tests before
this. Seven, including the reported cycle end to end: provision, delete with
the files kept, hand the uid to the next site, and require the refusal. Each
skips unless running as root, because they are about who owns a file.

`tests/integration/site_ownership.sh` — the same cycle through the panel's own
API, then looking at the filesystem to see what it actually did. Every part of
this bug was invisible from inside the panel, which is why it lasted, so the
checks refuse to take the panel's word for anything.

Both were verified by putting the defect back: with `Neutralize` stubbed out
and the refusal removed, the tests report the original symptom in the original
words — `config.php still carries the freed uid`, and `a new site must not be
provisioned into the retained directory, got <nil>`.

**Phase 7.1's FTP suite, the one that surfaced this, now passes.**

---

## 7. Known limitations

- **Recreating a website on the same document root now fails** unless the old
  files are removed first. That is the intended refusal, and it is a real
  change in behaviour for anyone who relied on the old adoption. The error
  names the directory and says what to do; `?remove_files=true` on the delete
  avoids it entirely.
- **The panel's UI does not offer "delete the files too".** The API takes the
  parameter and the Agent has always supported it, but the websites page still
  sends a plain delete, so through the interface the only way to clear a
  directory is a shell. That is the next piece of work and it is small.
- **Misowned directories are reported, never repaired.** Section 4 explains
  why. An operator still has to look at 35 directories by hand on the
  development host, and the panel could narrow that list for them — it knows
  which sites are live and which share an account — but that means a
  panel-side reconciliation this change did not build.
- **The audit only looks one level down.** It checks who owns each site
  directory, not every file inside it. A tree whose top is correct but which
  holds files owned by a stale uid underneath would not be reported. Deletion
  reassigns the whole tree, so this only affects damage done before the fix.
- **Nothing detects the moment a uid is reused.** The protection is that
  nothing carries a freed uid, not that reuse is noticed. If a directory
  escaped neutralisation some other way, it would still change hands silently.
