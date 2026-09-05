#!/bin/sh
# Phase 5 Docker integration test — PHP Manager.
#
# Black-box checks against the running stack. The acceptance criterion from
# TASKS.md is in the middle of this file: a website on each of three PHP
# versions, each actually executing that version. Around it are the checks that
# the isolation and hardening a hosting panel promises are really in place.
#
# Run with:  make docker-test-php

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
SITES_BASE_URL="${SITES_BASE_URL:-http://agent:80}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# The versions the agent image ships. Each gets a website.
VERSIONS="${PHP_VERSIONS:-8.2 8.3 8.4}"
DOMAIN_SUFFIX="phase5.integration.test"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1
fi

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

site_status() {
  host="$1"; path="${2:-/}"
  curl -s -o /dev/null -w '%{http_code}' --max-time 30 \
    -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
}

site_body() {
  host="$1"; path="${2:-/}"
  curl -s --max-time 30 -H "Host: $host" "$SITES_BASE_URL$path" 2>/dev/null || true
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

# own_as gives a file the same owner and group as a reference directory.
#
# BusyBox chown has no --reference, and a file left owned by root is unreadable
# by the pool, which FPM reports as a bare "Access denied." — a confusing way
# to discover the test set the file up wrong.
own_as() {
  reference="$1"; target="$2"; mode="${3:-640}"
  owner="$(stat -c '%U:%G' "$reference" 2>/dev/null || echo '')"
  if [ -n "$owner" ]; then
    chown "$owner" "$target" 2>/dev/null || true
  fi
  chmod "$mode" "$target"
}

json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 120 ]; do
    state="$(json_field "$(api GET "/api/v1/jobs/$job_id")" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

log "Phase 5 PHP checks"
log "API:   $API_BASE_URL"
log "Sites: $SITES_BASE_URL"
# This runs inside the agent container, which is the managed host in
# development: the execution checks below write into a site's document root the
# way a deployment would, which a separate container could not do.
log ""

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

# refresh_token signs in again if the current token has aged out.
#
# This suite provisions and reloads PHP across three versions, which takes long
# enough that an access token issued at the start can expire before the last
# checks run — turning real results into a wall of 401s.
refresh_token() {
  code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    -H "Authorization: Bearer $token" "$API_BASE_URL/api/v1/auth/me" 2>/dev/null || true)"
  if [ "$code" = "401" ]; then
    login
  fi
}

login

if [ -z "$token" ]; then
  log "FAILED: could not sign in as $ADMIN_USER"
  exit 1
fi

# --------------------------------------------------------------- clean slate

for version in $VERSIONS; do
  domain="php$(printf '%s' "$version" | tr -d '.').$DOMAIN_SUFFIX"
  stale="$(api GET /api/v1/websites | tr '{' '\n' |
    grep -F "\"primary_domain\":\"$domain\"" |
    sed -n 's/.*"id":"\([0-9a-f-]*\)".*/\1/p' | head -n 1)"
  if [ -n "$stale" ]; then
    job="$(json_field "$(api DELETE "/api/v1/websites/$stale?remove_files=true")" id)"
    [ -n "$job" ] && await_job "$job" >/dev/null
  fi
done

# ------------------------------------------------------------ authorization

log "Authorization"
expect_status "GET /php/versions without a token returns 401" 401 \
  "$(api_status GET /api/v1/php/versions '' '')"
expect_status "POST /php/versions/install without a token returns 401" 401 \
  "$(api_status POST /api/v1/php/versions/install '{"version":"8.3"}' '')"
expect_status "GET /php/versions with a valid token returns 200" 200 \
  "$(api_status GET /api/v1/php/versions)"

# --------------------------------------------------------------- validation

log ""
log "Input validation"
# The version reaches a package name and a configuration path, so anything
# that is not a bare major.minor must never get that far.
expect_status "a patch level is refused"       400 "$(api_status GET /api/v1/php/versions/8.3.19)"
expect_status "a word is refused"              400 "$(api_status GET /api/v1/php/versions/latest)"
expect_status "a shell payload is refused"     400 \
  "$(api_status GET '/api/v1/php/versions/8.3%3Brm%20-rf%20%2F')"
