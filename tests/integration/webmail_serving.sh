#!/bin/sh
# Webmail works when it is installed, and serves nothing it should not.
#
# Two defects, found together by loading webmail over HTTP for the first time.
#
# It never worked. Roundcube keeps contacts, preferences and sessions in
# SQLite, and the installer's PHP had no SQLite driver: every request answered
# 500 with "Class PDO not found" in the site's PHP log.
#
# And fixing only that would have published it. The whole release was unpacked
# into the document root and made readable by the web server's group, and under
# nginx Roundcube's .htaccess protects nothing: its error log, its temp files
# (attachments being written), its database (Roundcube chmods it 0640) and every
# script in vendor/ were a URL away. Roundcube ships public_html/ so that the web
# root can be exactly that; the Agent now draws the same line with file modes,
# and this checks the line from the outside, as a visitor.
#
# Every "not served" check compares bytes: the vhost sends unknown paths to
# index.php, so a private file shows up as the login page with a 200, and a
# status code alone would say nothing.
#
# Needs the internet: the Agent downloads the pinned, checksummed release.
#
# Run with:  make docker-test-suite   (discovered by tests/run-integration.sh)

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"
# The PHP the test image carries Roundcube's extensions for.
WEBMAIL_PHP="${WEBMAIL_PHP:-8.4}"

STAMP="$(date +%s)"
SITE="webmail$STAMP.test"
PROBE="jothost-webmail-probe-$STAMP"
failures=0
token=""
site_id=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

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

field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p" | head -n 1; }
website_id() { printf '%s' "$1" | sed -n 's/.*"website":{"id":"\([^"]*\)".*/\1/p'; }

api() {
  api_method="$1"; api_path="$2"; api_body="${3:-}"
  if [ -n "$api_body" ]; then
    curl -sS --max-time 600 -X "$api_method" "$API_BASE_URL$api_path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "$api_body"
  else
    curl -sS --max-time 60 -X "$api_method" "$API_BASE_URL$api_path" \
      -H "Authorization: Bearer $token"
  fi
}

status_of() {
  curl -sS -o /dev/null -w '%{http_code}' --max-time 600 -X "$1" "$API_BASE_URL$2" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "${3:-}"
}

served_status() { curl -sS -o /dev/null -w '%{http_code}' -H "Host: $SITE" "http://127.0.0.1$1" 2>/dev/null || true; }
served_body()   { curl -sS -H "Host: $SITE" "http://127.0.0.1$1" 2>/dev/null || true; }
served_sum()    { served_body "$1" | sha256sum | cut -d' ' -f1; }
disk_sum()      { sha256sum "$1" | cut -d' ' -f1; }

# The file at DOCROOT/rel is what the site serves at /rel.
expect_served() {
  es_label="$1"; es_rel="$2"
  if [ ! -f "$DOCROOT/$es_rel" ]; then
    fail "$es_label (there is no $es_rel on disk to serve)"
  elif [ "$(served_sum "/$es_rel")" = "$(disk_sum "$DOCROOT/$es_rel")" ]; then
    pass "$es_label"
  else
    fail "$es_label (/$es_rel answered HTTP $(served_status "/$es_rel") with other bytes)"
  fi
}

# The file at DOCROOT/rel is not what the site serves at /rel.
expect_private() {
  ep_label="$1"; ep_rel="$2"
  if [ ! -f "$DOCROOT/$ep_rel" ]; then
    fail "$ep_label (there is no $ep_rel on disk; the check would prove nothing)"
  elif [ "$(served_sum "/$ep_rel")" = "$(disk_sum "$DOCROOT/$ep_rel")" ]; then
    fail "$ep_label (/$ep_rel is served)"
  else
    pass "$ep_label"
  fi
}

await_job() {
  aj_waited=0
  [ -n "$1" ] || { printf 'NO JOB'; return 0; }
  while [ "$aj_waited" -lt 120 ]; do
    aj_state="$(field "$(api GET "/api/v1/jobs/$1")" status)"
    case "$aj_state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$aj_state"; return 0 ;;
    esac
    sleep 2; aj_waited=$((aj_waited + 2))
  done
  printf 'TIMEOUT'
}

