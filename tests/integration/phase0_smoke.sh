#!/bin/sh
# Phase 0 Docker integration test.
#
# Black-box checks against the running development stack. Verifies the
# foundation contracts: response envelope, request IDs, readiness reporting,
# the API -> Agent Unix socket path, and that the Agent is not reachable
# over TCP.
#
# Run with:  make docker-test-integration

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
PANEL_BASE_URL="${PANEL_BASE_URL:-http://nginx:80}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# curl is required: busybox wget discards the response body on a non-2xx
# status, which is exactly what the error-envelope checks need to inspect.
if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

# body URL — prints the response body regardless of status code.
body() {
  curl -s --max-time 10 "$1" 2>/dev/null || true
}

# status URL — prints the HTTP status code, or 000 if unreachable.
# curl writes 000 itself on a connection failure, so the exit code is
# swallowed rather than echoing a second value.
status() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$1" 2>/dev/null || true
}

# check_body NAME EXPECTED_SUBSTRING URL
check_body() {
  name="$1"; expected="$2"; url="$3"
  actual="$(body "$url")"
  case "$actual" in
    *"$expected"*) pass "$name" ;;
    *) fail "$name (expected '$expected' in: ${actual:-<empty>})" ;;
  esac
}

# check_status NAME EXPECTED_CODE URL
check_status() {
  name="$1"; expected="$2"; url="$3"
  actual="$(status "$url")"
  if [ "$actual" = "$expected" ]; then
    pass "$name"
  else
    fail "$name (expected HTTP $expected, got $actual)"
  fi
}

log "Phase 0 integration checks"
log "API:   $API_BASE_URL"
log "Panel: $PANEL_BASE_URL"
log ""

log "API health"
check_status "GET /healthz returns 200"            200 "$API_BASE_URL/healthz"
check_body   "liveness reports service=api"        '"service":"api"'    "$API_BASE_URL/healthz"
check_body   "liveness uses the success envelope"  '"success":true'     "$API_BASE_URL/healthz"
check_body   "every response carries a request_id" '"request_id":"req_' "$API_BASE_URL/healthz"

log ""
log "API versioned routes"
check_status "GET /api/v1/health returns 200"  200 "$API_BASE_URL/api/v1/health"
check_status "GET /api/v1/version returns 200" 200 "$API_BASE_URL/api/v1/version"
check_status "unknown route returns 404"       404 "$API_BASE_URL/api/v1/nope"
check_body   "404 uses the error envelope"     '"code":"RESOURCE_NOT_FOUND"' "$API_BASE_URL/api/v1/nope"
check_body   "error envelope sets success=false" '"success":false'           "$API_BASE_URL/api/v1/nope"

log ""
log "Security headers"
headers="$(curl -s -D - -o /dev/null --max-time 10 "$API_BASE_URL/healthz" 2>/dev/null || true)"
for header in "X-Content-Type-Options: nosniff" "X-Frame-Options: DENY" "Referrer-Policy: no-referrer"; do
  case "$headers" in
    *"$header"*) pass "$header" ;;
    *) fail "missing header: $header" ;;
  esac
done
# Go canonicalises the header to X-Request-Id; HTTP header names are
# case-insensitive, so the check is too.
if printf '%s' "$headers" | grep -qi '^x-request-id: req_'; then
  pass "X-Request-ID is echoed"
else
  fail "missing header: X-Request-ID"
fi

log ""
log "Readiness (Postgres, Redis, Agent)"
check_status "GET /readyz returns 200"          200 "$API_BASE_URL/readyz"
check_body   "postgres reachable"               '"postgres":{"status":"up"}' "$API_BASE_URL/readyz"
check_body   "redis reachable"                  '"redis":{"status":"up"}'    "$API_BASE_URL/readyz"
# The agent check succeeds only if the API reached the privileged Agent over
# the shared Unix socket, which is the whole Phase 0 trust boundary.
check_body   "agent reachable over unix socket" '"agent":{"status":"up"}'    "$API_BASE_URL/readyz"

log ""
log "Readiness must not leak credentials"
ready_body="$(body "$API_BASE_URL/readyz")"
case "$ready_body" in
  *password*|*"postgres://"*) fail "readiness response leaks connection credentials" ;;
  *) pass "readiness response contains no credentials" ;;
esac

log ""
log "Agent network exposure"
# The Agent process must be unreachable over TCP: it speaks only over its Unix
# socket (ARCHITECTURE.md section 10).
#
# Port 80 is deliberately excluded. Since Phase 4 the agent container also runs
# the nginx that serves hosted websites, which is a separate process. That port
# belongs to the sites, not to the Agent, and is checked below instead.
agent_exposed=0
for port in 8080 9000 9090 7000; do
  code="$(status "http://agent:$port/")"
  if [ "$code" != "000" ] && [ -n "$code" ]; then
    agent_exposed=1
    fail "agent answered HTTP on port $port (status $code)"
  fi
done
if [ "$agent_exposed" -eq 0 ]; then
  pass "the agent protocol is not exposed over TCP"
fi

# Port 80 answers, but as a web server for hosted sites: a hostname no site
# claims must get 404, and the port must not speak the agent protocol.
sites_root="$(status "http://agent:80/")"
if [ "$sites_root" = "404" ]; then
  pass "the site web server refuses an unclaimed hostname"
else
  fail "the site web server answered $sites_root for an unclaimed hostname (expected 404)"
fi

sites_body="$(body "http://agent:80/")"
case "$sites_body" in
  *'"operation"'*|*'"request_id"'*)
    fail "port 80 answered with the agent protocol" ;;
  *)
    pass "port 80 does not speak the agent protocol" ;;
esac

log ""
log "Panel reverse proxy"
check_status "nginx proxies /healthz"       200 "$PANEL_BASE_URL/healthz"
check_status "nginx proxies /api/v1/health" 200 "$PANEL_BASE_URL/api/v1/health"
check_status "nginx serves the frontend"    200 "$PANEL_BASE_URL/"
check_body   "frontend serves the panel shell" "JotHost Panel" "$PANEL_BASE_URL/"

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 0 integration checks passed"
