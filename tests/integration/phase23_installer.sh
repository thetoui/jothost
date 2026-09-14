#!/bin/sh
# Phase 23 Docker integration test — the production installer.
#
# The acceptance is not that the installer exits zero. It is that a machine
# which had nothing on it now serves the panel, over HTTPS, on the name it was
# given, with an administrator who can sign in and a database behind them —
# and that the same script can update, repair and remove all of it.
#
# So this runs on a bare alpine:3.21 container. Nothing is pre-installed: no
# nginx, no PostgreSQL, no Redis, no panel. That emptiness is the whole point,
# because an installer tested against the development image would be tested
# against a host that already had everything it was supposed to install.
#
# What is proved here that nothing else can prove:
#
#   * A single command turns an empty machine into a working panel, with no
#     configuration beyond the domain.
#   * The refusals hold: no domain, a domain that is not one, a bad username,
#     missing artefacts. Each says what it wanted rather than failing halfway.
#   * The panel answers on its own name, over TLS, and serves the frontend.
#   * The administrator the installer created can actually sign in, and the
#     token they get back works against the API.
#   * The API runs unprivileged and the Agent runs as root — the split the
#     whole architecture rests on — and the socket between them is reachable by
#     one account and no other.
#   * Secrets are generated once: an update and a repair leave the encryption
#     key, the Agent token and the database password exactly as they were.
#     Regenerating any of them would silently orphan every encrypted value.
#   * `repair` fixes a machine somebody has broken by hand.
#   * `uninstall` removes the panel and leaves the customers' websites alone,
#     and `--purge` is what removes the panel's own data.
#
# Run with:  make docker-test-installer

set -eu

DIST=${DIST:-/dist}
DOMAIN=${PANEL_DOMAIN:-panel.installer.test}
ADMIN_USER=installer_admin

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 300))" ;;
  esac
}

not_contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) fail "$name (found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

same() {
  name="$1"; got="$2"; want="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (got '$got', want '$want')"; fi
}

differs() {
  name="$1"; a="$2"; b="$3"
  if [ "$a" != "$b" ]; then pass "$name"; else fail "$name (both are '$a')"; fi
}

# env_value reads one variable out of one of the installer's env files.
#
# The quotes are stripped: the file is written quoted so that both systemd and
# a shell can read it, and a comparison against the value should not be a
# comparison against the value plus its punctuation.
env_value() {
  sed -n "s/^$2=//p" "$1" 2>/dev/null | head -n 1 | sed 's/^"//; s/"$//'
}

log "Phase 23 — the production installer"
log ""

# ------------------------------------------------------------- 0. the host

log "0. A machine with nothing on it"

# curl, however this host installs things. The suite runs on Alpine and on
# Debian with systemd, because the installer picks its package manager and its
# service manager from what it finds and both branches have to be exercised.
if ! command -v curl >/dev/null 2>&1; then
  apk add --no-cache curl >/dev/null 2>&1 ||
    { apt-get update -qq >/dev/null 2>&1 &&
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends curl >/dev/null 2>&1; } ||
    true
fi

for absent in nginx psql redis-server; do
  if command -v "$absent" >/dev/null 2>&1; then
    fail "this host is not clean: $absent is already installed"
  else
    pass "$absent is not installed yet"
  fi
done

[ -x "$DIST/install.sh" ] || { log "FATAL: no installer at $DIST/install.sh — run: make dist"; exit 1; }
[ -x "$DIST/bin/jothost-api" ] || { log "FATAL: no API binary in $DIST/bin"; exit 1; }

# The domain has to resolve to this machine, or the final check would be a
# request to localhost dressed up as a request to the panel. With this, nginx's
# server_name matching is exercised for real.
grep -q "$DOMAIN" /etc/hosts 2>/dev/null || printf '127.0.0.1 %s\n' "$DOMAIN" >> /etc/hosts
pass "$DOMAIN resolves to this machine"

# --------------------------------------------------------- 1. the refusals

