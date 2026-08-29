#!/bin/sh
# Phase 7 Docker integration test — File Manager.
#
# Black-box checks against the running stack. Every operation goes through the
# API exactly as the panel does, and the result is then verified on disk: a job
# reporting SUCCESS is not evidence that a file was written, moved, or removed.
#
# The four security checks TASKS.md requires for this phase — path traversal,
# symlink escape, permissions, unauthorized access — are exercised here against
# a real filesystem, on top of the unit coverage in agent/internal/files.
#
# Run with:  make docker-test-files

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

ROOT="/var/www"
DOMAIN="files.phase7.integration.test"
BASE="$ROOT/$DOMAIN"
WORK="$BASE/public"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# curl drives the API, python3 builds a hostile zip, and psql reads the audit
# table because there is no audit endpoint until a later phase. Installed here
# rather than baked into the image: they are the test's dependencies, not the
# Agent's, and the Agent's image should stay as small as what it actually runs.
for tool in curl python3 psql; do
  if ! command -v "$tool" >/dev/null 2>&1; then
    case "$tool" in
      psql) apk add --no-cache postgresql-client >/dev/null 2>&1 || true ;;
      *)    apk add --no-cache "$tool" >/dev/null 2>&1 || true ;;
    esac
  fi
done

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

refresh_token() {
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Authorization: Bearer $token" "$API_BASE_URL/api/v1/auth/me" 2>/dev/null || true)"
  if [ "$code" = "401" ]; then
    login
  fi
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
  method="$1"; path="$2"; body="${3:-}"; bearer="${4-$token}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 60 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 60 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" 2>/dev/null || true
  fi
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

# expect_refused accepts any of the refusal codes, because which one is right
# depends on whether the API or the Agent caught it — and both are correct
# outcomes for input that must never reach the disk.
expect_refused() {
  name="$1"; got="$2"
  case "$got" in
    400|403|404|422) pass "$name (refused with $got)" ;;
    *) fail "$name (expected a refusal, got HTTP $got)" ;;
  esac
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
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

# urlencode percent-encodes a path for a query string.
urlencode() {
  printf '%s' "$1" | sed -e 's|/|%2F|g' -e 's| |%20|g' -e 's|\.\.|%2E%2E|g'
}

log "Phase 7 file manager checks"
log "API:  $API_BASE_URL"
log "Root: $ROOT"
log ""