expect_status "a traversal is refused"         400 \
  "$(api_status GET '/api/v1/php/versions/..%2F..%2Fetc')"
expect_status "an unknown version is not found" 404 "$(api_status GET /api/v1/php/versions/5.6)"
expect_status "a malformed install is refused" 422 \
  "$(api_status POST /api/v1/php/versions/install '{"version":"latest"}')"
expect_status "a misspelled field is reported" 400 \
  "$(api_status POST /api/v1/php/versions/install '{"verison":"8.3"}')"

# ------------------------------------------------------------------ versions

log ""
log "Version detection"
versions="$(api GET /api/v1/php/versions)"
contains "the panel reports detected versions" "$versions" '"versions"'

for version in $VERSIONS; do
  contains "PHP $version was detected" "$versions" "\"version\":\"$version\""
done

# A version is only offered because a binary answered, not because a row says
# so: a panel offering a version nothing can run produces sites that 502.
contains "detected versions carry a binary path" "$versions" '"binary_path":"/usr/sbin/php-fpm'

# --------------------------------------- the acceptance criterion (TASKS.md)

log ""
log "A website per PHP version (TASKS.md Phase 5 acceptance)"
refresh_token

for version in $VERSIONS; do
  compact="$(printf '%s' "$version" | tr -d '.')"
  domain="php$compact.$DOMAIN_SUFFIX"

  created="$(api POST /api/v1/websites "{\"domain\":\"$domain\"}")"
  website_id="$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')"
  create_job="$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

  if [ -z "$website_id" ]; then
    fail "could not create $domain: $(printf '%s' "$created" | head -c 200)"
    continue
  fi

  state="$(await_job "$create_job")"
  if [ "$state" != "SUCCESS" ]; then
    fail "provisioning $domain ended $state"
    continue
  fi

  # A new site is static, so its PHP panel must say so rather than erroring.
  php_state="$(api GET "/api/v1/websites/$website_id/php")"
  contains "$domain starts as a static site" "$php_state" '"enabled":false'

  set_body="{\"version\":\"$version\"}"
  set_response="$(api PATCH "/api/v1/websites/$website_id/php" "$set_body")"
  set_job="$(printf '%s' "$set_response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

  if [ -z "$set_job" ]; then
    fail "could not set PHP $version on $domain: $(printf '%s' "$set_response" | head -c 200)"
    continue
  fi

  state="$(await_job "$set_job")"
  if [ "$state" = "SUCCESS" ]; then
    pass "PHP $version was enabled on $domain"
  else
    detail="$(api GET "/api/v1/jobs/$set_job")"
    fail "enabling PHP $version ended $state: $(printf '%s' "$detail" | head -c 240)"
    continue
  fi

  # The panel must now agree that the site runs this version.
  php_state="$(api GET "/api/v1/websites/$website_id/php")"
  contains "$domain reports PHP $version" "$php_state" "\"php_version\":\"$version\""

  # The pool socket carries the version, so switching never has two FPM
  # masters contending for one path.
  contains "$domain has a version-specific socket" "$php_state" "\"socket_path\":\"/run/php-fpm/"

  eval "WEBSITE_ID_$compact=\$website_id"
done

# This is the heart of the acceptance criterion. A probe script reports the
# version PHP itself is running, so the check is what executes rather than what
# was configured.
#
# The script runs inside the agent container, which is the managed host, so it
# can write into a site's document root the way a deployment would.
log ""
log "PHP is actually executing"
refresh_token

