#!/bin/sh
# Site directory ownership — the delete-then-create cycle.
#
# The defect this guards against, in the order it happens:
#
#   1. A website is deleted. Its files are kept, which is deliberate: a deleted
#      vhost can be recreated, deleted content cannot.
#   2. Its system account is removed, and the uid goes back into the pool.
#   3. Enough sites are created afterwards that the pool comes round again, and
#      one of them is handed the number written on the old files.
#   4. That customer now owns a previous customer's directory. Nothing said so.
#
# It was found from the far end: Phase 7.1's FTP checks failed because a file
# uploaded to one site appeared to be owned by a different site's account.
#
# The checks below drive the panel's own API rather than the filesystem, then
# look at the filesystem to see what the panel actually did — because every
# part of this bug was invisible from inside the panel, which is why it lasted.
#
# Run with:  make docker-test-site-ownership

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
FIRST_DOMAIN="own1-$STAMP.test"
SECOND_DOMAIN="own2-$STAMP.test"

failures=0
token=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# A control is a check that the thing under test was actually reached. Every
# assertion below is about a directory on the Agent's filesystem, and a
# mistyped path answers every question with silence — so the setup is proved
# before anything is concluded from it.
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

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 30 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

json_field() { printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1; }

await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 90 ]; do
    state="$(json_field "$(api GET "/api/v1/jobs/$job_id")" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

# The Agent's filesystem is the subject, and this suite runs inside the Agent
# container, so these read it directly.
owner_uid()  { stat -c %u "$1" 2>/dev/null || printf 'missing'; }
owner_name() { stat -c %U "$1" 2>/dev/null || printf 'missing'; }

log "Site directory ownership — delete, then create"
log ""

token=$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
  sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
[ -n "$token" ] || { log "FATAL: could not sign in as $ADMIN_USER"; exit 1; }

cleanup() {
  for domain in "$FIRST_DOMAIN" "$SECOND_DOMAIN"; do
    id=$(api GET /api/v1/websites | tr '{' '\n' | grep "\"primary_domain\":\"$domain\"" |
      grep -o '"id":"[^"]*"' | head -n 1 | sed 's/^[^:]*:"//; s/"$//')
    if [ -n "$id" ]; then
      job=$(json_field "$(api DELETE "/api/v1/websites/$id?remove_files=true")" id)
      [ -n "$job" ] && await_job "$job" >/dev/null
    fi
  done
  rm -rf "/var/www/$FIRST_DOMAIN" "/var/www/$SECOND_DOMAIN" 2>/dev/null || true
  return 0
}
trap cleanup EXIT

# ------------------------------------------------------ 1. a site is created

log "1. A website is created"

created=$(api POST /api/v1/websites "{\"domain\":\"$FIRST_DOMAIN\"}")
website_id=$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')
job_id=$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')
system_user=$(json_field "$created" system_user)

if [ -z "$website_id" ] || [ -z "$job_id" ]; then
  log "FATAL: could not create a website: $(printf '%s' "$created" | head -c 300)"
  exit 1
fi

control "the provisioning job finished" "$(await_job "$job_id")" "SUCCESS"

SITE_ROOT="/var/www/$FIRST_DOMAIN"
control "the site directory exists" "$([ -d "$SITE_ROOT" ] && echo yes || echo no)" "yes"

first_uid="$(owner_uid "$SITE_ROOT")"
if [ "$(owner_name "$SITE_ROOT")" = "$system_user" ]; then
  pass "the site directory belongs to its own account ($system_user, uid $first_uid)"
else
  fail "the site directory belongs to $(owner_name "$SITE_ROOT"), not $system_user"
fi

# Something worth not handing to a stranger.
printf '<?php $db_password = "hunter2";' > "$SITE_ROOT/public/config.php"
chown "$first_uid" "$SITE_ROOT/public/config.php"
control "the site has content owned by its account" \
  "$(owner_uid "$SITE_ROOT/public/config.php")" "$first_uid"

# ------------------------------------------------- 2. deleted, files retained

log ""
log "2. The website is deleted and its files are kept"

# No remove_files: the default is to keep them, and that default is the subject
# of everything below.
delete_job=$(json_field "$(api DELETE "/api/v1/websites/$website_id")" id)
[ -n "$delete_job" ] || { log "FATAL: delete was not queued"; exit 1; }
control "the deletion job finished" "$(await_job "$delete_job")" "SUCCESS"

# The retention is the documented behaviour, so it is asserted rather than
# assumed: if a later change starts deleting the files, everything below stops
# testing anything and this says so.
control "the files were kept, as the design intends" \
  "$([ -f "$SITE_ROOT/public/config.php" ] && echo yes || echo no)" "yes"

if [ "$(owner_name "$SITE_ROOT")" = "$system_user" ] && id "$system_user" >/dev/null 2>&1; then
  fail "the account still exists after deletion, so this proves nothing yet"
else
  pass "the system account is gone"
