#!/bin/sh
# Phase 4.1 Docker integration test — the subdomain manager.
#
# Black-box checks against the running stack. The acceptance is not that the API
# returns 201: it is that a subdomain created through the panel is served by
# nginx on its own name, from its own directory, with its own logs — that a
# wildcard subdomain catches every name beneath the parent that nothing else
# claims, that an exact name still beats it, and that removing a subdomain
# leaves the parent exactly as it was.
#
# It runs inside the agent container, which is the managed host in development,
# so it can look at the vhost files, the directories and the accounts directly.
#
# Run with:  make docker-test-subdomains

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

PARENT_DOMAIN="${PARENT_DOMAIN:-p41.integration.test}"
NESTED="shop.$PARENT_DOMAIN"
ISOLATED="app.$PARENT_DOMAIN"
WILDCARD="*.$PARENT_DOMAIN"

# Where the Agent writes vhosts. Alpine's nginx reads conf.d; Debian's reads
# sites-enabled, and the Agent is told which by its configuration — so the
# suite asks the same setting rather than assuming one distribution.
SITES_DIR="${AGENT_NGINX_SITES_DIR:-/etc/nginx/conf.d}"

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
  if [ -z "$needle" ]; then
    fail "$name (the value searched for is empty, so this check proves nothing)"
    return
  fi
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle', which must not be there)" ;;
    *) pass "$name" ;;
  esac
}

# json_field JSON KEY — the value of the FIRST occurrence of KEY.
json_field() {
  printf '%s' "$1" | awk -v key="\"$2\":\"" '
    {
      at = index($0, key)
      if (at == 0) { exit }
      rest = substr($0, at + length(key))
      end = index(rest, "\"")
      if (end == 0) { exit }
      print substr(rest, 1, end - 1)
    }'
}

# site_id LISTING DOMAIN — the id of a website by its domain.
site_id() {
  printf '%s' "$1" | tr '{' '\n' | grep -F "\"primary_domain\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# await_active ID — wait for a website to finish provisioning.
#
# The website's own status, not any status in the response: a website carries a
# domains array whose rows are "active" from the moment they are written, so
# matching the whole body would succeed while the site is still being created.
await_active() {
  await_id="$1"
  await_waited=0
  while [ "$await_waited" -lt 90 ]; do
    await_site="$(api GET "/api/v1/websites/$await_id")"
    await_state="$(json_field "$await_site" 'status')"
    case "$await_state" in
      active|failed) break ;;
    esac
    sleep 2
    await_waited=$((await_waited + 2))
  done
  printf '%s' "$await_state"
}

# await_body HOST PATTERN — fetch a name until the body contains PATTERN.
#
# An nginx reload is graceful: the old workers finish what they are serving
# before the new configuration takes over. A check firing the instant the panel
# returns can legitimately see the previous configuration.
await_body() {
  await_host="$1"; await_pattern="$2"
  await_waited=0
  while [ "$await_waited" -lt 20 ]; do
    await_out="$(curl -s --max-time 20 -H "Host: $await_host" http://127.0.0.1/ 2>/dev/null || true)"
    case "$await_out" in
      *"$await_pattern"*) printf '%s' "$await_out"; return ;;
    esac
    sleep 1
    await_waited=$((await_waited + 1))
  done
  printf '%s' "$await_out"
}

# await_gone HOST — fetch a name until it is no longer served by its own vhost.
await_gone() {
  await_host="$1"; await_pattern="$2"
  await_waited=0
  while [ "$await_waited" -lt 20 ]; do
    await_out="$(curl -s --max-time 20 -H "Host: $await_host" http://127.0.0.1/ 2>/dev/null || true)"
    case "$await_out" in
      *"$await_pattern"*) ;;
      *) printf '%s' "$await_out"; return ;;
    esac
    sleep 1
    await_waited=$((await_waited + 1))
  done
  printf '%s' "$await_out"
}

# remove_site DOMAIN — delete a website and wait for the row to go.
remove_site() {
  remove_id="$(site_id "$(api GET '/api/v1/websites?include_subdomains=true')" "$1")"
  [ -n "$remove_id" ] || return 0

  case "$2" in
    subdomain) api DELETE "/api/v1/subdomains/$remove_id" >/dev/null 2>&1 || true ;;
    *)         api DELETE "/api/v1/websites/$remove_id" >/dev/null 2>&1 || true ;;
  esac

  remove_waited=0
  while [ "$remove_waited" -lt 40 ]; do
    if [ -z "$(site_id "$(api GET '/api/v1/websites?include_subdomains=true')" "$1")" ]; then
      return 0
    fi
    sleep 2
    remove_waited=$((remove_waited + 2))
  done
}

