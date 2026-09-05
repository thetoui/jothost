#!/bin/sh
# Phase 15 Docker integration test — the Security Center.
#
# The acceptance is not that the API returns 200. It is that a real weakness
# introduced on this host is found by a real scan, that a check which cannot run
# is reported as unknown rather than as a pass, and that neither accepting a
# risk nor anything else lets a person mark a real problem as fixed.
#
# Four things are proved here that nothing else can prove:
#
#   * A service bound to 0.0.0.0 on this host is found by reading /proc, named,
#     and graded critical — and the same service on 127.0.0.1 is not.
#   * A world-writable file and an exposed .env, created for the test in a real
#     site's document root, are found by a real filesystem walk.
#   * Fixing something makes the finding go away by itself on the next scan,
#     and a rescan that finds the same thing does not duplicate it.
#   * Accepting requires a reason, and there is no way to resolve by hand.
#
# It runs inside the agent container, which is the managed host in development.
#
# Run with:  make docker-test-security

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DOMAIN="sec${STAMP}.test"
# A port nothing else uses, that the panel treats as sensitive: 6379 is Redis,
# which by default requires no password at all.
EXPOSED_PORT=6379

failures=0
website_id=""
listener_pid=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  if [ -n "$listener_pid" ]; then
    kill "$listener_pid" 2>/dev/null || true
  fi
  if [ -n "$website_id" ]; then
    curl -s -o /dev/null -X DELETE \
      "$API_BASE_URL/api/v1/websites/$website_id?remove_files=true" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  fi
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
    curl -s --max-time 900 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 900 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 900 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 900 -X "$method" "$API_BASE_URL$path" \
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

await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 180 ]; do
    state="$(json_field "$(api GET "/api/v1/jobs/$job_id")" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

# finding_block prints the one finding object whose fingerprint matches.
finding_block() {
  api GET '/api/v1/security/findings?status=open' |
    tr '{' '\n' | grep "\"fingerprint\":\"$1\"" || true
}

# finding_id extracts the id of the finding with the given fingerprint.
finding_id() {
  api GET '/api/v1/security/findings?status=open' |
    tr '{' '\n' | grep "\"fingerprint\":\"$1\"" |
    sed -n 's/.*"id":"\([0-9a-f-]*\)".*/\1/p' | head -1
}

log '== Phase 15: the Security Center =='

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

# --- a host nobody has scanned ---------------------------------------------

# This runs against a shared stack, so the host may already have been scanned by
# an earlier run. Only the never-scanned shape is asserted when it applies.
before="$(api GET /api/v1/security/score)"
case "$before" in
  *'"last_scan":null'*)
    contains 'an unscanned host says so rather than showing a score' \
      "$before" '"grade":"unknown"'
    ;;
  *)
    pass 'this host has been scanned before (the never-scanned case is covered by unit tests)'
    ;;
esac

# --- introduce a real weakness ----------------------------------------------

created="$(api POST /api/v1/websites "{\"domain\":\"$DOMAIN\"}")"
website_id="$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')"
create_job="$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
if [ -z "$website_id" ]; then
  log "FATAL: could not create $DOMAIN: $(printf '%s' "$created" | head -c 300)"
  exit 1
fi
state="$(await_job "$create_job")"
if [ "$state" != "SUCCESS" ]; then
  log "FATAL: provisioning $DOMAIN ended $state"
  exit 1
fi
pass "a website was created to scan ($DOMAIN)"

site_root="$(json_field "$(api GET "/api/v1/websites/$website_id?remove_files=true")" document_root)"
if [ -z "$site_root" ] || [ ! -d "$site_root" ]; then
  log "FATAL: $DOMAIN has no document root on disk"
  exit 1
fi

# A world-writable file, and a .env inside the served directory. Both are things
# a real deployment does by accident, and both are what the scanner is for.
printf '<?php\n' > "$site_root/uploads.php"
chmod 0666 "$site_root/uploads.php"
printf 'DB_PASSWORD=hunter2\n' > "$site_root/.env"
pass 'a world-writable file and an exposed .env were created in the site'

# A service on 0.0.0.0. nc is BusyBox's, which is in this image; the point is
# that something real is bound to a real public address and the scanner has to
# find it by reading /proc rather than by being told.
nc -l -p "$EXPOSED_PORT" -s 0.0.0.0 >/dev/null 2>&1 &
listener_pid=$!
sleep 1
if kill -0 "$listener_pid" 2>/dev/null; then
  pass "a service was bound to 0.0.0.0:$EXPOSED_PORT"
else
  fail "could not bind a listener on $EXPOSED_PORT"
  listener_pid=""
fi

# --- the scan ---------------------------------------------------------------

scan="$(api POST /api/v1/security/scan)"
contains 'a scan reports how many checks ran' "$scan" '"checks_run":'
contains 'a scan reports how many there were in total' "$scan" '"checks_total":7'
contains 'a scan records what each scanner did' "$scan" '"scanners":['

# --- what it found ----------------------------------------------------------

exposed="$(finding_block "ports:public:$EXPOSED_PORT")"
if [ -n "$exposed" ]; then
  pass "the service on 0.0.0.0:$EXPOSED_PORT was found"
  contains 'an exposed database port is graded critical' "$exposed" '"severity":"critical"'
  contains 'and the finding says what the port is for' "$exposed" 'Redis'
  contains 'and says what to do about it' "$exposed" '"remediation":"'
else
  fail "the service on 0.0.0.0:$EXPOSED_PORT was not found by the port scan"
fi

# The panel's own database is on 127.0.0.1 in this container and must not be
# reported: the whole value of the scanner is the distinction between the two.
not_contains 'a service on loopback is not reported as exposed' \
  "$(api GET '/api/v1/security/findings?status=open')" '"fingerprint":"ports:public:3306"'

