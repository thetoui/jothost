# Phase 20 — Notifications

What this phase adds, the failure it is built around, the bug it uncovered in
Phase 19, and — stated plainly — what it will not do.

---

## 1. The failure everything else follows from

**A notification system cannot report its own failure through itself.**

When email delivery breaks, the email saying "email delivery is broken" does not
arrive. The operator's experience is silence, and silence is exactly what a
healthy machine produces. Every other phase in this panel can tell you when it
has gone wrong; this one is the exception, and the whole design is a response to
that.

So:

- **Every attempt is a row.** What was sent, where, whether it arrived, how many
  tries it took, and the reason if it did not.
- **A channel counts its consecutive failures**, and the page leads with that
  count.
- **A channel that has never succeeded is shown as untested, not as working.**
  "We have never got a message through this" and "this is fine" are different
  facts and only one of them belongs in green.
- **A test send** delivers a real message now, while somebody is watching. It is
  the same idea as Phase 14's destination check, for the same reason: a channel
  nobody has ever delivered through looks like protection and is not.

The delivery record is not a footnote on this page. It is the page.

---

## 2. Why an outbox

Each phase writes an event; a dispatcher delivers it. Two reasons, and the
second is the one that matters.

A phase calling a sender directly would block its own loop on somebody's slow
SMTP server — the monitor evaluating alerts must not wait on a mail relay. And a
process that died between "the alert opened" and "the mail was sent" would lose
the notification with nothing recording that it had ever existed. A row written
in the same breath as the alert survives that.

The five sources each declare their **own** one-method interface rather than
importing this package. A nil notifier is normal and means the phase behaves
exactly as it did before Phase 20 existed.

---

## 3. Why the flooding is handled by an index

One event per thing that happened, enforced by a unique index on a dedupe key.

A disk sitting above its threshold for a week is one event or it is ten thousand
emails, and which one depends on a constraint rather than on every caller
remembering. Two API processes racing would each pass a check written in Go;
neither gets past the index.

The keys are the identity of the *thing*, not of the moment it was noticed:
`alert.opened:<alert id>`, `backup.failed:<backup id>`,
`security.finding:<finding id>`, `ssl.expiring:<domain>`.

Events are raised on **every** evaluation rather than only the first, and the
index is what makes that safe. It also makes it better: a panel that could not
reach its database on the minute an alert opened still notifies when it comes
back, instead of having missed its one chance.

Certificates get three thresholds — thirty days, seven days, expired — rather
than a daily reminder for a month, because a daily reminder about the same
certificate is a daily reminder people filter.

---

## 4. The bug this phase found in Phase 19

Migration 0016 keyed one open alert per `(server, metric, target, severity)`.
That is right about the *thing being watched* and wrong about *who is watching
it*.

Two rules can legitimately watch one filesystem at the same severity — a general
"any disk above 85%" and a specific "this one above 60%" — and Phase 19's own
uniqueness on `alert_rules` permits it, because `""` and `/var` are different
targets. Under the old key those two rules shared one alert row and fought over
it: on every evaluation the rule that was not breaching resolved the alert the
other had just opened, and the next tick reversed it.

Phase 19 logged that quietly and it looked like nothing. Phase 20 turned every
turn of the fight into an email — one a minute, which is precisely the flood
this phase exists to prevent. The integration test caught it on its first real
run.

**Migration 0020** re-keys an open alert to `(server, rule_id, target)`, and
`ResolveAlert` is scoped to the rule that raised the alert. Severity leaves the
key because it is a property of the rule: one rule has one severity, so keying
on the rule already covers it.

While there: deleting a rule now resolves its open alerts. Nothing else ever
could — the monitor resolves an alert by evaluating the rule that raised it —
so one left open would have stayed open forever about a condition nobody was
watching.

This is CLAUDE.md section 21 in practice: the dependency was identified,
explained, fixed as small as it could be, and written down.

---

## 5. The three channels, and the one that does not exist

**There is no webhook channel, and no free-form URL anywhere in this phase.**

A notification channel that accepts a URL is a request forger sitting inside the
panel, pointed at whatever an admin account can be talked into typing — from a
machine that sits on the private network beside every site it hosts and can
reach a cloud metadata endpoint. Telegram and LINE each publish one API host,
and those are compiled in as constants.

Email is the exception that proves the rule: its host is the operator's own mail
server, which is a thing they already run rather than a thing they were
persuaded to point at.

