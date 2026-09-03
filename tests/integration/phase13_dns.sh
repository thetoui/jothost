#!/bin/sh
# Phase 13 Docker integration test — the DNS manager.
#
# The acceptance is not that the API returns 200. It is that a real BIND, given
# a zone through the panel, answers real queries about it: the records resolve,
# the signatures verify, a secondary transfers the zone from a *different*
# server the panel configured it to pull from, and a deleted zone stops being
# answered.
#
# Four of the things checked here are things this phase got wrong first, and
# every one of them would have passed a test that only read back the file the
# panel wrote:
#
#   - the zone file's header used "#", which a zone file does not treat as a
#     comment, so named refused every zone with "unknown RR type 'Managed'"
#   - a zone whose name servers are inside it was written with no glue, which
#     named-checkzone refuses outright
#   - the servers table's ipv4 column had never been filled by anything, so
#     there was no address to put in those records
#   - a zone that failed to finish left its row behind and took its own name,
#     so the next attempt was refused as a duplicate of something that had
#     never worked
#
# It runs inside the agent container, which is the managed host in development.
#
# Run with:  make docker-test-dns

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

ZONE="${ZONE:-p13.integration.test}"
REVERSE_NET="${REVERSE_NET:-203.0.113.0/24}"
REVERSE_ZONE="113.0.203.in-addr.arpa"
# The throwaway primary the panel's secondary zone transfers from. A second
# named on a second loopback address, so replication is proved by an actual
# transfer between two servers rather than by reading a configuration file.
# The throwaway primary runs on this container's own network address while the
# panel's server is told to answer only on the loopback. Two servers need two
# addresses, and these are the two this container actually has: BIND binds only
# addresses that exist on an interface, so a second loopback address it could
# not find would leave it silently listening on nothing.
REPLICA_ZONE="${REPLICA_ZONE:-p13replica.integration.test}"

failures=0
work="$(mktemp -d)"
trap 'cleanup' EXIT

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  # The second named is this test's own; stop it however it started.
  if [ -f "$work/replica.pid" ]; then
    kill "$(cat "$work/replica.pid")" 2>/dev/null || true
  fi
  pkill -f "named -c $work/replica.conf" 2>/dev/null || true
  rm -rf "$work"
}

for tool in curl dig; do
  command -v "$tool" >/dev/null 2>&1 || apk add --no-cache curl bind-tools >/dev/null 2>&1
done

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 200))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (unexpectedly found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

# json_field pulls one top-level field out of the data object.
json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -1
}

# first_id pulls the first id out of a response.
first_id() {
  printf '%s' "$1" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -1
}

log '== Phase 13: DNS =='

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

# --- the host's name server ------------------------------------------------

status="$(api GET /api/v1/dns)"
contains 'the DNS endpoint reports the host' "$status" '"available":true'
contains 'BIND can sign zones' "$status" '"supports_dnssec":true'

# Clean up anything a previous run left, so the checks below are about this run.
for id in $(printf '%s' "$(api GET /api/v1/dns/zones)" | tr '{' '\n' |
            grep -E "\"name\":\"($ZONE|$REVERSE_ZONE|$REPLICA_ZONE)\"" |
            sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); do
  api_status DELETE "/api/v1/dns/zones/$id" >/dev/null
done

# --- settings and a zone ---------------------------------------------------

# The panel's server is told which addresses to answer on rather than "any".
#
# That is what leaves this container's network address free for the second name
# server further down, and it is the only way the replication check can be a
# real one: two servers need two addresses. It exercises listen-on at the same
# time, a setting that used to be written once and never again — so it silently
# stopped applying after the first zone.
OWN_ADDR="$(hostname -i 2>/dev/null | awk '{print $1}')"
settings="$(api PUT /api/v1/dns/settings \
  "{\"default_ns\":[\"ns1.$ZONE.\",\"ns2.$ZONE.\"],\"hostmaster\":\"hostmaster@$ZONE\",\"dnssec_policy\":\"default\",\"default_ttl\":3600,\"listen_on\":[\"127.0.0.1\"]}")"
