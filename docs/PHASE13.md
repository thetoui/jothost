# Phase 13 — DNS & Local Name Server

What this phase adds, the five things it got wrong first, and — stated plainly —
what it will not do.

---

## 1. What it does

The panel runs an authoritative name server. It writes zones, publishes records
in them, signs them, replicates them to other servers, and can push the same
records to Cloudflare.

It is the first phase whose output is not a file on this host or a process on
it. Everything else the panel does is undone by fixing this machine; a DNS
change is a statement to the whole internet, cached by resolvers the operator
has no access to, and it stays wrong for as long as its TTL says.

---

## 2. Why BIND

The task list offers BIND or PowerDNS. The deciding difference is not features
but where a zone lives.

PowerDNS keeps zones in a SQL database of its own. Adopting it would mean a
second database beside the panel's, with its own schema, migrations and backup
story, and no file an operator can read to see what their name server is
actually serving. BIND keeps zones in files — which is also what makes every
claim in this phase checkable: by the panel, by `named-checkzone`, and by
whoever has to debug it at three in the morning.

The other reason is DNSSEC. `dnssec-policy` (BIND 9.16 and later) makes the
*server* responsible for generating keys, signing, and rolling keys over on
schedule. A panel doing that from outside would be a cron job racing a daemon
over a set of key files, and a key rollover done wrong takes a domain off the
internet for as long as the old signatures are cached.

---

## 3. What the panel owns, and what it does not

Two things, and deliberately not a third:

- **The zone files.** One per zone, entirely generated, in a directory of the
  panel's own making.
- **One include file** of zone statements, which `named.conf` includes.

It does **not** own `named.conf`. If the host has none — Alpine ships two
samples and no live file — the panel writes one, because a name server with no
configuration is not a name server, and it then keeps that file in step with the
settings. If the host already has one, the panel appends a single `include` line
and changes nothing else.

Global settings in somebody else's `named.conf` are read and **reported**, never
rewritten. An operator who set `listen-on` to one address did so for a reason,
and a panel that silently widened it would have opened a name server to the
internet as a side effect of adding a record.

---

## 4. The five things this got wrong first

All five were found by running it against a real server. Four would have passed
a test that only read back the file the panel wrote.

### The zone file's header was not a comment

The first line said `# Managed by JotHost Panel`. A zone file's comment
character is `;`. named read the line as a resource record and refused every
zone the panel produced:

```text
/var/bind/jothost/.candidate-example.test.zone:1: unknown RR type 'Managed'
```

A name server that will not start, from a file that looked perfectly reasonable.

### A zone whose name servers are inside it had no glue

`ns1.example.com` inside `example.com` is a chicken-and-egg problem: to find
ns1 a resolver must ask example.com's name server, which is ns1. The address
record in the zone itself breaks the loop, and without it the zone is not merely
incomplete — `named-checkzone` refuses to load it:

```text
zone example.test/IN: NS 'ns1.example.test' has no address records (A or AAAA)
```

The panel now writes that glue when a zone's name servers are inside it, and
refuses the zone with a plain explanation when it does not know this host's
address. Name servers *outside* the zone get none: their addresses are somebody
else's zone's business.

### Nothing had ever filled the servers table's address columns

Which is what the glue needed. `ipv4` and `ipv6` have been in the schema since
Phase 3 and no code ever wrote to them. The Agent now reports the host's
addresses — from the kernel's own interface list, not by running `ip` — and
registration stores them. Written up in section 8.

### A zone that failed to finish kept its name

The row was created, the records failed, and the row stayed. The operator's next
attempt was then refused as a duplicate of something that had never worked.
Everything after the row exists now rolls the row back.

### The rewritten named.conf was not readable by named

`named.conf` is installed through a temporary file and a rename, so the new file
belonged to root while named runs as its own account. named logged

```text
reloading configuration failed: permission denied
```

and went on serving the configuration it already had — while `rndc` reported
success and the panel reported the change as applied.

Two things came out of that. The file is given back to the name server's account
every time it is written; and after every reload the panel asks the server
whether it is really serving the zones that changed, because rndc's exit status
says the command was *accepted*, not that the reload worked.

---

## 5. The serial nobody should compare

With inline signing named maintains its own serial on the signed copy of a zone,
and it diverges from the one in the file. Measured, not assumed: a file at
serial 2026090302 was being served as 2026090304 within seconds of loading.

Every part of this phase that reads a serial back therefore reports it as the
*served* serial, separately from the panel's own, and nothing treats a
difference between them as drift. A panel that compared them would report every
signed zone as broken, forever.

The panel's own serial advances to the greater of "one more than the current
one" and the current unix time. The time keeps it meaningful to a human reading
a `dig` output; the increment covers two changes in one second, which the
YYYYMMDDnn convention also handles until the hundredth edit of a day.

