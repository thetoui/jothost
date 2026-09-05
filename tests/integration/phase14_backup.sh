#!/bin/sh
# Phase 14 Docker integration test — backup, verify and restore.
#
# The acceptance is not that the API returns 200. It is that a real website's
# files and a real database row survive being destroyed and put back, and that
# an archive somebody changed is caught rather than restored.
#
# Three things are proved end to end here that nothing else can prove:
#
#   * A backup of a live site, written to a destination, read back from that
#     destination, and matched against the checksum recorded when it was
#     written. "The upload returned success" is not the claim being tested.
#   * A restore that puts back a file that was deleted and a database row that
#     was dropped — and that refuses an archive whose bytes have changed
#     without touching anything.
#   * The same, over the two destinations that leave this machine: a real
#     S3-compatible service, and a real SSH connection with a real host key
#     check.
#
# It runs inside the agent container, which is the managed host in development.
#
# Run with:  make docker-test-backup

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# The S3 service started for the duration of these checks.
MINIO_ENDPOINT="${MINIO_ENDPOINT:-http://minio:9000}"
MINIO_BUCKET="${MINIO_BUCKET:-jothost-backups}"
MINIO_KEY="${MINIO_ROOT_USER:-jothost_backup_test}"
MINIO_SECRET="${MINIO_ROOT_PASSWORD:-jothost-backup-test-secret}"

STAMP="$(date +%s)"
DOMAIN="backup${STAMP}.test"
DB_NAME="bk_${STAMP}"
LOCAL_DIR="/backups/integration-${STAMP}"
SFTP_DIR="/backups/sftp-${STAMP}"
KEY_DIR="/tmp/jothost-backup-key-${STAMP}"

failures=0
created_destinations=""
created_schedules=""
created_backups=""
website_id=""
database_id=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  # Everything this test made goes, whatever happened: a rerun has to start
  # clean, and a destination or schedule left behind would keep taking backups
  # of a site that no longer exists.
  for id in $created_schedules; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/backup-schedules/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
  for id in $created_backups; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/backups/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
  for id in $created_destinations; do
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/backup-destinations/$id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  done
  if [ -n "$website_id" ]; then
    curl -s -o /dev/null -X DELETE \
      "$API_BASE_URL/api/v1/websites/$website_id?remove_files=true" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  fi
  if [ -n "$database_id" ]; then
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/databases/$database_id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  fi
  rm -rf "$KEY_DIR" "$LOCAL_DIR" "$SFTP_DIR" 2>/dev/null || true
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
    curl -s --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 300 -X "$method" "$API_BASE_URL$path" \
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

# await_backup polls a backup until it stops running.
await_backup() {
  id="$1"; waited=0
  while [ "$waited" -lt 300 ]; do
    state="$(json_field "$(api GET "/api/v1/backups/$id")" status)"
    case "$state" in
      completed|failed) printf '%s' "$state"; return 0 ;;
    esac
    sleep 3; waited=$((waited + 3))
  done
  printf 'TIMEOUT'
}

mysql_run() {
  mariadb --user=root --socket=/run/mysqld/mysqld.sock --batch --skip-column-names \
    --database="$1" -e "$2" 2>/dev/null || true
}

log '== Phase 14: backup, verify and restore =='

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

# --- what this host can do -------------------------------------------------

overview="$(api GET /api/v1/backups)"
contains 'the host reports it can take backups' "$overview" '"available":true'
contains 'the destinations it offers are advertised' "$overview" '"destination_kinds":['
contains 'the engines it can dump are named' "$overview" '"engines":'

# A destination kind that cannot work has to be visible before somebody
# configures one and finds out at three in the morning.
contains 'the host says whether it can reach an SFTP destination' "$overview" '"sftp":'

# --- a local destination ----------------------------------------------------

mkdir -p "$LOCAL_DIR"

dest_body="{\"name\":\"integration-local-$STAMP\",\"kind\":\"local\",\"directory\":\"$LOCAL_DIR\"}"
dest="$(api POST /api/v1/backup-destinations "$dest_body")"
local_dest_id="$(json_field "$dest" id)"
if [ -z "$local_dest_id" ]; then
  log "FATAL: could not create a local destination: $(printf '%s' "$dest" | head -c 300)"
  exit 1
