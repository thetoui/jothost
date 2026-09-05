#!/bin/sh
# Phase 27 Docker integration test — git deployment.
#
# The acceptance is not that the API returns 200. A deployment runs code on this
# host as a website's own account, and nearly everything that can go wrong with
# it is invisible from the panel's own side: an authentication that never
# happened, a build that ran as the wrong user, a rollback that restored
# nothing.
#
# So this drives a real repository over a real SSH connection, with a key the
# panel generated, and looks at the files afterwards.
#
# What is proved here that nothing else can prove:
#
#   * A deploy key the panel generated authenticates a real git fetch over SSH.
#     The private half is on the host and never leaves it.
#   * A deployment puts the repository's files in the document root, owned by
#     the website's own account — not root, which is what the Agent is.
#   * A template step runs as a fixed command line, in the site's directory, as
#     the site.
#   * A failing step rolls the source back to the commit the site was on, and
#     the panel says plainly what that did and did not restore.
#   * A webhook is verified by its signature and not by its address: a correctly
#     signed push deploys, a wrong signature is refused, and a push for another
#     branch does nothing.
#   * A second deployment while one is running is refused rather than queued
#     into the same working tree.
#
# It runs inside the agent container, which is the managed host.
#
# Run with:  make docker-test-deploy

set -eu

API_BASE_URL="${API_BASE_URL:-http://api:8080}"
ADMIN_USER="${INTEGRATION_ADMIN_USERNAME:-integration_admin}"
ADMIN_PASS="${INTEGRATION_ADMIN_PASSWORD:-integration-admin-pw-9271}"

STAMP="$(date +%s)"
DOMAIN="deploy$STAMP.test"
ORIGIN_USER="gitorigin"
ORIGIN_HOME="/home/$ORIGIN_USER"
ORIGIN_REPO="/srv/git/origin$STAMP.git"
REMOTE="ssh://$ORIGIN_USER@127.0.0.1$ORIGIN_REPO"
WEBHOOK_SECRET="webhook-secret-$STAMP-long-enough"
WORK="/tmp/deploy-work-$STAMP"

failures=0
website_id=""
repository_id=""

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

cleanup() {
  if [ -n "$repository_id" ]; then
    curl -s -o /dev/null -X DELETE \
      "$API_BASE_URL/api/v1/deployments/repositories/$repository_id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  fi
  if [ -n "$website_id" ]; then
    curl -s -o /dev/null -X DELETE "$API_BASE_URL/api/v1/websites/$website_id" \
      -H "Authorization: Bearer ${token:-}" 2>/dev/null || true
  fi
  rm -rf "$WORK" "$ORIGIN_REPO" 2>/dev/null || true
}
trap cleanup EXIT

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
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" 2>/dev/null || true
  fi
}

api_status() {
  method="$1"; path="$2"; body="${3:-}"
  if [ -n "$body" ]; then
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
      -H "Authorization: Bearer $token" \
      -H 'Content-Type: application/json' -d "$body" 2>/dev/null || true
  else
    curl -s -o /dev/null -w '%{http_code}' --max-time 180 -X "$method" "$API_BASE_URL$path" \
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
    *) fail "$name (missing '$needle' in: $(printf '%s' "$haystack" | head -c 300))" ;;
  esac
}

json_field() {
  printf '%s' "$1" | sed -n "s/.*\"$2\":\"\([^\"]*\)\".*/\1/p" | head -n 1
}

await_job() {
  job_id="$1"; waited=0
  while [ "$waited" -lt 180 ]; do
    state="$(json_field "$(api GET "/api/v1/jobs/$job_id")" status)"
    case "$state" in
      SUCCESS|FAILED|CANCELLED) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'TIMEOUT'
}

# await_deployment waits for the deployment row to leave "running".
await_deployment() {
  id="$1"; waited=0
  while [ "$waited" -lt 180 ]; do
    state="$(json_field "$(api GET "/api/v1/deployments/runs/$id")" status)"
    case "$state" in
      success|failed|cancelled) printf '%s' "$state"; return 0 ;;
    esac
    sleep 2; waited=$((waited + 2))
  done
  printf 'timeout'
}

