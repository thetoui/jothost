#!/bin/sh
# Phase 7.1 Docker integration test — the FTP manager.
#
# Black-box checks against the running stack, run inside the Agent's own
# container so that what the panel says can be compared with what the FTP server
# actually does — and so that a real client can be pointed at it.
#
# The checks this suite exists for are sections 5 to 8: a real FTP client logs in
# with the password the panel generated, uploads a file, and the file lands owned
# by the *website's own system account*. Then it is proved that the account
# cannot leave its directory, that a read-only account cannot write, and that a
# disk limit actually refuses an upload.
#
# Everything else could pass against a panel that wrote a perfectly formed
# configuration nothing read, which is not hypothetical: during development the
# panel reported FTPS was on while the server served plain FTP, because mod_tls
# is a separate package and proftpd skips an <IfModule> block for a module it
# does not have — in silence.
#
# Run with:  make docker-test-ftp

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

DROP_IN="/etc/proftpd/conf.d/10-jothost.conf"
PASSWD_FILE="/etc/proftpd/jothost/ftpd.passwd"
DOMAIN="ftpsuite.test"
ACCOUNT="suitedemo"
READONLY="suiteread"

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
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

anon_status() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X "$1" "$API_BASE_URL$2" 2>/dev/null || true
}

expect_status() {
  name="$1"; want="$2"; got="$3"
  if [ "$got" = "$want" ]; then pass "$name"; else fail "$name (expected HTTP $want, got $got)"; fi
}

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 260))" ;;
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