login
if [ -z "${token:-}" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# --------------------------------------------------------------- clean slate

rm -rf "$BASE"
mkdir -p "$WORK"
# Owned the way a real site is: the panel must inherit this, not root.
if id nginx >/dev/null 2>&1; then
  chown -R nginx:nginx "$BASE"
fi
chmod 750 "$BASE" "$WORK"

# ------------------------------------------------------------ authorization

log "Authorization (TASKS.md Phase 7 security)"

expect_status "listing without a token returns 401" 401 \
  "$(api_status GET "/api/v1/files?path=$(urlencode "$WORK")" '' '')"
expect_status "download without a token returns 401" 401 \
  "$(api_status GET "/api/v1/files/download?path=$(urlencode "$WORK/x")" '' '')"
expect_status "delete without a token returns 401" 401 \
  "$(api_status DELETE "/api/v1/files?path=$(urlencode "$WORK/x")" '' '')"
expect_status "creating a folder without a token returns 401" 401 \
  "$(api_status POST /api/v1/files/folder "{\"path\":\"$WORK\",\"name\":\"x\"}" '')"
expect_status "a forged token is refused" 401 \
  "$(api_status GET "/api/v1/files?path=$(urlencode "$WORK")" '' 'not-a-real-token')"
expect_status "listing with a valid token returns 200" 200 \
  "$(api_status GET "/api/v1/files?path=$(urlencode "$WORK")")"

# --------------------------------------------------------------- traversal

log ""
log "Path traversal (TASKS.md Phase 7 security)"

# The Agent runs as root. A path that escapes its root is a full compromise,
# so each of these must be refused and must not read anything back.
for hostile in \
  "/etc/passwd" \
  "/etc/shadow" \
  "$ROOT/../../etc/passwd" \
  "$WORK/../../../../etc/passwd" \
  "/root/.ssh/id_rsa" \
  "relative/path" \
  "/"
do
  expect_refused "listing $hostile is refused" \
    "$(api_status GET "/api/v1/files?path=$(urlencode "$hostile")")"
  expect_refused "downloading $hostile is refused" \
    "$(api_status GET "/api/v1/files/download?path=$(urlencode "$hostile")")"
done

# And nothing leaks through the body either.
body="$(api GET "/api/v1/files/download?path=$(urlencode /etc/passwd)")"
not_contains "no /etc/passwd content is returned" "$body" "root:x:0:0"

# A name is a single segment: accepting a path would place a file anywhere.
for name in "../escape.txt" "nested/child.txt" "/absolute.txt" ".." "."; do
  expect_refused "creating a file named '$name' is refused" \
    "$(api_status POST /api/v1/files/file "{\"path\":\"$WORK\",\"name\":\"$name\"}")"
done
if [ -f "$ROOT/escape.txt" ] || [ -f "/escape.txt" ]; then
  fail "a refused name still created a file outside the folder"
  rm -f "$ROOT/escape.txt" /escape.txt
else
  pass "no file escaped the folder it was created in"
fi

# --------------------------------------------------------- symlink escape

log ""
log "Symlink escape (TASKS.md Phase 7 security)"

secret="/root/phase7-secret.txt"
printf 'this must never be readable through the panel\n' > "$secret"
chmod 600 "$secret"

ln -sfn "$secret" "$WORK/escape-file"
ln -sfn /root "$WORK/escape-dir"

expect_refused "reading through a symlink out of the root is refused" \
  "$(api_status GET "/api/v1/files/download?path=$(urlencode "$WORK/escape-file")")"
expect_refused "listing through a symlink out of the root is refused" \
  "$(api_status GET "/api/v1/files?path=$(urlencode "$WORK/escape-dir")")"
expect_refused "reading a file under an escaping symlink is refused" \
  "$(api_status GET "/api/v1/files/download?path=$(urlencode "$WORK/escape-dir/phase7-secret.txt")")"

leak="$(api GET "/api/v1/files/download?path=$(urlencode "$WORK/escape-file")")"
not_contains "the secret is not returned through the link" "$leak" "never be readable"

# Writing through the link must not reach the target either.
expect_refused "writing under an escaping symlink is refused" \
  "$(api_status POST /api/v1/files/file "{\"path\":\"$WORK/escape-dir\",\"name\":\"planted.txt\"}")"
if [ -f /root/planted.txt ]; then
  fail "a write reached outside the root through a symlink"
  rm -f /root/planted.txt
else
  pass "no file was planted outside the root"
fi

# The link is still listed, so someone can see it and remove it.
listing="$(api GET "/api/v1/files?path=$(urlencode "$WORK")")"
contains "the symlink is shown in the listing" "$listing" '"escape-file"'
contains "the symlink is labelled as one" "$listing" '"type":"symlink"'

rm -f "$WORK/escape-file" "$WORK/escape-dir" "$secret"

# ------------------------------------------------------------------ browse

log ""
log "Browsing"

listing="$(api GET "/api/v1/files?path=$(urlencode "$ROOT")")"
contains "the root lists its entries" "$listing" '"entries"'
contains "the listing reports a total" "$listing" '"total"'
# The root has no parent a caller may navigate to.
contains "the root reports no parent" "$listing" '"parent":""'

listing="$(api GET "/api/v1/files?path=$(urlencode "$WORK")")"
contains "a folder below the root reports its parent" "$listing" "\"parent\":\"$BASE\""

# ------------------------------------------------------- create and inspect

log ""
log "Creating files and folders"
refresh_token

expect_status "a new folder is created" 201 \
  "$(api_status POST /api/v1/files/folder "{\"path\":\"$WORK\",\"name\":\"assets\"}")"
if [ -d "$WORK/assets" ]; then
  pass "the folder exists on disk"
else
  fail "the API reported success but no folder was created"
fi

expect_status "a new file is created" 201 \
  "$(api_status POST /api/v1/files/file "{\"path\":\"$WORK\",\"name\":\"notes.txt\"}")"
if [ -f "$WORK/notes.txt" ]; then
  pass "the file exists on disk"
else
  fail "the API reported success but no file was created"
fi

# Creating over an existing file must not truncate it.
printf 'important content\n' > "$WORK/notes.txt"
expect_refused "creating over an existing file is refused" \
  "$(api_status POST /api/v1/files/file "{\"path\":\"$WORK\",\"name\":\"notes.txt\"}")"
if grep -q 'important content' "$WORK/notes.txt" 2>/dev/null; then
  pass "the existing file was not truncated by the refused create"
else
  fail "a refused create destroyed the file's contents"
fi

# A file the panel creates must belong to the site, not to root: a root-owned
# file in a document root is one PHP cannot write and the customer cannot fix.
if id nginx >/dev/null 2>&1; then
  owner="$(stat -c '%U' "$WORK/assets" 2>/dev/null || echo '')"
  if [ "$owner" = "nginx" ]; then
    pass "a created folder inherits its parent's owner ($owner)"
  else
    fail "a created folder is owned by $owner, not the folder's own owner"
  fi
fi

# ------------------------------------------------------- upload / download

log ""
log "Upload and download"
refresh_token

# Larger than one transport chunk (256 KiB), so the chunked path is what is
# actually exercised rather than a single-message shortcut.
payload=/tmp/phase7-upload.bin
: > "$payload"
i=0
while [ "$i" -lt 700 ]; do
  printf 'jothost-phase7-payload-line-%04d-0123456789abcdef0123456789abcdef\n' "$i" >> "$payload"
  i=$((i + 1))
done
size="$(wc -c < "$payload" | tr -d ' ')"

code="$(curl -s -o /tmp/phase7-upload.json -w '%{http_code}' --max-time 120 \
  -X POST "$API_BASE_URL/api/v1/files/upload?path=$(urlencode "$WORK")" \
  -H "Authorization: Bearer $token" \
  -F "file=@$payload;filename=upload.bin" 2>/dev/null || true)"
expect_status "an upload is accepted" 201 "$code"

if [ -f "$WORK/upload.bin" ]; then
  uploaded="$(wc -c < "$WORK/upload.bin" | tr -d ' ')"
  if [ "$uploaded" = "$size" ]; then
    pass "the uploaded file is the right size on disk ($size bytes, over one chunk)"
  else
    fail "uploaded file is $uploaded bytes, expected $size"
  fi
  if cmp -s "$payload" "$WORK/upload.bin"; then
    pass "the uploaded bytes match the original exactly"
  else
    fail "the uploaded file differs from what was sent"
  fi
else
  fail "the upload reported success but no file exists"
fi

# An upload lands owned by the site, like anything else the panel creates.
if id nginx >/dev/null 2>&1; then
  owner="$(stat -c '%U' "$WORK/upload.bin" 2>/dev/null || echo '')"
  if [ "$owner" = "nginx" ]; then
    pass "the uploaded file inherits its folder's owner"
  else
    fail "the uploaded file is owned by $owner"
  fi
fi

# Downloading it back must reproduce it byte for byte.
code="$(curl -s -o /tmp/phase7-download.bin -w '%{http_code}' --max-time 120 \
  "$API_BASE_URL/api/v1/files/download?path=$(urlencode "$WORK/upload.bin")" \
  -H "Authorization: Bearer $token" 2>/dev/null || true)"
expect_status "a download returns 200" 200 "$code"
if cmp -s "$payload" /tmp/phase7-download.bin; then
  pass "the downloaded bytes match the original exactly"
else
  fail "the download does not match what was uploaded"
fi

# A customer's HTML must never come back as HTML on the panel's own origin.
headers="$(curl -s -D - -o /dev/null --max-time 60 \
  "$API_BASE_URL/api/v1/files/download?path=$(urlencode "$WORK/upload.bin")" \
  -H "Authorization: Bearer $token" 2>/dev/null | tr 'A-Z' 'a-z' || true)"
contains "a download is served as an attachment" "$headers" "content-disposition: attachment"
contains "a download is served as an opaque type" "$headers" "application/octet-stream"
contains "content sniffing is disabled" "$headers" "x-content-type-options: nosniff"

# A directory is not a download.
expect_refused "downloading a directory is refused" \
  "$(api_status GET "/api/v1/files/download?path=$(urlencode "$WORK")")"

# --------------------------------------------------------- rename and move

log ""
log "Rename, copy and move"
refresh_token

expect_status "a file is renamed" 200 \
  "$(api_status PATCH /api/v1/files "{\"path\":\"$WORK/notes.txt\",\"name\":\"renamed.txt\"}")"
if [ -f "$WORK/renamed.txt" ] && [ ! -f "$WORK/notes.txt" ]; then
  pass "the rename took effect on disk"
else
  fail "the rename did not move the file"
fi

expect_status "a file is copied" 200 \
  "$(api_status POST /api/v1/files/copy \
     "{\"source\":\"$WORK/renamed.txt\",\"destination\":\"$WORK/assets/copied.txt\"}")"
if [ -f "$WORK/assets/copied.txt" ] && [ -f "$WORK/renamed.txt" ]; then
  pass "the copy exists and the original is still there"
else
  fail "the copy did not behave like a copy"
fi

expect_status "a file is moved" 200 \
  "$(api_status POST /api/v1/files/move \
     "{\"source\":\"$WORK/assets/copied.txt\",\"destination\":\"$WORK/moved.txt\"}")"
if [ -f "$WORK/moved.txt" ] && [ ! -f "$WORK/assets/copied.txt" ]; then
  pass "the move took effect on disk"
else
  fail "the move did not remove the source"
fi

# Moving out of the root would put a customer's file anywhere on the host.
expect_refused "moving a file out of the root is refused" \
  "$(api_status POST /api/v1/files/move \
     "{\"source\":\"$WORK/moved.txt\",\"destination\":\"/tmp/stolen.txt\"}")"
if [ -f /tmp/stolen.txt ]; then
  fail "a file was moved outside the root"
  rm -f /tmp/stolen.txt
else
  pass "no file landed outside the root"
fi

# Overwriting must be asked for.
expect_refused "a move will not silently overwrite" \
  "$(api_status POST /api/v1/files/move \
     "{\"source\":\"$WORK/moved.txt\",\"destination\":\"$WORK/renamed.txt\"}")"

# --------------------------------------------------------------- permissions

log ""
log "Permissions (TASKS.md Phase 7 security)"
refresh_token

expect_status "permissions are changed" 200 \
  "$(api_status PATCH /api/v1/files "{\"path\":\"$WORK/renamed.txt\",\"mode\":\"0600\"}")"
mode="$(stat -c '%a' "$WORK/renamed.txt" 2>/dev/null || echo '')"
if [ "$mode" = "600" ]; then
  pass "the mode is 0600 on disk"
else
  fail "the mode on disk is $mode, expected 600"
fi

# setuid in a document root is a local root exploit waiting for someone to run
# it, so the panel must never be able to set it.
for bad in "4755" "2755" "1777" "04755"; do
  expect_refused "mode $bad is refused" \
    "$(api_status PATCH /api/v1/files "{\"path\":\"$WORK/renamed.txt\",\"mode\":\"$bad\"}")"
done
mode="$(stat -c '%a' "$WORK/renamed.txt" 2>/dev/null || echo '')"
if [ "$mode" = "600" ]; then
  pass "a refused mode left the file unchanged"
else
  fail "a refused mode still altered the file to $mode"
fi

for bad in "abc" "999" "77777" ""; do
  expect_refused "malformed mode '$bad' is refused" \
    "$(api_status PATCH /api/v1/files "{\"path\":\"$WORK/renamed.txt\",\"mode\":\"$bad\"}")"
done

# A PATCH that changes nothing is a mistake, not a silent no-op.
expect_refused "a patch with nothing to change is refused" \
  "$(api_status PATCH /api/v1/files "{\"path\":\"$WORK/renamed.txt\"}")"

# ------------------------------------------------------------------ archive

log ""
log "Archive and extract"
refresh_token

printf 'inside the archive\n' > "$WORK/assets/archived.txt"
chmod 640 "$WORK/assets/archived.txt"

expect_status "an archive is created" 201 \
  "$(api_status POST /api/v1/files/zip \
     "{\"sources\":[\"$WORK/assets\"],\"destination\":\"$WORK/assets.zip\"}")"
if [ -s "$WORK/assets.zip" ]; then
  pass "the archive exists and is not empty"
else
  fail "no archive was written"
fi

expect_status "an archive is extracted" 200 \
  "$(api_status POST /api/v1/files/unzip \
     "{\"path\":\"$WORK/assets.zip\",\"destination\":\"$WORK/restored\"}")"
if [ -f "$WORK/restored/assets/archived.txt" ] &&
   grep -q 'inside the archive' "$WORK/restored/assets/archived.txt" 2>/dev/null; then
  pass "the extracted file has its original content"
else
  fail "the archive did not round-trip"
fi

# Zip slip: an entry named ../../etc/cron.d/evil escapes the destination on any
# extractor that joins names blindly. This is the most dangerous input the file
# manager accepts, because the Agent extracts as root.
if command -v python3 >/dev/null 2>&1; then
  python3 - "$WORK/slip.zip" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1], "w") as archive:
    archive.writestr("../../../../tmp/phase7-escaped.txt", "escaped")
    archive.writestr("/tmp/phase7-absolute.txt", "escaped")