log ""
log "1. What it refuses, before it changes anything"

# Each of these must fail *and* say what it wanted. An installer that exits
# non-zero with a shell error from three functions deep leaves somebody with a
# half-built machine and no idea which half.
out=$("$DIST/install.sh" install --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "an install with no domain is refused"; else fail "an install with no domain was accepted"; fi
contains "and says that --domain is required" "$out" "--domain is required"

out=$("$DIST/install.sh" install --domain "not a domain" --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "a domain that is not one is refused"; else fail "a bad domain was accepted"; fi
contains "and says so by name" "$out" "is not a domain name"

# A domain carrying a semicolon would close an nginx directive and open one of
# the caller's choosing. It is refused by shape, not by searching for
# characters somebody thought of.
out=$("$DIST/install.sh" install --domain 'evil.test;return 200 "x"' --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "a domain that would close an nginx directive is refused"; else fail "a domain containing a semicolon was accepted"; fi

out=$("$DIST/install.sh" install --domain "$DOMAIN" --admin-user 'Root Admin' --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "an unusable administrator name is refused"; else fail "a bad username was accepted"; fi

out=$("$DIST/install.sh" install --domain "$DOMAIN" --from /nonexistent --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "an install with no artefacts is refused"; else fail "missing artefacts were accepted"; fi
contains "and says where it looked" "$out" "/nonexistent"

out=$("$DIST/install.sh" install --domain "$DOMAIN" --frobnicate --yes 2>&1) && rc=0 || rc=$?
if [ "${rc:-0}" != 0 ]; then pass "an unknown option is refused rather than ignored"; else fail "an unknown option was ignored"; fi

# Nothing above may have touched the machine.
if [ -d /opt/jothost ]; then
  fail "a refused install still created /opt/jothost"
else
  pass "nothing was installed by any of the refusals"
fi

# ------------------------------------------------------------ 2. installing

log ""
log "2. One command, one domain"

start=$(date +%s)
# Deliberately not --minimal. The claim being checked is that a machine is
# ready to host a website when the installer finishes, and a panel that has to
# install PHP before its first site can run one has not finished.
if "$DIST/install.sh" install \
     --domain "$DOMAIN" \
     --email "admin@$DOMAIN" \
     --admin-user "$ADMIN_USER" \
     --self-signed \
     --health-host 127.0.0.1 \
     --yes > /tmp/install.log 2>&1; then
  pass "the installer completed"
else
  fail "the installer failed"
  tail -40 /tmp/install.log | sed 's/^/        /'
fi
elapsed=$(( $(date +%s) - start ))
log "        (took ${elapsed}s)"

install_out=$(cat /tmp/install.log)
# The distribution the installer says it found. Asserted rather than ignored:
# an installer that misidentifies the host carries on with the wrong package
# manager and the wrong service manager, and every later failure describes a
# symptom of that rather than the cause.
contains "it reported the host it found" "$install_out" "${EXPECT_DISTRO:-alpine}"
contains "it created the administrator" "$install_out" "administrator \"$ADMIN_USER\" created"
contains "it installed PHP, so the first website can run one" "$install_out" \
  "PHP installed, so the first website can run an application"
contains "it checked the API is healthy" "$install_out" "the API is healthy"
contains "it checked the dependencies are reachable" "$install_out" \
  "it can reach PostgreSQL, Redis and the Agent"
contains "it checked the domain serves the panel" "$install_out" "$DOMAIN serves the panel"
# The password is shown once and never written down. A test that only checked
# it was printed would not notice it being stored.
contains "it printed the administrator's password once" "$install_out" "This password is shown once"

ADMIN_PASSWORD=$(printf '%s' "$install_out" | sed -n 's/^  password  //p' |
  sed 's/\x1b\[[0-9;]*m//g' | tr -d ' \r')
if [ -n "$ADMIN_PASSWORD" ]; then
  pass "the password was printed where a person can read it"
else
  fail "no password was printed"
fi
if grep -rq "$ADMIN_PASSWORD" /etc/jothost/ 2>/dev/null; then
  fail "the administrator's password was written to disk"
else
  pass "the administrator's password is nowhere on disk"
fi

# ---------------------------------------------------- 3. what is on the host

log ""
log "3. What it laid down"

for path in /opt/jothost/bin/jothost-api /opt/jothost/bin/jothost-agent \
            /opt/jothost/frontend/index.html /opt/jothost/migrations \
            /etc/jothost/api.env /etc/jothost/agent.env /etc/jothost/install.env; do
  if [ -e "$path" ]; then pass "$path exists"; else fail "$path is missing"; fi
done

# The configuration holds the database password, the encryption key and the
# Agent token. Anything on this machine that can read it can be the panel.
mode=$(stat -c '%a %U:%G' /etc/jothost/api.env)
same "the API's configuration is 0640 root:jothost" "$mode" "640 root:jothost"
mode=$(stat -c '%a %U:%G' /etc/jothost/agent.env)
same "the Agent's configuration is 0600 root:root" "$mode" "600 root:root"

if id jothost-api >/dev/null 2>&1; then pass "the unprivileged API account exists"; else fail "no jothost-api account"; fi

# The split the whole architecture rests on: the process reachable from the
# network is not root, and the one that is root is not reachable.
#
# Read from /proc rather than from ps. busybox's ps truncates a long user name
# to the column width and does not take the options that would widen it, so a
# check written against ps compares "jothost-" to "jothost-api" and reports a
# failure that is only in the measuring. The owner of /proc/<pid> is the
# process's effective user, exactly, on every Linux.
process_user() {
  pid=$(pgrep -f "$1" 2>/dev/null | head -n 1)
  [ -n "$pid" ] || return 1
  stat -c '%U' "/proc/$pid" 2>/dev/null
}

api_user=$(process_user '/opt/jothost/bin/jothost-api serve' || true)
same "the API runs as jothost-api, not as root" "$api_user" "jothost-api"

agent_user=$(process_user '/opt/jothost/bin/jothost-agent' || true)
same "the Agent runs as root, which is the only part that may" "$agent_user" "root"

if [ -S /run/jothost/agent.sock ]; then
  pass "the Agent's socket exists"
  sock=$(stat -c '%a %U:%G' /run/jothost/agent.sock)
  same "and is 0660 root:jothost — reachable by the API and nothing else" "$sock" "660 root:jothost"
else
  fail "no Agent socket at /run/jothost/agent.sock"
fi

# The second lock: the socket's group says who *can* connect, and this says who
# may even so.
allowed=$(env_value /etc/jothost/agent.env AGENT_ALLOWED_UIDS)
same "the Agent only accepts the API's own uid" "$allowed" "$(id -u jothost-api)"

# ------------------------------------------------------ 4. the panel answers

log ""
log "4. The panel, on its own name"

body=$(curl -fsSk --max-time 15 "https://$DOMAIN/" 2>/dev/null || true)
contains "the domain serves the frontend over HTTPS" "$body" '<div id="root"'
contains "and it is the panel's own page" "$body" "JotHost"

health=$(curl -fsSk --max-time 15 "https://$DOMAIN/api/v1/health" 2>/dev/null || true)
contains "the API answers through nginx" "$health" '"status":"ok"'

# Plain HTTP must redirect, except for the ACME challenge — which has to stay
# answerable without a valid certificate, since that is the state a renewal is
# fixing.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "http://$DOMAIN/" || true)
same "plain HTTP redirects to HTTPS" "$code" "301"
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 \
  "http://$DOMAIN/.well-known/acme-challenge/probe" || true)
if [ "$code" = "404" ]; then
  pass "the ACME challenge path stays on plain HTTP, so a renewal can be answered"
else
  fail "the ACME challenge path returned HTTP $code rather than 404 (it must not redirect)"
fi

# A single-page application: any path the server does not have is the app's to
# route, or a reload of /websites is a 404.
code=$(curl -s -o /dev/null -w '%{http_code}' -k --max-time 10 "https://$DOMAIN/websites" || true)
same "a deep link falls through to the application" "$code" "200"

# The panel's own applications are proxied to its private nginx, which runs on
# loopback and is a different process from the one serving websites. Nothing
# checked this before, and the failure it hides is a quiet one: with no
# location block, /phpmyadmin/ falls through to the single-page application
# above and answers 200 with the panel's own HTML. The browser then fetches
# what it thinks is phpMyAdmin's login form, parses the panel's index page,
# finds no CSRF token, and reports that phpMyAdmin returned no login form.
#
# So this stands something on the panel's port and checks the answer came from
# there. A 502 would prove only that *something* is proxied; it would pass
# just as happily against a proxy aimed at the wrong port.
panel_port=8791
probe_reply="proxied-to-the-panel-stack"
(
  while true; do
    # The escapes are spelled out rather than embedded as real CR bytes:
    # git normalises CRLF in the working tree, which would silently turn
    # this into a malformed HTTP response the next time it touched the file.
    printf 'HTTP/1.1 200 OK\r\nContent-Length: %s\r\nConnection: close\r\n\r\n%s' \
      "${#probe_reply}" "$probe_reply" |
      # Two spellings, because the platforms ship different netcats: busybox
      # wants "-l -p PORT -s ADDR" and OpenBSD's wants "-l ADDR PORT". Getting
      # it wrong fails silently - the listener never starts, nginx answers 502,
      # and the check reports a working proxy as broken.
      { nc -l -p "$panel_port" -s 127.0.0.1 2>/dev/null ||
        nc -l 127.0.0.1 "$panel_port" 2>/dev/null; } >/dev/null || break
  done
) &
probe_pid=$!
sleep 1

body=$(curl -s -k --max-time 10 "https://$DOMAIN/phpmyadmin/index.php" 2>/dev/null || true)
kill "$probe_pid" >/dev/null 2>&1 || true
wait "$probe_pid" 2>/dev/null || true

case "$body" in
  *"$probe_reply"*)
    pass "/phpmyadmin/ is proxied to the panel's own web stack on $panel_port" ;;
  *"<!doctype html"*|*"<!DOCTYPE html"*)
    fail "/phpmyadmin/ fell through to the single-page application: no proxy is configured" ;;
  *)
    fail "/phpmyadmin/ did not reach the panel's stack on $panel_port (got: $(printf '%.60s' "$body"))" ;;
esac

# Self-signed was asked for, so the certificate must not be trusted — and the
# installer must have said so rather than implying otherwise.
if curl -fsS --max-time 10 "https://$DOMAIN/" >/dev/null 2>&1; then
  fail "the self-signed certificate was accepted without -k, which cannot be right"
else
  pass "the certificate is self-signed, as asked, and is not trusted"
fi
contains "and the installer said so plainly" "$install_out" "SELF-SIGNED"
not_contains "and did not claim HSTS with an untrusted certificate" \
  "$(cat /etc/nginx/http.d/00-jothost-panel.conf 2>/dev/null || cat /etc/nginx/conf.d/00-jothost-panel.conf 2>/dev/null || true)" \
  "Strict-Transport-Security"

# ------------------------------------------------------------ 5. signing in

log ""
log "5. The administrator can actually sign in"

login=$(curl -fsSk --max-time 15 -X POST "https://$DOMAIN/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" 2>/dev/null || true)
contains "the generated password works" "$login" '"access_token"'

TOKEN=$(printf '%s' "$login" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
if [ -n "$TOKEN" ]; then
  me=$(curl -fsSk --max-time 15 "https://$DOMAIN/api/v1/auth/me" \
    -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)
  contains "the token identifies the administrator" "$me" "\"username\":\"$ADMIN_USER\""
  contains "who holds the admin role" "$me" '"admin"'

  # The panel is talking to the Agent, which is the whole point of the socket
  # above: this endpoint asks the host about itself.
  server=$(curl -fsSk --max-time 20 "https://$DOMAIN/api/v1/dashboard" \
    -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)
  contains "the panel can reach the Agent and report on the host" "$server" '"cpu"'
else
  fail "no token was returned, so nothing further could be checked"
fi

# A wrong password must still be refused on a freshly installed machine.
code=$(curl -s -o /dev/null -w '%{http_code}' -k --max-time 15 -X POST \
  "https://$DOMAIN/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"not-the-password\"}" || true)
same "a wrong password is refused" "$code" "401"

# ------------------------------------------------ 6. it can host a website

log ""
log "6. The machine can host a website, straight away"

# The claim this section exists to check, and the one nothing else can.
#
# Everything above proves the *panel* works. This proves the machine is a
# hosting machine: a website created through the panel, provisioned by the
# Agent, served by the nginx the installer configured, running PHP through the
# pool the panel wrote — with nothing configured beyond the domain given to the
# installer. Every part of that is a place where the installer could have laid
# something down in the wrong directory or with the wrong owner, and none of
# those would have shown up in a health check.
SITE_DOMAIN="firstsite.installer.test"
grep -q "$SITE_DOMAIN" /etc/hosts 2>/dev/null || printf '127.0.0.1 %s\n' "$SITE_DOMAIN" >> /etc/hosts

created=$(curl -fsSk --max-time 60 -X POST "https://$DOMAIN/api/v1/websites" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"first site\"}" 2>/dev/null || true)
SITE_ID=$(printf '%s' "$created" | grep -o '"id":"[^"]*"' | head -n 1 | sed 's/.*:"//; s/"//')

if [ -n "$SITE_ID" ]; then
  pass "a website can be created through the panel"
else
  fail "a website could not be created: $(printf '%s' "$created" | head -c 200)"
fi

# Provisioning is asynchronous: the row exists at once, the directories, the
# account and the vhost arrive when the worker has run.
state=""
waited=0
while [ "$waited" -lt 120 ]; do
  state=$(curl -fsSk --max-time 20 "https://$DOMAIN/api/v1/websites/$SITE_ID" \
    -H "Authorization: Bearer $TOKEN" 2>/dev/null |
    grep -o '"status":"[^"]*"' | head -n 1 | sed 's/.*:"//; s/"//')
  case "$state" in active|failed) break ;; esac
  sleep 3; waited=$((waited + 3))
done
same "and is provisioned on the host" "$state" "active"

if [ -d "/var/www/$SITE_DOMAIN/public" ]; then
  pass "its directory exists under the site root the installer configured"
else
  fail "no directory at /var/www/$SITE_DOMAIN/public"
fi

# The vhost has to land where *this* distribution's nginx reads from. Written
# into the other directory it is not merely ignored: on Alpine, conf.d is
# included at the main level, where a server block stops nginx starting at all.
site_vhost="$(ls /etc/nginx/http.d/$SITE_DOMAIN.conf /etc/nginx/conf.d/$SITE_DOMAIN.conf 2>/dev/null | head -n 1)"
if [ -n "$site_vhost" ]; then
  pass "its vhost is in the directory this distribution's nginx reads"
else
  fail "no vhost for $SITE_DOMAIN in either nginx directory"
fi

body=$(curl -fsS --max-time 20 "http://$SITE_DOMAIN/" 2>/dev/null || true)
contains "and the site is served on its own name" "$body" "$SITE_DOMAIN"

# The site's own account, which is what its files are owned by and what PHP
# runs as. A site owned by root is a site every other customer's code can read.
owner=$(stat -c '%U' "/var/www/$SITE_DOMAIN/public" 2>/dev/null || echo unknown)
case "$owner" in
  web_*) pass "its files belong to the site's own account ($owner)" ;;
  *)     fail "its files belong to '$owner', which is not a per-site account" ;;
