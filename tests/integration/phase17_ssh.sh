#!/bin/sh
# Phase 17 Docker integration test — SSH security.
#
# Black-box checks against the running stack, run inside the Agent's own
# container so that what the panel says can be compared with what `sshd -T`
# says. That comparison is the point: this is the one phase where a change that
# reports success and does nothing would leave an operator believing their host
# is configured a way it is not — and then acting on that belief.
#
# The container runs a real OpenSSH server on a private network with no
# published port, which is what makes it safe for this suite to move the port,
# turn password authentication off, and put it all back.
#
# Run with:  make docker-test-ssh

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

DROP_IN="/etc/ssh/sshd_config.d/10-jothost.conf"
KEY_FILE="/root/.ssh/authorized_keys"

# A real key, generated with ssh-keygen. Its fingerprint is what OpenSSH prints
# for it, so the panel's answer is checked against OpenSSH's rather than against
# its own arithmetic.
TEST_KEY="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPET4elVkXnrnYM7ZqOVA3PS+5lkpIHu7EZtLDnyuhO p17@integration"
TEST_FP="SHA256:YMmWTrFzpPPhr9IXTGTy/ZsV92FDiTJI7maIneV1HeA"

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
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 120 -X "$method" "$API_BASE_URL$path" \
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

# effective NAME — what the running server resolves a directive to.
#
# This is the answer the panel has to agree with. Reading the file instead would
# prove only that the panel can write a file.
effective() {
  sshd -T 2>/dev/null | sed -n "s/^$1 //p" | head -n 1
}

# The host is put back as it was found, whatever happens in between.
restore() {
  if [ -n "${token:-}" ]; then
    api PATCH /api/v1/security/ssh \
      '{"port":22,"password_authentication":true,"pubkey_authentication":true,
        "permit_empty_passwords":false,"x11_forwarding":false,"max_auth_tries":6,
        "root_login":"prohibit-password"}' >/dev/null 2>&1 || true
    api DELETE "/api/v1/security/ssh/keys/$(printf '%s' "$TEST_FP" | sed 's|/|%2F|g; s|+|%2B|g')?account=root" \
      >/dev/null 2>&1 || true
  fi
  # Section 3 empties these to reach the no-key refusal at all. Whatever was
  # there before belongs to the host, not to these checks.
  for backup in /root/.ssh/authorized_keys.p17bak /home/*/.ssh/authorized_keys.p17bak; do
    [ -f "$backup" ] || continue
    cp "$backup" "${backup%.p17bak}" 2>/dev/null || true
    rm -f "$backup" 2>/dev/null || true
  done
  rm -f "$DROP_IN" "$DROP_IN.backup" 2>/dev/null || true
  if command -v rc-service >/dev/null 2>&1; then
    rc-service sshd restart >/dev/null 2>&1 || true
  fi
}
trap restore EXIT

# ------------------------------------------------------------------ the run

log 'Phase 17 — SSH security'
log ''

login
if [ -z "${token:-}" ]; then
  fail 'sign in as the integration administrator'
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'sign in as the integration administrator'

# --- 1. authorization -----------------------------------------------------

log ''
log '1. Authorization'

expect_status 'reading the settings without a token is refused' 401 \
  "$(anon_status GET /api/v1/security/ssh)"
expect_status 'changing them without a token is refused' 401 \
  "$(anon_status PATCH /api/v1/security/ssh)"
expect_status 'adding a key without a token is refused' 401 \
  "$(anon_status POST /api/v1/security/ssh/keys)"

# --- 2. reading -----------------------------------------------------------

log ''
log '2. What the server actually says'

status="$(api GET /api/v1/security/ssh)"
contains 'the panel reports the SSH settings' "$status" '"config":'
contains 'this host has an SSH server' "$status" '"available":true'
contains 'and the panel can change it' "$status" '"managed":true'
contains 'it says which file it writes' "$status" "$DROP_IN"
contains 'and lists the accounts that can log in' "$status" '"name":"root"'

# The panel's answer has to be the server's answer. A panel that parsed the
# configuration file would report a directive that is commented out as unset,
# when it is in force at a default that differs between versions.
port="$(effective port)"
contains "the port the panel reports is the one sshd resolved ($port)" "$status" "\"ports\":[$port]"

root_login="$(effective permitrootlogin)"
contains "the root-login setting matches sshd ($root_login)" "$status" "\"root_login\":\"$root_login\""

# A website's account has a nologin shell, so it cannot log in over SSH and is
# not offered a key it could never use.
not_contains 'website accounts are not offered SSH keys' "$status" '"name":"web_'

# --- 3. the refusals ------------------------------------------------------

log ''
log '3. What the panel refuses'

# The refusal below only happens when no account has a key, and this script did
# not put the host in that state - the backup checks authorise a key for their
# SFTP destination and leave it there, so run after them these checks reported
# a panel that had failed to refuse when it had correctly allowed.
#
# So the precondition is established rather than assumed, and put back
# afterwards. A check that silently depends on the order its suite happens to
# run in is a check that will one day accuse the wrong code.
# Every login account, not just root. The first attempt at this cleared root
# alone and the panel still allowed the change - correctly, because the
# deployment checks leave a key on the gitorigin account. "No account has a
# key" is a statement about the host, and clearing one account does not make
# it true.
for keyfile in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys; do
  [ -s "$keyfile" ] || continue
  cp "$keyfile" "$keyfile.p17bak" 2>/dev/null || true
  : > "$keyfile"
done

remaining=0
for keyfile in /root/.ssh/authorized_keys /home/*/.ssh/authorized_keys; do
  [ -s "$keyfile" ] && remaining=$((remaining + 1))
