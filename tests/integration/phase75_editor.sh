#!/bin/sh
# Phase 7.5 Docker integration test — Code Editor.
#
# The editor is two endpoints on top of the Phase 7 file API, so this checks
# what those two actually do to a file on disk: that a save writes exactly the
# bytes sent, that a stale save is refused rather than silently winning, and
# that a file the editor must not open is refused before it can be corrupted.
#
# Run with:  make docker-test-editor

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

ROOT="/var/www"
DOMAIN="editor.phase75.integration.test"
BASE="$ROOT/$DOMAIN"
WORK="$BASE/public"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# curl drives the API, python3 builds the round-trip payloads, and psql reads
# the audit table because there is no audit endpoint until a later phase.
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

expect_refused() {
  name="$1"; got="$2"
  case "$got" in
    400|403|404|409|422) pass "$name (refused with $got)" ;;
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

urlencode() {
  printf '%s' "$1" | sed -e 's|/|%2F|g' -e 's| |%20|g' -e 's|\.\.|%2E%2E|g'
}

# json_string reads one string field out of a JSON response.
json_string() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

log "Phase 7.5 code editor checks"
log "API: $API_BASE_URL"
log ""

login
if [ -z "${token:-}" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# --------------------------------------------------------------- clean slate

rm -rf "$BASE"
mkdir -p "$WORK"
if id nginx >/dev/null 2>&1; then
  chown -R nginx:nginx "$BASE"
fi
chmod 750 "$BASE" "$WORK"

printf '<?php\necho "hello";\n' > "$WORK/index.php"
chmod 640 "$WORK/index.php"
if id nginx >/dev/null 2>&1; then
  chown nginx:nginx "$WORK/index.php"
fi

# ------------------------------------------------------------ authorization

log "Authorization"

expect_status "reading content without a token returns 401" 401 \
  "$(api_status GET "/api/v1/files/content?path=$(urlencode "$WORK/index.php")" '' '')"
expect_status "saving without a token returns 401" 401 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/index.php\",\"content\":\"x\"}" '')"
expect_status "reading content with a valid token returns 200" 200 \
  "$(api_status GET "/api/v1/files/content?path=$(urlencode "$WORK/index.php")")"

# --------------------------------------------------------------- traversal

log ""
log "Path validation"

for hostile in "/etc/passwd" "$WORK/../../../../etc/shadow" "relative/file.php" "/root/.bashrc"; do
  expect_refused "opening $hostile is refused" \
    "$(api_status GET "/api/v1/files/content?path=$(urlencode "$hostile")")"
  expect_refused "saving to $hostile is refused" \
    "$(api_status PUT /api/v1/files/content "{\"path\":\"$hostile\",\"content\":\"pwned\"}")"
done

if [ -f /etc/passwd ] && grep -q 'pwned' /etc/passwd 2>/dev/null; then
  fail "a save reached /etc/passwd"
else
  pass "no save reached a file outside the root"
fi

# --------------------------------------------------------------- open a file

log ""
log "Opening a file"

response="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/index.php")")"
contains "the content is returned" "$response" 'echo \"hello\";'
contains "the language is derived from the name" "$response" '"language":"php"'
contains "the mode is reported" "$response" '"mode":"0640"'
contains "a checksum is returned" "$response" '"checksum":"'
contains "the line ending is reported" "$response" '"end_of_line":"lf"'

checksum="$(json_string "$response" checksum)"
if [ -n "$checksum" ]; then
  pass "the checksum is a usable value"
else
  fail "no checksum was returned, so a stale save cannot be detected"
fi

# ---------------------------------------------------------------- save a file

log ""
log "Saving a file"

new_content='<?php\necho \"goodbye\";\n'
expect_status "a save is accepted" 200 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/index.php\",\"content\":\"$new_content\",\"checksum\":\"$checksum\"}")"

if grep -q 'goodbye' "$WORK/index.php" 2>/dev/null; then
  pass "the new content is on disk"
else
  fail "the save reported success but the file was not changed"
fi
if grep -q 'hello' "$WORK/index.php" 2>/dev/null; then
  fail "the old content is still in the file"
else
  pass "the old content was replaced, not appended to"
fi

# The file keeps its owner and mode: a save is an edit, not a re-creation.
mode="$(stat -c '%a' "$WORK/index.php" 2>/dev/null || echo '')"
if [ "$mode" = "640" ]; then
  pass "the file keeps its permissions across a save"
else
  fail "the mode changed to $mode across a save"
fi
if id nginx >/dev/null 2>&1; then
  owner="$(stat -c '%U' "$WORK/index.php" 2>/dev/null || echo '')"
  if [ "$owner" = "nginx" ]; then
    pass "the file keeps its owner across a save"
  else
    fail "the owner changed to $owner across a save"
  fi
