#!/bin/sh
# Phase 22 Docker integration test — multi-tenancy.
#
# The acceptance is not that the API returns 200. A quota that is recorded and
# not enforced is worse than no quota: it is a promise to a customer that the
# panel does not keep, and it looks identical from the outside to one that
# works. So every limit in here is proved by being *hit*.
#
# What is proved here that nothing else can prove:
#
#   * The hierarchy is a boundary. A reseller cannot create another reseller,
#     cannot see accounts outside its own subtree, and cannot touch a
#     subscription that is not below it.
#   * A hard limit refuses. The second website in a one-website plan is turned
#     away with a conflict, not accepted and counted later.
#   * A limit is charged to the *owner*, not the caller. An administrator with
#     no plan at all is refused when creating inside a customer's subscription,
#     which is the hole every quota system has if it looks at who is asking.
#   * An add-on raises the limit by exactly what it says, and the next one over
#     is refused again.
#   * A soft limit allows and says so, rather than silently allowing.
#   * Suspension stops growth immediately.
#   * Disk and bandwidth are measured from the real filesystem and a real
#     access log, and bandwidth survives a log rotation instead of resetting.
#   * "Not measured" is a distinct answer from "nothing used".
#   * A resource limit becomes a real systemd slice unit on the host, with the
#     directives systemd understands — and the panel says 'declared' rather
#     than 'applied' on a host that is not enforcing it.
#   * Impersonation is one hop, downwards, recorded, and revoked at once when
#     it ends.
#
# It runs inside the agent container, which is the managed host: the disk and
# slice checks look at real files.
#
# Run with:  make docker-test-tenancy

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
RESELLER="t22res$STAMP"
CUSTOMER="t22cust$STAMP"
OUTSIDER="t22out$STAMP"
PASSWORD="phase22-test-password-9271"
DOMAIN_A="t22a$STAMP.test"
DOMAIN_B="t22b$STAMP.test"
DOMAIN_C="t22c$STAMP.test"
UNIT_DIR="/etc/systemd/system"

failures=0
token=""
reseller_token=""
plan_id=""
addon_id=""
soft_plan_id=""
subscription_id=""
reseller_id=""
customer_id=""
outsider_id=""
website_a=""
website_b=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

login() {
  curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
}

as() {
  who="$1"; method="$2"; path="$3"; body="${4:-}"
  bearer="$token"
  [ "$who" = reseller ] && bearer="$reseller_token"
  if [ -n "$body" ]; then
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" 2>/dev/null || true
  fi
}

as_status() {
  who="$1"; method="$2"; path="$3"; body="${4:-}"
  bearer="$token"
  [ "$who" = reseller ] && bearer="$reseller_token"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $bearer" 2>/dev/null || true
  fi
}

api()        { as admin "$@"; }
api_status() { as_status admin "$@"; }

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 300))" ;;
  esac
}

lacks() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

# The FIRST match, not the last.
#
# A sed script of the form .*"id":"..." looks like it reads the first id and
# reads the last one, because the leading .* is greedy. On a create response —
# which carries the website and then the job that provisions it — that silently
# returns the job's id, and every check afterwards is about the wrong object.
json_field() {
  printf %s "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -n 1 |
    sed 's/^[^:]*:"//; s/"$//'
}

json_number() {
  printf %s "$1" | grep -o "\"$2\":-\?[0-9][0-9]*" | head -n 1 | sed 's/^[^:]*://'
}

# await_website waits for the provisioning job to make the site's directories.
#
# Creating a website returns immediately with a queued job; the directory this
# script measures does not exist until the worker has run. Without this the disk
# figures below would be measuring a path that is not there yet.
await_website() {
  waited=0
  while [ "$waited" -lt 120 ]; do
    state="$(json_field "$(api GET "/api/v1/websites/$1")" status)"
    case "$state" in
      active|failed) printf %s "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf TIMEOUT
}

