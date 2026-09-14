#!/bin/sh
# The panel restore drill: rebuild a dead host's panel onto a new one.
#
# docs/PANEL_BACKUP.md, section 8. Two installed hosts, and the only things that
# cross from the first to the second are the two things an operator would have
# kept: a panel backup and the exported key.
#
#   1. Install onto host A. Create a website, a PostgreSQL database and a
#      destination whose secret is stored encrypted.
#   2. Take a panel backup through the API, and export the key.
#   3. Install onto a clean host B, and refuse a restore with the wrong key -
#      leaving B's own panel untouched.
#   4. Restore A's backup onto B with A's key.
#   5. On B: A's administrator signs in, A's website is listed, and A's stored
#      secret decrypts, which is the check that the key really came across.
#
#   sh tests/recovery/panel_restore_drill.sh
#
# Runs on the machine that runs Docker, with dist/ built (make dist).

set -eu

COMPOSE_TEST="${COMPOSE_TEST:-docker compose -f docker-compose.test.yml}"
HOST_A=restore-host-a
HOST_B=restore-host-b
HOST_SCRIPT=/tests/recovery/panel_restore_host.sh
# Where the archive and key wait between the two hosts. DRILL_WORK overrides it
# for a Docker whose idea of /tmp is not this shell's, as on Windows.
if [ -n "${DRILL_WORK:-}" ]; then
  WORK="$DRILL_WORK"; mkdir -p "$WORK"
else
  WORK=$(mktemp -d)
fi
FAILED=0

log()  { printf '%s\n' "$*"; }
fail() { log "  FAIL: $*"; FAILED=1; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] || [ "$FAILED" -ne 0 ]; then
    for host in "$HOST_A" "$HOST_B"; do
      log ""
      log "--- $host: the panel's services"
      $COMPOSE_TEST exec -T "$host" journalctl -u jothost-api -u jothost-agent -n 40 --no-pager 2>/dev/null || true
    done
  fi
  $COMPOSE_TEST rm -sf "$HOST_A" "$HOST_B" >/dev/null 2>&1 || true
  rm -rf "$WORK"
}
trap cleanup EXIT

on() { host="$1"; shift; $COMPOSE_TEST exec -T "$host" "$@"; }

# report prints a host step's output and records its failures.
report() {
  printf '%s\n' "$1" | grep -E '^(pass|FAIL):|^check_response=' | sed 's/^/  /' || true
  printf '%s\n' "$1" | grep -q '^FAIL:' && FAILED=1
  return 0
}

value() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -n 1; }

wait_for_systemd() {
  waited=0
  while [ "$waited" -lt 120 ]; do
    case "$(on "$1" systemctl is-system-running 2>/dev/null || true)" in
      running|degraded) return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  log "FATAL: systemd on $1 did not settle within 120s"
  exit 1
}

install_on() {
  out=$(on "$1" sh "$HOST_SCRIPT" install) || { printf '%s\n' "$out"; log "FATAL: installing onto $1 failed"; exit 1; }
  password=$(value "$out" password)
  [ -n "$password" ] || { log "FATAL: no administrator password from installing onto $1"; exit 1; }
  printf '%s' "$password"
}

log "Starting two hosts"
$COMPOSE_TEST up -d --build "$HOST_A" "$HOST_B"
wait_for_systemd "$HOST_A"
wait_for_systemd "$HOST_B"

log ""
log "1. Host A: install, and give the panel something to lose"
PASSWORD_A=$(install_on "$HOST_A")
log "  installed"
out=$(on "$HOST_A" sh "$HOST_SCRIPT" seed "$PASSWORD_A" || true)
report "$out"

log ""
log "2. Host A: take a panel backup, and export the key"
out=$(on "$HOST_A" sh "$HOST_SCRIPT" backup "$PASSWORD_A" || true)
report "$out"
archive=$(value "$out" archive)
[ -n "$archive" ] || { log "FATAL: host A produced no panel backup"; exit 1; }

out=$(on "$HOST_A" /dist/install.sh export-key --to /root/panel.key 2>&1) || { printf '%s\n' "$out"; log "FATAL: export-key failed"; exit 1; }
mode=$(on "$HOST_A" stat -c %a /root/panel.key)
[ "$mode" = 600 ] && log "  pass: the key file is 0600" || fail "the key file is mode $mode"
if printf '%s\n' "$out" | grep -Eq '[0-9a-f]{64}'; then
  fail "export-key printed the key"
fi
if on "$HOST_A" /dist/install.sh export-key --to /root/panel.key >/dev/null 2>&1; then
  fail "export-key overwrote an existing key file"
else
  log "  pass: export-key refuses to overwrite a key file"
fi

# What an operator would have kept, and nothing else.
$COMPOSE_TEST cp "$HOST_A:$archive" "$WORK/panel.sealed"
$COMPOSE_TEST cp "$HOST_A:/root/panel.key" "$WORK/panel.key"
$COMPOSE_TEST rm -sf "$HOST_A" >/dev/null
log "  host A is gone"

log ""
log "3. Host B: a clean install, and a restore with the wrong key"
PASSWORD_B=$(install_on "$HOST_B")
log "  installed"
$COMPOSE_TEST cp "$WORK/panel.sealed" "$HOST_B:/root/panel.sealed"
$COMPOSE_TEST cp "$WORK/panel.key" "$HOST_B:/root/panel.key"
on "$HOST_B" sh -c 'grep -v "^#" /root/panel.key | tr "0-9a-f" "1-9a-f0" > /root/wrong.key'

if out=$(on "$HOST_B" /dist/install.sh restore-panel --from /root/panel.sealed --key-file /root/wrong.key --yes 2>&1); then
  fail "a restore with the wrong key succeeded"
else
  printf '%s\n' "$out" | grep -q 'could not be opened with this key' &&
    log "  pass: the wrong key is refused, and says so" ||
    fail "the wrong key was refused for another reason: $(printf '%s\n' "$out" | tail -n 5)"
fi
out=$(on "$HOST_B" sh "$HOST_SCRIPT" check "$PASSWORD_B" 2>&1 || true)
printf '%s\n' "$out" | grep -q '^pass: the administrator from the backup' &&
  log "  pass: host B's own administrator still signs in after the refused restore" ||
  fail "host B's own panel was changed by a refused restore"

log ""
log "4. Host B: restore host A's panel"
if out=$(on "$HOST_B" /dist/install.sh restore-panel --from /root/panel.sealed --key-file /root/panel.key --yes 2>&1); then
  log "  pass: restore-panel completed"
else
  printf '%s\n' "$out" | tail -n 30 | sed 's/^/     /'
  log "FATAL: restore-panel failed"
  exit 1
fi

log ""
log "5. Host B: it is host A's panel now"
out=$(on "$HOST_B" sh "$HOST_SCRIPT" check "$PASSWORD_A" || true)
report "$out"
if on "$HOST_B" sh "$HOST_SCRIPT" check "$PASSWORD_B" 2>/dev/null | grep -q '^pass: the administrator from the backup'; then
  fail "host B's old administrator password still works: the database was not replaced"
else
  log "  pass: host B's old administrator password no longer works"
fi
status=$(on "$HOST_B" /dist/install.sh status 2>&1 || true)
printf '%s\n' "$status" | grep -q 'dependencies  ready' &&
  log "  pass: the restored panel reaches PostgreSQL, Redis and the Agent" ||
  fail "the restored panel is not ready: $status"

log ""
if [ "$FAILED" -ne 0 ]; then
  log "The panel restore drill FAILED."
  exit 1
fi
log "The panel restore drill passed."