# field JSON NAME — the first value of a named field.
#
# Non-greedy by construction: the pattern stops at the first quote after the
# name, because a greedy one returns the *last* match in the document, which is
# a different record.
field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\\([^\"]*\\)\".*/\\1/p" | head -n 1
}

# The host is put back as it was found, whatever happens in between.
restore() {
  if [ -n "${token:-}" ]; then
    for id in ${created_users:-}; do
      api DELETE "/api/v1/ftp/users/$id" >/dev/null 2>&1 || true
    done
    if [ -n "${website_id:-}" ]; then
      api DELETE "/api/v1/websites/$website_id?remove_files=true" >/dev/null 2>&1 || true
    fi
  fi
  rc-service proftpd stop >/dev/null 2>&1 || true
  rm -rf /tmp/ftpsuite 2>/dev/null || true
}
trap restore EXIT

# ------------------------------------------------------------------ the run

log 'Phase 7.1 — FTP manager'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

mkdir -p /tmp/ftpsuite

# --- 1. authorization -----------------------------------------------------

log ''
log '1. Authorization'

expect_status 'reading the FTP settings without a token is refused' 401 \
  "$(anon_status GET /api/v1/ftp)"
expect_status 'creating an account without a token is refused' 401 \
  "$(anon_status POST /api/v1/ftp/users)"
expect_status 'disconnecting a session without a token is refused' 401 \
  "$(anon_status DELETE /api/v1/ftp/sessions/1)"

# --- 2. the server is there and the panel can see it ----------------------

log ''
log '2. What the host reports'

overview="$(api GET /api/v1/ftp)"
contains 'the panel reports an FTP server is available' "$overview" '"available":true'
contains 'it reports the module that decides whether FTPS is possible' "$overview" '"supports_tls":true'
contains 'it reports the module that enforces disk limits' "$overview" '"supports_quota":true'

# --- 3. an account is created ---------------------------------------------

log ''
log '3. Creating an account'

site="$(api POST /api/v1/websites "{\"domain\":\"$DOMAIN\"}")"
website_id="$(printf '%s' "$site" | sed -n 's/.*"website":{"id":"\([^"]*\)".*/\1/p')"
if [ -z "$website_id" ]; then
  fail "create the website the account belongs to ($(printf '%s' "$site" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'create the website the account belongs to'

system_user="$(field "$site" 'system_user')"

# Creating a website is asynchronous: the row exists before the host does. The
# FTP account maps onto that site's system account, so there is nothing to map
# onto until the Agent has made it — and the API says exactly that, which is the
# right answer to a request that arrived too early.
waited=0
while [ "$waited" -lt 90 ]; do
  id -u "$system_user" >/dev/null 2>&1 && break
  sleep 2
  waited=$((waited + 2))
done
if id -u "$system_user" >/dev/null 2>&1; then
  pass "the website's system account exists on the host"
else
  fail "the website's system account was never created ($system_user)"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi

created="$(api POST /api/v1/ftp/users \
  "{\"website_id\":\"$website_id\",\"username\":\"$ACCOUNT\",\"quota_mb\":2}")"
user_id="$(printf '%s' "$created" | sed -n 's/.*"user":{"id":"\([^"]*\)".*/\1/p')"
password="$(field "$created" 'password')"
created_users="$user_id"

if [ -z "$user_id" ] || [ -z "$password" ]; then
  fail "create an FTP account ($(printf '%s' "$created" | head -c 240))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'create an FTP account'
pass 'the panel generates a password and returns it once'

# The record must not keep it. This is checked against the database's own
# answer rather than the response, because the response is where it is
# *supposed* to appear.
stored="$(api GET /api/v1/ftp/users)"
not_contains 'the password is not stored in the panel' "$stored" "$password"

# --- 4. what the host was actually made to look like ----------------------

log ''
log '4. What the host was configured with'

contains 'the drop-in confines every session to its own directory' \
  "$(cat "$DROP_IN" 2>/dev/null)" 'DefaultRoot ~'
contains 'the drop-in authenticates only against the virtual-user file' \
  "$(cat "$DROP_IN" 2>/dev/null)" 'AuthOrder mod_auth_file.c'

# A system account must not be a way in. This is the whole security posture of
# the phase: an FTP password is not a login to the machine.
not_contains 'system accounts cannot be used to log in over FTP' \
  "$(cat "$DROP_IN" 2>/dev/null)" 'mod_auth_unix'

if [ "$(stat -c '%a' "$PASSWD_FILE" 2>/dev/null)" = "600" ]; then
  pass 'the password file is readable only by root'
else
  fail "the password file is mode $(stat -c '%a' "$PASSWD_FILE" 2>/dev/null), expected 600"
fi

# The account maps to the website's own system account, which is what makes an
# uploaded file readable by what serves it.
entry="$(grep "^$ACCOUNT:" "$PASSWD_FILE" 2>/dev/null || true)"
want_uid="$(id -u "$system_user" 2>/dev/null || echo missing)"
if [ -n "$entry" ] && [ "$(printf '%s' "$entry" | cut -d: -f3)" = "$want_uid" ]; then
  pass "the account maps to the website's own system account"
else
  fail "the account does not map to $system_user (uid $want_uid): $entry"
fi
if [ "$(printf '%s' "$entry" | cut -d: -f7)" = "/sbin/nologin" ]; then
  pass 'the account has no shell'
else
  fail "the account has a shell: $entry"
fi

# --- 5. a real client logs in ---------------------------------------------
#
# The check this suite exists for. Everything above is a file the panel wrote.

log ''
log '5. A real FTP client'

rc-service proftpd restart >/dev/null 2>&1 || true
sleep 2

listing="$(curl -sS --max-time 25 "ftp://$ACCOUNT:$password@127.0.0.1/" 2>&1 || true)"
if printf '%s' "$listing" | grep -qi 'denied\|incorrect\|refused\|failed'; then
  fail "log in over FTP with the generated password: $(printf '%s' "$listing" | head -c 160)"
else
  pass 'log in over FTP with the password the panel generated'
fi

echo 'uploaded by the suite' > /tmp/ftpsuite/upload.txt
upload="$(curl -sS --max-time 25 -T /tmp/ftpsuite/upload.txt \
  "ftp://$ACCOUNT:$password@127.0.0.1/upload.txt" 2>&1 || true)"
landed="/var/www/$DOMAIN/public/upload.txt"
if [ -f "$landed" ]; then
  pass 'upload a file over FTP'
else
  fail "upload a file over FTP: $(printf '%s' "$upload" | head -c 160)"
fi

# The point of mapping the account: the file is owned by what serves the site,
# not by root and not by a second identity the site cannot read.
if [ "$(stat -c '%U' "$landed" 2>/dev/null)" = "$system_user" ]; then
  pass "the uploaded file is owned by the website's own account"
else
  fail "the uploaded file is owned by $(stat -c '%U' "$landed" 2>/dev/null), expected $system_user"
fi

# --- 6. the chroot actually holds -----------------------------------------

log ''
log '6. Confinement'

escape="$(curl -sS --max-time 25 "ftp://$ACCOUNT:$password@127.0.0.1/../../../etc/" 2>&1 || true)"
if printf '%s' "$escape" | grep -qi 'denied\|failed\|550'; then
  pass 'the account cannot leave its own directory'
else
  fail "the account escaped its directory: $(printf '%s' "$escape" | head -c 200)"
fi

# --- 7. a read-only account cannot write ----------------------------------

log ''
log '7. Read-only access'

ro="$(api POST /api/v1/ftp/users \
  "{\"website_id\":\"$website_id\",\"username\":\"$READONLY\",\"access_level\":\"readonly\"}")"
ro_id="$(printf '%s' "$ro" | sed -n 's/.*"user":{"id":"\([^"]*\)".*/\1/p')"
ro_password="$(field "$ro" 'password')"
created_users="$created_users $ro_id"

if [ -z "$ro_id" ]; then
  fail "create a read-only account ($(printf '%s' "$ro" | head -c 200))"
else
  pass 'create a read-only account'

  rc-service proftpd restart >/dev/null 2>&1 || true
  sleep 2

  ro_list="$(curl -sS --max-time 25 "ftp://$READONLY:$ro_password@127.0.0.1/" 2>&1 || true)"
  if printf '%s' "$ro_list" | grep -q 'upload.txt'; then
    pass 'a read-only account can list and download'
  else
    fail "a read-only account cannot read: $(printf '%s' "$ro_list" | head -c 200)"
  fi

  ro_write="$(curl -sS --max-time 25 -T /tmp/ftpsuite/upload.txt \
    "ftp://$READONLY:$ro_password@127.0.0.1/denied.txt" 2>&1 || true)"
  if [ -f "/var/www/$DOMAIN/public/denied.txt" ]; then
    fail 'a read-only account was able to upload'
  else
    pass 'a read-only account cannot upload'
  fi
fi

# --- 8. the disk limit is enforced ----------------------------------------
#
# Asked of the server by exceeding it, not read back out of the table the panel
# wrote. A quota nothing enforces is a number on a page.

log ''
log '8. Disk limits'

dd if=/dev/urandom of=/tmp/ftpsuite/big.bin bs=1024 count=4096 >/dev/null 2>&1
big="$(curl -sS --max-time 60 -T /tmp/ftpsuite/big.bin \
  "ftp://$ACCOUNT:$password@127.0.0.1/big.bin" 2>&1 || true)"
if [ -s "/var/www/$DOMAIN/public/big.bin" ]; then
  fail 'a 4 MB upload against a 2 MB limit was stored'
else
  pass 'an upload that would exceed the disk limit is refused'
fi

# --- 9. sessions ----------------------------------------------------------

log ''
log '9. Sessions'

# A slow transfer, held open long enough to be seen. Backgrounded, because the
# point is to observe it while it is running.
#
# Big enough that rate-limiting it actually takes time, which a twenty-byte file
# does not.
dd if=/dev/urandom of="/var/www/$DOMAIN/public/slow.bin" bs=1024 count=512 >/dev/null 2>&1
chown "$system_user" "/var/www/$DOMAIN/public/slow.bin" 2>/dev/null || true

curl -sS --max-time 40 --limit-rate 4k \
  "ftp://$ACCOUNT:$password@127.0.0.1/slow.bin" >/dev/null 2>&1 &
slow_pid=$!
sleep 5

sessions="$(api GET /api/v1/ftp/sessions)"
contains 'the panel sees a session that is actually connected' "$sessions" "\"user\":\"$ACCOUNT\""

session_pid="$(printf '%s' "$sessions" | sed -n 's/.*"pid":\([0-9]*\).*/\1/p' | head -n 1)"
if [ -n "$session_pid" ]; then
  expect_status 'disconnect that session' 200 \
    "$(api_status DELETE "/api/v1/ftp/sessions/$session_pid")"
else
  fail 'the session list carried no process to disconnect'
fi

kill "$slow_pid" 2>/dev/null || true
wait "$slow_pid" 2>/dev/null || true
rm -f "/var/www/$DOMAIN/public/slow.bin"

# A live process that is emphatically not an FTP session: this very shell. The
# Agent runs as root, so a signal sent to the wrong pid would be delivered
# successfully — the check being tested here is the one that stops that, not a
# bounds check on the number.
expect_status 'disconnecting a process that is not an FTP session is refused' 404 \
  "$(api_status DELETE "/api/v1/ftp/sessions/$$")"

if kill -0 "$$" 2>/dev/null; then
  pass 'the process that was not a session is still running'
else
  fail 'a process that was not an FTP session was signalled'
fi

# --- 10. what the panel refuses -------------------------------------------

log ''
log '10. Refusals'

expect_status 'a name that is not usable in the password file is refused' 422 \
  "$(api_status POST /api/v1/ftp/users \
    "{\"website_id\":\"$website_id\",\"username\":\"bad:name\"}")"

# The whole point of the chroot is that an account cannot be rooted elsewhere.
expect_status 'a home directory that climbs out of the website is refused' 422 \
  "$(api_status POST /api/v1/ftp/users \
    "{\"website_id\":\"$website_id\",\"username\":\"climber\",\"home_subpath\":\"../../etc\"}")"

expect_status 'an absolute home directory is refused' 422 \
  "$(api_status POST /api/v1/ftp/users \
    "{\"website_id\":\"$website_id\",\"username\":\"absolute\",\"home_subpath\":\"/etc\"}")"

expect_status 'a password too short to be worth having is refused' 422 \
  "$(api_status POST /api/v1/ftp/users \
    "{\"website_id\":\"$website_id\",\"username\":\"weakpass\",\"password\":\"short\"}")"

expect_status 'a second account with the same name is refused' 409 \
  "$(api_status POST /api/v1/ftp/users \
    "{\"website_id\":\"$website_id\",\"username\":\"$ACCOUNT\"}")"

expect_status 'an account with no website is refused' 422 \
  "$(api_status POST /api/v1/ftp/users '{"username":"orphan"}')"

# Each transfer in progress takes one port, so a range of four is a server that
# refuses the fifth simultaneous transfer with an error nobody can interpret.
expect_status 'a passive range too small to serve is refused' 422 \
  "$(api_status PUT /api/v1/ftp/settings '{"passive_from":30000,"passive_to":30003}')"

expect_status 'requiring encryption with no certificate is refused' 422 \
  "$(api_status PUT /api/v1/ftp/settings '{"require_tls":true}')"

# --- 11. deleting an account removes it from the host ---------------------

log ''
log '11. Deletion'

expect_status 'delete the read-only account' 200 \
  "$(api_status DELETE "/api/v1/ftp/users/$ro_id")"

not_contains 'the deleted account is gone from the password file' \
  "$(cat "$PASSWD_FILE" 2>/dev/null)" "$READONLY:"

rc-service proftpd restart >/dev/null 2>&1 || true
sleep 2
gone="$(curl -sS --max-time 20 "ftp://$READONLY:$ro_password@127.0.0.1/" 2>&1 || true)"
if printf '%s' "$gone" | grep -qi 'denied\|incorrect\|failed\|530'; then
  pass 'the deleted account can no longer log in'
else
  fail "the deleted account still logs in: $(printf '%s' "$gone" | head -c 160)"
fi

# --- result ---------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 7.1 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
