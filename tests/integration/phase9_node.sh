#!/bin/sh
# Phase 9 Docker integration test — the Node.js manager.
#
# Black-box checks against the running stack. The acceptance is not that the API
# returns 200: it is that a real Express application, deployed through the panel,
# answers a request through nginx on its own domain, under its own system
# account, with the environment the panel gave it — and stops answering when the
# panel is told to stop it.
#
# It runs inside the agent container, which is the managed host in development,
# so it can look at the process, its account, and its logs directly.
#
# Run with:  make docker-test-node

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

SITE_DOMAIN="${SITE_DOMAIN:-p9.integration.test}"
APP_PORT="${APP_PORT:-3210}"

failures=0
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

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
    curl -s --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 300 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 300 -X "$method" "$API_BASE_URL$path" \
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

# await_body PATTERN — fetch the site until the body contains PATTERN.
#
# An nginx reload is graceful: the old workers finish what they are serving
# before the new configuration takes over. That is the correct behaviour — no
# request is dropped — and it means a check firing the instant the panel
# returns can legitimately see the previous configuration.
await_body() {
  await_pattern="$1"
  await_waited=0
  while [ "$await_waited" -lt 15 ]; do
    await_out="$(curl -s --max-time 20 -H "Host: $SITE_DOMAIN" http://127.0.0.1/ 2>/dev/null || true)"
    case "$await_out" in
      *"$await_pattern"*) printf '%s' "$await_out"; return ;;
    esac
    sleep 1
    await_waited=$((await_waited + 1))
  done
  printf '%s' "$await_out"
}

# await_status CODE — fetch the site until it answers with CODE.
await_status() {
  await_want="$1"
  await_waited=0
  while [ "$await_waited" -lt 15 ]; do
    await_code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20       -H "Host: $SITE_DOMAIN" http://127.0.0.1/ 2>/dev/null || true)"
    if [ "$await_code" = "$await_want" ]; then
      printf '%s' "$await_code"
      return
    fi
    sleep 1
    await_waited=$((await_waited + 1))
  done
  printf '%s' "$await_code"
}

# json_number JSON KEY — an unquoted numeric value.
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

# site_id LISTING DOMAIN — the id of a website by its domain.
site_id() {
  printf '%s' "$1" | tr '{' '\n' | grep "\"primary_domain\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# app_id LISTING DOMAIN — the id of the application serving a domain.
app_id() {
  printf '%s' "$1" | tr '{' '\n' | grep "\"website_domain\":\"$2\"" |
    sed -n 's/^"id":"\([^"]*\)".*/\1/p' | head -n 1
}

# purge removes anything a previous run left behind, on both sides.
purge() {
  purge_app="$(app_id "$(api GET /api/v1/node/apps)" "$SITE_DOMAIN")"
  if [ -n "$purge_app" ]; then
    api DELETE "/api/v1/node/apps/$purge_app" >/dev/null 2>&1 || true
  fi

  purge_site="$(site_id "$(api GET /api/v1/websites)" "$SITE_DOMAIN")"
  if [ -n "$purge_site" ]; then
    api DELETE "/api/v1/websites/$purge_site?remove_files=true" >/dev/null 2>&1 || true
    # Deleting a website is a job; give it a moment so the next create is not
    # refused as a duplicate domain.
    waited=0
    while [ "$waited" -lt 30 ]; do
      if [ -z "$(site_id "$(api GET /api/v1/websites)" "$SITE_DOMAIN")" ]; then
        break
      fi
      sleep 2
      waited=$((waited + 2))
    done
  fi

  # And the directory, if the delete did not get to it. A tree left behind
  # belongs to an account that no longer exists, so the next run's site would
  # inherit files nginx cannot read.
  rm -rf "/var/www/$SITE_DOMAIN"
}

# ------------------------------------------------------------------ the run

log 'Phase 9 — Node.js manager'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# --- 1. the runtime ------------------------------------------------------

log ''
log '1. Runtime'

versions="$(api GET /api/v1/node/versions)"
contains 'the versions endpoint reports what the host can do' "$versions" '"can_install"'

case "$versions" in
  *'"available":true'*) ;;
  *)
    # Installing is part of the feature, and a bare host is where a real
    # operator starts, so the suite installs it rather than requiring it.
    log '  ...installing Node.js, which this host does not have yet'
    installed="$(api POST /api/v1/node/versions/install '{"package":"nodejs"}' 2>/dev/null || true)"
    versions="$(api GET /api/v1/node/versions)"
    ;;
