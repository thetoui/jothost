#!/bin/sh
# Phase 24 security audit — the adversarial suite.
#
# Every other suite in this repository drives the panel the way an operator
# would. This one drives it the way somebody trying to get in would: with no
# token, with a stolen one, with a token that should have stopped working, with
# paths that leave the root, with values that would close a command line, and
# from an account that holds no permissions at all.
#
# ---------------------------------------------------------------------------
# The rule this suite has to keep, and the reason it exists
#
# **A refusal proves nothing unless the request reached the code that refused
# it.**
#
# That is not a hypothetical. Writing this suite, a set of path-traversal
# probes came back refused, one after another, and every one of them was a lie:
# the query parameter never arrived, so the answer was "you gave me no path"
# dressed up as "I will not read that path". A typo made the whole section
# pass while testing nothing.
#
# So every section here begins with a **control**: the same endpoint, the same
# shape of request, with a value that must be *accepted*. If the control fails,
# the refusals below it mean nothing and the suite says so rather than
# reporting a wall of green.
#
# ---------------------------------------------------------------------------
# What is proved here
#
#   1. What an unauthenticated caller can learn, and that it is bounded.
#   2. Authentication: every way of not having a token is refused; logout takes
#      effect immediately; a replayed refresh token tears the session down.
#   3. RBAC: **every route registered in the source** refuses an account with
#      no permissions. The list is read from the source at run time, so a route
#      added tomorrow without a guard fails this suite rather than waiting to
#      be noticed.
#   4. Path traversal: every endpoint that takes a path refuses to leave its
#      root — including through a symlink, which normalisation alone misses.
#   5. Command injection: values that would close a shell command, an nginx
#      directive or an SQL statement are refused before they reach one.
#   6. Privilege escalation: the tenancy hierarchy, impersonation, and quotas.
#   7. Rate limiting: the login throttle actually throttles.
#   8. Secrets: what the panel stores never comes back out.
#
# It runs from a container on the stack's network with the repository mounted
# read-only, because section 3 reads the routes out of the source.
#
# Run with:  make docker-test-security-audit

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"
SRC="${SRC_DIR:-/src}"

STAMP="$(date +%s)"
LOW_USER="sec$STAMP"
LOW_PASS="phase24-low-privilege-pw-9271"
SITE_DOMAIN="sec$STAMP.test"

failures=0
controls=0
token=""
low_token=""
low_id=""
website_id=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# control asserts that a request the suite depends on actually works.
#
# A failed control is reported apart from a failed refusal, because they mean
# opposite things: a failed refusal is a hole in the panel, a failed control is
# a hole in this suite.
control() {
  name="$1"; got="$2"; want="$3"
  if [ "$got" = "$want" ]; then
    printf '  ctrl  %s\n' "$name"
  else
    printf '  CTRL  %s (expected %s, got %s) — the checks below it prove nothing\n' \
      "$name" "$want" "$got"
    controls=$((controls + 1))
    failures=$((failures + 1))
  fi
}

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

login() {
  curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
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

code_for() {
  method="$1"; path="$2"; bearer="${3:-}"; body="${4:-}"
  set -- -s -o /dev/null -w '%{http_code}' --max-time 30 -X "$method" "$API_BASE_URL$path"
  [ -n "$bearer" ] && set -- "$@" -H "Authorization: Bearer $bearer"
  [ -n "$body" ] && set -- "$@" -H 'Content-Type: application/json' -d "$body"
  # See the note in tests/recovery/phase24_recovery.sh: curl prints 000
  # itself, and what the fallback is for is its exit status under `set -e`.
  curl "$@" 2>/dev/null || true
}

# refused accepts any of the ways the panel says no, 404 included.
#
# 404 is a legitimate refusal here: the file manager answers "no such file" for
# a path outside its root, which is a better answer than 403 because it does
# not confirm the file exists. What makes it safe to accept is the *control* at
# the head of each section — without one, 404 would also be the answer for a
# route that does not exist, and the check would pass for ever while testing
# nothing.
refused() {
  name="$1"; code="$2"
  case "$code" in
    400|401|403|404|409|422) pass "$name" ;;
    *) fail "$name (it answered HTTP $code)" ;;
  esac
}

