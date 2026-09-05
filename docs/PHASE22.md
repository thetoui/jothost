# Phase 22 — Multi-Tenant Hierarchy & Subscriptions

Who this server hosts, what they were sold, and what stops them taking more
than that.

---

## 1. Four ideas that are not the same idea

Nearly every mistake available in this phase is a conflation, so the four are
named apart before anything else.

**A tier is not a role.** A role says what somebody may *do*; a tier says whose
accounts they may do it to. An operator and a reseller can hold identical
permissions and still must not see each other's customers, so the hierarchy is
a separate fact on the account, checked separately. Permissions are RBAC's
business and stayed there.

**A plan is not a subscription.** A plan is a promise, edited by whoever sells
it. A subscription is one customer's instance of that promise with add-ons on
top. Editing a plan changes every subscription on it — which is what a plan is
*for*, and is why a subscription cannot be edited into something its plan does
not allow.

**A limit is not a count.** Six of the eight dimensions are counted from rows
this panel wrote, so a limit on one is enforceable at the moment somebody asks
for one more. The other two — disk and bandwidth — are measured on the host
after the event, so a limit on one can only ever be reported. Section 4 is
about that difference, because calling both "hard limits" is a promise the
second kind cannot keep.

**Unlimited is not zero.** A nil limit means no limit; a zero limit means none
at all. A plan with no mailbox limit and a plan that includes no mailboxes are
opposite promises, and a scheme that spelled both `0` could not tell a customer
which one they bought. Every limit column is nullable for this reason, the page
writes "Unlimited" and "None", and the add-on arithmetic reads the two nils
differently depending on which side they are on: unlimited absorbs an add-on,
and an add-on that says nothing about a dimension adds nothing to it.

---

## 2. The hierarchy is a boundary, and it cannot loop

Three tiers: an admin owns the server, a reseller sells space on it, a customer
buys some. An account is created **strictly below** its parent — never at the
same level.

That is the security rule and it is also what makes the tree safe to walk. Every
edge runs from a higher tier to a lower one, so following parents strictly
increases the tier rank and must terminate. There is no cycle detection anywhere
in this phase because a cycle cannot be built: `validate.TierMayOwn` is a
strict comparison, and a reseller who could create a reseller is the only way
one could form.

"What may I see" is one recursive query — `descendantsCTE` — rather than a
parent check, because a reseller's customers are one level down and an admin's
are two, and a scheme that only looked at `parent_user_id` would hide a
reseller's customer from the person who owns the machine.

**A refusal is a 404, not a 403.** Everything outside an actor's subtree
answers "not found", including things that plainly exist. A reseller walking
ids must not be able to tell "no such subscription" from "somebody else's
subscription", because the second answer confirms it exists.

---

## 3. The quota guard, and whose quota is charged

The enforcement is one middleware around the whole router, with the guarded
routes in a single table in `server.go`. That is uniformity rather than
elegance: a quota enforced inside six services is a quota with six chances to be
forgotten by the seventh, and the seventh is always the feature written after
the person who made the rule has moved on. A short visible list is one somebody
adds to.

Two decisions inside it are worth arguing about.

### The routes are matched by a ServeMux, not by comparing strings

The guard holds a second `http.ServeMux` registered with the same patterns.
`POST /api/v1/mail/domains/{id}/mailboxes` is therefore matched by exactly the
code that will route it, so the guard cannot come to disagree with the router
about which requests it covers.

The subtlety underneath that cost a real bug, and section 7 tells it.

### The subscription charged is the *owner's*, not the caller's

This is the decision that makes the whole thing hold. A reseller creating a
database inside a customer's website is spending the customer's plan; an
administrator doing the same is spending the customer's plan. A guard that
looked at who was asking would let every limit be walked around by having
somebody senior press the button — and on this panel that is the ordinary case,
because a customer does not yet have a login (section 6).