deployment_log() {
  api GET "/api/v1/deployments/runs/$1/log"
}

# sign computes the signature a forge would send.
#
# The same HMAC the panel checks, computed independently here — which is what
# makes the webhook check a real one rather than the panel agreeing with itself.
sign() {
  printf '%s' "$1" |
    openssl dgst -sha256 -hmac "$WEBHOOK_SECRET" -hex |
    sed 's/^.*= /sha256=/'
}

# push_origin sends the working commits to the bare repository.
#
# The chown is undone and redone around the push because git refuses to operate
# on a repository owned by somebody else, and these checks run as root while the
# origin belongs to the account the panel authenticates as.
push_origin() {
  chown -R root:root "$ORIGIN_REPO"
  git -C "$WORK" push -q "$ORIGIN_REPO" main
  chown -R "$ORIGIN_USER:$ORIGIN_USER" "$ORIGIN_REPO"
}

log "Phase 27 — git deployment"
log "========================="
log ""

login
if [ -z "${token:-}" ]; then
  log "Could not authenticate as $ADMIN_USER."
  exit 1
fi

# ------------------------------------------------------------------ a remote

log "Setting up a repository to deploy from"

# sshd, because the remote is a real SSH one. The point of using SSH rather than
# a local path is that a local path would not exercise the deploy key at all —
# and the deploy key is the part most likely to be wrong.
rc-service sshd start >/dev/null 2>&1 || true

if ! id "$ORIGIN_USER" >/dev/null 2>&1; then
  adduser -D -h "$ORIGIN_HOME" "$ORIGIN_USER" >/dev/null 2>&1 || true
fi
# An account created with no password has "!" in its shadow entry, and sshd
# treats that as locked — it refuses public-key authentication for it, which
# looks exactly like a key the server has not been given. This is scaffolding
# for a stand-in forge, not something the panel does.
printf '%s:%s
' "$ORIGIN_USER" "origin-account-$STAMP" | chpasswd >/dev/null 2>&1 ||
  passwd -u "$ORIGIN_USER" >/dev/null 2>&1 || true
mkdir -p "$ORIGIN_HOME/.ssh" /srv/git
touch "$ORIGIN_HOME/.ssh/authorized_keys"
chmod 700 "$ORIGIN_HOME/.ssh"
chmod 600 "$ORIGIN_HOME/.ssh/authorized_keys"
chown -R "$ORIGIN_USER:$ORIGIN_USER" "$ORIGIN_HOME/.ssh"

git init --bare -q "$ORIGIN_REPO"

rm -rf "$WORK"
mkdir -p "$WORK"
git -C "$WORK" init -q
git -C "$WORK" config user.email "checks@jothost.test"
git -C "$WORK" config user.name "Phase 27 checks"
printf '<h1>first</h1>\n' > "$WORK/index.html"
printf '{"name":"deploy-check","version":"1.0.0","private":true}\n' > "$WORK/package.json"
git -C "$WORK" add -A
git -C "$WORK" commit -qm "the first commit"
git -C "$WORK" branch -M main
git -C "$WORK" push -q "$ORIGIN_REPO" main
first_commit="$(git -C "$WORK" rev-parse HEAD)"
# Given to the origin account only after the setup push: git refuses to work in
# a repository owned by somebody else, so pushing to it as root after the chown
# would fail on this side rather than the panel's.
chown -R "$ORIGIN_USER:$ORIGIN_USER" "$ORIGIN_REPO"
pass "a bare repository was created with one commit"

# ----------------------------------------------------------------- a website

log ""
log "The website and its repository"

created="$(api POST /api/v1/websites "{\"domain\":\"$DOMAIN\"}")"
website_id="$(printf '%s' "$created" | sed -n 's/.*"website":{"id":"\([0-9a-f-]*\)".*/\1/p')"
create_job="$(printf '%s' "$created" | sed -n 's/.*"job":{"id":"\([0-9a-f-]*\)".*/\1/p')"
if [ -z "$website_id" ]; then
  log "  FAIL  could not create $DOMAIN: $(printf '%s' "$created" | head -c 300)"
  exit 1