# denied is refused()'s stricter form: the caller was authenticated and the
# route exists, so the only correct answer is 403.
#
# Neither accepts 404, deliberately. A path that does not exist answers 404,
# which reads exactly like a refusal — so a check aimed at a route that was
# renamed, or never existed, passes for ever while testing nothing. That is not
# hypothetical: "a customer cannot read the audit log" pointed at
# /api/v1/audit, which no handler serves, and it had been passing.
denied() {
  name="$1"; code="$2"
  case "$code" in
    403) pass "$name" ;;
    404) fail "$name (HTTP 404 — this route does not exist, so nothing was refused)" ;;
    *) fail "$name (it answered HTTP $code rather than 403)" ;;
  esac
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 200))" ;;
  esac
}

lacks() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

json_field() {
  printf '%s' "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -n 1 | sed 's/^[^:]*:"//; s/"$//'
}

cleanup() {
  [ -n "$website_id" ] && api DELETE "/api/v1/websites/$website_id?remove_files=true" >/dev/null 2>&1
  [ -n "$low_id" ] && api DELETE "/api/v1/tenancy/accounts/$low_id" >/dev/null 2>&1
  return 0
}
trap cleanup EXIT

log "Phase 24 — security audit"
log ""

token="$(login "$ADMIN_USER" "$ADMIN_PASS")"
if [ -z "$token" ]; then
  log "FATAL: could not sign in as $ADMIN_USER"
  exit 1
fi

# The adversary, created before anything else needs it.
#
# A customer account holds no roles at all, which makes it the strongest
# attacker to test with: not "a user with fewer permissions" but one with none.
# It is also what the session tests in section 2 run against, for a reason
# worth stating — see there.
created=$(api POST /api/v1/tenancy/accounts \
  "{\"username\":\"$LOW_USER\",\"password\":\"$LOW_PASS\",\"tier\":\"customer\"}")
low_id=$(json_field "$created" id)
if [ -z "$low_id" ]; then
  log "FATAL: the low-privilege account could not be created:"
  log "  $(printf '%s' "$created" | head -c 200)"
  exit 1
fi

# ------------------------------------------------ 1. unauthenticated surface

log "1. What an unauthenticated caller can reach"

# The whole unauthenticated surface, enumerated from the source rather than
# from memory. Anything here that is not on this list is a route somebody
# forgot to guard, and section 3 is what catches that.
# find rather than `grep --include`, because this runs on BusyBox and BusyBox's
# grep has neither --include nor --exclude. It does not fail when given them —
# it finds nothing, silently, and an empty list then satisfies every
# expectation below. That is how the first version of this section passed while
# reading no source at all, which is the precise failure the controls in this
# suite exist to catch. The route sweep in section 3 had it too.
#
# _test.go is excluded because the dns and server packages register routes
# inside their own test fixtures, and a fixture is not a surface.
go_sources() {
  find "$SRC/api/internal" -name '*.go' ! -name '*_test.go' 2>/dev/null
}

open_routes=$(go_sources | xargs grep -ho 'mux\.HandleFunc("[A-Z]* /[^"]*"' 2>/dev/null |
  sed 's/.*("//; s/"$//' | sort -u)

# The count is asserted before anything is compared against it: a sweep over
# zero routes proves nothing and must say so rather than report green.
open_found=$(printf '%s\n' "$open_routes" | grep -c . || echo 0)
control "the source was read ($open_found unauthenticated routes found)" \
  "$([ "$open_found" -ge 4 ] && echo enough || echo "$open_found")" "enough"
expected_open='POST /api/v1/auth/login
POST /api/v1/auth/refresh
POST /api/v1/auth/2fa/verify
POST /api/v1/webhooks/deploy/{token}
GET /healthz
GET /readyz
GET /api/v1/health
GET /api/v1/version'

unexpected=""
for route in $(printf '%s' "$open_routes" | tr ' ' '~'); do
  entry=$(printf '%s' "$route" | tr '~' ' ')
  case "$entry" in */) continue ;; esac
  if ! printf '%s\n' "$expected_open" | grep -qxF "$entry"; then
    unexpected="$unexpected$entry
