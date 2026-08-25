#!/bin/sh
# Phase 3 Docker integration test — Dashboard.
#
# Black-box checks against the running stack. Verifies that the local server is
# registered, that the dashboard aggregates live Agent data behind
# authentication, that widgets degrade independently, and that the metric
# history is sampled and queryable.
#
# Run with:  make docker-test-dashboard

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

# get PATH [BEARER] — prints the response body.
get() {
  path="$1"; bearer="${2:-}"
  if [ -n "$bearer" ]; then
    curl -s --max-time 20 -H "Authorization: Bearer $bearer" "$API_BASE_URL$path" 2>/dev/null || true
  else
    curl -s --max-time 20 "$API_BASE_URL$path" 2>/dev/null || true
  fi
}

# status PATH [BEARER] — prints the HTTP status code.
status() {
  path="$1"; bearer="${2:-}"
  if [ -n "$bearer" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
      -H "Authorization: Bearer $bearer" "$API_BASE_URL$path" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$API_BASE_URL$path" 2>/dev/null || true
  fi
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then
    pass "$name"
  else
    fail "$name (expected HTTP $want, got $got)"
  fi
}

# contains NAME HAYSTACK NEEDLE
contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle')" ;;
  esac
}

log "Phase 3 dashboard checks"
log "API: $API_BASE_URL"
log ""

# ------------------------------------------------------------------- login

token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
  sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"

if [ -z "$token" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# ------------------------------------------------------------ authorization

log "Authorization"
# Host metrics reveal what runs and how loaded it is, so none of this is public.
expect_status "GET /dashboard without a token returns 401"        401 "$(status /api/v1/dashboard)"
expect_status "GET /servers without a token returns 401"          401 "$(status /api/v1/servers)"
expect_status "GET /dashboard with a garbage token returns 401"   401 "$(status /api/v1/dashboard not-a-real-token)"
expect_status "GET /dashboard with a valid token returns 200"     200 "$(status /api/v1/dashboard "$token")"

# ---------------------------------------------------------------- servers

log ""
log "Server registration"
servers="$(get /api/v1/servers "$token")"
contains "the local server is registered" "$servers" '"hostname"'
contains "the server carries a status"    "$servers" '"status"'

server_id="$(printf '%s' "$servers" |
  sed -n 's/.*"servers":\[{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -n "$server_id" ]; then
  pass "the server has an id"
else
  fail "could not read a server id from: $(printf '%s' "$servers" | head -c 200)"
fi

expect_status "GET /servers/:id returns the server"  200 "$(status "/api/v1/servers/$server_id" "$token")"
expect_status "a malformed id is a bad request"      400 "$(status "/api/v1/servers/not-a-uuid" "$token")"
expect_status "an unknown id is not found"           404 \
  "$(status "/api/v1/servers/00000000-0000-0000-0000-000000000000" "$token")"

# --------------------------------------------------------------- dashboard

log ""
log "Dashboard snapshot"
dashboard="$(get /api/v1/dashboard "$token")"

contains "the snapshot names the server"     "$dashboard" '"server"'
contains "the snapshot is timestamped"       "$dashboard" '"generated_at"'
contains "the snapshot carries an alert list" "$dashboard" '"alerts"'

for widget in system cpu memory disk network load services; do
  contains "the $widget widget is present" "$dashboard" "\"$widget\":"
done

# The Agent is running, so live metrics must actually be filled rather than
# every panel reporting unavailable.
if printf '%s' "$dashboard" | grep -q '"cpu":{"available":true'; then
  pass "cpu is collected from the live agent"
else
  fail "cpu was not collected: $(printf '%s' "$dashboard" | head -c 300)"
fi
if printf '%s' "$dashboard" | grep -q '"memory":{"available":true'; then
  pass "memory is collected from the live agent"
else
  fail "memory was not collected"
fi

# A real host reports a non-zero memory capacity; zero would mean the panel
# rendered without a reading behind it.
total="$(printf '%s' "$dashboard" | sed -n 's/.*"total_bytes":\([0-9]*\).*/\1/p' | head -1)"
if [ -n "$total" ] && [ "$total" -gt 0 ]; then
  pass "memory capacity is non-zero ($total bytes)"
else
  fail "memory capacity is missing or zero"
fi

log ""
log "Independent degradation"
# There is no systemd in the dev container. The services widget must say so and
# still report the dependencies the API checks itself, rather than going blank.
if printf '%s' "$dashboard" | grep -q '"services":{"available":true'; then
  pass "the services widget still reports what it has"
else
  fail "the services widget went blank without systemd"
fi
contains "postgres is reported as a dependency" "$dashboard" '"postgres"'
contains "redis is reported as a dependency"    "$dashboard" '"redis"'

# Credentials must never appear in a dashboard payload.
case "$dashboard" in
  *password*|*"$ADMIN_PASS"*|*token*) fail "the snapshot leaks credential material" ;;
  *) pass "the snapshot contains no credential material" ;;
esac

# ----------------------------------------------------------------- metrics

log ""
log "Metric history"
expect_status "a valid range is accepted"   200 "$(status "/api/v1/servers/$server_id/metrics?range=1h" "$token")"
expect_status "an absent range defaults"    200 "$(status "/api/v1/servers/$server_id/metrics" "$token")"
expect_status "an unsupported range is a bad request" 400 \
  "$(status "/api/v1/servers/$server_id/metrics?range=2h" "$token")"
expect_status "metrics for an unknown server are not found" 404 \
  "$(status "/api/v1/servers/00000000-0000-0000-0000-000000000000/metrics" "$token")"

for range in 1h 24h 7d 30d; do
  expect_status "range $range is supported" 200 \
    "$(status "/api/v1/servers/$server_id/metrics?range=$range" "$token")"
done

series="$(get "/api/v1/servers/$server_id/metrics?range=1h" "$token")"
contains "the series names its range"  "$series" '"range":"1h"'
contains "the series names its bucket" "$series" '"bucket"'
contains "the series carries points"   "$series" '"points"'

log ""
log "Sampling"
# The sampler writes on a cadence, so a freshly started stack needs a moment
# before the graph has anything in it.
sampled=0
attempt=0
while [ $attempt -lt 24 ]; do
  series="$(get "/api/v1/servers/$server_id/metrics?range=1h" "$token")"
  case "$series" in
    *'"points":[]'*) ;;
    *'"points":['*) sampled=1; break ;;
  esac
  attempt=$((attempt + 1))
  sleep 5
done

if [ $sampled -eq 1 ]; then
  pass "the sampler recorded metric history"
else
  fail "no metrics were sampled within 2 minutes"
fi

if [ $sampled -eq 1 ]; then
  contains "recorded points carry a timestamp" "$series" '"timestamp"'
  # memory_percent is recorded on every successful sample, unlike cpu_percent
  # which is absent on the very first reading.
  if printf '%s' "$series" | grep -q '"memory_percent":[0-9]'; then
    pass "recorded points carry a memory reading"
  else
    fail "no memory reading was recorded: $(printf '%s' "$series" | head -c 300)"
  fi
fi

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 3 dashboard checks passed"