cleanup() {
  [ -n "$website_a" ] && api DELETE "/api/v1/websites/$website_a" >/dev/null 2>&1
  [ -n "$website_b" ] && api DELETE "/api/v1/websites/$website_b" >/dev/null 2>&1
  # Websites are deleted through a job, so the subscription may still own rows
  # for a moment. It is retried a few times rather than raced.
  waited=0
  while [ "$waited" -lt 60 ]; do
    [ -n "$subscription_id" ] || break
    code="$(api_status DELETE "/api/v1/tenancy/subscriptions/$subscription_id")"
    [ "$code" = "200" ] || [ "$code" = "404" ] && break
    sleep 3; waited=$((waited + 3))
  done
  [ -n "$customer_id" ] && api DELETE "/api/v1/tenancy/accounts/$customer_id" >/dev/null 2>&1
  [ -n "$outsider_id" ] && api DELETE "/api/v1/tenancy/accounts/$outsider_id" >/dev/null 2>&1
  [ -n "$reseller_id" ] && api DELETE "/api/v1/tenancy/accounts/$reseller_id" >/dev/null 2>&1
  [ -n "$addon_id" ] && api DELETE "/api/v1/tenancy/plans/$addon_id" >/dev/null 2>&1
  [ -n "$soft_plan_id" ] && api DELETE "/api/v1/tenancy/plans/$soft_plan_id" >/dev/null 2>&1
  [ -n "$plan_id" ] && api DELETE "/api/v1/tenancy/plans/$plan_id" >/dev/null 2>&1
  return 0
}
trap cleanup EXIT

log "Phase 22 — multi-tenancy"
log ""

token="$(login "$ADMIN_USER" "$ADMIN_PASS")"
if [ -z "$token" ]; then
  log "FATAL: could not sign in as $ADMIN_USER"
  exit 1
fi

# ------------------------------------------------------------------- 1. host

log "1. What this host can enforce"

overview="$(api GET /api/v1/tenancy/overview)"
contains "the overview reports what the host does about resource limits" \
  "$overview" '"isolation_available"'
# A slice with nothing in it is a limit on nothing, and the panel reports that
# as its own fact rather than letting availability imply it.
contains "placement is reported separately from availability" "$overview" '"placement"'

# ------------------------------------------------------------- 2. hierarchy

log ""
log "2. The hierarchy is a boundary"

created="$(api POST /api/v1/tenancy/accounts \
  "{\"username\":\"$RESELLER\",\"password\":\"$PASSWORD\",\"tier\":\"reseller\"}")"
reseller_id="$(json_field "$created" id)"
contains "an admin can create a reseller" "$created" "\"username\":\"$RESELLER\""
contains "a reseller is given the reseller role" "$created" '"reseller"'

created="$(api POST /api/v1/tenancy/accounts \
  "{\"username\":\"$CUSTOMER\",\"password\":\"$PASSWORD\",\"tier\":\"customer\",\"parent_id\":\"$reseller_id\"}")"
customer_id="$(json_field "$created" id)"
contains "a customer can be created under a named reseller" "$created" "\"parent_username\":\"$RESELLER\""

created="$(api POST /api/v1/tenancy/accounts \
  "{\"username\":\"$OUTSIDER\",\"password\":\"$PASSWORD\",\"tier\":\"customer\"}")"
outsider_id="$(json_field "$created" id)"

reseller_token="$(login "$RESELLER" "$PASSWORD")"
if [ -z "$reseller_token" ]; then
  fail "the reseller could not sign in"
else
  pass "the reseller can sign in"
fi

# Strictly below, always. A reseller who could create a reseller could create
# one whose parent is their own parent — and the tree could then contain a
# cycle, which is what makes the descendant query terminate.
expect_status "a reseller cannot create another reseller" 403 \
  "$(as_status reseller POST /api/v1/tenancy/accounts \
    "{\"username\":\"t22rogue$STAMP\",\"password\":\"$PASSWORD\",\"tier\":\"reseller\"}")"

seen="$(as reseller GET /api/v1/tenancy/accounts)"
contains "a reseller sees its own customer" "$seen" "\"username\":\"$CUSTOMER\""
lacks "a reseller does not see an account outside its subtree" "$seen" "\"username\":\"$OUTSIDER\""
lacks "a reseller does not see the administrator" "$seen" "\"username\":\"$ADMIN_USER\""

