#!/bin/sh
# Phase 1 Docker integration test — authentication.
#
# Black-box checks against the running development stack. Verifies the login
# contract from API_SPEC.md section 2, that protected endpoints actually refuse
# anonymous callers, that refresh rotates and detects reuse, that logout takes
# effect immediately, and that credentials never appear in responses.
#
# Requires an administrator created by `make docker-test-integration`.
#
# Run with:  make docker-test-auth

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

# post PATH JSON [BEARER] — prints the response body.
post() {
  path="$1"; payload="$2"; bearer="${3:-}"
  if [ -n "$bearer" ]; then
    curl -s --max-time 10 -X POST "$API_BASE_URL$path" \
      -H 'Content-Type: application/json' \
      -H "Authorization: Bearer $bearer" \
      -d "$payload" 2>/dev/null || true
  else
    curl -s --max-time 10 -X POST "$API_BASE_URL$path" \
      -H 'Content-Type: application/json' \
      -d "$payload" 2>/dev/null || true
  fi
}

# post_status PATH JSON [BEARER] — prints the HTTP status code.
post_status() {
  path="$1"; payload="$2"; bearer="${3:-}"
  if [ -n "$bearer" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API_BASE_URL$path" \
      -H 'Content-Type: application/json' \
      -H "Authorization: Bearer $bearer" \
      -d "$payload" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 -X POST "$API_BASE_URL$path" \
      -H 'Content-Type: application/json' \
      -d "$payload" 2>/dev/null || true
  fi
}

# get_status PATH [BEARER]
get_status() {
  path="$1"; bearer="${2:-}"
  if [ -n "$bearer" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
      -H "Authorization: Bearer $bearer" "$API_BASE_URL$path" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 10 "$API_BASE_URL$path" 2>/dev/null || true
  fi
}

# get PATH [BEARER]
get() {
  path="$1"; bearer="${2:-}"
  if [ -n "$bearer" ]; then
    curl -s --max-time 10 -H "Authorization: Bearer $bearer" "$API_BASE_URL$path" 2>/dev/null || true
  else
    curl -s --max-time 10 "$API_BASE_URL$path" 2>/dev/null || true
  fi
}

# json_field BODY FIELD — extracts a string field without a JSON parser.
json_field() {
  printf '%s' "$1" | sed -n 's/.*"'"$2"'":"\([^"]*\)".*/\1/p'
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then
    pass "$name"
  else
    fail "$name (expected HTTP $want, got $got)"
  fi
}

log "Phase 1 authentication checks"
log "API:   $API_BASE_URL"
log "Admin: $ADMIN_USER"
log ""

# ------------------------------------------------------------------- login

log "Login"
credentials="{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}"

login_body="$(post /api/v1/auth/login "$credentials")"
access_token="$(json_field "$login_body" access_token)"
refresh_token="$(json_field "$login_body" refresh_token)"

if [ -n "$access_token" ] && [ -n "$refresh_token" ]; then
  pass "login returns an access and refresh token"
else
  fail "login did not return tokens: $login_body"
  log ""
  log "FAILED: cannot continue without a session"
  exit 1
fi

case "$login_body" in
  *'"token_type":"Bearer"'*) pass "token_type is Bearer" ;;
  *) fail "token_type missing from login response" ;;
esac
case "$login_body" in
  *'"expires_in":'*) pass "expires_in is present" ;;
  *) fail "expires_in missing from login response" ;;
esac

log ""
log "Login failures"
expect_status "wrong password returns 401" 401 \
  "$(post_status /api/v1/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"definitely-not-the-password\"}")"
expect_status "unknown user returns 401" 401 \
  "$(post_status /api/v1/auth/login '{"username":"nosuchuser","password":"definitely-not-the-password"}')"
expect_status "missing fields return 422" 422 \
  "$(post_status /api/v1/auth/login '{"username":"","password":""}')"
expect_status "malformed JSON returns 400" 400 \
  "$(post_status /api/v1/auth/login 'not json')"
expect_status "unknown fields are rejected" 400 \
  "$(post_status /api/v1/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"x\",\"role\":\"admin\"}")"

# The two failure bodies must be indistinguishable, or the endpoint enumerates
# valid usernames.
wrong_pw_body="$(post /api/v1/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"definitely-not-the-password\"}")"
unknown_body="$(post /api/v1/auth/login '{"username":"nosuchuser","password":"definitely-not-the-password"}')"
wrong_pw_msg="$(json_field "$wrong_pw_body" message)"
unknown_msg="$(json_field "$unknown_body" message)"
if [ "$wrong_pw_msg" = "$unknown_msg" ] && [ -n "$wrong_pw_msg" ]; then
  pass "a wrong password and an unknown user are indistinguishable"
else
  fail "login errors differ: '$wrong_pw_msg' vs '$unknown_msg'"
fi

# --------------------------------------------------------------- protected

