#!/bin/sh
# Phase 10 Docker integration test — scheduled jobs.
#
# Black-box checks against the running stack, run inside the Agent's own
# container so the crontab the panel wrote can be read and the job's output can
# be waited for.
#
# The check this suite exists for is the last one in section 5: a job scheduled
# for every minute, and then a wait of up to two minutes for the host's cron
# daemon to actually run it. Everything else here could pass on a panel that
# wrote a perfectly formed crontab nothing ever read — which is exactly what
# happened during development, twice, for two different reasons: BusyBox crond
# silently ignores a crontab that is not owned by root, and a command with a
# `;` in it has only its last part redirected.
#
# Run with:  make docker-test-cron

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

DOMAIN="p10-cron.test"
SPOOL="/var/spool/cron/crontabs"
JOB_LOGS="/var/log/jothost/cron"

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
    curl -s --max-time 700 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" -H 'Content-Type: application/json' \
      -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 700 -X "$method" "$API_BASE_URL$path" \
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
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 240))" ;;
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

# field JSON NAME — the first value of a string field.
#
# The first, not the last: a sed pattern with a greedy .* matches the final
# occurrence, and on a payload with nested objects that is a different record's
# identifier.
field() {
  printf '%s' "$1" | grep -o "\"$2\":\"[^\"]*\"" | head -n 1 | cut -d'"' -f4
}

cleanup() {
  if [ -n "${token:-}" ] && [ -n "${website_id:-}" ]; then
    for id in ${created_jobs:-}; do
      api DELETE "/api/v1/cron/$id" >/dev/null 2>&1 || true
    done
    api DELETE "/api/v1/websites/$website_id?remove_files=true" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# ------------------------------------------------------------------ the run

log 'Phase 10 — scheduled jobs'
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

expect_status 'listing jobs without a token is refused' 401 "$(anon_status GET /api/v1/cron)"
expect_status 'creating a job without a token is refused' 401 "$(anon_status POST /api/v1/cron)"

# --- 2. a website to schedule for -----------------------------------------

log ''
log '2. A website to schedule for'

# Removed first in case a previous run left it behind.
existing="$(api GET '/api/v1/websites?limit=100')"
old_id="$(printf '%s' "$existing" | tr '{' '\n' | grep -F "\"primary_domain\":\"$DOMAIN\"" |
  sed -n 's/.*"id":"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -n "$old_id" ]; then
  api DELETE "/api/v1/websites/$old_id?remove_files=true" >/dev/null 2>&1 || true
  # Waited for rather than slept through. Deleting a website removes its system
  # account, and a new site with the same domain gets the same account name — so
  # a delete still in flight when the next create finishes removes the account
  # the new site just made, and every job for it then fails with "the account
  # does not exist". That is what happened here before this loop existed.
  waited=0
  while [ "$waited" -lt 60 ]; do
    case "$(api GET '/api/v1/websites?limit=100')" in
      *"\"primary_domain\":\"$DOMAIN\""*) sleep 2; waited=$((waited + 2)) ;;
      *) break ;;
    esac
  done
fi

created="$(api POST /api/v1/websites "{\"domain\":\"$DOMAIN\"}")"
website_id="$(field "$created" id)"
if [ -z "$website_id" ]; then
  fail "create the website ($(printf '%s' "$created" | head -c 200))"
  log ''
  log "FAILED: $failures check(s)"
  exit 1
fi
pass 'create the website the jobs belong to'

# The site is provisioned asynchronously; its account has to exist before a
# crontab can be written for it.
#
# The website's own status is the *first* one in the response: the domains
# below it carry a status too, and theirs is "active" from the moment the
# record exists. Matching the payload as a whole finds that one and concludes
# the site is ready while it is still being built.
waited=0
while [ "$waited" -lt 60 ]; do
  site="$(api GET "/api/v1/websites/$website_id?remove_files=true")"
  status="$(printf '%s' "$site" | grep -o '"status":"[^"]*"' | head -n 1 | cut -d'"' -f4)"
  case "$status" in
    active) break ;;
    failed) fail 'the website was provisioned'; break ;;
  esac
  sleep 2
  waited=$((waited + 2))
done
account="$(field "$site" system_user)"

# A PHP version, because one of the jobs below runs a PHP script and the panel
# resolves the interpreter from the site's own version. Setting it is a queued
# job, so it is waited for rather than assumed.
api PATCH "/api/v1/websites/$website_id/php" '{"version":"8.4"}' >/dev/null 2>&1 || true
waited=0
while [ "$waited" -lt 60 ]; do
  case "$(api GET "/api/v1/websites/$website_id?remove_files=true")" in
    *'"php_version":"8.4"'*) break ;;
  esac
  sleep 2
  waited=$((waited + 2))
done

if [ -n "$account" ]; then
  pass "the website has a system account ($account)"
else
  fail 'the website has a system account'
fi

# --- 3. creating jobs ------------------------------------------------------

log ''
log '3. Creating jobs'

marker="/tmp/p10-fired-$$"
body="{\"website_id\":\"$website_id\",\"name\":\"Minute marker\",\"job_type\":\"command\",
       \"schedule\":\"* * * * *\",\"target\":\"echo ran; touch $marker\"}"