---

## 6. DNSSEC, and the one step nobody can automate

named generates the keys, keeps the zone signed as records change, and rolls the
keys over on its own schedule. The panel turns that on for a zone and reports
what named has.

What it cannot do is put the **DS record** in the parent zone. Until that record
is there, no resolver knows to check the signatures — a zone can be signed
perfectly and mean nothing. The parent belongs to a registrar this panel has no
account with, so the DS record is shown, in the form a registrar's form wants,
to be copied.

That is the whole reason DNSSEC has a page in a control panel rather than being
a switch nobody sees.

---

## 7. Why the firewall is reported and not changed

Port 53 has to be open or the zone is invisible. The panel checks and says so;
it does not open it.

A firewall change is its own deliberate act with its own protocol — back up,
validate, apply, verify connectivity, commit, roll back on failure (CLAUDE.md
section 19) — and doing one as a side effect of adding a DNS record would open a
port without the operator seeing which, and without the audit trail recording
that a firewall change had happened.

Saying it is worth the trouble here because of how this fails: the panel's own
checks pass, `dig` from the server itself answers perfectly, and the zone is
simply invisible from the internet. There is no error anywhere to find.

This is the same boundary Phases 17 and 7.1 drew.

---

## 8. A dependency this phase had to build

Phase 3 gave the `servers` table `ipv4` and `ipv6` columns and nothing ever
filled them. This phase needs an address — for a zone's seeded records, for the
glue that makes a zone loadable at all, and for a subdomain's record in its
parent's zone.

So `system.info` now reports the host's addresses, read from the kernel's own
interface list rather than by running `ip addr` — which is the rule that
collector already followed: there is no program to execute and no output format
to break. Registration stores them.

"First global unicast address of each family" is the honest limitation. A host
with several public addresses has no way to say which is canonical — that is a
policy question, not a fact about the machine — so the first the kernel lists is
what is reported, and an operator whose zone should name a different one edits
the record.

This is CLAUDE.md section 21's rule followed: identify the missing dependency,
explain it, build the smallest version that works, write it down.

---

## 9. What the panel refuses

- **A record type it cannot check.** Nine are supported; each has its own rdata
  grammar, and a type the panel cannot check is a line it writes into a file a
  root process parses without knowing what it says.
- **An AAAA record holding an IPv4 address**, and the reverse. Both parse, both
  are written, and both resolve for nobody.
- **A CNAME sharing a name with another record**, and a CNAME at the apex.
  named-checkzone refuses the zone for this, so without the check the failure
  would arrive as a rejected file naming a line number rather than the record
  that was just added.
- **A misspelt CAA tag.** "issued" is not an error in DNS; it is a certificate
  authority policy that silently authorises nobody.
- **A TTL under a minute or over a week.** The floor stops a typo producing a
  record every resolver re-asks for continuously; the ceiling stops one being
  cached past the point anybody can fix it.
- **A retry longer than the refresh.** Each value is in range on its own, and
  together they are a zone that heals more slowly the more it breaks.
- **A reverse zone for a network that cannot have one.** A /25 has a reverse
  zone only as an RFC 2317 delegation the address's owner has to make, so
  generating one would produce a zone that is correct, served, and never asked.
- **A PTR for an address outside its zone.** Invisible until somebody's mail is
  rejected.
- **A secondary zone's records.** They arrive by transfer and are replaced at
  the next one, so an edit here would silently revert.
- **Signing on a server that cannot sign.** A zone recorded as signed on a
  server with no `dnssec-policy` is one the panel reports as protected and that
  is served with no signatures at all.
- **A zone with no name servers.** The panel will not invent them: it cannot
  know what an operator's are called, and a zone delegated to nothing is one no
  resolver can be sent to.

---

## 10. Publishing to Cloudflare

A remote provider is a second place to put the same records. The sync is one-way
— from the panel outwards — and it is a deliberate act rather than something
that happens on every edit: a push that ran automatically would turn a typo into
a change at a provider the operator was not looking at.

**Deleting is opt-in.** A Cloudflare zone usually holds records this panel never
knew about: a verification TXT added in their dashboard, an email provider's
setup, a page rule's helper record. A sync that removed everything it did not
recognise would break them silently. "Make it match exactly" is a real thing to
want, so prune exists; "delete what I do not recognise" is a bad default for
somebody else's DNS, so it is off.

The API token is encrypted with the panel's `ENCRYPTION_KEY`, against the
provider row's own id, and is never returned by any endpoint. It cannot be
hashed the way a password is — it has to be sent to Cloudflare on every call —
and it is a credential that can rewrite every DNS record in somebody's account.

