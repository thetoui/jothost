#!/bin/sh
# Phase 18 Docker integration test — intrusion prevention.
#
# Black-box checks against the running stack, run inside the Agent's own
# container so that what the panel says can be compared with what the daemon
# says — and so that a ban can be *caused* rather than simulated.
#
# The check this suite exists for is section 5: real authentication failures are
# written to the log fail2ban is watching, and the suite waits for the daemon to
# decide to ban the address they came from. Everything else could pass against a
# panel that wrote a perfectly formed configuration nothing read, which is
# exactly what happened during development — fail2ban reads its drop-ins in an
# order that made the panel's file lose to the distribution's, silently.
#
# The addresses used are TEST-NET-3 (203.0.113.0/24), reserved for documentation
# and routed nowhere.
#
# Run with:  make docker-test-fail2ban

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

DROP_IN="/etc/fail2ban/jail.d/99-jothost.local"
AUTH_LOG="/var/log/messages"
ATTACKER="203.0.113.77"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

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
    curl -s --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

anon_status() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X "$1" "$API_BASE_URL$2" 2>/dev/null || true
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 260))" ;;
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

# effective JAIL OPTION — what the daemon resolved, which is the answer the
# panel has to agree with.
effective() {
  fail2ban-client get "$1" "$2" 2>/dev/null | head -n 1 | tr -d ' '
}

# The host is put back as it was found, whatever happens in between.
restore() {
  fail2ban-client set sshd unbanip "$ATTACKER" >/dev/null 2>&1 || true
  if [ -n "${token:-}" ]; then
    api PATCH /api/v1/security/fail2ban/jails/sshd '{"enabled":false}' >/dev/null 2>&1 || true
  fi
  rm -f "$DROP_IN" "$DROP_IN.backup" 2>/dev/null || true
  rc-service fail2ban stop >/dev/null 2>&1 || true
}
trap restore EXIT

# ------------------------------------------------------------------ the run

log 'Phase 18 — intrusion prevention'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# --- 1. authorization -----------------------------------------------------

log ''
log '1. Authorization'

expect_status 'reading the settings without a token is refused' 401 \
  "$(anon_status GET /api/v1/security/fail2ban)"
expect_status 'unbanning without a token is refused' 401 \
  "$(anon_status POST /api/v1/security/fail2ban/unban)"
expect_status 'installing without a token is refused' 401 \
  "$(anon_status POST /api/v1/security/fail2ban/install)"

# --- 2. what this host has ------------------------------------------------

log ''
log '2. What this host has'

status="$(api GET /api/v1/security/fail2ban)"
contains 'the panel reports the jails' "$status" '"jails":'
contains 'fail2ban is installed here' "$status" '"available":true'
contains 'and the panel says which file it writes' "$status" "$DROP_IN"
# The name is what makes the file win: fail2ban reads .conf files before .local
# ones and the last value of an option is the one it uses.
contains 'the drop-in is a .local so the distribution cannot override it' \
  "$status" '99-jothost.local'

contains 'the SSH jail is offered' "$status" '"name":"sshd"'
contains 'with words an operator can read' "$status" '"label":"SSH"'

# --- 3. configuring a jail -------------------------------------------------

log ''
log '3. Configuring a jail'

applied="$(api PATCH /api/v1/security/fail2ban/jails/sshd \
  '{"enabled":true,"max_retry":3,"ban_time":600,"find_time":600}')"
contains 'a jail can be switched on' "$applied" '"enabled":true'
contains 'with the policy that was asked for' "$applied" '"max_retry":3'

contains 'the drop-in holds the jail' "$(cat "$DROP_IN")" '[sshd]'
contains 'and the threshold' "$(cat "$DROP_IN")" 'maxretry = 3'
# The distribution knows which filter its own log format needs — Alpine's sshd
# jail uses one written for BusyBox's syslog prefix — so the panel does not
# write one.
not_contains 'the panel does not write a filter' "$(cat "$DROP_IN")" 'filter'

api POST /api/v1/services/fail2ban/start '{}' >/dev/null
sleep 4

# The check this phase turns on: what the daemon resolved, not what was written.
if [ "$(effective sshd maxretry)" = "3" ]; then
  pass 'the daemon is running the threshold the panel set'
else
  fail "the daemon is running the threshold the panel set (it says $(effective sshd maxretry))"
fi

status="$(api GET /api/v1/security/fail2ban)"
contains 'and the panel reports the daemon as running' "$status" '"running":true'

# --- 4. addresses that are never banned ------------------------------------

log ''
log '4. Never banned'

ignored="$(api PUT /api/v1/security/fail2ban/ignored '{"ignored":["203.0.113.200"]}')"
contains 'an address can be exempted' "$ignored" '203.0.113.200'
# Loopback is added whether it was asked for or not: a host that has banned its
# own loopback has broken every local service that talks to another over it.
contains 'and loopback is added even when it was not asked for' "$ignored" '127.0.0.1/8'

sleep 2
if fail2ban-client get sshd ignoreip 2>/dev/null | grep -q '203.0.113.200'; then
  pass 'the daemon has the exemption'
else
  fail 'the daemon has the exemption'
fi

# --- 5. a real ban ---------------------------------------------------------

log ''
log '5. The daemon actually bans'

# Real sshd failure lines, written to the log the jail is watching. The filter
# has to match them, the counter has to reach the threshold, and the action has
# to run — none of which a mock would prove.
i=1
while [ "$i" -le 4 ]; do
  now="$(date '+%b %e %H:%M:%S')"
  printf '%s jothost-agent auth.info sshd[90%s]: Failed password for invalid user admin from %s port 5432%s ssh2\n' \
    "$now" "$i" "$ATTACKER" "$i" >> "$AUTH_LOG"
  sleep 1
  i=$((i + 1))
