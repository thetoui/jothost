#!/bin/sh
# DNS templates: the records a new zone starts with.
#
# A zone used to be seeded with two records hard-coded in Go - an A for the
# apex and one for www. Anybody who wanted every new domain to start with an
# MX, an SPF record or a CAA added them by hand to each one, and anybody who
# did not want www deleted it each time.
#
# The claim under test is that a template decides what a zone gets, and that a
# template which cannot produce valid records is refused when it is written
# rather than when somebody creates a domain. So the zone is read back and its
# records counted, and the placeholders are checked to have actually been
# substituted - a zone containing the literal text {ip} is one the name server
# refuses to load, which takes down every domain on the host rather than only
# the new one.
#
# Run with:  make docker-test-dns-templates

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
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

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -sS -X "$method" "$API_BASE_URL$path" -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body"
  else
    curl -sS -X "$method" "$API_BASE_URL$path" -H "Authorization: Bearer $token"
  fi
}

status_of() {
  curl -sS -o /dev/null -w '%{http_code}' -X "$1" "$API_BASE_URL$2" \
    -H "Authorization: Bearer $token" -H 'Content-Type: application/json' -d "${3:-}"
}

builtin_ids() {
  api GET /api/v1/dns/templates | tr '{' '\n' |
    sed -n 's/.*"id":"\([^"]*\)".*"builtin":true.*/\1/p'
}

log 'DNS templates'
log '============='

token="$(field "$(curl -sS -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}")" access_token)"
if [ -z "$token" ]; then
  log 'Could not sign in; nothing below would mean anything.'
  exit 1
fi

log ''
log '1. The host starts with a built-in template'
control 'the templates endpoint answers' "$(status_of GET /api/v1/dns/templates)" 200
listing="$(api GET /api/v1/dns/templates)"
case "$listing" in
  *'"builtin":true'*) pass 'a built-in template exists' ;;
  *) fail 'no built-in template: a new zone would start with nothing' ;;
esac
case "$listing" in
  *'{domain}'*) pass 'the response names the placeholders a template may use' ;;
  *) fail 'the response does not say which placeholders are available' ;;
esac

log ''
log '2. A template that cannot produce valid records is refused'
# Refused when it is written, not when somebody creates a domain and gets a
# zone the name server will not load.
control 'an unknown placeholder is refused' \
  "$(status_of POST /api/v1/dns/templates \
     '{"name":"Bad ipv6","records":[{"name":"@","type":"AAAA","value":"{ipv6}"}]}')" 422
control 'a nonsense address is refused' \
  "$(status_of POST /api/v1/dns/templates \
     '{"name":"Bad A","records":[{"name":"@","type":"A","value":"nonsense"}]}')" 422
control 'a nameless template is refused' \
  "$(status_of POST /api/v1/dns/templates '{"name":"","records":[]}')" 422

log ''
log '3. A template with mail on it, made the default'
made="$(api POST /api/v1/dns/templates "{
  \"name\": \"With mail $STAMP\",
  \"is_default\": true,
  \"records\": [
    {\"name\":\"@\",\"type\":\"A\",\"value\":\"{ip}\"},
    {\"name\":\"www\",\"type\":\"A\",\"value\":\"{ip}\"},
    {\"name\":\"mail\",\"type\":\"A\",\"value\":\"{ip}\"},
    {\"name\":\"@\",\"type\":\"MX\",\"value\":\"mail.{domain}\",\"priority\":10},
    {\"name\":\"@\",\"type\":\"TXT\",\"value\":\"v=spf1 a mx -all\"}
  ]}")"
template_id="$(field "$made" id)"
if [ -z "$template_id" ]; then
  fail "the template was not created: $made"
  exit 1
fi
pass 'the template was created'

# One default per server, enforced by a partial unique index rather than by
# hoping two requests never arrive together.
defaults="$(api GET /api/v1/dns/templates | grep -o '"is_default":true' | wc -l | tr -d ' ')"
if [ "$defaults" = "1" ]; then
  pass 'exactly one template is the default'
else
  fail "$defaults templates claim to be the default"
fi

log ''
log '4. A new zone is seeded from it'
zone_name="tpl$STAMP.test"
# The zone names its own name servers. Without them the panel falls back to
# the host's configured defaults and refuses the zone when there are none -
# deliberately. This suite used to rely on phase13_dns.sh having configured
# those defaults, which it only had on a machine where phase 13 had run at
# some point before; on a fresh stack this suite runs first, alphabetically,
# and every check below it failed on "a zone needs at least one name server".
# Outside the zone, so no glue record is needed either.
zone="$(api POST /api/v1/dns/zones "{\"name\":\"$zone_name\",\"seed_records\":true,\"nameservers\":[\"ns1.dns-templates.test.\"]}")"
zone_id="$(field "$zone" id)"
if [ -z "$zone_id" ]; then
  fail "the zone was not created: $zone"
  exit 1
fi
sleep 2

detail="$(api GET "/api/v1/dns/zones/$zone_id")"
for expected in '"type":"MX"' '"type":"TXT"' '"name":"mail"' '"name":"www"'; do
  case "$detail" in
    *"$expected"*) pass "the zone has $expected" ;;
    *) fail "the zone is missing $expected" ;;
  esac
done

log ''
log '5. The placeholders were substituted'
case "$detail" in
  *"mail.$zone_name"*) pass "the domain placeholder became $zone_name" ;;
  *) fail 'the domain placeholder was not substituted' ;;
esac
case "$detail" in
  *'{ip}'*) fail 'the address placeholder was left in the zone as literal text' ;;
  *) pass 'the address placeholder was substituted' ;;
esac

log ''
log '6. The built-in cannot be deleted'
# It can be edited - an operator wanting different defaults should not have to
# make a second template - but deleting the only one leaves new zones with
# nothing at all.
for id in $(builtin_ids); do
  control 'deleting the built-in is refused' \
    "$(status_of DELETE "/api/v1/dns/templates/$id")" 409
done

log ''
log '7. The trail records the change'
trail="$(api GET '/api/v1/audit?action=dns.template.save&limit=5')"
case "$trail" in
  *dns.template.save*) pass 'saving a template was audited' ;;
  *) fail 'no audit entry for saving a template' ;;
esac

log ''
log 'Cleaning up'
api DELETE "/api/v1/dns/zones/$zone_id" >/dev/null 2>&1 || true
# The built-in goes back to being the default before the one made here is
# removed, or the host is left with no default at all and the next run of these
# checks would start from a state this one created.
for id in $(builtin_ids); do
  api PUT "/api/v1/dns/templates/$id" '{"name":"Default","description":"The records a new domain starts with: the apex and www, both pointing at this host.","is_default":true,"records":[{"name":"@","type":"A","value":"{ip}"},{"name":"www","type":"A","value":"{ip}"}]}' >/dev/null 2>&1 || true
done
api DELETE "/api/v1/dns/templates/$template_id" >/dev/null 2>&1 || true

log ''
if [ "$failures" -eq 0 ]; then
  log 'All checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