fi

# This is the fix. Nothing in the retained tree may still carry the freed uid,
# because that number is now in the pool and will be handed out again.
still_owned=$(find "$SITE_ROOT" -uid "$first_uid" 2>/dev/null | head -5)
if [ -z "$still_owned" ]; then
  pass "nothing in the retained tree still carries the freed uid $first_uid"
else
  fail "the freed uid $first_uid still owns files that will change hands:
$still_owned"
fi

if [ "$(owner_uid "$SITE_ROOT")" = "0" ]; then
  pass "the retained directory belongs to root, the one uid never reissued"
else
  fail "the retained directory is owned by uid $(owner_uid "$SITE_ROOT"), not root"
fi

perm=$(stat -c %a "$SITE_ROOT" 2>/dev/null || echo none)
if [ "$perm" = "700" ]; then
  pass "and it is closed to everyone but root"
else
  fail "the retained directory is mode $perm, not 700"
fi

# ------------------------------------------- 3. the next site cannot take it

log ""
log "3. A new website cannot be provisioned into what was left behind"

# The panel derives a document root from the domain, so reaching the retained
# directory means asking for the same domain again — which is exactly what an
# operator recreating a deleted site does.
recreated=$(api POST /api/v1/websites "{\"domain\":\"$FIRST_DOMAIN\"}")
recreate_job=$(printf '%s' "$recreated" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')
recreate_id=$(printf '%s' "$recreated" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')

if [ -z "$recreate_job" ]; then
  fail "recreating the domain was refused before it reached the host, so the
       provisioning guard was never exercised: $(printf '%s' "$recreated" | head -c 200)"
else
  state="$(await_job "$recreate_job")"
  if [ "$state" = "FAILED" ]; then
    pass "provisioning into another account's directory failed rather than adopting it"
  else
    fail "provisioning into the retained directory reported $state — it was adopted"
  fi

  # A refusal the operator cannot act on is barely better than none.
  detail=$(api GET "/api/v1/jobs/$recreate_job")
  case "$detail" in
    *"already holds files"*) pass "the failure says what is in the way" ;;
    *) fail "the failure does not explain itself: $(printf '%s' "$detail" | head -c 200)" ;;
  esac

  # And the previous customer's file is untouched and still not theirs.
  if [ "$(owner_uid "$SITE_ROOT/public/config.php")" = "0" ]; then
    pass "the retained content did not change hands"
  else
    fail "the retained content is now owned by uid $(owner_uid "$SITE_ROOT/public/config.php")"
  fi

  if [ -n "$recreate_id" ]; then
    job=$(json_field "$(api DELETE "/api/v1/websites/$recreate_id?remove_files=true")" id)
    [ -n "$job" ] && await_job "$job" >/dev/null
  fi
fi

# ----------------------------------------- 4. an unrelated site is unaffected

log ""
log "4. An ordinary site is still created normally"

# The guard refuses a directory that belongs to somebody else. It must not have
# become a guard against creating sites at all, which is the way a fix like
# this fails.
second=$(api POST /api/v1/websites "{\"domain\":\"$SECOND_DOMAIN\"}")
second_job=$(printf '%s' "$second" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')
second_user=$(json_field "$second" system_user)

if [ -z "$second_job" ]; then
  fail "a fresh domain could not be created at all: $(printf '%s' "$second" | head -c 200)"
else
  if [ "$(await_job "$second_job")" = "SUCCESS" ]; then
    pass "a site on a fresh domain is created as before"
  else
    fail "a site on a fresh domain failed to provision"
  fi
  if [ "$(owner_name "/var/www/$SECOND_DOMAIN")" = "$second_user" ]; then
    pass "and it belongs to its own account ($second_user)"
  else
    fail "the new site belongs to $(owner_name "/var/www/$SECOND_DOMAIN"), not $second_user"
  fi
fi

# --------------------------------------------------------- 5. the host audit

log ""
log "5. The Agent reports directories nobody owns"

# The fix stops new orphans. It does nothing for a host that already has them,
# so the Agent has to be able to say so — otherwise an upgraded machine stays
# quietly broken and nobody finds out until the uid comes round.
audit=$(/usr/local/bin/jothost-agent -repair-site-ownership 2>&1 || true)
case "$audit" in
  *"site directory ownership repaired"*)
    pass "the audit runs and reports what it did"
    ;;
  *)
    fail "the ownership audit did not complete: $(printf '%s' "$audit" | tail -c 300)"
    ;;
esac

orphans=0
for d in /var/www/*/; do
  [ "$(owner_name "${d%/}")" = "UNKNOWN" ] && orphans=$((orphans + 1))
done
if [ "$orphans" -eq 0 ]; then
  pass "no directory is left owned by an account that does not exist"
else
  fail "$orphans directories are still owned by accounts that no longer exist"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All site ownership checks passed."
  exit 0
fi
log "$failures site ownership check(s) failed."
exit 1