done

log '  ...waiting up to 60s for the daemon to decide'
waited=0
banned=no
while [ "$waited" -lt 60 ]; do
  if api GET /api/v1/security/fail2ban/banned | grep -q "$ATTACKER"; then
    banned=yes
    break
  fi
  sleep 3
  waited=$((waited + 3))
done

if [ "$banned" = yes ]; then
  pass "the address was banned after real failures (in ${waited}s)"
else
  fail 'the address was banned after real failures'
  log "  ...jail says: $(fail2ban-client status sshd 2>&1 | tr '\n' ' ' | head -c 200)"
fi

# A ban is a firewall rule, and this is where it is.
if iptables -S 2>/dev/null | grep -q "$ATTACKER"; then
  pass 'and a firewall rule is blocking it'
else
  fail 'and a firewall rule is blocking it'
fi

banned_list="$(api GET /api/v1/security/fail2ban/banned)"
contains 'the panel lists it with the jail that banned it' "$banned_list" '"jail":"sshd"'

status="$(api GET /api/v1/security/fail2ban)"
contains 'and counts it' "$status" '"banned":1'

# --- 6. unbanning ----------------------------------------------------------

log ''
log '6. Unbanning'

released="$(api POST /api/v1/security/fail2ban/unban \
  "{\"jail\":\"sshd\",\"address\":\"$ATTACKER\"}")"
contains 'an address can be released' "$released" '"banned":false'

sleep 2
not_contains 'and is gone from the list' "$(api GET /api/v1/security/fail2ban/banned)" "$ATTACKER"
if iptables -S 2>/dev/null | grep -q "$ATTACKER"; then
  fail 'and its firewall rule is gone'
else
  pass 'and its firewall rule is gone'
fi

# fail2ban answers "0" and exits zero when it removed nothing, which is a
# success code for a call that did not do what was asked.
expect_status 'unbanning something that is not banned is a 404' 404 \
  "$(api_status POST /api/v1/security/fail2ban/unban \
    "{\"jail\":\"sshd\",\"address\":\"$ATTACKER\"}")"

# --- 7. banning by hand ----------------------------------------------------

log ''
log '7. Banning by hand'

manual="$(api POST /api/v1/security/fail2ban/ban \
  '{"jail":"sshd","address":"203.0.113.123"}')"
contains 'an address can be banned deliberately' "$manual" '"banned":true'
sleep 2
contains 'and it appears in the list' "$(api GET /api/v1/security/fail2ban/banned)" '203.0.113.123'
api POST /api/v1/security/fail2ban/unban '{"jail":"sshd","address":"203.0.113.123"}' >/dev/null

# --- 8. what is refused ----------------------------------------------------

log ''
log '8. Refusals'

# An address becomes an argument to fail2ban-client and a firewall rule. A jail
# name becomes a section header and an argument. Neither is taken on trust.
for bad in 'not-an-ip' '203.0.113.7; id' 'evil.example.com' '' '203.0.113.0/99'; do
  code="$(api_status POST /api/v1/security/fail2ban/unban \
    "{\"jail\":\"sshd\",\"address\":\"$bad\"}")"
  expect_status "an address that is not one is refused: ${bad:-(empty)}" 422 "$code"
done

for bad in '../../etc/passwd' 'sshd;id' 'a b'; do
  code="$(api_status POST /api/v1/security/fail2ban/unban \
    "{\"jail\":\"$bad\",\"address\":\"203.0.113.7\"}")"
  expect_status "a jail name that is not one is refused: $bad" 422 "$code"
done

expect_status 'a jail the panel does not offer is a 404' 404 \
  "$(api_status PATCH /api/v1/security/fail2ban/jails/invented '{"enabled":true}')"

# A ban shorter than the window failures are counted in means the counter never
# resets: the address is banned again the moment it is released.
expect_status 'a ban shorter than the counting window is refused' 422 \
  "$(api_status PATCH /api/v1/security/fail2ban/jails/sshd \
    '{"find_time":3600,"ban_time":600}')"
expect_status 'a threshold nothing would ever reach is refused' 422 \
  "$(api_status PATCH /api/v1/security/fail2ban/jails/sshd '{"max_retry":5000}')"
expect_status "fail2ban's permanent ban is refused" 422 \
  "$(api_status PATCH /api/v1/security/fail2ban/jails/sshd '{"ban_time":-1}')"
expect_status 'an unknown field is refused rather than ignored' 400 \
  "$(api_status PATCH /api/v1/security/fail2ban/jails/sshd '{"filter":"custom"}')"
expect_status 'an address that is not one cannot be exempted' 422 \
  "$(api_status PUT /api/v1/security/fail2ban/ignored '{"ignored":["nonsense"]}')"

# Starting and stopping the daemon is the service manager's job. Two places that
# start the same thing is how a panel comes to disagree with itself.
expect_status 'the daemon is started from the Services page, not here' 409 \
  "$(api_status POST /api/v1/security/fail2ban/enable '{}')"

# --- 9. the log ------------------------------------------------------------

log ''
log '9. Its own log'

# fail2ban's log is a source the log viewer offers, which is the extension point
# Phase 11 was built around — this phase writes no viewer of its own.
sources="$(api GET /api/v1/logs)"
contains 'fail2ban is a log source' "$sources" '"key":"fail2ban"'
viewer="$(api GET '/api/v1/logs/fail2ban?limit=5')"
contains 'and the viewer serves it' "$viewer" '/var/log/fail2ban.log'

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 18 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
