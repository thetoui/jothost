#!/bin/sh
# Phase 24 — recovery drills.
#
# This one runs on the machine that runs the stack, not inside it, because a
# recovery drill is about the deployment: it has to be able to take services
# away and put them back. A drill that could not stop anything would be a
# description of a drill.
#
# ---------------------------------------------------------------------------
# What is being asked
#
# The panel is not the hosting. It configures nginx, PHP-FPM, the databases and
# the mail server, and then those serve the customers whether or not the panel
# is running. That claim is made all through this repository's documentation and
# it is the reason the architecture is shaped as it is — and until now nothing
# has ever checked it.
#
# So the drills are the three ways the panel can be lost:
#
#   1. **The API dies.** Nobody can administer anything. Every website must go
#      on being served, and the panel must come back knowing what it knew.
#   2. **The Agent dies.** The panel can no longer change the machine. It must
#      say so rather than reporting itself well, and the websites must go on
#      being served.
#   3. **The database dies.** The panel has lost its records. It must refuse to
#      pretend: liveness may stay up, readiness must not, and no request may
#      answer with something that looks like success.
#
# And after all three, the data must be exactly what it was. An outage that
# quietly loses a row is worse than one that stops.
#
# The other two bullets of this phase are already drilled elsewhere and are not
# repeated here:
#
#   * backup restore — tests/integration/phase14_backup.sh takes a backup,
#     destroys the file and the row, restores both, and refuses an archive
#     whose checksum has changed.
#   * firewall recovery — tests/integration/phase16_firewall.sh applies a rule
#     provisionally, never confirms it, and watches the host put itself back.
#
# Run with:  make docker-test-recovery

set -eu

COMPOSE="${COMPOSE:-docker compose}"

# Where the stack actually published its ports, asked of the stack itself.
#
# These were hardcoded to 18080 and 18090, which is what one developer's .env
# maps them to. compose defaults to 8080 and 8090 and .env.example sets those,
# so in CI the drill polled a port nothing was listening on, waited two minutes
# and gave up before running a single drill — reporting the stack unready when
# the stack was fine. A port belongs to whoever started the containers, so ask
# them rather than guess.
published_port() {
  # "0.0.0.0:8080" and "[::]:8080" both end in the port.
  $COMPOSE port "$1" "$2" 2>/dev/null | head -1 | sed 's/.*://'
}

api_port="$(published_port api 8080)"
sites_port="$(published_port agent 80)"

API_URL="${RECOVERY_API_URL:-http://localhost:${api_port:-8080}}"
SITES_URL="${RECOVERY_SITES_URL:-http://localhost:${sites_port:-8090}}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
SITE_DOMAIN="dr$STAMP.test"

failures=0
token=""
website_id=""

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

login() {
  curl -s --max-time 20 -X POST "$API_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p'
}

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 60 -X "$method" "$API_URL$path" -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 60 -X "$method" "$API_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

code_for() {
  # `|| true` rather than `|| echo 000`: curl already prints 000 through -w when
  # it cannot connect, so the fallback printed a second one and every failure
  # read as "HTTP 000000". What the fallback is actually needed for is curl's
  # *exit status*, which is non-zero on a refused connection and would abort
  # the script under `set -e` — in a suite whose whole job is to make requests
  # that fail.
  curl -s -o /dev/null -w '%{http_code}' --max-time 15 "$@" 2>/dev/null || true
}

# site_answers reports whether the customer's website is still being served.
#
# Through nginx on the managed host, with the site's own Host header — the same
# request a visitor makes, and the only one that answers the question this
# whole script is about.
site_answers() {
  code_for -H "Host: $SITE_DOMAIN" "$SITES_URL/"
}

json_field() {
  printf '%s' "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -n 1 | sed 's/^[^:]*:"//; s/"$//'
}

# await_api waits for the panel to answer again after being restarted.
await_api() {
  waited=0
  while [ "$waited" -lt 90 ]; do
    [ "$(code_for "$API_URL/healthz")" = "200" ] && return 0
    sleep 2; waited=$((waited + 2))
  done
  return 1
}

# await_site waits for the customer-facing nginx to answer again.
#
# In the development stack that server lives inside the Agent's container, so
# restarting the Agent restarts it too and it takes a few seconds to come back.
# Without this wait, the next drill measures a web server that is still
# starting and reports the panel for it.
await_site() {
  waited=0
  while [ "$waited" -lt 90 ]; do
    [ "$(site_answers)" = "200" ] && return 0
    sleep 2; waited=$((waited + 2))
  done
  return 1
}

restore_stack() {
  $COMPOSE start api agent postgres >/dev/null 2>&1 || true
  await_api || true
  await_site || true
}

cleanup() {
  restore_stack
  if [ -n "$website_id" ]; then
    token="$(login)"
    api DELETE "/api/v1/websites/$website_id" >/dev/null 2>&1 || true
  fi
  return 0
}
trap cleanup EXIT

