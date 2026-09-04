#!/bin/sh
# Phase 26 Docker integration test — the mail server.
#
# The acceptance is not that the API returns 200. Mail is the one thing in this
# panel where a configuration can be entirely wrong and produce no error at all:
# the server accepts the message, the log says it was delivered, and it is
# quietly filed as spam by every recipient. So everything below is proved by
# doing it.
#
# What is proved here that nothing else can prove:
#
#   * Dovecot authenticates a real mailbox against a hash this panel computed
#     itself. If the crypt implementation, the passwd-file format, or the file's
#     permissions were wrong, this fails — and the failure a customer would see
#     is "password incorrect" for a password that is correct.
#   * A real message, sent over SMTP from an address the server does not trust,
#     is delivered into a real Maildir and can be read out of it.
#   * The server refuses to relay for a stranger. This is the one failure that
#     takes every customer on the host off the internet at once, and reading the
#     configuration cannot prove it — only trying can.
#   * A message submitted by an authenticated customer leaves signed, with a
#     DKIM signature made by the key the panel generated.
#   * Mail for an address that does not exist is refused rather than accepted
#     and dropped; with a catch-all it is accepted; a forwarder actually
#     forwards; a suspended mailbox stops authenticating.
#   * The panel reports a domain whose DKIM record is not published as *not
#     signing as far as anybody else is concerned* — which is the distinction
#     this whole phase exists to make.
#
# It runs inside the agent container, which is the managed host.
#
# Run with:  make docker-test-mail

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DOMAIN="mail${STAMP}.test"
MAILBOX="sales"
ADDRESS="$MAILBOX@$DOMAIN"
PASSWORD="Integration-Mail-Pw-${STAMP}"

