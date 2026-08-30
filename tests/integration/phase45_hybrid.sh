#!/bin/sh
# Phase 4.5 Docker integration test — the Apache hybrid engine.
#
# Black-box checks against the running stack. The acceptance is not that the API
# returns 202: it is that after switching arrangement the same site still serves
# the same content, now through Apache — that .htaccess is read, that Apache
# sees the real visitor address rather than the proxy, that Apache is not
# reachable from outside the host, and that switching back leaves the site
# exactly as it was.
#
# It runs inside the agent container, which is the managed host in development,
# so it can look at the configuration files, the processes and the logs.
#
# Run with:  make docker-test-hybrid

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

SITE_DOMAIN="${SITE_DOMAIN:-p45.integration.test}"
APACHE_CONF_DIR="${AGENT_APACHE_CONF_DIR:-/etc/apache2/conf.d}"
NGINX_SITES_DIR="${AGENT_NGINX_SITES_DIR:-/etc/nginx/conf.d}"

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
    curl -s --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 600 -X "$method" "$API_BASE_URL$path" \
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

json_number() {
  printf '%s' "$1" | awk -v key="\"$2\":" '
    {
      at = index($0, key)
      if (at == 0) { exit }
      rest = substr($0, at + length(key))
      n = ""
      for (i = 1; i <= length(rest); i++) {
        c = substr(rest, i, 1)
        if (c >= "0" && c <= "9") { n = n c } else { break }
      }
      print n
    }'
}

site_id() {
  printf '%s' "$1" | tr '{' '\n' | grep -F "\"primary_domain\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# await_body PATTERN — fetch the site until the body contains PATTERN.
#
# An nginx reload is graceful and an Apache graceful restart is too: both let
# the old workers finish what they are serving. A check firing the instant the
# panel returns can legitimately see the previous configuration.
await_body() {
  await_pattern="$1"
  await_waited=0
  while [ "$await_waited" -lt 30 ]; do
    await_out="$(curl -s --max-time 20 -H "Host: $SITE_DOMAIN" http://127.0.0.1/ 2>/dev/null || true)"
    case "$await_out" in
      *"$await_pattern"*) printf '%s' "$await_out"; return ;;
    esac
    sleep 1
    await_waited=$((await_waited + 1))
  done
  printf '%s' "$await_out"
}

# await_mode MODE — wait for the panel to report an arrangement.
await_mode() {
  await_want="$1"
  await_waited=0
  while [ "$await_waited" -lt 60 ]; do
    await_state="$(json_field "$(api GET /api/v1/webserver)" 'mode')"
    if [ "$await_state" = "$await_want" ]; then
      printf '%s' "$await_state"
      return
    fi
    sleep 2
    await_waited=$((await_waited + 2))
  done
  printf '%s' "$await_state"
}

# await_jobs — wait for every queued website job to finish.
#
# A mode change queues one job per website. Checking the host before they have
# run would be checking the arrangement the host had a moment ago.
await_jobs() {
  await_waited=0
  while [ "$await_waited" -lt 120 ]; do
    await_pending="$(api GET '/api/v1/jobs?status=PENDING' | grep -c '"type":"website.update"' || true)"
    await_running="$(api GET '/api/v1/jobs?status=RUNNING' | grep -c '"type":"website.update"' || true)"
    if [ "${await_pending:-0}" = "0" ] && [ "${await_running:-0}" = "0" ]; then
      return
    fi
    sleep 2
    await_waited=$((await_waited + 2))
  done
}

set_mode() {
  api PUT /api/v1/webserver "{\"mode\":\"$1\"}" >/dev/null
  await_mode "$1" >/dev/null
  await_jobs
}

purge() {
  purge_site="$(site_id "$(api GET '/api/v1/websites?include_subdomains=true')" "$SITE_DOMAIN")"
  if [ -n "$purge_site" ]; then
    api DELETE "/api/v1/websites/$purge_site" >/dev/null 2>&1 || true
    waited=0
    while [ "$waited" -lt 40 ]; do
      if [ -z "$(site_id "$(api GET '/api/v1/websites')" "$SITE_DOMAIN")" ]; then
        break
      fi
      sleep 2
      waited=$((waited + 2))
    done
  fi
  rm -rf "/var/www/$SITE_DOMAIN"
  rm -f "$APACHE_CONF_DIR/jothost-$SITE_DOMAIN.conf" "$NGINX_SITES_DIR/$SITE_DOMAIN.conf"
}

# ------------------------------------------------------------------ the run

log 'Phase 4.5 — Apache hybrid engine'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# The host starts from nginx alone whatever a previous run left behind.
set_mode nginx
purge

# --- 1. the arrangement --------------------------------------------------