PY
  expect_refused "an archive that escapes its destination is refused" \
    "$(api_status POST /api/v1/files/unzip \
       "{\"path\":\"$WORK/slip.zip\",\"destination\":\"$WORK/slipout\"}")"
  if [ -f /tmp/phase7-escaped.txt ] || [ -f /tmp/phase7-absolute.txt ]; then
    fail "a zip entry escaped the destination"
    rm -f /tmp/phase7-escaped.txt /tmp/phase7-absolute.txt
  else
    pass "nothing was written outside the destination"
  fi
else
  log "  SKIP  zip slip check needs python3"
fi

# A file that is not an archive must be reported as such, not half-extracted.
expect_refused "extracting something that is not a zip is refused" \
  "$(api_status POST /api/v1/files/unzip \
     "{\"path\":\"$WORK/renamed.txt\",\"destination\":\"$WORK/notazip\"}")"

# ------------------------------------------------------------------- search

log ""
log "Search"
refresh_token

printf 'alpha\nbeta phase7needle here\ngamma\n' > "$WORK/assets/haystack.txt"

result="$(api GET "/api/v1/files/search?path=$(urlencode "$WORK")&query=haystack")"
contains "a name search finds the file" "$result" "haystack.txt"

result="$(api GET "/api/v1/files/search?path=$(urlencode "$WORK")&query=phase7needle&content=true")"
contains "a content search finds the file" "$result" "haystack.txt"
contains "a content search returns the matching line" "$result" "phase7needle"

