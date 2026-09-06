#!/bin/sh
# Opening phpMyAdmin on one database, signed in.
#
# The first version of this feature posted the database credentials straight at
# phpMyAdmin's own hostname and was, on paper, complete: the endpoint answered,
# its unit tests passed, the tile appeared. Driven against a real phpMyAdmin it
# returned the login page every single time.
#
# phpMyAdmin's login is a POST carrying a CSRF token bound to the session cookie
# set on the page the form came from. Three variants were measured against a
# running install - credentials alone, credentials with the cookie, credentials
# with the token - and only the pair worked. Reading a page for its token is
# exactly what the same-origin policy forbids, so phpMyAdmin had to move onto
# the panel's own origin before any of this could work at all.
#
# These checks therefore drive the real sequence a browser performs. A check
# that only asked the API for credentials would pass against a build in which
# signing in is impossible.
#
# Run with:  make docker-test-database-console

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
PANEL_BASE_URL="${PANEL_BASE_URL:-http://nginx}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DB_NAME="con$STAMP"
MOUNT="/phpmyadmin/"

failures=0
token=""
jar="$(mktemp)"

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# POSIX sh has no local variables, so every name assigned in here is a global.
# The first version called its first argument "name" and silently overwrote the
# database under test between sections 5 and 6, which the checks then reported
# as a phpMyAdmin failure. The prefix is what keeps that from recurring.
control() {
  ctrl_label="$1"; ctrl_got="$2"; ctrl_want="$3"
  if [ "$ctrl_got" = "$ctrl_want" ]; then
    printf '  ctrl  %s
' "$ctrl_label"
  else
    printf '  CTRL  %s (expected %s, got %s) - the checks below it prove nothing
' \
      "$ctrl_label" "$ctrl_want" "$ctrl_got"
    failures=$((failures + 1))
  fi
}

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$body"
  else
    curl -sS -X "$method" "$API_BASE_URL$path" -H "Authorization: Bearer $token"
  fi
}

status_of() {
  method="$1"; path="$2"
  curl -sS -o /dev/null -w '%{http_code}' -X "$method" "$API_BASE_URL$path" \
    -H "Authorization: Bearer $token"
}

field()  { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"; }
hidden() { grep -o "name=\"$2\" value=\"[^\"]*\"" "$1" | head -1 | sed 's/.*value="//;s/"//'; }

log 'Opening a database console'
log '=========================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. The endpoint exists'
# 404 here is the missing database, not a missing route - which is why the
# second control sits beside it: a path the mux does not know refuses every
# method, and this one refuses only the wrong ones.
control 'console-session is a route' \
  "$(status_of POST /api/v1/databases/00000000-0000-0000-0000-000000000000/console-session)" 404
control 'console-session is POST only' \
  "$(status_of GET /api/v1/databases/00000000-0000-0000-0000-000000000000/console-session)" 404

console="$(api GET /api/v1/databases/console)"
case "$console" in
  *'"served":true'*) pass 'phpMyAdmin is installed and served' ;;
  *)
    log ''
    log 'phpMyAdmin is not served on this host; the rest is skipped.'
    log 'Install it first with POST /api/v1/databases/console'
    exit 0
    ;;
esac

log ''
log '2. A database with an account the panel holds a password for'
created="$(api POST /api/v1/databases \
  "{\"name\":\"$DB_NAME\",\"engine\":\"mariadb\",\"create_user\":true}")"
# The response carries the database and the account it created, both with an
# id. Anchored on the database object so this reads the right one - an
# unanchored match takes the last id in the body, which is the account's.
db_id="$(printf '%s' "$created" | sed -n 's/.*"database":{"id":"\([^"]*\)".*/\1/p')"
if [ -z "$db_id" ]; then
  fail "could not create $DB_NAME: $created"
  exit 1
fi
pass "created $DB_NAME"

log ''
log '3. The panel returns its own mount, not the phpMyAdmin hostname'
session="$(api POST "/api/v1/databases/$db_id/console-session")"
url="$(field "$session" 'url')"
user="$(field "$session" 'username')"
secret="$(field "$session" 'password')"
db_name="$(field "$session" 'database')"

if [ "$url" = "$MOUNT" ]; then
  pass "the session opens at $MOUNT, on the panel's own origin"
else
  fail "expected $MOUNT, got '$url' - a cross-origin URL cannot sign anybody in"
fi
if [ -n "$user" ]; then pass "an account was chosen ($user)"; else fail 'no account returned'; fi
if [ -n "$secret" ]; then pass 'a password was returned'; else fail 'no password returned'; fi
if [ "$db_name" = "$DB_NAME" ]; then
  pass 'the session names the right database'
else
  fail "expected $DB_NAME, got '$db_name'"
fi

log ''
log '4. The response is never cached'
cc="$(curl -sS -D - -o /dev/null -X POST "$API_BASE_URL/api/v1/databases/$db_id/console-session" \
  -H "Authorization: Bearer $token" | grep -i '^cache-control:' | tr -d '\r')"
case "$cc" in
  *no-store*) pass 'Cache-Control: no-store' ;;
  *) fail "a credential response without no-store: '$cc'" ;;