created="$(api POST /api/v1/cron "$body")"
job_id="$(field "$created" id)"
created_jobs="$job_id"

if [ -z "$job_id" ]; then
  fail "create a command job ($(printf '%s' "$created" | head -c 240))"
else
  pass 'create a command job'
  contains 'it is enabled by default' "$created" '"enabled":true'
  contains 'and reports when it will next run' "$created" '"next_run_at":"'
fi

# The @-shorthands are expanded, so everything downstream deals with one
# representation of a schedule.
daily="$(api POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Daily PHP\",
  \"job_type\":\"php\",\"schedule\":\"@daily\",\"target\":\"cron.php\"}")"
daily_id="$(field "$daily" id)"
created_jobs="$created_jobs $daily_id"
contains 'a shorthand schedule is expanded' "$daily" '"schedule":"0 0 * * *"'
# A PHP job is an interpreter the panel chose and a path inside the site's own
# directory. Nothing the caller typed decides either.
contains 'a PHP job runs the interpreter the panel resolved' "$daily" '/usr/bin/php'
contains 'against a script inside the site' "$daily" "/var/www/$DOMAIN/public/cron.php"

url_job="$(api POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Fetch\",
  \"job_type\":\"url\",\"schedule\":\"0 * * * *\",\"target\":\"https://example.test/cron\"}")"
created_jobs="$created_jobs $(field "$url_job" id)"
contains 'a URL job becomes a bounded fetch' "$url_job" 'curl -fsS --max-time 300 https://example.test/cron'

# --- 4. what reached the host ----------------------------------------------

log ''
log '4. What reached the host'

crontab="$(cat "$SPOOL/$account" 2>/dev/null || true)"
contains 'the crontab for that account holds the job' "$crontab" '* * * * *'
contains 'named so it can be found from the panel' "$crontab" "job $job_id"
contains 'and its output is collected in its own log' "$crontab" "$JOB_LOGS/$job_id.log"

# BusyBox crond ignores a crontab that is not owned by root — silently, at any
# log level — so this is the difference between a job that runs and a file that
# looks right.
owner="$(stat -c '%U %a' "$SPOOL/$account" 2>/dev/null || echo 'missing')"
if [ "$owner" = "root 600" ]; then
  pass 'the crontab is root-owned and private'
else
  fail "the crontab is root-owned and private (got '$owner')"
fi

# An environment assignment inside the panel's block would change how the
# customer's own entries below it behave.
not_contains 'the block sets no PATH' "$crontab" 'PATH='
not_contains 'and no MAILTO' "$crontab" 'MAILTO='

# --- 5. the job actually runs ----------------------------------------------

log ''
log '5. The schedule fires'

# This is the check the suite exists for. Everything above could pass against a
# panel whose crontab nothing ever read.
log '  ...waiting up to 130s for the cron daemon to run the job'
waited=0
fired=no
while [ "$waited" -lt 130 ]; do
  if [ -f "$marker" ]; then
    fired=yes
    break
  fi
  sleep 5
  waited=$((waited + 5))
done

if [ "$fired" = yes ]; then
  pass "the host ran the job on its schedule (after ${waited}s)"
else
  fail 'the host ran the job on its schedule'
  log "  ...crond log: $(tail -n 3 /var/log/crond.log 2>/dev/null | tr '\n' ' ')"
fi

if [ -s "$JOB_LOGS/$job_id.log" ]; then
  pass 'and what it printed was collected'
  contains 'into the log that belongs to the job' "$(cat "$JOB_LOGS/$job_id.log")" 'ran'
else
  fail 'and what it printed was collected'
fi

rm -f "$marker"

# --- 6. running a job on demand --------------------------------------------

log ''
log '6. Running a job on demand'

result="$(api POST "/api/v1/cron/$job_id/run")"
contains 'a manual run reports the outcome' "$result" '"status":"success"'
contains 'with the exit code' "$result" '"exit_code":0'
contains 'and what the job printed' "$result" 'ran'

# The run is recorded against the job, so the list shows what happened without
# anybody having to look at a log.
after="$(api GET "/api/v1/cron/$job_id")"
contains 'the job records its last outcome' "$after" '"last_status":"success"'
contains 'and when it ran' "$after" '"last_run_at":"2'

# A manual run goes to the same log as the scheduled ones, so one file holds a
# job's whole history.
contains 'the manual run is in the job log too' \
  "$(cat "$JOB_LOGS/$job_id.log")" 'run from the panel'

# And the log viewer from Phase 11 serves it, which is why this phase built no
# viewer of its own.
pointer="$(api GET "/api/v1/cron/$job_id/logs")"
contains 'the job points at its log source' "$pointer" "\"source\":\"cron.$job_id\""
viewer="$(api GET "/api/v1/logs/cron.$job_id?limit=5")"
contains 'and the log viewer serves it' "$viewer" "$JOB_LOGS/$job_id.log"
contains 'under the name the operator gave it' "$(api GET /api/v1/logs)" '"label":"Minute marker"'

# --- 7. disabling and deleting ---------------------------------------------

