# Stage 2 runbook — proving the panel against the real internet

The procedure that turns a provisioned staging host (see
[STAGE2_PREREQUISITES.md](STAGE2_PREREQUISITES.md)) into the stage 2 report:
a site on a real domain with a trusted, renewed certificate; mail that reaches
an inbox; and a host that survives a reboot.

Run it top to bottom on the staging host, over SSH. Each step says what to do,
the command, and what a pass looks like. `tests/staging/verify.sh` (run from
anywhere that can see the public internet) checks the externally-observable
parts — DNS, reverse DNS, the certificate, and the mail records — in one pass;
the manual steps below are the ones a script cannot judge, such as whether a
message actually landed in an inbox.

Nothing here has been executed against a real host by the maintainer of this
document: it is the plan, and its external checks have been proven only against
public reference domains. Treat a first run as the test, and record what
actually happens in the report at the end.

Throughout, `example.com` is your domain, `panel.example.com` the panel, and
`203.0.113.10` the host's public IPv4 — substitute your own.

---

## 0. Before you start

- The host is a fresh Ubuntu 24.04 / 22.04 or Debian 12 box with a static
  public IPv4 and root or sudo.
- You have the built artefacts on it (`make dist` output, or a release archive).
- Your provider allows **outbound port 25** if you are doing the mail steps.

---

## 1. Point DNS at the host, then install

1. At your DNS, create an `A` record: `panel.example.com` → `203.0.113.10`
   (and `AAAA` if you have IPv6). Wait for it to resolve publicly:

   ```bash
   dig +short panel.example.com @1.1.1.1
   ```

   It must return the host's IP before the next step, or the certificate
   challenge cannot be answered.

2. Install, **without `--self-signed`**, so the installer asks Let's Encrypt for
   a real certificate:

   ```bash
   sudo ./install.sh install --domain panel.example.com --email you@example.com --yes
   ```

   A pass: the closing summary prints the administrator password and does **not**
   warn about a self-signed certificate. If DNS was not ready and it fell back
   to self-signed, fix DNS and run `sudo ./install.sh repair` to upgrade it.

3. Export and store the encryption key off the host now, before there is
   anything to lose (see [RECOVERY.md](RECOVERY.md)):

   ```bash
   sudo ./install.sh export-key --to /root/jothost-panel.key
   ```

---

## 2. A website on a real domain, over HTTPS

1. Sign in to `https://panel.example.com`, create a website for a real name you
   control (`site.example.com`), and point that name's `A` record at the host.
2. Once it resolves, issue a certificate for it — in the panel, or:

   ```bash
   curl -fsS -X POST https://panel.example.com/api/v1/websites/<id>/ssl/issue \
     -H "Authorization: Bearer <token>"
   ```

3. Pass: `https://site.example.com` loads with a certificate a browser trusts
   (issuer *Let's Encrypt*), no warning.

---

## 3. Certificate renewal

A stage 2 pass requires a certificate that has **renewed at least once**, not
merely issued. The renewal sweep runs on a timer; to prove it without waiting
for expiry, force one:

```bash
curl -fsS -X POST https://panel.example.com/api/v1/websites/<id>/ssl/renew \
  -H "Authorization: Bearer <token>"
```

Pass: the certificate's *notBefore* date moves to today, and the renewal also
re-aligned any DNS records it manages. Confirm with `verify.sh` (below) or:

```bash
echo | openssl s_client -connect site.example.com:443 -servername site.example.com 2>/dev/null \
  | openssl x509 -noout -issuer -dates
```

---

## 4. Authoritative DNS (only if the panel is the name server)

Skip this section if DNS stays with your existing provider.

1. `POST /api/v1/dns/install` (or the DNS page's "install") to bring up the name
   server, then create a zone for a domain you can delegate:
   `POST /api/v1/dns/zones`.
2. At the **registrar**, set the domain's nameservers (and glue records) to the
   host.
3. Pass: a public resolver answers the zone's SOA and NS from the panel's name
   server:

   ```bash
   dig +norecurse SOA delegated.example.com @1.1.1.1
   dig NS delegated.example.com @8.8.8.8
   ```

---

## 5. Mail that reaches an inbox

Needs outbound port 25 and the PTR from the prerequisites.

1. Set the host's **reverse DNS** at the provider so `203.0.113.10` →
   `mail.example.com`, and an `A` record `mail.example.com` → `203.0.113.10`.
2. `POST /api/v1/mail/install`, then add the mail domain
   (`POST /api/v1/mail/domains`) and generate its DKIM key
   (`POST /api/v1/mail/domains/{id}/dkim`).
3. Publish the records the panel gives you at your DNS:
   - `MX` → `mail.example.com`
   - `TXT` SPF: `v=spf1 mx -all`
   - `TXT` DKIM at `<selector>._domainkey` (from the DKIM step)
   - `TXT` DMARC at `_dmarc`: `v=DMARC1; p=quarantine; rua=mailto:…`
4. Create a mailbox and send a message from it to **a Gmail address and an
   Outlook address**.
5. Pass: the message arrives in the **inbox** (not spam) at both, and its
   headers show `spf=pass`, `dkim=pass`, `dmarc=pass`. This is the manual check
   `verify.sh` cannot make for you — open the two inboxes and look.

---

## 6. Firewall and SSH over a live session

The panel rolls a firewall change back if it severs connectivity. Prove it for
real: from your own SSH session, apply a rule that would lock you out and watch
access come back on its own. Do this with the provider's console open as a
safety net.

---

## 7. Reboot

```bash
sudo reboot
```

Pass, once it is back: `sudo ./install.sh status` shows every service active and
dependencies ready, `https://site.example.com` still serves, and the panel
reaches the Agent again — with nothing started by hand.

---

## 8. The external checks, in one command

From any machine that can see the public internet:

```bash
sh tests/staging/verify.sh \
  --panel panel.example.com \
  --site site.example.com \
  --ip 203.0.113.10 \
  --mail-host mail.example.com \
  --mail-domain example.com \
  --dkim-selector default
```

It reports, per check: public DNS resolution, forward/reverse DNS agreement,
the certificate's trust, issuer and freshness, and the presence of the SPF,
DKIM and DMARC records. It does not judge inbox placement (section 5) or the
reboot (section 7) — those are yours to observe.

---

## 9. The report

Write up what happened, pass or fail, against the [stage 2
gate](STAGE2_PREREQUISITES.md#the-gate-this-works-towards): the trusted, renewed
certificate; the message in a real inbox; the reboot recovered. A failure is a
finding — record it and its cause, do not paper over it. That report is what
closes, or reopens, stage 2.
