#!/bin/sh
# Phase 6 Docker integration test — SSL.
#
# Black-box checks against the running stack. The acceptance is that a website
# issued a certificate actually completes a TLS handshake, that plain HTTP is
# redirected to it, and that the ACME challenge path survives that redirect —
# the last of which is what keeps renewal working sixty days later.
#
# It runs inside the agent container, which is the managed host in development,
# so it can inspect private key permissions on disk.
#
# Run with:  make docker-test-ssl

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
SITES_BASE_URL="${SITES_BASE_URL:-http://agent:80}"
SITES_TLS_URL="${SITES_TLS_URL:-https://agent:443}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

SITE_DOMAIN="${SITE_DOMAIN:-phase6.integration.test}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

# refresh_token signs in again if the token has aged out. Issuing certificates
# takes long enough that one issued at the start can expire before the end.
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
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"; bearer="${4-$token}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" 2>/dev/null || true
  fi
}

# http_status HOST [PATH] — a plain HTTP request, never following redirects.
http_status() {
  host="$1"; path="${2:-/}"
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
}

http_body() {
  host="$1"; path="${2:-/}"
  curl -s --max-time 20 -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
}

# https_status HOST [PATH] — insecure because a self-signed certificate is
# expected; the handshake itself is what is being checked.
https_status() {
  host="$1"; path="${2:-/}"
  curl -sk -o /dev/null -w '%{http_code}' --max-time 20 \
    --resolve "$host:443:127.0.0.1" "https://$host$path" 2>/dev/null || true
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 160))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

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

log "Phase 6 SSL checks"
log "API:   $API_BASE_URL"
log "Sites: $SITES_BASE_URL"
log ""

login
if [ -z "$token" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# --------------------------------------------------------------- clean slate

stale="$(api GET /api/v1/websites | tr '{' '\n' |
  grep -F "\"primary_domain\":\"$SITE_DOMAIN\"" |
  sed -n 's/.*"id":"\([0-9a-f-]*\)".*/\1/p' | head -n 1)"
if [ -n "$stale" ]; then
  job="$(json_field "$(api DELETE "/api/v1/websites/$stale?remove_files=true")" id)"
  [ -n "$job" ] && await_job "$job" >/dev/null
fi

# ------------------------------------------------------------ authorization

log "Authorization"
expect_status "GET /ssl without a token returns 401" 401 \
  "$(api_status GET /api/v1/ssl '' '')"
expect_status "GET /ssl/providers without a token returns 401" 401 \
  "$(api_status GET /api/v1/ssl/providers '' '')"
expect_status "GET /ssl with a valid token returns 200" 200 \
  "$(api_status GET /api/v1/ssl)"

# --------------------------------------------------------------- providers

log ""
log "Providers"
providers="$(api GET /api/v1/ssl/providers)"
# Self-signed needs nothing but the Agent itself, so it is always available.
contains "self-signed is always available" "$providers" '"selfsigned":true'
contains "the provider list names Let'\''s Encrypt" "$providers" '"letsencrypt"'

# --------------------------------------------------------------- validation

log ""
log "Input validation"
expect_status "a malformed website id is refused" 400 \
  "$(api_status GET /api/v1/websites/not-a-uuid/ssl)"
expect_status "an unknown website is not found" 404 \
  "$(api_status POST /api/v1/websites/00000000-0000-0000-0000-000000000000/ssl/issue '{}')"

# ------------------------------------------------------------------ issuing

log ""
log "Issuing a certificate"
refresh_token

created="$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\"}")"
website_id="$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')"
create_job="$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$website_id" ]; then
  log "FAILED: could not create $SITE_DOMAIN"
  exit 1
fi

state="$(await_job "$create_job")"
if [ "$state" != "SUCCESS" ]; then
  log "FAILED: provisioning $SITE_DOMAIN ended $state"
  exit 1
fi

# A new site has no certificate, which is a valid configuration rather than an
# error.
ssl_state="$(api GET "/api/v1/websites/$website_id/ssl")"
contains "a new website has no certificate" "$ssl_state" '"enabled":false'
expect_status "it is served over plain HTTP" 200 "$(http_status "$SITE_DOMAIN")"

expect_status "an unknown provider is refused" 422 \
  "$(api_status POST "/api/v1/websites/$website_id/ssl/issue" '{"provider":"acme-corp"}')"
expect_status "a misspelled field is reported" 400 \
  "$(api_status POST "/api/v1/websites/$website_id/ssl/issue" '{"provdier":"selfsigned"}')"

issued="$(api POST "/api/v1/websites/$website_id/ssl/issue" \
  '{"provider":"selfsigned","https_redirect":true}')"
issue_job="$(printf '%s' "$issued" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$issue_job" ]; then
  fail "issuing returned no job: $(printf '%s' "$issued" | head -c 200)"
