#!/bin/sh
# Phase 24 — the Agent's boundary, from inside the machine it manages.
#
# The panel's whole architecture rests on one line: the API is reachable from
# the network and is not privileged; the Agent is privileged and is not
# reachable from the network. Everything else is a consequence.
#
# The audit suite tests the API from outside, which is where an attacker starts.
# This one tests the boundary itself, which is where they would end up: the
# socket between the two, what it accepts, what it refuses, and what the panel
# leaves lying on disk for anybody who did get a foothold.
#
# What is proved here that nothing else can prove:
#
#   * The Agent listens on no network port at all.
#   * Its socket refuses a process outside the group, refuses a uid the panel
#     did not authorise even inside the group, and refuses a caller with no
#     token even as root — three locks, each tested by defeating the ones
#     before it.
#   * It refuses an operation outside its allowlist, and refuses a payload
#     large enough to be an attack on its memory rather than a request.
#   * A symlink out of a website's directory is not followed. This needs a real
#     symlink on a real filesystem, which is why it is here and not in the
#     audit suite.
#   * The secrets on disk are readable by the accounts that need them and by
#     nothing else, and none of them reach a log file.
#
# Run with:  make docker-test-agent-boundary

set -eu

SOCKET="${AGENT_SOCKET:-/run/jothost/agent.sock}"
# The API runs in its own container in the development stack, so it is not on
# this machine's loopback. Naming it by service is what makes the symlink
# checks below actually reach a panel rather than a closed port — and a closed
# port would have reported every read as "refused", which is the shape of a
# pass.
API_BASE_URL="${API_BASE_URL:-http://api:8080}"

failures=0
PROBE_USER=p24probe

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

contains() {
  name="$1"; haystack="$2"; needle="$3"
  case "$haystack" in
    *"$needle"*) pass "$name" ;;
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 200))" ;;
  esac
}

cleanup() {
  deluser "$PROBE_USER" >/dev/null 2>&1 || true
  delgroup "$PROBE_USER" >/dev/null 2>&1 || true
  rm -f /tmp/p24-*.json
  return 0
}
trap cleanup EXIT

command -v socat >/dev/null 2>&1 || apk add --no-cache socat >/dev/null 2>&1 || true

log "Phase 24 — the Agent's boundary"
log ""

# ------------------------------------------------- 1. no network exposure

log "1. The Agent is not on the network"

if [ -S "$SOCKET" ]; then
  pass "the Agent listens on a Unix socket"
else
  fail "no socket at $SOCKET — nothing below could be tested"
  log ""
  log "FAILED: 1 check(s)"
  exit 1
fi

# The Agent must hold no TCP port. Its process is found first, so the check is
# about *this* process rather than about the machine happening to be quiet.
agent_pid=$(pgrep -f 'jothost-agent' 2>/dev/null | head -n 1)
if [ -n "$agent_pid" ]; then
  pass "the Agent is running (pid $agent_pid)"
  listening=$(netstat -ltnp 2>/dev/null || ss -ltnp 2>/dev/null || true)
  if printf '%s' "$listening" | grep -q "$agent_pid/"; then
    fail "the Agent holds a listening TCP port:
$(printf '%s' "$listening" | grep "$agent_pid/")"
  else
    pass "and holds no listening TCP port"
  fi
else
  fail "the Agent process could not be found"
fi

# ------------------------------------------------------ 2. the three locks

log ""
log "2. Three locks on the socket, each tested past the one before it"

mode=$(stat -c '%a' "$SOCKET" 2>/dev/null || echo unknown)
owner=$(stat -c '%U:%G' "$SOCKET" 2>/dev/null || echo unknown)
if [ "$mode" = "660" ]; then
  pass "the socket is 0660 ($owner)"
else
  fail "the socket is $mode, not 0660 ($owner)"
fi

# An account outside the group. The first lock is the filesystem's, and it
# should stop the connection before a byte is spoken.
deluser "$PROBE_USER" >/dev/null 2>&1 || true
delgroup "$PROBE_USER" >/dev/null 2>&1 || true
addgroup -S "$PROBE_USER" >/dev/null 2>&1 || true
adduser -S -D -H -G "$PROBE_USER" -s /sbin/nologin "$PROBE_USER" >/dev/null 2>&1 || true