# purge removes anything a previous run left behind, on both sides.
#
# Subdomains first: a parent with subdomains is refused, which is the product
# behaviour this suite checks later on.
purge() {
  remove_site "$WILDCARD" subdomain
  remove_site "$NESTED" subdomain
  remove_site "$ISOLATED" subdomain
  remove_site "$PARENT_DOMAIN" website

  # And the directories, if a delete did not get to them. A tree left behind
  # belongs to an account that no longer exists, so the next run's site would
  # inherit files nginx cannot read.
  rm -rf "/var/www/$PARENT_DOMAIN" "/var/www/$ISOLATED" "/var/www/_wildcard.$PARENT_DOMAIN"
  rm -f "$SITES_DIR/$PARENT_DOMAIN.conf" \
        "$SITES_DIR/$NESTED.conf" \
        "$SITES_DIR/$ISOLATED.conf" \
        "$SITES_DIR/_wildcard.$PARENT_DOMAIN.conf"
}

# ------------------------------------------------------------------ the run

log 'Phase 4.1 — subdomain manager'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

purge

# --- 1. the parent -------------------------------------------------------

log ''
log '1. The parent website'

created="$(api POST /api/v1/websites "{\"domain\":\"$PARENT_DOMAIN\"}")"
PARENT="$(json_field "$created" 'id')"
if [ -z "$PARENT" ]; then
  fail "create the parent website (got: $(printf '%s' "$created" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

state="$(await_active "$PARENT")"
if [ "$state" = "active" ]; then
  pass 'the parent website is active'
else
  fail "the parent website is active (it is $state)"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

parent_site="$(api GET "/api/v1/websites/$PARENT")"
PARENT_USER="$(json_field "$parent_site" 'system_user')"
contains 'it has no parent of its own' "$parent_site" '"parent_website_id":null'
not_contains 'a fresh website carries no subdomains' "$parent_site" '"subdomains"'

# --- 2. a nested subdomain ----------------------------------------------

log ''
log '2. A nested subdomain'

nested="$(api POST "/api/v1/websites/$PARENT/subdomains" '{"name":"shop"}')"
NESTED_ID="$(json_field "$nested" 'id')"
if [ -z "$NESTED_ID" ]; then
  fail "create the nested subdomain (got: $(printf '%s' "$nested" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

contains 'the name is derived from the parent' "$nested" "\"primary_domain\":\"$NESTED\""
contains 'it is linked to its parent' "$nested" "\"parent_website_id\":\"$PARENT\""
contains 'it inherits the parent account by default' "$nested" '"system_user_mode":"inherit"'
contains 'its files sit inside the parent directory' "$nested" \
  "\"document_root\":\"/var/www/$PARENT_DOMAIN/$NESTED/public\""
contains 'it shares the parent system account' "$nested" "\"system_user\":\"$PARENT_USER\""

state="$(await_active "$NESTED_ID")"
if [ "$state" = "active" ]; then
  pass 'the nested subdomain is active'
else
  fail "the nested subdomain is active (it is $state)"
fi

# What is actually on the host.
if [ -d "/var/www/$PARENT_DOMAIN/$NESTED/public" ]; then
  pass 'its document root exists on the host'
else
  fail 'its document root exists on the host'
fi

# Beside the parent's content, not inside it: inside would mean the parent
# serves the subdomain's files at its own URLs, so a config.php under the
# subdomain would be readable at the parent's address.
if [ -d "/var/www/$PARENT_DOMAIN/public/$NESTED" ]; then
  fail 'its files are outside the parent document root'
else
  pass 'its files are outside the parent document root'
fi

# Its own logs, which is what makes a subdomain's traffic separable at all.
if [ -d "/var/www/$PARENT_DOMAIN/$NESTED/logs" ]; then
  pass 'it has its own access and error logs'
else
  fail 'it has its own access and error logs'
fi

if [ -f "$SITES_DIR/$NESTED.conf" ]; then
  pass 'it has a vhost of its own, not an alias on the parent'
else
  fail 'it has a vhost of its own, not an alias on the parent'
fi

# The parent's vhost must not have gained the subdomain's name: two server
# blocks answering to one name is a configuration nginx warns about and
# resolves by picking one, which is not a decision the panel should leave to it.
parent_conf="$(cat "$SITES_DIR/$PARENT_DOMAIN.conf" 2>/dev/null || true)"
if [ -z "$parent_conf" ]; then
  # An empty read would make the next check pass for the wrong reason.
  fail 'the parent vhost is readable'
else
  pass 'the parent vhost is readable'
  not_contains 'the parent vhost does not also claim the name' "$parent_conf" "$NESTED"
fi

body="$(await_body "$NESTED" 'html')"
contains 'the subdomain is served on its own name' "$body" "$NESTED"