website_status() {
  api GET "/api/v1/websites/$1" | sed 's/"domains".*//' |
    sed -n 's/.*"status":"\([^"]*\)".*/\1/p'
}

wait_active() {
  wait_i=0
  while [ "$wait_i" -lt 45 ]; do
    wait_state="$(website_status "$1")"
    if [ "$wait_state" = "active" ]; then printf 'active'; return 0; fi
    wait_i=$((wait_i + 1))
    sleep 2
  done
  printf '%s' "${wait_state:-unknown}"
}

# The install is asynchronous and its last step walks the tree giving it to the
# site. It is finished when nothing is left owned by root and nothing is
# world-readable, whichever umask the unpack ran under.
wait_installed() {
  wi_i=0
  while [ "$wi_i" -lt 150 ]; do
    if [ -f "$DOCROOT/config/config.inc.php" ] && [ -d "$DOCROOT/db" ] &&
       [ -z "$(find "$DOCROOT" \( -user root -o -perm -o=r \) ! -type l -print 2>/dev/null | head -n 1)" ]; then
      printf 'installed'; return 0
    fi
    wi_i=$((wi_i + 1))
    sleep 2
  done
  printf 'not finished after 300s'
}

# A file written as root, world-readable, into a directory of the install. It
# stands for whatever the application writes there, at whatever mode.
plant() {
  printf '%s\n' "$PROBE" > "$DOCROOT/$1"
  chmod 0644 "$DOCROOT/$1"
}

cleanup() {
  if [ -n "$site_id" ]; then
    api DELETE /api/v1/mail/webmail >/dev/null 2>&1 || true
    api DELETE "/api/v1/websites/$site_id?remove_files=true" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

log 'Webmail works, and serves nothing it should not'
log '==============================================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'FATAL: could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. A website to serve it from'
site="$(api POST /api/v1/websites "{\"domain\":\"$SITE\"}")"
site_id="$(website_id "$site")"
if [ -z "$site_id" ]; then
  log "FATAL: the website was not created: $site"
  exit 1
fi
control 'the website became active' "$(wait_active "$site_id")" active
DOCROOT="$(api GET "/api/v1/websites/$site_id" | sed 's/"domains".*//' |
  sed -n 's/.*"document_root":"\([^"]*\)".*/\1/p')"
if [ -z "$DOCROOT" ] || [ ! -d "$DOCROOT" ]; then
  log "FATAL: no document root on this host for the site (got '$DOCROOT')"
  exit 1
fi
OWNER="$(stat -c %U "$DOCROOT")"
control 'the site serves its placeholder page' "$(served_status /)" 200

# Webmail is pointed at this host's mail server by name, and refuses to install
# without one. Set to what the mail suite uses if nothing has set it yet.
if [ -z "$(field "$(api GET /api/v1/mail)" hostname)" ]; then
  control 'the mail server is given a name' "$(status_of PUT /api/v1/mail/settings \
    '{"server_id":"","enabled":true,"hostname":"mail.jothost.test","require_tls":false,
      "spam_enabled":false,"spam_reject_score":15,"virus_enabled":false,
      "max_message_mb":25,"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}')" 200
fi

log ''
log '2. Installed, and running'
control 'the install is accepted' \
  "$(status_of POST /api/v1/mail/webmail "{\"website_id\":\"$site_id\"}")" 202
control 'the install finished' "$(wait_installed)" installed
php_job="$(api PATCH "/api/v1/websites/$site_id/php" "{\"version\":\"$WEBMAIL_PHP\"}" |
  sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
control "PHP $WEBMAIL_PHP is switched on for the site" "$(await_job "$php_job")" SUCCESS

login=""
i=0
while [ "$i" -lt 15 ]; do
  login="$(served_body /)"
  case "$login" in *rcmloginuser*) break ;; esac
  i=$((i + 1)); sleep 2
done
case "$login" in
  *rcmloginuser*) pass 'the login page renders' ;;
  *) fail "the login page does not render (HTTP $(served_status /); see $(dirname "$DOCROOT")/logs/php-error.log)" ;;
