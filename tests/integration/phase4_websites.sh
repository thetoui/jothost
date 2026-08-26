#!/bin/sh
# Phase 4 Docker integration test — Website Manager.
#
# Black-box checks against the running stack. The acceptance criterion from
# TASKS.md is the end of this file: create a website through the API, request
# it over HTTP, and receive a response. Everything before it establishes that
# the site was created the way it was asked for, and everything after it that
# deleting one actually removes it from the host.
#
# Run with:  make docker-test-websites

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
# The Agent container runs the nginx that serves the sites the panel creates.
# This is deliberately a different host from the panel's own reverse proxy.
SITES_BASE_URL="${SITES_BASE_URL:-http://agent:80}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# A name no other test uses, so a leftover site cannot make this pass.
SITE_DOMAIN="${SITE_DOMAIN:-phase4.integration.test}"
ALIAS_DOMAIN="www.$SITE_DOMAIN"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

# api METHOD PATH [BODY] — prints the response body.
api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 20 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 20 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

# api_status METHOD PATH [BODY] [BEARER] — prints the HTTP status code.
api_status() {
  method="$1"; path="$2"; body="${3:-}"; bearer="${4-$token}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" \
      -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" 2>/dev/null || true
  fi
}

# site_status HOST [PATH] — requests a hosted site by name.
site_status() {
  host="$1"; path="${2:-/}"
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
}

# site_body HOST [PATH]
site_body() {
  host="$1"; path="${2:-/}"
  curl -s --max-time 20 -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then
    pass "$name"
  else
    fail "$name (expected HTTP $want, got $got)"
  fi
}

# contains NAME HAYSTACK NEEDLE
contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle')" ;;
  esac
}

# json_field BODY KEY — reads a top-level string value.
json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

# await_job JOB_ID — blocks until a job reaches a terminal state, printing it.
await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 60 ]; do
    body="$(api GET "/api/v1/jobs/$job_id")"
    state="$(json_field "$body" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED)
        printf '%s' "$state"
        return 0
        ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

log "Phase 4 website checks"
log "API:   $API_BASE_URL"
log "Sites: $SITES_BASE_URL"
log ""

# ------------------------------------------------------------------- login

token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
  sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"

if [ -z "$token" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# --------------------------------------------------------------- clean slate

# A site left behind by an earlier run would make the create below fail on a
# duplicate rather than testing anything.
existing="$(api GET "/api/v1/websites")"
stale_id="$(printf '%s' "$existing" |
  tr '{' '\n' | grep -F "\"primary_domain\":\"$SITE_DOMAIN\"" |
  sed -n 's/.*"id":"\([0-9a-f-]*\)".*/\1/p' | head -n 1)"
if [ -n "$stale_id" ]; then
  log "Removing a website left behind by an earlier run"
  stale_job="$(json_field "$(api DELETE "/api/v1/websites/$stale_id")" id)"
  [ -n "$stale_job" ] && await_job "$stale_job" >/dev/null
fi

# ------------------------------------------------------------ authorization

log "Authorization"
# Creating a website provisions a system account and rewrites the web server's
# configuration. None of it is reachable without a session.
expect_status "GET /websites without a token returns 401"  401 \
  "$(api_status GET /api/v1/websites '' '')"
expect_status "POST /websites without a token returns 401" 401 \
  "$(api_status POST /api/v1/websites '{"domain":"nope.test"}' '')"
expect_status "GET /jobs without a token returns 401"      401 \
  "$(api_status GET /api/v1/jobs '' '')"
expect_status "a garbage token is refused"                 401 \
  "$(api_status GET /api/v1/websites '' not-a-real-token)"
expect_status "GET /websites with a valid token returns 200" 200 \
  "$(api_status GET /api/v1/websites)"

# --------------------------------------------------------------- validation

log ""
log "Input validation"
# The domain reaches a vhost's server_name and a filesystem path, so a name
# that is not a hostname must never get that far.
expect_status "a single label is refused"        422 \
  "$(api_status POST /api/v1/websites '{"domain":"localhost"}')"
expect_status "a shell payload is refused"       422 \
  "$(api_status POST /api/v1/websites '{"domain":"example.test;rm -rf /"}')"
expect_status "a path traversal is refused"      422 \
  "$(api_status POST /api/v1/websites '{"domain":"../../etc/passwd"}')"
expect_status "an empty domain is refused"       422 \
  "$(api_status POST /api/v1/websites '{"domain":""}')"
expect_status "a misspelled field is reported"   400 \
  "$(api_status POST /api/v1/websites '{"domian":"example.test"}')"
# SSL is Phase 6. Accepting the flag and ignoring it would leave someone
# believing their site is encrypted when it is served over plain HTTP.
expect_status "ssl_enabled is refused, not ignored" 400 \
  "$(api_status POST /api/v1/websites "{\"domain\":\"ssl.$SITE_DOMAIN\",\"ssl_enabled\":true}")"
expect_status "a malformed id is a bad request"  400 \
  "$(api_status GET /api/v1/websites/not-a-uuid)"
expect_status "an unknown id is not found"       404 \
  "$(api_status GET /api/v1/websites/00000000-0000-0000-0000-000000000000)"

# ------------------------------------------------------------------ create

log ""
log "Website creation"