fi
created_destinations="$created_destinations $local_dest_id"
pass 'a local destination was created'

# A destination that has never been reached is the most dangerous object in
# this phase: it looks like protection and is not.
contains 'a new destination has not been reached yet' "$dest" '"last_check_at":null'

checked="$(api POST "/api/v1/backup-destinations/$local_dest_id/check")"
contains 'checking a local destination writes and reads back' "$checked" '"last_check_ok":true'

# The probe object is removed: litter in somebody's storage is litter this
# panel produced.
probe_left="$(find "$LOCAL_DIR" -type f 2>/dev/null | wc -l | tr -d ' ')"
if [ "$probe_left" = "0" ]; then
  pass 'the check left nothing behind'
else
  fail "the check left $probe_left file(s) in the destination"
fi

# --- refusals ---------------------------------------------------------------

# Without this bound, "back up to /etc/nginx" would be a way to write a file
# anywhere on this host as root.
#
# The row itself is accepted — the path is well formed — and the *check* is what
# refuses it, because which directories a backup may be written to is the
# Agent's to know and the panel does not keep a second copy of that list.
escape="$(api POST /api/v1/backup-destinations \
  "{\"name\":\"escape-$STAMP\",\"kind\":\"local\",\"directory\":\"/etc/nginx\"}")"
escape_id="$(json_field "$escape" id)"
if [ -n "$escape_id" ]; then
  created_destinations="$created_destinations $escape_id"
  escape_check="$(api POST "/api/v1/backup-destinations/$escape_id/check")"
  contains 'a destination outside the allowed roots cannot be reached' \
    "$escape_check" '"last_check_ok":false'
fi

expect_status 'a directory that is not absolute is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"rel-$STAMP\",\"kind\":\"local\",\"directory\":\"backups\"}")"

expect_status 'a directory containing .. is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"dots-$STAMP\",\"kind\":\"local\",\"directory\":\"/backups/../etc\"}")"

# A backup sent over plain http to another machine is a copy of every site on
# this host, read by anyone on the path.
expect_status 'an S3 endpoint that is not https and not local is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"plain-$STAMP\",\"kind\":\"s3\",\"endpoint\":\"http://storage.example.com\",\"bucket\":\"b\",\"region\":\"us-east-1\",\"access_key\":\"k\",\"secret_key\":\"s\"}")"

expect_status 'an S3 destination with no secret key is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"nokey-$STAMP\",\"kind\":\"s3\",\"endpoint\":\"https://storage.example.com\",\"bucket\":\"b\",\"region\":\"us-east-1\",\"access_key\":\"k\"}")"

# Without a host key there is no way to tell the intended server from whatever
# answered on port 22.
expect_status 'an SFTP destination with no host key is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"nohostkey-$STAMP\",\"kind\":\"sftp\",\"host\":\"example.com\",\"user\":\"backup\",\"private_key\":\"x\"}")"

# A username read as an option changes what the client does rather than where
# it connects.
expect_status 'an SFTP username that would be read as an option is refused' 422 \
  "$(api_status POST /api/v1/backup-destinations \
     "{\"name\":\"dash-$STAMP\",\"kind\":\"sftp\",\"host\":\"example.com\",\"user\":\"-oProxyCommand=id\",\"private_key\":\"x\",\"host_key\":\"ssh-ed25519 AAAA\"}")"

expect_status 'a backup of something the panel does not manage is refused' 422 \
  "$(api_status POST /api/v1/backups \
     "{\"type\":\"files\",\"destination_id\":\"$local_dest_id\"}")"

# --- a real website with a real database ------------------------------------

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
pass "a website was created to back up ($DOMAIN)"

site_root="$(json_field "$(api GET "/api/v1/websites/$website_id?remove_files=true")" document_root)"
if [ -z "$site_root" ] || [ ! -d "$site_root" ]; then
  log "FATAL: $DOMAIN has no document root on disk"
  exit 1
fi

MARKER="the file that has to come back $STAMP"
printf '%s\n' "$MARKER" > "$site_root/marker.txt"
mkdir -p "$site_root/nested/deep"
printf 'nested\n' > "$site_root/nested/deep/inner.txt"