log ""
log "Protected endpoints"
expect_status "GET /auth/me without a token returns 401" 401 "$(get_status /api/v1/auth/me)"
expect_status "GET /auth/me with a garbage token returns 401" 401 "$(get_status /api/v1/auth/me not-a-real-token)"
expect_status "GET /auth/me with a valid token returns 200" 200 "$(get_status /api/v1/auth/me "$access_token")"
expect_status "POST /auth/logout without a token returns 401" 401 "$(post_status /api/v1/auth/logout '{}')"
expect_status "POST /auth/2fa/setup without a token returns 401" 401 "$(post_status /api/v1/auth/2fa/setup '{}')"

# A token in the query string must not authenticate.
expect_status "a query-string token does not authenticate" 401 \
  "$(get_status "/api/v1/auth/me?access_token=$access_token")"

log ""
log "Profile contents"
me_body="$(get /api/v1/auth/me "$access_token")"
case "$me_body" in
  *"\"username\":\"$ADMIN_USER\""*) pass "profile reports the signed-in user" ;;
  *) fail "profile did not return the expected username: $me_body" ;;
esac
case "$me_body" in
  *'"permissions":['*) pass "profile carries permissions" ;;
  *) fail "profile is missing permissions" ;;
esac
case "$me_body" in
  *'"roles":['*'"admin"'*) pass "profile carries the admin role" ;;
  *) fail "profile is missing the admin role" ;;
esac
# Credential material must never appear in a profile response.
case "$me_body" in
  *password_hash*|*"$ADMIN_PASS"*|*secret*) fail "profile leaks credential material" ;;
  *) pass "profile contains no credential material" ;;
esac

# ----------------------------------------------------------------- refresh

log ""
log "Refresh rotation"
refresh_body="$(post /api/v1/auth/refresh "{\"refresh_token\":\"$refresh_token\"}")"
new_access="$(json_field "$refresh_body" access_token)"
new_refresh="$(json_field "$refresh_body" refresh_token)"

if [ -n "$new_access" ] && [ -n "$new_refresh" ]; then
  pass "refresh returns a new token pair"
else
  fail "refresh did not return tokens: $refresh_body"
fi
if [ "$new_refresh" != "$refresh_token" ]; then
  pass "the refresh token is rotated"
else
  fail "the refresh token was reused rather than rotated"
fi
expect_status "the new access token works" 200 "$(get_status /api/v1/auth/me "$new_access")"
expect_status "the previous access token is revoked" 401 "$(get_status /api/v1/auth/me "$access_token")"

log ""
log "Refresh reuse detection"
# Replaying the original refresh token models a stolen token being used after
# the legitimate client already rotated.
expect_status "a replayed refresh token is rejected" 401 \
  "$(post_status /api/v1/auth/refresh "{\"refresh_token\":\"$refresh_token\"}")"
# The whole session family must die, including the token the honest client holds.
expect_status "reuse revokes the current refresh token too" 401 \
  "$(post_status /api/v1/auth/refresh "{\"refresh_token\":\"$new_refresh\"}")"
expect_status "reuse revokes the live access token" 401 "$(get_status /api/v1/auth/me "$new_access")"

expect_status "an unknown refresh token is rejected" 401 \
  "$(post_status /api/v1/auth/refresh '{"refresh_token":"never-issued"}')"
expect_status "a missing refresh token is a validation error" 422 \
  "$(post_status /api/v1/auth/refresh '{}')"

# ------------------------------------------------------------------ logout

log ""
log "Logout"
login_body="$(post /api/v1/auth/login "$credentials")"
access_token="$(json_field "$login_body" access_token)"
refresh_token="$(json_field "$login_body" refresh_token)"

expect_status "the fresh session works" 200 "$(get_status /api/v1/auth/me "$access_token")"
expect_status "logout succeeds" 200 "$(post_status /api/v1/auth/logout '{}' "$access_token")"
# Revocation must be immediate, not deferred until the token expires.
expect_status "the access token is revoked immediately" 401 "$(get_status /api/v1/auth/me "$access_token")"
expect_status "the refresh token is revoked" 401 \
  "$(post_status /api/v1/auth/refresh "{\"refresh_token\":\"$refresh_token\"}")"

# ------------------------------------------------------------- rate limits

log ""
log "Login rate limiting"
# A throwaway username keeps the admin account out of the throttle.
probe_user="ratelimit_probe_$$"
rate_limited=0
i=0
while [ $i -lt 12 ]; do
  code="$(post_status /api/v1/auth/login "{\"username\":\"$probe_user\",\"password\":\"definitely-not-the-password\"}")"
  if [ "$code" = "429" ]; then
    rate_limited=1
    break
  fi
  i=$((i + 1))
done

if [ "$rate_limited" -eq 1 ]; then
  pass "repeated failures are throttled with 429"
else
  fail "login was never rate limited after 12 attempts"
fi

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 1 authentication checks passed"
