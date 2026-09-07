#!/bin/sh
# Phase 8 Docker integration test — the database manager.
#
# Black-box checks against the running stack. The acceptance is not that the API
# returns 200: it is that a database the panel says it created is one the server
# actually has, that an account the panel says it made can connect with the
# password the panel handed back, and that the privilege level it was given is
# the privilege level the server enforces. A panel that reports success without
# those three being true is worse than one that reports nothing.
#
# It runs inside the agent container, which is the managed host in development,
# so it can connect to MariaDB and PostgreSQL directly and check the panel's
# claims against the servers themselves.
#
# Run with:  make docker-test-databases

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

MYSQL_SOCKET="${AGENT_MYSQL_SOCKET:-/run/mysqld/mysqld.sock}"
PG_HOST="${AGENT_POSTGRES_HOST:-/run/postgresql}"
PG_ADMIN_PASS="${AGENT_POSTGRES_ADMIN_PASSWORD:-jothost_pg_admin_dev}"

# Distinctive names, so a leftover from an interrupted run is obvious and the
# cleanup below cannot touch anything a person created.
DB_MY="${DB_MY:-p8_my_shop}"
DB_PG="${DB_PG:-p8_pg_shop}"
DB_PMA="${DB_PMA:-p8_pma_demo}"

failures=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

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
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 200))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  # An empty needle matches every string, so the check would prove nothing.
  # That is a broken test, not a passing one, and it says so.
  if [ -z "$needle" ]; then
    fail "$name (the value searched for is empty, so this check proves nothing)"
    return
  fi
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

# json_field JSON KEY — the value of the FIRST occurrence of KEY.
#
# awk rather than sed, deliberately. A sed substitution with a leading .* is
# greedy, so it matches the LAST occurrence in the document: on a response
# carrying both a database and its user, asking for "id" silently returned the
# user's id and every request built from it answered 404. That is exactly what
# it did before this was rewritten.
json_field() {
  printf '%s' "$1" | awk -v key="\"$2\":\"" '
    {
      at = index($0, key)
      if (at == 0) { exit }
      rest = substr($0, at + length(key))
      end = index(rest, "\"")
      if (end == 0) { exit }
      print substr(rest, 1, end - 1)
    }'
}

# ---------------------------------------------------------------- SQL helpers

# my_admin runs SQL on MariaDB as root over the socket.
my_admin() {
  mariadb --socket="$MYSQL_SOCKET" --user=root --batch --skip-column-names -e "$1" 2>/dev/null || true
}

# my_as runs SQL as a panel-created account, authenticating with the password
# the panel handed back. This is the check that matters: the password only
# works if the panel and the server agree about it.
#
# The credentials go in a mode-0600 option file rather than on the command line,
# for the same reason the Agent does it — argv is world-readable. The values are
# quoted because an option file treats '#' and ';' as the start of a comment,
# and the panel's generated passwords can contain either; unquoted, the client
# authenticates with a truncated password.
my_as() {
  user="$1"; password="$2"; sql="$3"
  printf '[client]\nuser="%s"\npassword="%s"\nsocket="%s"\n' \
    "$user" "$password" "$MYSQL_SOCKET" > "$work/my.cnf"
  chmod 600 "$work/my.cnf"
  mariadb --defaults-extra-file="$work/my.cnf" --batch --skip-column-names -e "$sql" 2>/dev/null
}

pg_admin() {
  printf '*:5432:*:postgres:%s\n' "$PG_ADMIN_PASS" > "$work/pg.pass"
  chmod 600 "$work/pg.pass"
  PGPASSFILE="$work/pg.pass" psql --no-psqlrc -q -t -A -w \
    -h "$PG_HOST" -U postgres -d "${2:-postgres}" -c "$1" 2>/dev/null || true
}

