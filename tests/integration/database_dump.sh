#!/bin/sh
# Exporting and importing one database as a .sql file.
#
# The panel's Export and Import tiles were links to the Backups page. That is a
# different thing wearing the same word: a backup is an archive of the host,
# taken on a schedule and restored whole. An operator who wants to send
# somebody a dump of one database, or who has been sent one, wants a file — and
# had no way to get one out or put one in.
#
# The claim under test is that the dump comes from the database server. So a
# table is created directly in MySQL, exported through the panel, dropped, and
# imported back — and then the rows are counted. Nothing here trusts the
# panel's own account of what it did.
#
# Run with:  make docker-test-database-dump

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DB_NAME="dump$STAMP"
WORK="$(mktemp -d)"

failures=0
token=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# A control establishes that the request reached the code under test. POSIX sh
# has no locals, so the names here are prefixed: called with a plain "name" this
# would overwrite whatever the script was working on.
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
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "$API_BASE_URL$path" -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body"
  else
    curl -sS -X "$method" "$API_BASE_URL$path" -H "Authorization: Bearer $token"
  fi
}

# sql runs a statement against the host's MySQL, whatever the client is called.
sql() {
  if command -v mariadb >/dev/null 2>&1; then
    mariadb -N -B -e "$1"
  else
    mysql -N -B -e "$1"
  fi
}

log 'Exporting and importing a database'
log '=================================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

if ! sql 'SELECT 1' >/dev/null 2>&1; then
  log ''
  log 'No MySQL client on this host; the rest is skipped.'
  exit 0
fi

log ''
log '1. A database with a table the panel did not create'
created="$(api POST /api/v1/databases "{\"name\":\"$DB_NAME\",\"engine\":\"mariadb\"}")"
db_id="$(printf '%s' "$created" | sed -n 's/.*"database":{"id":"\([^"]*\)".*/\1/p')"
if [ -z "$db_id" ]; then
  fail "could not create $DB_NAME: $created"
  exit 1
fi

# Straight into the server. The export has to find what is actually there, not
# what the panel remembers putting there.
sql "USE \`$DB_NAME\`;
  CREATE TABLE widgets (id INT PRIMARY KEY, label VARCHAR(40));
  INSERT INTO widgets VALUES (1,'first'),(2,'second'),(3,'third');" >/dev/null
control 'three rows are in the server' \
  "$(sql "SELECT COUNT(*) FROM \`$DB_NAME\`.widgets;")" 3

log ''
log '2. The export is a dump, from the database'
code="$(curl -sS -o "$WORK/dump.sql" -w '%{http_code}' -D "$WORK/headers" \
  -H "Authorization: Bearer $token" "$API_BASE_URL/api/v1/databases/$db_id/export")"
control 'the export answered' "$code" 200

if grep -q 'CREATE TABLE' "$WORK/dump.sql" && grep -q 'widgets' "$WORK/dump.sql"; then
  pass 'the dump carries the table definition'
else
  fail 'the dump does not contain the table'
fi
if grep -q 'second' "$WORK/dump.sql"; then
  pass 'and the rows'
else
  fail 'the dump has no data in it'
fi

# A dump is somebody's data. A browser must save it, not try to render it.
headers="$(tr -d '\r' < "$WORK/headers")"
case "$headers" in
  *"attachment; filename=\"$DB_NAME.sql\""*)
    pass "it is sent as an attachment named $DB_NAME.sql" ;;
  *) fail "the download is not an attachment named for the database: $(printf '%s' "$headers" | grep -i disposition)" ;;
esac
case "$headers" in
  *[Aa]pplication/octet-stream*) pass 'and as octet-stream, so nothing renders it' ;;
  *) fail 'the dump is not sent as octet-stream' ;;
esac

log ''
log '3. It really is a dump and not a backup archive'
# A backup is a tar.gz of the host. If the Export tile were still pointing at
# that, this is what would come back.
if head -c 2 "$WORK/dump.sql" | od -An -tx1 | grep -q '1f 8b'; then
  fail 'the export is gzip: this is an archive, not a dump'
else
  pass 'the export is plain SQL, not a compressed archive'
fi

log ''
log '4. Dropping the table, so the import has work to do'
sql "USE \`$DB_NAME\`; DROP TABLE widgets;" >/dev/null
control 'the table is gone' \
  "$(sql "SELECT COUNT(*) FROM information_schema.tables
     WHERE table_schema='$DB_NAME' AND table_name='widgets';")" 0

log ''
log '5. The import puts it back'
loaded="$(curl -sS -X POST -H "Authorization: Bearer $token" \
  --data-binary "@$WORK/dump.sql" \
  "$API_BASE_URL/api/v1/databases/$db_id/import?filename=dump.sql")"
case "$loaded" in
  *'"loaded":true'*) pass 'the import reported success' ;;
  *) fail "the import failed: $loaded" ;;
esac

rows="$(sql "SELECT COUNT(*) FROM \`$DB_NAME\`.widgets;" 2>/dev/null || echo missing)"
if [ "$rows" = "3" ]; then
  pass 'all three rows are back in the database'
else
  fail "the table has $rows rows after the import, not 3"
fi

log ''
log '6. Nothing is left on the host afterwards'
# A dump is every row of somebody's database. Left in the spool it would sit
# there until something else happened to clear it.
left="$(ls -1 /var/lib/jothost/db-transfers 2>/dev/null | wc -l | tr -d ' ')"
if [ "$left" = "0" ]; then
  pass 'the transfer spool is empty'
else
  fail "$left file(s) left in the transfer spool"
fi

if [ -d /var/lib/jothost/db-transfers ]; then
  mode="$(stat -c '%a' /var/lib/jothost/db-transfers 2>/dev/null || echo unknown)"
  if [ "$mode" = "700" ]; then
    pass 'and it is private to root'
  else
    fail "the spool is mode $mode, not 700"
  fi
fi

log ''
log '7. The trail records both, without the data'
trail="$(api GET '/api/v1/audit?action=database.export&limit=5')"
case "$trail" in
  *database.export*) pass 'the export was audited' ;;
  *) fail 'no audit entry for the export' ;;
esac
trail="$(api GET '/api/v1/audit?action=database.import&limit=5')"
case "$trail" in
  *database.import*) pass 'the import was audited' ;;
  *) fail 'no audit entry for the import' ;;
esac

log ''
log 'Cleaning up'
api DELETE "/api/v1/databases/$db_id" >/dev/null 2>&1 || true
rm -rf "$WORK"

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