"
  fi
done
if [ -z "$unexpected" ]; then
  pass "the unauthenticated surface is only the eight routes it is meant to be"
else
  fail "routes registered without authentication that are not on the expected list:
$unexpected"
fi

# Each of the health endpoints answers without a token, by design: a load
# balancer has none. What matters is what they say.
version=$(curl -s --max-time 10 "$API_BASE_URL/api/v1/version" 2>/dev/null || true)
control "the version endpoint answers unauthenticated" \
  "$(code_for GET /api/v1/version)" "200"
lacks "it does not disclose the host's name" "$version" '"hostname"'

ready=$(curl -s --max-time 10 "$API_BASE_URL/readyz" 2>/dev/null || true)
lacks "readiness does not disclose a connection string" "$ready" "postgres://"
lacks "readiness does not disclose a password" "$ready" "password"

# A failed login must not say which half was wrong. "No such user" is a user
# enumeration oracle: it turns a password guess into a list of real accounts.
unknown=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"definitely-not-a-real-account","password":"x"}' 2>/dev/null)
known=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"definitely-not-the-password\"}" 2>/dev/null)
unknown_msg=$(json_field "$unknown" message)
known_msg=$(json_field "$known" message)
if [ -n "$unknown_msg" ] && [ "$unknown_msg" = "$known_msg" ]; then
  pass "an unknown username and a wrong password answer identically"
else
  fail "login discloses which half was wrong ('$unknown_msg' vs '$known_msg')"
fi

# The 404 for an unknown path must not differ from the 404 for a path that
# exists but is not readable, or the difference maps the API.
lacks "an unknown path does not name what it looked for" \
  "$(curl -s --max-time 10 "$API_BASE_URL/api/v1/does-not-exist" 2>/dev/null)" \
  "does-not-exist"

# ------------------------------------------------------- 2. authentication

log ""
log "2. Authentication"

control "a valid token is accepted" "$(code_for GET /api/v1/websites "$token")" "200"

refused "no token at all"          "$(code_for GET /api/v1/websites)"
refused "a token that is nonsense" "$(code_for GET /api/v1/websites 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')"
refused "an empty bearer"          "$(code_for GET /api/v1/websites ' ')"

# Only the Authorization header, and only Bearer. A token accepted from a query
# string is a token in the access log, in the referrer of every outbound link,
# and in browser history.
refused "a token in the query string" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
     "$API_BASE_URL/api/v1/websites?access_token=$token" 2>/dev/null)"
refused "a token in a cookie" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
     -H "Cookie: access_token=$token" "$API_BASE_URL/api/v1/websites" 2>/dev/null)"
refused "the Basic scheme" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
     -H "Authorization: Basic $token" "$API_BASE_URL/api/v1/websites" 2>/dev/null)"

# Revocation has to be immediate. Access tokens are opaque and server-side
# precisely so that logout is not "wait fifteen minutes".
# From here to the end of the section the tests run against the throwaway
# account rather than the one this suite is signing in with, and that is not
# tidiness.
#
# Replaying a refresh token revokes **every session belonging to that
# account**, not merely the request: the panel cannot tell which of the two
# holders is the thief, so it stops trusting all of them. Run against the
# administrator, that ends this suite's own session halfway through — which
# is exactly what happened the first time it was written, and is why every
# section below it reported a wall of green that proved nothing.
pair=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$LOW_USER\",\"password\":\"$LOW_PASS\"}" 2>/dev/null)
throwaway=$(json_field "$pair" access_token)
throwaway_refresh=$(json_field "$pair" refresh_token)
control "the throwaway session works" "$(code_for GET /api/v1/auth/me "$throwaway")" "200"
curl -s -o /dev/null --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/logout" \
  -H "Authorization: Bearer $throwaway" 2>/dev/null || true
refused "a token stops working the instant its session is ended" \
  "$(code_for GET /api/v1/auth/me "$throwaway")"
refused "and its refresh token goes with it" \
  "$(code_for POST /api/v1/auth/refresh '' "{\"refresh_token\":\"$throwaway_refresh\"}")"

