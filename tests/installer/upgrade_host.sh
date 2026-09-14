#!/bin/sh
# The steps of the upgrade test that run inside one host.
# Driven by tests/installer/upgrade.sh; see there for what is being asked.
#
#   upgrade_host.sh install-previous
#   upgrade_host.sh seed     PASSWORD
#   upgrade_host.sh update
#   upgrade_host.sh check    PASSWORD SITE_ID CRON_ID BACKUP_ID S3_ID SITE_BODY_SUM SECRETS_SUM
#
# Each prints "key=value" lines the driver reads and "pass:"/"FAIL:" lines for
# what it checked, and exits non-zero if anything failed.

set -u

PREVIOUS=${PREVIOUS:-/dist-previous}
CANDIDATE=${CANDIDATE:-/dist}
DOMAIN=${PANEL_DOMAIN:-panel.installer.test}
SITE_DOMAIN=upgrade-site.test
CRON_TARGET=https://example.test/upgrade-cron
FAILED=0

fail() { printf 'FAIL: %s\n' "$*"; FAILED=1; }
pass() { printf 'pass: %s\n' "$*"; }

api() {
  curl -sk --max-time 60 --resolve "$DOMAIN:443:127.0.0.1" \
    -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' "$@"
}

# field prints the first value of a string field in a JSON response. The first,
# because a record's own id comes before the ids of anything nested in it.
field() { grep -o "\"$1\":\"[^\"]*\"" | head -n 1 | sed "s/^\"$1\":\"//; s/\"\$//"; }

sign_in() {
  TOKEN=$(curl -sk --max-time 30 --resolve "$DOMAIN:443:127.0.0.1" \
    -X POST "https://$DOMAIN/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"username\":\"admin\",\"password\":\"$1\"}" | field access_token)
  [ -n "$TOKEN" ]
}

# as_api runs the API binary the way the installer does: as its account, with
# its configuration.
as_api() {
  su -s /bin/sh jothost-api -c "set -a; . /etc/jothost/api.env; set +a; /opt/jothost/bin/jothost-api $*"
}

# The secrets an upgrade must carry across unchanged, as one digest so no
# secret is ever printed.
secrets_sum() {
  for key in ENCRYPTION_KEY AGENT_TOKEN DATABASE_URL; do
    sed -n "s/^$key=//p" /etc/jothost/api.env
  done | sha256sum | cut -d' ' -f1
}

site_body_sum() {
  curl -s --max-time 15 -H "Host: $SITE_DOMAIN" http://127.0.0.1/ | sha256sum | cut -d' ' -f1
}

wait_for_backup() {
  status=""
  waited=0
  while [ "$waited" -lt 180 ]; do
    record=$(api "https://$DOMAIN/api/v1/backups/$1")
    status=$(printf '%s' "$record" | field status)
    case "$status" in completed|verified|failed) return 0 ;; esac
    sleep 3; waited=$((waited + 3))
  done
}