fi

# Saving a shorter file must not leave the old tail behind.
response="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/index.php")")"
checksum="$(json_string "$response" checksum)"
expect_status "a shorter save is accepted" 200 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/index.php\",\"content\":\"x\",\"checksum\":\"$checksum\"}")"
size="$(wc -c < "$WORK/index.php" | tr -d ' ')"
if [ "$size" = "1" ]; then
  pass "a shorter save truncated the file to its new length"
else
  fail "the file is $size bytes after saving one byte"
fi

# An empty save must empty the file rather than leave it alone.
response="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/index.php")")"
checksum="$(json_string "$response" checksum)"
expect_status "an empty save is accepted" 200 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/index.php\",\"content\":\"\",\"checksum\":\"$checksum\"}")"
size="$(wc -c < "$WORK/index.php" | tr -d ' ')"
if [ "$size" = "0" ]; then
  pass "an empty save leaves an empty file"
else
  fail "the file is $size bytes after saving nothing"
fi

# --------------------------------------------------------------- stale saves

log ""
log "Concurrent edits"

printf 'original\n' > "$WORK/shared.txt"
if id nginx >/dev/null 2>&1; then
  chown nginx:nginx "$WORK/shared.txt"
fi

response="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/shared.txt")")"
stale="$(json_string "$response" checksum)"

# Someone else writes to the file while the editor has it open.
printf 'changed by someone else\n' > "$WORK/shared.txt"

expect_status "a save with a stale checksum is refused" 409 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/shared.txt\",\"content\":\"my version\",\"checksum\":\"$stale\"}")"

if grep -q 'changed by someone else' "$WORK/shared.txt" 2>/dev/null; then
  pass "the other person's work survived the refused save"
else
  fail "a refused save still overwrote the file"
fi

# Overwriting is possible, but only by asking for it.
expect_status "an explicit overwrite is accepted" 200 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/shared.txt\",\"content\":\"my version\",\"checksum\":\"$stale\",\"force\":true}")"
if grep -q 'my version' "$WORK/shared.txt" 2>/dev/null; then
  pass "the forced overwrite took effect"
else
  fail "the forced overwrite did not write"
fi

# A save with no checksum at all is how a new file is written.
expect_status "a save with no checksum creates a file" 200 \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/created.txt\",\"content\":\"new file\"}")"
if [ -f "$WORK/created.txt" ] && grep -q 'new file' "$WORK/created.txt"; then
  pass "the new file exists with its content"
else
  fail "the file was not created"
fi
if id nginx >/dev/null 2>&1; then
  owner="$(stat -c '%U' "$WORK/created.txt" 2>/dev/null || echo '')"
  if [ "$owner" = "nginx" ]; then
    pass "a file created by the editor inherits its folder's owner"
  else
    fail "the created file is owned by $owner, not the folder's owner"
  fi
fi

# --------------------------------------------------- large file protection

log ""
log "Large file protection"

# Just over the 2 MiB editor limit.
python3 -c "
import sys
with open('$WORK/large.log', 'w') as handle:
    handle.write('x' * (2 * 1024 * 1024 + 1024))
" 2>/dev/null || dd if=/dev/zero bs=1024 count=2049 2>/dev/null | tr '\0' 'x' > "$WORK/large.log"

expect_refused "opening a file over the limit is refused" \
  "$(api_status GET "/api/v1/files/content?path=$(urlencode "$WORK/large.log")")"

detail="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/large.log")")"
contains "the refusal explains what to do instead" "$detail" "Download it instead"

# It is still downloadable: too big to edit is not too big to fetch.
expect_status "a file too large to edit can still be downloaded" 200 \
  "$(api_status GET "/api/v1/files/download?path=$(urlencode "$WORK/large.log")")"

# ------------------------------------------------------------ binary files

log ""
log "Binary files"

printf 'PK\003\004\000\000binary\000content\n' > "$WORK/archive.bin"

expect_refused "opening a binary file is refused" \
  "$(api_status GET "/api/v1/files/content?path=$(urlencode "$WORK/archive.bin")")"
detail="$(api GET "/api/v1/files/content?path=$(urlencode "$WORK/archive.bin")")"
contains "the refusal explains why" "$detail" "not text"

before="$(wc -c < "$WORK/archive.bin" | tr -d ' ')"
expect_refused "saving content with a null byte is refused" \
  "$(api_status PUT /api/v1/files/content \
     "{\"path\":\"$WORK/archive.bin\",\"content\":\"text\\u0000more\"}")"
after="$(wc -c < "$WORK/archive.bin" | tr -d ' ')"
if [ "$before" = "$after" ]; then
  pass "the binary file was not corrupted by a refused save"
