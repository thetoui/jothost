#!/bin/sh
# A document root per domain, not only per website.
#
# Every name on a site used to be served from the one directory the website
# recorded. That is right for an alias whose purpose is to be another spelling
# of the same site, and wrong for one site answering for two things - a
# marketing domain and a shop, an old name kept on a frozen copy, an alias
# pointed at a staging build.
#
# nginx has one root per server block, so a name with a root of its own needs a
# block of its own. What is checked here is the generated file on the host, not
# the panel's answer about it: the panel reporting a path it did not write is
# the failure this whole file exists to catch. The content is fetched too,
# because a config that parses and serves the wrong directory looks identical
# from the outside.
#
# Run with:  make docker-test-domain-roots

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
SITE="roots$STAMP.test"
ALIAS="shop$STAMP.test"
failures=0
token=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# POSIX sh has no local variables, so these are prefixed: called with a plain
# "name" this would overwrite whatever the script was working on.
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

# A website's own id, not the last "id" in the response: the creation response
# carries the site's domains too, each with an id of its own.
website_id() { printf '%s' "$1" | sed -n 's/.*"website":{"id":"\([^"]*\)".*/\1/p'; }

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

# The site's status, read with the domains cut off first: a domain carries a
# status of its own, and sed is greedy enough to find that one instead.
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

# Where the Agent writes generated vhosts (nginx.DefaultSitesDir). Read off
# disk rather than asked for: the panel reporting a path it did not write is
# the failure this file exists to catch.
VHOST="/etc/nginx/conf.d/$SITE.conf"

log 'A document root per domain'
log '=========================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. A site with an alias on it'
site="$(api POST /api/v1/websites "{\"domain\":\"$SITE\"}")"
site_id="$(website_id "$site")"
if [ -z "$site_id" ]; then
  log "  the website was not created: $site"
  exit 1
fi
control 'the website became active' "$(wait_active "$site_id")" active

added="$(api POST "/api/v1/websites/$site_id/domains" \
  "{\"domain\":\"$ALIAS\",\"type\":\"alias\"}")"
alias_id="$(printf '%s' "$added" | sed -n 's/.*"domain":{"id":"\([^"]*\)".*/\1/p')"
if [ -z "$alias_id" ]; then
  log "  the alias was not added: $added"
  exit 1
fi
pass "the alias $ALIAS is on the site"
sleep 4

# The file has to be there before anything is counted in it. Without this a
# wrong path reads as "0 server blocks" and every count below is a statement
# about a file that does not exist.
if [ -f "$VHOST" ]; then
  pass "the vhost is at $VHOST"
else
  fail "no vhost at $VHOST - the checks below would all be about nothing"
  log "  what is there: $(ls /etc/nginx/conf.d/ | tr '\n' ' ')"
  api DELETE "/api/v1/websites/$site_id" >/dev/null 2>&1 || true
  exit 1
fi

# Before: one server block, and the alias is another name on it. Establishing
# this is what makes the count below a statement about the change.
before="$(grep -c '^server {' "$VHOST" 2>/dev/null || echo 0)"
control 'the site starts with one server block' "$before" 1

log ''
log '2. The alias is given a directory of its own'
control 'the panel accepts the change' \
  "$(status_of PATCH "/api/v1/domains/$alias_id" '{"document_root":"shop"}')" 202
sleep 5

log ''
log '3. The generated config says so'
# Read off the host. A panel that answered "shop" and wrote nothing would pass
# every check made of its own responses.
after="$(grep -c '^server {' "$VHOST" 2>/dev/null || echo 0)"
control 'the alias got a server block of its own' "$after" 2

if grep -q "server_name $ALIAS;" "$VHOST"; then
  pass 'the block answers for the alias alone'
else
  fail "the alias has no block of its own: $(grep server_name "$VHOST" | tr '\n' ' ')"
fi
if grep -q "root /var/www/$SITE/shop;" "$VHOST"; then
  pass 'and serves the directory it was given'
else
  fail "the root is wrong: $(grep -c 'root ' "$VHOST") root lines, $(grep 'root ' "$VHOST" | tr '\n' ' ')"
fi

# The site's own names are still on their own block and still on their own
# root. Two blocks answering for one name is a config whose behaviour depends
# on which nginx read first.
if [ "$(grep -c "server_name $SITE;" "$VHOST")" = "1" ]; then
  pass 'the site keeps exactly one block of its own'
else
  fail "the site's name appears in $(grep -c "server_name $SITE;" "$VHOST") blocks"
fi

log ''
log '4. The directory was created and is what gets served'
# nginx pointed at a directory that is not there answers 404 to everything with
# nothing to say why, so the panel creates it.
if [ -d "/var/www/$SITE/shop" ]; then
  pass "/var/www/$SITE/shop exists"
else
  fail "the document root was not created"
fi

printf 'the shop, not the site\n' > "/var/www/$SITE/shop/index.html" 2>/dev/null || true
printf 'the site itself\n' > "/var/www/$SITE/public/index.html" 2>/dev/null || true
chmod 644 "/var/www/$SITE/shop/index.html" "/var/www/$SITE/public/index.html" 2>/dev/null || true

served="$(curl -sS -H "Host: $ALIAS" http://127.0.0.1/ 2>/dev/null || true)"
case "$served" in
  *'the shop, not the site'*) pass 'a request for the alias is served from the shop' ;;
  *) fail "the alias served: $(printf '%s' "$served" | head -c 120)" ;;
