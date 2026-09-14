#!/bin/sh
# Runs the Phase 23 installer checks on one systemd host from
# docker-compose.test.yml, then removes the host.
#
#   sh tests/installer/systemd-host.sh installer-host-ubuntu-2404
#
# systemd has to be PID 1 for systemctl to mean anything, so the host is
# started as a service and the suite is exec'd into it once systemd has
# settled.
#
# Settled is required, not hoped for. The Makefile recipe this replaces waited
# sixty rounds and then ran the suite whether systemd had come up or not, so a
# host that never booted produced a page of installer failures describing
# symptoms of that instead of saying it.

set -eu

SERVICE="${1:?usage: systemd-host.sh <compose service>}"
COMPOSE_TEST="${COMPOSE_TEST:-docker compose -f docker-compose.test.yml}"

cleanup() {
  $COMPOSE_TEST rm -sf "$SERVICE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "Starting $SERVICE"
$COMPOSE_TEST up -d --build "$SERVICE"

echo "Waiting for systemd..."
state=""
waited=0
while [ "$waited" -lt 120 ]; do
  state=$($COMPOSE_TEST exec -T "$SERVICE" systemctl is-system-running 2>/dev/null || true)
  case "$state" in
    running|degraded)
      echo "systemd is $state"
      break
      ;;
  esac
  sleep 2
  waited=$((waited + 2))
done

case "$state" in
  running|degraded) ;;
  *)
    echo "FATAL: systemd on $SERVICE did not settle within 120s (last state: '${state:-none}')" >&2
    echo "Units that failed:" >&2
    $COMPOSE_TEST exec -T "$SERVICE" systemctl --failed --no-legend >&2 2>/dev/null || true
    exit 1
    ;;
esac

# A degraded host is accepted: some unit a container cannot run has failed.
# Which one is printed, because a degraded state that hides a unit the panel
# needs would otherwise pass unnoticed until an installer check tripped on it.
if [ "$state" = degraded ]; then
  echo "Units that failed on the host before installing (the host, not the panel):"
  $COMPOSE_TEST exec -T "$SERVICE" systemctl --failed --no-legend 2>/dev/null || true
fi

$COMPOSE_TEST exec -T "$SERVICE" sh /tests/integration/phase23_installer.sh