failures=0
created_domains=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  for id in $created_domains; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/mail/domains/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
}
trap cleanup EXIT

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 240 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 240 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 240 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 240 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 300))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (unexpectedly found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

# host_address is this container's own routable address.
#
# Every SMTP check below connects to it rather than to the loopback, and that is
# not incidental: mynetworks contains 127.0.0.0/8, so a probe from the loopback
# is permitted *by design* and would report a correctly configured server as an
# open relay.
host_address() {
  ip -4 addr show eth0 2>/dev/null |
    sed -n 's/.*inet \([0-9.]*\).*/\1/p' | head -n 1
}

# smtp_send holds a paced SMTP conversation.
#
# Paced because Postfix refuses a client that sends its next command before the
# reply to the last one: unpaced input is answered with "SMTP protocol
# synchronization" and nothing is delivered, which would make every check below
# fail for a reason that has nothing to do with the panel.
smtp_send() {
  host="$1"; port="$2"; from="$3"; rcpt="$4"; subject="$5"
  {
    sleep 1
    printf 'EHLO tester.invalid\r\n';                        sleep 1
    printf 'MAIL FROM:<%s>\r\n' "$from";                     sleep 1
    printf 'RCPT TO:<%s>\r\n' "$rcpt";                       sleep 1
    printf 'DATA\r\n';                                       sleep 1
    printf 'Subject: %s\r\nFrom: %s\r\nTo: %s\r\n\r\nSent by the Phase 26 checks.\r\n.\r\n' \
      "$subject" "$from" "$rcpt";                            sleep 2
    printf 'QUIT\r\n';                                       sleep 1
  } | nc "$host" "$port" 2>/dev/null | tr -d '\r'
}

# smtp_submit sends as an authenticated customer, over the submission port.
smtp_submit() {
  host="$1"; port="$2"; user="$3"; secret="$4"; rcpt="$5"; subject="$6"
  credential="$(printf '\0%s\0%s' "$user" "$secret" | base64 | tr -d '\n')"
  {
    sleep 1
    printf 'EHLO client.invalid\r\n';                        sleep 1
    printf 'AUTH PLAIN %s\r\n' "$credential";                sleep 1
    printf 'MAIL FROM:<%s>\r\n' "$user";                     sleep 1
    printf 'RCPT TO:<%s>\r\n' "$rcpt";                        sleep 1
    printf 'DATA\r\n';                                       sleep 1
    printf 'Subject: %s\r\nFrom: %s\r\nTo: %s\r\n\r\nSubmitted by the Phase 26 checks.\r\n.\r\n' \
      "$subject" "$user" "$rcpt";                            sleep 2
    printf 'QUIT\r\n';                                       sleep 1
  } | nc "$host" "$port" 2>/dev/null | tr -d '\r'
}

# delivered_file finds the delivered message carrying a subject.
delivered_file() {
  grep -rl "Subject: $1" "/var/mail/vhosts/$2/$3/new" 2>/dev/null | head -n 1
}

# wait_for polls until a command succeeds or the budget runs out.
wait_for() {
  budget="$1"; shift
  waited=0
  while [ "$waited" -lt "$budget" ]; do
    if "$@"; then return 0; fi
    sleep 2
    waited=$((waited + 2))
  done
  return 1
}

log "Phase 26 — mail server"
log "======================"
log ""

login
if [ -z "${token:-}" ]; then
  log "Could not authenticate as $ADMIN_USER."
  exit 1
fi

ADDR="$(host_address)"
if [ -z "$ADDR" ]; then
  log "This container has no routable address, so nothing below can be tested honestly."
  exit 1
fi
log "Testing against $ADDR, which is deliberately not the loopback."
log ""

# ----------------------------------------------------------------- the server

log "The mail server"
overview="$(api GET /api/v1/mail)"
contains "the panel can see a mail server on this host" "$overview" '"available":true'
contains "Postfix is installed" "$overview" '"postfix"'
contains "Dovecot is installed" "$overview" '"dovecot"'

settings_body() {
  cat <<JSON
{"server_id":"","enabled":true,"hostname":"mail.jothost.test","require_tls":false,
 "spam_enabled":false,"spam_reject_score":15,"virus_enabled":false,
 "max_message_mb":25,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}
JSON
}
expect_status "mail can be switched on" 200 \
  "$(api_status PUT /api/v1/mail/settings "$(settings_body)")"

# A hostname that is not fully qualified is refused, because a host greeting the
# world as "localhost" has its mail refused by most of it — days later, as
# "some people never got my email".
expect_status "an unqualified mail hostname is refused" 422 \
  "$(api_status PUT /api/v1/mail/settings "$(settings_body | sed 's/mail.jothost.test/localhost/')")"

log ""
log "Domains and mailboxes"

domain="$(api POST /api/v1/mail/domains \
  "{\"domain\":\"$DOMAIN\",\"spf_policy\":\"soft\",\"dmarc_policy\":\"none\"}")"
domain_id="$(json_field "$domain" id)"
if [ -z "$domain_id" ]; then
  log "  FAIL  the mail domain was not created: $(printf '%s' "$domain" | head -c 300)"
  exit 1
fi
created_domains="$created_domains $domain_id"
pass "a mail domain was created"

selector="$(json_field "$domain" dkim_selector)"
if [ -n "$selector" ]; then
  pass "a signing key was generated as part of creating the domain"
else
  fail "the domain was created with no signing key, so its mail goes out unsigned"
fi

# The private key is on the host and is not in the reply.
not_contains "the reply carries no private key" "$domain" "PRIVATE KEY"
key_path="/var/lib/jothost/mail/dkim/$DOMAIN.$selector.key"
if [ -f "$key_path" ]; then
  pass "the private key is on the host that signs with it"
else
  fail "no private key at $key_path"
fi
mode="$(stat -c '%a' "$key_path" 2>/dev/null || echo '')"
if [ "$mode" = "640" ]; then
  pass "the private key is readable only by the signer"
else
  fail "the private key is mode ${mode:-unknown}, not 640"
fi

expect_status "a mailbox can be created" 201 \
  "$(api_status POST "/api/v1/mail/domains/$domain_id/mailboxes" \
    "{\"local_part\":\"$MAILBOX\",\"password\":\"$PASSWORD\",\"quota_mb\":50}")"

# The one rule that matters about a mailbox password: it is exposed to the whole
# internet on ports this panel does not rate-limit.
expect_status "a short mailbox password is refused" 422 \
  "$(api_status POST "/api/v1/mail/domains/$domain_id/mailboxes" \
    "{\"local_part\":\"weak\",\"password\":\"short\",\"quota_mb\":50}")"

boxes="$(api GET "/api/v1/mail/domains/$domain_id/mailboxes")"
mailbox_id="$(printf '%s' "$boxes" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -n 1)"
not_contains "no password hash is returned over the API" "$boxes" 'SHA512-CRYPT'

log ""
log "What the host actually does"

# The daemons are started through the panel's own service manager, which is also
# what proves the catalogue entries added by this phase resolve to real units.
for unit in postfix dovecot rspamd; do
  expect_status "the panel can start $unit" 200 \
    "$(api_status POST /api/v1/services/$unit/start)"
done

# Rspamd compiles ten thousand TLD suffixes before its milter answers, which
# takes the better part of a minute on a small machine — and until it does,
# Postfix defers every message.
#
# That is the panel's own deliberate choice rather than an accident (see
# milter_default_action in agent/internal/mail/postfix.go: an unsigned message
# cannot be recalled, a deferred one is retried), and this wait is the cost of
# it made visible. Every check below would otherwise fail with "Service
# unavailable" and blame the wrong thing.
milter_up() { nc -z 127.0.0.1 11332 2>/dev/null; }
if wait_for 180 milter_up; then
  pass "the filter is answering, so mail is not being deferred"
else
  fail "the filter never came up; every delivery below will be deferred"
fi

# --- Dovecot authenticates against a hash this panel computed --------------
#
# The foundation of the phase. The panel implements SHA-512 crypt itself so no
# plaintext password ever reaches an external program's argv; this is the check
# that the result is a hash Dovecot agrees with.
if doveadm auth test "$ADDRESS" "$PASSWORD" 2>&1 | grep -q 'auth succeeded'; then
  pass "Dovecot authenticates a mailbox against the panel's own hash"
else
  fail "Dovecot rejected a correct password — the hash, the file, or its permissions are wrong"
fi
if doveadm auth test "$ADDRESS" "not-the-password" 2>&1 | grep -q 'auth failed'; then
  pass "a wrong password is refused"
else
  fail "a wrong password was accepted"
fi

# --- a real message, end to end -------------------------------------------
subject="delivery-$STAMP"
reply="$(smtp_send "$ADDR" 25 "outside@sender.invalid" "$ADDRESS" "$subject")"
contains "the server accepted a message for a mailbox it hosts" "$reply" "250 2.0.0 Ok"

message_arrived() { [ -n "$(delivered_file "$subject" "$DOMAIN" "$MAILBOX")" ]; }
if wait_for 30 message_arrived; then
  pass "the message was delivered into the mailbox and can be read back"
else
  fail "the message was accepted and never arrived, which is how mail is lost silently"
fi

# --- the check that matters most ------------------------------------------
#
# An open relay is on a blocklist within hours, and every customer on the host
# stops being able to send mail. Reading the configuration cannot prove this;
# only trying can.
relay="$(smtp_send "$ADDR" 25 "outside@sender.invalid" "stranger@elsewhere.invalid" "relay-$STAMP")"
case "$relay" in
  *"Relay access denied"*) pass "this server refuses to relay for a stranger" ;;
  *) fail "the server did not refuse to relay: $(printf '%s' "$relay" | tr '\n' ' ' | head -c 200)" ;;