if id "$PROBE_USER" >/dev/null 2>&1; then
  printf '{"operation":"agent.ping","request_id":"p24"}\n' > /tmp/p24-plain.json
  chmod 644 /tmp/p24-plain.json

  outside=$(su "$PROBE_USER" -s /bin/sh -c \
    "socat -t2 - UNIX-CONNECT:$SOCKET < /tmp/p24-plain.json" 2>&1 || true)
  case "$outside" in
    *"Permission denied"*|*"permission denied"*)
      pass "lock 1: an account outside the group cannot even connect" ;;
    *) fail "an account outside the group reached the socket: $(printf '%s' "$outside" | head -c 150)" ;;
  esac

  # Now defeat lock 1 by putting the account in the group, so lock 2 is the
  # one being tested rather than being hidden behind lock 1.
  addgroup "$PROBE_USER" jothost >/dev/null 2>&1 || \
    adduser "$PROBE_USER" jothost >/dev/null 2>&1 || true

  if id "$PROBE_USER" 2>/dev/null | grep -q jothost; then
    control "the probe account is now in the socket's group" "in" "in"

    inside=$(su "$PROBE_USER" -s /bin/sh -c \
      "socat -t2 - UNIX-CONNECT:$SOCKET < /tmp/p24-plain.json" 2>&1 || true)
    case "$inside" in
      *UNAUTHORIZED*|*"Not authorised"*)
        pass "lock 2: a uid the Agent does not authorise is refused even inside the group" ;;
      *"Permission denied"*)
        fail "the connection was still refused by the filesystem, so lock 2 was not tested" ;;
      *) fail "an unauthorised uid was accepted: $(printf '%s' "$inside" | head -c 150)" ;;
    esac
  else
    fail "the probe account could not be added to the group, so lock 2 was not tested"
  fi
else
  fail "a probe account could not be created, so locks 1 and 2 were not tested"
fi

# Lock 3 is the token, and it is tested as root — which defeats locks 1 and 2
# outright. Without it, anything that reached the filesystem would be the Agent.
root_no_token=$(socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-plain.json 2>&1 || true)
case "$root_no_token" in
  *UNAUTHORIZED*|*"Not authorised"*)
    pass "lock 3: root with no token is refused" ;;
  *) fail "root with no token was accepted: $(printf '%s' "$root_no_token" | head -c 150)" ;;
esac

printf '{"operation":"agent.ping","request_id":"p24","token":"not-the-token"}\n' \
  > /tmp/p24-wrong.json
wrong=$(socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-wrong.json 2>&1 || true)
case "$wrong" in
  *UNAUTHORIZED*|*"Not authorised"*) pass "and root with the wrong token is refused" ;;
  *) fail "a wrong token was accepted: $(printf '%s' "$wrong" | head -c 150)" ;;
esac

# The control for this whole section: with the right token, the Agent answers.
# Without it, every refusal above could be the Agent being down.
if [ -n "${AGENT_TOKEN:-}" ]; then
  printf '{"operation":"agent.ping","request_id":"p24","token":"%s"}\n' "$AGENT_TOKEN" \
    > /tmp/p24-good.json
  good=$(socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-good.json 2>&1 || true)
  case "$good" in
    *'"status":"SUCCESS"'*|*pong*|*OK*)
      printf '  ctrl  the Agent answers a correctly authenticated request\n' ;;
    *)
      printf '  CTRL  the Agent did not answer a correct request (%s) — the refusals above prove nothing\n' \
        "$(printf '%s' "$good" | head -c 120)"
      failures=$((failures + 1)) ;;
  esac
else
  log "        (no AGENT_TOKEN in this environment; the positive control was skipped)"
fi

# ------------------------------------------ 3. what the protocol refuses

log ""
log "3. What the protocol refuses"