esac

# A new site serves files and refuses .php until somebody chooses a version.
# That is Phase 4's decision and it is the right one — a site with PHP off
# still has its config.php — so what is checked here is that the refusal holds
# rather than that PHP happens to run.
code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "http://$SITE_DOMAIN/index.php" 2>/dev/null || echo 000)
if [ "$code" = "403" ] || [ "$code" = "404" ]; then
  pass "a site with no PHP version does not execute .php"
else
  fail "a site with no PHP version answered HTTP $code for a .php path"
fi

# The integration the installer is responsible for: the PHP it installed is
# found by the panel, without anybody being told where it is.
versions=$(curl -fsSk --max-time 20 "https://$DOMAIN/api/v1/php/versions" \
  -H "Authorization: Bearer $TOKEN" 2>/dev/null || true)
PHP_VERSION=$(printf '%s' "$versions" | grep -o '"version":"[0-9.]*"' | head -n 1 |
  sed 's/.*:"//; s/"//')
if [ -n "$PHP_VERSION" ]; then
  pass "the panel found the PHP the installer laid down ($PHP_VERSION)"
else
  fail "the panel found no PHP version: $(printf '%s' "$versions" | head -c 200)"
fi

# Turning it on is the end of the chain the installer had to get right: the
# packages, the pool directory, the FPM socket, the web group and the vhost.
if [ -n "$PHP_VERSION" ]; then
  applied=$(curl -fsSk --max-time 60 -X PATCH "https://$DOMAIN/api/v1/websites/$SITE_ID/php" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "{\"version\":\"$PHP_VERSION\"}" 2>/dev/null || true)
  if [ -n "$applied" ]; then
    pass "PHP $PHP_VERSION can be turned on for the site"
  else
    fail "PHP could not be turned on for the site"
  fi

  # The probe uses only core PHP, and it writes a file rather than asking who
  # it is.
  #
  # posix_geteuid() would be the direct way and is an extension Alpine does not
  # ship by default — a probe that used it would report "PHP is broken" on a
  # host where PHP is fine. Writing a file proves two things at once with
  # nothing but the core: that PHP runs, and, from the owner of what it wrote,
  # which account it ran as.
  cat > "/var/www/$SITE_DOMAIN/public/probe.php" <<'PHP'
