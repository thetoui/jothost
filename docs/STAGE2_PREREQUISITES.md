# Stage 2 prerequisites — what the maintainer must provide

Stage 2 of the production roadmap proves the panel against the real internet:
DNS that resolvers answer, a certificate a browser trusts, mail that reaches an
inbox, and a host that comes back from a reboot. None of it can be faked in
containers — propagation, reverse DNS, certificate-authority rate limits and
blocklists only exist out there — so this stage needs infrastructure only the
maintainer can supply.

This document is that list. Everything here is a one-time setup; once it is in
place the checks are scripted and repeatable.

---

## The gate this works towards

Stage 2 is done when a written staging report shows:

- a website on a **real domain** served over HTTPS with a **trusted certificate
  that has renewed at least once**;
- a message sent from the host that **lands in a major provider's inbox**, not
  its spam folder, with SPF, DKIM and DMARC passing;
- the host **rebooted** and every service back on its own, with sites still
  served and the Agent reconnected.

---

## What to provide

### 1. A staging host

| Requirement | Detail |
|---|---|
| Operating system | Ubuntu 24.04, Ubuntu 22.04, or Debian 12 — the tested production targets. A fresh install, nothing else on it. |
| Size | Modest is fine: 2 vCPU / 4 GB / 40 GB is comfortable. This is also the chance to record real [capacity](CAPACITY.md) numbers, so a spec worth publishing limits for is ideal. |
| Access | `root`, or a `sudo` user, over SSH. |
| Public IPv4 | A **static** address. IPv6 too if you want AAAA and IPv6 mail tested. |
| Inbound ports (provider firewall) | 22 (SSH), 80 and 443 (HTTP/S and the ACME challenge), and — if the panel will run its own name server — 53 TCP **and** UDP. For mail: 25, 465, 587, 993, 995. |
| **Outbound port 25** | **The one that trips people up.** Most large clouds (AWS, GCP, Azure, Oracle) block outbound 25 by default and will not send mail at all. Confirm your provider allows it, or request the block be lifted, before counting on the mail checks. Providers like Hetzner and OVH usually allow it on request. |

### 2. A domain you control

- A real registered domain, with access to its DNS.
- A name to serve the panel on (for example `panel.example.com`) and at least
  one **test website domain** (for example `site.example.com`).
- **One decision to make** (see below): whether the panel runs as the
  authoritative name server for a zone, or whether DNS stays with your existing
  provider and simply points at the host.

### 3. Reverse DNS (PTR) for the host's IP

- Set in the **hosting provider's** control panel (not the domain's DNS): the
  IP must resolve back to the mail hostname (for example `mail.example.com`),
  and that name must resolve forward to the same IP.
- Mail receivers reject or spam-file mail from an IP whose forward and reverse
  DNS do not match, so this is a hard requirement for the deliverability check.

### 4. For the mail checks specifically

- Outbound port 25 (above) and the matching PTR (above).
- The ability to publish **SPF, DKIM and DMARC** TXT records for the domain —
  the panel generates the values; you place them where your DNS is hosted.
- A **Gmail** inbox and a **Microsoft/Outlook** inbox to receive the test
  message, to confirm it lands in the inbox rather than spam.
- Awareness that a brand-new cloud IP is sometimes already on a blocklist;
  we check, and delisting is occasionally part of the work.

### 5. For the certificate checks

- The panel's domain must resolve **publicly** to the host, so Let's Encrypt's
  HTTP-01 challenge can reach it.
- A real email address for the ACME account (expiry notices). We issue against
  Let's Encrypt **staging first** to avoid burning the production rate limit
  while iterating.

---

## Decisions I need from you

1. **DNS mode.** Two options, and they need different setup:
   - **Authoritative** — the panel is the name server for a zone. This exercises
     the most (delegation, glue, SOA/NS, DNSSEC), and needs you to set **glue
     records and nameserver delegation at the registrar** pointing at the host.
   - **Delegated** — DNS stays with your current provider, and you just point an
     A/AAAA record at the host. Simpler; skips the authoritative-DNS checks.
2. **Which provider and region**, so I can confirm the port-25 position before
   you commit to the mail checks.
3. **How much of the stage to run.** The certificate and reboot checks need only
   the host and the domain. The mail checks need the port-25 / PTR / inbox
   pieces as well. If mail is hard to arrange, we can land the rest and treat
   mail as its own step.

---

## How we run it, and the split of the work

I cannot provision servers, and I will not handle your SSH keys, registrar
logins, or any other credential — those steps are yours, at your own console.
What I provide is the rest:

1. You stand up the host and domain from the checklist above.
2. I write the staging runbook and the check scripts (installer invocation,
   certificate issuance and renewal, the DNS and mail assertions, the reboot
   drill).
3. You run them on the host and paste the output back — or run the installer's
   own commands, which already report what they did.
4. I read the results, diagnose anything that failed, and iterate until the
   gate above is met.
5. The staging report is written up, and stage 2 closes.

Send me the provider, the domain, and which DNS mode you want, and I will start
on the runbook while you provision.