else
  fail "a refused save changed the file from $before to $after bytes"
fi

# A directory is not a file to edit.
expect_refused "opening a directory is refused" \
  "$(api_status GET "/api/v1/files/content?path=$(urlencode "$WORK")")"

# ------------------------------------------------------------ round trip

log ""
log "Round trip"

# Content with the characters that break naive JSON or shell handling.
python3 - <<PY 2>/dev/null || true
import json, subprocess, sys

tricky = '<?php\n\$x = "quotes \\" and \\\\ backslash";\n// tab\there\n// unicode: café ünïcode 日本語\n\$y = \'single\';\n'
path = "$WORK/tricky.php"
body = json.dumps({"path": path, "content": tricky})

save = subprocess.run([
    "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "60",
    "-X", "PUT", "$API_BASE_URL/api/v1/files/content",
    "-H", "Authorization: Bearer $token",
    "-H", "Content-Type: application/json",
    "-d", body,
], capture_output=True, text=True)

if save.stdout.strip() != "200":
    print("  FAIL  saving content with quotes, tabs and unicode (HTTP %s)" % save.stdout.strip())
    sys.exit(0)

with open(path, "r", encoding="utf-8") as handle:
    on_disk = handle.read()

if on_disk == tricky:
    print("  PASS  quotes, backslashes, tabs and unicode survive a save exactly")
else:
    print("  FAIL  the saved file does not match what was sent")
    sys.exit(0)

read = subprocess.run([
    "curl", "-s", "--max-time", "60",
    "$API_BASE_URL/api/v1/files/content?path=" + path.replace("/", "%2F"),
    "-H", "Authorization: Bearer $token",
], capture_output=True, text=True)

payload = json.loads(read.stdout)
if payload.get("data", {}).get("content") == tricky:
    print("  PASS  reading it back returns exactly what was written")
else:
    print("  FAIL  the round trip changed the content")
PY

# A file larger than one transport chunk still round-trips, which is what
# proves the chunked write reassembles in the right order.
#
# The body goes to a file and curl reads it with @: a few hundred kilobytes on
# the command line exceeds the container's argument limit, and the failure looks
# like the test hanging rather than the test being wrong.
if python3 - <<'PY' 2>/dev/null
lines = ['line %05d: the quick brown fox jumps over the lazy dog' % i for i in range(8000)]
with open('/tmp/phase75-big.txt', 'w') as handle:
    handle.write('\n'.join(lines))
PY
then
  python3 - "$WORK/big.txt" <<'PY' > /tmp/phase75-body.json 2>/dev/null
import json, sys
content = open('/tmp/phase75-big.txt').read()
sys.stdout.write(json.dumps({"path": sys.argv[1], "content": content}))
PY
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 120 \
    -X PUT "$API_BASE_URL/api/v1/files/content" \
    -H "Authorization: Bearer $token" \
    -H 'Content-Type: application/json' \
    --data-binary @/tmp/phase75-body.json 2>/dev/null || true)"

  if [ "$code" != "200" ]; then
    fail "saving a file larger than one chunk (HTTP $code)"
  elif cmp -s /tmp/phase75-big.txt "$WORK/big.txt"; then
    bytes="$(wc -c < /tmp/phase75-big.txt | tr -d ' ')"
    pass "a file spanning several transport chunks saves byte for byte ($bytes bytes)"
  else
    fail "a multi-chunk save did not reassemble correctly"
  fi
else
  log "  SKIP  the multi-chunk check needs python3"
fi

# ------------------------------------------------------------------ audit

log ""
log "Audit"

AUDIT_DB_URL="${DATABASE_URL:-postgres://${POSTGRES_USER:-jothost}:${POSTGRES_PASSWORD:-jothost_dev_password}@postgres:5432/${POSTGRES_DB:-jothost}?sslmode=disable}"
if command -v psql >/dev/null 2>&1; then
  audited="$(psql "$AUDIT_DB_URL" -t -A \
    -c "select count(*) from audit_logs where action = 'file.write'" 2>/dev/null || echo '')"
  if [ -n "$audited" ] && [ "$audited" -gt 0 ] 2>/dev/null; then
    pass "saves reached the audit log ($audited events)"
  else
    fail "no save reached the audit log"
  fi
else
  log "  SKIP  the audit check needs psql in this container"
fi

# ------------------------------------------------------------------ cleanup

log ""
log "Cleanup"
rm -rf "$BASE" /tmp/phase75-big.txt /tmp/phase75-body.json
if [ -d "$BASE" ]; then
  fail "test files were left behind"
else
  pass "test files were removed"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 7.5 code editor checks passed"
else
  log "FAILED: $failures Phase 7.5 check(s) failed"
  exit 1
fi
