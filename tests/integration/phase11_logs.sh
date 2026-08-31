#!/bin/sh
# Phase 11 Docker integration test — the log viewer.
#
# Black-box checks against the running stack, run inside the Agent's own
# container so that what the panel returns can be compared with the files on
# disk. That comparison is the point of this suite: a log viewer that returned
# plausible-looking lines nobody checked against the file would pass a test
# written any other way.
#
# What it proves, for real: the panel finds the logs this host has and says
# which ones it does not have; the lines it shows are the lines in the file;
# search and level filtering happen on the host rather than in the browser;
# following a log returns only what was appended; a rotated file is reported as
# rotated; a download is byte-for-byte the file; and a log that is a symlink out
# of the allowed roots is refused — which is the difference between a log viewer
# and a way to read any file on the machine.
#
# Run with:  make docker-test-logs

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# A Node application log directory, because those are contributed at runtime
# from what is on disk: creating one proves the dynamic half of the catalogue as
# well as giving this suite a log it owns and may write to.
CHECK_APP="p11-logcheck"
CHECK_DIR="/var/log/jothost/node/$CHECK_APP"
CHECK_LOG="$CHECK_DIR/out.log"
CHECK_KEY="node.$CHECK_APP.out"

# A second directory whose log is a symlink pointing out of the allowed roots.
EVIL_APP="p11-symlink"
EVIL_DIR="/var/log/jothost/node/$EVIL_APP"
EVIL_KEY="node.$EVIL_APP.out"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

cleanup() {
  rm -rf "$CHECK_DIR" "$EVIL_DIR" /tmp/p11-download.log 2>/dev/null || true
}
trap cleanup EXIT

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  curl -s --max-time 60 "$API_BASE_URL$1" -H "Authorization: Bearer $token" 2>/dev/null || true
}

api_status() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 60 "$API_BASE_URL$1" \
    -H "Authorization: Bearer $token" 2>/dev/null || true
}

anon_status() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$API_BASE_URL$1" 2>/dev/null || true
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 240))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  if [ -z "$needle" ]; then
    fail "$name (the value searched for is empty, so this check proves nothing)"
    return
  fi
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

# entry LISTING KEY — the JSON object for one source.
entry() {
  printf '%s' "$1" | tr '{' '\n' | grep -F "\"key\":\"$2\"" | head -n 1
}

# field JSON NAME — one numeric field's value.
field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\([0-9-]*\).*/\1/p" | head -n 1
}

# ------------------------------------------------------------------ the run

log 'Phase 11 — logs'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# --- 1. authorization -----------------------------------------------------

log ''
log '1. Authorization'

expect_status 'listing logs without a token is refused' 401 "$(anon_status /api/v1/logs)"
expect_status 'reading a log without a token is refused' 401 "$(anon_status /api/v1/logs/agent)"
expect_status 'downloading a log without a token is refused' 401 \
  "$(anon_status /api/v1/logs/agent/download)"

# --- 2. what this host has ------------------------------------------------

log ''
log '2. The catalogue'

# A log the suite creates, which also exercises the runtime half of the
# catalogue: Node application logs are found by reading the directory the Agent
# writes into, not from the panel's database.
mkdir -p "$CHECK_DIR"
printf '%s\n' \
  'starting up' \
  'GET /index.php served' \
  'a problem occurred' > "$CHECK_LOG"

listing="$(api /api/v1/logs)"
contains 'the panel lists the logs on this host' "$listing" '"sources":'
contains 'and the levels it can filter by' "$listing" '"levels":'
contains 'nginx access is catalogued' "$listing" '"key":"nginx.access"'
contains 'nginx error is catalogued' "$listing" '"key":"nginx.error"'
contains "the agent's own audit log is catalogued" "$listing" '"key":"agent"'

check_entry="$(entry "$listing" "$CHECK_KEY")"
if [ -z "$check_entry" ]; then
  fail 'a log created at runtime appears without the panel being told'