log ''
log '7. Disabling and deleting'

api PATCH "/api/v1/cron/$job_id" '{"enabled":false}' >/dev/null
crontab="$(cat "$SPOOL/$account" 2>/dev/null || true)"
not_contains 'a disabled job leaves the crontab entirely' "$crontab" "job $job_id"
# Not commented out: a commented entry is one `crontab -e` away from running,
# and the panel would not know.
not_contains 'and is not left commented out' "$crontab" "#* * * * *"

api PATCH "/api/v1/cron/$job_id" '{"enabled":true}' >/dev/null
contains 're-enabling puts it back' "$(cat "$SPOOL/$account" 2>/dev/null || true)" "job $job_id"

expect_status 'a job can be deleted' 200 "$(api_status DELETE "/api/v1/cron/$job_id")"
not_contains 'and its entry is gone from the host' \
  "$(cat "$SPOOL/$account" 2>/dev/null || true)" "job $job_id"
if [ -f "$JOB_LOGS/$job_id.log" ]; then
  fail 'its log goes with it'
else
  pass 'its log goes with it'
fi
created_jobs="$(printf '%s' "$created_jobs" | sed "s/$job_id//")"

# --- 8. what is refused ----------------------------------------------------

log ''
log '8. Refusals'

# A crontab is a line-oriented format with no quoting. A value with a newline in
# it is not a long value; it is a second entry, running whatever was chosen.
refusal="$(api POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Injected\",
  \"job_type\":\"command\",\"schedule\":\"@daily\",\"target\":\"echo a\\n* * * * * id\"}")"
contains 'a command containing a newline is refused' "$refusal" '"success":false'

# Cron's own escape: an unescaped % ends the command and the rest becomes stdin.
refusal="$(api POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Percent\",
  \"job_type\":\"command\",\"schedule\":\"@daily\",\"target\":\"date +%Y\"}")"
contains 'a command containing % is refused' "$refusal" '"success":false'

for bad_schedule in '99 * * * *' '* * * *' '* * * * * *' '@reboot' '0 0 31 2 * ; id'; do
  code="$(api_status POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Bad\",
    \"job_type\":\"command\",\"schedule\":\"$bad_schedule\",\"target\":\"true\"}")"
  expect_status "a schedule the panel cannot run is refused: $bad_schedule" 422 "$code"
done

# A PHP job names a script inside its own site, and nothing else.
for bad_target in '../../../etc/passwd.php' '/etc/shadow.php' 'cron.php; id' 'cron.sh'; do
  code="$(api_status POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Bad\",
    \"job_type\":\"php\",\"schedule\":\"@daily\",\"target\":\"$bad_target\"}")"
  expect_status "a PHP job cannot name: $bad_target" 422 "$code"
done

# A URL job fetches http or https, and nothing that a shell would read as
# syntax.
for bad_url in 'file:///etc/passwd' 'https://x.test/a;id' 'https://x.test/a$(id)' 'notaurl'; do
  code="$(api_status POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Bad\",
    \"job_type\":\"url\",\"schedule\":\"@daily\",\"target\":\"$bad_url\"}")"
  expect_status "a URL job cannot fetch: $bad_url" 422 "$code"
done

# A job belongs to a website, because a website is what supplies the account it
# runs as. There is no way to ask for one that runs as root.
code="$(api_status POST /api/v1/cron '{"name":"Rootless","job_type":"command",
  "schedule":"@daily","target":"id"}')"
expect_status 'a job with no website is refused' 422 "$code"

expect_status 'a job type the panel does not have is refused' 422 \
  "$(api_status POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Bad\",
    \"job_type\":\"shell\",\"schedule\":\"@daily\",\"target\":\"id\"}")"

expect_status 'an unknown field is refused rather than ignored' 400 \
  "$(api_status POST /api/v1/cron "{\"website_id\":\"$website_id\",\"name\":\"Bad\",
    \"job_type\":\"command\",\"schedule\":\"@daily\",\"target\":\"true\",\"user\":\"root\"}")"

expect_status 'a job that does not exist is a 404' 404 \
  "$(api_status GET /api/v1/cron/00000000-0000-4000-8000-000000000000)"

# --- 9. removing the website -----------------------------------------------

log ''
log '9. When the website goes'

api DELETE "/api/v1/websites/$website_id?remove_files=true" >/dev/null
sleep 3
website_id=''

remaining="$(api GET /api/v1/cron)"
not_contains "the site's jobs go with it" "$remaining" "$DOMAIN"

# And the logs of those jobs, which would otherwise sit in the log viewer for
# ever under a name nothing in the panel could explain.
leftover=0
for id in ${created_jobs:-}; do
  [ -n "$id" ] || continue
  [ -f "$JOB_LOGS/$id.log" ] && leftover=$((leftover + 1))
done
if [ "$leftover" -eq 0 ]; then
  pass 'and the logs of its jobs go too'
else
  fail "and the logs of its jobs go too ($leftover left behind)"
fi
if [ -f "$SPOOL/$account" ]; then
  fail "and its crontab is gone from the host"
else
  pass 'and its crontab is gone from the host'
fi

# --- summary --------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 10 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
