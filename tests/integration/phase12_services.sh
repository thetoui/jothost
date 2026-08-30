#!/bin/sh
# Phase 12 Docker integration test — the service manager.
#
# Black-box checks against the running stack. The acceptance is that the panel
# tells the truth about this host: which services it actually has, which are
# actually up — read from the process table, so the answer is real whether or
# not systemd is there — and, where control is not possible, that it says so
# instead of offering buttons that cannot work.
#
# What this suite deliberately does not prove: that `systemctl start nginx`
# starts nginx. This container has no systemd, and no amount of test scaffolding
# here would make that claim true. The panel's half of it — that a service key
# becomes exactly the unit and verb the Agent chose — is proved by the Go tests
# in agent/internal/services, which run a real command runner against a
# recording stub. See docs/PHASE12.md section 5.
#
# Run with:  make docker-test-services

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

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  method="$1"; path="$2"
  curl -s --max-time 90 -X "$method" "$API_BASE_URL$path" \
    -H "Authorization: Bearer $token" 2>/dev/null || true
}

api_status() {
  method="$1"; path="$2"
  curl -s -o /dev/null -w '%{http_code}' --max-time 90 -X "$method" "$API_BASE_URL$path" \
    -H "Authorization: Bearer $token" 2>/dev/null || true
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

# entry LISTING KEY — the JSON object for one service.
entry() {
  printf '%s' "$1" | tr '{' '\n' | grep -F "\"key\":\"$2\"" | head -n 1
}

# ------------------------------------------------------------------ the run

log 'Phase 12 — service manager'
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

expect_status 'listing services without a token is refused' 401 \
  "$(anon_status GET /api/v1/services)"
expect_status 'acting on a service without a token is refused' 401 \
  "$(anon_status POST /api/v1/services/nginx/restart)"

# --- 2. detection ---------------------------------------------------------

log ''
log '2. Detection'

listing="$(api GET /api/v1/services)"
contains 'the panel lists the services on this host' "$listing" '"services":'
contains 'it says whether this host can control them' "$listing" '"controllable":'

# nginx is running in this container, started by the entrypoint. The panel has
# to find it without being told it exists.
nginx="$(entry "$listing" nginx)"
if [ -z "$nginx" ]; then
  fail 'nginx is detected'
else
  pass 'nginx is detected'
  contains 'it is reported as running' "$nginx" '"running":true'
  contains 'with the pid it is actually running under' "$nginx" '"pid":'
  contains 'and what it is for' "$nginx" '"role":"web"'
fi

# The state has to be real, not a guess: the pid the panel reports must be a
# process that exists, and be nginx.
pid="$(printf '%s' "$nginx" | sed -n 's/.*"pid":\([0-9]*\).*/\1/p')"
if [ -n "$pid" ] && [ "$pid" != "0" ] && [ -r "/proc/$pid/comm" ]; then
  comm="$(cat "/proc/$pid/comm" 2>/dev/null || true)"
  if [ "$comm" = "nginx" ]; then
    pass "the reported pid is the running nginx ($pid)"
  else
    fail "the reported pid is the running nginx (pid $pid is '$comm')"
  fi
else
  fail "the reported pid is the running nginx (got '$pid')"
fi

# The databases this host runs, found the same way.
contains 'MariaDB is detected' "$listing" '"key":"mariadb"'
contains 'PostgreSQL is detected' "$listing" '"key":"postgresql"'

# PHP-FPM is one service per installed version, and which versions exist is
# only knowable at runtime — a static list would be wrong on every host.
php_versions="$(api GET /api/v1/php/versions)"
case "$php_versions" in
  *'"installed":true'*)
    contains 'PHP-FPM is listed per installed version' "$listing" '"key":"php-fpm8.'
    contains 'and named so an operator knows which' "$listing" '"label":"PHP-FPM 8.'
    ;;
  *) log '  ...skipped: no PHP version is installed on this host' ;;
esac

# A panel listing services the host does not have is offering to manage
# something that is not there.
not_contains 'nothing uninstalled is listed' "$listing" '"installed":false'

# --- 3. honest degradation ------------------------------------------------

log ''
log '3. What this host cannot do'

# This container has no systemd. The panel says so — once, as a property of the
# host — rather than failing each action separately with an obscure message.
case "$listing" in
  *'"controllable":false'*)
    pass 'the host reports that it cannot control services'

    code="$(api_status POST /api/v1/services/nginx/restart)"
    expect_status 'an action is refused with a conflict, not an internal error' 409 "$code"

    refusal="$(curl -s --max-time 30 -X POST "$API_BASE_URL/api/v1/services/nginx/restart" \
      -H "Authorization: Bearer $token" 2>/dev/null || true)"
    contains 'and the refusal says why' "$refusal" 'no service manager'

    # The states are still true and still shown: "nginx is running, and I
    # cannot restart it here" is a useful answer; a blank page is not.
    contains 'the states are reported anyway' "$nginx" '"running":true'
    ;;
  *)
    pass 'this host can control services'
    log '  ...running the control checks against a host that has a service manager'

    before="$(entry "$(api GET /api/v1/services)" nginx)"
    contains 'nginx is running before the restart' "$before" '"running":true'

    result="$(curl -s --max-time 90 -X POST "$API_BASE_URL/api/v1/services/nginx/restart" \
      -H "Authorization: Bearer $token" 2>/dev/null || true)"
    contains 'restarting reports the state afterwards' "$result" '"running":true'
    contains 'and which unit it acted on' "$result" '"unit":"'
    ;;
esac

# --- 4. what is refused ---------------------------------------------------

log ''
log '4. Refusals'

# A request names a key from the Agent's catalogue. A unit name is not a key,
# and neither is anything else a caller invents.
for bad in systemd-logind.service jothost-agent ../../etc/passwd nginx.service NGINX; do
  code="$(api_status POST "/api/v1/services/$bad/restart")"
  if [ "$code" = "404" ] || [ "$code" = "409" ]; then
    pass "a service outside the catalogue is refused: $bad"
  else
    fail "a service outside the catalogue is refused: $bad (got HTTP $code)"
  fi
done

# The verb is part of the route, so an invented one is simply not a route.
code="$(api_status POST /api/v1/services/nginx/mask)"
expect_status 'a verb the panel does not have is not a route' 404 "$code"

# --- 5. the dashboard agrees ----------------------------------------------

log ''
log '5. The dashboard'

# The dashboard reads the same detection rather than a configured list of unit
# names: two sources for "which services exist" is two answers.
dashboard="$(api GET /api/v1/dashboard)"
contains 'the dashboard reports services' "$dashboard" '"services":'
contains 'and names nginx among them' "$dashboard" 'nginx'

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 12 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