# ----------------------------------------------------------------- 3. plans

log ""
log "3. Plans, and what a limit means"

created="$(api POST /api/v1/tenancy/plans \
  "{\"name\":\"P22 Starter $STAMP\",\"kind\":\"plan\",\"enforcement\":\"hard\",
    \"limits\":{\"max_websites\":1,\"max_databases\":0,\"max_subdomains\":0,\"disk_mb\":1024},
    \"isolation\":{\"cpu_percent\":50,\"memory_mb\":512}}")"
plan_id="$(json_field "$created" id)"
# Unlimited is a null and none is a zero, and the two survive the round trip as
# different values. A plan with no mailbox limit and a plan including no
# mailboxes are opposite promises.
contains "an unset limit comes back as unlimited, not zero" "$created" '"max_mailboxes":null'
contains "a limit of none comes back as zero" "$created" '"max_databases":0'

created="$(api POST /api/v1/tenancy/plans \
  "{\"name\":\"P22 Extra $STAMP\",\"kind\":\"addon\",\"limits\":{\"max_websites\":1}}")"
addon_id="$(json_field "$created" id)"
contains "an add-on can be built" "$created" '"kind":"addon"'

expect_status "a plan with a negative limit is refused" 422 \
  "$(api_status POST /api/v1/tenancy/plans \
    "{\"name\":\"P22 Bad $STAMP\",\"limits\":{\"max_websites\":-1},\"isolation\":{}}")"
expect_status "a memory cap that would kill every process is refused" 422 \
  "$(api_status POST /api/v1/tenancy/plans \
    "{\"name\":\"P22 Bad2 $STAMP\",\"limits\":{},\"isolation\":{\"memory_mb\":4}}")"
expect_status "a name carrying a newline is refused" 422 \
  "$(api_status POST /api/v1/tenancy/plans \
    "{\"name\":\"P22 Bad3\nMemoryMax=1K\",\"limits\":{},\"isolation\":{}}")"

# --------------------------------------------------------- 4. subscriptions

log ""
log "4. Subscriptions and the slice"

created="$(as reseller POST /api/v1/tenancy/subscriptions \
  "{\"owner_user_id\":\"$customer_id\",\"plan_id\":\"$plan_id\",\"name\":\"Acme $STAMP\"}")"
subscription_id="$(json_field "$created" id)"
contains "a reseller can put its customer on a plan" "$created" "\"owner_username\":\"$CUSTOMER\""

slice="$(json_field "$created" slice_name)"
state="$(json_field "$created" isolation_state)"
case "$state" in
  applied|declared) pass "the host reported what it did with the limits ($state)" ;;
  *) fail "the isolation state is $state" ;;
esac

if [ -f "$UNIT_DIR/$slice" ]; then
  pass "a systemd slice unit was written for the subscription"
  unit="$(cat "$UNIT_DIR/$slice")"
  contains "the unit caps CPU in the spelling systemd understands" "$unit" "CPUQuota=50%"
  contains "the unit caps memory in the spelling systemd understands" "$unit" "MemoryMax=512M"
  contains "the unit is a slice" "$unit" "[Slice]"
  # A dash is a level of nesting to systemd, so a subscription id's dashes must
  # not survive into the name or one customer's cap contains another's.
  case "$slice" in
    jothost-sub-*.slice)
      stem="${slice%.slice}"
      dashes="$(printf '%s' "$stem" | tr -cd '-' | wc -c | tr -d ' ')"
      if [ "$dashes" = "2" ]; then
        pass "the slice name is flat rather than nested under another"
      else
        fail "the slice name has $dashes dashes: $slice"
      fi ;;
    *) fail "unexpected slice name: $slice" ;;
  esac
else
  fail "no slice unit at $UNIT_DIR/$slice"
fi

if [ "$state" = "declared" ]; then
  contains "a host that cannot enforce says so in words" "$created" "does not run systemd"
fi

# ---------------------------------------------------------------- 5. quotas

log ""
log "5. A hard limit refuses"

created="$(api POST /api/v1/websites \
  "{\"domain\":\"$DOMAIN_A\",\"name\":\"a\",\"subscription_id\":\"$subscription_id\"}")"