contains 'the name server settings are saved' "$settings" '"dnssec_policy":"default"'
contains 'the listen addresses are recorded' "$settings" '"listen_on":["127.0.0.1"]'

created="$(api POST /api/v1/dns/zones "{\"name\":\"$ZONE\"}")"
contains 'the zone is created' "$created" "\"name\":\"$ZONE\""
zone_id="$(first_id "$created")"
if [ -z "$zone_id" ]; then
  log 'FATAL: the zone was not created; nothing below can be checked'
  log "$created"
  exit 1
fi

# A second zone of the same name is a configuration named refuses to load.
expect_status 'a duplicate zone is refused' 409 \
  "$(api_status POST /api/v1/dns/zones "{\"name\":\"$ZONE\"}")"

# --- the server actually answers -------------------------------------------

soa="$(dig +short @127.0.0.1 "$ZONE" SOA 2>/dev/null)"
contains 'the name server answers for the zone' "$soa" "ns1.$ZONE."

# The glue. Without it named-checkzone refuses the zone outright, so this is
# not a nicety: it is what makes the zone loadable at all.
glue="$(dig +short @127.0.0.1 "ns1.$ZONE" A 2>/dev/null)"
if [ -n "$glue" ]; then
  pass "the zone's own name server has an address record ($glue)"
else
  fail "ns1.$ZONE has no address record, so the zone could not have loaded"
fi

seeded="$(dig +short @127.0.0.1 "www.$ZONE" A 2>/dev/null)"
if [ -n "$seeded" ]; then
  pass "the seeded www record resolves ($seeded)"
else
  fail 'the seeded www record does not resolve'
fi

# The zone file is a zone file: its comments start with ";". A "#" here is not
# a comment, and named refuses the whole zone.
zone_file="/var/bind/jothost/$ZONE.zone"
if [ -f "$zone_file" ]; then
  pass 'the zone file was written'
  case "$(head -1 "$zone_file")" in
    ';'*) pass 'the zone file starts with a zone-file comment' ;;
    *) fail "the zone file's first line is not a comment: $(head -1 "$zone_file")" ;;
  esac
  if ! grep -q '^#' "$zone_file"; then
    pass 'no line in the zone file begins with a #'
  else
    fail 'a line in the zone file begins with a #, which named reads as a record'
  fi
else
  fail "the zone file is missing: $zone_file"
fi

# --- records ---------------------------------------------------------------

expect_status 'an MX record is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"@\",\"type\":\"MX\",\"priority\":10,\"value\":\"mail.$ZONE.\"}")"
mx="$(dig +short @127.0.0.1 "$ZONE" MX 2>/dev/null)"
contains 'the MX record resolves with its priority' "$mx" "10 mail.$ZONE."

expect_status 'a TXT record is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"@\",\"type\":\"TXT\",\"value\":\"v=spf1 mx -all\"}")"
txt="$(dig +short @127.0.0.1 "$ZONE" TXT 2>/dev/null)"
contains 'the TXT record resolves' "$txt" 'v=spf1 mx -all'

# A DKIM key is longer than a single character-string can hold. One long string
# is a zone file named refuses to load.
long="$(printf 'k%.0s' $(seq 1 400))"
expect_status 'a TXT value longer than 255 bytes is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"selector._domainkey\",\"type\":\"TXT\",\"value\":\"$long\"}")"
dkim="$(dig +short @127.0.0.1 "selector._domainkey.$ZONE" TXT 2>/dev/null)"
if printf '%s' "$dkim" | tr -d '" ' | grep -q "$(printf 'k%.0s' $(seq 1 300))"; then
  pass 'a long TXT value is served as one reassembled string'
else
  fail "the long TXT value did not come back whole: $(printf '%s' "$dkim" | head -c 80)"
fi

