# Phase 4.1 — Subdomain Manager

What this phase adds, why a subdomain is a website rather than a new kind of
thing, what the live checks found, and what is deliberately not here.

---

## 1. What it does

A website can host sites beneath it: `shop.example.com`, `dev.shop.example.com`,
or `*.example.com` as a catch-all. Each gets its own document root, its own
vhost, its own logs, and — if you want it — its own system account and its own
PHP pool. Each is served by nginx on its own name.

---

## 2. The decision this phase rests on

**A subdomain is a row in `websites` with a parent, not a separate table.**

A subdomain needs a document root, a system account, a PHP version, a
certificate, logs, a file manager, a database, possibly a Node application.
That is the list of things a website has. A separate `subdomains` table would
duplicate every one of them, and every feature already built would have to
learn that a site is sometimes one thing and sometimes another — the file
manager, the SSL panel, the PHP panel, the database assignment, the Node
manager, each with its own branch and its own way of getting it wrong.

As a website row it inherits all of them unchanged. Nothing in Phases 5 to 9
was touched to make a subdomain work with SSL, PHP, files, databases or Node;
they key on a website id, and a subdomain has one.

What it costs, stated plainly: `websites` no longer means "top-level site", so
anything listing sites has to say whether it wants subdomains too. That is one
explicit clause per query — `include_subdomains` on the listing, and the
`parent_website_id IS NULL` default beneath it — against a duplicate of the
entire feature set.

---

## 3. Design decisions

### 3.1 The name is a label, not a hostname

`POST /websites/:id/subdomains` takes `"shop"`, and the full name is derived
from the parent's own record. A caller cannot name a site under a domain the
parent does not own, because it never supplies the parent half at all.

The label may contain dots, which is how `dev.shop.example.com` is created —
as one subdomain of the top-level site, not a subdomain of a subdomain. One
level is the whole model, and it is enforced by a database trigger as well as
in Go: the rule is about another row, which a CHECK constraint cannot express.
Allowing a chain would mean every listing, every deletion and every path
computation has to walk it, and give a different answer if it is ever cyclic.

### 3.2 Nested means beside the parent's content, never inside it

A nested subdomain's files live at `/var/www/example.com/shop.example.com/`,
alongside the parent's `public` and `logs` rather than within them.

Inside would mean the parent serves the subdomain's files at its own URLs, so
`https://example.com/shop/config.php` would hand the subdomain's database
password to anyone who guessed the path. The integration suite checks the
directory is *not* under the parent's document root, because "nested" is a
word that invites exactly that mistake.

Isolated puts it at `/var/www/shop.example.com/` instead, which is what an
operator wants when a subdomain is really a separate site that happens to share
a name.

### 3.3 The account and the pool move together

Three choices are offered — where the files live, who owns them, whose PHP pool
serves them — and one combination is refused: **its own account with the
parent's pool**. The parent's pool runs as the parent's user, so PHP would run
as one account over files owned by another. Every write fails, and it reads as
a broken application rather than a bad choice made at creation.

The API refuses it, the database refuses it, and the form moves the pool with
the account so nobody discovers the rule by being rejected.

Inheriting is the default for both, because a subdomain usually belongs to the
same customer as its parent: the parent's file manager reaches it, and no
second pool is spawned to serve a site that gets ten requests a day.

### 3.4 A wildcard lives in the server_name and nowhere else

`*.example.com` answers for every name beneath the parent that no other site
claims. That is nginx's own matching order — exact names first, then wildcards
— so an existing `shop.example.com` keeps working the moment a wildcard is
added. The suite checks that, because the alternative would be a catch-all
quietly taking over every subdomain already hosted.

The asterisk never reaches the filesystem. Its directory is
`/var/www/example.com/_wildcard.example.com/` and its vhost is
`_wildcard.example.com.conf`, because `*` is a glob to every tool that later
walks those directories — a backup, an archive, an rsync, a shell loop over
`*.conf`.

Only the leading form is written. nginx also accepts `www.*` and
`.example.com`; each additional shape is another thing that has to agree with
the certificate, the DNS record, and the panel's idea of which site answers a
request.

### 3.5 Deleting a parent with subdomains is refused

The database would cascade the rows, but the rows are not the sites: each
subdomain has a vhost and possibly an account on the host, and nothing would be
queued to remove them. The panel would forget about names nginx is still
serving — and with a nested layout, the parent's deletion takes the subdomain's
files while its configuration stays live, which is a 403 for a site nobody can
find in the panel any more.

So it is refused, and the refusal names what is in the way.

### 3.6 An inherited account is never removed with the subdomain

Deleting a subdomain that shares its parent's account must not take that
account. Doing so would leave the parent's files owned by a user that no longer
exists and the parent returning 403 to every visitor — an outage on a site
nobody touched. `remove_user` is set from the subdomain's own ownership mode,
and both directions are tested.

---

## 4. The dependency this phase had to fix first

**The Agent renders a vhost from the payload it is given, and does not merge
with what is already there.** That is deliberate and correct: a configuration
assembled from the panel's record converges on the same file however many times
it is applied.