for version in $VERSIONS; do
  compact="$(printf '%s' "$version" | tr -d '.')"
  domain="php$compact.$DOMAIN_SUFFIX"
  root="/var/www/$domain/public"

  if [ ! -d "$root" ]; then
    fail "$domain has no document root at $root"
    continue
  fi

  # Owned by the site and readable by the web server group, exactly as a
  # deployed file would be.
  printf '%s' '<?php echo "RUNNING ", PHP_MAJOR_VERSION, ".", PHP_MINOR_VERSION; ?>'     > "$root/probe.php"
  own_as "$root" "$root/probe.php"

  body="$(site_body "$domain" "/probe.php")"
  contains "$domain executes PHP $version" "$body" "RUNNING $version"
  not_contains "$domain never returns PHP source" "$body" "<?php"
done

log ""
log "PHP hardening"

first_version="$(printf '%s' "$VERSIONS" | cut -d' ' -f1)"
first_compact="$(printf '%s' "$first_version" | tr -d '.')"
harden_domain="php$first_compact.$DOMAIN_SUFFIX"
harden_root="/var/www/$harden_domain/public"

if [ -d "$harden_root" ]; then
  # The classic path-info remote code execution: upload an image containing
  # PHP, then request it with /x.php appended. Without try_files before
  # fastcgi_pass, FPM walks back to the real file and executes it.
  mkdir -p "$harden_root/uploads"
  printf '%s' '<?php echo "EXECUTED"; ?>' > "$harden_root/uploads/avatar.jpg"
  own_as "$harden_root" "$harden_root/uploads" 750
  own_as "$harden_root" "$harden_root/uploads/avatar.jpg"

  attack="$(site_body "$harden_domain" "/uploads/avatar.jpg/x.php")"
  not_contains "an uploaded file is not executed as PHP" "$attack" "EXECUTED"
  expect_status "the path-info attack is refused" 404     "$(site_status "$harden_domain" "/uploads/avatar.jpg/x.php")"

  # open_basedir is the backstop for a path traversal in application code.
  printf '%s' '<?php echo @file_get_contents("/etc/passwd") ?: "DENIED"; ?>'     > "$harden_root/escape.php"
  own_as "$harden_root" "$harden_root/escape.php"
  contains "PHP cannot read outside the site"     "$(site_body "$harden_domain" "/escape.php")" "DENIED"

  # A panel that leaves these enabled gives every site shell access.
  printf '%s' '<?php echo function_exists("shell_exec") ? "SHELL" : "NO-SHELL"; ?>'     > "$harden_root/shell.php"
  own_as "$harden_root" "$harden_root/shell.php"
  contains "shell functions are disabled"     "$(site_body "$harden_domain" "/shell.php")" "NO-SHELL"

  # Dotfiles stay denied whether or not PHP is on.
  expect_status "a dotfile is denied on a PHP site" 403     "$(site_status "$harden_domain" "/.env")"

  # Each pool runs as its own account, so one site cannot read another's files.
  other_compact="$(printf '%s' "$VERSIONS" | cut -d' ' -f2 | tr -d '.')"
  if [ -n "$other_compact" ] && [ "$other_compact" != "$first_compact" ]; then
    printf '%s' "<?php echo @file_get_contents(\"/var/www/php$other_compact.$DOMAIN_SUFFIX/public/index.html\") ?: \"ISOLATED\"; ?>"       > "$harden_root/neighbour.php"
    own_as "$harden_root" "$harden_root/neighbour.php"
    contains "one site cannot read another"       "$(site_body "$harden_domain" "/neighbour.php")" "ISOLATED"
  fi
fi

# ----------------------------------------------------------------- switching

log ""
log "Switching versions"
refresh_token

first="$(printf '%s' "$VERSIONS" | cut -d' ' -f1)"
last="$(printf '%s' "$VERSIONS" | rev | cut -d' ' -f1 | rev)"
compact="$(printf '%s' "$first" | tr -d '.')"
# Defaulted: a creation that failed earlier must report a failed check, not
# abort the whole script with an unset-variable error under `set -u`.
eval "switch_id=\${WEBSITE_ID_$compact:-}"
switch_domain="php$compact.$DOMAIN_SUFFIX"