So each rule says where the owner is found: a website id in the path, a website
id in the body, a mail domain in the path that leads to a website. Only
`POST /api/v1/websites` falls back to the caller, because a website that does
not exist yet cannot say who owns it — and even there the body may name a
subscription, which is how a reseller creates a site *for* a customer and is
charged for it.

Both fallbacks run in the same direction: towards charging *somebody*, never
towards charging nobody because a lookup was inconvenient. The one deliberate
exception is a website that belongs to no subscription — an administrator's own
site — where there is nothing to charge and falling back to the caller would
bill a reseller's plan for work inside a site that is not in it.

### Smaller decisions

- **A refusal is 409, not 403.** The caller has the right to do this; it is the
  state of the account that prevents it, which is what a conflict is. A 403
  sends somebody to look at permissions that are not the problem.
- **A refusal is audited.** It is the panel telling a paying customer no, and
  the first question afterwards is always "when, and what were they at". Without
  a row the only evidence is a 409 in an access log with no numbers in it.
- **A soft limit sets a header and proceeds.** `X-JotHost-Quota-Warning`, so a
  page can say so without every feature's response growing a quota field.
- **The guard refuses at wiring time to enforce disk or bandwidth.** There is no
  request it could turn away to keep one inside its limit, and a route claiming
  otherwise would be a promise nothing keeps. `NewGuard` returns an error rather
  than accepting one.
- **A body it cannot parse is passed through.** The handler will reject it with
  an error that explains itself; turning that into a quota failure would explain
  nothing.

---

## 4. What a hard limit can and cannot mean

For a counted dimension, "hard" means refused: the request that would be the
fifth mailbox on a four-mailbox plan does not happen.

For disk and bandwidth it cannot mean that. Nothing crosses this panel at the
moment a customer writes a megabyte, so there is no request to refuse. What the
panel does instead is measure, record, and show — and say which kind each
dimension is rather than implying they behave alike.

**Suspension** is the lever for the measured kind, and it is honest about its
reach: a suspended subscription cannot grow — every quota-guarded creation is
refused by name rather than by number — and **it does not take the customer's
websites offline.** That would need the vhost to know about it and nginx to
serve a 503, which is Phase 4's file and not this phase's. It is the clearest
piece of work left and section 9 says so.

---

## 5. Measuring, and the answer that is not a number

Disk is read with `du -sk` rather than a walk in Go. Hard links are why: this
panel's own backups make them, and a walk summing every entry's size counts one
file once per name and tells a customer they are using several times what they
have. `du` also counts blocks rather than apparent size, which is what a disk
quota is about.

Bandwidth is the sum of the response sizes in each site's own access log. Two
details are not incidental:

- The status and size are found from the **closing quote of the request**, not
  by splitting on spaces and taking the tenth field. The request line is the
  part of an access log that a visitor writes, so counting from the left is
  counting from something an attacker chooses — `GET /?x=200 999999 HTTP/1.1`
  is a request anybody can send.
- A log rotation resets the file's total, so the panel keeps the last raw
  reading and accumulates deltas. A reading lower than the last means a
  rotation, and the whole reading is the delta. Without this every rotation
  would hand a customer their allowance back.

**`NULL` means not measured, and it is not zero.** A subscription whose disk
could not be read is not one using no disk, and a page showing "0 B" would tell
a customer they are inside a quota nobody checked. This is the same distinction
Phase 21 draws about outstanding updates and Phase 19 about a probe that timed
out. The one place zero is the truth is a subscription owning no websites: the
panel knows it owns nothing.

Measuring runs on a timer in the panel, alongside Phase 19's metric sampler and
Phase 21's update scheduler — the privileged half stays in the Agent, the
schedule stays somewhere describable. Fifteen minutes, because walking a
hundred sites more often is a panel that spends its life running `du`.

---

## 6. Resource isolation, and what it does not yet do

A plan's CPU, memory and IO caps become a **systemd slice** per subscription:
one unit in `/etc/systemd/system`, rendered from a template, with `CPUQuota=`,
`MemoryMax=` and `IOWeight=` in the spellings systemd understands.

