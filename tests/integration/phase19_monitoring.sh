#!/bin/sh
# Phase 19 Docker integration test — monitoring and the alert engine.
#
# The acceptance is not that the API returns 200. It is that a rule pointed at a
# real filesystem on this host opens a real alert with the real reading in it,
# that acknowledging does not resolve it, that the condition clearing does, and
# that a breach shorter than its rule's duration opens nothing at all.
#
# That last one is the phase's whole argument. A monitor that fires on a single
# reading gets muted within a week, and a muted monitor is worse than none —
# so the test proves the panel stays quiet when it should.
#
# It runs inside the agent container, which is the managed host in development.
#
# Run with:  make docker-test-monitoring

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# How long to wait for the monitor's loop to come round. It ticks once a minute.
WAIT_SECONDS="${WAIT_SECONDS:-150}"

failures=0
created_rules=""
trap 'cleanup' EXIT

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  # Every rule this test made goes, whatever happened, so a rerun starts clean
  # and the host is not left with a threshold nobody chose.
  for id in $created_rules; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/monitoring/rules/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
}

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
    curl -s --max-time 60 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 60 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 60 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 60 -X "$method" "$API_BASE_URL$path" \
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
  case "$haystack" in
    *"$needle"*) fail "$name (unexpectedly found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

first_id() {
  printf '%s' "$1" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1
}

# alerts_for prints the open alerts whose target is the one given.
alerts_for() {
  api GET "/api/v1/monitoring/alerts?status=open" |
    tr '{' '\n' | grep "\"target\":\"$1\"" || true
}

# wait_for polls until a command succeeds or the budget runs out.
wait_for() {
  waited=0
  while [ "$waited" -lt "$WAIT_SECONDS" ]; do
    if "$@"; then
      return 0
    fi
    sleep 5
    waited=$((waited + 5))
  done
  return 1
}

log '== Phase 19: monitoring =='

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

# --- what the panel starts watching ----------------------------------------

rules="$(api GET /api/v1/monitoring/rules)"
contains 'a host starts with rules rather than nothing' "$rules" '"metric":"disk"'
contains 'the supported metrics are advertised' "$rules" '"metrics":['

# A default rule that fired on a single reading would be the thing this phase
# exists to avoid, so the defaults all carry a duration.
not_contains 'no default rule fires on a single reading' "$rules" '"for_seconds":0'

overview="$(api GET /api/v1/monitoring)"
contains 'the overview counts what is open' "$overview" '"counts":'
contains 'and reports what each service is doing' "$overview" '"services":'
contains 'with how long it has been that way' "$overview" '"for_seconds"'

# The panel records service state as it polls, so nginx — which is running in
# this container — is there with a start time.
contains 'a running service is recorded' "$overview" '"service":"nginx"'

history="$(api GET /api/v1/monitoring/services/nginx)"
contains 'a service has a state history' "$history" '"running":true'

# --- a rule that must fire -------------------------------------------------
#
# Pointed at a filesystem that certainly exists, with a threshold no disk can be
# under, so the only reason it would not fire is the engine not working.

# The mount point is discovered rather than assumed. This container reports
# /run/jothost and /tests and no "/" at all, and a test that hardcoded a
# filesystem would be testing the container's layout rather than the engine.
MOUNT="$(api GET /api/v1/dashboard |
  sed -n 's/.*"filesystems":\[{[^}]*"mount_point":"\([^"]*\)".*/\1/p' | head -1)"
if [ -z "$MOUNT" ]; then
  log 'FATAL: this host reports no filesystems, so the alert engine cannot be exercised'
  exit 1
fi
pass "a filesystem to watch was found ($MOUNT)"

critical="$(api POST /api/v1/monitoring/rules \
  "{\"name\":\"Integration disk probe\",\"metric\":\"disk\",\"target\":\"$MOUNT\",\"comparison\":\"above\",\"threshold\":0,\"for_seconds\":0,\"severity\":\"critical\"}")"
rule_id="$(first_id "$critical")"
if [ -z "$rule_id" ]; then
  log "FATAL: the probe rule was not created: $critical"
  exit 1
fi
created_rules="$created_rules $rule_id"
pass 'a rule pointed at one filesystem is accepted'

if wait_for sh -c "[ -n \"\$(curl -s --max-time 20 '$API_BASE_URL/api/v1/monitoring/alerts?status=open' -H 'Authorization: Bearer $token' | tr '{' '\n' | grep '\"target\":\"$MOUNT\"' | grep '\"severity\":\"critical\"')\" ]"; then
  pass 'the alert engine opened an alert for it'
else
  fail "no alert opened for $MOUNT within ${WAIT_SECONDS}s"
fi

alert="$(alerts_for "$MOUNT" | grep '"severity":"critical"' | head -1)"
contains 'the alert carries the reading that opened it' "$alert" '"value":'
contains 'and the threshold it crossed' "$alert" '"threshold":0'
contains 'and says so in words' "$alert" 'is above 0%'
contains 'and it is open' "$alert" '"status":"open"'

alert_id="$(printf '%s' "$alert" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)"

# --- acknowledging is not resolving ----------------------------------------

acked="$(api POST "/api/v1/monitoring/alerts/$alert_id/acknowledge")"
contains 'acknowledging is recorded' "$acked" '"acknowledged_at":'
contains 'and leaves the alert open' "$acked" '"status":"open"'

