#!/bin/sh
# Phase 24 — the load test.
#
# A different question from the audit suite's. Not "can this be broken into"
# but "does it stay correct when several people use it at once", which is the
# ordinary condition of a hosting panel and the one nothing in this repository
# has ever put it in.
#
# ---------------------------------------------------------------------------
# What a load test here is for, and what it is not
#
# It is not a benchmark. The number of requests a second this panel can serve
# on a developer's laptop, inside Docker, on a shared machine, is not a fact
# about the panel — and publishing it as one would be inventing a figure. So
# nothing here asserts a throughput.
#
# What it asserts is **correctness under concurrency**, which is a real
# property and does not depend on the machine:
#
#   1. No request fails. Not "few": none. A 500 under load is a bug that exists
#      without load and was merely hard to reach.
#   2. No request is answered wrongly. Every response is checked, not counted —
#      a load test that only counts status codes cannot see two sessions being
#      served each other's data, which is the failure that matters most.
#   3. The rate limiter does not throttle authenticated reads. It exists to
#      make password guessing expensive, and a limiter that also throttled an
#      operator's dashboard would be a denial of service the panel performs on
#      itself.
#   4. Concurrent writes do not collide. The panel is asked to create the same
#      thing several times at once, and exactly one must succeed.
#   5. It is still healthy afterwards, with no connection or memory left
#      leaking — measured by the panel still answering the same way it did
#      before, having done thousands of requests in between.
#
# Run with:  make docker-test-load

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# Modest and fixed. The point is concurrency, not volume: a bigger number would
# measure the laptop rather than the panel, and would make the suite slow enough
# that nobody ran it.
WORKERS="${LOAD_WORKERS:-12}"
REQUESTS_PER_WORKER="${LOAD_REQUESTS:-40}"

STAMP="$(date +%s)"
WORK=/tmp/phase24-load
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

cleanup() { rm -rf "$WORK"; return 0; }
trap cleanup EXIT

rm -rf "$WORK"; mkdir -p "$WORK"

log "Phase 24 — load"
log ""

token=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
  sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { log "FATAL: could not sign in as $ADMIN_USER"; exit 1; }

single() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 30 "$API_BASE_URL$1" \
    -H "Authorization: Bearer $token" 2>/dev/null || true
}

control "the panel answers before any load is applied" "$(single /api/v1/websites)" "200"

# --------------------------------------------------- 1. concurrent reads

log ""
log "1. $WORKERS concurrent readers, $REQUESTS_PER_WORKER requests each"

# A mixture of endpoints rather than one, because the interesting failures are
# in what they share: the connection pool, the Redis client, the Agent socket.
# Hammering one handler exercises one code path and calls it load.
PATHS='/api/v1/websites
/api/v1/dashboard
/api/v1/auth/me
/api/v1/tenancy/overview
/api/v1/jobs
/api/v1/services'

worker() {
  id="$1"
  out="$WORK/w$id"
  : > "$out"
  n=0
  while [ "$n" -lt "$REQUESTS_PER_WORKER" ]; do
    for path in $PATHS; do
      # The body is kept, not discarded. Section 2 reads it: a load test that
      # only counted status codes could not see one session being handed
      # another's data, which is the failure worth finding here.
      body=$(curl -s --max-time 30 "$API_BASE_URL$path" \
        -H "Authorization: Bearer $token" -w '\n%{http_code}' 2>/dev/null || true)
      code=$(printf '%s' "$body" | tail -n 1)
      # A "000" is not an answer from the panel: curl could not complete the
      # connection at all. Under a burst of concurrency that is a client-side
      # hiccup — an ephemeral port not yet freed, a listen backlog momentarily
      # full — and immediately retrying succeeds. A real fault (the panel
      # refusing connections) fails the retry too and is then counted. An
      # actual HTTP status is never retried: a 500 stays a 500, a 429 a 429.
      if [ "$code" = "000" ]; then
        body=$(curl -s --max-time 30 "$API_BASE_URL$path" \
          -H "Authorization: Bearer $token" -w '\n%{http_code}' 2>/dev/null || true)
        code=$(printf '%s' "$body" | tail -n 1)
      fi
      printf '%s %s\n' "$code" "$path" >> "$out"
      case "$(printf '%s' "$body" | head -n -1)" in
        *'"username":"'*)
          who=$(printf '%s' "$body" | grep -o '"username":"[^"]*"' | head -n 1)
          printf '%s\n' "$who" >> "$WORK/identities"
          ;;
      esac
    done
    n=$((n + 1))
  done
}

started=$(date +%s)
i=0
while [ "$i" -lt "$WORKERS" ]; do
  worker "$i" &
  i=$((i + 1))
done
wait
elapsed=$(( $(date +%s) - started ))

cat "$WORK"/w* > "$WORK/all" 2>/dev/null || true
total=$(wc -l < "$WORK/all" | tr -d ' ')
ok=$(grep -c '^200 ' "$WORK/all" || true)
log "        $total requests in ${elapsed}s"

if [ "$total" -lt 100 ]; then
  control "enough requests were made to mean anything" "$total" "at least 100"