# Refresh rotation, and what happens when an old token is replayed. A replay
# means two parties hold the token: the panel cannot tell which is the thief,
# so it revokes the session rather than guessing.
pair=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$LOW_USER\",\"password\":\"$LOW_PASS\"}" 2>/dev/null)
r1=$(json_field "$pair" refresh_token)
rotated=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/refresh" \
  -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$r1\"}" 2>/dev/null)
r2=$(json_field "$rotated" refresh_token)
a2=$(json_field "$rotated" access_token)
if [ -n "$r2" ] && [ "$r1" != "$r2" ]; then
  pass "a refresh rotates the token rather than reusing it"
else
  fail "the refresh token was not rotated"
fi
control "the rotated session works" "$(code_for GET /api/v1/auth/me "$a2")" "200"
refused "replaying the retired refresh token is refused" \
  "$(code_for POST /api/v1/auth/refresh '' "{\"refresh_token\":\"$r1\"}")"
refused "and the whole session is torn down, not just the request" \
  "$(code_for GET /api/v1/auth/me "$a2")"

# -------------------------------------------------------------- 3. RBAC

log ""
log "3. Every route, against an account with no permissions"

# A fresh session. The replay test above deliberately tore down every session
# this account had, which is the property it was proving.
low_token="$(login "$LOW_USER" "$LOW_PASS")"

if [ -z "$low_token" ]; then
  fail "the low-privilege account could not sign in, so nothing below was tested"
else
  control "the low-privilege account is authenticated" \
    "$(code_for GET /api/v1/auth/me "$low_token")" "200"
  profile=$(curl -s --max-time 20 "$API_BASE_URL/api/v1/auth/me" \
    -H "Authorization: Bearer $low_token" 2>/dev/null)
  contains "and holds no permissions at all" "$profile" '"permissions":[]'

  # The routes are read out of the source, so this cannot drift. A route added
  # next month without a guard fails here rather than waiting to be noticed.
  routes=$(go_sources | xargs grep -ho 'mux\.Handle\(Func\)\?("[A-Z]* /api/v1/[^"]*"' 2>/dev/null |
    sed 's/.*("//; s/"$//' | sort -u)
  route_count=$(printf '%s\n' "$routes" | grep -c . || echo 0)

  # The sweep's own control. This panel has upwards of two hundred routes; a
  # run that found a handful has read the wrong tree, and every refusal it then
  # reports is a refusal it never asked for.
  control "the route table was read from the source ($route_count routes)" \
    "$([ "$route_count" -ge 100 ] && echo enough || echo "$route_count")" "enough"

  : > /tmp/phase24-open.txt

  printf '%s\n' "$routes" | while read -r method path; do
    [ -n "$method" ] || continue
    case "$path" in
      */auth/*|*/health|*/version|*/webhooks/*) continue ;;
      # Deliberately unguarded: a session stripped of tenant.impersonate must
      # still be able to stop being one, and reading its own status discloses
      # nothing about anybody else. Asserted separately below.
      */tenancy/impersonation) continue ;;
    esac
    url=$(printf '%s' "$path" | sed 's|{[a-zA-Z]*}|00000000-0000-0000-0000-000000000000|g')
    code=$(code_for "$method" "$url" "$low_token" '{}')
    case "$code" in
      401|403) : ;;
      *) printf '%s %s -> %s\n' "$method" "$path" "$code" >> /tmp/phase24-open.txt ;;
    esac
  done

  open_count=$(wc -l < /tmp/phase24-open.txt 2>/dev/null | tr -d ' ')
  if [ "${open_count:-0}" = "0" ]; then
    pass "all $route_count registered routes refuse an account with no permissions"
  else
    fail "$open_count route(s) did not refuse an account with no permissions:
$(cat /tmp/phase24-open.txt)"
  fi

  # The two that are unguarded on purpose, checked for what they actually do
  # rather than waved past.
  own=$(curl -s --max-time 20 "$API_BASE_URL/api/v1/tenancy/impersonation" \
    -H "Authorization: Bearer $low_token" 2>/dev/null)
  contains "reading impersonation status from an ordinary session says 'none'" \
    "$own" '"impersonation":null'
  lacks "and discloses nothing about anybody else" "$own" '"actor_username"'
  refused "ending an impersonation that is not one is refused" \
    "$(code_for DELETE /api/v1/tenancy/impersonation "$low_token")"

  # The same feature reached by two different verbs, both registered: a guard
  # written per-path rather than per-route is walked around exactly here.
  denied "reading the firewall is refused" \
    "$(code_for GET /api/v1/firewall "$low_token")"
  denied "and so is changing it" \
    "$(code_for POST /api/v1/firewall/rules "$low_token" \
       '{"action":"allow","protocol":"tcp","port":"22"}')"
fi

# ---------------------------------------------------- 4. path traversal

log ""
log "4. Paths that try to leave their root"

site=$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"audit\"}")
website_id=$(json_field "$site" id)
waited=0
while [ "$waited" -lt 120 ]; do
  state=$(json_field "$(api GET "/api/v1/websites/$website_id?remove_files=true")" status)
  case "$state" in active|failed) break ;; esac
  sleep 3; waited=$((waited + 3))
done

probe_root="/var/www/$SITE_DOMAIN/public"
probe_file="$probe_root/audit-control.txt"

# The creation endpoint takes the directory and the name separately; the write
# endpoint takes a whole path. Sending one shape to the other is how the first
# draft of this section produced a control that never created anything — and
# then eleven refusals that were only the endpoint saying "there is no such
# file".
api POST /api/v1/files/file \
  "{\"path\":\"$probe_root\",\"name\":\"audit-control.txt\"}" >/dev/null
api PUT /api/v1/files/content \
  "{\"path\":\"$probe_file\",\"content\":\"control\"}" >/dev/null

# The control. Without it, every refusal below could be the endpoint saying
# "you sent me nothing" — which is exactly what happened while this was being
# written, and it made a whole section of green mean nothing.
control "a legitimate file inside the site can be read" \
  "$(code_for GET "/api/v1/files/content?path=$probe_file" "$token")" "200"

for target in \
  "/etc/passwd" \
  "/etc/shadow" \
  "/etc/jothost/api.env" \
  "/root/.ssh/id_rsa" \
  "/proc/self/environ" \
  "/proc/1/cmdline" \
  "$probe_root/../../../etc/passwd" \
  "/var/www/../../etc/passwd" \
  "/var/www/./../../etc/passwd" \
  "//etc/passwd" \
  "/var/www/$SITE_DOMAIN/public/../../../../etc/passwd"
do
  refused "reading $target is refused" \
    "$(code_for GET "/api/v1/files/content?path=$target" "$token")"
done

# Symlink escape is not probed here. It needs a symlink inside the site's own
# directory, and this container has no /var/www — a probe that could not create
# the link would report "refused" for a file that was never there, which is the
# exact failure this suite's controls exist to catch.
# tests/integration/phase24_agent.sh does it from inside the managed host,
# where the link is real.

# Writing is the half that turns a read of /etc/passwd into an account.
refused "writing outside the root is refused" \
  "$(code_for PUT /api/v1/files/content "$token" \
     '{"path":"/etc/cron.d/pwned","content":"* * * * * root id"}')"
refused "creating a folder outside the root is refused" \
  "$(code_for POST /api/v1/files/folder "$token" '{"path":"/etc/pwned"}')"
refused "moving a file out of the root is refused" \
  "$(code_for POST /api/v1/files/move "$token" \
     "{\"source\":\"$probe_file\",\"destination\":\"/etc/pwned.txt\"}")"
refused "unzipping into a path outside the root is refused" \
  "$(code_for POST /api/v1/files/unzip "$token" \
     "{\"path\":\"$probe_file\",\"destination\":\"/etc\"}")"

# The log viewer reads files chosen by key, not by path, so the attack is a key
# that is really a path.
refused "the log viewer refuses a key that is a path" \
  "$(code_for GET "/api/v1/logs/..%2f..%2f..%2fetc%2fpasswd" "$token")"
