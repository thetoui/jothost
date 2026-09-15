#!/bin/sh
# Run the capacity harness against a freshly installed panel on a systemd host.
#
#   sh tests/capacity/run.sh                       # against a throwaway container
#   TARGET_URL=https://panel.example.com \
#     TARGET_PASSWORD=... sh tests/capacity/run.sh --against-target
#
# The container run is for developing the harness and catching regressions in
# the numbers on the SAME machine over time. The figures it prints are not
# supported limits: a container on a laptop is not the reference hardware, and
# the report says so. To measure a real host, install the panel there and point
# the harness at it with --against-target and the TARGET_* variables.
#
# Build dist/ first (make dist); the container installs from it.

set -eu

COMPOSE_TEST="${COMPOSE_TEST:-docker compose -f docker-compose.test.yml}"
SERVICE=capacity-host
DOMAIN=panel.capacity.test
OUT="${CAPACITY_OUT:-capacity-report.md}"

# Harness parameters, kept modest by default so a laptop run finishes; raise
# them on a real host to find where it bends.
SITES="${CAPACITY_SITES:-15}"
USERS="${CAPACITY_USERS:-20}"
LOAD_DURATION="${CAPACITY_LOAD_DURATION:-30s}"

if [ "${1:-}" = "--against-target" ]; then
  : "${TARGET_URL:?set TARGET_URL}" "${TARGET_PASSWORD:?set TARGET_PASSWORD}"
  exec go run . \
    -url "$TARGET_URL" -user "${TARGET_USER:-admin}" -password "$TARGET_PASSWORD" \
    -insecure -host-spec "${HOST_SPEC:-unspecified target}" \
    -sites "$SITES" -users "$USERS" -load-duration "$LOAD_DURATION" \
    -destination-id "${TARGET_DESTINATION_ID:-}" -out "$OUT"
fi

[ -x dist/install.sh ] || { echo "FATAL: build dist/ first (make dist)"; exit 1; }

cleanup() { $COMPOSE_TEST rm -sf "$SERVICE" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "Starting $SERVICE"
$COMPOSE_TEST up -d --build "$SERVICE"
waited=0
until case "$($COMPOSE_TEST exec -T "$SERVICE" systemctl is-system-running 2>/dev/null || true)" in running|degraded) true ;; *) false ;; esac; do
  [ "$waited" -lt 120 ] || { echo "FATAL: systemd did not settle"; exit 1; }
  sleep 2; waited=$((waited + 2))
done

echo "Installing the panel"
$COMPOSE_TEST exec -T "$SERVICE" /dist/install.sh install \
  --domain "$DOMAIN" --admin-user admin --self-signed --health-host 127.0.0.1 \
  --no-firewall --yes > /tmp/cap-install.log 2>&1 || {
    tail -20 /tmp/cap-install.log; exit 1;
  }
password=$(sed -n 's/^  password  //p' /tmp/cap-install.log)

# A local destination so the backup phase has somewhere to write.
dest=$($COMPOSE_TEST exec -T "$SERVICE" sh -c "
  t=\$(curl -sk --resolve $DOMAIN:443:127.0.0.1 -X POST https://$DOMAIN/api/v1/auth/login \
        -H 'Content-Type: application/json' -d '{\"username\":\"admin\",\"password\":\"$password\"}' \
        | sed -n 's/.*\"access_token\":\"\\([^\"]*\\)\".*/\\1/p')
  mkdir -p /var/lib/jothost/backups
  curl -sk --resolve $DOMAIN:443:127.0.0.1 -X POST https://$DOMAIN/api/v1/backup-destinations \
    -H \"Authorization: Bearer \$t\" -H 'Content-Type: application/json' \
    -d '{\"name\":\"capacity\",\"kind\":\"local\",\"directory\":\"/var/lib/jothost/backups\"}' \
    | sed -n 's/.*\"id\":\"\\([^\"]*\\)\".*/\\1/p' | head -1")

echo "Cross-building the harness (the host image has no Go toolchain)"
# A static linux/amd64 binary built on the developer side and copied in, so the
# host image stays minimal. The panel's own vhost is the numeric-prefix
# catch-all, so 127.0.0.1 reaches it; -insecure accepts the self-signed cert.
docker run --rm -v "$PWD":/src -v jothost-go-cache:/root/.cache \
  -e CGO_ENABLED=0 -w /src/tests/capacity golang:1.26-alpine \
  go build -o /src/tests/capacity/capacity.bin . >/dev/null
$COMPOSE_TEST cp tests/capacity/capacity.bin "$SERVICE":/capacity.bin
rm -f tests/capacity/capacity.bin

echo "Running the harness inside the host"
$COMPOSE_TEST exec -T "$SERVICE" /capacity.bin \
  -url https://127.0.0.1 -user admin -password "$password" -insecure \
  -host-spec 'throwaway container (NOT reference hardware)' \
  -sites "$SITES" -users "$USERS" -load-duration "$LOAD_DURATION" \
  -destination-id "$dest" -out /capacity-report.md || true

$COMPOSE_TEST cp "$SERVICE":/capacity-report.md "$OUT" 2>/dev/null && echo "report: $OUT" || true