A slice rather than writing to `/sys/fs/cgroup` directly, because systemd owns
the cgroup tree on every host that runs it: two writers and one hierarchy means
the next reconcile silently undoes the panel's work, and the kernel takes no
side. The slice name is derived from the subscription's id **with the dashes
removed**, because systemd reads a dash as a level of nesting —
`jothost-sub-a-b.slice` is a child of `jothost-sub-a.slice`, which would put one
customer's cap inside another's.

Then the honest part.

**Creating a slice does not put anything in it.** Nothing on this host is placed
in a subscription's slice today, so nothing is capped in practice, and the panel
says so in its own words — `placement` is reported as a separate fact from
`isolation_available`, and the page carries a warning when a slice is installed
and empty. A panel that reported one from the other would show a green tick for
a limit that limits nothing.

The state has four values rather than two, and the fourth is the one that earns
its keep:

| state      | what it means                                              |
|------------|------------------------------------------------------------|
| `none`     | this plan caps nothing, so there is no slice                |
| `applied`  | the unit is installed and systemd has read it               |
| `declared` | the limits are written and this host is not applying them   |
| `failed`   | the host refused, and the detail says why                   |

`declared` is what a host without systemd gets. The unit is still written, so a
host that gains systemd later finds the limits already there — but nothing is
enforced, and one word says that.

**What it would take to finish.** PHP-FPM is one service for every site, so its
pools cannot join a per-subscription slice without per-site FPM instances, which
is a change to Phase 5's shape. Node.js applications are the easy half — each is
already its own unit, and a `Slice=` line would place it — and that is a change
to Phase 9's payload rather than to this package. Both are named in section 9
rather than left for somebody to discover from a cap that never bit.

---

## 7. Impersonation

The panel's answer to a support call, and its whole design comes from one
problem: **the moment somebody uses the panel as somebody else, every audit row
afterwards has the wrong name on it.**

So the record is written first, in a table of its own; the actor's id rides on
the session's access tokens; and both facts reach the audit trail — whose
account acted, and who made it act. An impersonation that recorded only one of
those would be worse than none, because it would look like evidence.

Every other rule is a refusal:

- **Only downwards**, strictly: the same tier check every other operation makes.
- **No nesting.** An impersonated session cannot impersonate. The second hop's
  record would name the first hop's subject as the actor, and the chain back to
  a person breaks exactly where it matters.
- **Nothing gained.** The session carries the subject's permissions minus the
  tenancy ones and `user.manage`. The last is deliberate: `user.manage` is the
  ability to change a password, and an operator who needs to see a customer's
  panel does not need to take the account over permanently. That difference is
  the reason impersonation exists rather than a password reset.
- **It ends, and it ends now.** Ending revokes the session *and* drops the
  access tokens, so "stop impersonating" stops it rather than leaving a working
  token in a browser tab for the rest of its life. The session is capped at an
  hour: an impersonation is a few minutes of looking at a problem, and a refresh
  token good for a fortnight would be a standing key to a customer's account.
- **An impersonation nothing recorded does not happen.** If the record cannot be
  written, the session that was just minted is torn down again.
- **A restart closes them.** The session went with the process and nothing would
  ever write the end time, so a panel that did not sweep at startup would show
  an operator inside a customer's account for ever.

The one endpoint with no permission on it is the one that *ends* an
impersonation — a session stripped of `tenant.impersonate` has to be able to
stop being one. And it takes the session from its own claims, never from a
request body, because a body would make it an endpoint for logging somebody else
out.

In the browser, the operator's own tokens are stashed in `sessionStorage` — it
dies with the tab, which is the right lifetime for "put me back afterwards" —
and the whole query cache is cleared on both hops, because every cached answer
belongs to a different account.

---

## 8. What running it found

Four defects, three of them mine, and none would have failed against a mock.