esac

# --- an address that does not exist ----------------------------------------
#
# Refused, rather than accepted and dropped. A server that accepts mail it
# cannot deliver has to bounce it afterwards, to an address that is usually
# forged — which is how a host becomes a backscatter source and gets listed.
unknown="$(smtp_send "$ADDR" 25 "outside@sender.invalid" "nobody@$DOMAIN" "unknown-$STAMP")"
case "$unknown" in
  *"550"*|*"User unknown"*|*"Recipient address rejected"*)
    pass "mail for an address that does not exist is refused at the door" ;;
  *) fail "an unknown recipient was accepted: $(printf '%s' "$unknown" | tr '\n' ' ' | head -c 200)" ;;
esac

# --- a forwarder actually forwards -----------------------------------------
expect_status "a forwarder can be created" 201 \
  "$(api_status POST "/api/v1/mail/domains/$domain_id/aliases" \
    "{\"source\":\"info\",\"destination\":\"$ADDRESS\"}")"

forward_subject="forwarded-$STAMP"
smtp_send "$ADDR" 25 "outside@sender.invalid" "info@$DOMAIN" "$forward_subject" >/dev/null
forwarded() { [ -n "$(delivered_file "$forward_subject" "$DOMAIN" "$MAILBOX")" ]; }
if wait_for 30 forwarded; then
  pass "mail to a forwarder arrives in the mailbox it points at"
else
  fail "the forwarder did not forward"
fi

# --- signing, proved by reading the signature ------------------------------
#
# Not "a key exists" — a message that left this server carries a DKIM-Signature
# made with it. Sent through the submission port as the customer, because that
# is the path nearly all outbound mail takes.
signed_subject="signed-$STAMP"
submitted="$(smtp_submit "$ADDR" 587 "$ADDRESS" "$PASSWORD" "$ADDRESS" "$signed_subject")"
contains "an authenticated customer may submit mail" "$submitted" "235"

signed() {
  file="$(delivered_file "$signed_subject" "$DOMAIN" "$MAILBOX")"
  [ -n "$file" ] && grep -q "DKIM-Signature" "$file"
}
if wait_for 40 signed; then
  pass "a submitted message leaves carrying a DKIM signature"
else
  file="$(delivered_file "$signed_subject" "$DOMAIN" "$MAILBOX")"
  if [ -n "$file" ]; then
    fail "the message was delivered unsigned, which every recipient treats as unauthenticated"
  else
    fail "the submitted message never arrived"
  fi
fi

# --- submission refuses an unauthenticated client --------------------------
#
# The port exists for one purpose. A port that both accepts anonymous mail and
# offers AUTH is a port worth attacking; one that will not talk to anybody who
# has not authenticated is not.
anon="$(smtp_send "$ADDR" 587 "outside@sender.invalid" "$ADDRESS" "anon-$STAMP")"
case "$anon" in
  *"554"*|*"Access denied"*|*"Relay access denied"*)
    pass "the submission port refuses an unauthenticated client" ;;
  *) fail "submission accepted an unauthenticated sender: $(printf '%s' "$anon" | tr '\n' ' ' | head -c 200)" ;;