refused "and a group and name that are one" \
  "$(code_for GET "/api/v1/logs/..%2f..%2fetc/passwd" "$token")"

# ------------------------------------------------- 5. command injection

log ""
log "5. Values that would close a command, a directive, or a statement"

# Every one of these reaches something that executes: a shell argv, an nginx
# configuration read by root, a systemd unit, or SQL. The panel's rule is that
# they are refused by *shape* rather than by searching for characters somebody
# thought of, and this is where that is checked from outside.
control "an ordinary domain is accepted" \
  "$(code_for POST /api/v1/websites "$token" "{\"domain\":\"ctl$STAMP.test\",\"name\":\"c\"}")" "201"
ctl_id=$(json_field "$(api GET /api/v1/websites)" id)

for hostile in \
  'evil.test;rm -rf /' \
  'evil.test$(id)' \
  'evil.test`id`' \
  'evil.test\nserver{listen 80;}' \
  '../../../etc/passwd' \
  '-evil.test' \
  'evil.test|id' \
  'evil test' \
  'evil.test%00.png'
do
  refused "a domain of the form '$hostile' is refused" \
    "$(code_for POST /api/v1/websites "$token" \
       "$(printf '{"domain":"%s","name":"x"}' "$hostile")")"
done

for hostile in 'db;DROP TABLE users' 'db`id`' 'db$(id)' '../db' 'db--' "db'--"; do
  refused "a database named '$hostile' is refused" \
    "$(code_for POST /api/v1/databases "$token" \
       "$(printf '{"name":"%s","engine":"mariadb"}' "$hostile")")"
done

# git's remote is a small language and three of its dialects run programs.
for hostile in \
  'ext::sh -c whoami' \
  '--upload-pack=/tmp/evil' \
  'file:///etc' \
  '/etc/passwd' \
  'https://host/repo.git;id'
do
  refused "a repository remote of '$hostile' is refused" \
    "$(code_for POST /api/v1/deployments/repositories "$token" \
       "$(printf '{"website_id":"%s","remote_url":"%s"}' "$website_id" "$hostile")")"
done

# A firewall rule reaches ufw's argv.
for hostile in '22; rm -rf /' '$(id)' '../../etc' '-1'; do
  refused "a firewall port of '$hostile' is refused" \
    "$(code_for POST /api/v1/firewall/rules "$token" \
       "$(printf '{"action":"allow","protocol":"tcp","port":"%s"}' "$hostile")")"
done

# A cron command is the one place the panel deliberately stores something a
# shell will run — bounded by the account it runs as, never by string filtering.
# What must not be possible is escaping the *schedule* into the command.
for hostile in '* * * * * ; id' 'not a schedule' '*/0 * * * *'; do
  refused "a cron schedule of '$hostile' is refused" \
    "$(code_for POST /api/v1/cron "$token" \
       "$(printf '{"website_id":"%s","name":"x","schedule":"%s","type":"command","command":"echo hi"}' \
          "$website_id" "$hostile")")"
done

# ------------------------------------------------ 6. privilege escalation

log ""
log "6. Getting more than was granted"

if [ -n "$low_token" ]; then
  # A customer must not be able to make itself anything.
  denied "a customer cannot create an account" \
    "$(code_for POST /api/v1/tenancy/accounts "$low_token" \
       "{\"username\":\"esc$STAMP\",\"password\":\"$LOW_PASS\",\"tier\":\"admin\"}")"
  denied "a customer cannot impersonate" \
    "$(code_for POST /api/v1/tenancy/impersonation "$low_token" \
       "{\"user_id\":\"00000000-0000-0000-0000-000000000000\"}")"
  denied "a customer cannot see the other accounts on the host" \
    "$(code_for GET /api/v1/tenancy/accounts "$low_token")"
  denied "a customer cannot read the host's logs" \
    "$(code_for GET /api/v1/logs "$low_token")"
  denied "a customer cannot read the jobs queue" \
    "$(code_for GET /api/v1/jobs "$low_token")"
  denied "nor cancel a job that is not theirs" \
    "$(code_for POST /api/v1/jobs/00000000-0000-0000-0000-000000000000/cancel "$low_token")"
