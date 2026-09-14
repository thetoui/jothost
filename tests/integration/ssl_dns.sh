#!/bin/sh
# Issuing a certificate puts this host's own DNS in order first.
#
# A certificate is obtained over the HTTP-01 challenge: the authority resolves
# every name on it and fetches a file from whatever answers. A name resolving
# nowhere fails, and a failed challenge is spent - Let's Encrypt allows a
# handful per hour and then stops looking for the rest of it. So a name in a
# zone this panel serves gets its address record before anything is asked of
# the authority.
#
# What is checked here is the record in the zone afterwards, not the response
# text: a panel that reported "added" and wrote nothing would pass any check
# made of its own summary. And the case that matters most is the one that is
# NOT touched - a name already pointing at another machine is reported and left
# alone, because repointing a live domain is a far larger act than issuing a
# certificate and nobody clicking Issue is asking for it.
#
# Run with:  make docker-test-ssl-dns

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
ZONE="acme$STAMP.test"
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

# A website's own id, not the last "id" in the response.
#
# The creation response carries the site's domains too, each with an id of its
# own, and field()'s greedy match takes the last one. Issuing against a domain
# id answers "Website not found", which reads like the site was never created
# and cost a whole run of these checks to work out.
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

# website_status reads a site's own status.
#
# Not a match on the whole response: it carries the site's domains, and a
# domain has a status of its own. Matching '"status":"active"' anywhere reports
# a site as up while it is still being created, which makes the wait below pass
# instantly and every check after it fail for a reason that is not DNS.
# The domains are cut off before the status is read.
#
# A site's response carries its domains, and a domain has a status of its own.
# Every pattern that reads the whole line finds the domain's - sed is greedy,
# and the domain's comes later - so a site still being created is reported as
# active the moment its first domain is. That is not a hypothetical: it made
# this wait return immediately and every check after it fail with "it is
# creating", while the control above them said the site was up.
website_status() {
  api GET "/api/v1/websites/$1" | sed 's/"domains".*//' |
    sed -n 's/.*"status":"\([^"]*\)".*/\1/p'
}

# wait_active waits for a site to be ready to take a certificate.
wait_active() {
  wait_i=0
  while [ "$wait_i" -lt 45 ]; do
    wait_state="$(website_status "$1")"
    case "$wait_state" in
      active) printf 'active'; return 0 ;;
      failed|'') ;;
    esac
    wait_i=$((wait_i + 1))
    sleep 2
  done
  printf '%s' "${wait_state:-unknown}"
}

# records prints "name type value" for every record in a zone.
records() {
  api GET "/api/v1/dns/zones/$1" | tr '{' '\n' |
    sed -n 's/.*"name":"\([^"]*\)","type":"\([^"]*\)".*"value":"\([^"]*\)".*/\1 \2 \3/p'
}

log 'Certificates and DNS'
log '===================='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. A zone this host serves, with no record for the name on the certificate'
# Seeding off: the point is a name with nothing pointing at it, which is the
# state a certificate is usually requested in.
zone="$(api POST /api/v1/dns/zones "{\"name\":\"$ZONE\",\"seed_records\":false}")"
zone_id="$(field "$zone" id)"
if [ -z "$zone_id" ]; then
  log "  the zone was not created: $zone"
  exit 1
fi
pass "the zone $ZONE exists"

before="$(records "$zone_id" | grep -c ' A ' || true)"
control 'the zone starts with no address records' "$before" 0

log ''
log '2. A website on that name, asking for a public certificate'
site="$(api POST /api/v1/websites "{\"domain\":\"$ZONE\"}")"
site_id="$(website_id "$site")"
if [ -z "$site_id" ]; then
  log "  the website was not created: $site"
  api DELETE "/api/v1/dns/zones/$zone_id" >/dev/null 2>&1 || true
  exit 1
fi
# Website creation is a job. The site has to reach a state that takes a
# certificate before the request below means anything.
active="$(wait_active "$site_id")"
control 'the website became active' "$active" active
if [ "$active" != "active" ]; then
  # Stopping rather than reporting each dependent check separately: with no
  # site, every one below fails for a reason that has nothing to do with DNS,
  # and one of them passes for the same wrong reason.
  log '  the site never came up, so nothing below would be a statement about DNS'
  api DELETE "/api/v1/websites/$site_id" >/dev/null 2>&1 || true
  api DELETE "/api/v1/dns/zones/$zone_id" >/dev/null 2>&1 || true
  exit 1