else
  pass 'a log created at runtime appears without the panel being told'
  contains 'it is reported as present' "$check_entry" '"present":true'
  contains 'with the path it was found at' "$check_entry" "$CHECK_LOG"

  size="$(field "$check_entry" size)"
  actual="$(wc -c < "$CHECK_LOG" | tr -d ' ')"
  if [ "$size" = "$actual" ]; then
    pass "the size reported is the file's own ($actual bytes)"
  else
    fail "the size reported is the file's own (said $size, file is $actual)"
  fi
fi

# A catalogued log this host does not have is still listed. "nginx has recorded
# no errors" and "this panel does not offer that log" are different answers.
system_entry="$(entry "$listing" system)"
if [ -z "$system_entry" ]; then
  fail 'a log this host does not have is still listed'
else
  pass 'a log this host does not have is still listed'
  contains 'and is marked absent rather than hidden' "$system_entry" '"present":false'
fi

# --- 3. reading ------------------------------------------------------------

log ''
log '3. Reading a log'

tail_body="$(api "/api/v1/logs/$CHECK_KEY")"
contains 'the lines in the file are the lines returned' "$tail_body" 'a problem occurred'
contains 'from the beginning of it' "$tail_body" 'starting up'
contains 'and the path is reported' "$tail_body" "$CHECK_LOG"

# The line count is honoured, and the *last* lines are the ones kept: a log
# viewer showing the beginning of a long file is showing the least useful part.
two="$(api "/api/v1/logs/$CHECK_KEY?limit=2")"
not_contains 'asking for fewer lines returns the last of them' "$two" 'starting up'
contains 'including the most recent' "$two" 'a problem occurred'

# Searching happens on the host. A browser filtering what it was already sent
# would have had to be sent the whole file first.
found="$(api "/api/v1/logs/$CHECK_KEY?search=index.php")"
contains 'search returns the matching line' "$found" 'GET /index.php served'
not_contains 'and only the matching line' "$found" 'a problem occurred'
if [ "$(field "$found" filtered)" = "2" ]; then
  pass 'and says how many lines it hid'
else
  fail "and says how many lines it hid (filtered = $(field "$found" filtered), want 2)"
fi

# Level filtering against real data: every audit line is an operation that
# either worked or did not, and "show me the failures" is the question worth
# asking of it.
errors="$(api '/api/v1/logs/agent?level=error&limit=20')"
if printf '%s' "$errors" | grep -q '"level":"error"'; then
  pass 'filtering the audit log by level returns failures'
  not_contains 'and no successful operations' "$errors" '"status":"SUCCESS"'
else
  # A host where nothing has ever failed is a legitimate outcome, not a bug.
  log '  ...skipped: this host has recorded no failed operations'
fi

# --- 4. following ----------------------------------------------------------

log ''
log '4. Following'

before="$(api "/api/v1/logs/$CHECK_KEY")"
offset="$(field "$before" offset)"
printf '%s\n' 'appended while following' >> "$CHECK_LOG"

after="$(api "/api/v1/logs/$CHECK_KEY?after=$offset")"
contains 'following returns what was appended' "$after" 'appended while following'
not_contains 'and not what was already read' "$after" 'starting up'
contains 'and does not report a rotation' "$after" '"rotated":false'

# A line still being written must not be shown in halves: the offset stops at
# the end of the last complete line, and the fragment arrives whole next time.
printf 'half a line' >> "$CHECK_LOG"
partial="$(api "/api/v1/logs/$CHECK_KEY?after=$(field "$after" offset)")"
not_contains 'a half-written line is not shown' "$partial" 'half a line'
printf ' and the rest\n' >> "$CHECK_LOG"
completed="$(api "/api/v1/logs/$CHECK_KEY?after=$(field "$after" offset)")"
contains 'and arrives whole once it is finished' "$completed" 'half a line and the rest'

# Rotation: the file is replaced by a shorter one, so the caller's offset now
# points past its end.
far="$(field "$completed" offset)"
printf '%s\n' 'a fresh file' > "$CHECK_LOG"
rotated="$(api "/api/v1/logs/$CHECK_KEY?after=$far")"
contains 'a rotated log is reported as rotated' "$rotated" '"rotated":true'
contains 'and the new file is shown' "$rotated" 'a fresh file'

# --- 5. download -----------------------------------------------------------

log ''
log '5. Download'

