#!/bin/sh
# Runs every integration suite against a running dev stack.
#
# It discovers the scripts rather than listing them, and that is the point.
# Six suites once existed in this directory without `make docker-test` ever
# running them - including four written the same week - because the aggregate
# target was a hand-maintained list and nothing noticed when it fell behind.
# A suite added here is now run by this script the moment it exists.
#
# The other half of that guard is EXTERNAL below. A script that cannot run
# inside the agent container has to be named there, with the target that does
# run it - and an entry naming a script that no longer exists fails this run,
# so the list cannot quietly rot into a set of excuses for suites that are
# gone.
#
# Run with:  make docker-test-suite

set -eu

COMPOSE="${COMPOSE:-docker compose}"
TEST_COMPOSE="${TEST_COMPOSE:-docker compose -f docker-compose.yml -f docker-compose.test.yml}"
INTEGRATION_DIR="${INTEGRATION_DIR:-tests/integration}"

# Suites that cannot run inside the agent container, and what does run them.
#
# Each needs something the agent does not have: a host with nothing installed
# on it, the repository source tree, or a client that is not the machine under
# load. They are listed here so that "not run by this script" is a deliberate
# statement rather than an oversight.
EXTERNAL="
phase23_installer.sh:make docker-test-installer (a clean host)
phase24_security.sh:make docker-test-security-audit (needs the source tree)
phase24_load.sh:make docker-test-load (drives the API from outside)
release.sh:make release-verify (unpacks a built archive)
"

pass=0
fail=0
failed=""

# Every excuse must still name a real file. Without this the list is where a
# deleted or renamed suite goes to be forgotten.
printf '%s' "$EXTERNAL" | while IFS= read -r entry; do
  [ -n "$entry" ] || continue
  named="${entry%%:*}"
  if [ ! -f "$INTEGRATION_DIR/$named" ]; then
    printf 'STALE %s is listed as run elsewhere and does not exist\n' "$named" >&2
    exit 1
  fi
done || exit 1

# Services some suites need. Brought up here rather than assumed: without
# Mailpit the notification suite says FATAL and exits, and without MinIO the
# backup suite reports its S3 destination as broken - both of which read as
# product failures and are neither.
$TEST_COMPOSE up -d mailpit minio minio-init >/dev/null 2>&1 || true

for path in "$INTEGRATION_DIR"/*.sh; do
  name="$(basename "$path")"

  reason="$(printf '%s' "$EXTERNAL" | sed -n "s|^$name:||p")"
  if [ -n "$reason" ]; then
    printf 'SKIP  %-28s %s\n' "$name" "$reason"
    continue
  fi

  # The login throttle is cleared first. phase1_auth.sh deliberately exhausts
  # it to prove it works, and every suite that ran after it then failed to sign
  # in - two dozen red results from one green feature.
  $COMPOSE exec -T redis sh -c \
    "redis-cli --scan --pattern 'jothost:rl:login:*' | xargs -r redis-cli del" \
    >/dev/null 2>&1 || true

  if out="$($COMPOSE exec -T agent sh "/tests/integration/$name" 2>&1)"; then
    printf 'PASS  %s\n' "$name"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s\n' "$name"
    # The failing checks, not the whole transcript: a suite that fails one
    # check should not bury it under two hundred passing lines.
    printf '%s\n' "$out" | grep -E '^ +(FAIL|CTRL)' | head -8 | sed 's/^/        /'
    fail=$((fail + 1))
    failed="$failed $name"
  fi
done

printf '\n%d passed, %d failed\n' "$pass" "$fail"
if [ "$fail" -gt 0 ]; then
  printf 'failing:%s\n' "$failed"
  exit 1
fi