esac
if [ -s "$DOCROOT/db/roundcube.db" ]; then
  pass 'its database was created'
else
  fail 'no database was created; webmail cannot keep a session'
fi
if [ "$(stat -c %a "$DOCROOT/db/roundcube.db" 2>/dev/null)" = 600 ]; then
  pass 'and only the site can read it'
else
  fail "the database has mode $(stat -c %a "$DOCROOT/db/roundcube.db" 2>/dev/null)"
fi

log ''
log '3. What a browser needs is served'
expect_served 'the stylesheets' "$(cd "$DOCROOT" && find skins -name '*.css' -type f | head -n 1)"
expect_served 'the application script' program/js/app.min.js
expect_served 'a plugin script' "$(cd "$DOCROOT" && find plugins -name '*.js' -type f | head -n 1)"
expect_served 'the resources' "$(cd "$DOCROOT" && find program/resources -type f | head -n 1)"
plant skins/probe.txt
expect_served 'a file planted beside them (the control for every check below)' skins/probe.txt
rm -f "$DOCROOT/skins/probe.txt"

log ''
log '4. Nothing else is'
expect_private 'the configuration' config/config.inc.php
expect_private 'the sample configuration' config/config.inc.php.sample
expect_private 'the database' db/roundcube.db
expect_private 'the dependency manifest' composer.lock
expect_private 'the changelog' CHANGELOG.md
expect_private 'the schema' SQL/sqlite.initial.sql
expect_private 'the maintenance scripts' bin/initdb.sh
expect_private 'the autoloader' vendor/autoload.php
expect_private 'the installer' installer/index.php
expect_private 'the application code' program/include/iniset.php
# What the application writes at run time, planted world-readable: the
# directories alone must keep it in.
for dir in logs temp db config; do
  plant "$dir/probe.txt"
  expect_private "a file written into $dir/" "$dir/probe.txt"
  rm -f "$DOCROOT/$dir/probe.txt"
done
# A PHP file is run rather than served, so comparing bytes says nothing about
# it. What matters is whether it can be run as a page at all: vendor/ is a
# library tree nobody audited as a set of entry points.
for script in config/config.inc.php vendor/autoload.php installer/index.php \
              program/include/iniset.php; do
  code="$(served_status "/$script")"
  if [ "$code" = 404 ] || [ "$code" = 403 ]; then
    pass "$script cannot be run as a page"
  else
    fail "$script answers HTTP $code: nginx can see it and hands it to PHP"
  fi
done

log ''
log '5. Reinstalled, it keeps what users stored'
printf '%s\n' "$PROBE" > "$DOCROOT/db/probe.txt"
chown "$OWNER" "$DOCROOT/db/probe.txt"
db_before="$(disk_sum "$DOCROOT/db/roundcube.db")"
control 'the reinstall is accepted' \
  "$(status_of POST /api/v1/mail/webmail "{\"website_id\":\"$site_id\"}")" 202
# The first install's config is replaced, so a new one appearing marks the swap.
old_config="$(disk_sum "$DOCROOT/config/config.inc.php")"
i=0
while [ "$i" -lt 150 ] && [ "$(disk_sum "$DOCROOT/config/config.inc.php" 2>/dev/null)" = "$old_config" ]; do
  i=$((i + 1)); sleep 2
done
control 'the reinstall finished' "$(wait_installed)" installed
if [ "$(cat "$DOCROOT/db/probe.txt" 2>/dev/null)" = "$PROBE" ] &&
   [ "$(disk_sum "$DOCROOT/db/roundcube.db")" = "$db_before" ]; then
  pass 'the database survived the reinstall'
else
  fail 'the reinstall replaced the database: every user lost their contacts'
fi
case "$(served_body /)" in
  *rcmloginuser*) pass 'and webmail still runs on it' ;;
  *) fail 'webmail does not run after the reinstall' ;;
esac

log ''
if [ "$failures" -eq 0 ]; then
  log 'Webmail runs, and serves only what Roundcube publishes.'
  exit 0
fi
log "FAILED: $failures check(s) did not pass"
exit 1