esac

site_served="$(curl -sS -H "Host: $SITE" http://127.0.0.1/ 2>/dev/null || true)"
case "$site_served" in
  *'the site itself'*) pass 'and the site is still served from its own root' ;;
  *) fail "the site served: $(printf '%s' "$site_served" | head -c 120)" ;;
esac

log ''
log '5. The primary name is refused'
# Moving the site is what PATCH /websites/{id} does. Two routes setting one
# value is how they come to disagree.
primary_id="$(api GET "/api/v1/websites/$site_id/domains" | tr '{' '\n' |
  sed -n 's/.*"id":"\([^"]*\)".*"type":"primary".*/\1/p')"
if [ -n "$primary_id" ]; then
  control 'the primary domain cannot be moved this way' \
    "$(status_of PATCH "/api/v1/domains/$primary_id" '{"document_root":"elsewhere"}')" 400
else
  fail 'the primary domain could not be found'
fi

log ''
log '6. A path outside the site is refused'
# This string becomes an nginx root directive read by a process running as
# root. Escaping the site would publish the host's own files under a domain.
for bad in '../../etc' '/etc' 'public/../../../etc'; do
  control "a document root of $bad is refused" \
    "$(status_of PATCH "/api/v1/domains/$alias_id" "{\"document_root\":\"$bad\"}")" 422
done

log ''
log '7. Clearing it puts the name back on the website'
control 'the panel accepts the clear' \
  "$(status_of PATCH "/api/v1/domains/$alias_id" '{"document_root":""}')" 202
sleep 5

cleared="$(grep -c '^server {' "$VHOST" 2>/dev/null || echo 0)"
control 'the extra block is gone' "$cleared" 1
if grep -q "server_name $SITE $ALIAS;" "$VHOST"; then
  pass 'and the alias is a name on the site again'
else
  fail "the names are: $(grep server_name "$VHOST" | tr '\n' ' ')"
fi

log ''
log '8. The trail records it'
case "$(api GET '/api/v1/audit?action=domain.update&limit=5')" in
  *domain.update*) pass 'changing a domain root was audited' ;;
  *) fail 'no audit entry for the change' ;;
esac

log ''
log 'Cleaning up'
api DELETE "/api/v1/websites/$site_id" >/dev/null 2>&1 || true

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