else
  state="$(await_job "$issue_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "the certificate was issued"
  else
    detail="$(api GET "/api/v1/jobs/$issue_job")"
    fail "issuing ended $state: $(printf '%s' "$detail" | head -c 260)"
  fi
fi

ssl_state="$(api GET "/api/v1/websites/$website_id/ssl")"
contains "the website now reports a certificate" "$ssl_state" '"enabled":true'
contains "the certificate is valid"              "$ssl_state" '"status":"valid"'
contains "it renews automatically by default"    "$ssl_state" '"auto_renew":true'
contains "the expiry is recorded"                "$ssl_state" '"expires_at"'

# ------------------------------------------- the acceptance (TASKS.md Phase 6)

log ""
log "Serving over HTTPS"
refresh_token

expect_status "the site completes a TLS handshake" 200 "$(https_status "$SITE_DOMAIN")"
expect_status "plain HTTP is redirected"           301 "$(http_status "$SITE_DOMAIN")"

# The redirect must reach the secure site rather than looping.
redirect="$(curl -s -o /dev/null -w '%{redirect_url}' --max-time 20 \
  -H "Host: $SITE_DOMAIN" "$SITES_BASE_URL/" 2>/dev/null || true)"
contains "the redirect points at HTTPS" "$redirect" "https://$SITE_DOMAIN"

expect_status "dotfiles stay denied over HTTPS" 403 "$(https_status "$SITE_DOMAIN" "/.env")"

# ------------------------------------------------ the renewal path stays open

log ""
log "The ACME challenge survives the redirect"

# This is what keeps renewal working. A certificate authority fetches the
# challenge over plain HTTP and will not follow a redirect to a certificate it
# has not issued yet, so a redirect that catches this path breaks renewal
# silently — sixty days later, when the certificate lapses and the site goes
# down for every visitor at once.
#
# What this does NOT show is that the Agent creates a directory nginx can
# serve from: the directory is made here, by this shell, under its own umask
# of 0022. The Agent runs with 0077, and its own creation of this path left
# .well-known and acme-challenge at 0700 - so this check passed while every
# real HTTP-01 validation would have failed. That property is tested where the
# Agent's umask can be reproduced: agent/internal/ssl/challenge_mode_test.go.
mkdir -p /var/www/.acme-challenge/.well-known/acme-challenge
printf 'acme-token-payload' > /var/www/.acme-challenge/.well-known/acme-challenge/probe
chmod 644 /var/www/.acme-challenge/.well-known/acme-challenge/probe

expect_status "the challenge is served over plain HTTP" 200 \
  "$(http_status "$SITE_DOMAIN" "/.well-known/acme-challenge/probe")"
contains "the challenge returns its token" \
  "$(http_body "$SITE_DOMAIN" "/.well-known/acme-challenge/probe")" "acme-token-payload"
# Everything else still goes to HTTPS.
expect_status "other paths are still redirected" 301 \
  "$(http_status "$SITE_DOMAIN" "/index.html")"

# -------------------------------------------------------- key material safety

log ""
log "Private key handling"

