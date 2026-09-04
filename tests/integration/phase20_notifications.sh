#!/bin/sh
# Phase 20 Docker integration test — notifications.
#
# The acceptance is not that the API returns 200. It is that a real message
# arrives in a real mailbox, sent by a real thing going wrong on this host.
#
# Four things are proved here that nothing else can prove:
#
#   * A test message composed by the panel is accepted by a real SMTP server and
#     is readable in the mailbox afterwards, with the subject and the link in it.
#   * A real alert, opened by the real monitor because a real threshold was
#     crossed, produces a real email — end to end, with nothing stubbed.
#   * The same alert, re-read by the monitor every minute, produces exactly one
#     message. That is the whole of the panel's protection against a full disk
#     becoming ten thousand emails.
#   * A channel that cannot deliver is recorded as failing, with the reason,
#     because that record is the only way an operator can learn that the silence
#     they have been enjoying was not good news.
#
# It runs inside the agent container, which can reach both the API and the mail
# server on the compose network.
#
# Run with:  make docker-test-notifications

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

MAIL_HOST="${MAIL_HOST:-mailpit}"
MAIL_SMTP_PORT="${MAIL_SMTP_PORT:-1025}"
MAIL_API="${MAIL_API:-http://mailpit:8025}"

STAMP="$(date +%s)"
# How long to wait for the dispatcher to come round. It drains every fifteen
# seconds, and the monitor that raises an alert ticks every minute.
DISPATCH_WAIT="${DISPATCH_WAIT:-90}"
ALERT_WAIT="${ALERT_WAIT:-180}"

failures=0
created_channels=""
created_rules=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  for id in $created_rules; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/monitoring/rules/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
  for id in $created_channels; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/notification-channels/$id" \
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
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
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

# mail_search returns the mailbox entries whose subject matches.
mail_search() {
  curl -s --max-time 20 "$MAIL_API/api/v1/search?query=$1" 2>/dev/null || true
}

mail_count() {
  mail_search "$1" | grep -o '"ID":"' | wc -l | tr -d ' '
}

# wait_for polls until a command succeeds or the budget runs out.
wait_for() {
  budget="$1"; shift
  waited=0
  while [ "$waited" -lt "$budget" ]; do
    if "$@"; then
      return 0
    fi
    sleep 5
    waited=$((waited + 5))
  done
  return 1
}

log '== Phase 20: notifications =='

if ! curl -s --max-time 10 "$MAIL_API/api/v1/info" >/dev/null 2>&1; then
  log "FATAL: no mail server at $MAIL_API — start it with: make docker-test-notifications"
  exit 1
fi
pass 'a real mail server is reachable'

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

# --- a panel with nowhere to send anything ---------------------------------

overview="$(api GET /api/v1/notifications)"
contains 'the overview advertises the channel kinds' "$overview" '"channel_kinds":['
contains 'and the events it can raise' "$overview" '"event_kinds":['
contains 'and what has been getting through' "$overview" '"stats":'

# --- refusals ---------------------------------------------------------------

# There is deliberately no webhook kind: a channel that accepted a URL would be
# a request forger sitting inside the panel.
expect_status 'a webhook channel is refused' 422 \
  "$(api_status POST /api/v1/notification-channels \
     "{\"name\":\"hook-$STAMP\",\"kind\":\"webhook\",\"host\":\"evil.example\"}")"

# A password sent in the clear to somewhere off this machine is a password given
# away, and the alert it was protecting is the least of it.
expect_status 'plain SMTP to another machine is refused until it is accepted' 422 \
  "$(api_status POST /api/v1/notification-channels \
     "{\"name\":\"plain-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"from\":\"panel@example.com\",\"to\":[\"ops@example.com\"],\"password\":\"x\"}")"

# An address carrying a newline is how a message gains a header its author did
# not write.
expect_status 'an address carrying a line break is refused' 422 \
  "$(api_status POST /api/v1/notification-channels \
     "{\"name\":\"inject-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"ops@example.com\\nBcc: attacker@example.com\"],\"password\":\"x\"}")"