fi

# Staging, so that if certbot is installed on this host nothing spends a
# production rate limit. The DNS work happens before either way.
issued="$(api POST "/api/v1/websites/$site_id/ssl/issue" \
  '{"provider":"letsencrypt","staging":true,"auto_renew":false}')"

case "$issued" in
  *'"dns"'*) pass 'the response reports what was done to DNS' ;;
  *) fail "the response says nothing about DNS: $(printf '%s' "$issued" | head -c 300)" ;;
esac

log ''
log '3. The record is in the zone'
# The zone is read back rather than the response believed. A panel that
# reported "added" and wrote nothing would pass a check made of its own summary.
after="$(records "$zone_id")"
case "$after" in
  *'@ A '*) pass "the apex now has an address record: $(printf '%s' "$after" | grep '@ A ' | head -1)" ;;
  *) fail "no address record was added: $after" ;;
esac

log ''
log '4. A name already pointing somewhere else is left alone'
# The case that matters most. Repointing a live domain can take a working site
# off the internet, and issuing a certificate is not consent for that.
other="other$STAMP.test"
other_zone="$(api POST /api/v1/dns/zones "{\"name\":\"$other\",\"seed_records\":false}")"
other_id="$(field "$other_zone" id)"
api POST "/api/v1/dns/zones/$other_id/records" \
  '{"name":"@","type":"A","value":"198.51.100.77"}' >/dev/null

other_site="$(api POST /api/v1/websites "{\"domain\":\"$other\"}")"
other_site_id="$(website_id "$other_site")"
control 'the second website became active' "$(wait_active "$other_site_id")" active

other_issued="$(api POST "/api/v1/websites/$other_site_id/ssl/issue" \
  '{"provider":"letsencrypt","staging":true,"auto_renew":false}')"
case "$other_issued" in
  *'"status":"elsewhere"'*) pass 'the name is reported as pointing at another machine' ;;
  *) fail "the conflict was not reported: $(printf '%s' "$other_issued" | head -c 300)" ;;
esac

kept="$(records "$other_id" | grep '@ A ' || true)"
case "$kept" in
  *198.51.100.77*) pass 'the existing record was not overwritten' ;;
  *) fail "the record was changed to: $kept" ;;
esac

log ''
log '5. A self-signed certificate touches no DNS'
# Nothing resolves anything for one: it is written on this host and no
# authority ever looks the name up.
third="self$STAMP.test"
third_zone="$(api POST /api/v1/dns/zones "{\"name\":\"$third\",\"seed_records\":false}")"
third_id="$(field "$third_zone" id)"
third_site="$(api POST /api/v1/websites "{\"domain\":\"$third\"}")"
third_site_id="$(website_id "$third_site")"
control 'the third website became active' "$(wait_active "$third_site_id")" active

self_issued="$(api POST "/api/v1/websites/$third_site_id/ssl/issue" \
  '{"provider":"selfsigned","auto_renew":false}')"
case "$self_issued" in
  *'"dns"'*) fail 'a self-signed certificate reported DNS work' ;;
  *) pass 'a self-signed certificate reports no DNS work' ;;
esac
if [ "$(records "$third_id" | grep -c ' A ' || true)" = "0" ]; then
  pass 'and wrote no record'
else
  fail "and wrote a record anyway: $(records "$third_id")"
fi

log ''
log '6. The trail records it'
case "$(api GET '/api/v1/audit?action=dns.certificate.align&limit=5')" in
  *dns.certificate.align*) pass 'pointing a name at this host was audited' ;;
  *) fail 'no audit entry for the DNS change' ;;
esac

log ''
log 'Cleaning up'
for id in "$site_id" "$other_site_id" "$third_site_id"; do
  [ -n "$id" ] && api DELETE "/api/v1/websites/$id" >/dev/null 2>&1 || true
done
for id in "$zone_id" "$other_id" "$third_id"; do
  [ -n "$id" ] && api DELETE "/api/v1/dns/zones/$id" >/dev/null 2>&1 || true
done

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