expect_status 'an SRV record is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"_sip._tcp\",\"type\":\"SRV\",\"priority\":10,\"weight\":5,\"port\":5060,\"value\":\"sip.$ZONE.\"}")"
srv="$(dig +short @127.0.0.1 "_sip._tcp.$ZONE" SRV 2>/dev/null)"
contains 'the SRV record resolves with all three numbers' "$srv" "10 5 5060 sip.$ZONE."

expect_status 'a CAA record is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"@\",\"type\":\"CAA\",\"flags\":0,\"tag\":\"issue\",\"value\":\"letsencrypt.org\"}")"
caa="$(dig +short @127.0.0.1 "$ZONE" CAA 2>/dev/null)"
contains 'the CAA record resolves' "$caa" 'issue "letsencrypt.org"'

# --- what the panel refuses ------------------------------------------------

expect_status 'an AAAA record holding an IPv4 address is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    '{"name":"bad","type":"AAAA","value":"203.0.113.1"}')"

expect_status 'a CNAME beside another record is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"www\",\"type\":\"CNAME\",\"value\":\"$ZONE.\"}")"

expect_status 'a CNAME at the apex is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"@\",\"type\":\"CNAME\",\"value\":\"$ZONE.\"}")"

expect_status 'a misspelt CAA tag is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    '{"name":"@","type":"CAA","flags":0,"tag":"issued","value":"letsencrypt.org"}')"

expect_status 'an SRV record with no port is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    "{\"name\":\"_x._tcp\",\"type\":\"SRV\",\"priority\":1,\"weight\":1,\"port\":0,\"value\":\"x.$ZONE.\"}")"

expect_status 'a TTL below the floor is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    '{"name":"quick","type":"A","ttl":5,"value":"203.0.113.9"}')"

expect_status 'an unsupported record type is refused' 422 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    '{"name":"@","type":"DNSKEY","value":"whatever"}')"

# --- the serial advances ---------------------------------------------------

before="$(dig +short @127.0.0.1 "$ZONE" SOA 2>/dev/null | awk '{print $3}')"
expect_status 'another record is accepted' 201 \
  "$(api_status POST "/api/v1/dns/zones/$zone_id/records" \
    '{"name":"shop","type":"A","value":"203.0.113.30"}')"
after="$(dig +short @127.0.0.1 "$ZONE" SOA 2>/dev/null | awk '{print $3}')"
if [ -n "$before" ] && [ -n "$after" ] && [ "$after" -gt "$before" ]; then
  pass "the serial advanced when a record changed ($before to $after)"
else
  fail "the serial did not advance ($before to $after): every secondary would ignore the change"
fi

# --- DNSSEC ----------------------------------------------------------------

signed="$(api PATCH "/api/v1/dns/zones/$zone_id" '{"dnssec":true}')"
contains 'signing is switched on' "$signed" '"dnssec":true'

# named generates the key and signs on its own schedule; give it a moment.
attempt=0
while [ "$attempt" -lt 20 ]; do
  dnskey="$(dig +short @127.0.0.1 "$ZONE" DNSKEY 2>/dev/null)"
  [ -n "$dnskey" ] && break
  attempt=$((attempt + 1))
  sleep 1
done
if [ -n "${dnskey:-}" ]; then
  pass 'the zone has a DNSKEY record'
else
  fail 'the zone was never signed'
fi

# The key being published and the zone being signed are separate steps, and
# named does the second on its own schedule.
attempt=0
rrsig=0
while [ "$attempt" -lt 20 ]; do
  rrsig="$(dig +dnssec @127.0.0.1 "www.$ZONE" A 2>/dev/null | grep -c RRSIG || true)"
  [ "${rrsig:-0}" -gt 0 ] && break
  attempt=$((attempt + 1))
  sleep 1
done
if [ "${rrsig:-0}" -gt 0 ]; then
  pass 'answers carry signatures'
else
  fail 'the zone is signed and its answers carry no signatures'
fi

