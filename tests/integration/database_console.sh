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
log '7. A second database, in the same browser'
# The reported failure: the console opened the first database and no other.
# phpMyAdmin keeps one signed-in account per browser, in a cookie, so the entry
# page answered the second click with the signed-in interface rather than a
# login form. The launcher looked for its CSRF token only inside #login_form,
# found none, and refused - saying phpMyAdmin had returned no login form, which
# was true and explained nothing.
#
# The token is on that page too, and posting credentials with it switches the
# account outright. No sign-out is involved; the first fix written for this
# used one and it was not needed.
#
# The jar is deliberately the one section 5 signed in with. A fresh jar would
# make this pass against the broken build.
second_name="${DB_NAME}b"
# A username unlike the database name, so nothing below can pass on the
# database name alone.
second_account="other${STAMP}"
second_created="$(api POST /api/v1/databases   "{\"name\":\"$second_name\",\"engine\":\"mariadb\",\"create_user\":true,\"username\":\"$second_account\"}")"
second_id="$(printf '%s' "$second_created" | sed -n 's/.*"database":{"id":"\([^"]*\)".*/\1/p')"

# whoami asks the database server who the phpMyAdmin session is connected as.
#
# Not phpMyAdmin's rendered page: the second account's name turns up in the
# markup of a page served to the first account, so a grep for it passes against
# a build that never switched. This is the server's own answer.
whoami() {
  curl -sS -o "$jar.sql" -b "$jar" -c "$jar"     "$PANEL_BASE_URL${MOUNT}index.php?route=/sql&db=$1"
  sql_token="$(hidden "$jar.sql" token)"
  [ -n "$sql_token" ] || return 0
  curl -sS -o "$jar.who" -b "$jar" -c "$jar"     --data-urlencode 'sql_query=SELECT CURRENT_USER()'     --data-urlencode "db=$1"     --data-urlencode "token=$sql_token"     "$PANEL_BASE_URL${MOUNT}index.php?route=/sql"
  grep -o '[A-Za-z0-9_]*@localhost' "$jar.who" | head -1
}

if [ -z "$second_id" ]; then
  fail "could not create $second_name"
else
  second_session="$(api POST "/api/v1/databases/$second_id/console-session")"
  second_user="$(field "$second_session" 'username')"
  second_secret="$(field "$second_session" 'password')"

  control 'the browser is signed in as the first database account'     "$(whoami "$DB_NAME")" "$user@localhost"

  # Step one, as the launcher does it.
  curl -sS -o "$jar.second" -b "$jar" -c "$jar" "$PANEL_BASE_URL${MOUNT}index.php?route=/"
  if [ -z "$(hidden "$jar.second" set_session)" ]; then
    pass 'phpMyAdmin answers a signed-in browser with its interface, not a login form'
  else
    fail 'the browser was not signed in, so this proves nothing about switching'
  fi

  t2="$(hidden "$jar.second" token)"
  if [ -n "$t2" ]; then
    pass 'and that page still carries the token a sign-in needs'
  else
    fail 'no token on the signed-in page - the second database cannot be opened'
  fi

  curl -sS -o /dev/null -b "$jar" -c "$jar"     --data-urlencode "pma_username=$second_user"     --data-urlencode "pma_password=$second_secret"     --data-urlencode "server=1"     --data-urlencode "token=$t2"     --data-urlencode "db=$second_name"     --data-urlencode "target=index.php?route=/database/structure&db=$second_name"     "$PANEL_BASE_URL${MOUNT}index.php?route=/"

  curl -sS -o "$jar.seconddb" -b "$jar" -c "$jar"     "$PANEL_BASE_URL${MOUNT}index.php?route=/database/structure&db=$second_name"

  if grep -q 'input_password' "$jar.seconddb"; then
    fail 'the second database bounced back to the login page'
  elif grep -q "$second_name" "$jar.seconddb"; then
    pass "the second database opened ($second_name)"
  else
    fail 'signed in but not on the second database'
  fi

  # The claim that matters, from the server rather than the page.
  connected="$(whoami "$second_name")"
  if [ "$connected" = "$second_account@localhost" ]; then
    pass "and the server says the session is $connected"
  else
    fail "the session is $connected, not $second_account@localhost"
  fi

  # And back again, so this is not a one-way switch.
  curl -sS -o "$jar.third" -b "$jar" -c "$jar" "$PANEL_BASE_URL${MOUNT}index.php?route=/"
  t3="$(hidden "$jar.third" token)"
  curl -sS -o /dev/null -b "$jar" -c "$jar"     --data-urlencode "pma_username=$user"     --data-urlencode "pma_password=$secret"     --data-urlencode "server=1"     --data-urlencode "token=$t3"     --data-urlencode "db=$DB_NAME"     "$PANEL_BASE_URL${MOUNT}index.php?route=/"
  back="$(whoami "$DB_NAME")"
  if [ "$back" = "$user@localhost" ]; then
    pass "and back to the first database as $back"
  else
    fail "returning to the first database left the session as $back"
  fi

  api DELETE "/api/v1/databases/$second_id" >/dev/null 2>&1 || true
fi

log ''
log '8. Where phpMyAdmin sends the browser next'
# The sign-in answers 302, and until a real browser drove this nothing checked
# where to. phpMyAdmin sees the request with the /phpmyadmin/ prefix stripped
# by the proxy, so its redirect was root-relative and carried no prefix:
#   Location: /index.php?route=/&db=...
# A browser follows that to the panel's own single-page application, and the
# operator ends up back in the panel having done nothing wrong. Every check
# written before this one missed it, because they asked for the database page
# by its full URL instead of following where phpMyAdmin pointed.
rm -f "$jar.redir"
curl -sS -D "$jar.redir" -o /dev/null -c "$jar.redir.jar"   "$PANEL_BASE_URL${MOUNT}index.php?route=/" >/dev/null
rt="$(hidden /dev/null token 2>/dev/null || true)"
curl -sS -o "$jar.rl" -c "$jar.rj" "$PANEL_BASE_URL${MOUNT}index.php?route=/"
rtok="$(hidden "$jar.rl" token)"
rses="$(hidden "$jar.rl" set_session)"
curl -sS -D "$jar.redir" -o /dev/null -b "$jar.rj" -c "$jar.rj"   --data-urlencode "pma_username=$user"   --data-urlencode "pma_password=$secret"   --data-urlencode "server=1"   --data-urlencode "token=$rtok"   --data-urlencode "set_session=$rses"   --data-urlencode "db=$DB_NAME"   "$PANEL_BASE_URL${MOUNT}index.php?route=/"

location="$(grep -i '^location:' "$jar.redir" | tr -d '' | sed 's/^[Ll]ocation: *//')"
control 'the sign-in redirects somewhere'   "$(grep -ci '^location:' "$jar.redir" 2>/dev/null || echo 0)" 1

case "$location" in
  *"$MOUNT"*)
    pass "the redirect keeps the panel's mount ($location)" ;;
  '')
    fail 'the sign-in sent no Location at all' ;;
  *)
    fail "the redirect drops the mount and lands in the panel: $location" ;;
esac

# And it must name the host the request arrived on. A path-only replacement
# makes nginx rebuild the URL from its own listening port, which is not the
# port the browser asked on wherever the two differ - and the operator is sent
# to a port nothing answers.
expected_host="$(printf '%s' "$PANEL_BASE_URL" | sed 's|^[a-z]*://||')"
case "$location" in
  http://*|https://*)
    case "$location" in
      *"$expected_host"*) pass "and the host the request arrived on ($expected_host)" ;;
      *) fail "the redirect names a different host than $expected_host: $location" ;;
    esac ;;
  *) pass 'and is relative, so the host cannot be wrong' ;;
esac

log ''
log '9. What the console must not become'
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
log '10. The trail records who opened it, not the credential'
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
log '11. A database the panel has no password for offers no console'
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
