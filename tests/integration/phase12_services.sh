#!/bin/sh
# Phase 12 Docker integration test — the service manager.
#
# Black-box checks against the running stack. The acceptance is that the panel
# tells the truth about this host: which services it actually has, which are
# actually up — read from the process table, so the answer is real whether or
# not systemd is there — and, where control is not possible, that it says so
# instead of offering buttons that cannot work.
#
# Where the host has an init system — this container runs OpenRC — the control
# half is exercised for real: nginx is stopped and started through the panel and
# the process table is read afterwards to see whether it actually happened. A
# check that only read the panel's own reply would pass just as happily against
# a panel that reported success and did nothing.
#
# What this suite does not prove is that the *systemd* backend drives systemd,
# because there is no systemd here. That half — that a service key becomes
# exactly the unit and verb the Agent chose — is proved by the Go tests in
# agent/internal/services, which run a real command runner against a recording
# stub. See docs/PHASE12.md section 5.
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
log '3. Control'

# nginx_masters — how many nginx master processes this host is running, read
# from the process table rather than from anything the panel said.
nginx_masters() {
  count=0
  for entry in /proc/[0-9]*; do
    [ -r "$entry/comm" ] || continue
    read -r name < "$entry/comm" 2>/dev/null || continue
    [ "$name" = "nginx" ] || continue
    # A worker's parent is the master; a master's parent is not nginx. Reading
    # the pid's own cmdline distinguishes them without pattern-matching ps.
    tr '\0' ' ' < "$entry/cmdline" 2>/dev/null | grep -q 'master process' && count=$((count + 1))
  done
  printf '%s' "$count"
}

# Whether the *host* can control services is the field at the end of the
# listing, not the per-service one: a service the panel starts itself reports
# "controllable":false on a host whose init system works perfectly well. The
# init system's name is the unambiguous signal, and empty means neither.
case "$listing" in
  *'"manager":"systemd"'* | *'"manager":"openrc"'*) host_manager=yes ;;
  *) host_manager=no ;;
esac

case "$host_manager" in
  no)
    # A host with neither systemd nor OpenRC. The panel says so once, as a
    # property of the host, rather than failing each action separately.
    pass 'the host reports that it cannot control services'

    code="$(api_status POST /api/v1/services/nginx/restart)"
    expect_status 'an action is refused with a conflict, not an internal error' 409 "$code"

    refusal="$(curl -s --max-time 30 -X POST "$API_BASE_URL/api/v1/services/nginx/restart"       -H "Authorization: Bearer $token" 2>/dev/null || true)"
    contains 'and the refusal says why' "$refusal" 'no service manager'

    # The states are still true and still shown: "nginx is running, and I
    # cannot restart it here" is a useful answer; a blank page is not.
    contains 'the states are reported anyway' "$nginx" '"running":true'
    ;;
  *)
    pass 'this host can control services'
    contains 'and names the init system driving it' "$listing" '"manager":"'

    before="$(entry "$(api GET /api/v1/services)" nginx)"
    contains 'nginx is running before the restart' "$before" '"running":true'

    # --- restart
    result="$(api POST /api/v1/services/nginx/restart)"
    contains 'restarting reports the state afterwards' "$result" '"running":true'
    contains 'and which unit it acted on' "$result" '"unit":"'
    sleep 1
    if [ "$(nginx_masters)" -ge 1 ]; then
      pass 'and nginx is running after it'
    else
      fail 'and nginx is running after it (no master process in /proc)'
    fi

    # --- stop. The claim under test is that the process is gone, not that the
    # panel said so.
    result="$(api POST /api/v1/services/nginx/stop)"
    contains 'stopping reports it as stopped' "$result" '"running":false'
    sleep 1
    if [ "$(nginx_masters)" -eq 0 ]; then
      pass 'and no nginx master process is left in the process table'
    else
      fail 'and no nginx master process is left in the process table'
    fi

    # The listing agrees with the host a moment later, because it reads the
    # same process table rather than remembering what the action returned.
    stopped="$(entry "$(api GET /api/v1/services)" nginx)"
    contains 'the listing reports nginx as stopped' "$stopped" '"running":false'

    # --- start
    result="$(api POST /api/v1/services/nginx/start)"
    contains 'starting reports it as running' "$result" '"running":true'
    sleep 1
    if [ "$(nginx_masters)" -ge 1 ]; then
      pass 'and nginx is serving again'
    else
      fail 'and nginx is serving again (no master process in /proc)'
    fi

    # --- boot state. Enabling has to change the host, not a field the panel
    # keeps: it is read back from a fresh listing.
    result="$(api POST /api/v1/services/nginx/enable)"
    contains 'enabling reports it as enabled' "$result" '"enabled":true'
    contains 'and a fresh listing agrees' "$(entry "$(api GET /api/v1/services)" nginx)" '"enabled":true'

    result="$(api POST /api/v1/services/nginx/disable)"
    contains 'disabling reports it as disabled' "$result" '"enabled":false'
    contains 'and a fresh listing agrees' "$(entry "$(api GET /api/v1/services)" nginx)" '"enabled":false'
    ;;
esac

# --- 3b. what the panel owns itself ---------------------------------------

log ''
log '3b. Services the panel starts itself'

# PHP-FPM and Apache are started by other parts of the panel. Offering to stop
# them from here would be two owners for one process: the init system would
# report success, the component that started it would carry on, and the panel
# would show a state that matched neither. The controls are withheld, with the
# reason given, rather than failing one click at a time.
php_key="$(printf '%s' "$listing" | tr '{' '\n' | grep -o '"key":"php-fpm[0-9.]*"' |
  head -n 1 | sed 's/"key":"//; s/"//')"
if [ -z "$php_key" ]; then
  log '  ...skipped: no PHP version is installed on this host'
else
  php_entry="$(entry "$listing" "$php_key")"
  contains 'PHP-FPM is marked as managed elsewhere' "$php_entry" '"self_managed":true'
  contains 'and says by what' "$php_entry" '"self_managed_by":"the PHP manager"'
  contains 'so the panel does not offer to control it' "$php_entry" '"controllable":false'

  code="$(api_status POST "/api/v1/services/$php_key/stop")"
  expect_status 'stopping it is refused with a conflict' 409 "$code"

  refusal="$(curl -s --max-time 30 -X POST "$API_BASE_URL/api/v1/services/$php_key/stop"     -H "Authorization: Bearer $token" 2>/dev/null || true)"
  contains 'and the refusal names the owner' "$refusal" 'the PHP manager'
fi

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