result="$(api GET "/api/v1/files/search?path=$(urlencode "$WORK")&query=nothingmatchesthis")"
contains "a search with no matches returns an empty set" "$result" '"matches":[]'

# An empty query matches every file on the host.
expect_refused "an empty search is refused" \
  "$(api_status GET "/api/v1/files/search?path=$(urlencode "$WORK")&query=")"

# ------------------------------------------------------------------- delete

log ""
log "Delete"
refresh_token

expect_status "a file is deleted" 200 \
  "$(api_status DELETE "/api/v1/files?path=$(urlencode "$WORK/moved.txt")")"
if [ -f "$WORK/moved.txt" ]; then
  fail "the file survived a delete that reported success"
else
  pass "the file is gone from disk"
fi

# A non-empty directory must not go without asking.
expect_refused "deleting a non-empty folder without recursive is refused" \
  "$(api_status DELETE "/api/v1/files?path=$(urlencode "$WORK/assets")")"
if [ -d "$WORK/assets" ]; then
  pass "the folder survived the refused delete"
else
  fail "a refused delete removed the folder anyway"
fi

expect_status "a folder is deleted recursively" 200 \
  "$(api_status DELETE "/api/v1/files?path=$(urlencode "$WORK/assets")&recursive=true")"
if [ -d "$WORK/assets" ]; then
  fail "the folder survived a recursive delete"