fi

if [ "$ok" = "$total" ]; then
  pass "every one of the $total requests was answered 200"
else
  fail "$((total - ok)) of $total requests were not 200:
$(grep -v '^200 ' "$WORK/all" | sort | uniq -c | head -10)"
fi

# 429 deserves its own sentence. The throttle is for password guessing; an
# authenticated read that gets throttled is the panel denying service to the
# person who is paying for it.
# `|| true`, not `|| echo 0`: grep -c already prints 0 when it matches nothing
# and *also* exits non-zero, so the fallback printed a second zero, the
# comparison read two lines instead of one, and a section with nothing wrong
# with it reported a failure. The same shape as the curl fallback in the other
# two suites.
throttled=$(grep -c '^429 ' "$WORK/all" || true)
if [ "$throttled" = "0" ]; then
  pass "no authenticated read was throttled"
else
  fail "$throttled authenticated reads were throttled by the login limiter"
fi

# 500 deserves its own sentence too, because it means something different from
# a refusal: a fault the panel did not expect, which exists without load and
# was merely hard to reach.
faults=$(grep -c '^5' "$WORK/all" || true)
if [ "$faults" = "0" ]; then
  pass "nothing answered with a server error"
else
  fail "$faults requests answered with a server error"
fi

# ----------------------------------------------- 2. answered correctly

log ""
log "2. Answered correctly, not merely answered"

# Every identity seen across every concurrent request must be the one that
# signed in. Two sessions being served each other's data is the failure a
# status-code count cannot see, and the one that would matter most.
if [ -f "$WORK/identities" ]; then
  seen=$(sort -u "$WORK/identities" | wc -l | tr -d ' ')
  if [ "$seen" = "1" ]; then
    pass "every response that named a user named the same one ($(sort -u "$WORK/identities"))"
  else
    fail "responses named $seen different users under concurrency:
$(sort -u "$WORK/identities" | head -5)"
  fi
else
  fail "no response carried an identity, so nothing could be compared"
fi

# ------------------------------------------- 3. concurrent writes collide

log ""
log "3. The same thing created several times at once"

# Uniqueness has to be the database's answer, not a check in Go: two API
# processes can both look, both find nothing, and both insert. The panel is
# asked for the same account by six workers at once, and exactly one must get
# it.
NAME="load$STAMP"
: > "$WORK/creates"
i=0
while [ "$i" -lt 6 ]; do
  (
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -X POST \
      "$API_BASE_URL/api/v1/tenancy/accounts" -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' \
      -d "{\"username\":\"$NAME\",\"password\":\"phase24-load-password-9271\",\"tier\":\"customer\"}" \
      2>/dev/null || true)
    printf '%s\n' "$code" >> "$WORK/creates"
  ) &
  i=$((i + 1))
done
wait

created=$(grep -c '^201$' "$WORK/creates" || true)
refused=$(grep -cE '^(409|422)$' "$WORK/creates" || true)
if [ "$created" = "1" ]; then
  pass "exactly one of six concurrent creations succeeded"
else
  fail "$created of six concurrent creations succeeded, not one"
fi
if [ "$((created + refused))" = "6" ]; then
  pass "and the other five were refused cleanly, not with a fault"
else
  fail "some concurrent creations neither succeeded nor were refused:
$(sort "$WORK/creates" | uniq -c)"
fi

# Tidy up the account that did get created.
acc=$(curl -s --max-time 20 "$API_BASE_URL/api/v1/tenancy/accounts" \
  -H "Authorization: Bearer $token" 2>/dev/null |
  tr '{' '\n' | grep "\"username\":\"$NAME\"" |
  grep -o '"id":"[^"]*"' | head -n 1 | sed 's/^[^:]*:"//; s/"$//')
[ -n "$acc" ] && curl -s -o /dev/null --max-time 20 -X DELETE \
  "$API_BASE_URL/api/v1/tenancy/accounts/$acc" -H "Authorization: Bearer $token" 2>/dev/null || true

# -------------------------------------------------- 4. still well afterwards

log ""
log "4. Still well afterwards"

# The panel has just done thousands of requests. A pool that leaked a
# connection per request, or a goroutine per request, shows up here as a panel
# that has stopped answering — which is why this is the same check that ran
# before the load, asked again.
ready=$(curl -s --max-time 20 "$API_BASE_URL/readyz" 2>/dev/null || true)
case "$ready" in
  *'"ready":true'*) pass "readiness still reports every dependency up" ;;
  *) fail "readiness after the load: $(printf '%s' "$ready" | head -c 200)" ;;
esac

if [ "$(single /api/v1/websites)" = "200" ]; then
  pass "and it answers as it did before the load"
else
  fail "the panel does not answer the way it did before the load"
fi

# A fresh sign-in, because the pool is what a login needs and it is the thing
# most likely to have been exhausted.
fresh=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null || true)
case "$fresh" in
  *access_token*) pass "a new session can still be opened" ;;
  *) fail "a new session could not be opened after the load" ;;
esac

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 24 load checks passed."
  exit 0
fi
log "$failures Phase 24 load check(s) failed."
exit 1
