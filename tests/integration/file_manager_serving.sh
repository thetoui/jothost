#!/bin/sh
# What the file manager puts into a website can be served by that website.
#
# The file manager's own suite (phase7_files.sh) proves a great deal about the
# files it writes - their bytes, their names, their confinement - and nothing
# about whether anyone can read them over HTTP, because it works in a scratch
# directory rather than a live site.
#
# That gap hid a real defect. Site content is read by nginx through the web
# server's group, so a file needs group read and a folder group traversal. The
# Agent runs with umask 0077, which removed both: a file created, uploaded or
# extracted in the panel came out 0600 or 0700, and the site answered 403 for
# it. Every check below fetches the content from the site itself, because
# "the file exists with the right bytes" is exactly what was true while it was
# broken.
#
# Run with:  make docker-test-suite   (discovered by tests/run-integration.sh)

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
SITE="served$STAMP.test"
TOKEN_TEXT="jothost-served-$STAMP"
failures=0
token=""
site_id=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# POSIX sh has no local variables, so these are prefixed.
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
website_id() { printf '%s' "$1" | sed -n 's/.*"website":{"id":"\([^"]*\)".*/\1/p'; }
urlencode() { printf '%s' "$1" | sed -e 's|/|%2F|g' -e 's| |%20|g'; }

api() {
  api_method="$1"; api_path="$2"; api_body="${3:-}"
  if [ -n "$api_body" ]; then
    curl -sS -X "$api_method" "$API_BASE_URL$api_path" -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$api_body"
  else
    curl -sS -X "$api_method" "$API_BASE_URL$api_path" -H "Authorization: Bearer $token"
  fi
}

status_of() {
  curl -sS -o /dev/null -w '%{http_code}' -X "$1" "$API_BASE_URL$2" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "${3:-}"
}

upload() {
  up_dir="$1"; up_name="$2"
  printf '%s\n' "$TOKEN_TEXT" > "/tmp/$up_name"
  curl -sS -o /dev/null -w '%{http_code}' \
    -X POST "$API_BASE_URL/api/v1/files/upload?path=$(urlencode "$up_dir")" \
    -H "Authorization: Bearer $token" \
    -F "file=@/tmp/$up_name;filename=$up_name"
}

# Fetched from the site, the way a visitor would, not read off the disk.
served_status() { curl -sS -o /dev/null -w '%{http_code}' -H "Host: $SITE" "http://127.0.0.1$1" 2>/dev/null || true; }
served_body()   { curl -sS -H "Host: $SITE" "http://127.0.0.1$1" 2>/dev/null || true; }

expect_served() {
  es_label="$1"; es_path="$2"
  es_code="$(served_status "$es_path")"
  if [ "$es_code" = "200" ]; then
    pass "$es_label"
  else
    fail "$es_label (HTTP $es_code for $es_path)"
  fi
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

cleanup() {
  if [ -n "$site_id" ]; then
    api DELETE "/api/v1/websites/$site_id?remove_files=true" >/dev/null 2>&1 || true
  fi
  rm -f /tmp/uploaded.txt /tmp/nested.txt
}
trap cleanup EXIT

log 'What the file manager puts in a site can be served'
log '=================================================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'FATAL: could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. A live site to put things in'
site="$(api POST /api/v1/websites "{\"domain\":\"$SITE\"}")"
site_id="$(website_id "$site")"
if [ -z "$site_id" ]; then
  log "FATAL: the website was not created: $site"
  exit 1
fi
control 'the website became active' "$(wait_active "$site_id")" active

DOCROOT="$(api GET "/api/v1/websites/$site_id" | sed 's/"domains".*//' | sed -n 's/.*"document_root":"\([^"]*\)".*/\1/p')"
if [ -z "$DOCROOT" ] || [ ! -d "$DOCROOT" ]; then
  log "FATAL: no document root on this host for the site (got '$DOCROOT')"
  exit 1
fi
# Serving works at all - the Agent's own placeholder page. Without this, a 403
# below could be the site rather than the file manager.
control 'the site serves its placeholder page' "$(served_status /)" 200

log ''
log '2. Created in the file manager'
control 'a file is created' \
  "$(status_of POST /api/v1/files/file "{\"path\":\"$DOCROOT\",\"name\":\"created.html\"}")" 201
expect_served 'a file created in the panel is served' /created.html

log ''
log '3. Uploaded'
control 'a file is uploaded' "$(upload "$DOCROOT" uploaded.txt)" 201
expect_served 'an uploaded file is served' /uploaded.txt
case "$(served_body /uploaded.txt)" in
  *"$TOKEN_TEXT"*) pass 'and it serves what was uploaded' ;;
  *) fail 'the uploaded file is served with the wrong content' ;;
esac

log ''
log '4. In a folder made in the panel'
control 'a folder is created' \
  "$(status_of POST /api/v1/files/folder "{\"path\":\"$DOCROOT\",\"name\":\"assets\"}")" 201
control 'a file is uploaded into it' "$(upload "$DOCROOT/assets" nested.txt)" 201
expect_served 'a file in a folder made in the panel is served' /assets/nested.txt

log ''
log '5. Extracted from an archive'
control 'the folder is archived' \
  "$(status_of POST /api/v1/files/zip \
     "{\"sources\":[\"$DOCROOT/assets\"],\"destination\":\"$DOCROOT/assets.zip\"}")" 201
control 'the archive is extracted' \
  "$(status_of POST /api/v1/files/unzip \
     "{\"path\":\"$DOCROOT/assets.zip\",\"destination\":\"$DOCROOT/unpacked\"}")" 200
expect_served 'an extracted file is served' /unpacked/assets/nested.txt

log ''
if [ "$failures" -eq 0 ]; then
  log 'Everything the file manager put in the site was served.'
  exit 0
fi
log "FAILED: $failures check(s) did not pass"
exit 1