website_a="$(json_field "$created" id)"
if [ -n "$website_a" ]; then
  pass "the first website in a one-website plan is created"
else
  fail "the first website was not created: $(printf '%s' "$created" | head -c 200)"
fi

state="$(await_website "$website_a")"
if [ "$state" = "active" ]; then
  pass "and provisioned on the host"
else
  fail "the website did not become active (it is $state)"
fi

# The administrator asking has no plan at all. If the guard charged the caller
# this would be allowed, which is the hole every quota system has when it looks
# at who is asking rather than at who owns the thing.
refusal="$(api POST /api/v1/websites \
  "{\"domain\":\"$DOMAIN_B\",\"name\":\"b\",\"subscription_id\":\"$subscription_id\"}")"
contains "the second is refused against the customer's plan, not the caller's" \
  "$refusal" "allows 1 websites"
expect_status "and refused with a conflict rather than a permission error" 409 \
  "$(api_status POST /api/v1/websites \
    "{\"domain\":\"$DOMAIN_B\",\"name\":\"b\",\"subscription_id\":\"$subscription_id\"}")"

# A database inside that website is charged to the website's owner too, and the
# plan includes none.
expect_status "a database is refused by the owning subscription's limit of none" 409 \
  "$(api_status POST /api/v1/databases \
    "{\"name\":\"t22db$STAMP\",\"engine\":\"mysql\",\"website_id\":\"$website_a\"}")"

# A subdomain is charged to the parent website's subscription, and its id comes
# from the *path* rather than the body. That is the case that was broken and
# invisible: a guard that reads a path value without routing the request reads
# an empty string, finds nothing, and charges the caller instead — so the limit
# silently stops binding for anybody senior enough to be asking.
subdomain_refusal="$(api_status POST "/api/v1/websites/$website_a/subdomains" "{\"name\":\"sub$STAMP\"}")"
expect_status "a subdomain is charged through the website named in the path" 409 "$subdomain_refusal"

log ""
log "6. An add-on raises the limit by what it says"

after="$(api POST "/api/v1/tenancy/subscriptions/$subscription_id/addons" \
  "{\"plan_id\":\"$addon_id\",\"quantity\":1}")"
contains "the effective website limit rises to two" "$after" '"max_websites":2'
# Unlimited absorbs an add-on rather than becoming limited by it.
contains "an unlimited dimension is untouched by the add-on" "$after" '"max_mailboxes":null'

created="$(api POST /api/v1/websites \
  "{\"domain\":\"$DOMAIN_B\",\"name\":\"b\",\"subscription_id\":\"$subscription_id\"}")"
website_b="$(json_field "$created" id)"
if [ -n "$website_b" ]; then
  pass "the second website is now allowed"
else
  fail "the second website was still refused: $(printf '%s' "$created" | head -c 200)"
fi

expect_status "the third is refused again at the new limit" 409 \
  "$(api_status POST /api/v1/websites \
    "{\"domain\":\"$DOMAIN_C\",\"name\":\"c\",\"subscription_id\":\"$subscription_id\"}")"

log ""
log "7. A soft limit allows and says so"

created="$(api POST /api/v1/tenancy/plans \
  "{\"name\":\"P22 Soft $STAMP\",\"kind\":\"plan\",\"enforcement\":\"soft\",
    \"limits\":{\"max_websites\":1},\"isolation\":{}}")"
soft_plan_id="$(json_field "$created" id)"

api PATCH "/api/v1/tenancy/subscriptions/$subscription_id" \
  "{\"plan_id\":\"$soft_plan_id\"}" >/dev/null
# Two websites already exist against a limit of one. A soft plan allows it and
# marks the response, which is a different answer from an unlimited plan.
warning="$(curl -s -D - -o /dev/null --max-time 60 -X POST "$API_BASE_URL/api/v1/websites" \
  -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
  -d "{\"domain\":\"$DOMAIN_C\",\"name\":\"c\",\"subscription_id\":\"$subscription_id\"}" \
  2>/dev/null | tr -d '\r')"