- **An account that had done anything could never be deleted.** Migration 0001
  gave `audit_logs.user_id` an `ON DELETE SET NULL` "so removing a user never
  erases history", and separately made the table append-only with a trigger
  refusing every UPDATE. Both are right; together they are impossible, because
  the cascade *is* an UPDATE. Deleting a reseller failed with an internal error
  naming a trigger — a message nobody can act on. Resolved in the direction the
  append-only rule wants: the foreign key is dropped, and a row saying "user X
  did this" is never rewritten to "somebody did this" because X was later
  removed. That was the edit the trigger existed to prevent, and a foreign key
  was quietly performing it.

- **The guard read an empty path value, and failed silently.** Asking a mux
  which handler it *would* pick answers the pattern question and leaves
  `PathValue` empty. So `{id}` read `""`, the lookup found nothing, and the
  guard charged the caller instead of the owner — which for an administrator
  means charging nobody. The limit stopped binding and nothing said so; what
  surfaced was an unrelated internal error about an empty UUID on a mailbox
  creation. The guard now *serves* the request through its matcher, whose
  handlers hand back the routed request with its path values filled in by the
  same code the real router uses.

- **An id read with `sed` was the wrong id.** `s/.*"id":"\([^"]*\)".*/\1/` looks
  like it reads the first id and reads the last, because the leading `.*` is
  greedy. On a create response — which carries the website and then the job that
  provisions it — that silently returned the job's id, and every check afterwards
  was about the wrong object. It is in the test harness rather than the product,
  and it was making checks pass that should not have.

- **A measurement of a directory that did not exist yet.** Creating a website
  returns immediately with a queued job; the directory only appears once the
  worker has run.

Also found, and **not** this phase's to fix: a wildcard subdomain cannot be
created while the host is in the Phase 4.5 hybrid arrangement, because Apache
refuses a wildcard `ServerName` and wants `ServerAlias`. It is a Phase 4.1/4.5
interaction, it predates this work, and phase discipline says it is not repaired
here.

---

## 9. Known limitations

- **Suspension does not take a site offline.** It stops a subscription growing.
  Serving a 503 for a suspended site needs the vhost template to know about it,
  which is Phase 4's file. This is the clearest piece of work left.
- **Nothing is placed in a subscription's slice**, so no CPU or memory cap binds
  in practice. Section 6 names what each service would need. The panel reports
  this rather than implying otherwise.
- **A customer account has no panel role.** This phase scopes tenancy, not the
  resource listings: a role granted `website.view` today would show a customer
  every website on the host. So a customer owns a subscription and does not get
  a login that browses the panel, and a reseller reaches a customer's hosting by
  impersonating them — which is recorded every time. **Per-resource read scoping
  is the phase this one is waiting on**, and it is the largest single thing
  outstanding: websites, databases, mail, FTP, cron, deployments and — hardest —
  the file manager's roots.
- **A reseller holds no hosting permissions either**, for the same reason.
- **Bandwidth is sampled, so a rotation between samples loses what was served in
  between.** The reading is a floor, and a very long access log is read only up
  to a cap, which is reported rather than hidden.
- **Disk is a level, not a history.** There is no record of what a subscription
  used last month; the page shows what it uses now and when that was measured.
- **No billing, no invoices, no prices.** A plan describes limits, not money.
  PRD.md section 3 lists billing as a non-goal and this phase keeps to that.
- **A subscription's quota is the first one when an account owns several.** The
  panel enforces the first limit that refuses, so spare capacity in one cannot
  be spent on another; it does not let somebody choose which to spend.
- **Moving a website between subscriptions is manual** and is not itself
  quota-checked: an operator can move a site into a subscription that is already
  full. The page shows it as over its limit, plainly, rather than refusing a
  move somebody has a reason for.
- **Tenancy is not in Phase 14's backup subjects.** It is all in the control
  plane database, which is backed up as a whole, so nothing is lost — but there
  is no per-tenant export.
