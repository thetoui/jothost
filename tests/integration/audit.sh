#!/bin/sh
# Reading the audit trail.
#
# The trail has been written since Phase 1 and, until now, nothing could read
# it back: `audit.view` was a permission that guarded no endpoint at all. The
# panel's answer to "who deleted that website" was to connect to PostgreSQL.
#
# Phase 24's sweep found it the embarrassing way — two of its own checks
# pointed at /api/v1/audit and were passing on the 404, which is what a
# refusal and a missing route look like when you only read the status code.
# So the first thing these checks establish is that the endpoint exists.
#
# Run with:  make docker-test-audit

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DOMAIN="audit$STAMP.test"

failures=0
token=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

control() {
  name="$1"; got="$2"; want="$3"
  if [ "$got" = "$want" ]; then
    printf '  ctrl  %s\n' "$name"
  else
    printf '  CTRL  %s (expected %s, got %s) — the checks below it prove nothing\n' \
      "$name" "$want" "$got"
    failures=$((failures + 1))
  fi
}

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; bearer="${3-$token}"
  curl -s -o /dev/null -w '%{http_code}' --max-time 30 -X "$method" "$API_BASE_URL$path" \
    -H "Authorization: Bearer $bearer" 2>/dev/null || true
}

json_field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1; }
json_number() { printf '%s' "$1" | sed -n "s/.*\"$2\":\([0-9]*\).*/\1/p" | head -n 1; }

await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 90 ]; do
    state="$(json_field "$(api GET "/api/v1/jobs/$job_id")" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

log "Audit trail — reading what the panel records"
log ""

token=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
  sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { log "FATAL: could not sign in as $ADMIN_USER"; exit 1; }

# ------------------------------------------------------- 1. the route exists

log "1. The endpoint exists"

# 200 exactly, not "not an error". A 404 is what this looked like for the
# panel's whole life, and it is indistinguishable from a refusal if the only
# thing being checked is that the request did not succeed.
control "GET /api/v1/audit answers" "$(api_status GET /api/v1/audit)" "200"
control "GET /api/v1/audit/actions answers" "$(api_status GET /api/v1/audit/actions)" "200"

# --------------------------------------------- 2. an action lands in the trail

log ""
log "2. A real action appears in the trail"

created=$(api POST /api/v1/websites "{\"domain\":\"$DOMAIN\"}")
website_id=$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')
job_id=$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')

if [ -z "$website_id" ]; then
  log "FATAL: could not create a website: $(printf '%s' "$created" | head -c 300)"
  exit 1
fi
control "the website was created" "$(await_job "$job_id")" "SUCCESS"

trail=$(api "GET" "/api/v1/audit?resource_type=website&resource_id=$website_id")
case "$trail" in
  *'"action":"website.create"'*) pass "the creation was recorded against the website" ;;
  *) fail "no website.create entry for $website_id: $(printf '%s' "$trail" | head -c 250)" ;;
esac

# The trail is worth nothing if it cannot say who. A uuid does not answer it,
# which is why the query joins users.
case "$trail" in
  *"\"username\":\"$ADMIN_USER\""*) pass "and it names the account that did it" ;;
  *) fail "the entry does not name the actor: $(printf '%s' "$trail" | head -c 250)" ;;
esac

case "$trail" in
  *"\"domain\":\"$DOMAIN\""*) pass "and carries the detail that says which website" ;;
  *) fail "the entry has no metadata naming the domain" ;;
esac

# Clean up before the assertions about filtering, so the deletion is recorded
# too and the resource has two entries rather than one.
delete_job=$(json_field "$(api DELETE "/api/v1/websites/$website_id?remove_files=true")" id)
[ -n "$delete_job" ] && await_job "$delete_job" >/dev/null

# ------------------------------------------------------------ 3. filtering

log ""
log "3. Filtering"

both=$(api "GET" "/api/v1/audit?resource_type=website&resource_id=$website_id")
total=$(json_number "$both" total)
if [ "${total:-0}" -ge 2 ]; then
  pass "the website's history holds both its creation and its deletion ($total entries)"
else
  fail "expected at least two entries for the website, got ${total:-none}"
fi

only_create=$(api "GET" "/api/v1/audit?resource_id=$website_id&action=website.create")
if [ "$(json_number "$only_create" total)" = "1" ]; then
  pass "filtering by action narrows it to one"
else
  fail "action filter returned $(json_number "$only_create" total), expected 1"
fi

# The action list is read from the trail rather than from a list in the code:
# every feature appends its own names, and a filter offering names nothing ever
# recorded is worse than no filter.
actions=$(api GET /api/v1/audit/actions)
case "$actions" in
  *'"website.create"'*) pass "the action list is built from what was actually recorded" ;;
  *) fail "website.create is missing from the action list: $(printf '%s' "$actions" | head -c 200)" ;;
esac

# ------------------------------------------------------------- 4. refusals

log ""
log "4. What it refuses"

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected $want, got $got)"; fi
}

expect_status "reading the trail needs authentication" 401 \
  "$(api_status GET /api/v1/audit "")"
expect_status "a malformed resource id is refused rather than crashing the query" 400 \
  "$(api_status GET "/api/v1/audit?resource_id=not-a-uuid")"
expect_status "an outcome that is not an outcome is refused" 400 \
  "$(api_status GET "/api/v1/audit?status=MAYBE")"
expect_status "a date the API cannot read is refused" 400 \
  "$(api_status GET "/api/v1/audit?since=last-tuesday")"

# ------------------------------------------------------------- 5. paging

log ""
log "5. Paging"

paged=$(api "GET" "/api/v1/audit?limit=1")
if [ "$(json_number "$paged" count)" = "1" ]; then
  pass "a page holds what was asked for"
else
  fail "limit=1 returned $(json_number "$paged" count) entries"
fi

# The count and the total are different numbers and the difference is the whole
# point: a page of 1 out of thousands must not read as a history of 1.
if [ "$(json_number "$paged" total)" -gt 1 ]; then
  pass "and still reports how many matched in total ($(json_number "$paged" total))"
else
  fail "total was not larger than the page: $(printf '%s' "$paged" | head -c 200)"
fi

# An unbounded read of a table that grows for the life of the installation is a
# way to ask the panel to load its own history into memory.
capped=$(api "GET" "/api/v1/audit?limit=100000")
if [ "$(json_number "$capped" limit)" = "200" ]; then
  pass "an oversized page is capped, and says so"
else
  fail "limit was reported as $(json_number "$capped" limit), expected the 200 cap"
fi

# ------------------------------------------------- 6. it is still append-only

log ""
log "6. The trail cannot be rewritten through the API"

# The table has had triggers rejecting UPDATE and DELETE since migration 0001.
# Adding a way to read it must not have added a way to change it.
for method in POST PUT PATCH DELETE; do
  code="$(api_status "$method" /api/v1/audit)"
  case "$code" in
    404|405) pass "$method /api/v1/audit is not a route" ;;
    *) fail "$method /api/v1/audit answered $code — the trail must be read-only" ;;
  esac
done

log ""
if [ "$failures" -eq 0 ]; then
  log "All audit trail checks passed."
  exit 0
fi
log "$failures audit trail check(s) failed."
exit 1