# Go canonicalises a header name, so this is the spelling on the wire.
contains "a soft limit lets it through and warns" "$warning" "X-Jothost-Quota-Warning"
contains "and names the overage" "$warning" "over its websites limit"
# Put it back and remove the extra site so the counts below are the ones the
# rest of this script reasons about.
third="$(api GET /api/v1/websites | tr '{' '\n' | grep "$DOMAIN_C" |
  sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -n 1)"
[ -n "$third" ] && api DELETE "/api/v1/websites/$third" >/dev/null
api PATCH "/api/v1/tenancy/subscriptions/$subscription_id" "{\"plan_id\":\"$plan_id\"}" >/dev/null

log ""
log "8. Suspension stops growth"

suspended="$(api POST "/api/v1/tenancy/subscriptions/$subscription_id/status" \
  '{"status":"suspended","reason":"unpaid invoice 42"}')"
contains "a suspension records why" "$suspended" '"suspended_reason":"unpaid invoice 42"'
refusal="$(api POST /api/v1/websites \
  "{\"domain\":\"t22sus$STAMP.test\",\"name\":\"s\",\"subscription_id\":\"$subscription_id\"}")"
contains "a suspended subscription is refused by name, not by its limits" \
  "$refusal" "suspended: unpaid invoice 42"
api POST "/api/v1/tenancy/subscriptions/$subscription_id/status" '{"status":"active"}' >/dev/null

# ------------------------------------------------------------ 9. measuring

log ""
log "9. Disk and bandwidth are measured, not assumed"

root="/var/www/$DOMAIN_A"
if [ -d "$root" ]; then
  pass "the website's directory exists on this host"
else
  fail "no directory at $root"
fi

before="$(api GET "/api/v1/tenancy/subscriptions/$subscription_id")"
contains "an unmeasured subscription says so rather than reporting zero" \
  "$before" '"disk_bytes":null'

dd if=/dev/zero of="$root/public/phase22.bin" bs=1024 count=2048 2>/dev/null
printf '1.2.3.4 - - [01/Jan/2026:10:00:00 +0000] "GET / HTTP/1.1" 200 12345 "-" "curl"\n' \
  >> "$root/logs/access.log"

measured="$(api POST "/api/v1/tenancy/subscriptions/$subscription_id/measure")"
disk="$(json_number "$measured" disk_bytes)"
band="$(json_number "$measured" bandwidth_bytes)"
if [ -n "$disk" ] && [ "$disk" -ge 2097152 ]; then
  pass "disk usage is read from the filesystem ($disk bytes)"
else
  fail "disk usage came back as '$disk', which is below the 2 MiB just written"
fi
if [ "$band" = "12345" ]; then
  pass "bandwidth is read from the site's own access log"
else
  fail "bandwidth came back as '$band', want 12345"
fi

# A log rotation resets the file's counter. Bandwidth must accumulate across it,
# or every rotation would hand a customer their allowance back.
mv "$root/logs/access.log" "$root/logs/access.log.1"
printf '1.2.3.4 - - [01/Jan/2026:11:00:00 +0000] "GET /x HTTP/1.1" 200 500 "-" "curl"\n' \
  > "$root/logs/access.log"
measured="$(api POST "/api/v1/tenancy/subscriptions/$subscription_id/measure")"
band="$(json_number "$measured" bandwidth_bytes)"
if [ "$band" = "12845" ]; then
  pass "bandwidth accumulates across a log rotation rather than resetting"
else
  fail "bandwidth after a rotation is '$band', want 12845"
fi

# --------------------------------------------------------- 10. impersonation

log ""
log "10. Impersonation is one hop, downwards, and recorded"

started="$(as reseller POST /api/v1/tenancy/impersonation \
  "{\"user_id\":\"$customer_id\",\"reason\":\"support ticket $STAMP\"}")"
imp_token="$(printf '%s' "$started" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
if [ -n "$imp_token" ]; then
  pass "a reseller can sign in as its own customer"
else
  fail "impersonation failed: $(printf '%s' "$started" | head -c 200)"
fi

current="$(curl -s --max-time 20 "$API_BASE_URL/api/v1/tenancy/impersonation" \
  -H "Authorization: Bearer $imp_token" 2>/dev/null)"