parent_body="$(await_body "$PARENT_DOMAIN" 'html')"
contains 'the parent still serves its own content' "$parent_body" "$PARENT_DOMAIN"
not_contains 'the parent is not serving the subdomain page' "$parent_body" "$NESTED"

# --- 3. an isolated subdomain with its own account ----------------------

log ''
log '3. An isolated subdomain with its own account'

isolated="$(api POST "/api/v1/websites/$PARENT/subdomains" \
  '{"name":"app","document_root_mode":"isolated","php_pool_mode":"dedicated","system_user_mode":"dedicated"}')"
ISOLATED_ID="$(json_field "$isolated" 'id')"

contains 'its files sit in a directory of their own' "$isolated" \
  "\"document_root\":\"/var/www/$ISOLATED/public\""
not_contains 'it does not share the parent account' "$isolated" "\"system_user\":\"$PARENT_USER\""

ISOLATED_USER="$(printf '%s' "$isolated" | tr '{' '\n' | grep -F "\"primary_domain\":\"$ISOLATED\"" |
  sed -n 's/.*"system_user":"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$ISOLATED_USER" ]; then
  ISOLATED_USER="$(json_field "$isolated" 'system_user')"
fi

state="$(await_active "$ISOLATED_ID")"
if [ "$state" = "active" ]; then
  pass 'the isolated subdomain is active'
else
  fail "the isolated subdomain is active (it is $state)"
fi

if id "$ISOLATED_USER" >/dev/null 2>&1; then
  pass 'its own system account exists on the host'
else
  fail "its own system account exists on the host ($ISOLATED_USER)"
fi

# Isolation is the point of choosing this: the parent's account must not own
# the subdomain's files.
owner="$(stat -c '%U' "/var/www/$ISOLATED/public" 2>/dev/null || true)"
if [ "$owner" = "$ISOLATED_USER" ]; then
  pass 'its files belong to its own account'
else
  fail "its files belong to its own account (owned by $owner)"
fi

body="$(await_body "$ISOLATED" 'html')"
contains 'the isolated subdomain is served on its own name' "$body" "$ISOLATED"

# A dedicated account with the parent's pool would run PHP as the parent's user
# over files owned by the subdomain's: every write fails, and it reads as a
# broken application rather than a bad configuration.
code="$(api_status POST "/api/v1/websites/$PARENT/subdomains" \
  '{"name":"bad","php_pool_mode":"inherit","system_user_mode":"dedicated"}')"
if [ "$code" = "409" ] || [ "$code" = "422" ]; then
  pass 'an own account with the parent pool is refused'
else
  fail "an own account with the parent pool is refused (got HTTP $code)"
fi

# --- 4. a wildcard, and catch-all routing -------------------------------

log ''
log '4. Wildcard and catch-all'

wildcard="$(api POST "/api/v1/websites/$PARENT/subdomains" '{"name":"*"}')"
WILDCARD_ID="$(json_field "$wildcard" 'id')"

contains 'the wildcard name is recorded as it will be served' "$wildcard" \
  "\"primary_domain\":\"$WILDCARD\""
# The asterisk is a glob to every tool that later walks this directory — a
# backup, an archive, an rsync — so it must not reach the filesystem.
not_contains 'the wildcard never reaches the filesystem' "$wildcard" '/var/www/*'
contains 'its directory is named literally' "$wildcard" "_wildcard.$PARENT_DOMAIN"

state="$(await_active "$WILDCARD_ID")"
if [ "$state" = "active" ]; then
  pass 'the wildcard subdomain is active'
else
  fail "the wildcard subdomain is active (it is $state)"
fi

if [ -f "$SITES_DIR/_wildcard.$PARENT_DOMAIN.conf" ]; then
  pass 'its vhost file carries no asterisk in its name'
else
  fail 'its vhost file carries no asterisk in its name'
fi

wildcard_conf="$(cat "$SITES_DIR/_wildcard.$PARENT_DOMAIN.conf" 2>/dev/null || true)"
contains 'the vhost itself serves the wildcard name' "$wildcard_conf" "server_name $WILDCARD;"

# The point of a wildcard: a name nobody registered is still served.
body="$(await_body "anything.$PARENT_DOMAIN" 'html')"
contains 'a name nothing claims is caught by the wildcard' "$body" "$WILDCARD"

# And the point of nginx's matching order: an exact name still wins. If it did
# not, creating a wildcard would take over every subdomain already hosted.
body="$(await_body "$NESTED" 'html')"
contains 'an exact subdomain still beats the wildcard' "$body" "$NESTED"

body="$(await_body "$PARENT_DOMAIN" 'html')"
contains 'the parent is not caught by its own wildcard' "$body" "$PARENT_DOMAIN"

