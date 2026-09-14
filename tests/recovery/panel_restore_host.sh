#!/bin/sh
# The steps of the panel restore drill that run inside one installed host.
# Driven by tests/recovery/panel_restore_drill.sh; see there for the story.
#
#   panel_restore_host.sh install
#   panel_restore_host.sh seed      PASSWORD
#   panel_restore_host.sh backup    PASSWORD
#   panel_restore_host.sh check     PASSWORD
#
# Each prints "key=value" lines the drill reads, and "FAIL: ..." for a check
# that did not hold, and exits non-zero if anything failed.

set -u

DIST=${DIST:-/dist}
DOMAIN=${PANEL_DOMAIN:-panel.installer.test}
SITE_DOMAIN=${SITE_DOMAIN:-drill-site.test}
FAILED=0

fail() { printf 'FAIL: %s\n' "$*"; FAILED=1; }
pass() { printf 'pass: %s\n' "$*"; }

api() {
  curl -sk --max-time 60 --resolve "$DOMAIN:443:127.0.0.1" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"
}

field() { sed -n "s/.*\"$1\":\"\\([^\"]*\\)\".*/\\1/p" | head -n 1; }

sign_in() {
  TOKEN=$(curl -sk --max-time 30 --resolve "$DOMAIN:443:127.0.0.1" \
    -X POST "https://$DOMAIN/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$1\"}" | field access_token)
  [ -n "$TOKEN" ]
}

case "${1:-}" in
  install)
    if "$DIST/install.sh" install --domain "$DOMAIN" --admin-user admin \
         --self-signed --health-host 127.0.0.1 --no-firewall --yes >/tmp/install.log 2>&1; then
      printf 'password=%s\n' "$(sed -n 's/^  password  //p' /tmp/install.log)"
    else
      tail -n 30 /tmp/install.log
      exit 1
    fi
    ;;

  seed)
    sign_in "$2" || { fail "sign in"; exit 1; }

    site=$(api -X POST "https://$DOMAIN/api/v1/websites" \
      -d "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"drill site\"}")
    [ -n "$(printf '%s' "$site" | field id)" ] && pass "a website was created" ||
      fail "creating a website: $(printf '%s' "$site" | cut -c1-300)"

    # A stored credential: an S3 destination's secret key is encrypted with
    # ENCRYPTION_KEY. Its endpoint is a closed local port, so a check reaches
    # the Agent and fails to connect - but only after the secret has been
    # decrypted, which is the part the drill is about.
    dest=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations" \
      -d '{"name":"drill s3","kind":"s3","endpoint":"http://127.0.0.1:9","region":"us-east-1","bucket":"drill","access_key":"drill-access","secret_key":"drill-secret-value","path_style":true}')
    DEST_ID=$(printf '%s' "$dest" | field id)
    [ -n "$DEST_ID" ] && pass "a destination with a stored secret was created" ||
      fail "creating the S3 destination: $(printf '%s' "$dest" | cut -c1-300)"

    # The fix this drill found: the Agent could not reach PostgreSQL on an
    # installed host, so a customer's PostgreSQL database could not be made.
    db=$(api -X POST "https://$DOMAIN/api/v1/databases" \
      -d '{"name":"drill_shop","engine":"postgres"}')
    printf '%s' "$db" | grep -q '"success":true' && pass "a PostgreSQL database was created" ||
      fail "creating a PostgreSQL database: $(printf '%s' "$db" | cut -c1-300)"
    ;;

  backup)
    sign_in "$2" || { fail "sign in"; exit 1; }
    mkdir -p /var/lib/jothost/backups
    local_dest=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations" \
      -d '{"name":"drill local","kind":"local","directory":"/var/lib/jothost/backups"}' | field id)
    [ -n "$local_dest" ] || { fail "creating the local destination"; exit 1; }

    created=$(api -X POST "https://$DOMAIN/api/v1/backups" \
      -d "{\"type\":\"panel\",\"destination_id\":\"$local_dest\"}")
    backup_id=$(printf '%s' "$created" | field id)
    [ -n "$backup_id" ] || { fail "requesting a panel backup: $(printf '%s' "$created" | cut -c1-300)"; exit 1; }

    status=""
    waited=0
    while [ "$waited" -lt 180 ]; do
      record=$(api "https://$DOMAIN/api/v1/backups/$backup_id")
      status=$(printf '%s' "$record" | field status)
      case "$status" in completed|verified|failed) break ;; esac
      sleep 3; waited=$((waited + 3))
    done
    case "$status" in
      completed|verified) pass "the panel backup finished ($status)" ;;
      *) fail "the panel backup ended $status: $(printf '%s' "$record" | field error)"; exit 1 ;;
    esac
    printf '%s' "$record" | grep -q '"verified_at":"' && pass "it was read back and verified" ||
      fail "the panel backup was not verified"

    # The record's own path, which follows its destination's name; the manifest
    # inside the record has member paths of its own.
    key=$(printf '%s' "$record" | grep -o '"destination":"[^"]*","path":"[^"]*"' | head -n 1 | sed 's/.*"path":"//; s/"$//')
    archive="/var/lib/jothost/backups/$key"
    [ -f "$archive" ] || { fail "no archive at $archive"; exit 1; }
    head -c 8 "$archive" | grep -q 'JHSEAL1' && pass "the stored archive is sealed" ||
      fail "the stored archive is not sealed"
    if gzip -dc "$archive" 2>/dev/null | grep -qa 'CREATE TABLE'; then
      fail "the panel's schema is readable in the stored archive"
    fi
    printf 'archive=%s\n' "$archive"
    ;;

  check)
    if sign_in "$2"; then
      pass "the administrator from the backup's host signs in"
    else
      fail "the administrator from the backup's host cannot sign in"
      exit 1
    fi

    sites=$(api "https://$DOMAIN/api/v1/websites")
    printf '%s' "$sites" | grep -q "\"$SITE_DOMAIN\"" && pass "the website is listed" ||
      fail "the website is not listed: $(printf '%s' "$sites" | cut -c1-300)"

    dest_id=$(api "https://$DOMAIN/api/v1/backup-destinations" |
      grep -o '"id":"[^"]*","server_id":"[^"]*","name":"drill s3"' | field id)
    if [ -z "$dest_id" ]; then
      fail "the S3 destination is not listed"
    else
      checked=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations/$dest_id/check")
      if printf '%s' "$checked" | grep -q 'stored credentials could not be read'; then
        fail "the stored secret does not decrypt: the key did not come across"
      else
        pass "the stored secret decrypts with the restored key"
      fi
      printf 'check_response=%s\n' "$(printf '%s' "$checked" | cut -c1-300)"
    fi
    ;;

  *)
    echo "usage: $0 install|seed|backup|check [PASSWORD]" >&2
    exit 2
    ;;
esac

exit "$FAILED"
