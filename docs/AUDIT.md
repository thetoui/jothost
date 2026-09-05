# The audit trail

What the panel records, and how to read it.

```bash
make docker-test-audit
```

---

## 1. The gap this closes

The trail has been written since Phase 1. Every sensitive action on the host —
creating a website, issuing a certificate, changing the firewall, a failed
login — has landed in `audit_logs` with the actor, the address, the outcome and
the context.

**Nothing could read it.** `audit.view` was defined as a permission in
migration 0001, granted to the admin role, and guarded no endpoint at all. The
panel's answer to "who deleted that website" was to open a connection to
PostgreSQL. An audit log nobody can read is one nobody reads, which is most of
the way to not having one.

It was found in Phase 24, in a way worth keeping: two of that suite's own
checks pointed at `/api/v1/audit` and had been *passing on the 404*. From the
outside a missing route and a refusal look the same if the only thing being
checked is that the request did not succeed. That is what prompted the stricter
`denied()` helper, which refuses to accept 404 as a permission refusal, and the
first thing the new suite establishes is that the endpoint exists at all.

---

## 2. Reading it

```http
GET /api/v1/audit
GET /api/v1/audit/actions
```

Filters: `action`, `user_id`, `resource_type`, `resource_id`, `status`
(`SUCCESS` or `FAILURE`), `since` and `until` as RFC 3339, plus `limit` and
`offset`. In the panel it is **Audit trail**, under Server.

Three decisions in there are worth stating.

**The total is returned alongside the page.** A page of 50 out of 9,318 must
not read as a history of 50 — the count is the answer to the question somebody
arrived with, and the page is just what fits on the screen.

**A page is capped at 200.** This table grows for the life of the installation
and never shrinks; an unbounded read is a way to ask the panel to load its own
history into memory. The applied limit is reported back, so a caller that asked
for 5,000 is told it got 200 rather than being left to infer it.

**The action list is read from the trail, not from the code.** `audit.go`
declares the names Phase 1 records; every phase since appends its own. A filter
built from the constants would offer names nothing ever recorded and hide the
ones that matter.

---

## 3. Who can read it

`audit.view`, and nothing else needs it.

It is deliberately not folded into `server.view`. The trail records who did
what and from which address, across every customer on the host; that is a
different thing to be trusted with from seeing how much disk is left. A
reseller who can see their own sites has no business reading everybody's
history.

Phase 24's route sweep picked up both endpoints on its next run without being
told — 259 routes became 261, and both refused an account holding no
permissions. That is the sweep working the way it was built to: a route that
appears without a guard fails a test the first time anybody runs it.

---

## 4. What it cannot do

There is no way to write, change or remove an entry through the API, and there
is not meant to be. `audit_logs` has carried triggers rejecting UPDATE and
DELETE since migration 0001, "including by a superuser".

The reading side is a separate type from the recording side — `Reader` holds no
method that writes — so that the same statement is made in Go and a later
change cannot casually add a mutation path through the part of the code that
was only ever supposed to look. The integration suite checks POST, PUT, PATCH
and DELETE against the route and expects them not to exist.

---

## 5. Two silences, told apart

An entry can have no actor for two quite different reasons, and the panel says
which:

- **not signed in** — nobody was identified. A failed login against a username
  that does not exist is the common case, and it is exactly the entry somebody
  reading a trail wants to see.
- **account deleted** — there was an actor, and the account was removed
  afterwards. Migration 0001 chose `ON DELETE SET NULL` for this: removing a
  user must not erase what they did. The row survives; the name cannot.

Collapsing those into one blank would lose the distinction between "an unknown
caller did this" and "somebody did this and is gone", which is the more
interesting of the two.

---

## 6. Indexes

Migration 0024 adds two.

`(resource_type, resource_id, created_at DESC)` serves the question an operator
actually asks first — what happened to *this* website. 0001 indexed
`created_at`, `user_id` and `action`, which covers "everything recently",
"everything this person did" and "every time this happened", but a resource
lookup was a sequential scan over the whole history. `created_at DESC` is the
third column rather than a separate index so the filter and the ordering are
answered together instead of sorting the matches afterwards.

A partial index on failures costs almost nothing — they are the rare rows — and
turns "show me what was refused" into a lookup rather than a scan discarding
successes. On the development host that is 862 rows out of 6,459.

The down migration drops the indexes and touches no data. A down migration that
removed history would be the one way to defeat the append-only trigger.

---

## 7. Known limitations

- **No export.** There is no CSV or JSON download, so taking the trail to
  somewhere it can be kept independently means querying the API and saving the
  result yourself. Anyone who needs tamper-evident retention wants it shipped
  off the host anyway, which is the larger version of this gap.
- **No retention policy.** The table grows forever. That is the safe default
  for an audit trail and the wrong one for a disk, and nothing currently warns
  when it gets large.
- **No full-text search.** Filtering is by field. "Find the entry mentioning
  example.test" means filtering by resource or reading pages, because the
  metadata is JSONB with no index on its contents.
- **The panel cannot show a resource's history from that resource's page.** The
  filter exists and the index serves it, but a website's detail page does not
  link to its own trail yet. It is a link and a query parameter.
- **Failed logins are recorded but not summarised.** The trail can show 862
  failures; nothing turns that into "this address has tried 40 times". Phase 18
  is what watches for that, and the two do not talk to each other.