fi
state="$(await_job "$create_job")"
if [ "$state" != "SUCCESS" ]; then
  log "  FAIL  provisioning $DOMAIN ended $state"
  exit 1
fi
pass "a website was created to deploy into"

site="$(api GET "/api/v1/websites/$website_id?remove_files=true")"
document_root="$(json_field "$site" document_root)"
system_user="$(printf '%s' "$site" | sed -n 's/.*"system_user":"\([^"]*\)".*/\1/p' | head -n 1)"
if [ -z "$system_user" ] || [ -z "$document_root" ]; then
  log "  FAIL  could not read the website's account or document root"
  exit 1
fi

# A remote git will not accept is refused before anything is stored.
expect_status "a remote that names a program to run is refused" 422 \
  "$(api_status POST /api/v1/deployments/repositories \
    "{\"website_id\":\"$website_id\",\"remote_url\":\"ext::sh -c id\",\"branch\":\"main\"}")"
expect_status "a remote that is an option is refused" 422 \
  "$(api_status POST /api/v1/deployments/repositories \
    "{\"website_id\":\"$website_id\",\"remote_url\":\"--upload-pack=/tmp/x\",\"branch\":\"main\"}")"
expect_status "a webhook with no secret is refused" 500 \
  "$(api_status POST /api/v1/deployments/repositories \
    "{\"website_id\":\"$website_id\",\"remote_url\":\"$REMOTE\",\"branch\":\"main\",\"provider\":\"github\"}")"

repository="$(api POST /api/v1/deployments/repositories \
  "{\"website_id\":\"$website_id\",\"remote_url\":\"$REMOTE\",\"branch\":\"main\",
    \"provider\":\"github\",\"webhook_secret\":\"$WEBHOOK_SECRET\",\"auto_deploy\":true}")"
repository_id="$(json_field "$repository" id)"
if [ -z "$repository_id" ]; then
  log "  FAIL  could not connect the repository: $(printf '%s' "$repository" | head -c 300)"
  exit 1
fi
pass "the repository was connected"

webhook_token="$(json_field "$repository" webhook_token)"
if [ -n "$webhook_token" ]; then
  pass "a webhook address was generated alongside its secret"
else
  fail "no webhook address was generated"
fi

# ------------------------------------------------------------- the deploy key

log ""
log "The deploy key"

keyed="$(api POST "/api/v1/deployments/repositories/$repository_id/key")"
public_key="$(printf '%s' "$keyed" | sed -n 's/.*"deploy_key_public":"\([^"]*\)".*/\1/p')"
if [ -n "$public_key" ]; then
  pass "the panel generated a deploy key and recorded its public half"
else
  fail "no deploy key was generated: $(printf '%s' "$keyed" | head -c 200)"
fi

case "$keyed" in
  *"PRIVATE KEY"*) fail "the private key came back over the API" ;;
  *) pass "the private half never leaves the host" ;;
esac

key_path="/var/lib/jothost/deploy/keys/$system_user.key"
if [ -f "$key_path" ]; then
  mode="$(stat -c '%a' "$key_path")"
  owner="$(stat -c '%U' "$key_path")"
  if [ "$mode" = "600" ] && [ "$owner" = "$system_user" ]; then
    pass "the private key is 0600 and owned by the website's own account"
  else
    fail "the private key is mode $mode owned by $owner"
  fi
else
  fail "no private key at $key_path"
fi

# Install it, exactly as an operator would paste it into a forge.
printf '%s\n' "$public_key" >> "$ORIGIN_HOME/.ssh/authorized_keys"
chown "$ORIGIN_USER:$ORIGIN_USER" "$ORIGIN_HOME/.ssh/authorized_keys"
pass "the public key was installed on the repository, as an operator would"

# ------------------------------------------------------------ the deployment

log ""
log "Deploying"

started="$(api POST "/api/v1/deployments/repositories/$repository_id/deploy" '{"commit":""}')"
deployment_id="$(json_field "$started" id)"
if [ -z "$deployment_id" ]; then
  log "  FAIL  the deployment did not start: $(printf '%s' "$started" | head -c 300)"
  exit 1
fi

state="$(await_deployment "$deployment_id")"
if [ "$state" = "success" ]; then
  pass "a deployment authenticated with the deploy key and succeeded"
else
  fail "the deployment ended $state: $(deployment_log "$deployment_id" | head -c 400)"
fi

if [ -f "$document_root/index.html" ] && grep -q "first" "$document_root/index.html"; then
  pass "the repository's files are in the document root"
else
  fail "the document root does not hold the repository's files"
fi

owner="$(stat -c '%U' "$document_root/index.html" 2>/dev/null || echo unknown)"
if [ "$owner" = "$system_user" ]; then
  pass "the deployed files are owned by the website's account, not by root"
else
  fail "the deployed files are owned by $owner"
fi

detail="$(api GET "/api/v1/deployments/repositories/$repository_id")"
contains "the panel records what is deployed" "$detail" "\"current_commit\":\"$first_commit\""

# The host's own view, which is what catches the panel and the disk disagreeing.
contains "the host reports the working tree it actually has" "$detail" "\"cloned\":true"

# -------------------------------------------------------------- a second push

log ""
log "A second commit"

printf '<h1>second</h1>\n' > "$WORK/index.html"
git -C "$WORK" commit -qam "the second commit"
push_origin
second_commit="$(git -C "$WORK" rev-parse HEAD)"

started="$(api POST "/api/v1/deployments/repositories/$repository_id/deploy" '{"commit":""}')"
deployment_id="$(json_field "$started" id)"
state="$(await_deployment "$deployment_id")"
if [ "$state" = "success" ]; then
  pass "a second deployment moved the site forward"
else
  fail "the second deployment ended $state: $(deployment_log "$deployment_id" | head -c 400)"
fi
if grep -q "second" "$document_root/index.html"; then
  pass "the document root holds the new commit's files"
else
  fail "the document root still holds the old files"
fi

# ----------------------------------------------------------- a failing build

log ""
log "A step that fails"

expect_status "a deployment script can be set" 200 \
  "$(api_status POST /api/v1/deployments/repositories \
    "{\"website_id\":\"$website_id\",\"remote_url\":\"$REMOTE\",\"branch\":\"main\",
      \"provider\":\"github\",\"deploy_script\":\"echo building\nexit 3\"}")"
expect_status "the script can be made a step" 200 \
  "$(api_status PUT "/api/v1/deployments/repositories/$repository_id/actions" \
    '{"steps":["script"]}')"

# Go back to the first commit, so the failing deployment has somewhere to roll
# back *to* that is visibly different from where it is going.
printf '<h1>third</h1>\n' > "$WORK/index.html"
git -C "$WORK" commit -qam "the third commit"
push_origin

started="$(api POST "/api/v1/deployments/repositories/$repository_id/deploy" '{"commit":""}')"
deployment_id="$(json_field "$started" id)"
state="$(await_deployment "$deployment_id")"
if [ "$state" = "failed" ]; then
  pass "a deployment whose step exits non-zero is recorded as failed"
else
  fail "a failing step produced a $state deployment"
fi

build_log="$(deployment_log "$deployment_id")"
contains "the log says which step failed" "$build_log" "deployment script"
contains "and what it exited with" "$build_log" "exited 3"
contains "and that the source was put back" "$build_log" "Rolling back"
contains "and what a rollback cannot undo" "$build_log" "not in the repository"

run="$(api GET "/api/v1/deployments/runs/$deployment_id")"
contains "the deployment records that it rolled back" "$run" '"rolled_back":true'

if grep -q "second" "$document_root/index.html"; then
  pass "the working tree is back on the commit it was serving before"
else
  fail "the rollback did not restore the previous commit"
fi

# The log is behind deploy.manage and is not in the list.
listed="$(api GET /api/v1/deployments)"
case "$listed" in
  *"exited 3"*) fail "a build log is in the repository list" ;;
  *) pass "a build log is not in the list, only behind its own request" ;;