fi

# An admin cannot create another admin: an account is created strictly below
# its parent, which is also what makes the hierarchy acyclic.
refused "even an admin cannot create a second admin through the tenancy API" \
  "$(code_for POST /api/v1/tenancy/accounts "$token" \
     "{\"username\":\"esc2$STAMP\",\"password\":\"$LOW_PASS\",\"tier\":\"admin\"}")"

# There is deliberately no way to submit a job at all.
#
# The queue is written by the panel's own services; the only verbs it exposes
# are reading one and cancelling one. A caller who cannot name an operation is
# a stronger property than one whose operation name is validated, and this is
# what checks the property still holds.
submit=$(code_for POST /api/v1/jobs "$token" '{"type":"agent.exec","payload":{"cmd":"id"}}')
case "$submit" in
  404|405) pass "the job queue accepts no submissions, so no operation can be named" ;;
  *) fail "the job queue accepted a submission (HTTP $submit)" ;;
esac

# ------------------------------------------------------- 7. rate limiting

log ""
log "7. The login throttle"

# The throttle is per account and per address. Guessing has to become expensive
# before the guesses run out, or a long password is only as good as the time it
# takes to try every short one.
throttled=0
attempt=0
while [ "$attempt" -lt 12 ]; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST \
    "$API_BASE_URL/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"$LOW_USER\",\"password\":\"wrong-$attempt\"}" 2>/dev/null || echo 000)
  if [ "$code" = "429" ]; then throttled=1; break; fi
  attempt=$((attempt + 1))
done
if [ "$throttled" = 1 ]; then
  pass "repeated failures are throttled after $attempt attempts"
else
  fail "twelve wrong passwords in a row were not throttled"
fi

# And the throttle must not be a way to lock somebody else out: a correct
# password from a throttled address is still refused, but the account is not
# disabled — it recovers on its own.
# 429 exactly, not merely "some refusal". A correct password answered with 401
# while throttled would be indistinguishable from a wrong one, and somebody
# locked out would spend the next hour retyping a password that was right.
code=$(code_for POST /api/v1/auth/login '' \
  "{\"username\":\"$LOW_USER\",\"password\":\"$LOW_PASS\"}")
if [ "$code" = "429" ]; then
  pass "a correct password is refused while the throttle holds, and says why"
else
  fail "a throttled correct password answered HTTP $code rather than 429"
fi

# -------------------------------------------------------------- 8. secrets

log ""
log "8. What the panel stores must not come back out"

# Nothing that authenticates anything may appear in a response. The API returns
# whole rows in several places, and a column added later is how a secret starts
# being served.
for path in /api/v1/websites /api/v1/databases /api/v1/backup-destinations \
            /api/v1/notifications /api/v1/deployments /api/v1/mail \
            /api/v1/tenancy/overview /api/v1/ftp/users; do
  body=$(api GET "$path")
  for secret in password_hash password_encrypted credentials_encrypted \
                webhook_secret_encrypted secret_encrypted private_key \
                encryption_key agent_token; do
    case "$body" in
      *"\"$secret\""*) fail "$path returns $secret" ;;
    esac
  done
done
pass "no listing returns a hash, an encrypted credential or a private key"

# The audit log is not read here, and that is a finding rather than an
# omission: the panel defines an audit.view permission and exposes no endpoint
# behind it, so the audit trail cannot be read through the API at all. What the
# Agent writes to its own audit file is checked in phase24_agent.sh, and the
# gap is recorded in docs/PHASE24.md.

# The two-factor secret is encrypted at rest and must not be readable through
# any listing either.
lacks "no listing returns a two-factor secret" \
  "$(api GET /api/v1/tenancy/accounts)" 'totp_secret'
lacks "nor does the profile endpoint" "$(api GET /api/v1/auth/me)" 'totp_secret'

log ""
if [ "$controls" -gt 0 ]; then
  log "$controls control(s) failed: some checks above proved nothing."
fi
if [ "$failures" -eq 0 ]; then
  log "All Phase 24 security checks passed."
  exit 0
fi
log "$failures Phase 24 security check(s) failed."
exit 1