log ''
log '1. The arrangement'

status="$(api GET /api/v1/webserver)"
contains 'the panel reports the arrangement' "$status" '"mode":"nginx"'
contains 'it reports what Apache is doing' "$status" '"apache":'

case "$status" in
  *'"available":true'*) ;;
  *)
    # Installing is part of the feature, and a bare host is where a real
    # operator starts, so the suite installs it rather than requiring it.
    log '  ...installing Apache, which this host does not have yet'
    api POST /api/v1/webserver/apache/install '' >/dev/null
    status="$(api GET /api/v1/webserver)"
    ;;
esac

contains 'Apache is available on this host' "$status" '"available":true'
contains 'the panel knows its version' "$status" '"version":"2.'

# --- 2. a site served by nginx alone -------------------------------------

log ''
log '2. A site, served by nginx'

created="$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\"}")"
SITE="$(json_field "$created" 'id')"
if [ -z "$SITE" ]; then
  fail "create the website (got: $(printf '%s' "$created" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

waited=0
while [ "$waited" -lt 90 ]; do
  site="$(api GET "/api/v1/websites/$SITE")"
  if [ "$(json_field "$site" 'status')" = "active" ]; then
    break
  fi
  sleep 2
  waited=$((waited + 2))
done

if [ "$(json_field "$site" 'status')" = "active" ]; then
  pass 'the website is active'
else
  fail "the website is active (it is $(json_field "$site" 'status'))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

OWNER="$(json_field "$site" 'system_user')"
ROOT="/var/www/$SITE_DOMAIN/public"
site_group="$(stat -c '%G' "$ROOT")"

# Content that says which server produced it, and a .htaccess that only Apache
# can act on. Both are written now, so nothing about the site changes when the
# arrangement does — only who serves it.
cat > "$ROOT/index.html" <<HTML
<!doctype html><html><body>SERVED $SITE_DOMAIN</body></html>
HTML

cat > "$ROOT/secret.txt" <<TXT
this file is denied by .htaccess
TXT

cat > "$ROOT/.htaccess" <<'HTACCESS'
# Only Apache reads this file. nginx never does.
<Files "secret.txt">
    Require all denied
</Files>
Redirect 302 /moved-away /
HTACCESS

chown "$OWNER":"$site_group" "$ROOT/index.html" "$ROOT/secret.txt" "$ROOT/.htaccess"
chmod 0640 "$ROOT/index.html" "$ROOT/secret.txt" "$ROOT/.htaccess"

body="$(await_body 'SERVED')"
contains 'the site serves its content' "$body" "SERVED $SITE_DOMAIN"

# nginx does not read .htaccess. The file is inert, which is the point of the
# comparison that follows.
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $SITE_DOMAIN" \
  http://127.0.0.1/secret.txt || true)"
expect_status 'nginx ignores .htaccess, so the file is served' 200 "$code"

if [ -f "$APACHE_CONF_DIR/jothost-$SITE_DOMAIN.conf" ]; then
  fail 'no Apache configuration exists while the host runs nginx alone'
else
  pass 'no Apache configuration exists while the host runs nginx alone'
fi

# --- 3. switching to hybrid ----------------------------------------------

log ''
log '3. Switching to nginx + Apache'

switched="$(api PUT /api/v1/webserver '{"mode":"hybrid"}')"
contains 'the switch is accepted and reports the new arrangement' "$switched" '"mode":"hybrid"'
contains 'it queues a rewrite per website' "$switched" '"type":"website.update"'

mode="$(await_mode hybrid)"
if [ "$mode" = "hybrid" ]; then
  pass 'the panel records the hybrid arrangement'
else
  fail "the panel records the hybrid arrangement (it says $mode)"
fi
await_jobs

site="$(api GET "/api/v1/websites/$SITE")"
PORT="$(json_number "$site" 'apache_port')"
if [ -n "$PORT" ] && [ "$PORT" -ge 7080 ] && [ "$PORT" -le 7979 ]; then
  pass "the site is assigned a backend port in the reserved range ($PORT)"
else
  fail "the site is assigned a backend port in the reserved range (got '$PORT')"
fi

if [ -f "$APACHE_CONF_DIR/jothost-$SITE_DOMAIN.conf" ]; then
  pass 'the site has an Apache virtual host'
else
  fail 'the site has an Apache virtual host'
fi

vhost="$(cat "$APACHE_CONF_DIR/jothost-$SITE_DOMAIN.conf" 2>/dev/null || true)"
contains 'the backend listens on the loopback only' "$vhost" "Listen 127.0.0.1:$PORT"
contains 'the vhost carries the site name' "$vhost" "ServerName $SITE_DOMAIN"
contains '.htaccess is enabled for the site' "$vhost" 'AllowOverride All'