# There is no endpoint that resolves one: whether a condition has cleared is a
# fact about the machine, not a thing a person can assert. Any non-success is
# the right answer here — the route simply is not there — so what is checked is
# that it did not work rather than which flavour of "no" came back.
resolve_status="$(api_status POST "/api/v1/monitoring/alerts/$alert_id/resolve")"
case "$resolve_status" in
  2*) fail "an alert was resolved by hand (HTTP $resolve_status)" ;;
  *) pass "there is no way to resolve an alert by hand (HTTP $resolve_status)" ;;
esac

# --- a second evaluation does not open a second alert ----------------------

before="$(api GET '/api/v1/monitoring/alerts?status=open')"
before_count="$(printf '%s' "$before" | sed -n 's/.*"count":\([0-9]*\).*/\1/p')"
sleep 65
after="$(api GET '/api/v1/monitoring/alerts?status=open')"
after_count="$(printf '%s' "$after" | sed -n 's/.*"count":\([0-9]*\).*/\1/p')"
if [ "${before_count:-0}" = "${after_count:-1}" ]; then
  pass "a still-breaching condition did not open a second alert ($after_count open)"
else
  fail "the open count went from $before_count to $after_count"
fi

# --- the condition clearing resolves it ------------------------------------
#
# Disabling the rule is the clean way to make the condition stop being watched,
# and an alert left open under a rule nobody applies would be a warning that can
# never clear.

api PATCH "/api/v1/monitoring/rules/$rule_id" '{"enabled":false}' >/dev/null
if wait_for sh -c "[ -z \"\$(curl -s --max-time 20 '$API_BASE_URL/api/v1/monitoring/alerts?status=open' -H 'Authorization: Bearer $token' | tr '{' '\n' | grep '\"target\":\"$MOUNT\"' | grep '\"severity\":\"critical\"')\" ]"; then
  pass 'disabling the rule resolved its alert'
else
  fail 'the alert stayed open under a disabled rule'
fi

resolved="$(api GET '/api/v1/monitoring/alerts?status=resolved')"
contains 'the resolved alert is kept as history' "$resolved" '"status":"resolved"'
contains 'with the worst reading it saw' "$resolved" '"worst":'

# --- a breach shorter than its duration opens nothing -----------------------
#
# The phase's whole argument. This rule is breaching from the moment it exists
# and requires an hour of it, so a monitor that fired here would be one somebody
# mutes within a week.

patient="$(api POST /api/v1/monitoring/rules \
  "{\"name\":\"Integration patience probe\",\"metric\":\"disk\",\"target\":\"$MOUNT\",\"comparison\":\"above\",\"threshold\":0,\"for_seconds\":3600,\"severity\":\"warning\"}")"
patient_id="$(first_id "$patient")"
if [ -n "$patient_id" ]; then
  created_rules="$created_rules $patient_id"
  sleep 65
  quiet="$(alerts_for "$MOUNT" | grep '"severity":"warning"' | head -1 || true)"
  if [ -z "$quiet" ]; then
    pass 'a breach shorter than its rule required opened nothing'
  else
    fail "a two-minute breach opened an alert that needed an hour: $quiet"
  fi
else
  fail 'the patience probe rule was not created'
fi

# --- what the panel refuses ------------------------------------------------

expect_status 'a metric this panel cannot measure is refused' 422 \
  "$(api_status POST /api/v1/monitoring/rules \
    '{"name":"Temperature","metric":"temperature","threshold":50}')"

expect_status 'a percentage threshold above 100 is refused' 422 \
  "$(api_status POST /api/v1/monitoring/rules \
    '{"name":"Impossible","metric":"memory","target":"x","threshold":150}')"

expect_status 'a service rule with no service is refused' 422 \
  "$(api_status POST /api/v1/monitoring/rules \
    '{"name":"Something down","metric":"service","comparison":"below","threshold":1}')"

expect_status 'a rule with no name is refused' 422 \
  "$(api_status POST /api/v1/monitoring/rules \
    '{"name":"   ","metric":"cpu","target":"probe","threshold":90}')"

expect_status 'a duration longer than a day is refused' 422 \
  "$(api_status POST /api/v1/monitoring/rules \
    '{"name":"Forever","metric":"cpu","target":"probe2","threshold":90,"for_seconds":90000}')"

# Two rules watching the same thing at the same severity would both fire.
expect_status 'a duplicate rule is refused' 409 \
  "$(api_status POST /api/v1/monitoring/rules \
    "{\"name\":\"Duplicate\",\"metric\":\"disk\",\"target\":\"$MOUNT\",\"threshold\":50,\"severity\":\"critical\"}")"

# A rule that watched something else would be a different rule, and the alerts
# it had already opened would describe a condition it never observed.
expect_status 'a rule cannot be pointed at a different metric' 422 \
  "$(api_status PATCH "/api/v1/monitoring/rules/$rule_id" '{"metric":"cpu"}')"

# --- aggregated history ----------------------------------------------------

series="$(api GET "/api/v1/servers/$(api GET /api/v1/servers | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)/metrics?range=90d")"
contains 'a long range is served' "$series" '"range":"90d"'
contains 'with a bucket wider than the raw ones' "$series" '"bucket":"6h0m0s"'

expect_status 'an unsupported range is still refused' 400 \
  "$(api_status GET "/api/v1/servers/$(api GET /api/v1/servers | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1)/metrics?range=5y")"

# --- permissions -----------------------------------------------------------

unauth="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "$API_BASE_URL/api/v1/monitoring" 2>/dev/null || true)"
expect_status 'reading the monitor needs authentication' 401 "$unauth"

# --- result ---------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 19 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
