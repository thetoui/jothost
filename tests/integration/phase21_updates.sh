#!/bin/sh
# Phase 21 Docker integration test — system updates.
#
# The acceptance is not that the API returns 200. It is that the panel reports
# what this host actually has waiting, applies it, and the host afterwards is
# genuinely at the newer version — and, just as importantly, that a panel which
# could not check says so instead of saying "up to date".
#
# A real pending update is created for the test rather than waited for: a
# package is installed from the previous Alpine release, which leaves the host
# genuinely behind by exactly one known package. That is what makes the
# before-and-after checkable rather than a matter of luck about mirror timing.
#
# Three of the things checked here are things this phase got wrong first:
#
#   - apk update and apk version both exit 0 when every repository is
#     unreachable, and print an empty list, so "up to date" and "could not
#     check" are indistinguishable by exit status
#   - apk prints a *different* summary when everything worked, which the first
#     parser did not know — and the conservative default turned that into
#     "not known" rather than a false "up to date"
#   - a version-pinned package appears in apk's version comparison forever and
#     is never touched by an upgrade, so listing it as pending would show a
#     queue that never empties
#
# It runs inside the agent container, which is the managed host in development.
#
# Run with:  make docker-test-updates

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

# The package used as a fixture, and the release its older build comes from.
# git differs between Alpine 3.20 and 3.21, which is what makes it usable.
FIXTURE_PACKAGE="${FIXTURE_PACKAGE:-git}"
FIXTURE_OLD_VERSION="${FIXTURE_OLD_VERSION:-2.45.4-r0}"
OLD_REPO="https://dl-cdn.alpinelinux.org/alpine/v3.20/main"

failures=0
work="$(mktemp -d)"
trap 'cleanup' EXIT

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  # Put the repositories back however this ended.
  if [ -f "$work/repositories" ]; then
    cp "$work/repositories" /etc/apk/repositories
    apk update >/dev/null 2>&1 || true
  fi
  rm -rf "$work"
}

command -v curl >/dev/null 2>&1 || apk add --no-cache curl >/dev/null 2>&1

login() {
  token="$(curl -s --max-time 20 -X POST "$API_BASE_URL/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" 2>/dev/null |
    sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')"
}

api() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 600 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
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
  case "$haystack" in
    *"$needle"*) fail "$name (unexpectedly found '$needle')" ;;
    *) pass "$name" ;;
  esac
}

# installed_version reads one package's version off the host.
#
# From `apk info -v` with no argument, which lists every installed package as
# "name-version". `apk info -v <name>` prints a description instead, which is
# how this helper silently returned nothing the first time it was written. The
# digit in the pattern is what keeps "git-init-template" out of the answer.
installed_version() {
  apk info -v 2>/dev/null | grep -E "^$1-[0-9]" | head -1 | sed -e "s/^$1-//"
}

log '== Phase 21: system updates =='

login
if [ -z "${token:-}" ]; then
  log 'FATAL: could not authenticate against the API'
  exit 1
fi
pass 'authenticated'

cp /etc/apk/repositories "$work/repositories"

# --- a host with nothing outstanding ---------------------------------------

apk update >/dev/null 2>&1
first="$(api POST /api/v1/updates/check)"
contains 'the panel finds this host'"'"'s package manager' "$first" '"manager":"apk"'
contains 'a check against reachable repositories succeeds' "$first" '"succeeded":true'

# apk prints a different summary when everything worked than when something did
# not, and knowing only one of them is how a healthy host gets reported as
# unreadable. This is the check that would have caught that.
not_contains 'a good check is not reported as unreadable' "$first" '"succeeded":false'

overview="$(api GET /api/v1/updates)"
contains 'the overview carries the check' "$overview" '"has_check":true'
contains 'Alpine says it cannot identify security updates' "$overview" '"security_known":false'

# --- a host that cannot be checked -----------------------------------------
#
# The failure this whole phase is built around: with every repository
# unreachable both apk commands exit zero and print nothing, so "up to date"
# and "could not check" look identical.

printf 'https://dl-cdn.alpinelinux.invalid/alpine/v3.21/main\n' > /etc/apk/repositories
broken="$(api POST /api/v1/updates/check)"
contains 'a check that reached nothing is reported as failed' "$broken" '"succeeded":false'
not_contains 'and is not reported as a clean check' "$broken" '"succeeded":true'
contains 'the reason names the repositories' "$broken" 'could not be reached'

# It must also refuse to apply on that basis, rather than "applying nothing"
# and reporting success.
expect_status 'applying on an unusable check is refused' 422 \
  "$(api_status POST /api/v1/updates/apply '{}')"

cp "$work/repositories" /etc/apk/repositories
apk update >/dev/null 2>&1

# --- a real pending update -------------------------------------------------

apk del "$FIXTURE_PACKAGE" >/dev/null 2>&1 || true
if apk add --no-cache --repository "$OLD_REPO" \
    "$FIXTURE_PACKAGE=$FIXTURE_OLD_VERSION" >/dev/null 2>&1; then
  pass "an older $FIXTURE_PACKAGE was installed as a fixture"
else
  log "FATAL: could not install the fixture package; nothing below can be checked"
  exit 1
fi

# apk records an "=version" install as a pin in its world file. The pinned case
# is checked next; the pending case needs it unpinned.
sed -i "s/^$FIXTURE_PACKAGE=.*/$FIXTURE_PACKAGE/" /etc/apk/world
apk update >/dev/null 2>&1