if [ -n "${switch_id:-}" ]; then
  response="$(api PATCH "/api/v1/websites/$switch_id/php" "{\"version\":\"$last\"}")"
  job="$(printf '%s' "$response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
  state="$(await_job "$job")"

  if [ "$state" = "SUCCESS" ]; then
    pass "$switch_domain switched from $first to $last"
  else
    detail="$(api GET "/api/v1/jobs/$job")"
    fail "switching ended $state: $(printf '%s' "$detail" | head -c 240)"
  fi

  php_state="$(api GET "/api/v1/websites/$switch_id/php")"
  contains "the pool now reports $last" "$php_state" "\"php_version\":\"$last\""
  # One pool per site: the website_id column is unique, so a switch replaces
  # rather than adding a second pool racing for the same socket.
  expect_status "the site is still served" 200 "$(site_status "$switch_domain")"

  # A switch that lands correctly can still drop requests while it happens.
  #
  # An nginx reload is graceful: the workers started under the old vhost keep
  # serving until their connections end, and they are still passing to the old
  # socket. Removing that pool the moment the reload returns unlinked their
  # upstream underneath them, which showed up as a burst of 502s on a site that
  # was never actually down. The site is probed continuously across a switch
  # here so that regression cannot come back unnoticed.
  rm -f /tmp/phase5_switch_done /tmp/phase5_switch_codes
  : > /tmp/phase5_switch_codes
  (
    while [ ! -f /tmp/phase5_switch_done ]; do
      printf '%s\n' "$(site_status "$switch_domain" "/probe.php")" \
        >> /tmp/phase5_switch_codes
    done
  ) &
  probe_pid=$!

  response="$(api PATCH "/api/v1/websites/$switch_id/php" "{\"version\":\"$first\"}")"
  job="$(printf '%s' "$response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
  state="$(await_job "$job")"

  touch /tmp/phase5_switch_done
  wait "$probe_pid" 2>/dev/null || true

  probes="$(wc -l < /tmp/phase5_switch_codes | tr -d ' ')"
  dropped="$(grep -v '^200$' /tmp/phase5_switch_codes | wc -l | tr -d ' ')"
  seen="$(sort -u /tmp/phase5_switch_codes | tr '\n' ' ')"
  rm -f /tmp/phase5_switch_done /tmp/phase5_switch_codes

  if [ "$state" != "SUCCESS" ]; then
    fail "switching back to $first ended $state"
  elif [ "$probes" -lt 5 ]; then
    fail "the switch was not probed enough times to judge it ($probes probes)"
  elif [ "$dropped" = "0" ]; then
    pass "the site served every one of $probes requests during a switch"
  else
    fail "$dropped of $probes requests were dropped during a switch (codes seen: $seen)"
  fi

  # And it really did switch back, so the check above was not measuring a
  # request that never changed anything.
  php_state="$(api GET "/api/v1/websites/$switch_id/php")"
  contains "the seamless switch landed on $first" "$php_state" "\"php_version\":\"$first\""
fi

# --------------------------------------------------------------- php.ini

log ""
log "Per-site configuration"
refresh_token

if [ -n "${switch_id:-}" ]; then
  config="$(api GET "/api/v1/websites/$switch_id/php/config")"
  contains "the config reports a memory limit" "$config" '"memory_limit"'
  contains "the config reports opcache"        "$config" '"opcache"'

  expect_status "a valid configuration is accepted" 202 \
    "$(api_status PATCH "/api/v1/websites/$switch_id/php/config" \
       '{"memory_limit":"512M","max_execution_time":60,"opcache":false}')"

  # These strings are written into an FPM pool file, where a newline would let
  # a caller append directives of their choosing.
  expect_status "an injected memory limit is refused" 422 \
    "$(api_status PATCH "/api/v1/websites/$switch_id/php/config" \
       '{"memory_limit":"512M\nuser = root"}')"
  expect_status "an unbounded memory limit is refused" 422 \
    "$(api_status PATCH "/api/v1/websites/$switch_id/php/config" \
       '{"memory_limit":"9999G"}')"
  expect_status "an unbounded execution time is refused" 422 \
    "$(api_status PATCH "/api/v1/websites/$switch_id/php/config" \
       '{"max_execution_time":100000}')"