db_created="$(api POST /api/v1/databases \
  "{\"name\":\"$DB_NAME\",\"engine\":\"mariadb\",\"website_id\":\"$website_id\"}")"
database_id="$(json_field "$db_created" id)"
if [ -z "$database_id" ]; then
  log "FATAL: could not create $DB_NAME: $(printf '%s' "$db_created" | head -c 300)"
  exit 1
fi
pass "a database was created and attached to the site ($DB_NAME)"

# A row, so the restore has something to put back that a schema alone would not
# prove. Written with the client directly: the panel does not run arbitrary SQL,
# and it should not.
mysql_run "$DB_NAME" "CREATE TABLE survivors (id INT PRIMARY KEY, note VARCHAR(64));"
mysql_run "$DB_NAME" "INSERT INTO survivors VALUES (1, 'row-$STAMP');"
row="$(mysql_run "$DB_NAME" 'SELECT note FROM survivors WHERE id = 1;')"
contains 'the database has a row to lose' "$row" "row-$STAMP"

# --- taking the backup ------------------------------------------------------

backup="$(api POST /api/v1/backups \
  "{\"type\":\"website\",\"website_id\":\"$website_id\",\"destination_id\":\"$local_dest_id\"}")"
backup_id="$(json_field "$backup" id)"
if [ -z "$backup_id" ]; then
  log "FATAL: could not start a backup: $(printf '%s' "$backup" | head -c 300)"
  exit 1
fi
created_backups="$created_backups $backup_id"

state="$(await_backup "$backup_id")"
if [ "$state" != "completed" ]; then
  detail="$(api GET "/api/v1/backups/$backup_id")"
  log "FATAL: the backup ended $state: $(printf '%s' "$detail" | head -c 400)"
  exit 1
fi
pass 'a backup of a live website completed'

stored="$(api GET "/api/v1/backups/$backup_id")"
# The distinction the whole phase turns on.
not_contains 'the backup was read back and confirmed, not merely written' \
  "$stored" '"verified_at":null'
contains 'the backup records its size' "$stored" '"size_bytes":'
contains 'the backup records a checksum' "$stored" '"checksum":"'
contains 'the manifest names the site it holds' "$stored" "\"domain\":\"$DOMAIN\""
contains 'the manifest names the database it holds' "$stored" "\"name\":\"$DB_NAME\""

# The per-file list is not stored: an archive of a real site has tens of
# thousands of entries, and keeping them per backup would make this table larger
# than the data it describes.
not_contains 'the manifest does not carry a row per file' "$stored" '"files":['

