#!/bin/sh
# The upgrade test: install the previous release, use it, update to this one.
#
#   sh tests/installer/upgrade.sh upgrade-host-debian
#
# Every other installer check installs a build and updates it to itself, which
# proves the update keeps its secrets and nothing about what a real update
# meets: a schema one release behind, data the previous release wrote, and
# services it configured. This installs the previous release from
# dist-previous/, gives it a website, a cron job, a backup and a stored secret
# through its own API, updates to dist/, and checks that all of it survived:
#
#   * the migrations since the previous release apply to its data
#   * the encryption key, Agent token and database password are unchanged
#   * the administrator's password still works
#   * the website is still active and nginx serves it the same page
#   * the cron job is still listed and still in the host's crontab
#   * the backup taken before the update still verifies
#   * the stored secret still decrypts
#   * the newest migration rolls back and forward on the populated database,
#     and is refused while a panel backup is recorded
#
# Build dist-previous/ from the release being upgraded from (make
# dist-previous) and dist/ from this tree (make dist).

set -eu

SERVICE="${1:?usage: upgrade.sh <compose service>}"
COMPOSE_TEST="${COMPOSE_TEST:-docker compose -f docker-compose.test.yml}"
HOST_SCRIPT=/tests/installer/upgrade_host.sh
FAILED=0

log() { printf '%s\n' "$*"; }

[ -x dist-previous/install.sh ] || { log "FATAL: no previous release in dist-previous/ (make dist-previous)"; exit 1; }
[ -x dist/install.sh ] || { log "FATAL: no candidate in dist/ (make dist)"; exit 1; }

cleanup() {
  status=$?
  if [ "$status" -ne 0 ] || [ "$FAILED" -ne 0 ]; then
    log ""
    log "--- $SERVICE: the panel's services"
    $COMPOSE_TEST exec -T "$SERVICE" journalctl -u jothost-api -u jothost-agent -n 40 --no-pager 2>/dev/null || true
  fi
  $COMPOSE_TEST rm -sf "$SERVICE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

on() { $COMPOSE_TEST exec -T "$SERVICE" "$@"; }
value() { printf '%s\n' "$1" | sed -n "s/^$2=//p" | head -n 1; }
report() {
  printf '%s\n' "$1" | grep -E '^(pass|FAIL):' | sed 's/^/  /' || true
  printf '%s\n' "$1" | grep -q '^FAIL:' && FAILED=1
  return 0
}

log "Starting $SERVICE"
$COMPOSE_TEST up -d --build "$SERVICE"
waited=0
until case "$(on systemctl is-system-running 2>/dev/null || true)" in running|degraded) true ;; *) false ;; esac; do
  [ "$waited" -lt 120 ] || { log "FATAL: systemd on $SERVICE did not settle within 120s"; exit 1; }
  sleep 2; waited=$((waited + 2))
done

log ""
log "1. Install the previous release ($(head -n 1 dist-previous/VERSION))"
out=$(on sh "$HOST_SCRIPT" install-previous) || { printf '%s\n' "$out"; log "FATAL: the previous release did not install"; exit 1; }
password=$(value "$out" password)
log "  installed, schema at $(value "$out" previous_migration)"

log ""
log "2. Use it"
out=$(on sh "$HOST_SCRIPT" seed "$password" || true)
report "$out"
for key in site_id cron_id backup_id s3_id site_body_sum secrets_sum; do
  [ -n "$(value "$out" "$key")" ] || { log "FATAL: seeding the previous release produced no $key"; exit 1; }
done
seeded="$out"

log ""
log "3. Update to the candidate"
out=$(on sh "$HOST_SCRIPT" update || true)
report "$out"
printf '%s\n' "$out" | grep -q '^pass: install.sh update completed' || { printf '%s\n' "$out"; exit 1; }

log ""
log "4. What the previous release had, the candidate still has"
check=$(on sh "$HOST_SCRIPT" check "$password" \
  "$(value "$seeded" site_id)" "$(value "$seeded" cron_id)" "$(value "$seeded" backup_id)" \
  "$(value "$seeded" s3_id)" "$(value "$seeded" site_body_sum)" "$(value "$seeded" secrets_sum)" || true)
report "$check"

log ""
if [ "$FAILED" -ne 0 ]; then
  log "The upgrade test FAILED on $SERVICE."
  exit 1
fi
log "The upgrade test passed on $SERVICE."