expect_status 'a severity outside the scale is refused' 422 \
  "$(api_status POST /api/v1/notification-channels \
     "{\"name\":\"sev-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"ops@example.com\"],\"min_severity\":\"catastrophic\"}")"

expect_status 'an event kind this panel does not raise is refused' 422 \
  "$(api_status POST /api/v1/notification-channels \
     "{\"name\":\"kind-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"ops@example.com\"],\"kinds\":[\"telepathy\"]}")"

# An unencrypted connection may carry a message; it may never carry a password.
# Two different concessions, and only the first is defensible.
expect_status 'a password over an unencrypted connection is refused' 422   "$(api_status POST /api/v1/notification-channels      "{\"name\":\"plainpw-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"ops@example.com\"],\"password\":\"hunter2\"}")"

# --- a channel that works ---------------------------------------------------

RECIPIENT="ops-$STAMP@example.com"
# No password: an unencrypted connection may carry a message and may never
# carry a password, which is a rule the panel enforces rather than discovers.
channel_body="{\"name\":\"integration-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":$MAIL_SMTP_PORT,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"$RECIPIENT\"],\"min_severity\":\"info\"}"

channel="$(api POST /api/v1/notification-channels "$channel_body")"
channel_id="$(json_field "$channel" id)"
if [ -z "$channel_id" ]; then
  log "FATAL: could not create a channel: $(printf '%s' "$channel" | head -c 300)"
  exit 1
fi
created_channels="$created_channels $channel_id"
pass 'an email channel was created'

# A channel nobody has ever delivered through looks like protection and is not.
contains 'a new channel has never delivered anything' "$channel" '"last_success_at":null'
not_contains 'a channel never returns a credential' "$channel" 'password'

# --- a real message, to a real mailbox --------------------------------------

tested="$(api POST "/api/v1/notification-channels/$channel_id/test")"
contains 'a test send succeeds against a real mail server' "$tested" '"failure_streak":0'
not_contains 'and the channel no longer reads as never having delivered' \
  "$tested" '"last_success_at":null'

check_test_mail() { [ "$(mail_count "Test%20notification")" -gt 0 ]; }
if wait_for 30 check_test_mail; then
  pass 'the test message arrived in the mailbox'
else
  fail 'no test message arrived'
fi

message="$(mail_search "Test%20notification")"
contains 'the message names this panel in its subject' "$message" 'JotHost Panel'
contains 'and was addressed to the configured recipient' "$message" "$RECIPIENT"

# --- a real alert, end to end -----------------------------------------------