if [ -n "${AGENT_TOKEN:-}" ]; then
  # An operation outside the allowlist. The Agent dispatches by a typed
  # constant, so a name it does not know must never reach a handler.
  printf '{"operation":"shell.exec","request_id":"p24","token":"%s","payload":{"command":"id"}}\n' \
    "$AGENT_TOKEN" > /tmp/p24-op.json
  out=$(socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-op.json 2>&1 || true)
  case "$out" in
    *FAILED*|*INVALID*|*"not allowed"*|*UNSUPPORTED*)
      pass "an operation outside the allowlist is refused" ;;
    *) fail "an unknown operation was not refused: $(printf '%s' "$out" | head -c 150)" ;;
  esac

  # The payload limit, tested from both sides.
  #
  # The Agent reads at most 1 MiB of a request, so a client cannot exhaust its
  # memory by streaming a line that never ends. Testing only the refusal would
  # not distinguish a working limit from an Agent that refuses everything, and
  # a first draft of this check sent 100 KB and reported the limit broken
  # because the payload was well inside it.
  filler() {
    i=0
    while [ "$i" -lt "$1" ]; do
      printf 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'
      i=$((i + 1))
    done
  }

  { printf '{"operation":"agent.ping","request_id":"p24","token":"%s","payload":{"x":"'       "$AGENT_TOKEN"
    filler 1024
    printf '"}}
'
  } > /tmp/p24-ok.json
  ok_size=$(wc -c < /tmp/p24-ok.json | tr -d ' ')
  out=$(socat -t5 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-ok.json 2>&1 || true)
  case "$out" in
    *'"status":"SUCCESS"'*)
      printf '  ctrl  a large but permitted payload is accepted (%s bytes)
' "$ok_size" ;;
    *)
      printf '  CTRL  a %s-byte payload was refused, so the limit below proves nothing
' "$ok_size"
      failures=$((failures + 1)) ;;
  esac

  { printf '{"operation":"agent.ping","request_id":"p24","token":"%s","payload":{"x":"'       "$AGENT_TOKEN"
    filler 40000
    printf '"}}
'
  } > /tmp/p24-big.json
  big_size=$(wc -c < /tmp/p24-big.json | tr -d ' ')
  out=$(socat -t5 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-big.json 2>&1 || true)
  case "$out" in
    *'"status":"SUCCESS"'*)
      fail "a payload of $big_size bytes was read and processed past the 1 MiB limit" ;;
    *) pass "a payload past the limit is refused ($big_size bytes)" ;;
  esac

  # Malformed input must not crash the listener: the next request has to work.
  printf 'this is not json at all\n' > /tmp/p24-junk.json
  socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-junk.json >/dev/null 2>&1 || true
  after=$(socat -t2 - "UNIX-CONNECT:$SOCKET" < /tmp/p24-good.json 2>&1 || true)
  case "$after" in
    *'"status":"SUCCESS"'*) pass "the Agent is still answering after malformed input" ;;
    *) fail "the Agent stopped answering after malformed input" ;;
  esac
fi

# ------------------------------------------------- 4. symlinks on a real fs

log ""
log "4. A symlink out of a website's directory"

site_dir=$(find /var/www -maxdepth 1 -mindepth 1 -type d 2>/dev/null | head -n 1)
if [ -n "$site_dir" ] && [ -d "$site_dir/public" ]; then
  probe="$site_dir/public/p24-control.txt"
  printf 'control\n' > "$probe"
  chmod 644 "$probe"
  ln -sfn /etc/passwd "$site_dir/public/p24-escape.txt"
  ln -sfn /etc "$site_dir/public/p24-escape-dir"

  token=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"${INTEGRATION_ADMIN_USERNAME:-integration_admin}\",\"password\":\"${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}\"}" \
    2>/dev/null | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')

  read_code() {
    curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
      "$API_BASE_URL/api/v1/files/content?path=$1" \
      -H "Authorization: Bearer $token" 2>/dev/null || echo 000
  }

  control "an ordinary file in the site can be read" "$(read_code "$probe")" "200"

  for target in "$site_dir/public/p24-escape.txt" \
                "$site_dir/public/p24-escape-dir/passwd"; do
    code=$(read_code "$target")
    case "$code" in
      200) fail "a symlink out of the root was followed: $target" ;;
      *)   pass "a symlink out of the root is not followed ($(basename "$target"))" ;;
    esac
  done

  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
    "$API_BASE_URL/api/v1/files?path=$site_dir/public/p24-escape-dir" \
    -H "Authorization: Bearer $token" 2>/dev/null || echo 000)
  case "$code" in
    200) fail "a symlinked directory was listed" ;;
    *)   pass "a symlinked directory is not listed" ;;
  esac

  rm -f "$site_dir/public/p24-escape.txt" "$site_dir/public/p24-escape-dir" "$probe"