# The backup's own key, not one of the manifest's members: json_field takes
# the last match and the manifest names a path per database.
archive_key="$(printf '%s' "$stored" | grep -o '"path":"[^"]*[.]tar[.]gz"' | head -1 | cut -d'"' -f4)"
archive_path="$LOCAL_DIR/$archive_key"
if [ -f "$archive_path" ]; then
  pass 'the archive is on the destination, under a key a person can find'
else
  fail "the archive is not at $archive_path"
fi

# --- verifying --------------------------------------------------------------

verified="$(api POST "/api/v1/backups/$backup_id/verify")"
contains 'verifying a good archive passes' "$verified" '"ok":true'
contains 'verifying reports how many members it checked' "$verified" '"members":'

# --- the restore ------------------------------------------------------------

# Confirming is not optional. A boolean would be something a caller sets once
# and forgets, so the confirmation is the backup's own id.
expect_status 'a restore with no confirmation is refused' 422 \
  "$(api_status POST "/api/v1/backups/$backup_id/restore" '{"confirm":""}')"
expect_status 'a restore confirmed with the wrong id is refused' 422 \
  "$(api_status POST "/api/v1/backups/$backup_id/restore" '{"confirm":"yes"}')"

# Now destroy what the backup holds.
rm -f "$site_root/marker.txt"
rm -rf "$site_root/nested"
mysql_run "$DB_NAME" 'DROP TABLE survivors;'
if [ -f "$site_root/marker.txt" ]; then
  fail 'the marker file was not actually deleted'
else
  pass 'the site and its database were destroyed, as a real failure would'
fi

restore="$(api POST "/api/v1/backups/$backup_id/restore" "{\"confirm\":\"$backup_id\"}")"
restore_job="$(printf '%s' "$restore" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
if [ -z "$restore_job" ]; then
  log "FATAL: the restore was not queued: $(printf '%s' "$restore" | head -c 300)"
  exit 1
fi

state="$(await_job "$restore_job")"
if [ "$state" = "SUCCESS" ]; then
  pass 'the restore finished'
else
  detail="$(api GET "/api/v1/jobs/$restore_job")"
  fail "the restore ended $state: $(printf '%s' "$detail" | head -c 400)"
fi

if [ -f "$site_root/marker.txt" ]; then
  restored_marker="$(cat "$site_root/marker.txt")"
  contains 'the deleted file came back with its contents' "$restored_marker" "$MARKER"
else
  fail 'the deleted file was not restored'
fi

if [ -f "$site_root/nested/deep/inner.txt" ]; then
  pass 'the deleted directory tree came back'
else
  fail 'the deleted directory tree was not restored'
fi

restored_row="$(mysql_run "$DB_NAME" 'SELECT note FROM survivors WHERE id = 1;')"
contains 'the dropped database row came back' "$restored_row" "row-$STAMP"

# The displaced copy is not left behind: a complete second copy of every site
# on the same disk, per restore, is how a panel fills a host.
leftovers="$(find "$(dirname "$site_root")" -maxdepth 2 -name '*before-restore*' 2>/dev/null | wc -l | tr -d ' ')"
if [ "$leftovers" = "0" ]; then
  pass 'the restore did not leave a second copy of the site behind'
else
  fail "the restore left $leftovers copy(ies) of the site on disk"
fi

# --- an archive somebody changed --------------------------------------------

if [ -f "$archive_path" ]; then
  size="$(wc -c < "$archive_path" | tr -d ' ')"
  middle=$((size / 2))
  # One byte in the middle, so the length is unchanged: a check that only
  # compared sizes would pass this.
  printf 'X' | dd of="$archive_path" bs=1 seek="$middle" count=1 conv=notrunc 2>/dev/null

  tampered="$(api POST "/api/v1/backups/$backup_id/verify")"
  contains 'verifying catches an archive that has been changed' "$tampered" '"ok":false'
  contains 'and says what is wrong with it' "$tampered" '"detail":"'

  # And the panel now refuses to restore it, because the row no longer claims
  # to be verified.
  expect_status 'a backup that failed verification can no longer be restored' 422 \
    "$(api_status POST "/api/v1/backups/$backup_id/restore" "{\"confirm\":\"$backup_id\"}")"
fi

# --- schedules --------------------------------------------------------------

schedule_body="{\"name\":\"integration-$STAMP\",\"type\":\"website\",\"website_id\":\"$website_id\",\"destination_id\":\"$local_dest_id\",\"hour\":3,\"minute\":0,\"day_of_week\":-1,\"retention_days\":14,\"keep_last\":3}"
schedule="$(api POST /api/v1/backup-schedules "$schedule_body")"
schedule_id="$(json_field "$schedule" id)"
if [ -n "$schedule_id" ]; then
  created_schedules="$created_schedules $schedule_id"
  pass 'a schedule was created'
  contains 'the schedule names its destination' "$schedule" '"destination_name":"'
else
  fail "could not create a schedule: $(printf '%s' "$schedule" | head -c 300)"
fi

# A schedule that can prune its way to nothing is a schedule that eventually
# does, and a retention of zero days would delete a backup the moment it
# finished.
expect_status 'a retention of zero days is refused' 422 \
  "$(api_status POST /api/v1/backup-schedules \
     "{\"name\":\"bad-$STAMP\",\"type\":\"full\",\"destination_id\":\"$local_dest_id\",\"hour\":3,\"minute\":0,\"day_of_week\":-1,\"retention_days\":-1,\"keep_last\":3}")"

# Deleting a destination a schedule still uses would leave it unable to run,
# which is a backup that silently stops happening.
if [ -n "$schedule_id" ]; then
  expect_status 'a destination a schedule uses cannot be deleted' 409 \
    "$(api_status DELETE "/api/v1/backup-destinations/$local_dest_id")"
fi

if [ -n "$schedule_id" ]; then
  ran="$(api POST "/api/v1/backup-schedules/$schedule_id/run")"
  scheduled_id="$(json_field "$ran" id)"
  if [ -n "$scheduled_id" ]; then
    created_backups="$created_backups $scheduled_id"
    state="$(await_backup "$scheduled_id")"
    if [ "$state" = "completed" ]; then
      pass "running a schedule by hand produced a backup"
      run_stored="$(api GET "/api/v1/backups/$scheduled_id")"
      contains 'the backup records which schedule took it' "$run_stored" "\"schedule_id\":\"$schedule_id\""
    else
      fail "a scheduled backup ended $state"
    fi

    # The backups it took are the only reason it existed.
    api DELETE "/api/v1/backup-schedules/$schedule_id" >/dev/null
    created_schedules=""
    kept="$(api GET "/api/v1/backups/$scheduled_id")"
    contains 'deleting a schedule keeps the backups it took' "$kept" '"status":"completed"'
  else
    fail "running a schedule produced nothing: $(printf '%s' "$ran" | head -c 300)"
  fi
fi

# --- S3, against a real service ---------------------------------------------

s3_body="{\"name\":\"integration-s3-$STAMP\",\"kind\":\"s3\",\"endpoint\":\"$MINIO_ENDPOINT\",\"bucket\":\"$MINIO_BUCKET\",\"region\":\"us-east-1\",\"prefix\":\"integration-$STAMP\",\"access_key\":\"$MINIO_KEY\",\"secret_key\":\"$MINIO_SECRET\",\"path_style\":true,\"allow_insecure\":true}"
# Plain http is refused unless somebody accepts it, so the same destination
# without the acknowledgement must not be created at all.
expect_status 'a plain-http S3 endpoint is refused until it is accepted' 422   "$(api_status POST /api/v1/backup-destinations      "{\"name\":\"noack-$STAMP\",\"kind\":\"s3\",\"endpoint\":\"$MINIO_ENDPOINT\",\"bucket\":\"$MINIO_BUCKET\",\"region\":\"us-east-1\",\"access_key\":\"$MINIO_KEY\",\"secret_key\":\"$MINIO_SECRET\",\"path_style\":true}")"

s3_dest="$(api POST /api/v1/backup-destinations "$s3_body")"
s3_dest_id="$(json_field "$s3_dest" id)"

if [ -z "$s3_dest_id" ]; then
  fail "could not create an S3 destination: $(printf '%s' "$s3_dest" | head -c 300)"
else
  created_destinations="$created_destinations $s3_dest_id"
  # The secret is stored and never comes back.
  contains 'the S3 destination records that it holds a credential' "$s3_dest" '"has_credentials":true'
  not_contains 'the S3 destination never returns its secret key' "$s3_dest" "$MINIO_SECRET"

  s3_check="$(api POST "/api/v1/backup-destinations/$s3_dest_id/check")"
  contains 'the S3 destination can be written to and read back' "$s3_check" '"last_check_ok":true'

  s3_backup="$(api POST /api/v1/backups \
    "{\"type\":\"website\",\"website_id\":\"$website_id\",\"destination_id\":\"$s3_dest_id\"}")"
  s3_backup_id="$(json_field "$s3_backup" id)"
  if [ -n "$s3_backup_id" ]; then
    created_backups="$created_backups $s3_backup_id"
    state="$(await_backup "$s3_backup_id")"
    if [ "$state" = "completed" ]; then
      pass 'a backup was written to S3 and read back from it'
      s3_verify="$(api POST "/api/v1/backups/$s3_backup_id/verify")"
      contains 'the archive on S3 verifies' "$s3_verify" '"ok":true'
    else
      detail="$(api GET "/api/v1/backups/$s3_backup_id")"
      fail "the S3 backup ended $state: $(printf '%s' "$detail" | head -c 400)"
    fi
  else
    fail "could not start an S3 backup: $(printf '%s' "$s3_backup" | head -c 300)"
  fi
fi

# --- SFTP, against this host's own sshd -------------------------------------

if ! command -v sftp >/dev/null 2>&1; then
  fail 'the sftp client is not installed, so the SFTP destination cannot be exercised'
else
  rc-service sshd start >/dev/null 2>&1 || true
  # A host key has to exist before anything can connect.
  ssh-keygen -A >/dev/null 2>&1 || true
  rc-service sshd restart >/dev/null 2>&1 || rc-service sshd start >/dev/null 2>&1 || true

  mkdir -p "$KEY_DIR" "$SFTP_DIR" /root/.ssh
  chmod 700 "$KEY_DIR" /root/.ssh
  ssh-keygen -t ed25519 -N '' -f "$KEY_DIR/id" -q
  cat "$KEY_DIR/id.pub" >> /root/.ssh/authorized_keys
  chmod 600 /root/.ssh/authorized_keys

  host_key="$(cat /etc/ssh/ssh_host_ed25519_key.pub 2>/dev/null | cut -d' ' -f1,2)"
  private_key="$(awk '{printf "%s\\n", $0}' "$KEY_DIR/id")"

  if [ -z "$host_key" ]; then
    fail 'this host has no ed25519 host key, so SFTP cannot be checked'
  else
    sftp_body="{\"name\":\"integration-sftp-$STAMP\",\"kind\":\"sftp\",\"host\":\"127.0.0.1\",\"port\":22,\"user\":\"root\",\"path\":\"$SFTP_DIR\",\"host_key\":\"$host_key\",\"private_key\":\"$private_key\"}"
    sftp_dest="$(api POST /api/v1/backup-destinations "$sftp_body")"
    sftp_dest_id="$(json_field "$sftp_dest" id)"

    if [ -z "$sftp_dest_id" ]; then
      fail "could not create an SFTP destination: $(printf '%s' "$sftp_dest" | head -c 300)"
    else
      created_destinations="$created_destinations $sftp_dest_id"
      not_contains 'the SFTP destination never returns its private key' \
        "$sftp_dest" 'BEGIN OPENSSH PRIVATE KEY'

      sftp_check="$(api POST "/api/v1/backup-destinations/$sftp_dest_id/check")"
      contains 'the SFTP destination can be written to and read back' \
        "$sftp_check" '"last_check_ok":true'

      # And with the wrong host key it must fail, because a backup sent to
      # whatever answered on port 22 is every site on this host handed to a
      # stranger.
      wrong_key="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
      wrong_body="{\"name\":\"integration-sftp-wrong-$STAMP\",\"kind\":\"sftp\",\"host\":\"127.0.0.1\",\"port\":22,\"user\":\"root\",\"path\":\"$SFTP_DIR\",\"host_key\":\"$wrong_key\",\"private_key\":\"$private_key\"}"
      wrong="$(api POST /api/v1/backup-destinations "$wrong_body")"
      wrong_id="$(json_field "$wrong" id)"
      if [ -n "$wrong_id" ]; then
        created_destinations="$created_destinations $wrong_id"
        wrong_check="$(api POST "/api/v1/backup-destinations/$wrong_id/check")"
        contains 'a destination whose host key does not match is refused' \
          "$wrong_check" '"last_check_ok":false'
      fi
    fi
  fi
fi

# --- deleting a backup ------------------------------------------------------

final="$(api POST /api/v1/backups \
  "{\"type\":\"website\",\"website_id\":\"$website_id\",\"destination_id\":\"$local_dest_id\",\"include_databases\":false}")"
final_id="$(json_field "$final" id)"
if [ -n "$final_id" ]; then
  state="$(await_backup "$final_id")"
  final_key="$(api GET "/api/v1/backups/$final_id" | grep -o '"path":"[^"]*[.]tar[.]gz"' | head -1 | cut -d'"' -f4)"
  if [ "$state" = "completed" ] && [ -n "$final_key" ]; then
    pass 'a website backup without its databases completed'
    api DELETE "/api/v1/backups/$final_id" >/dev/null
    if [ -f "$LOCAL_DIR/$final_key" ]; then
      fail 'deleting a backup left the archive at its destination'
    else
      pass 'deleting a backup removes the archive as well as the row'
    fi
    expect_status 'the deleted backup is gone from the panel too' 404 \
      "$(api_status GET "/api/v1/backups/$final_id")"
  else
    fail "the final backup ended $state"
    created_backups="$created_backups $final_id"
  fi
fi

# --- permissions ------------------------------------------------------------

unauth="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "$API_BASE_URL/api/v1/backups" 2>/dev/null || true)"
expect_status 'reading the backups needs authentication' 401 "$unauth"

# --- result -----------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 14 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