# A disk rule that is breaching from the moment it exists and fires on the first
# reading, so the monitor opens a real alert on its next tick.
mount="$(api GET /api/v1/dashboard | sed -n 's/.*"mount_point":"\([^"]*\)".*/\1/p' | head -1)"
if [ -z "$mount" ]; then
  mount="/"
fi

rule_body="{\"name\":\"notify-test-$STAMP\",\"metric\":\"disk\",\"target\":\"$mount\",\"comparison\":\"above\",\"threshold\":1,\"for_seconds\":0,\"severity\":\"critical\"}"
rule="$(api POST /api/v1/monitoring/rules "$rule_body")"
rule_id="$(json_field "$rule" id)"
if [ -z "$rule_id" ]; then
  fail "could not create an alert rule: $(printf '%s' "$rule" | head -c 300)"
else
  created_rules="$created_rules $rule_id"
  pass "an alert rule was created that this host will breach ($mount above 1%)"

  # The alert's subject is built from the rule's name, which is what makes it
  # searchable without matching anything else in the mailbox.
  check_alert_mail() { [ "$(mail_count "notify-test-$STAMP")" -gt 0 ]; }
  if wait_for "$ALERT_WAIT" check_alert_mail; then
    pass 'a real alert produced a real email, with nothing stubbed'
  else
    fail 'the alert did not produce an email'
  fi

  alert_mail="$(mail_search "notify-test-$STAMP")"
  contains 'the alert message carries its severity in the subject' "$alert_mail" 'CRITICAL'

  # The body has to be fetched: the search endpoint returns summaries. A
  # notification that says something is wrong without saying where to look costs
  # the reader more than it gives them.
  alert_id="$(printf '%s' "$alert_mail" | grep -o '"ID":"[^"]*"' | head -1 | cut -d'"' -f4)"
  alert_body="$(curl -s --max-time 20 "$MAIL_API/api/v1/message/$alert_id" 2>/dev/null || true)"
  contains 'and says where to go and look' "$alert_body" '/monitoring'

  # --- the deduplication ----------------------------------------------------

  # The monitor re-reads the same open alert every minute. If that produced a
  # message each time, a full disk would be ten thousand emails — so this is the
  # single most important assertion in the file.
  before="$(mail_count "notify-test-$STAMP")"
  if [ "$before" != "1" ]; then
    fail "one alert produced $before messages before waiting at all"
  fi
  sleep 70
  after="$(mail_count "notify-test-$STAMP")"
  if [ "$before" = "$after" ]; then
    pass "an alert that stays open sends one message, not one a minute ($after after 70s)"
  else
    fail "the same open alert sent more messages: $before then $after"
  fi

  # --- the record -----------------------------------------------------------

  deliveries="$(api GET '/api/v1/notifications/deliveries?status=sent')"
  contains 'the delivery record says what was sent' "$deliveries" '"status":"sent"'
  contains 'and which channel it went to' "$deliveries" "integration-$STAMP"

  # --- resolving ------------------------------------------------------------

  # Raising the threshold back clears the condition, and an alert that clears
  # itself is worth telling somebody about: it is the difference between
  # getting up and going back to sleep.
  api PATCH "/api/v1/monitoring/rules/$rule_id" '{"threshold":99.9}' >/dev/null

  check_resolved_mail() { mail_search "notify-test-$STAMP" | grep -q "Resolved:"; }
  if wait_for "$ALERT_WAIT" check_resolved_mail; then
    pass 'an alert clearing on its own also sends a message'
  else
    fail 'no message was sent when the alert resolved'
  fi
fi

# --- a channel that cannot deliver ------------------------------------------

# Pointed at a port nothing is listening on. This is the case the whole phase is
# built around: the panel cannot tell somebody their notifications are broken
# through the thing that is broken, so it has to record it.
broken_body="{\"name\":\"broken-$STAMP\",\"kind\":\"email\",\"host\":\"$MAIL_HOST\",\"port\":9,\"security\":\"none\",\"allow_insecure\":true,\"from\":\"panel@example.com\",\"to\":[\"$RECIPIENT\"]}"
broken="$(api POST /api/v1/notification-channels "$broken_body")"
broken_id="$(json_field "$broken" id)"

if [ -z "$broken_id" ]; then
  fail "could not create a channel to break: $(printf '%s' "$broken" | head -c 300)"
else
  created_channels="$created_channels $broken_id"

  broken_test="$(api POST "/api/v1/notification-channels/$broken_id/test")"
  contains 'a channel that cannot deliver is recorded as failing' \
    "$broken_test" '"failure_streak":1'
  contains 'and the reason is kept where somebody can read it' \
    "$broken_test" '"last_error":"'
  contains 'and it still reads as never having delivered anything' \
    "$broken_test" '"last_success_at":null'

  # The failure is reported on the channel rather than as an error envelope:
  # "we could not reach it, and here is what happened" is the answer.
  expect_status 'testing a broken channel is answered, not failed' 200 \
    "$(api_status POST "/api/v1/notification-channels/$broken_id/test")"

  after_break="$(api GET /api/v1/notifications)"
  contains 'the overview counts the broken channel' "$after_break" '"broken_channels":1'
fi

# --- permissions ------------------------------------------------------------

unauth="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "$API_BASE_URL/api/v1/notifications" 2>/dev/null || true)"
expect_status 'reading the notification settings needs authentication' 401 "$unauth"

# --- result -----------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 20 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