else
  log "        (no provisioned website on this host; symlink checks skipped)"
fi

# ------------------------------------------------------ 5. secrets on disk

log ""
log "5. What is readable, and by whom"

# The Agent's own configuration in the development stack comes from the
# environment rather than a file, so what is checked here is what the panel
# writes: deploy keys, DKIM keys, and the audit log.
check_mode() {
  path="$1"; want="$2"; what="$3"
  [ -e "$path" ] || return 0
  got=$(stat -c '%a' "$path" 2>/dev/null || echo unknown)
  if [ "$got" = "$want" ]; then
    pass "$what is $want"
  else
    fail "$what is $got, expected $want ($path)"
  fi
}

for key in /var/lib/jothost/deploy/keys/*; do
  [ -f "$key" ] || continue
  case "$key" in *.pub) continue ;; esac
  check_mode "$key" 600 "a deploy key's private half"
done

for key in /var/lib/jothost/mail/dkim/*.private /etc/rspamd/dkim/*.key; do
  [ -f "$key" ] || continue
  got=$(stat -c '%a' "$key" 2>/dev/null || echo unknown)
  case "$got" in
    600|640) pass "a DKIM signing key is $got" ;;
    *) fail "a DKIM signing key is $got ($key)" ;;
  esac
done

# The world must not be able to read a site's content: on a shared host that is
# one customer reading another's configuration file.
#
# It is the *last* octal digit that says what everybody else may do, so the
# check reads that digit rather than pattern-matching the mode. `case "$got" in
# *0)` looks like it tests the other-bits and matches 750 as readily as 705,
# which is how the first version of this reported forty-five failures on a
# filesystem that was entirely correct.
world_open=0
for dir in /var/www/*/; do
  [ -d "$dir" ] || continue
  # Only the directories the panel created. /var/www on a stock Alpine already
  # holds localhost and modules, which belong to the nginx package and are
  # meant to be world-readable; judging them by the panel's rule would be
  # reporting somebody else's decision as this panel's failure.
  case "$(stat -c '%U' "$dir" 2>/dev/null)" in
    web_*) ;;
    *) continue ;;
  esac
  got=$(stat -c '%a' "$dir" 2>/dev/null || echo 000)
  other=$(printf '%s' "$got" | tail -c 1)
  [ -n "$other" ] || other=0
  if [ "$other" != "0" ]; then
    fail "a site directory lets the world in: $dir ($got)"
    world_open=$((world_open + 1))
  fi
done
[ "$world_open" = 0 ] && pass "no site directory is readable by the world"

# Nothing that authenticates anything may reach a log file. A token in a log is
# a token in every backup of that log.
for logfile in /var/log/jothost/*.log; do
  [ -f "$logfile" ] || continue
  for secret in "${AGENT_TOKEN:-__no_token__}" "${ENCRYPTION_KEY:-__no_key__}"; do
    case "$secret" in __no_*) continue ;; esac
    if grep -qF "$secret" "$logfile" 2>/dev/null; then
      fail "$(basename "$logfile") contains a secret in plaintext"
    fi
  done
done
pass "no secret appears in the Agent's logs"

# The audit log records the operation and its outcome, and must not record what
# was in the payload — which is where a password would be.
if [ -f /var/log/jothost/agent-audit.log ]; then
  if grep -qi '"password"' /var/log/jothost/agent-audit.log 2>/dev/null; then
    fail "the audit log records a password field"
  else
    pass "the audit log records operations, not payloads"
  fi
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 24 Agent boundary checks passed."
  exit 0
fi
log "$failures Phase 24 Agent boundary check(s) failed."
exit 1