All three are built on the standard library — `net/smtp` and `net/http` — which
is enough for four requests between them.

### Two concessions, and why only one of them is defensible

**An unencrypted connection may carry a message.** An internal relay on a
private network is an ordinary arrangement, and a panel that flatly refused it
would be worked around rather than obeyed. So plain SMTP off this machine is
possible with an explicit acknowledgement — the same answer Phase 14 reached for
a plain-http S3 endpoint.

**An unencrypted connection may never carry a password.** Sending one in the
clear is giving it away, and the alert it was protecting is the least of what is
lost. The panel refuses that combination at the moment it is configured rather
than letting it fail on the first night something goes wrong. (Go's
`smtp.PlainAuth` refuses it too; discovering that during the integration test is
what made the inconsistency in the first design visible.)

---

## 6. What is sent, and what is not

Five kinds: an alert opening, an alert resolving, a backup failing, a
certificate running out of time, and a security finding serious enough to act on
today.

The test of whether something belongs is not "is it interesting" but **"would
somebody want to be interrupted by it"**. A successful backup is not news. A
medium security finding is a thing to fix this week. A certificate that renewed
itself is the system working. A channel that reported those is a channel
somebody mutes — and a muted channel does not deliver the one message that
mattered.

**An alert resolving is sent**, and that is not an afterthought: an alert that
clears itself at four in the morning is the difference between getting up and
going back to sleep. A system that wakes people and never tells them it is over
teaches them to get up every time.

**A failed backup is critical**, deliberately. It is not dangerous today; it is
dangerous on the day somebody needs it, by which point nothing can be done. The
severity is expressing that asymmetry.

**An accepted security finding is skipped.** Somebody has already looked at it
and written down why.

A channel takes a **severity floor** rather than a set of checkboxes, because
the question an operator actually has is "how bad does it have to be before you
wake me" — and a set of checkboxes invites the answer "all of them" followed by
a filter rule in their mail client.

---

## 7. The retry policy, and why it gives up

Backoff — a minute, four, sixteen — and then a limit.

Exponential rather than fixed, because the failures worth retrying are outages,
and one that has already lasted five minutes is more likely to last another five
than to end in the next thirty seconds. A fixed interval spends its attempts in
the window where they are least likely to work.

**A permanent failure is not retried at all.** A wrong bot token, a chat the bot
was removed from, a mail server refusing the sender: retrying four times reaches
the same answer four times and delays every notification behind it. The senders
classify their own failures, and rate limiting is deliberately *not* permanent —
giving up on a 429 would drop a notification for being too prompt.

**A delivery retried forever is a queue that never drains.** So it gives up, and
the row stays with the reason, because a notification that was never delivered
is the most important thing this table records.

The attempt counter is incremented as part of *claiming* a delivery, so a
dispatcher that crashed after sending but before recording sends again once
rather than forever. Duplicating an alert is a much better failure than an
infinite loop of them.

---

## 8. Where the credentials are not

An SMTP password and a bot token are both full credentials — anyone holding a
bot token can post as the bot to every chat it is in.

They are AES-256-GCM encrypted and bound to their row's id, so a ciphertext
moved from another channel fails to decrypt rather than quietly authenticating
somewhere it should not. The `Channel` struct handlers serialise has **no field
for them at all**: a field that is only sometimes cleared is one that will one
day be returned.

The SMTP *username* is not encrypted — it is an identifier, and a page needs to
show which account a channel sends as.

One redaction is worth naming: a Telegram URL carries the bot token in its path,
so a transport error containing it would put a full credential into the delivery
record, which is shown in the panel and kept for weeks. Errors have their
addresses stripped before they are stored.

---

## 9. Where this diverges from the specification

Notifications appear in **TASKS.md** and in PRD.md's alert list, and in neither
API_SPEC.md nor DATABASE.md. Everything here — three tables, nine endpoints, one
permission — is designed rather than specified, and is written up in API_SPEC
section 29 and DATABASE sections 31 to 31.2 as built.

PRD.md's six alert kinds map onto what the panel can actually raise: SSL
expiring, backup failed and security issue are their own event kinds; disk
usage, memory usage and service down all arrive as `alert.opened` from Phase
19's monitor, which is where those thresholds live. Adding three more event
kinds that were all "an alert opened" would have been three ways to say one
thing.

`notification.manage` is granted to **admin only**, unlike `monitor.manage`
which operators get. Silencing a false alarm at three in the morning is an
operator's job; silencing every alert on the machine is not.