The consequence is that any payload omitting a fact turns that fact off. Three
packages were each building their own payload from the fields they happened to
care about, and every gap between them was a live defect:

| Doing this | …silently did this |
| --- | --- |
| adding an alias to a PHP site | turned PHP off — the site served its source |
| adding an alias to a Node site | stopped the reverse proxy |
| adding an alias to an HTTPS site | dropped it back to plain HTTP |
| choosing a PHP version on an HTTPS site | dropped the certificate |
| issuing a certificate on a Node site | stopped the reverse proxy |
| starting an application on an HTTPS site | dropped the certificate |

None of these were reachable from the subdomain work alone, but every one of
them was reachable, and Phase 4.1 multiplies the combinations rather than
adding to them. So the desired state is now assembled in one place —
`websites.Repository.VhostPayload` — from the panel's record, and every caller
starts from it and overrides only what its own operation decides.

The resolvers live behind interfaces (`PHPPoolSource`, `CertificateSource`,
`ProxySource`) implemented in the server package, because php, ssl and node all
import websites: depending on them directly would be a cycle the compiler
refuses.

A related refusal came out of it: **PHP cannot be enabled on a site served by a
Node application**. A vhost carrying both would send some URLs to FPM and the
rest to the application depending on the path. The renderer already refused
that configuration; now the API refuses it first, with a message instead of a
job that fails on the host.

---

## 5. What the live checks found

**1. `website.create` and `website.update` rejected `website_id`.** The Agent
decodes with unknown fields refused — which is right — and every other
vhost-writing operation carries the id while these two did not. The one payload
builder was unusable for the two operations that matter most. The field is now
accepted and logged.

**2. `ssl.issue`, `ssl.renew` and `ssl.revoke` rejected `system_user`**, for
the same reason, and had no `proxy_port` at all: issuing a certificate on a
Node site would have taken it off its own application.

**3. `website.php.set` rejected `php_socket`** — it has `socket_path`, the pool
it is about to write. Two answers to one question; the API now sends only the
one the operation acts on.

**4. Deleting a wildcard site warned about an invalid certificate domain** on
every deletion. A wildcard cannot have a certificate here — that needs a DNS
challenge, which this panel does not do — so it is skipped rather than
attempted and logged. A warning that always appears is a warning nobody reads.

And one in the suite itself: the vhost directory was assumed to be
`/etc/nginx/http.d`, where this host uses `conf.d`. The checks looking at the
parent's vhost were reading an empty file and passing for the wrong reason, so
that read is now asserted before anything is concluded from it.

---

## 6. Verification

```bash
make docker-test-subdomains
```

56 checks against the running stack, from inside the agent container so it can
see the vhosts, the directories and the accounts.

What it verifies beyond status codes:

- a nested subdomain's files sit inside the parent's directory and **outside**
  its document root
- it has its own logs, its own vhost, and the parent's vhost does not also
  claim the name
- it is served on its own name, and the parent still serves its own content
- an isolated subdomain gets its own directory, its own account exists on the
  host, and its files belong to it rather than to the parent
- a wildcard catches a name nothing claims, an exact subdomain still beats it,
  and the parent is not caught by its own wildcard
- the wildcard never reaches the filesystem or a vhost filename
- a subdomain of a subdomain is refused, while a dotted label creates the same
  name under the top-level site
- unusable names are refused; an already-hosted name is a conflict
- deleting a parent with subdomains is refused, and a top-level site cannot be
  deleted through the subdomain endpoint
- removing a subdomain keeps the shared account and leaves the parent serving,
  while a subdomain with its own account takes it with it

Every earlier suite still passes, which is what the payload work above had to
be measured against.

---

## 7. What is not here, and why

Two items in the Phase 4.1 task list belong to phases that do not exist yet.
Building them here would mean building those phases:

- **Automatic DNS record injection into the parent's zone.** ~~There is no
  zone.~~ **Done in Phase 13**, and it was what this note predicted: a small
  addition to subdomain creation. It still does nothing when the panel does not
  serve the parent's zone, for the reason given here — a subdomain is reached
  through whatever already resolves the parent, so DNS from this panel is a
  convenience and never a requirement. See docs/PHASE13.md section 11.
- **Apache VirtualHost generation.** There is no Apache provider. The hybrid
  nginx + Apache engine is Phase 4.5; this phase writes the nginx vhost, which
  is what the host actually runs today.

Both are noted in `TASKS.md` rather than quietly dropped.

Other limitations:

- **One level of nesting.** A deeper name is created as a subdomain of the
  top-level site with a dotted label, which produces the same hostname.
- **No wildcard certificate.** HTTPS on a wildcard subdomain needs a DNS-01
  challenge; Phase 6 does HTTP-01. A wildcard site is served over plain HTTP.
- **No moving between layouts.** Changing a subdomain from nested to isolated
  means creating it again: the alternative is moving a customer's files with a
  panel operation, which is a restore, not an edit.
- **The parent must be provisioned first.** A subdomain cannot be added to a
  site that is still being created — there is no directory for a nested one to
  live in yet.