contains "the session says whose account it is" "$current" "\"subject_username\":\"$CUSTOMER\""
contains "and who opened it" "$current" "\"actor_username\":\"$RESELLER\""
contains "and why" "$current" "support ticket $STAMP"

# No nesting. The second hop's record would otherwise name the first hop's
# subject as the actor, and the chain back to a person breaks exactly where it
# matters.
nested="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  -X POST "$API_BASE_URL/api/v1/tenancy/impersonation" \
  -H "Authorization: Bearer $imp_token" -H 'Content-Type: application/json' \
  -d "{\"user_id\":\"$outsider_id\"}" 2>/dev/null)"
expect_status "an impersonated session cannot impersonate" 403 "$nested"

# Nothing gained: the tenancy permissions are stripped from the session.
expect_status "an impersonated session cannot manage the tenancy" 403 \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    "$API_BASE_URL/api/v1/tenancy/accounts" \
    -H "Authorization: Bearer $imp_token" 2>/dev/null)"

expect_status "a reseller cannot impersonate an account outside its subtree" 404 \
  "$(as_status reseller POST /api/v1/tenancy/impersonation "{\"user_id\":\"$outsider_id\"}")"

ended="$(curl -s --max-time 20 -X DELETE "$API_BASE_URL/api/v1/tenancy/impersonation" \
  -H "Authorization: Bearer $imp_token" 2>/dev/null)"
contains "the impersonation can be ended from inside it" "$ended" '"ended":true'

# Ending drops the access token too, not only the session. Otherwise "stop
# impersonating" would leave a working token in a browser tab.
expect_status "the token stops working at once" 401 \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    "$API_BASE_URL/api/v1/tenancy/impersonation" \
    -H "Authorization: Bearer $imp_token" 2>/dev/null)"

history="$(as reseller GET /api/v1/tenancy/impersonation/history)"
contains "the impersonation is in the history" "$history" "support ticket $STAMP"
contains "with an end time" "$history" '"ended_at":"2'

# ------------------------------------------------------------- 11. deleting

log ""
log "11. Nothing is deleted out from under what depends on it"

expect_status "a plan a subscription is on cannot be deleted" 409 \
  "$(api_status DELETE "/api/v1/tenancy/plans/$plan_id")"
expect_status "a subscription that still owns websites cannot be deleted" 409 \
  "$(api_status DELETE "/api/v1/tenancy/subscriptions/$subscription_id")"
expect_status "an account that still owns a subscription cannot be deleted" 403 \
  "$(api_status DELETE "/api/v1/tenancy/accounts/$customer_id")"
expect_status "a reseller with customers under it cannot be deleted" 409 \
  "$(api_status DELETE "/api/v1/tenancy/accounts/$reseller_id")"

log ""
log "12. And an account that has done things can still be deleted"

# The regression this section exists for.
#
# audit_logs.user_id was ON DELETE SET NULL "so removing a user never erases
# history", and the same table is append-only with a trigger refusing every
# UPDATE. The cascade is an UPDATE, so deleting any account that had ever done
# something auditable failed with an internal error naming a trigger. This
# reseller has signed in, created a subscription and impersonated somebody, so
# it has audit rows; deleting it is what proves the fix.
api DELETE "/api/v1/websites/$website_a" >/dev/null
api DELETE "/api/v1/websites/$website_b" >/dev/null
waited=0
while [ "$waited" -lt 90 ]; do
  code="$(api_status DELETE "/api/v1/tenancy/subscriptions/$subscription_id")"
  [ "$code" = "200" ] && break
  sleep 3; waited=$((waited + 3))
done
expect_status "the subscription is deleted once its websites are gone" 200 "${code:-none}"
subscription_id=""
website_a=""
website_b=""

expect_status "a customer with audit history is deleted" 200   "$(api_status DELETE "/api/v1/tenancy/accounts/$customer_id")"
customer_id=""
expect_status "a reseller with audit history is deleted" 200   "$(api_status DELETE "/api/v1/tenancy/accounts/$reseller_id")"
reseller_id=""

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 22 checks passed."
  exit 0
fi
log "$failures Phase 22 check(s) failed."
exit 1