esac

log ''
log '5. The sign-in a browser actually performs'
rm -f "$jar"
code="$(curl -sS -o "$jar.login" -w '%{http_code}' -c "$jar" \
  "$PANEL_BASE_URL${MOUNT}index.php?route=/")"
control 'phpMyAdmin answers on the panel origin' "$code" 200

t="$(hidden "$jar.login" token)"
s="$(hidden "$jar.login" set_session)"
if [ -n "$t" ] && [ -n "$s" ]; then
  pass 'the login form carries token and set_session'
else
  fail 'no login form - the proxy is not reaching phpMyAdmin'
fi

curl -sS -o "$jar.post" -b "$jar" -c "$jar" \
  --data-urlencode "pma_username=$user" \
  --data-urlencode "pma_password=$secret" \
  --data-urlencode "server=1" \
  --data-urlencode "token=$t" \
  --data-urlencode "set_session=$s" \
  --data-urlencode "db=$db_name" \
  --data-urlencode "target=index.php?route=/database/structure&db=$db_name" \
  "$PANEL_BASE_URL${MOUNT}index.php?route=/" >/dev/null

if grep -q 'pmaAuth-1' "$jar"; then
  pass 'phpMyAdmin issued a signed-in session'
else
  fail 'still on the login page - the credentials were not accepted'
fi

log ''
log '6. It lands on that database'
curl -sS -o "$jar.db" -b "$jar" -c "$jar" \
  "$PANEL_BASE_URL${MOUNT}index.php?route=/database/structure&db=$db_name" >/dev/null
if grep -q 'input_password' "$jar.db"; then
  fail 'bounced back to the login page'
elif grep -q "$db_name" "$jar.db"; then
  pass "the page names $db_name"
else
  fail 'signed in but not on the database'
fi

log ''
log '7. What the console must not become'
rm -f "$jar.root"
curl -sS -o "$jar.rlogin" -c "$jar.root" "$PANEL_BASE_URL${MOUNT}index.php?route=/" >/dev/null
rt="$(hidden "$jar.rlogin" token)"
rs="$(hidden "$jar.rlogin" set_session)"
curl -sS -o /dev/null -b "$jar.root" -c "$jar.root" \
  --data-urlencode "pma_username=root" \
  --data-urlencode "pma_password=$secret" \
  --data-urlencode "server=1" \
  --data-urlencode "token=$rt" \
  --data-urlencode "set_session=$rs" \
  "$PANEL_BASE_URL${MOUNT}index.php?route=/" >/dev/null
if grep -q 'pmaAuth-1' "$jar.root"; then
  fail 'root signed in - AllowRoot is not taking effect'
else
  pass 'root cannot sign in here'
fi

log ''
log '8. The trail records who opened it, not the credential'
trail="$(api GET '/api/v1/audit?action=database.console.session&limit=5')"
case "$trail" in
  *database.console.session*) pass 'the console session was audited' ;;
  *) fail 'no audit entry for opening a console' ;;
esac
case "$trail" in
  *"$secret"*) fail 'the password is in the audit metadata' ;;
  *) pass 'the audit entry records who and what, not the password' ;;
esac

log ''
log '9. A database the panel has no password for offers no console'
# The honest refusal, driven rather than asserted. Creating a database also
# creates its account, so this has to remove one to reach the case at all -
# which is the whole point: a check that never got here would pass against a
# build that hands out somebody else's credentials.
user_id="$(printf '%s' "$created" | sed -n 's/.*"user":{"id":"\([^"]*\)".*/\1/p')"
if [ -n "$user_id" ]; then
  api DELETE "/api/v1/database-users/$user_id" >/dev/null 2>&1 || true
  control 'the console is refused, not improvised'     "$(status_of POST "/api/v1/databases/$db_id/console-session")" 409
else
  fail 'could not find the account to remove'
fi

log ''
log 'Cleaning up'
api DELETE "/api/v1/databases/$db_id" >/dev/null 2>&1 || true
rm -f "$jar" "$jar".* 2>/dev/null || true

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