# pg_as connects as a panel-created role with the password the panel returned.
#
# The password is escaped before it goes in the pgpass file. A colon is that
# file's field separator and a backslash is its escape character, and both are
# in the panel's generated alphabet — unescaped, a colon splits the line and
# libpq authenticates with a truncated secret.
pg_as() {
  user="$1"; password="$2"; database="$3"; sql="$4"
  escaped="$(printf '%s' "$password" | sed -e 's/\\/\\\\/g' -e 's/:/\\:/g')"
  printf '*:5432:*:%s:%s\n' "$user" "$escaped" > "$work/pguser.pass"
  chmod 600 "$work/pguser.pass"
  PGPASSFILE="$work/pguser.pass" psql --no-psqlrc -q -t -A -w \
    -h "$PG_HOST" -U "$user" -d "$database" -c "$sql" 2>/dev/null
}

# database_id_for LISTING NAME — the id of the database called NAME.
#
# The listing is split on the brace that opens each object, so each line begins
# with that object's own "id". Anchoring on it avoids the greedy-match problem
# json_field exists to solve.
database_id_for() {
  printf '%s' "$1" | tr '{' '\n' | grep "\"name\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# user_id_for LISTING USERNAME — the id of the account called USERNAME.
user_id_for() {
  printf '%s' "$1" | tr '{' '\n' | grep "\"username\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# purge NAME removes a database, and the account named after it, left behind by
# an interrupted run.
#
# All three parts matter. Dropping the database on the server alone leaves the
# panel's row behind and the next create is refused as a duplicate. Leaving the
# *account* behind is subtler and was worse: creating an account is idempotent,
# so the second run's account kept the first run's password, and every check
# that tried to connect failed for a reason that had nothing to do with the code
# under test.
# Every variable here is prefixed. POSIX sh has no function scope, so a plain
# "user_id" inside this function silently overwrites the caller's — which it
# did, and every check after the first purge then addressed an empty id and
# failed with a symptom unrelated to its cause.
purge() {
  purge_name="$1"
  purge_db_id="$(database_id_for "$(api GET /api/v1/databases)" "$purge_name")"
  if [ -n "$purge_db_id" ]; then
    api DELETE "/api/v1/databases/$purge_db_id" >/dev/null 2>&1 || true
  fi

  # The panel's account row goes too. Deleting a database deliberately leaves
  # its accounts behind — one account can hold grants on several — so a rerun
  # would otherwise find a row whose password no longer matches the server.
  purge_user_id="$(user_id_for "$(api GET /api/v1/database-users)" "$purge_name")"
  if [ -n "$purge_user_id" ]; then
    api DELETE "/api/v1/database-users/$purge_user_id" >/dev/null 2>&1 || true
  fi

  my_admin "DROP DATABASE IF EXISTS \`$purge_name\`;" >/dev/null
  my_admin "DROP USER IF EXISTS '$purge_name'@'localhost';" >/dev/null
  pg_admin "DROP DATABASE IF EXISTS \"$purge_name\";" >/dev/null
  pg_admin "DROP ROLE IF EXISTS \"$purge_name\";" >/dev/null
}


# ------------------------------------------------------------------ the run

log 'Phase 8 — database manager'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# --- 1. what the host runs ------------------------------------------------

log ''
log '1. Engines'

engines="$(api GET /api/v1/databases/engines)"
contains 'engines endpoint reports availability' "$engines" '"available":true'
# Both halves matter. Asserting only that the engine is named would pass while
# it was reported unavailable, and every later check would then fail with a
# different symptom than the actual cause — which is what happened once.
contains 'MariaDB is available' "$engines" '"engine":"mariadb","available":true'
contains 'PostgreSQL is available' "$engines" '"engine":"postgres","available":true'
# The panel must know that a MySQL account is a user/host pair and a PostgreSQL
# role is not, or it shows a control that means nothing on one of them.
contains 'host patterns are reported per engine' "$engines" '"supports_host_patterns":true'
contains 'the offered privilege levels are fixed' "$engines" '"privileges":["readonly","readwrite","full"]'

# --- 2. MariaDB: create, and check the server actually has it -------------

log ''
log '2. MariaDB'

purge "$DB_MY"

created="$(api POST /api/v1/databases "{\"name\":\"$DB_MY\",\"engine\":\"mariadb\"}")"
contains 'creating a database reports the name back' "$created" "\"name\":\"$DB_MY\""
contains 'the database is active, not merely queued' "$created" '"status":"active"'

my_id="$(json_field "$created" 'id')"
my_password="$(json_field "$created" 'password')"
my_user="$(json_field "$created" 'username')"

if [ -n "$my_password" ]; then
  pass 'a password is returned once, with the account'
else
  fail 'a password is returned once, with the account'
fi

# The claim under test: the panel said it created a database, so the server
# must have one.
on_server="$(my_admin "SHOW DATABASES LIKE '$DB_MY';")"
contains 'the database exists on the MariaDB server' "$on_server" "$DB_MY"

charset="$(my_admin "SELECT DEFAULT_CHARACTER_SET_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME='$DB_MY';")"
# utf8 rather than utf8mb4 silently mangles anything outside the BMP.
contains 'the database is utf8mb4' "$charset" 'utf8mb4'

# --- 3. the account can actually connect ---------------------------------

log ''
log '3. MariaDB account'

if [ -n "$my_password" ] && my_as "$my_user" "$my_password" 'SELECT 1' >/dev/null 2>&1; then
  pass 'the account connects with the password the panel returned'
else
  fail 'the account connects with the password the panel returned'
fi

# "full" must mean full inside this database.
if my_as "$my_user" "$my_password" "CREATE TABLE \`$DB_MY\`.t (id INT); INSERT INTO \`$DB_MY\`.t VALUES (1);" >/dev/null 2>&1; then
  pass 'a full-access account can create and write tables'
else
  fail 'a full-access account can create and write tables'
fi

# ...and nothing outside it. An account that can read mysql.user is an account
# that can read every password hash on the server.
if my_as "$my_user" "$my_password" 'SELECT 1 FROM mysql.user LIMIT 1' >/dev/null 2>&1; then
  fail 'the account cannot reach the server-owned schemas'
else
  pass 'the account cannot reach the server-owned schemas'
fi

# --- 4. lowering a grant actually takes privileges away ------------------

log ''
log '4. Privileges are enforced, not just recorded'

users="$(api GET "/api/v1/databases/$my_id/users")"
user_id="$(json_field "$users" 'id')"

code="$(api_status PATCH "/api/v1/databases/$my_id/users/$user_id" '{"privilege":"readonly"}')"
expect_status 'lowering an account to read-only' '200' "$code"

# The point of the whole exercise. A panel that records a restriction the
# server is not applying is telling its operator something untrue.
grants="$(my_admin "SHOW GRANTS FOR '$my_user'@'localhost';")"
not_contains 'the server no longer grants ALL PRIVILEGES' "$grants" 'ALL PRIVILEGES'

if my_as "$my_user" "$my_password" "INSERT INTO \`$DB_MY\`.t VALUES (2)" >/dev/null 2>&1; then
  fail 'a read-only account can no longer write'
else
  pass 'a read-only account can no longer write'
fi
if my_as "$my_user" "$my_password" "SELECT count(*) FROM \`$DB_MY\`.t" >/dev/null 2>&1; then
  pass 'a read-only account can still read'
else
  fail 'a read-only account can still read'
fi

# Raising it again must restore what was taken away, or a lowered grant is a
# one-way door.
code="$(api_status PATCH "/api/v1/databases/$my_id/users/$user_id" '{"privilege":"readwrite"}')"
expect_status 'raising an account back to read/write' '200' "$code"
if my_as "$my_user" "$my_password" "INSERT INTO \`$DB_MY\`.t VALUES (3)" >/dev/null 2>&1; then
  pass 'a read/write account can write again'
else
  fail 'a read/write account can write again'
fi
if my_as "$my_user" "$my_password" "CREATE TABLE \`$DB_MY\`.t2 (id INT)" >/dev/null 2>&1; then
  fail 'a read/write account still cannot change the schema'
else
  pass 'a read/write account still cannot change the schema'
fi

# --- 5. password rotation reaches the server -----------------------------

log ''
log '5. Password rotation'

rotated="$(api PATCH "/api/v1/database-users/$user_id/password" '{"password":""}')"
new_password="$(json_field "$rotated" 'password')"

if [ -n "$new_password" ] && [ "$new_password" != "$my_password" ]; then
  pass 'rotating returns a different password'
else
  fail 'rotating returns a different password'
fi
if my_as "$my_user" "$new_password" 'SELECT 1' >/dev/null 2>&1; then
  pass 'the new password works on the server'
else
  fail 'the new password works on the server'
fi
# A panel that reports a rotation the server did not apply leaves an operator
# with a credential that silently does not work.
if my_as "$my_user" "$my_password" 'SELECT 1' >/dev/null 2>&1; then
  fail 'the old password stops working'
else
  pass 'the old password stops working'
fi

# The stored copy must be the one that works, not the one that was replaced.
revealed="$(api GET "/api/v1/database-users/$user_id/password")"
contains 'the stored password is the one that now works' "$revealed" "$new_password"

# --- 6. the password never leaks into a listing --------------------------

log ''
log '6. Storage'

listing="$(api GET /api/v1/databases)"
not_contains 'a database listing never carries a password' "$listing" "$new_password"
users="$(api GET "/api/v1/databases/$my_id/users")"
not_contains 'a user listing never carries a password' "$users" "$new_password"
all_users="$(api GET /api/v1/database-users)"
not_contains 'the account listing never carries a password' "$all_users" "$new_password"

# --- 7. PostgreSQL -------------------------------------------------------

log ''
log '7. PostgreSQL'

purge "$DB_PG"

pg_created="$(api POST /api/v1/databases "{\"name\":\"$DB_PG\",\"engine\":\"postgres\"}")"
contains 'creating a PostgreSQL database reports the name back' "$pg_created" "\"name\":\"$DB_PG\""

pg_id="$(json_field "$pg_created" 'id')"
pg_password="$(json_field "$pg_created" 'password')"
pg_user="$(json_field "$pg_created" 'username')"

pg_on_server="$(pg_admin "SELECT datname FROM pg_database WHERE datname = '$DB_PG';")"
contains 'the database exists on the PostgreSQL server' "$pg_on_server" "$DB_PG"

# A PostgreSQL role is global; recording a host for one would be a lie about
# where it can connect from.
contains 'a PostgreSQL account carries no host' "$pg_created" '"host":""'

if [ -n "$pg_password" ] && pg_as "$pg_user" "$pg_password" "$DB_PG" 'SELECT 1' >/dev/null 2>&1; then
  pass 'the PostgreSQL role connects with the password the panel returned'
else
  fail 'the PostgreSQL role connects with the password the panel returned'
fi

# Full access on PostgreSQL has to include CREATE on the schema, or the
# application cannot run its own migrations — the most common way a hosting
# panel's "full access" turns out not to be.
if pg_as "$pg_user" "$pg_password" "$DB_PG" 'CREATE TABLE t (id int); INSERT INTO t VALUES (1);' >/dev/null 2>&1; then
  pass 'a full-access role can create tables in the public schema'
else
  fail 'a full-access role can create tables in the public schema'
fi

# The isolation that actually matters on a shared host: one customer's role
# must not be able to connect to another customer's database.
#
# PostgreSQL grants CONNECT to PUBLIC on every new database, and PUBLIC is every
# role on the server, so this is only true because the panel revokes it at
# creation. Without that revoke this check fails — which is how it was found.
purge "${DB_PG}_other"
other="$(api POST /api/v1/databases "{\"name\":\"${DB_PG}_other\",\"engine\":\"postgres\"}")"
other_id="$(json_field "$other" 'id')"

if pg_as "$pg_user" "$pg_password" "${DB_PG}_other" 'SELECT 1' >/dev/null 2>&1; then
  fail "a role cannot connect to another tenant's database"
else
  pass "a role cannot connect to another tenant's database"
fi

if [ -n "$other_id" ]; then
  api DELETE "/api/v1/databases/$other_id" >/dev/null 2>&1 || true
fi

# --- 8. what the panel refuses -------------------------------------------

log ''
log '8. Refusals'

# The whole injection surface. None of these may reach a statement.
for bad in 'Shop; DROP DATABASE other' 'app%60' 'app-prod' '1app' 'mysql' 'information_schema'; do
  code="$(api_status POST /api/v1/databases "{\"name\":\"$bad\"}")"
  if [ "$code" = "422" ] || [ "$code" = "400" ] || [ "$code" = "409" ]; then
    pass "a name the validator refuses is rejected: $bad"
  else
    fail "a name the validator refuses is rejected: $bad (got HTTP $code)"
  fi
done

# Nothing was created by any of them.
leaked="$(my_admin "SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME LIKE 'app%' OR SCHEMA_NAME LIKE '1app%' OR SCHEMA_NAME LIKE 'shop%';")"
if [ -z "$leaked" ]; then
  pass 'no database was created by a rejected name'
else
  fail "no database was created by a rejected name (found: $leaked)"
fi

# A privilege the panel does not offer must never be forwarded: SUPER and FILE
# are server-wide and would turn a website account into a way into everything.
for bad in 'SUPER' 'ALL PRIVILEGES' 'owner'; do
  code="$(api_status PATCH "/api/v1/databases/$my_id/users/$user_id" "{\"privilege\":\"$bad\"}")"
  if [ "$code" = "422" ]; then
    pass "a privilege the panel does not offer is refused: $bad"
  else
    fail "a privilege the panel does not offer is refused: $bad (got HTTP $code)"
  fi
done

# An engine the host does not run must be refused rather than guessed at.
code="$(api_status POST /api/v1/databases '{"name":"p8_nope","engine":"sqlite"}')"
expect_status 'an engine that is not ours is refused' '422' "$code"

# A misspelled field must be reported, not ignored: a mistyped "privilege"
# silently granting the default is exactly the failure this prevents.
code="$(api_status POST /api/v1/databases '{"name":"p8_typo","privilage":"full"}')"
expect_status 'an unknown field is refused rather than ignored' '400' "$code"

# --- 9. deletion removes it from the server too --------------------------

log ''
log '9. Deletion'

code="$(api_status DELETE "/api/v1/databases/$my_id")"
expect_status 'deleting a database' '200' "$code"

gone="$(my_admin "SHOW DATABASES LIKE '$DB_MY';")"
if [ -z "$gone" ]; then
  pass 'the database is gone from the MariaDB server'
else
  fail 'the database is gone from the MariaDB server'
fi

code="$(api_status DELETE "/api/v1/databases/$pg_id")"
expect_status 'deleting a PostgreSQL database' '200' "$code"

pg_gone="$(pg_admin "SELECT datname FROM pg_database WHERE datname = '$DB_PG';")"
if [ -z "$pg_gone" ]; then
  pass 'the database is gone from the PostgreSQL server'
else
  fail 'the database is gone from the PostgreSQL server'
fi

# The account outlives the database it was made for, because one account can
# hold grants on several. Removing it is a separate decision.
code="$(api_status DELETE "/api/v1/database-users/$user_id")"
expect_status 'deleting the account' '200' "$code"

still_there="$(my_admin "SELECT User FROM mysql.user WHERE User = '$my_user';")"
if [ -z "$still_there" ]; then
  pass 'the account is gone from the MariaDB server'
else
  fail 'the account is gone from the MariaDB server'
fi

# --- 10. phpMyAdmin ------------------------------------------------------

log ''
log '10. phpMyAdmin'

PMA_HOST="${PMA_HOST:-phpmyadmin.integration.test}"

# phpMyAdmin is served by the panel's own nginx, on loopback, not by the nginx
# that serves customer websites. That is deliberate: a website whose
# configuration nginx refuses cannot take the database console down with it.
# These checks therefore ask the panel's instance, and a check still asking
# port 80 would be asking the wrong web server - which is what they did, and
# they failed with a 404 that read like phpMyAdmin was broken.
PMA_BASE="${PMA_BASE:-http://127.0.0.1:8791}"


status="$(api GET /api/v1/databases/console)"
contains 'the console reports whether it can be installed' "$status" '"can_install"'

# Installed once and left installed: it is slow to install, and every check
# below wants it there. A rerun finds it already served and the install
# reconciles rather than failing.
job="$(api POST /api/v1/databases/console "{\"server_name\":\"$PMA_HOST\"}")"
contains 'installing phpMyAdmin is queued as a job' "$job" '"type":"phpmyadmin.install"'

# The worker runs it against the Agent; installing a package and its extensions
# takes longer than any other operation in this suite.
# Waiting for the name that was asked for, not merely for "served".
#
# On a host where phpMyAdmin was already installed under another name, served
# is true the moment this starts looking - so the wait ended before the job it
# had just queued had done anything, and the checks below then reported the
# previous install's name as a failure of the panel. The panel had done exactly
# the right thing; the wait had not waited.
waited=0
while [ "$waited" -lt 180 ]; do
  status="$(api GET /api/v1/databases/console)"
  case "$status" in
    *"\"server_name\":\"$PMA_HOST\""*) break ;;
  esac
  sleep 3
  waited=$((waited + 3))
done

contains 'phpMyAdmin is installed' "$status" '"installed":true'
contains 'phpMyAdmin is served' "$status" '"served":true'
contains 'it is served on the name that was asked for' "$status" "\"server_name\":\"$PMA_HOST\""

# The claim under test: it answers, and with its own login form rather than a
# PHP error. Installing the package without its extensions produces a page
# whose entire content is "the mysqli extension is missing", and that is a 200.
page="$(curl -s --max-time 30 -H "Host: $PMA_HOST" "$PMA_BASE/" 2>/dev/null || true)"
contains 'it serves its login form' "$page" 'input_username'
not_contains 'the page is not an error' "$page" 'phpMyAdmin - Error'

# The nginx that serves customer websites must not serve it. Two web servers
# that both answer for phpMyAdmin would be two front doors, and only one of
# them is reached through the panel.
website_served="$(curl -s -o /dev/null -w '%{http_code}' --max-time 15   -H "Host: $PMA_HOST" http://127.0.0.1:80/index.php 2>/dev/null || true)"
if [ "$website_served" = "200" ]; then
  fail 'the websites nginx also serves phpMyAdmin'
else
  pass "the websites nginx does not serve phpMyAdmin (answered $website_served)"
fi

# Nothing of the panel's may appear where the website system looks.
if grep -rl 'jothost:phpmyadmin' /etc/nginx/ >/dev/null 2>&1; then
  fail 'a phpMyAdmin vhost is in the websites nginx directory'
else
  pass 'no phpMyAdmin configuration under /etc/nginx'
fi

# The configuration must carry no credentials. One that names a user and
# password turns reaching the page into having the database.
config="$(cat /usr/share/webapps/phpmyadmin/config.inc.php 2>/dev/null || true)"
contains 'authentication is cookie mode' "$config" "auth_type'] = 'cookie'"
not_contains 'no user is stored in the configuration' "$config" "\$i]['user']"
not_contains 'no password is stored in the configuration' "$config" "\$i]['password']"
contains 'a blank password is refused' "$config" "AllowNoPassword'] = false"
contains 'root cannot sign in' "$config" "AllowRoot'] = false"

# It runs as its own account, not root and not one shared with a website.
pool_user="$(ps -o user= -C php-fpm 2>/dev/null | sort -u | grep -c jothost_pma || true)"
if [ "${pool_user:-0}" -gt 0 ]; then
  pass 'it runs under its own system account'
else
  # ps output varies by busybox version; fall back to the socket's owner.
  # In the panel's own run directory now, served by the panel's own PHP
  # master. /run/php-fpm belongs to the websites' master.
  owner="$(find /run/jothost-web -name 'jothost-pma*.sock' -exec stat -c '%U' {} + 2>/dev/null | head -1)"
  if [ "$owner" = "jothost_pma" ]; then
    pass 'it runs under its own system account'
  else
    fail "it runs under its own system account (socket owned by ${owner:-nothing})"
  fi
fi

# The end-to-end check: an account the panel created signs in and sees its own
# database. Everything above can pass while this fails.
purge "$DB_PMA"
made="$(api POST /api/v1/databases "{\"name\":\"$DB_PMA\",\"engine\":\"mariadb\"}")"
pma_id="$(json_field "$made" 'id')"
pma_pw="$(json_field "$made" 'password')"
pma_user="$(json_field "$made" 'username')"

rm -f "$work/pma.jar"
form="$(curl -s --max-time 30 -c "$work/pma.jar" -H "Host: $PMA_HOST" "$PMA_BASE/" 2>/dev/null || true)"
form_token="$(printf '%s' "$form" | sed -n 's/.*name="token" value="\([^"]*\)".*/\1/p' | head -1)"

signed_in="$(curl -s --max-time 30 -b "$work/pma.jar" -c "$work/pma.jar" -L -H "Host: $PMA_HOST" \
  --data-urlencode "pma_username=$pma_user" \
  --data-urlencode "pma_password=$pma_pw" \
  --data-urlencode "server=1" \
  --data-urlencode "token=$form_token" \
  "$PMA_BASE/index.php" 2>/dev/null || true)"

contains 'an account the panel created can sign in' "$signed_in" "$DB_PMA"
not_contains 'signing in did not bounce back to the form' "$signed_in" 'input_password'

# root must be refused even with the server's own socket authentication.
rm -f "$work/root.jar"
form="$(curl -s --max-time 30 -c "$work/root.jar" -H "Host: $PMA_HOST" "$PMA_BASE/" 2>/dev/null || true)"
form_token="$(printf '%s' "$form" | sed -n 's/.*name="token" value="\([^"]*\)".*/\1/p' | head -1)"
as_root="$(curl -s --max-time 30 -b "$work/root.jar" -c "$work/root.jar" -L -H "Host: $PMA_HOST" \
  --data-urlencode "pma_username=root" --data-urlencode "pma_password=" \
  --data-urlencode "server=1" --data-urlencode "token=$form_token" \
  "$PMA_BASE/index.php" 2>/dev/null || true)"
contains 'root is refused' "$as_root" 'input_username'

# An address that is not a host name must still be refused rather than written
# into an nginx server_name.
code="$(api_status POST /api/v1/databases/console '{"server_name":"not a host"}')"
expect_status 'an invalid address is refused' '422' "$code"

# An empty one is now the ordinary case and is accepted. phpMyAdmin is served
# by the panel's own nginx on the loopback and reached only through the panel's
# /phpmyadmin/ location, which addresses it by a fixed internal name - so there
# is no address to choose, and the panel stopped asking for one. This check
# asserted a 422 while the field was still on the form.
code="$(api_status POST /api/v1/databases/console '{"server_name":""}')"
expect_status 'no address is needed, because it is always the panel own' '202' "$code"

if [ -n "$pma_id" ]; then
  api DELETE "/api/v1/databases/$pma_id" >/dev/null 2>&1 || true
fi
purge "$DB_PMA"

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 8 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