printf '%s\n' 'line one' 'line two' 'line three' > "$CHECK_LOG"
code="$(curl -s -o /tmp/p11-download.log -w '%{http_code}' --max-time 60 \
  "$API_BASE_URL/api/v1/logs/$CHECK_KEY/download" \
  -H "Authorization: Bearer $token" 2>/dev/null || true)"
expect_status 'a log can be downloaded' 200 "$code"

if cmp -s /tmp/p11-download.log "$CHECK_LOG"; then
  pass 'and what arrives is byte-for-byte the file on disk'
else
  fail 'and what arrives is byte-for-byte the file on disk'
fi

headers="$(curl -s -D - -o /dev/null --max-time 60 \
  "$API_BASE_URL/api/v1/logs/$CHECK_KEY/download" \
  -H "Authorization: Bearer $token" 2>/dev/null || true)"
# A log is attacker-influenced content: anyone who can make a request can write
# a line into an access log. It is served as a download and never as something
# a browser will render on the panel's own origin.
contains 'it is served as an attachment' "$headers" 'attachment;'
contains 'as an opaque byte stream' "$headers" 'application/octet-stream'
contains 'with sniffing disabled' "$headers" 'nosniff'
contains 'and is not cached anywhere' "$headers" 'no-store'

expect_status 'downloading a log this host does not have is a 404' 404 \
  "$(api_status /api/v1/logs/system/download)"

# --- 6. the spec's URLs ----------------------------------------------------

log ''
log '6. The documented routes'

# API_SPEC section 19 spells these with a slash; the Agent's keys use a dot.
# Both reach the same log.
expect_status 'GET /logs/nginx/access works as documented' 200 \
  "$(api_status /api/v1/logs/nginx/access)"
contains 'and names the same source as the key does' "$(api /api/v1/logs/nginx/access)" \
  '"key":"nginx.access"'

# --- 7. what is refused ----------------------------------------------------

log ''
log '7. Refusals'

# A request names a key from the Agent's catalogue. A path is not a key, and
# neither is anything else a caller invents. This is the whole security of the
# feature: without it, a log viewer is a file reader.
for bad in \
  'etc/passwd' \
  '..%2F..%2Fetc%2Fpasswd' \
  '%2Fetc%2Fshadow' \
  'nginx.access%00' \
  'NGINX.ACCESS' \
  'invented'
do
  code="$(api_status "/api/v1/logs/$bad")"
  if [ "$code" = "404" ] || [ "$code" = "400" ]; then
    pass "a log outside the catalogue is refused: $bad"
  else
    fail "a log outside the catalogue is refused: $bad (got HTTP $code)"
  fi
done

expect_status 'a level outside the vocabulary is refused' 422 \
  "$(api_status '/api/v1/logs/agent?level=critical')"
expect_status 'a negative offset is refused' 422 \
  "$(api_status '/api/v1/logs/agent?after=-1')"
expect_status 'a limit beyond the maximum is refused' 422 \
  "$(api_status '/api/v1/logs/agent?limit=100000')"
expect_status 'a limit that is not a number is refused' 422 \
  "$(api_status '/api/v1/logs/agent?limit=lots')"

# --- 8. path safety --------------------------------------------------------

log ''
log '8. A log that is a symlink'

# nginx's access log is writable by the account nginx runs as on many hosts.
# If a symlink there were followed, a compromised web server would become a way
# to read any file on the machine through the panel.
mkdir -p "$EVIL_DIR"
ln -sf /etc/shadow "$EVIL_DIR/out.log"

listing="$(api /api/v1/logs)"
evil_entry="$(entry "$listing" "$EVIL_KEY")"
if [ -z "$evil_entry" ]; then
  pass 'a log symlinked out of the allowed roots is not offered'
else
  contains 'a log symlinked out of the allowed roots is reported absent' \
    "$evil_entry" '"present":false'
fi

body="$(api "/api/v1/logs/$EVIL_KEY")"
not_contains 'and reading it returns nothing from the file it points at' "$body" 'root:'
code="$(api_status "/api/v1/logs/$EVIL_KEY")"
if [ "$code" = "404" ]; then
  pass 'reading it is a 404'
else
  fail "reading it is a 404 (got HTTP $code)"
fi

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 11 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