<?php
@file_put_contents(__DIR__ . '/wrote-by-php.txt', 'x');
echo 'PHP-OK-', PHP_MAJOR_VERSION;
PHP
  chown "$owner" "/var/www/$SITE_DOMAIN/public/probe.php" 2>/dev/null || true
  rm -f "/var/www/$SITE_DOMAIN/public/wrote-by-php.txt"

  php_body=""
  waited=0
  while [ "$waited" -lt 90 ]; do
    php_body=$(curl -fsS --max-time 20 "http://$SITE_DOMAIN/probe.php" 2>/dev/null || true)
    case "$php_body" in PHP-OK-*) break ;; esac
    sleep 3; waited=$((waited + 3))
  done

  case "$php_body" in
    PHP-OK-*)
      pass "PHP runs on the new site, through the pool the panel wrote"

      # Per-site accounts are the whole isolation story: PHP running as the web
      # server would let every customer's code read every other customer's.
      wrote_by=$(stat -c '%U' "/var/www/$SITE_DOMAIN/public/wrote-by-php.txt" 2>/dev/null || echo none)
      case "$wrote_by" in
        "$owner") pass "and it runs as the site's own account, not as the web server" ;;
        none)     fail "PHP ran but could not write into its own document root" ;;
        *)        fail "PHP ran as '$wrote_by' rather than as the site's account" ;;
      esac ;;
    *) fail "PHP did not run on the new site (got: $(printf '%s' "$php_body" | head -c 160))" ;;
  esac
