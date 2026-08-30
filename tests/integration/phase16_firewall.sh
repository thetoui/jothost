#!/bin/sh
# Phase 16 Docker integration test — the firewall.
#
# Black-box checks against the running stack, driving a real ufw against a real
# packet filter. The acceptance is CLAUDE.md section 19: every change is backed
# up, validated, applied provisionally, verified, and undone automatically when
# nothing confirms it.
#
# The dangerous half is testable here because the Agent container has its own
# network namespace and NET_ADMIN: a rule written here filters this container's
# traffic and nothing else, so connectivity can be broken on purpose and the
# rollback watched, without touching the machine running Docker.
#
# The suite leaves the firewall reset and disabled, whatever happens: every
# other suite reaches this container over the network.
#
# Run with:  make docker-test-firewall

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# Whatever happens, this container must not be left filtering traffic: every
# other suite reaches it over the network.
cleanup() {
  ufw --force reset >/dev/null 2>&1 || true
  ufw --force disable >/dev/null 2>&1 || true
  rm -f /var/lib/jothost/firewall/pending.json 2>/dev/null || true
}
trap cleanup EXIT

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 90 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 90 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 90 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 90 -X "$method" "$API_BASE_URL$path" \
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
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 240))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  if [ -z "$needle" ]; then
    fail "$name (the value searched for is empty, so this check proves nothing)"
    return
  fi
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

# change_id extracts the identifier from a change response.
change_id() { printf '%s' "$1" | sed -n 's/.*"id":"\(fwc_[^"]*\)".*/\1/p'; }

# apply_and_confirm makes a change and commits it, which is what the panel does
# once the browser has re-read the firewall over the network.
apply_and_confirm() {
  response="$(api "$1" "$2" "$3")"
  id="$(change_id "$response")"
  if [ -z "$id" ]; then
    printf '%s' "$response"
    return 1
  fi
  api POST "/api/v1/firewall/changes/$id/confirm" >/dev/null
  printf '%s' "$response"
}

# ------------------------------------------------------------------ the run

log 'Phase 16 — firewall'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# Start from nothing, whatever a previous run left behind.
ufw --force reset >/dev/null 2>&1 || true
ufw --force disable >/dev/null 2>&1 || true
rm -f /var/lib/jothost/firewall/pending.json 2>/dev/null || true

# --- 1. what the host has -------------------------------------------------

log ''
log '1. The firewall'

status="$(api GET /api/v1/firewall)"
contains 'the panel reports the firewall' "$status" '"available":true'
contains 'it says whether it is switched on' "$status" '"enabled":false'
contains 'and which ports may never be closed' "$status" '"guarded_ports":[22,80,443]'

# --- 2. rules staged before the firewall is on ----------------------------

log ''
log '2. Staging rules'

# A disabled ufw reports no rules under "status" — it is describing what is
# being enforced, and nothing is. The rules are still there, and the panel has
# to show them: an operator writing five rules before switching on would
# otherwise be working blind, and the guard below would see nothing to protect
# the host with.
added="$(apply_and_confirm POST /api/v1/firewall/rules \
  '{"action":"allow","port":"22","protocol":"tcp","comment":"ssh"}')"
contains 'a rule is applied and reported back' "$added" '"kind":"rule.add"'
contains 'with a deadline to confirm it by' "$added" '"deadline":"'

status="$(api GET /api/v1/firewall)"
contains 'a staged rule is listed while the firewall is off' "$status" '"port":"22"'
contains 'with the note it was given' "$status" '"comment":"ssh"'

# --- 3. the lockout guard -------------------------------------------------

log ''
log '3. What cannot be done'

# Switching on a default-deny firewall with nothing allowing the panel's ports
# would take the panel away, so it is refused — and the refusal names what to
# do about it.
refusal="$(api POST /api/v1/firewall/enable)"
contains 'enabling is refused while the panel ports are unprotected' "$refusal" 'lock this host out'
contains 'and the refusal names the ports' "$refusal" '80'

# The ports the host is administered through cannot be closed, however the rule
# is phrased.
for rule in \
  '{"action":"deny","port":"22","protocol":"tcp"}' \
  '{"action":"reject","port":"22","protocol":"tcp"}' \
  '{"action":"deny","port":"20:25","protocol":"tcp"}' \
  '{"action":"deny","port":"443","protocol":"tcp"}' \
  '{"action":"deny","source":"203.0.113.7","port":"22","protocol":"tcp"}'
do
  code="$(api_status POST /api/v1/firewall/rules "$rule")"
  if [ "$code" = "409" ]; then
    pass "a rule closing an administration port is refused: $rule"
  else
    fail "a rule closing an administration port is refused: $rule (got HTTP $code)"
  fi
done

# And a refused rule never reaches the firewall at all.
not_contains 'the refused rules were never applied' "$(ufw show added)" 'deny'

# Nonsense is refused before it becomes a command.
for rule in \
  '{"action":"drop","port":"8080"}' \
  '{"action":"allow","port":"0"}' \
  '{"action":"allow","port":"70000"}' \
  '{"action":"allow","port":"8080","source":"999.1.1.1"}' \
  '{"action":"allow","port":"8080","comment":"has \"quotes\""}'
do
  code="$(api_status POST /api/v1/firewall/rules "$rule")"
  if [ "$code" = "422" ] || [ "$code" = "400" ]; then
    pass "an unusable rule is refused: $rule"
  else
    fail "an unusable rule is refused: $rule (got HTTP $code)"
  fi
done

# --- 4. switching it on ---------------------------------------------------

log ''
log '4. Switching it on'