created="$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"Phase 4\"}")"
contains "the response carries the website"  "$created" '"website"'
contains "the response carries its job"      "$created" '"job"'
contains "the site starts in creating"       "$created" '"status":"creating"'
contains "the document root is under /var/www" "$created" "\"document_root\":\"/var/www/$SITE_DOMAIN/public\""
contains "the site is given a system user"   "$created" '"system_user":"web_'

website_id="$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')"
job_id="$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$website_id" ] || [ -z "$job_id" ]; then
  log "FAILED: could not read the website or job id from: $(printf '%s' "$created" | head -c 300)"
  exit 1
fi

# The site must not claim to work before the host has done anything.
expect_status "a duplicate domain is a conflict" 409 \
  "$(api_status POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\"}")"

state="$(await_job "$job_id")"
if [ "$state" = "SUCCESS" ]; then
  pass "the provisioning job succeeded"
else
  detail="$(api GET "/api/v1/jobs/$job_id")"
  fail "the provisioning job ended $state: $(printf '%s' "$detail" | head -c 300)"
fi

site="$(api GET "/api/v1/websites/$website_id")"
contains "the site is now active"          "$site" '"status":"active"'
contains "the site lists its primary domain" "$site" "\"domain\":\"$SITE_DOMAIN\""

# ------------------------------------------------- the acceptance criterion

log ""
log "Serving the site (TASKS.md Phase 4 acceptance)"

expect_status "the website answers over HTTP" 200 "$(site_status "$SITE_DOMAIN")"
contains "the response is this site's page" "$(site_body "$SITE_DOMAIN")" "$SITE_DOMAIN"

# A hostname no site claims must not be served by whichever vhost loaded first.
expect_status "an unclaimed hostname is not served" 404 "$(site_status "unclaimed.invalid")"

# Application configuration lives in dotfiles. Serving them would hand out
# database passwords and API keys to anyone who guessed the name.
expect_status "a dotfile is denied" 403 "$(site_status "$SITE_DOMAIN" "/.env")"

# ------------------------------------------------------------------ domains

log ""
log "Domains"

alias_response="$(api POST "/api/v1/websites/$website_id/domains" \
  "{\"domain\":\"$ALIAS_DOMAIN\",\"type\":\"alias\"}")"
contains "an alias is accepted" "$alias_response" "\"domain\":\"$ALIAS_DOMAIN\""

alias_job="$(printf '%s' "$alias_response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
if [ -n "$alias_job" ]; then
  state="$(await_job "$alias_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "the vhost update succeeded"
  else
    fail "the vhost update ended $state"
  fi
fi

# The alias is only real if the web server answers to it.
expect_status "the alias is served" 200 "$(site_status "$ALIAS_DOMAIN")"

# Redirect domains are refused until the Agent can remove a stale redirect
# vhost; accepting one would create configuration the panel could not undo.
expect_status "a redirect domain is refused for now" 400   "$(api_status POST "/api/v1/websites/$website_id/domains"      "{\"domain\":\"go.$SITE_DOMAIN\",\"type\":\"redirect\",\"redirect_to\":\"$SITE_DOMAIN\"}")"

# The primary domain is the site's identity and its vhost's server_name.
primary_domain_id="$(printf '%s' "$site" |
  tr '{' '\n' | grep -F '"type":"primary"' |
  sed -n 's/.*"id":"\([0-9a-f-]*\)".*/\1/p' | head -n 1)"
if [ -n "$primary_domain_id" ]; then
  expect_status "the primary domain cannot be detached" 400 \
    "$(api_status DELETE "/api/v1/domains/$primary_domain_id")"
else
  fail "could not read the primary domain id"
fi

# ---------------------------------------------------------------- the jobs

log ""
log "Job records"
jobs="$(api GET "/api/v1/jobs?resource_type=website&resource_id=$website_id")"
contains "the site's work is recorded"     "$jobs" '"type":"website.create"'
contains "finished jobs report a result"   "$jobs" '"status":"SUCCESS"'
expect_status "an unknown job is not found" 404 \
  "$(api_status GET /api/v1/jobs/00000000-0000-0000-0000-000000000000)"
expect_status "a bad status filter is refused" 400 \
  "$(api_status GET '/api/v1/jobs?status=BOGUS')"
# A finished job cannot be cancelled: the host has already changed.
expect_status "a finished job cannot be cancelled" 409 \
  "$(api_status POST "/api/v1/jobs/$job_id/cancel")"

# ------------------------------------------------------------------ delete

log ""
log "Website deletion"

delete_response="$(api DELETE "/api/v1/websites/$website_id")"
delete_job="$(printf '%s' "$delete_response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$delete_job" ]; then
  fail "delete returned no job: $(printf '%s' "$delete_response" | head -c 200)"
else
  # The row survives until the host confirms; removing it first would strand
  # the files with nothing in the panel pointing at them.
  expect_status "the site is still readable while deleting" 200 \
    "$(api_status GET "/api/v1/websites/$website_id")"

  state="$(await_job "$delete_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "the delete job succeeded"
  else
    detail="$(api GET "/api/v1/jobs/$delete_job")"
    fail "the delete job ended $state: $(printf '%s' "$detail" | head -c 300)"
  fi

  expect_status "the website record is gone" 404 \
    "$(api_status GET "/api/v1/websites/$website_id")"
  # The vhost must go with it, or the host keeps serving a site the panel no
  # longer knows about.
  expect_status "the host no longer serves the domain" 404 "$(site_status "$SITE_DOMAIN")"
fi

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 4 website checks passed"