case "${1:-}" in
  install-previous)
    if "$PREVIOUS/install.sh" install --domain "$DOMAIN" --admin-user admin \
         --self-signed --health-host 127.0.0.1 --no-firewall --yes >/tmp/install-previous.log 2>&1; then
      printf 'password=%s\n' "$(sed -n 's/^  password  //p' /tmp/install-previous.log)"
      printf 'previous_migration=%s\n' "$(as_api migrate status | awk '$2=="applied"{m=$1} END{print m}')"
    else
      tail -n 30 /tmp/install-previous.log
      exit 1
    fi
    ;;

  seed)
    sign_in "$2" || { fail "sign in to the previous release"; exit 1; }

    site=$(api -X POST "https://$DOMAIN/api/v1/websites" \
      -d "{\"domain\":\"$SITE_DOMAIN\",\"name\":\"upgrade site\"}")
    site_id=$(printf '%s' "$site" | field id)
    [ -n "$site_id" ] || { fail "creating a website: $(printf '%s' "$site" | cut -c1-300)"; exit 1; }
    waited=0; state=""
    while [ "$waited" -lt 90 ]; do
      state=$(api "https://$DOMAIN/api/v1/websites/$site_id" | field status)
      case "$state" in active|failed) break ;; esac
      sleep 3; waited=$((waited + 3))
    done
    [ "$state" = active ] && pass "a website is active" || fail "the website ended $state"
    code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 -H "Host: $SITE_DOMAIN" http://127.0.0.1/)
    [ "$code" = 200 ] && pass "nginx serves it" || fail "nginx answered $code for the website"

    cron=$(api -X POST "https://$DOMAIN/api/v1/cron" \
      -d "{\"website_id\":\"$site_id\",\"name\":\"Upgrade fetch\",\"job_type\":\"url\",\"schedule\":\"0 * * * *\",\"target\":\"$CRON_TARGET\"}")
    cron_id=$(printf '%s' "$cron" | field id)
    [ -n "$cron_id" ] && pass "a cron job was scheduled" || fail "scheduling a cron job: $(printf '%s' "$cron" | cut -c1-300)"

    s3=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations" \
      -d '{"name":"upgrade s3","kind":"s3","endpoint":"http://127.0.0.1:9","region":"us-east-1","bucket":"upgrade","access_key":"upgrade-access","secret_key":"upgrade-secret-value","path_style":true}')
    s3_id=$(printf '%s' "$s3" | field id)
    [ -n "$s3_id" ] && pass "a destination with a stored secret was created" || fail "creating the S3 destination"

    mkdir -p /var/lib/jothost/backups
    local_id=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations" \
      -d '{"name":"upgrade local","kind":"local","directory":"/var/lib/jothost/backups"}' | field id)
    backup=$(api -X POST "https://$DOMAIN/api/v1/backups" \
      -d "{\"type\":\"website\",\"website_id\":\"$site_id\",\"destination_id\":\"$local_id\"}")
    backup_id=$(printf '%s' "$backup" | field id)
    if [ -n "$backup_id" ] && wait_for_backup "$backup_id" && [ "$status" != failed ]; then
      pass "a website backup was taken ($status)"
    else
      fail "the website backup ended ${status:-unrequested}: $(printf '%s' "${record:-$backup}" | field error)"
    fi

    printf 'site_id=%s\ncron_id=%s\nbackup_id=%s\ns3_id=%s\n' "$site_id" "$cron_id" "$backup_id" "$s3_id"
    printf 'site_body_sum=%s\nsecrets_sum=%s\n' "$(site_body_sum)" "$(secrets_sum)"
    ;;

  update)
    if "$CANDIDATE/install.sh" update --from "$CANDIDATE" >/tmp/update.log 2>&1; then
      pass "install.sh update completed"
    else
      tail -n 30 /tmp/update.log
      fail "install.sh update failed"
    fi
    ;;

  check)
    password=$2 site_id=$3 cron_id=$4 backup_id=$5 s3_id=$6 body_before=$7 secrets_before=$8

    status=$(as_api migrate status)
    if printf '%s\n' "$status" | grep -q pending; then
      fail "migrations are pending after the update: $(printf '%s\n' "$status" | grep pending | tr '\n' ' ')"
    else
      pass "every migration is applied, up to $(printf '%s\n' "$status" | awk 'END{print $1}')"
    fi

    [ "$(secrets_sum)" = "$secrets_before" ] && pass "the encryption key, Agent token and database password are unchanged" ||
      fail "a secret in api.env changed across the update"

    sign_in "$password" && pass "the administrator signs in with the password from before" ||
      { fail "the administrator cannot sign in after the update"; exit 1; }

    [ "$(api "https://$DOMAIN/api/v1/websites/$site_id" | field status)" = active ] &&
      pass "the website is still active" || fail "the website is not active after the update"
    [ "$(site_body_sum)" = "$body_before" ] && pass "nginx serves the website the same page" ||
      fail "the website serves something different after the update"

    api "https://$DOMAIN/api/v1/cron" | grep -q "\"id\":\"$cron_id\"" && pass "the cron job is still listed" ||
      fail "the cron job is gone"
    if grep -rqs "$CRON_TARGET" /var/spool/cron/crontabs /var/spool/cron /etc/crontabs 2>/dev/null; then
      pass "and still in the host's crontab"
    else
      fail "the cron job is no longer in any crontab"
    fi

    verified=$(api -X POST "https://$DOMAIN/api/v1/backups/$backup_id/verify")
    printf '%s' "$verified" | grep -q '"ok":true' && pass "the backup taken before the update still verifies" ||
      fail "the backup from before the update does not verify: $(printf '%s' "$verified" | cut -c1-300)"

    checked=$(api -X POST "https://$DOMAIN/api/v1/backup-destinations/$s3_id/check")
    if printf '%s' "$checked" | grep -q 'stored credentials could not be read'; then
      fail "the stored secret no longer decrypts"
    else
      pass "the stored secret still decrypts"
    fi

    # What the previous release could not do on an installed host, and the
    # update is what makes possible: its Agent never reached PostgreSQL.
    db=$(api -X POST "https://$DOMAIN/api/v1/databases" -d '{"name":"upgrade_shop","engine":"postgres"}')
    printf '%s' "$db" | grep -q '"success":true' && pass "a PostgreSQL database can be created now" ||
      fail "creating a PostgreSQL database after the update: $(printf '%s' "$db" | cut -c1-300)"

    # The newest migration rolled back and forward on the populated database,
    # with the API stopped so nothing writes while the schema moves.
    systemctl stop jothost-api
    if as_api migrate down >/tmp/down.log 2>&1 && as_api migrate up >/tmp/up.log 2>&1; then
      pass "the newest migration rolls back and forward on the populated database"
    else
      fail "migrate down/up on the populated database: $(tail -n 3 /tmp/down.log /tmp/up.log | tr '\n' ' ')"
    fi
    systemctl start jothost-api
    waited=0
    until curl -fsS --max-time 5 http://127.0.0.1:8080/readyz >/dev/null 2>&1 || [ "$waited" -ge 60 ]; do
      sleep 2; waited=$((waited + 2))
    done
    sign_in "$password" || { fail "the API did not come back after the migrations"; exit 1; }
    [ "$(api "https://$DOMAIN/api/v1/websites/$site_id" | field status)" = active ] &&
      pass "the website survived the rollback and forward" || fail "the website is gone after migrate down/up"

    local_id=$(api "https://$DOMAIN/api/v1/backup-destinations" |
      grep -o '"id":"[^"]*","server_id":"[^"]*","name":"upgrade local"' | field id)
    panel=$(api -X POST "https://$DOMAIN/api/v1/backups" -d "{\"type\":\"panel\",\"destination_id\":\"$local_id\"}")
    panel_id=$(printf '%s' "$panel" | field id)
    if [ -n "$panel_id" ] && wait_for_backup "$panel_id" && [ "$status" != failed ]; then
      pass "a panel backup can be taken on the upgraded host"
    else
      fail "a panel backup on the upgraded host ended ${status:-unrequested}: $(printf '%s' "${record:-$panel}" | field error)"
    fi

    # And with a panel backup recorded, rolling 0030 back is refused rather
    # than discarding the only record of where the sealed archive went.
    if as_api migrate down >/tmp/refused.log 2>&1; then
      fail "migrate down removed the panel type while a panel backup is recorded"
      as_api migrate up >/dev/null 2>&1
    elif grep -q 'panel backups or schedules are still recorded' /tmp/refused.log; then
      pass "migrate down is refused while a panel backup is recorded"
    else
      fail "migrate down failed for another reason: $(tail -n 3 /tmp/refused.log | tr '\n' ' ')"
    fi

    curl -fsS --max-time 5 http://127.0.0.1:8080/readyz >/dev/null 2>&1 && pass "the panel is ready" ||
      fail "the panel is not ready at the end"
    ;;

  *)
    echo "usage: $0 install-previous|seed|update|check ..." >&2
    exit 2
    ;;
esac

exit "$FAILED"