detail="$(api GET "/api/v1/dns/zones/$zone_id")"
contains 'the panel reports the signing policy' "$detail" '"policy":"default"'
contains 'the panel reports the DS record for the registrar' "$detail" '"digest_type":2'
contains 'the panel reports the key as signing' "$detail" '"zone_signing":true'

# The measured surprise: with inline signing the served serial runs ahead of
# the file's. A panel that compared them would report drift on every signed
# zone, forever — so it reports both, separately.
file_serial="$(printf '%s' "$detail" | sed -n 's/.*"serial":\([0-9]*\).*/\1/p' | head -1)"
served_serial="$(printf '%s' "$detail" | sed -n 's/.*"signed_serial":\([0-9]*\).*/\1/p' | head -1)"
if [ -n "$served_serial" ] && [ "$served_serial" -gt 0 ]; then
  pass "the panel reports the file's serial and the served one separately ($file_serial and $served_serial)"
else
  fail 'the served serial was not reported'
fi

# --- zone transfer ---------------------------------------------------------

refused="$(dig @127.0.0.1 "$ZONE" AXFR 2>&1 || true)"
contains 'a transfer is refused before anybody is allowed' "$refused" 'Transfer failed'

allowed="$(api PATCH "/api/v1/dns/zones/$zone_id" '{"allow_transfer":["127.0.0.1"]}')"
contains 'a transfer target is recorded' "$allowed" '127.0.0.1'
transfer="$(dig +noall +answer @127.0.0.1 "$ZONE" AXFR 2>/dev/null | wc -l)"
if [ "$transfer" -gt 5 ]; then
  pass "the whole zone transfers to an allowed address ($transfer records)"
else
  fail "the transfer returned $transfer records"
fi

# --- master/slave replication ----------------------------------------------
#
# A second named, on a second loopback address, is this test's own primary. The
# panel is then given a *secondary* zone pointing at it, and the check is that
# the panel's server answers for a zone whose contents it was never given —
# which can only have arrived by transfer.

mkdir -p "$work/zones"
cat > "$work/zones/replica.zone" <<EOF
\$TTL 3600
@	IN	SOA	ns1.$REPLICA_ZONE. hostmaster.$REPLICA_ZONE. (
			2026090301 3600 900 1209600 3600 )
@	IN	NS	ns1.$REPLICA_ZONE.
ns1	IN	A	$OWN_ADDR
onlyhere	IN	A	198.51.100.77
EOF

cat > "$work/replica.conf" <<EOF
options {
	directory "$work/zones";
	listen-on port 53 { $OWN_ADDR; };
	listen-on-v6 { none; };
	pid-file "$work/replica.pid";
	recursion no;
	allow-recursion { none; };
	allow-transfer { any; };
};
zone "$REPLICA_ZONE" IN {
	type master;
	file "$work/zones/replica.zone";
};
EOF

chmod -R 0777 "$work"

# The panel's own server has to have stopped listening on 127.0.0.2 for this to
# bind at all, which is what the listen-on setting above arranged.
if grep -q "listen-on { 127.0.0.1; };" /etc/bind/named.conf 2>/dev/null; then
  pass "the panel's server listens only where it was told"
else
  fail "the panel's named.conf does not carry the listen addresses that were saved"
fi