fi

# --------------------------------------------------------------- 7. status

log ""
log "7. What status reports"

status=$("$DIST/install.sh" status 2>&1 || true)
contains "status names the domain" "$status" "$DOMAIN"
contains "status reports the API healthy" "$status" "api           healthy"
contains "status reports the dependencies ready" "$status" "dependencies  ready"

# ------------------------------------------------------- 8. secrets persist

log ""
log "8. An update keeps the secrets it must keep"

key_before=$(env_value /etc/jothost/api.env ENCRYPTION_KEY)
token_before=$(env_value /etc/jothost/api.env AGENT_TOKEN)
db_before=$(env_value /etc/jothost/api.env DATABASE_URL)

if "$DIST/install.sh" update --from "$DIST" > /tmp/update.log 2>&1; then
  pass "update completed"
else
  fail "update failed"
  tail -30 /tmp/update.log | sed 's/^/        /'
fi

key_after=$(env_value /etc/jothost/api.env ENCRYPTION_KEY)
token_after=$(env_value /etc/jothost/api.env AGENT_TOKEN)
db_after=$(env_value /etc/jothost/api.env DATABASE_URL)

# The failure this guards against does not appear until somebody signs in: a
# regenerated key leaves every stored two-factor secret and every encrypted
# credential undecryptable, and nothing says so at the time.
same "the encryption key is unchanged" "$key_after" "$key_before"
same "the Agent token is unchanged" "$token_after" "$token_before"
same "the database password is unchanged" "$db_after" "$db_before"