else
  pass "the folder and its contents are gone"
fi

# The root itself must never be removable through the file manager.
expect_refused "deleting the root is refused" \
  "$(api_status DELETE "/api/v1/files?path=$(urlencode "$ROOT")&recursive=true")"
if [ -d "$ROOT" ]; then
  pass "the site root still exists"
else
  fail "the site root was deleted"
fi

# ------------------------------------------------------------------ audit

log ""
log "Audit"
refresh_token

# There is no audit-log endpoint yet — it belongs to a later phase — so the
# rows are read from the database directly.
#
# This is checked rather than assumed because it already went wrong once: the
# path was passed as resource_id, which is a uuid column, so every file audit
# write failed a type cast. The API logged it and the panel carried on with no
# file audit trail at all (CLAUDE.md section 15).
AUDIT_DB_URL="${DATABASE_URL:-postgres://${POSTGRES_USER:-jothost}:${POSTGRES_PASSWORD:-jothost_dev_password}@postgres:5432/${POSTGRES_DB:-jothost}?sslmode=disable}"

if command -v psql >/dev/null 2>&1; then
  audited="$(psql "$AUDIT_DB_URL" \
    -t -A -c "select count(*) from audit_logs where action like 'file.%'" 2>/dev/null || echo '')"
  if [ -n "$audited" ] && [ "$audited" -gt 0 ] 2>/dev/null; then
    pass "file changes reached the audit log ($audited events)"
  else
    fail "no file action reached the audit log"
  fi
else
  log "  SKIP  the audit check needs psql in this container"
fi

# ------------------------------------------------------------------ cleanup

log ""
log "Cleanup"
rm -rf "$BASE" /tmp/phase7-upload.bin /tmp/phase7-download.bin /tmp/phase7-upload.json
if [ -d "$BASE" ]; then
  fail "test files were left behind"
else
  pass "test files were removed"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 7 file manager checks passed"
else
  log "FAILED: $failures Phase 7 check(s) failed"
  exit 1
fi