---

## 11. Phase 4.1's deferred item, paid off

`docs/PHASE4.1.md` section 7 left "automatic DNS record injection into the
parent domain's zone" for this phase, on the grounds that there was no zone to
put a record in. There is now, and it is what that note predicted: a small
addition to subdomain creation.

It does nothing when the panel does not serve the parent's zone, which is the
common case — a subdomain is reached through whatever already resolves the
parent — so a panel that failed subdomain creation because it does not host the
parent's DNS would be refusing to do something it was never asked to do. The
record it writes is marked as the panel's, is shown and not editable, and goes
when the subdomain does. A record an operator added at the same name is theirs
and survives.

---

## 12. Where it meets the other phases

- **Services (12).** named is in the service catalogue, so it is started,
  stopped and enabled at boot from the Services page like every other daemon.
  The DNS page writes zones and asks for a reload; it does not own the lifecycle.
- **Firewall (16).** Reported, not changed. See section 7.
- **Websites (4) and subdomains (4.1).** A zone can belong to a site, and a
  subdomain publishes itself in its parent's zone. Deleting a website does *not*
  delete its zone: the names in it may point at other hosts, mail included, and
  unpublishing a customer's MX records because a vhost was removed would take
  their mail down with the site.
- **Mail (26), which this unblocks.** DKIM, SPF and DMARC are TXT records under
  service labels, which is why `_dmarc` and `_domainkey` names are accepted and
  why a TXT value longer than 255 bytes is split rather than refused.

---

## 13. What the tests prove

**Go tests** (`agent/internal/dns`, 26 tests) run against recording stubs whose
output is copied from a live BIND 9.18.49. `rndc dnssec -status` and `rndc
zonestatus` print text meant for a person and are the only interface BIND offers
for those questions, so the parser is the risk — and a stub printing what I
imagined would test my imagination, which is how Phase 7.1 shipped a session
monitor that silently dropped every session. One frozen sample exists purely
because reality surprised me: zonestatus reports two serials, and they differ.

**API tests** (`api/internal/dns`) drive the Cloudflare client against a stub
that answers like the real API, and check what it *sends*: a record this panel
formats wrongly is a record that silently becomes something else in somebody's
live DNS.

**Integration** (`tests/integration/phase13_dns.sh`, 59 checks) runs inside the
Agent's container against a real BIND:

- a zone created through the API is answered for by the running server
- its records resolve — A, MX, TXT, SRV with all three numbers, CAA, PTR
- a TXT value of 400 characters comes back as one reassembled string
- the serial advances when a record changes, so secondaries will actually pull
- switching signing on produces a DNSKEY, signatures on answers, and the DS
  record for the registrar
- a transfer is refused until an address is allowed, and then succeeds
- **a second name server** is started with a zone the panel has never seen, the
  panel is given a secondary zone pointing at it, and the panel's server then
  answers with data it can only have transferred
- a reverse zone is named after its network and its PTR resolves
- a deleted zone stops being answered, and its file, journal and signatures go
- and every refusal, each with the reason it exists

```bash
make docker-test-dns
```

**Frontend** (`DnsPage.test.tsx` and `ZoneEditor.test.tsx`, 23 tests) cover the
zone list, the signed and secondary markers, the firewall warning, the "not
reading the panel's zones" warning, the host's own warnings, zones the panel
does not manage, the install offer, creating forward and reverse zones, the
refusal messages, the delete confirmation, the settings, composite record
rendering, the managed record that cannot be edited, the two serials, and the DS
record for the registrar.

---

## 14. Known limitations

- **One name server.** BIND. PowerDNS is not supported, for the reason in
  section 2 rather than for lack of time.
- **One provider.** Cloudflare. The client is behind an interface and a second
  one is a file, but only what is tested is claimed.
- **No zone import.** A zone that exists at Cloudflare is not pulled into the
  panel, so adopting an existing domain means entering its records. Import
  reads somebody else's data and writes it here as though the panel produced it,
  and getting that half-right is worse than not offering it.
- **No secondary-only view of a transferred zone's contents.** The panel reports
  that a secondary loaded and when; it does not list the records inside it,
  because it does not have them — they are its primary's.
- **One address per host.** See section 8.
- **No DNSSEC key policy of the panel's own.** BIND's `default` is what is
  offered: one ECDSA key, rolled on BIND's schedule. Inventing a key policy is
  how a zone becomes unresolvable for the length of its longest TTL.
- **No delegation checking.** The panel does not verify that the parent zone
  actually points at this server, so a zone can be perfect here and unreachable
  from the internet. Checking would mean querying the parent's name servers,
  which is a monitoring feature rather than a DNS one.