esac

contains 'a Node.js runtime is available' "$versions" '"available":true'
contains 'npm is available with it' "$versions" '"npm_version":"'
contains 'the host says how it runs applications' "$versions" '"managed_by":"'

# --- 2. a website to serve it -------------------------------------------

log ''
log '2. The website'

purge

created="$(api POST /api/v1/websites "{\"domain\":\"$SITE_DOMAIN\"}")"
SITE="$(json_field "$created" 'id')"
if [ -z "$SITE" ]; then
  fail "create the website (got: $(printf '%s' "$created" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

# The website's own status, not any status in the response.
#
# A website carries a domains array, and a domain row is "active" from the
# moment it is written — so matching the whole body for "status":"active"
# succeeds while the website itself is still being created, and everything
# after it then fails for reasons that have nothing to do with the code under
# test. json_field takes the first occurrence, which is the website's own.
waited=0
while [ "$waited" -lt 90 ]; do
  site="$(api GET "/api/v1/websites/$SITE?remove_files=true")"
  if [ "$(json_field "$site" 'status')" = "active" ]; then
    break
  fi
  sleep 2
  waited=$((waited + 2))
done

site_status="$(json_field "$site" 'status')"
if [ "$site_status" = "active" ]; then
  pass 'the website is active'
else
  fail "the website is active (it is ${site_status:-unknown})"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

ROOT="$(json_field "$site" 'document_root')"
OWNER="$(json_field "$site" 'system_user')"
contains 'it has its own system account' "$OWNER" 'web_'

# The panel reports a site active once the host says it finished, and the
# directory is part of that. Waited for anyway: everything below writes into it,
# and "no such directory" is a far worse way to learn the site is not ready than
# a check that says so.
waited=0
while [ "$waited" -lt 30 ] && [ ! -d "$ROOT" ]; do
  sleep 1
  waited=$((waited + 1))
done
if [ -d "$ROOT" ]; then
  pass 'its document root exists on the host'
else
  fail "its document root exists on the host ($ROOT)"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

# The account the directory actually belongs to, and the account the panel says
# it belongs to, must be the same. That is worth checking rather than assuming:
# a panel whose record disagrees with the host is a panel giving wrong answers.
waited=0
while [ "$waited" -lt 30 ] && ! id "$OWNER" >/dev/null 2>&1; do
  sleep 1
  waited=$((waited + 1))
done

disk_owner="$(stat -c '%U' "$ROOT" 2>/dev/null || echo '')"
if [ "$disk_owner" = "$OWNER" ]; then
  pass "the panel's record of the account matches the host"
else
  fail "the panel's record of the account matches the host (panel says $OWNER, host says ${disk_owner:-nothing})"
fi

# --- 3. a real application ----------------------------------------------

log ''
log '3. The application'

# A real Express application, not a stub: the point is to prove the panel can
# run the thing people actually deploy.
cat > "$ROOT/package.json" <<'JSON'
{
  "name": "jothost-phase9-check",
  "version": "1.0.0",
  "private": true,
  "dependencies": { "express": "^4.19.2" }
}
JSON

cat > "$ROOT/server.js" <<'JS'
const express = require("express");
const os = require("os");
const app = express();

app.get("/", (req, res) => {
  res.json({
    ok: true,
    port: process.env.PORT,
    nodeEnv: process.env.NODE_ENV,
    greeting: process.env.GREETING || null,
    forwardedProto: req.headers["x-forwarded-proto"] || null,
    forwardedFor: req.headers["x-forwarded-for"] || null,
    host: req.headers.host || null,
    user: os.userInfo().username,
  });
});

app.listen(process.env.PORT, "127.0.0.1", () => {
  console.log("phase9 listening on " + process.env.PORT);
});
JS