key="/etc/jothost/ssl/$SITE_DOMAIN/privkey.pem"
if [ -f "$key" ]; then
  mode="$(stat -c '%a' "$key")"
  owner="$(stat -c '%U' "$key")"
  # Anyone who can read this file can impersonate the site until the
  # certificate expires, and no later action undoes that.
  if [ "$mode" = "600" ]; then
    pass "the private key is readable only by its owner"
  else
    fail "the private key is mode $mode, want 600"
  fi
  if [ "$owner" = "root" ]; then
    pass "the private key is owned by root"
  else
    fail "the private key is owned by $owner, want root"
  fi

  dirmode="$(stat -c '%a' "/etc/jothost/ssl/$SITE_DOMAIN")"
  if [ "$dirmode" = "700" ]; then
    pass "the certificate directory is not listable by others"
  else
    fail "the certificate directory is mode $dirmode, want 700"
  fi

  # A key beneath a document root would be served to anyone who guessed its
  # name, whatever the web server config said.
  case "$key" in
    /var/www/*) fail "the private key is under a document root" ;;
    *) pass "the private key is outside every document root" ;;
  esac
else
  fail "no private key was written at $key"
fi

# The certificate is public, but the key must never be reachable over HTTP.
expect_status "the key is not served over HTTPS" 404 \
  "$(https_status "$SITE_DOMAIN" "/privkey.pem")"

# --------------------------------------------------------------- TLS quality

log ""
log "TLS configuration"

if command -v openssl >/dev/null 2>&1 || apk add --no-cache openssl >/dev/null 2>&1; then
  negotiated="$(echo | openssl s_client -connect 127.0.0.1:443 -servername "$SITE_DOMAIN" 2>/dev/null |
    grep -E 'Protocol|New, TLS' | head -2)"
  case "$negotiated" in
    *TLSv1.3*|*TLSv1.2*) pass "the handshake negotiates TLS 1.2 or 1.3" ;;
    *) fail "unexpected negotiated protocol: $negotiated" ;;
  esac

  # Deprecated and prohibited for anything handling card data.
  if echo | openssl s_client -connect 127.0.0.1:443 -servername "$SITE_DOMAIN" -tls1_1 2>&1 |
     grep -qiE 'no protocols available|alert|failure|error'; then
    pass "TLS 1.1 is refused"
  else
    fail "TLS 1.1 was accepted"
  fi
else
  fail "openssl is unavailable, so the negotiated protocol could not be checked"
fi

# Lowercased before matching: HTTP/2 mandates lowercase header names, so the
# capitalised spelling never appears on the wire for an HTTPS response.
headers="$(curl -sk -D- -o /dev/null --max-time 20 --resolve "$SITE_DOMAIN:443:127.0.0.1" \
  "https://$SITE_DOMAIN/" 2>/dev/null | tr 'A-Z' 'a-z' || true)"
contains "HSTS is set"                "$headers" "strict-transport-security"
contains "content sniffing is off"    "$headers" "x-content-type-options"
contains "framing is restricted"      "$headers" "x-frame-options"
not_contains "the version is not advertised" "$headers" "nginx/"

# ------------------------------------------------------------------- listing

log ""
log "The certificate dashboard"
refresh_token

listing="$(api GET /api/v1/ssl)"
contains "the certificate is listed"       "$listing" "\"primary_domain\":\"$SITE_DOMAIN\""
contains "the listing reports days left"   "$listing" '"days_remaining"'
contains "the listing names the provider"  "$listing" '"provider":"selfsigned"'

# ------------------------------------------------------------------- renewal

log ""
log "Renewal"

renewed="$(api POST "/api/v1/websites/$website_id/ssl/renew" '')"
renew_job="$(printf '%s' "$renewed" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$renew_job" ]; then
  fail "renewal returned no job: $(printf '%s' "$renewed" | head -c 200)"
else
  state="$(await_job "$renew_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "the certificate was renewed"
  else
    fail "renewal ended $state"
  fi
  # A renewal that leaves the site down is worse than one that never ran.
  expect_status "the site still serves HTTPS after renewal" 200 "$(https_status "$SITE_DOMAIN")"
fi

# Turning auto-renewal off changes a row and nothing on the host, so it needs
# no job and no vhost rewrite.
expect_status "auto-renewal can be turned off" 200 \
  "$(api_status PATCH "/api/v1/websites/$website_id/ssl" '{"auto_renew":false}')"
contains "auto-renewal is now off" "$(api GET "/api/v1/websites/$website_id/ssl")" '"auto_renew":false'

# ------------------------------------------------------------------ revoking

log ""
log "Revoking"
refresh_token

revoked="$(api POST "/api/v1/websites/$website_id/ssl/revoke" '')"
revoke_job="$(printf '%s' "$revoked" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

if [ -z "$revoke_job" ]; then
  fail "revocation returned no job: $(printf '%s' "$revoked" | head -c 200)"
else
  state="$(await_job "$revoke_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "the certificate was revoked"
  else
    fail "revocation ended $state"
  fi

  contains "the website reports no certificate" \
    "$(api GET "/api/v1/websites/$website_id/ssl")" '"enabled":false'
  # Back on plain HTTP, serving rather than redirecting to a certificate that
  # is no longer there.
  expect_status "the site is served over plain HTTP again" 200 "$(http_status "$SITE_DOMAIN")"

  if [ -f "$key" ]; then
    fail "the private key survived revocation"
  else
    pass "the private key was removed"
  fi
fi

# ------------------------------------------------------------------ cleanup

log ""
log "Cleanup"

# Deleting a website must take its certificate with it: a private key with no
# owner is the same class of leftover as an orphaned PHP pool.
issued="$(api POST "/api/v1/websites/$website_id/ssl/issue" '{"provider":"selfsigned"}')"
issue_job="$(printf '%s' "$issued" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
[ -n "$issue_job" ] && await_job "$issue_job" >/dev/null

delete_job="$(printf '%s' "$(api DELETE "/api/v1/websites/$website_id?remove_files=true")" |
  sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
if [ -n "$delete_job" ]; then
  await_job "$delete_job" >/dev/null
fi

if [ -d "/etc/jothost/ssl/$SITE_DOMAIN" ]; then
  fail "the certificate outlived its website"
else
  pass "deleting the website removed its certificate"
fi

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 6 SSL checks passed"