for port in 80 443; do
  apply_and_confirm POST /api/v1/firewall/rules \
    "{\"action\":\"allow\",\"port\":\"$port\",\"protocol\":\"tcp\"}" >/dev/null
done

enabled="$(apply_and_confirm POST /api/v1/firewall/enable '')"
contains 'the firewall can be switched on once the ports are allowed' "$enabled" '"kind":"enable"'

# What the host is actually enforcing, read from ufw rather than from the panel.
running="$(ufw status)"
contains 'the packet filter is active' "$running" 'Status: active'
contains 'and SSH is allowed through it' "$running" '22/tcp'

status="$(api GET /api/v1/firewall)"
contains 'the panel agrees that it is on' "$status" '"enabled":true'
contains 'and reports the default policy' "$status" '"default_incoming":"deny"'

# --- 5. the rollback ------------------------------------------------------

log ''
log '5. What happens when nobody confirms'

# This is CLAUDE.md section 19 in one check. The rule is applied and live; the
# confirmation never comes — which is exactly what happens when a change cuts
# the panel off — and the Agent puts the host back on its own.
provisional="$(api POST /api/v1/firewall/rules \
  '{"action":"allow","port":"3306","protocol":"tcp","window_seconds":5}')"
change="$(change_id "$provisional")"
if [ -z "$change" ]; then
  fail "the provisional change was not accepted: $(printf '%s' "$provisional" | head -c 200)"
else
  pass 'a change is applied provisionally'

  # It is live immediately: this is a real rule, not a proposal.
  contains 'the rule is enforced straight away' "$(ufw status)" '3306'

  status="$(api GET /api/v1/firewall)"
  contains 'the panel reports it as waiting to be confirmed' "$status" "\"id\":\"$change\""

  # Nothing else may change while one is in flight: two overlapping windows
  # would each hold a backup of a state the other had moved away from.
  code="$(api_status POST /api/v1/firewall/rules '{"action":"allow","port":"5432","protocol":"tcp"}')"
  expect_status 'a second change is refused while one is unconfirmed' 409 "$code"

  waited=0
  while [ "$waited" -lt 25 ]; do
    case "$(ufw status)" in
      *3306*) ;;
      *) break ;;
    esac
    sleep 1
    waited=$((waited + 1))
  done

  not_contains 'the unconfirmed change is undone by itself' "$(ufw status)" '3306'
  not_contains 'and the panel stops reporting it' "$(api GET /api/v1/firewall)" '"pending"'

  # The host is still exactly as it was: the rollback restored the rules, it
  # did not merely remove the one rule.
  contains 'the rules it had before are back' "$(ufw status)" '22/tcp'
  contains 'and it is still switched on' "$(ufw status)" 'Status: active'
fi

# --- 6. confirming keeps it ------------------------------------------------

log ''
log '6. Confirming'

kept="$(api POST /api/v1/firewall/rules '{"action":"allow","port":"5432","protocol":"tcp","window_seconds":5}')"
change="$(change_id "$kept")"
if [ -z "$change" ]; then
  fail 'the change to confirm was not accepted'
else
  confirmed="$(api POST "/api/v1/firewall/changes/$change/confirm")"
  contains 'confirming answers with the change it committed' "$confirmed" "\"id\":\"$change\""

  sleep 8
  contains 'a confirmed rule is still there after the window would have closed' \
    "$(ufw status)" '5432'
fi

# --- 7. rolling back on request -------------------------------------------

log ''
log '7. Rolling back on request'

undone="$(api POST /api/v1/firewall/rules '{"action":"allow","port":"6379","protocol":"tcp"}')"
change="$(change_id "$undone")"
if [ -z "$change" ]; then
  fail 'the change to roll back was not accepted'
else
  contains 'the rule is live before the rollback' "$(ufw status)" '6379'
  api POST "/api/v1/firewall/changes/$change/rollback" >/dev/null
  not_contains 'rolling back removes it at once' "$(ufw status)" '6379'
  # Without waiting for the window: the rollback is immediate.
  contains 'and leaves the rest alone' "$(ufw status)" '5432'
fi

# A change that is gone cannot be confirmed.
code="$(api_status POST "/api/v1/firewall/changes/$change/confirm")"
expect_status 'confirming a change that is over is refused' 404 "$code"

# --- 8. deleting a rule ---------------------------------------------------

log ''
log '8. Deleting'

apply_and_confirm DELETE /api/v1/firewall/rules \
  '{"action":"allow","port":"5432","protocol":"tcp"}' >/dev/null
not_contains 'a deleted rule stops being enforced' "$(ufw status)" '5432'

# The rule that keeps SSH open cannot be removed while the firewall would then
# close it: a lockout with an extra step is still a lockout.
code="$(api_status DELETE /api/v1/firewall/rules '{"action":"allow","port":"22","protocol":"tcp"}')"
expect_status 'removing the rule that keeps SSH open is refused' 409 "$code"

# --- 9. authorization ------------------------------------------------------

log ''
log '9. Authorization'

anon="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$API_BASE_URL/api/v1/firewall" || true)"
expect_status 'reading the firewall without a token is refused' 401 "$anon"

anon="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST \
  "$API_BASE_URL/api/v1/firewall/rules" || true)"
expect_status 'changing the firewall without a token is refused' 401 "$anon"

# --- 10. leaving the host as it was ---------------------------------------

log ''
log '10. Cleanup'

apply_and_confirm POST /api/v1/firewall/disable '' >/dev/null
not_contains 'the firewall can be switched off again' "$(ufw status)" 'Status: active'

cleanup
contains 'the host is left unfiltered' "$(ufw status)" 'Status: inactive'

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 16 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