# Owner only, keeping the group. A site's files are group-owned by the web
# server's group so nginx can read them; changing that is how a site that was
# serving perfectly well starts returning 403.
site_group="$(stat -c '%G' "$ROOT")"
chown "$OWNER":"$site_group" "$ROOT/package.json" "$ROOT/server.js"

APP_NAME="jothost-p9-check"
app_created="$(api POST /api/v1/node/apps \
  "{\"website_id\":\"$SITE\",\"port\":$APP_PORT,\"startup_file\":\"server.js\",\"name\":\"$APP_NAME\"}")"
APP="$(json_field "$app_created" 'id')"

contains 'creating the application reports it back' "$app_created" '"port":'
contains 'it is created stopped, not started' "$app_created" '"status":"stopped"'
contains 'it runs under the website account' "$app_created" "\"system_user\":\"$OWNER\""

if [ -z "$APP" ]; then
  fail "the application was not created (got: $(printf '%s' "$app_created" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

# The site must still be serving its files: creating is not starting.
code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  -H "Host: $SITE_DOMAIN" http://127.0.0.1/ 2>/dev/null || true)"
expect_status 'the website still serves its files' '200' "$code"

log ''
log '4. Dependencies'

code="$(api_status POST "/api/v1/node/apps/$APP/dependencies")"
expect_status 'installing dependencies' '200' "$code"

if [ -d "$ROOT/node_modules/express" ]; then
  pass 'express is installed'
else
  fail 'express is installed'
fi

# node_modules must belong to the account that will read it. Installed as
# root, a deployment ends up with a tree it can never update.
modules_owner="$(stat -c '%U' "$ROOT/node_modules" 2>/dev/null || echo '')"
if [ "$modules_owner" = "$OWNER" ]; then
  pass 'node_modules belongs to the website account'
else
  fail "node_modules belongs to the website account (owned by ${modules_owner:-nothing})"
fi

# --- 5. environment ------------------------------------------------------

log ''
log '5. Environment'

code="$(api_status PUT "/api/v1/node/apps/$APP/environment" \
  '{"key":"GREETING","value":"set through the panel"}')"
expect_status 'setting a variable' '200' "$code"

# The panel says what is set, never what it is set to. The names come from the
# application's own record; a listing deliberately carries neither, so it is
# the detail endpoint that is checked for the name and both that are checked
# for the value.
detail="$(api GET "/api/v1/node/apps/$APP")"
contains 'the application names the variable' "$detail" 'GREETING'
not_contains 'it never carries the value' "$detail" 'set through the panel'

listing="$(api GET /api/v1/node/apps)"
not_contains 'a listing never carries the value either' "$listing" 'set through the panel'

# The names that change what runs rather than how it behaves.
for bad in LD_PRELOAD NODE_OPTIONS PATH PORT; do
  code="$(api_status PUT "/api/v1/node/apps/$APP/environment" \
    "{\"key\":\"$bad\",\"value\":\"x\"}")"
  if [ "$code" = "422" ]; then
    pass "a variable that would change what runs is refused: $bad"
  else
    fail "a variable that would change what runs is refused: $bad (got HTTP $code)"
  fi
done

# A newline would close the line in a unit or env file and start a directive.
code="$(api_status PUT "/api/v1/node/apps/$APP/environment" \
  '{"key":"CONFIG","value":"a\nExecStart=/bin/sh"}')"
if [ "$code" = "422" ] || [ "$code" = "400" ]; then
  pass 'a value containing a newline is refused'
else
  fail "a value containing a newline is refused (got HTTP $code)"
fi

# --- 6. running it -------------------------------------------------------

log ''
log '6. Running'

started="$(api POST "/api/v1/node/apps/$APP/start")"
contains 'the application starts' "$started" '"status":"running"'
contains 'and it is listening' "$started" '"listening":true'

# The claim under test. Everything above can pass while this fails.
body="$(await_body '"ok":true')"
contains 'the domain reaches the application' "$body" '"ok":true'
contains 'the panel gave it the port' "$body" "\"port\":\"$APP_PORT\""
contains 'the panel gave it the environment' "$body" '"greeting":"set through the panel"'