login=$(curl -fsSk --max-time 15 -X POST "https://$DOMAIN/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}" 2>/dev/null || true)
contains "the administrator can still sign in after the update" "$login" '"access_token"'

# --------------------------------------------------------------- 9. repair

log ""
log "9. Repair fixes a machine somebody has broken"

vhost=/etc/nginx/http.d/00-jothost-panel.conf
[ -f "$vhost" ] || vhost=/etc/nginx/conf.d/00-jothost-panel.conf
rm -f "$vhost"
"$DIST/install.sh" status >/dev/null 2>&1 || true

if "$DIST/install.sh" repair > /tmp/repair.log 2>&1; then
  pass "repair completed"
else
  fail "repair failed"
  tail -30 /tmp/repair.log | sed 's/^/        /'
fi

if [ -f "$vhost" ]; then pass "the deleted vhost was put back"; else fail "the vhost was not restored"; fi
body=$(curl -fsSk --max-time 15 "https://$DOMAIN/" 2>/dev/null || true)
contains "and the panel serves again" "$body" '<div id="root"'

same "repair did not change the encryption key either" \
  "$(env_value /etc/jothost/api.env ENCRYPTION_KEY)" "$key_before"

# ------------------------------------------------ 10. two-factor recovery

log ""
log "10. Two-factor, and the ways back in without the authenticator"