log "Phase 24 — recovery drills"
log ""

command -v curl >/dev/null 2>&1 || { log "FATAL: curl is required"; exit 1; }
$COMPOSE ps >/dev/null 2>&1 || { log "FATAL: the stack is not running (try: make dev)"; exit 1; }

# Everything below takes services away and expects a particular answer, so it
# has to start from a stack that is entirely up. A previous run interrupted
# halfway leaves one stopped, and the first drill then measures an outage
# nobody caused — which is how the first run of this script reported the panel
# unable to provision a website.
log "Bringing the stack to a known state before starting"
$COMPOSE start api agent postgres redis >/dev/null 2>&1 || true

waited=0
while [ "$waited" -lt 120 ]; do
  [ "$(code_for "$API_URL/readyz")" = "200" ] && break
  sleep 3; waited=$((waited + 3))
done
if [ "$(code_for "$API_URL/readyz")" != "200" ]; then
  log "FATAL: the stack did not become ready; nothing below would mean anything"
  exit 1
fi
log "  the panel is ready"
log ""

token="$(login)"
[ -n "$token" ] || { log "FATAL: could not sign in as $ADMIN_USER"; exit 1; }

# ------------------------------------------------------- 0. something to lose

log "0. A website to lose"

created=$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"recovery\"}")
website_id=$(json_field "$created" id)
[ -n "$website_id" ] || { log "FATAL: the drill site could not be created"; exit 1; }

waited=0
while [ "$waited" -lt 150 ]; do
  state=$(json_field "$(api GET "/api/v1/websites/$website_id")" status)
  case "$state" in active|failed) break ;; esac
  sleep 3; waited=$((waited + 3))
done
control "the drill website is provisioned" "$state" "active"
control "and is being served before anything is taken away" "$(site_answers)" "200"

accounts_before=$(api GET /api/v1/tenancy/accounts | grep -c '"username"' || echo 0)
sites_before=$(api GET /api/v1/websites | grep -c '"primary_domain"' || echo 0)

# ------------------------------------------------------- 1. the API dies

log ""
log "1. The API dies"

$COMPOSE stop api >/dev/null 2>&1
sleep 3

# Nobody can administer anything. That is expected and is not the point.
down=$(code_for "$API_URL/healthz")
if [ "$down" = "000" ] || [ "$down" = "502" ] || [ "$down" = "503" ]; then
  pass "the panel is unreachable, as it should be"
else
  fail "the panel answered HTTP $down with its API stopped"
fi

# The point. A control panel outage is not a hosting outage: nginx holds the
# port, the vhosts are on disk, and PHP-FPM is its own service. If this ever
# fails, the panel has quietly become a dependency of every site on the host.
served=$(site_answers)
if [ "$served" = "200" ]; then
  pass "every website is still served with the panel down"
else
  fail "the website stopped being served when the panel did (HTTP $served)"
fi

$COMPOSE start api >/dev/null 2>&1
if await_api; then
  pass "the panel comes back on its own"
else
  fail "the panel did not come back within 90 seconds"
fi

token="$(login)"
control "and can be signed in to again" "$([ -n "$token" ] && echo yes || echo no)" "yes"

sites_after=$(api GET /api/v1/websites | grep -c '"primary_domain"' || echo 0)
if [ "$sites_after" = "$sites_before" ]; then
  pass "it knows the same $sites_after websites it knew before"
else
  fail "the panel came back knowing $sites_after websites, not $sites_before"
fi

# ------------------------------------------------------ 2. the Agent dies

log ""
log "2. The Agent dies"

$COMPOSE stop agent >/dev/null 2>&1
sleep 3

# The panel can no longer change the machine. What matters is that it says so:
# a panel that reported itself well while unable to do anything would have an
# operator chasing the wrong fault.
live=$(code_for "$API_URL/healthz")
ready=$(code_for "$API_URL/readyz")
if [ "$live" = "200" ]; then
  pass "the API is alive and says so"
else
  fail "the API stopped answering liveness when the Agent stopped (HTTP $live)"
fi
if [ "$ready" != "200" ]; then
  pass "and readiness says it is not ready, rather than claiming otherwise"
else
  fail "readiness answered 200 with the Agent stopped"
fi

body=$(curl -s --max-time 15 "$API_URL/readyz" 2>/dev/null || true)
case "$body" in
  *agent*) pass "and names the Agent as what is missing" ;;
  *) fail "readiness does not say which dependency is down: $(printf '%s' "$body" | head -c 150)" ;;
esac