# nginx must now be a proxy rather than a file server for this site: two
# servers both serving the files is how a .htaccess rule gets bypassed by
# whichever one answers first.
nginx_conf="$(cat "$NGINX_SITES_DIR/$SITE_DOMAIN.conf" 2>/dev/null || true)"
if [ -z "$nginx_conf" ]; then
  fail 'the nginx configuration is readable'
else
  pass 'the nginx configuration is readable'
  contains 'nginx proxies the site to the backend' "$nginx_conf" \
    "proxy_pass http://127.0.0.1:$PORT"
  contains 'nginx passes the real client address' "$nginx_conf" 'X-Forwarded-For'
fi

# --- 4. what the visitor sees --------------------------------------------

log ''
log '4. What the visitor sees'

body="$(await_body 'SERVED')"
contains 'the same content is still served' "$body" "SERVED $SITE_DOMAIN"

# The whole reason for this arrangement: a rule in a file the site's owner
# wrote is now enforced.
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $SITE_DOMAIN" \
  http://127.0.0.1/secret.txt || true)"
expect_status 'Apache enforces the .htaccess deny rule' 403 "$code"

code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $SITE_DOMAIN" \
  http://127.0.0.1/moved-away || true)"
expect_status 'Apache applies a .htaccess redirect' 302 "$code"

# The file that protects a directory must not be served by the server that
# reads it.
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $SITE_DOMAIN" \
  http://127.0.0.1/.htaccess || true)"
if [ "$code" = "403" ] || [ "$code" = "404" ]; then
  pass 'the .htaccess file itself is not servable'
else
  fail "the .htaccess file itself is not servable (got HTTP $code)"
fi

# --- 5. the backend is a backend -----------------------------------------

log ''
log '5. The backend is not a front end'

listening="$(netstat -ltn 2>/dev/null || ss -ltn 2>/dev/null || true)"
if printf '%s' "$listening" | grep -q "127.0.0.1:$PORT"; then
  pass 'the backend is listening on the loopback'
else
  fail "the backend is listening on the loopback (port $PORT not found)"
fi

# Bound to 127.0.0.1 rather than every address: on a real host the difference
# is whether the internet can reach Apache directly, bypassing nginx and its
# certificate.
if printf '%s' "$listening" | grep -E "(0\.0\.0\.0|:::)\:?$PORT" >/dev/null 2>&1; then
  fail 'the backend is reachable from outside the host'
else
  pass 'the backend is reachable from this host only'
fi

# Apache must not have taken port 80 from nginx.
apache_conf="$(cat /etc/apache2/httpd.conf 2>/dev/null || true)"
not_contains 'Apache does not hold the public port' "$apache_conf" '
Listen 80'

# The real client address, not the proxy. Without mod_remoteip every log line
# on every site would read 127.0.0.1.
curl -s -o /dev/null --max-time 10 -H "Host: $SITE_DOMAIN" \
  -H 'X-Forwarded-For: 203.0.113.42' http://127.0.0.1/ || true
sleep 1
access_log="$(tail -n 20 "/var/www/$SITE_DOMAIN/logs/apache-access.log" 2>/dev/null || true)"
if [ -z "$access_log" ]; then
  fail 'Apache writes its own access log for the site'
else
  pass 'Apache writes its own access log for the site'
  contains 'the log records the real client address' "$access_log" '203.0.113.42'
fi

# --- 6. PHP through the backend ------------------------------------------

log ''
log '6. PHP'

versions="$(api GET /api/v1/php/versions)"
case "$versions" in
  *'"installed":true'*)
    php_version="$(printf '%s' "$versions" | tr '{' '\n' | grep '"installed":true' |
      sed -n 's/.*"version":"\([^"]*\)".*/\1/p' | head -n 1)"

    cat > "$ROOT/info.php" <<'PHP'
<?php echo "PHP-VIA-", php_sapi_name(), "-", PHP_VERSION;
PHP
    chown "$OWNER":"$site_group" "$ROOT/info.php"
    chmod 0640 "$ROOT/info.php"

    api PATCH "/api/v1/websites/$SITE/php" "{\"version\":\"$php_version\"}" >/dev/null
    await_jobs

    waited=0
    while [ "$waited" -lt 30 ]; do
      php_body="$(curl -s --max-time 20 -H "Host: $SITE_DOMAIN" \
        http://127.0.0.1/info.php 2>/dev/null || true)"
      case "$php_body" in
        *PHP-VIA-*) break ;;
      esac
      sleep 2
      waited=$((waited + 2))
    done

    contains 'PHP runs behind Apache' "$php_body" 'PHP-VIA-fpm-fcgi'
    # Through FPM, not in the Apache process: mod_php would run every site's
    # code as one shared account.
    not_contains 'PHP does not run inside Apache itself' "$php_body" 'PHP-VIA-apache2handler'

    # And the site is still a site: choosing a PHP version must not have taken
    # it out of Apache, which is what a payload missing the backend port would
    # have done.
    site="$(api GET "/api/v1/websites/$SITE")"
    still="$(json_number "$site" 'apache_port')"
    if [ "$still" = "$PORT" ]; then
      pass 'setting a PHP version keeps the site in Apache'
    else
      fail "setting a PHP version keeps the site in Apache (port is now '$still')"
    fi
    ;;
  *)
    log '  ...skipped: no PHP version is installed on this host'
    ;;