# The proxy headers an application behind nginx needs. Without them it builds
# every absolute URL wrong and sees one client for every request.
contains 'it is told the real host' "$body" "\"host\":\"$SITE_DOMAIN\""
contains 'it is told the scheme' "$body" '"forwardedProto":"http"'
contains 'it is told the client address' "$body" '"forwardedFor":"'

# It must not be root, and it must be the site's own account rather than one
# shared with anything else.
contains 'it runs as the website account' "$body" "\"user\":\"$OWNER\""
not_contains 'it does not run as root' "$body" '"user":"root"'

# The port is bound on the loopback only: nothing outside the server reaches
# the application except through nginx.
if command -v netstat >/dev/null 2>&1; then
  listeners="$(netstat -ltn 2>/dev/null | grep ":$APP_PORT" || true)"
  case "$listeners" in
    *"0.0.0.0:$APP_PORT"*) fail 'the application listens only on the loopback' ;;
    *) pass 'the application listens only on the loopback' ;;
  esac
fi

log ''
log '7. Logs'

logs="$(api GET "/api/v1/node/apps/$APP/logs")"
contains 'its own output is readable' "$logs" 'phase9 listening on'

log ''
log '8. Restart and stop'

restarted="$(api POST "/api/v1/node/apps/$APP/restart")"
contains 'restarting keeps it running' "$restarted" '"status":"running"'

body="$(await_body '"ok":true')"
contains 'it still answers after a restart' "$body" '"ok":true'

stopped="$(api POST "/api/v1/node/apps/$APP/stop")"
contains 'stopping it' "$stopped" '"status":"stopped"'

# A deliberate stop must not look like an outage: the site goes back to
# serving its files rather than returning 502 to every visitor.
code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  -H "Host: $SITE_DOMAIN" http://127.0.0.1/ 2>/dev/null || true)"
if [ "$code" = "200" ]; then
  pass 'the website returns to serving its files'
else
  fail "the website returns to serving its files (got HTTP $code)"
fi

# --- 9. what the panel refuses ------------------------------------------

log ''
log '9. Refusals'

# A privileged port, and one in the range the kernel hands to outgoing
# connections. Neither is a port an unprivileged application can rely on.
for bad in 80 1023 40000; do
  code="$(api_status POST /api/v1/node/apps "{\"website_id\":\"$SITE\",\"port\":$bad}")"
  if [ "$code" = "422" ] || [ "$code" = "409" ]; then
    pass "a port the application could not use is refused: $bad"
  else
    fail "a port the application could not use is refused: $bad (got HTTP $code)"
  fi
done

# A startup file outside the application's own directory.
for bad in '/etc/passwd' '../../server.js' 'a/../../b.js'; do
  code="$(api_status POST /api/v1/node/apps \
    "{\"website_id\":\"$SITE\",\"port\":3999,\"startup_file\":\"$bad\"}")"
  if [ "$code" = "422" ] || [ "$code" = "409" ]; then
    pass "a startup file outside the application is refused: $bad"
  else
    fail "a startup file outside the application is refused: $bad (got HTTP $code)"
  fi
done

# One application per website: a second would need a second vhost to reach it.
code="$(api_status POST /api/v1/node/apps "{\"website_id\":\"$SITE\",\"port\":3998}")"
expect_status 'a second application on one website is refused' '409' "$code"

# A misspelled field must be reported, not ignored.
code="$(api_status POST /api/v1/node/apps "{\"website_id\":\"$SITE\",\"prot\":3997}")"
expect_status 'an unknown field is refused rather than ignored' '400' "$code"

# --- 10. removal ---------------------------------------------------------

log ''
log '10. Removal'

code="$(api_status DELETE "/api/v1/node/apps/$APP")"
expect_status 'removing the application' '200' "$code"

# The customer's code is not the process manager's business.
if [ -f "$ROOT/server.js" ] && [ -d "$ROOT/node_modules" ]; then
  pass 'the code and its dependencies are left alone'
else
  fail 'the code and its dependencies are left alone'
fi

# And nothing of the panel's is left behind. The pid file names the
# application, so only this application's is looked for: another suite's
# application may legitimately be running alongside.
if [ -f "/var/lib/jothost/node/$APP_NAME.pid" ]; then
  fail 'no process record is left behind'
else
  pass 'no process record is left behind'
fi

purge

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 9 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