done
if [ "$remaining" -ne 0 ]; then
  # Not a FAIL of the panel: it is this script saying its own ground is not
  # what the checks below assume, so their result would mean nothing.
  printf '  CTRL  %s account(s) still have a key - the refusal below proves nothing
'     "$remaining"
  failures=$((failures + 1))
else
  printf '  ctrl  no account has a key, so the refusal below is the one meant
'
fi

# The classic way to lose a host, and entirely predictable from here. This is a
# refusal rather than a warning: a warning is something an operator clicks past
# at the end of a long day, and the cost is a machine that needs a console.
refusal="$(api PATCH /api/v1/security/ssh '{"password_authentication":false}')"
contains 'turning passwords off with no key is refused' "$refusal" '"success":false'
contains 'and the refusal says what to do first' "$refusal" 'Add a key first'

# And the file was not touched on the way to refusing.
if [ -f "$DROP_IN" ]; then
  not_contains 'nothing was written while refusing' "$(cat "$DROP_IN")" 'PasswordAuthentication no'
else
  pass 'nothing was written while refusing'
fi
if [ "$(effective passwordauthentication)" = "yes" ]; then
  pass 'and the server still accepts passwords'
else
  fail 'and the server still accepts passwords'
fi

expect_status 'a root-login value sshd does not have is refused' 422 \
  "$(api_status PATCH /api/v1/security/ssh '{"root_login":"maybe"}')"
expect_status 'a port outside the range is refused' 422 \
  "$(api_status PATCH /api/v1/security/ssh '{"port":70000}')"
expect_status 'a request that changes nothing is refused' 422 \
  "$(api_status PATCH /api/v1/security/ssh '{}')"
expect_status 'an unknown field is refused rather than ignored' 400 \
  "$(api_status PATCH /api/v1/security/ssh '{"permit_root_login":"yes"}')"

# --- 4. keys --------------------------------------------------------------

log ''
log '4. Authorised keys'

added="$(api POST /api/v1/security/ssh/keys "{\"account\":\"root\",\"key\":\"$TEST_KEY\"}")"
contains 'a key can be authorised' "$added" '"fingerprint":"SHA256:'
# The fingerprint the panel computes is the one OpenSSH prints for the same key.
contains 'and its fingerprint is the one ssh-keygen prints' "$added" "$TEST_FP"
contains 'with the comment that identifies it' "$added" 'p17@integration'

if [ -f "$KEY_FILE" ]; then
  pass 'the key reached the account'
  contains 'as a line OpenSSH will read' "$(cat "$KEY_FILE")" "$TEST_KEY"
  mode="$(stat -c '%a' "$KEY_FILE" 2>/dev/null || echo unknown)"
  dir_mode="$(stat -c '%a' /root/.ssh 2>/dev/null || echo unknown)"
  # sshd refuses to read either if anyone but the owner can write, and says so
  # only in its own log. A key that silently does not work is what this avoids.
  if [ "$mode" = "600" ] && [ "$dir_mode" = "700" ]; then
    pass 'with the modes sshd requires'
  else
    fail "with the modes sshd requires (file $mode, directory $dir_mode)"
  fi
else
  fail 'the key reached the account'
fi

# The same key twice is the same key: the fingerprint is of the material, not
# of the comment.
expect_status 'the same key is not authorised twice' 409 \
  "$(api_status POST /api/v1/security/ssh/keys "{\"account\":\"root\",\"key\":\"$TEST_KEY renamed\"}")"

for bad in \
  'not-a-key' \
  'ssh-ed25519 not-base64!!' \
  'ssh-dss AAAAB3NzaC1kc3MAAACBAP1 old' \
  'command=\"/bin/sh\" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOPET4elVkXnrnYM7ZqOVA3PS+5lkpIHu7EZtLDnyuhO forced'
do
  code="$(api_status POST /api/v1/security/ssh/keys "{\"account\":\"root\",\"key\":\"$bad\"}")"
  expect_status "a key that is not one is refused: $(printf '%s' "$bad" | head -c 30)" 422 "$code"
done

# The account is matched against the host's own list of login accounts, not
# turned into a path.
for account in 'web_nobody' '../../root' 'nosuchuser'; do
  code="$(api_status POST /api/v1/security/ssh/keys \
    "{\"account\":\"$account\",\"key\":\"$TEST_KEY\"}")"
  expect_status "an account this host does not offer is refused: $account" 404 "$code"