# The websites are deliberately *not* checked here, and the reason is a
# property of this stack rather than of the panel.
#
# In production nginx is its own systemd service and stopping jothost-agent
# leaves it running — that is what the installer lays down and what
# tests/integration/phase23_installer.sh checks on a real host. In development
# the Agent's container is also the machine it manages, so the customer-facing
# nginx lives inside it and stops when it does.
#
# Asserting "the sites keep serving" against this topology would be asserting
# something the topology cannot provide, and it would fail for a reason that
# has nothing to do with the claim. The claim is checked where it is real.
log "        (site availability without the Agent is checked on a real host,"
log "         in the installer suite: here the Agent's container is the host)"

# An operation that needs the Agent must fail cleanly rather than hang or leak.
token="$(login)"
attempt=$(code_for -X POST "$API_URL/api/v1/websites" -H "Authorization: Bearer $token" \
  -H 'Content-Type: application/json' \
  -d "{\"domain\":\"nope$STAMP.test\",\"name\":\"x\"}")
case "$attempt" in
  500|502|503|504|000)
    pass "a change that needs the Agent fails rather than half-succeeding" ;;
  201)
    # The row is written and the job queued; the work happens when the Agent
    # returns. That is also a correct answer, and a better one — but only if
    # the site is not reported as working.
    pass "a change is queued for when the Agent returns" ;;
  *) fail "a change that needs the Agent answered HTTP $attempt" ;;
esac

$COMPOSE start agent >/dev/null 2>&1
waited=0
while [ "$waited" -lt 90 ]; do
  [ "$(code_for "$API_URL/readyz")" = "200" ] && break
  sleep 2; waited=$((waited + 2))
done
if [ "$(code_for "$API_URL/readyz")" = "200" ]; then
  pass "readiness returns once the Agent is back"
else
  fail "readiness did not return within 90 seconds of the Agent restarting"
fi

if await_site; then
  pass "and the websites are served again"
else
  fail "the websites did not come back after the Agent restarted"
fi

# --------------------------------------------------- 3. the database dies

log ""
log "3. The database dies"

$COMPOSE stop postgres >/dev/null 2>&1
sleep 3

live=$(code_for "$API_URL/healthz")
ready=$(code_for "$API_URL/readyz")
if [ "$live" = "200" ]; then
  pass "the API process is alive and says so"
else
  fail "liveness went down with the database (HTTP $live)"
fi
if [ "$ready" != "200" ]; then
  pass "readiness refuses to claim the panel is usable"
else
  fail "readiness answered 200 with no database"
fi

# The panel has lost its records. What it must not do is answer a request as
# though it had them, and it must not spill the connection string while saying
# so — an error page is read by whoever caused the error.
listing=$(curl -s --max-time 20 "$API_URL/api/v1/websites" \
  -H "Authorization: Bearer $token" 2>/dev/null || true)
case "$listing" in
  *'"success":true'*) fail "the panel answered a listing with no database" ;;
  *) pass "a request that needs the database fails rather than answering emptily" ;;
esac
case "$listing" in
  *postgres://*|*password*) fail "the failure disclosed the connection string" ;;
  *) pass "and the failure does not disclose how it connects" ;;
esac

served=$(site_answers)
if [ "$served" = "200" ]; then
  pass "every website is still served with the database down"
else
  fail "the website stopped being served when the database did (HTTP $served)"
fi

$COMPOSE start postgres >/dev/null 2>&1
waited=0
while [ "$waited" -lt 120 ]; do
  [ "$(code_for "$API_URL/readyz")" = "200" ] && break
  sleep 3; waited=$((waited + 3))
done
if [ "$(code_for "$API_URL/readyz")" = "200" ]; then
  pass "the panel recovers when the database returns, without being restarted"
else
  fail "the panel did not recover within 120 seconds of the database returning"
fi

# ------------------------------------------------ 4. nothing was lost

log ""
log "4. After all three, the records are what they were"

token="$(login)"
control "the panel can be signed in to" "$([ -n "$token" ] && echo yes || echo no)" "yes"

sites_now=$(api GET /api/v1/websites | grep -c '"primary_domain"' || echo 0)
accounts_now=$(api GET /api/v1/tenancy/accounts | grep -c '"username"' || echo 0)

if [ "$sites_now" = "$sites_before" ]; then
  pass "the same $sites_now websites are recorded"
else
  fail "$sites_now websites are recorded, not the $sites_before there were"
fi
if [ "$accounts_now" = "$accounts_before" ]; then
  pass "the same $accounts_now accounts are recorded"
else
  fail "$accounts_now accounts are recorded, not the $accounts_before there were"
fi

detail=$(api GET "/api/v1/websites/$website_id")
case "$detail" in
  *"$SITE_DOMAIN"*) pass "the drill website is still there, by name" ;;
  *) fail "the drill website is not in the panel any more" ;;
esac
if [ "$(site_answers)" = "200" ]; then
  pass "and it is still being served"
else
  fail "the drill website is no longer served"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 24 recovery drills passed."
  exit 0
fi
log "$failures Phase 24 recovery drill(s) failed."
exit 1