---

## 10. What the panel refuses

- **A webhook, or any channel with a URL.** Section 5.
- **Plain SMTP off this machine** without an explicit acknowledgement, and a
  **password over an unencrypted connection** under any circumstances.
- **STARTTLS that silently downgrades.** A server not offering it fails the send
  rather than continuing in the clear — the operator asked for encryption and
  believes they have it.
- **An address or subject carrying a line break**, which is how a message gains
  a header its author did not write. The text is composed from a host's own
  output — a process name, a mount point, a domain — which is not this panel's
  to trust.
- **A severity or event kind outside the closed sets**, so a typo is a 422
  rather than a filter that silently matches nothing.
- **A channel with no credential**, in the schema as well as in Go — with the
  one exception of an email channel for an unauthenticated relay, where the
  absence is recorded rather than left blank.

---

## 11. What the tests prove

**Go, no database** (`api/internal/notifications`, 18 tests): that Telegram is
sent as plain text with no parse mode — its Markdown mode rejects an unbalanced
character, and a filename with an underscore would be enough to make an alert
fail to send; that a 401 is permanent and a 429 and a 502 are not; that LINE
truncates a message it would otherwise refuse; that an email subject carrying a
line break is refused; that a body line of a single dot is escaped, which
`net/smtp` does not do and which would otherwise truncate the message; that a
bot token never survives into a stored error; and that a channel which has never
delivered is not healthy however few times it has failed.

**Go, with a database** (`api/internal/notifications`, 15 tests): that the same
thing happening twice is one event and the resolution of it is another; that
queueing the same delivery three times produces one; that claiming counts the
attempt; that a disabled channel receives nothing; that a rescheduled delivery
is not claimed until it is due; that a failed one always says why; that a
channel counts consecutive failures and one success clears them; that broken and
untested channels are counted separately; and that deleting a channel keeps the
events while taking its deliveries.

**Go, monitoring** (`api/internal/monitoring`, 2 new tests): that two rules
watching one target no longer fight over its alert, and that deleting a rule
resolves the alerts it had open.

**Frontend** (`NotificationsPage.test.tsx`, 16 tests): that a panel with no
channels says so; that broken channels lead the page and say why nothing else
could have told you; that an untested channel is flagged; that the delivery
record shows what did not arrive; that there is no webhook option anywhere in
the form; and that a severity floor is offered rather than checkboxes.

**Integration** (`tests/integration/phase20_notifications.sh`, 33 checks)
against the live stack, a **real SMTP server** and the real monitor:

- a test message composed by the panel is accepted by a real mail server, and is
  readable in the mailbox afterwards with its subject, its recipient and a link
  that goes somewhere
- a **real alert**, opened by the real monitor because a real threshold was
  crossed on this host, produces a **real email** — end to end, nothing stubbed
- the same alert, re-read by the monitor every minute for seventy seconds,
  produces **exactly one** message
- the alert clearing on its own also sends one
- a channel pointed at a port nothing is listening on is recorded as failing,
  with the reason, and the overview counts it as broken
- and every refusal in section 10

```bash
make docker-test-notifications
```

---

## 12. Known limitations

- **No quiet hours and no digest.** An alert at three in the morning is sent at
  three in the morning. The deduplication means it is one message rather than
  sixty, but there is no way to say "batch anything below critical until
  breakfast".
- **No per-user notification preferences.** Channels belong to the server, not
  to people. A team wanting different things is a team wanting several channels.
- **The dashboard does not surface a broken channel.** It is visible on the
  notifications page and in the log, and nowhere else. That is the honest
  boundary of the phase's central problem — the panel cannot notify anybody that
  notifications are broken — but somebody still has to open the page.
- **No delivery receipt beyond the handover.** "Sent" means the mail server
  accepted the message or the API returned 200. A message accepted by a relay
  and then dropped is recorded as delivered, because nothing else is knowable
  from here.
- **No LINE or Telegram inbound.** These are one-way: the panel cannot be
  replied to, and an alert cannot be acknowledged from a chat.
- **No rate limit across events.** Each event is deduplicated, but fifty
  different things going wrong at once is fifty messages. In practice Phase 19's
  sustained-breach rule and Phase 15's grouping keep that number small.
- **No escalation.** A notification that nobody acts on is not re-sent, and does
  not move to a second channel after an hour.
- **Retention is thirty days.** The delivery record is the only evidence a
  notification failed, and after a month it is gone.