done

# --- 5. a change that actually takes ---------------------------------------

log ''
log '5. A change the server adopts'

# With a key present the same change is allowed: the refusal was about the state
# of the host, not about the setting.
applied="$(api PATCH /api/v1/security/ssh '{"password_authentication":false}')"
contains 'with a key present, passwords can be turned off' "$applied" '"success":true'
contains 'and the server restarted to pick it up' "$applied" '"reloaded":true'

# The check the rest of this phase exists for: what the server resolves, not
# what the panel wrote.
if [ "$(effective passwordauthentication)" = "no" ]; then
  pass 'and sshd now refuses passwords'
else
  fail "and sshd now refuses passwords (it says $(effective passwordauthentication))"
fi

contains 'the drop-in holds the directive' "$(cat "$DROP_IN")" 'PasswordAuthentication no'
mode="$(stat -c '%a' "$DROP_IN" 2>/dev/null || echo unknown)"
if [ "$mode" = "600" ]; then
  pass 'and is private'
else
  fail "and is private (mode $mode)"
fi

# The distribution's own file is not the panel's to edit.
not_contains 'the main sshd_config was not edited' \
  "$(cat /etc/ssh/sshd_config)" 'JotHost'

# A second change must not drop the first: the file is rewritten whole, so
# changing one setting would otherwise turn password authentication back on.
api PATCH /api/v1/security/ssh '{"x11_forwarding":true}' >/dev/null
contains 'a later change keeps the earlier one' "$(cat "$DROP_IN")" 'PasswordAuthentication no'
contains 'and adds the new one' "$(cat "$DROP_IN")" 'X11Forwarding yes'
if [ "$(effective passwordauthentication)" = "no" ] && [ "$(effective x11forwarding)" = "yes" ]; then
  pass 'and the server has both'
else
  fail 'and the server has both'
fi

# The panel sets what it was asked for and nothing else: a directive written at
# its default would pin a value the distribution may later change for good
# reason.
not_contains 'nothing the operator did not ask for is written' "$(cat "$DROP_IN")" 'LoginGraceTime'

# --- 6. the firewall interlock ---------------------------------------------

log ''
log '6. Moving the port'

firewall="$(api GET /api/v1/firewall)"
case "$firewall" in
  *'"enabled":true'*)
    # A port the firewall does not admit is a port nothing can reach. The panel
    # will not open it as a side effect — a firewall change is its own
    # deliberate act — so it refuses and says what to do first.
    refusal="$(api PATCH /api/v1/security/ssh '{"port":2222}')"
    contains 'a port the firewall blocks is refused' "$refusal" '"success":false'
    contains 'and the refusal names the firewall' "$refusal" 'firewall'
    if [ "$(effective port)" = "22" ]; then
      pass 'and the server did not move'
    else
      fail 'and the server did not move'
    fi
    ;;
  *)
    # No firewall is not a closed firewall: a host with nothing filtering admits
    # every port, and refusing on that basis would refuse a safe change.
    applied="$(api PATCH /api/v1/security/ssh '{"port":2222}')"
    contains 'with no firewall running, the port can be moved' "$applied" '"success":true'
    if [ "$(effective port)" = "2222" ]; then
      pass 'and sshd is listening on the new port'
    else
      fail "and sshd is listening on the new port (it says $(effective port))"
    fi
    api PATCH /api/v1/security/ssh '{"port":22}' >/dev/null
    if [ "$(effective port)" = "22" ]; then
      pass 'and it can be moved back'
    else
      fail 'and it can be moved back'
    fi
    ;;
esac

# --- 7. removing the key ---------------------------------------------------

log ''
log '7. Withdrawing a key'

encoded="$(printf '%s' "$TEST_FP" | sed 's|/|%2F|g; s|+|%2B|g')"
removed="$(api DELETE "/api/v1/security/ssh/keys/$encoded?account=root")"
contains 'a key can be withdrawn by fingerprint' "$removed" "$TEST_FP"

if [ -f "$KEY_FILE" ]; then
  not_contains 'and is gone from the account' "$(cat "$KEY_FILE")" 'p17@integration'
else
  pass 'and is gone from the account'
fi

expect_status 'withdrawing a key that is not there is a 404' 404 \
  "$(api_status DELETE "/api/v1/security/ssh/keys/$encoded?account=root")"

# --- 8. the recommendations ------------------------------------------------

log ''
log '8. Recommendations'

status="$(api GET /api/v1/security/ssh)"
contains 'the panel says what is worth changing' "$status" '"findings":'
# With the key gone there is nothing to log in with but a password, which is
# exactly what the audit should say.
contains 'and notices that no account has a key' "$status" 'ssh.no-keys'
# Moving the port is not a security control, and the panel does not pretend it
# is: it is informational, alongside the ones that are not.
contains 'the default port is reported as informational' "$status" '"id":"ssh.default-port","severity":"info"'

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 17 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