if named -c "$work/replica.conf" >/dev/null 2>&1; then
  sleep 2
  if dig +short "@$OWN_ADDR" "onlyhere.$REPLICA_ZONE" A 2>/dev/null | grep -q 198.51.100.77; then
    pass 'a second name server is running with a zone the panel has never seen'

    secondary="$(api POST /api/v1/dns/zones \
      "{\"name\":\"$REPLICA_ZONE\",\"kind\":\"slave\",\"masters\":[\"$OWN_ADDR\"]}")"
    contains 'the panel accepts a secondary zone' "$secondary" '"kind":"slave"'
    secondary_id="$(first_id "$secondary")"

    attempt=0
    replicated=""
    while [ "$attempt" -lt 20 ]; do
      replicated="$(dig +short @127.0.0.1 "onlyhere.$REPLICA_ZONE" A 2>/dev/null)"
      [ -n "$replicated" ] && break
      attempt=$((attempt + 1))
      sleep 1
    done
    if printf '%s' "$replicated" | grep -q 198.51.100.77; then
      pass 'the panel-configured secondary answers with data it transferred from the primary'
    else
      fail 'the secondary never transferred the zone'
    fi

    # A secondary's contents are its primary's. Editing here would be a change
    # that is silently replaced at the next transfer.
    if [ -n "$secondary_id" ]; then
      expect_status "a secondary zone's records cannot be edited" 422 \
        "$(api_status POST "/api/v1/dns/zones/$secondary_id/records" \
          '{"name":"x","type":"A","value":"203.0.113.5"}')"
      expect_status 'the secondary zone is deleted' 200 \
        "$(api_status DELETE "/api/v1/dns/zones/$secondary_id")"
    fi
  else
    fail 'the second name server did not answer; replication was not checked'
  fi
else
  fail 'the second name server would not start; replication was not checked'
fi

# --- reverse zones ---------------------------------------------------------

reverse="$(api POST /api/v1/dns/zones \
  "{\"reverse_network\":\"$REVERSE_NET\",\"nameservers\":[\"ns1.$ZONE.\"],\"primary_ns\":\"ns1.$ZONE.\",\"hostmaster\":\"hostmaster@$ZONE\"}")"
contains 'a reverse zone is named after its network' "$reverse" "\"name\":\"$REVERSE_ZONE\""
reverse_id="$(first_id "$reverse")"

if [ -n "$reverse_id" ]; then
  expect_status 'a PTR is accepted, addressed by the IP it is for' 201 \
    "$(api_status POST "/api/v1/dns/zones/$reverse_id/records" \
      "{\"name\":\"203.0.113.7\",\"type\":\"PTR\",\"value\":\"mail.$ZONE.\"}")"
  ptr="$(dig +short @127.0.0.1 -x 203.0.113.7 2>/dev/null)"
  contains 'the reverse lookup resolves' "$ptr" "mail.$ZONE."

  expect_status 'a network with no zone of its own is refused' 422 \
    "$(api_status POST /api/v1/dns/zones \
      "{\"reverse_network\":\"203.0.113.0/25\",\"nameservers\":[\"ns1.$ZONE.\"]}")"

  expect_status 'the reverse zone is deleted' 200 \
    "$(api_status DELETE "/api/v1/dns/zones/$reverse_id")"
fi

# --- deleting a zone stops it being served ---------------------------------

expect_status 'the zone is deleted' 200 "$(api_status DELETE "/api/v1/dns/zones/$zone_id")"

gone="$(dig @127.0.0.1 "$ZONE" SOA 2>/dev/null | grep -c 'status: REFUSED' || true)"
if [ "${gone:-0}" -gt 0 ]; then
  pass 'the deleted zone is no longer answered'
else
  fail 'the server still answers for the deleted zone'
fi

if [ ! -f "$zone_file" ]; then
  pass "the deleted zone's file was removed"
else
  fail "the deleted zone's file is still on disk"
fi

# A journal left behind is not harmless: named replays it over a recreated
# zone and serves records the panel does not have.
if [ ! -f "$zone_file.jnl" ] && [ ! -f "$zone_file.signed" ]; then
  pass "the deleted zone's journal and signatures were removed with it"
else
  fail "the deleted zone left a journal or a signed copy behind"
fi

# --- named is still healthy ------------------------------------------------

if named-checkconf /etc/bind/named.conf >/dev/null 2>&1; then
  pass 'the configuration the panel leaves behind is one named accepts'
else
  fail 'named-checkconf refuses the configuration the panel left'
fi

final="$(api GET /api/v1/dns)"
contains 'the name server is still running at the end' "$final" '"running":true'

# --- result ---------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 13 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