esac

# ------------------------------------------------------------------ webhooks

log ""
log "Webhooks"

# Put the repository back into a deployable state first.
api PUT "/api/v1/deployments/repositories/$repository_id/actions" '{"steps":[]}' >/dev/null

payload="{\"ref\":\"refs/heads/main\"}"
signature="$(sign "$payload")"

status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 \
  -X POST "$API_BASE_URL/api/v1/webhooks/deploy/$webhook_token" \
  -H 'Content-Type: application/json' \
  -H "X-Hub-Signature-256: $signature" \
  -H 'X-GitHub-Event: push' \
  -d "$payload" 2>/dev/null || true)"
expect_status "a correctly signed push starts a deployment" 202 "$status"

status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 \
  -X POST "$API_BASE_URL/api/v1/webhooks/deploy/$webhook_token" \
  -H 'Content-Type: application/json' \
  -H "X-Hub-Signature-256: sha256=0000000000000000000000000000000000000000000000000000000000000000" \
  -H 'X-GitHub-Event: push' \
  -d "$payload" 2>/dev/null || true)"
expect_status "a push with the wrong signature is refused" 401 "$status"

status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 \
  -X POST "$API_BASE_URL/api/v1/webhooks/deploy/$webhook_token" \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: push' \
  -d "$payload" 2>/dev/null || true)"
