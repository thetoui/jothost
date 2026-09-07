#!/bin/sh
# The panel can put its own DNS configuration back.
#
# The overview already detected the state that needs this - a named.conf that
# does not include the panel's zones means every zone is written to disk and
# served by nobody - and reported it with nothing attached. The only way to
# reach the repair was to open Name server settings and save them unchanged,
# which is not a thing anybody would guess from the message.
#
# The break is real: the files are deleted before the repair is asked for, and
# what is checked afterwards is the file on the host rather than the panel's
# answer about it.
#
# Run with:  make docker-test-dns-repair

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

failures=0
token=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# POSIX sh has no local variables, so these are prefixed: called with a plain
# "name" this would overwrite whatever the script was working on.
control() {
  ctrl_label="$1"; ctrl_got="$2"; ctrl_want="$3"
  if [ "$ctrl_got" = "$ctrl_want" ]; then
    printf '  ctrl  %s\n' "$ctrl_label"
  else
    printf '  CTRL  %s (expected %s, got %s) - the checks below it prove nothing\n' \
      "$ctrl_label" "$ctrl_want" "$ctrl_got"
    failures=$((failures + 1))
  fi
}

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"; }

api() {
  curl -sS -X "$1" "$API_BASE_URL$2" -H "Authorization: Bearer $token"
}

status_of() {
  curl -sS -o /dev/null -w '%{http_code}' -X "$1" "$API_BASE_URL$2" \
    -H "Authorization: Bearer $token"
}

# included reports what the panel says about its own configuration.
included() {
  api GET /api/v1/dns | tr ',' '\n' |
    sed -n 's/.*"config_included":\([a-z]*\).*/\1/p'
}

MAIN=/etc/bind/named.conf
INCLUDE=/etc/bind/jothost.conf

log 'Repairing the name server configuration'
log '======================================'

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

case "$(api GET /api/v1/dns)" in
  *'"available":true'*) ;;
  *)
    log 'No name server is installed on this host, so there is nothing to repair.'
    exit 0
    ;;
esac

log ''
log '1. The configuration is healthy to begin with'
# Establishing this is what makes the break below a break rather than a
# description of the state the host was already in.
if [ "$(included)" != "true" ]; then
  control 'the panel repairs it first' "$(status_of POST /api/v1/dns/repair)" 200
  sleep 2
fi
control 'the panel is being read by the name server' "$(included)" true

log ''
log '2. The configuration is removed from underneath it'
rm -f "$MAIN" "$INCLUDE"
if [ -f "$MAIN" ]; then
  fail "$MAIN could not be removed, so nothing below is a test"
  exit 1
fi
pass 'named.conf and the include file are gone'

# The panel must notice. A repair control on a panel that reports everything
# as fine is a control nobody would ever press.
control 'the panel reports that its zones are not being read' "$(included)" false

log ''
log '3. The repair puts it back'
control 'the repair is accepted' "$(status_of POST /api/v1/dns/repair)" 200
sleep 3

# Read off the host. A panel reporting a file it did not write is the failure
# this check exists for.
if [ -f "$MAIN" ]; then
  pass "$MAIN was written again"
else
  fail "$MAIN was not written"
fi
if [ -f "$INCLUDE" ]; then
  pass "$INCLUDE was written again"
else
  fail "$INCLUDE was not written"
fi
if grep -q "$INCLUDE" "$MAIN" 2>/dev/null; then
  pass 'and named.conf includes the panel zones'
else
  fail "named.conf does not include $INCLUDE"
fi
control 'the panel agrees the warning is gone' "$(included)" true

log ''
log '4. Running it again changes nothing'
# It is the same reconcile every zone change runs, so it has to be safe to
# press twice - which is the state somebody is in when the first press looked
# like it did nothing.
before="$(md5sum "$MAIN" | cut -d' ' -f1)"
control 'the second repair is accepted' "$(status_of POST /api/v1/dns/repair)" 200
sleep 3
after="$(md5sum "$MAIN" | cut -d' ' -f1)"
if [ "$before" = "$after" ]; then
  pass 'named.conf is unchanged'
else
  fail 'a second repair rewrote the file differently'
fi

log ''
log '5. It needs the DNS permission'
control 'an unauthenticated repair is refused' \
  "$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$API_BASE_URL/api/v1/dns/repair")" 401

log ''
log '6. The trail records it'
case "$(api GET '/api/v1/audit?action=dns.repair&limit=5')" in
  *dns.repair*) pass 'the repair was audited' ;;
  *) fail 'no audit entry for the repair' ;;
esac

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