# shellcheck disable=SC1091
. /tests/lib/totp.sh

api_json() {
  method="$1"; path="$2"; body="$3"; bearer="${4:-}"
  if [ -n "$bearer" ]; then
    curl -sk --max-time 15 -X "$method" "https://$DOMAIN/api/v1$path" \
      -H 'Content-Type: application/json' -H "Authorization: Bearer $bearer" -d "$body" 2>/dev/null || true
  else
    curl -sk --max-time 15 -X "$method" "https://$DOMAIN/api/v1$path" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  fi
}
first_field() { printf '%s' "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -n 1 | sed "s/^\"$2\":\"//; s/\"\$//"; }
sign_in_admin() { api_json POST /auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASSWORD\"}"; }

TOKEN=$(first_field "$(sign_in_admin)" access_token)
setup=$(api_json POST /auth/2fa/setup '{}' "$TOKEN")
secret=$(first_field "$setup" secret)
code=$(totp "$secret" 2>/dev/null || true)
enabled=$(api_json POST /auth/2fa/enable "{\"code\":\"$code\"}" "$TOKEN")
contains "the administrator turns on two-factor with a real authenticator code" "$enabled" '"two_factor_enabled":true'

recovery_codes=$(printf '%s' "$enabled" | grep -o '"recovery_codes":\[[^]]*\]' | sed 's/^"recovery_codes":\[//; s/\]$//' | tr ',' '\n' | tr -d '"')
same "and is given ten recovery codes, once" "$(printf '%s\n' "$recovery_codes" | grep -c .)" 10
first_code=$(printf '%s\n' "$recovery_codes" | head -n 1)