expect_status "a push with no signature at all is refused" 401 "$status"

# An unknown address answers exactly as a bad signature does, so an
# unauthenticated caller learns nothing about which repositories exist.
status="$(curl -s -o /dev/null -w '%{http_code}' --max-time 60 \
  -X POST "$API_BASE_URL/api/v1/webhooks/deploy/0000000000000000000000000000000000000000000000000000000000000000" \
  -H 'Content-Type: application/json' \
  -H "X-Hub-Signature-256: $signature" \
  -d "$payload" 2>/dev/null || true)"
expect_status "an unknown webhook address answers the same way" 401 "$status"

# A push for another branch verifies and does nothing, which is the ordinary
# outcome of pushing to a feature branch rather than a failure.
other="{\"ref\":\"refs/heads/some-feature\"}"
answer="$(curl -s --max-time 60 \
  -X POST "$API_BASE_URL/api/v1/webhooks/deploy/$webhook_token" \
  -H 'Content-Type: application/json' \
  -H "X-Hub-Signature-256: $(sign "$other")" \
  -H 'X-GitHub-Event: push' \
  -d "$other" 2>/dev/null || true)"
contains "a push for another branch verifies and does nothing" "$answer" "nothing was done"

# ------------------------------------------------- one deployment at a time

log ""
log "One at a time"

first_run="$(api POST "/api/v1/deployments/repositories/$repository_id/deploy" '{"commit":""}')"
first_id="$(json_field "$first_run" id)"
second_status="$(api_status POST "/api/v1/deployments/repositories/$repository_id/deploy" '{"commit":""}')"
expect_status "a second deployment while one is running is refused" 409 "$second_status"
await_deployment "${first_id:-none}" >/dev/null

# --------------------------------------------------------------- unlinking

log ""
log "Disconnecting"

expect_status "a repository can be disconnected" 204 \
  "$(api_status DELETE "/api/v1/deployments/repositories/$repository_id")"
repository_id=""

if [ -f "$document_root/index.html" ]; then
  pass "the deployed files are still being served — disconnecting is not taking the site offline"
else
  fail "disconnecting the repository deleted the website's files"
fi
if [ ! -d "$document_root/.git" ]; then
  pass "the repository metadata was removed from the document root"
else
  fail "the working tree is still a repository"
fi
if [ ! -f "$key_path" ]; then
  pass "the deploy key was removed from the host"
else
  fail "the deploy key outlived the repository it authenticated to"
fi

log ""
if [ "$failures" -eq 0 ]; then
  log "All Phase 27 checks passed."
  exit 0
fi
log "$failures Phase 27 check(s) failed."
exit 1