writable="$(finding_block 'permissions:world_writable')"
if [ -n "$writable" ]; then
  pass 'the world-writable file was found'
  contains 'a world-writable file under a site is graded high' "$writable" '"severity":"high"'
else
  fail 'the world-writable file was not found by the permission scan'
fi

secret="$(finding_block 'permissions:exposed_secret')"
if [ -n "$secret" ]; then
  pass 'the .env inside the document root was found'
  contains 'an exposed credential file is graded critical' "$secret" '"severity":"critical"'
else
  fail 'the .env inside the document root was not found'
fi

# --- the score --------------------------------------------------------------

overview="$(api GET /api/v1/security/score)"
contains 'the score is reported with the checks behind it' "$overview" '"checks_total":7'
contains 'the score carries a grade in words' "$overview" '"grade":"'
contains 'the counts separate critical from the rest' "$overview" '"critical":'

# A host with a wide-open Redis and an exposed .env cannot be scoring well.
score="$(printf '%s' "$overview" | sed -n 's/.*"score":{"value":\([0-9]*\).*/\1/p')"
if [ -n "$score" ] && [ "$score" -lt 70 ]; then
  pass "a host with real critical findings scores badly ($score)"
else
  fail "a host with a wide-open Redis and an exposed .env scored $score"
fi

# --- a rescan does not duplicate --------------------------------------------

before_count="$(api GET '/api/v1/security/findings?status=open' |
  grep -o '"fingerprint":' | wc -l | tr -d ' ')"
api POST /api/v1/security/scan >/dev/null
after_count="$(api GET '/api/v1/security/findings?status=open' |
  grep -o '"fingerprint":' | wc -l | tr -d ' ')"

if [ "$before_count" = "$after_count" ]; then
  pass "a rescan updates rather than duplicating ($after_count findings both times)"
else
  fail "a rescan changed the finding count from $before_count to $after_count"
fi

# --- accepting --------------------------------------------------------------

exposed_id="$(finding_id "ports:public:$EXPOSED_PORT")"
if [ -z "$exposed_id" ]; then
  fail 'no finding to accept'
else
  # A mute button on a security page is how a real problem becomes permanent.
  expect_status 'accepting with no reason is refused' 422 \
    "$(api_status PATCH "/api/v1/security/findings/$exposed_id" \
       '{"status":"accepted","reason":""}')"
  expect_status 'accepting with a reason too short to be one is refused' 422 \
    "$(api_status PATCH "/api/v1/security/findings/$exposed_id" \
       '{"status":"accepted","reason":"ok"}')"

  # Whether a weakness still exists is the scanner's to decide.
  expect_status 'a finding cannot be marked resolved by hand' 422 \
    "$(api_status PATCH "/api/v1/security/findings/$exposed_id" \
       '{"status":"resolved","reason":"we fixed it"}')"

  accepted="$(api PATCH "/api/v1/security/findings/$exposed_id" \
    '{"status":"accepted","reason":"this port is only reachable inside the rack"}')"
  contains 'a risk can be accepted with a reason' "$accepted" '"status":"accepted"'
  contains 'and the reason is kept' "$accepted" 'only reachable inside the rack'
  contains 'and the severity at the time is kept' "$accepted" '"accepted_severity":"critical"'

  after_accept="$(api GET /api/v1/security/score)"
  contains 'an accepted risk is counted and never hidden' "$after_accept" '"accepted":1'
  not_contains 'an accepted risk is no longer outstanding' \
    "$(api GET '/api/v1/security/findings?status=open')" \
    "\"fingerprint\":\"ports:public:$EXPOSED_PORT\""

  reopened="$(api PATCH "/api/v1/security/findings/$exposed_id" \
    '{"status":"open","reason":""}')"
  contains 'an acceptance can be withdrawn' "$reopened" '"status":"open"'
fi

# --- fixing something makes the finding go away -----------------------------

# This is the half that a page of checkboxes could never do: the panel notices
# on its own, because the scanner no longer finds it.
chmod 0644 "$site_root/uploads.php"
rm -f "$site_root/.env"
if [ -n "$listener_pid" ]; then
  kill "$listener_pid" 2>/dev/null || true
  listener_pid=""
  sleep 1
fi

api POST /api/v1/security/scan >/dev/null
after_fix="$(api GET '/api/v1/security/findings?status=open')"

not_contains 'fixing the permissions resolved the finding by itself' \
  "$after_fix" '"fingerprint":"permissions:world_writable"'
not_contains 'removing the .env resolved its finding by itself' \
  "$after_fix" '"fingerprint":"permissions:exposed_secret"'
not_contains 'stopping the service resolved its finding by itself' \
  "$after_fix" "\"fingerprint\":\"ports:public:$EXPOSED_PORT\""

# --- refusals ---------------------------------------------------------------

expect_status 'an unknown scanner is refused' 422 \
  "$(api_status GET '/api/v1/security/findings?scanner=telepathy')"
expect_status 'an unknown severity is refused' 422 \
  "$(api_status GET '/api/v1/security/findings?severity=catastrophic')"
expect_status 'a finding that does not exist is a 404' 404 \
  "$(api_status PATCH '/api/v1/security/findings/00000000-0000-0000-0000-000000000000' \
     '{"status":"accepted","reason":"this does not exist"}')"

# --- history and permissions ------------------------------------------------

history="$(api GET '/api/v1/security/history?limit=5')"
contains 'the scan history records the score over time' "$history" '"score":'

unauth="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "$API_BASE_URL/api/v1/security/score" 2>/dev/null || true)"
expect_status 'reading the security posture needs authentication' 401 "$unauth"

# --- result -----------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 15 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