fi

# ------------------------------------------------------------------ removal

log ""
log "Version removal"
refresh_token

# Removing a version websites still run would take every one of them offline.
#
# The request is only issued when the version really is in use. Sending it
# unconditionally would, if an earlier step had failed and left nothing using
# the version, actually uninstall PHP from the host — a test must not remove
# software from the machine it is testing.
in_use="$(api GET "/api/v1/php/versions/$last" |
  sed -n 's/.*"in_use":\([0-9]*\).*/\1/p' | head -n 1)"

if [ "${in_use:-0}" -gt 0 ]; then
  expect_status "a version in use cannot be removed" 409 \
    "$(api_status DELETE "/api/v1/php/versions/$last")"
else
  fail "PHP $last is used by no website, so the refusal could not be checked"
fi

# Removing a version the host does not have must reconcile, not fail.
#
# The package manager refuses to remove a package it never installed, so this
# used to fail — which left a version whose installation had failed pinned in
# the panel, because the only action that could clear it was the one that would
# not run. 8.5 is safe to ask for here precisely because it is not installed:
# nothing is taken off the machine running the test.
absent="8.5"
if printf '%s' "$versions" | grep -q "\"version\":\"$absent\",\"binary_path\":\"/"; then
  log "  SKIP  PHP $absent is installed here, so its removal is not a no-op"
else
  removal="$(api DELETE "/api/v1/php/versions/$absent")"
  job="$(printf '%s' "$removal" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
  if [ -z "$job" ]; then
    fail "removing an absent version was not accepted: $(printf '%s' "$removal" | head -c 200)"
  else
    state="$(await_job "$job")"
    if [ "$state" = "SUCCESS" ]; then
      pass "removing a version that is not installed succeeds"
    else
      fail "removing an absent version ended $state"
    fi

    # And it settles: a finished removal must not leave the panel showing one
    # still in progress.
    after="$(api GET "/api/v1/php/versions/$absent")"
    not_contains "the removed version is not left mid-operation" "$after" '"status":"removing"'
  fi
fi

# --------------------------------------------------------------- turning off

log ""
log "Turning PHP off"
refresh_token

if [ -n "${switch_id:-}" ]; then
  response="$(api PATCH "/api/v1/websites/$switch_id/php" '{"version":null}')"
  job="$(printf '%s' "$response" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"

  if [ -z "$job" ]; then
    fail "disabling PHP returned no job: $(printf '%s' "$response" | head -c 200)"
  else
    state="$(await_job "$job")"
    if [ "$state" = "SUCCESS" ]; then
      pass "PHP was disabled on $switch_domain"
    else
      fail "disabling PHP ended $state"
    fi

    php_state="$(api GET "/api/v1/websites/$switch_id/php")"
    contains "the site reports PHP as off" "$php_state" '"enabled":false'

    # A site that had PHP switched off still has its config.php with the
    # database password in it. Serving it as source would hand that over.
    expect_status "a .php file is denied rather than served as source" 403 \
      "$(site_status "$switch_domain" "/index.php")"
    expect_status "the static site still serves" 200 "$(site_status "$switch_domain")"
  fi
fi

# ------------------------------------------------------------------ cleanup

log ""
log "Cleanup"
refresh_token
for version in $VERSIONS; do
  compact="$(printf '%s' "$version" | tr -d '.')"
  eval "site_id=\${WEBSITE_ID_$compact:-}"
  [ -z "$site_id" ] && continue

  job="$(printf '%s' "$(api DELETE "/api/v1/websites/$site_id?remove_files=true")" |
    sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
  [ -n "$job" ] && await_job "$job" >/dev/null
done
pass "test websites were removed"

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 5 PHP checks passed"