challenge=$(first_field "$(sign_in_admin)" mfa_token)
[ -n "$challenge" ] && pass "signing in now asks for the second factor" || fail "signing in did not ask for the second factor"
recovered=$(api_json POST /auth/2fa/verify "{\"mfa_token\":\"$challenge\",\"recovery_code\":\"$first_code\"}")
contains "a recovery code signs in without the authenticator" "$recovered" '"access_token"'

challenge=$(first_field "$(sign_in_admin)" mfa_token)
reused=$(api_json POST /auth/2fa/verify "{\"mfa_token\":\"$challenge\",\"recovery_code\":\"$first_code\"}")
contains "the same recovery code does not work twice" "$reused" 'already used recovery code'

# With neither, the host is the way back, as docs/RECOVERY.md tells an
# operator to do it: as the API's account, with its configuration.
if su -s /bin/sh jothost-api -c "set -a; . /etc/jothost/api.env; set +a; /opt/jothost/bin/jothost-api reset-two-factor $ADMIN_USER" > /tmp/reset-2fa.log 2>&1; then
  pass "reset-two-factor runs on the host"
else
  fail "reset-two-factor failed: $(tail -n 3 /tmp/reset-2fa.log | tr '\n' ' ')"
fi
contains "and the administrator signs in with the password alone" "$(sign_in_admin)" '"access_token"'

# ----------------------------------------------------------- 11. uninstall

log ""
log "11. Uninstall removes the panel and nothing else"

# A customer's website, to prove what uninstall does not touch. This is the
# single most destructive thing the script could do, and nobody typing
# "uninstall" is asking for their customers' sites to be deleted.
mkdir -p /var/www/customer.test/public
printf 'a customer site\n' > /var/www/customer.test/public/index.html

if "$DIST/install.sh" uninstall --yes > /tmp/uninstall.log 2>&1; then
  pass "uninstall completed"
else
  fail "uninstall failed"
  tail -20 /tmp/uninstall.log | sed 's/^/        /'
fi

if [ -d /opt/jothost ]; then fail "/opt/jothost is still there"; else pass "/opt/jothost is gone"; fi
if [ -f "$vhost" ]; then fail "the panel's vhost is still there"; else pass "the panel's vhost is gone"; fi
if pgrep -f jothost-api >/dev/null 2>&1; then fail "the API is still running"; else pass "the API is stopped"; fi
if pgrep -f jothost-agent >/dev/null 2>&1; then fail "the Agent is still running"; else pass "the Agent is stopped"; fi

# Kept without --purge: an operator reinstalling should not lose the panel's
# records because they removed the binaries.
if [ -f /etc/jothost/api.env ]; then
  pass "the configuration is kept, so a reinstall reuses it"
else
  fail "the configuration was removed without --purge"
fi
if su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='jothost'\"" 2>/dev/null | grep -q 1; then
  pass "the panel's database is kept"
else
  fail "the panel's database was dropped without --purge"
fi
if [ -f /var/www/customer.test/public/index.html ]; then
  pass "a customer's website is untouched"
else
  fail "a customer's website was deleted"
fi

log ""
log "12. Purge removes the panel's own data, and still not the customers'"

if "$DIST/install.sh" uninstall --purge --yes > /tmp/purge.log 2>&1; then
  pass "purge completed"
else
  fail "purge failed"
  tail -20 /tmp/purge.log | sed 's/^/        /'
fi

if su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='jothost'\"" 2>/dev/null | grep -q 1; then
  fail "the panel's database survived --purge"
else
  pass "the panel's database is gone"
fi
if [ -d /etc/jothost ]; then fail "the configuration survived --purge"; else pass "the configuration is gone"; fi
if [ -f /var/www/customer.test/public/index.html ]; then
  pass "a customer's website is still untouched, even by --purge"
else
  fail "--purge deleted a customer's website"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 23 checks passed."
  exit 0
fi
log "$failures Phase 23 check(s) failed."
exit 1