# --- 5. what the panel says ---------------------------------------------

log ''
log '5. The panel record'

subdomains="$(api GET "/api/v1/websites/$PARENT/subdomains")"
contains 'the parent lists the nested subdomain' "$subdomains" "\"primary_domain\":\"$NESTED\""
contains 'the parent lists the isolated subdomain' "$subdomains" "\"primary_domain\":\"$ISOLATED\""
contains 'the parent lists the wildcard' "$subdomains" "\"primary_domain\":\"$WILDCARD\""

listing="$(api GET /api/v1/websites)"
not_contains 'the websites listing leaves subdomains out by default' "$listing" \
  "\"primary_domain\":\"$NESTED\""
contains 'the parent is still in the websites listing' "$listing" \
  "\"primary_domain\":\"$PARENT_DOMAIN\""

listing="$(api GET '/api/v1/websites?include_subdomains=true')"
contains 'asking for them includes them' "$listing" "\"primary_domain\":\"$NESTED\""

# --- 6. what is refused --------------------------------------------------

log ''
log '6. Refusals'

# One level is the whole model. A name several levels down is created as a
# subdomain of the top-level site with a dotted label instead.
code="$(api_status POST "/api/v1/websites/$NESTED_ID/subdomains" '{"name":"dev"}')"
expect_status 'a subdomain of a subdomain is refused' 400 "$code"

deeper="$(api POST "/api/v1/websites/$PARENT/subdomains" '{"name":"dev.shop"}')"
contains 'a deeper name is created under the top-level site' "$deeper" \
  "\"primary_domain\":\"dev.shop.$PARENT_DOMAIN\""
DEEPER_ID="$(json_field "$deeper" 'id')"
await_active "$DEEPER_ID" >/dev/null
remove_site "dev.shop.$PARENT_DOMAIN" subdomain

for bad in 'sh op' '-bad' 'shop.*'; do
  code="$(api_status POST "/api/v1/websites/$PARENT/subdomains" "{\"name\":\"$bad\"}")"
  if [ "$code" = "422" ] || [ "$code" = "400" ]; then
    pass "an unusable name is refused: $bad"
  else
    fail "an unusable name is refused: $bad (got HTTP $code)"
  fi
done

code="$(api_status POST "/api/v1/websites/$PARENT/subdomains" '{"name":"shop"}')"
if [ "$code" = "409" ]; then
  pass 'a name already hosted is refused'
else
  fail "a name already hosted is refused (got HTTP $code)"
fi

code="$(api_status POST "/api/v1/websites/$PARENT/subdomains" '{"name":"x","surprise":true}')"
expect_status 'an unknown field is refused rather than ignored' 400 "$code"

# Deleting the parent would take the nested subdomain's files with it while its
# vhost stayed live: the panel would have no record of a name nginx still
# serves.
code="$(api_status DELETE "/api/v1/websites/$PARENT")"
expect_status 'deleting a website with subdomains is refused' 409 "$code"

# A subdomain is not deleted through the website endpoint: the two differ on
# whether the account goes with the site.
code="$(api_status DELETE "/api/v1/subdomains/$PARENT")"
expect_status 'a top-level website is not deletable as a subdomain' 400 "$code"

# --- 7. removal ----------------------------------------------------------

log ''
log '7. Removal'

remove_site "$WILDCARD" subdomain
body="$(await_gone "anything.$PARENT_DOMAIN" "$WILDCARD")"
not_contains 'the wildcard stops catching names' "$body" "$WILDCARD"

remove_site "$NESTED" subdomain
if [ -f "$SITES_DIR/$NESTED.conf" ]; then
  fail 'the subdomain vhost is removed'
else
  pass 'the subdomain vhost is removed'
fi

# The account belongs to the parent. Removing it with the subdomain would leave
# the parent's files owned by a user that no longer exists — a 403 for every
# visitor to a site nobody touched.
if id "$PARENT_USER" >/dev/null 2>&1; then
  pass 'the shared account survives the subdomain'
else
  fail 'the shared account survives the subdomain'
fi

body="$(await_body "$PARENT_DOMAIN" 'html')"
contains 'the parent is still served afterwards' "$body" "$PARENT_DOMAIN"

remove_site "$ISOLATED" subdomain
if id "$ISOLATED_USER" >/dev/null 2>&1; then
  fail 'a subdomain with its own account takes it with it'
else
  pass 'a subdomain with its own account takes it with it'
fi

# With nothing beneath it, the parent can be deleted.
code="$(api_status DELETE "/api/v1/websites/$PARENT")"
expect_status 'the parent is deletable once it is empty' 202 "$code"

purge

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 4.1 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