esac

# --- 7. switching back ----------------------------------------------------

log ''
log '7. Switching back to nginx'

set_mode nginx

mode="$(json_field "$(api GET /api/v1/webserver)" 'mode')"
if [ "$mode" = "nginx" ]; then
  pass 'the panel records the nginx arrangement'
else
  fail "the panel records the nginx arrangement (it says $mode)"
fi

body="$(await_body 'SERVED')"
contains 'the site still serves its content' "$body" "SERVED $SITE_DOMAIN"

# nginx never reads .htaccess, so the rule stops applying — which is the
# honest consequence of the arrangement, and the reason the panel says so.
code="$(curl -s -o /dev/null -w '%{http_code}' -H "Host: $SITE_DOMAIN" \
  http://127.0.0.1/secret.txt || true)"
expect_status 'the .htaccess rule stops applying, as nginx does not read it' 200 "$code"

if [ -f "$APACHE_CONF_DIR/jothost-$SITE_DOMAIN.conf" ]; then
  fail 'the Apache virtual host is removed'
else
  pass 'the Apache virtual host is removed'
fi

# Apache with no vhost of ours has no listening socket and cannot start, so it
# is stopped rather than left "enabled" to fail at its next reload.
if [ -f /run/apache2/httpd.pid ] && [ -d "/proc/$(cat /run/apache2/httpd.pid 2>/dev/null)" ]; then
  fail 'Apache is stopped once no site uses it'
else
  pass 'Apache is stopped once no site uses it'
fi

# The port is kept, so switching back and forth does not renumber every
# backend on the host.
site="$(api GET "/api/v1/websites/$SITE")"
kept="$(json_number "$site" 'apache_port')"
if [ "$kept" = "$PORT" ]; then
  pass 'the site keeps its backend port for next time'
else
  fail "the site keeps its backend port for next time (was $PORT, now '$kept')"
fi

# --- 8. refusals ----------------------------------------------------------

log ''
log '8. Refusals'

code="$(api_status PUT /api/v1/webserver '{"mode":"apache"}')"
expect_status 'an arrangement that does not exist is refused' 422 "$code"

code="$(api_status PUT /api/v1/webserver '{"mode":"nginx"}')"
expect_status 'switching to the arrangement already in use is refused' 409 "$code"

code="$(api_status PUT /api/v1/webserver '{"mode":"hybrid","surprise":true}')"
expect_status 'an unknown field is refused rather than ignored' 400 "$code"

# A backend port is a host-wide resource, and a Node application asking for one
# has to see it.
api PUT /api/v1/webserver '{"mode":"hybrid"}' >/dev/null
await_mode hybrid >/dev/null
await_jobs

# The application manager has to be usable for this to mean anything: with no
# runtime installed the create is refused for that reason instead, and a check
# that passes because the feature is missing is worse than one that is skipped.
case "$(api GET /api/v1/node/versions)" in
  *'"available":true'*) ;;
  *) api POST /api/v1/node/versions/install '{"package":"nodejs"}' >/dev/null 2>&1 || true ;;
esac

case "$(api GET /api/v1/node/versions)" in
  *'"available":true'*)
    # PHP is turned off first, so the refusal that follows can only be about
    # the port. A site that serves PHP is refused an application for that
    # reason instead — which would make this check pass while proving nothing.
    api PATCH "/api/v1/websites/$SITE/php" '{"version":""}' >/dev/null 2>&1 || true
    await_jobs

    conflict="$(api POST /api/v1/node/apps "{\"website_id\":\"$SITE\",\"port\":$PORT}")"
    # The reason, not only the status: "port in use" has to be about the
    # Apache backend rather than about anything else that could refuse here.
    contains 'an application cannot take the Apache backend port' \
      "$conflict" 'Apache backend'
    ;;
  *)
    log '  ...skipped: no Node.js runtime could be installed to check the port against'
    ;;
esac

set_mode nginx
purge

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 4.5 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