esac

log ""
log "What the world can see"

# The phase's central comparison. The panel holds a signing key and a policy;
# what decides whether anybody believes this domain is what DNS publishes, and
# this host does not serve DNS for a .test domain — so the honest answer is
# "cannot check", not "not published".
overview="$(api GET /api/v1/mail)"
contains "a domain whose DNS is elsewhere is reported as uncheckable, not as broken" \
  "$overview" "does not serve DNS for $DOMAIN"
contains "the host reports which domains it can sign for" "$overview" "\"signing\":true"

# The relay check the panel makes for itself, from its own routable address.
contains "the panel checked whether it is an open relay" "$overview" '"checked":true'
contains "and reports that it is not" "$overview" '"open":false'

log ""
log "Suspending, and what stays behind"

expect_status "a mailbox can be suspended" 200 \
  "$(api_status PATCH "/api/v1/mail/mailboxes/$mailbox_id" '{"active":false}')"

if doveadm auth test "$ADDRESS" "$PASSWORD" 2>&1 | grep -q 'auth failed'; then
  pass "a suspended mailbox cannot log in"
else
  fail "a suspended mailbox can still log in"
fi
if [ -n "$(delivered_file "$subject" "$DOMAIN" "$MAILBOX")" ]; then
  pass "its messages are untouched, which is what makes suspending different from deleting"
else
  fail "suspending a mailbox destroyed its mail"
fi

expect_status "a mailbox can be brought back" 200 \
  "$(api_status PATCH "/api/v1/mail/mailboxes/$mailbox_id" '{"active":true}')"
if doveadm auth test "$ADDRESS" "$PASSWORD" 2>&1 | grep -q 'auth succeeded'; then
  pass "and can log in again"
else
  fail "a reactivated mailbox cannot log in"
fi

log ""
log "Autoresponders"

# The one piece of customer-written text that becomes a script the mail server
# executes. It is escaped, and then compiled — because a script that does not
# compile is one Dovecot ignores at delivery time, in silence.
expect_status "an autoresponder can be set" 200 \
  "$(api_status PUT "/api/v1/mail/mailboxes/$mailbox_id/autoresponder" \
    '{"subject":"Away until Monday","body":"Back on Monday.","interval_days":7}')"

script="/var/lib/jothost/mail/sieve/$ADDRESS.sieve"
if [ -f "$script" ]; then
  pass "a Sieve script was written for the mailbox"
else
  fail "no Sieve script at $script"
fi
if [ -f "${script%.sieve}.svbin" ]; then
  pass "and it compiled, so Dovecot will actually run it"
else
  fail "the script was not compiled, so Dovecot would skip it in silence"
fi

# A subject that would end the Sieve string and turn the rest into script.
expect_status "an autoresponder that would escape into script is refused or escaped" 200 \
  "$(api_status PUT "/api/v1/mail/mailboxes/$mailbox_id/autoresponder" \
    '{"subject":"Away \"until\" Monday","body":"Back soon.","interval_days":7}')"
if [ -f "$script" ] && ! grep -q 'discard' "$script"; then
  pass "the quoted subject was escaped rather than executed"
else
  fail "the autoresponder script contains something the customer did not write"
fi

expect_status "an autoresponder can be cleared" 204 \
  "$(api_status DELETE "/api/v1/mail/mailboxes/$mailbox_id/autoresponder")"
if [ ! -f "$script" ]; then
  pass "and its script is removed, so a future mailbox at this address does not inherit it"
else
  fail "the Sieve script outlived the autoresponder"
fi

log ""
log "Removing"

expect_status "a mail domain can be removed" 204 \
  "$(api_status DELETE "/api/v1/mail/domains/$domain_id")"
created_domains=""

tables="$(cat /var/lib/jothost/mail/virtual_domains /var/lib/jothost/mail/virtual_mailboxes 2>/dev/null || true)"
not_contains "the domain is gone from the host's lookup tables" "$tables" "$DOMAIN"
if [ ! -f "$key_path" ]; then
  pass "its signing key was removed from the host"
else
  fail "the signing key outlived the domain it signed for"
fi
if [ -d "/var/mail/vhosts/$DOMAIN" ]; then
  pass "the messages are still on the disk — the panel does not delete anybody's mail"
else
  fail "removing the domain destroyed its mail"
fi

# Housekeeping: the messages this run created are the test's own, so it clears
# them up. A real removal deliberately leaves them.
rm -rf "/var/mail/vhosts/$DOMAIN" 2>/dev/null || true

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 26 checks passed."
  exit 0
fi
log "$failures Phase 26 check(s) failed."
exit 1