pending="$(api POST /api/v1/updates/check)"
contains 'the pending update is found' "$pending" "\"name\":\"$FIXTURE_PACKAGE\""
contains 'with the version installed now' "$pending" "\"installed\":\"$FIXTURE_OLD_VERSION\""
not_contains 'and the check is not reported as failed' "$pending" '"succeeded":false'

# A package name that is really an option must never reach a program running as
# root.
expect_status 'a package name that is an option is refused' 422 \
  "$(api_status POST /api/v1/updates/apply '{"packages":["--allow-untrusted"]}')"

# --- applying it -----------------------------------------------------------

before="$(installed_version "$FIXTURE_PACKAGE")"
applied="$(api POST /api/v1/updates/apply "{\"packages\":[\"$FIXTURE_PACKAGE\"]}")"
contains 'the update is applied' "$applied" '"status":"succeeded"'
contains 'and the record says what moved' "$applied" "\"from\":\"$FIXTURE_OLD_VERSION\""

after="$(installed_version "$FIXTURE_PACKAGE")"
if [ -n "$after" ] && [ "$after" != "$before" ]; then
  pass "the host is actually at the newer version ($before to $after)"
else
  fail "the host is still at $after"
fi

# A package manager resolves dependencies, so asking for one package routinely
# moves several — and the record has to describe the machine, not the request.
changed="$(printf '%s' "$applied" | tr ',' '\n' | grep -c '"name"' || true)"
if [ "${changed:-0}" -gt 1 ]; then
  pass "the record includes the dependencies that moved with it ($changed packages)"
else
  fail "only $changed package was recorded; dependencies were not read back"
fi

# And the host is clean afterwards.
recheck="$(api POST /api/v1/updates/check)"
not_contains 'the applied update is no longer pending' "$recheck" "\"name\":\"$FIXTURE_PACKAGE\""

history="$(api GET /api/v1/updates/history)"
contains 'the run is in the history' "$history" '"trigger":"manual"'
contains 'with the exact versions it replaced' "$history" "\"from\":\"$FIXTURE_OLD_VERSION\""

# --- a pinned package ------------------------------------------------------
#
# Measured, not assumed: apk lists a pinned package as having something newer
# forever, and correctly never upgrades it. Listing that as pending would show
# an operator a queue that never empties however many times they applied it.

apk del "$FIXTURE_PACKAGE" >/dev/null 2>&1 || true
apk add --no-cache --repository "$OLD_REPO" \
  "$FIXTURE_PACKAGE=$FIXTURE_OLD_VERSION" >/dev/null 2>&1
apk update >/dev/null 2>&1

pinned="$(api POST /api/v1/updates/check)"
if printf '%s' "$pinned" | grep -q '"held":\[\]'; then
  fail 'a pinned package was not reported as held'
else
  pass 'a pinned package is reported as held'
fi
contains 'and the pin itself is named' "$pinned" 'pinned to'

# The important half: it is not in the pending list, where it would be work
# that never gets done.
pending_only="$(printf '%s' "$pinned" | sed 's/"held":.*//')"
not_contains 'a pinned package is not listed as outstanding' \
  "$pending_only" "\"name\":\"$FIXTURE_PACKAGE\""

# --- reverting -------------------------------------------------------------
#
# Not a rollback, and the panel does not pretend otherwise: it asks the package
# manager whether that exact version can still be installed and refuses when it
# cannot. On a host whose repositories carry only the current version, that
# refusal is the ordinary answer.

expect_status 'reverting to a version this host cannot install is refused' 404 \
  "$(api_status POST /api/v1/updates/revert \
    "{\"package\":\"$FIXTURE_PACKAGE\",\"version\":\"0.0.1-r0\"}")"

expect_status 'a revert with a version that is an option is refused' 422 \
  "$(api_status POST /api/v1/updates/revert \
    "{\"package\":\"$FIXTURE_PACKAGE\",\"version\":\"--force\"}")"

# Put the host back to the current version so the fixture leaves nothing behind.
sed -i "s/^$FIXTURE_PACKAGE=.*/$FIXTURE_PACKAGE/" /etc/apk/world
apk upgrade "$FIXTURE_PACKAGE" >/dev/null 2>&1 || true

# --- automatic updates -----------------------------------------------------

settings="$(api PUT /api/v1/updates/settings \
  '{"policy":"security","check_interval_hours":12,"day_of_week":0,"hour":4,"minute":30}')"
contains 'the automatic policy is saved' "$settings" '"policy":"security"'
contains 'with its window' "$settings" '"hour":4'

# "Every day" is -1, and zero is Sunday. A schedule that confused them would
# quietly become weekly.
every_day="$(api PUT /api/v1/updates/settings '{"policy":"off","day_of_week":-1,"hour":3}')"
contains 'every day is recorded as -1 rather than as Sunday' "$every_day" '"day_of_week":-1'

expect_status 'an unknown policy is refused' 422 \
  "$(api_status PUT /api/v1/updates/settings '{"policy":"yes"}')"
expect_status 'an hour outside the day is refused' 422 \
  "$(api_status PUT /api/v1/updates/settings '{"policy":"off","hour":24}')"
expect_status 'a package name that is an option cannot be excluded' 422 \
  "$(api_status PUT /api/v1/updates/settings '{"policy":"off","excluded":["-rf"]}')"

# --- permissions -----------------------------------------------------------

unauth="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 \
  "$API_BASE_URL/api/v1/updates" 2>/dev/null || true)"
expect_status 'reading updates needs authentication' 401 "$unauth"

# --- result ---------------------------------------------------------------

log ''
if [ "$failures" -eq 0 ]; then
  log 'All Phase 21 checks passed.'
  exit 0
fi
log "FAILED: $failures check(s)"
exit 1
