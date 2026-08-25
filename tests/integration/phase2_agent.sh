#!/bin/sh
# Phase 2 Docker integration test — Host Agent.
#
# Runs inside the agent container and drives a live Agent through its own
# socket, using the operator diagnostic mode. It verifies that the collectors
# read a real host, that authentication is enforced, that injection-shaped
# input is refused, and that every operation is audited.
#
# Run with:  make docker-test-agent

set -eu

AGENT="${AGENT_BIN:-jothost-agent}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# call OPERATION [PAYLOAD] — prints the response, never fails the script.
call() {
  operation="$1"
  payload="${2:-}"
  if [ -n "$payload" ]; then
    "$AGENT" -call "$operation" -payload "$payload" 2>&1 || true
  else
    "$AGENT" -call "$operation" 2>&1 || true
  fi
}

# expect_success NAME OPERATION [PAYLOAD] [EXPECTED_FIELD]
expect_success() {
  name="$1"; operation="$2"; payload="${3:-}"; field="${4:-}"
  output="$(call "$operation" "$payload")"

  case "$output" in
    *'"status": "SUCCESS"'*) ;;
    *) fail "$name (not successful: $(printf '%s' "$output" | head -4 | tr '\n' ' '))"; return ;;
  esac

  if [ -n "$field" ]; then
    case "$output" in
      *"\"$field\""*) ;;
      *) fail "$name (response is missing $field)"; return ;;
    esac
  fi
  pass "$name"
}

# expect_local_refusal NAME OPERATION
#
# Asserts the client refused the operation itself, without contacting the
# Agent. The message must name the allowlist so an operator can see what is
# actually callable.
expect_local_refusal() {
  name="$1"; operation="$2"
  output="$(call "$operation")"

  case "$output" in
    *"unknown operation"*)
      case "$output" in
        *"allowlisted operations"*) pass "$name" ;;
        *) fail "$name (refusal did not name the allowlist)" ;;
      esac
      ;;
    *) fail "$name (not refused: $(printf '%s' "$output" | head -4 | tr '\n' ' '))" ;;
  esac
}

# expect_error NAME EXPECTED_CODE OPERATION [PAYLOAD]
expect_error() {
  name="$1"; code="$2"; operation="$3"; payload="${4:-}"
  output="$(call "$operation" "$payload")"

  case "$output" in
    *"\"code\": \"$code\""*) pass "$name" ;;
    *) fail "$name (expected $code, got: $(printf '%s' "$output" | head -6 | tr '\n' ' '))" ;;
  esac
}

log "Phase 2 Host Agent checks"
log ""

# ------------------------------------------------------------ reachability

log "Agent reachability"
if "$AGENT" -ping >/dev/null 2>&1; then
  pass "agent responds to -ping"
else
  fail "agent did not respond to -ping"
  log ""
  log "FAILED: cannot continue without a running agent"
  exit 1
fi

expect_success "agent.info reports capabilities" "agent.info" "" "capabilities"
expect_success "agent.info lists its operations" "agent.info" "" "operations"

# --------------------------------------------------------------- collectors

log ""
log "Collectors against the real host"
expect_success "system.info reports the kernel"  "system.info"     "" "kernel_version"
expect_success "system.info reports uptime"      "system.info"     "" "uptime_seconds"
expect_success "metrics.cpu reports core count"  "metrics.cpu"     "" "cores"
expect_success "metrics.memory reports capacity" "metrics.memory"  "" "total_bytes"
expect_success "metrics.load reports load_1"     "metrics.load"    "" "load_1"
expect_success "metrics.network lists interfaces" "metrics.network" "" "interfaces"
expect_success "metrics.disk lists filesystems"  "metrics.disk"    "" "filesystems"
expect_success "process.list returns processes"  "process.list"    '{"limit":5}' "processes"

# A real host must report a non-zero memory capacity; zero would mean the
# parser ran but read nothing.
memory="$(call metrics.memory)"
total="$(printf '%s' "$memory" | sed -n 's/.*"total_bytes": \([0-9]*\).*/\1/p')"
if [ -n "$total" ] && [ "$total" -gt 0 ]; then
  pass "memory capacity is non-zero ($total bytes)"
else
  fail "memory capacity is zero or missing"
fi

# The second CPU sample has an interval to compare against, so it must report
# a real sample window rather than the first-sample zero.
call metrics.cpu >/dev/null
sleep 2
cpu="$(call metrics.cpu)"
case "$cpu" in
  *'"sample_window": "0s"'*) fail "the second CPU sample must span a real interval" ;;
  *'"sample_window"'*) pass "CPU usage is computed from a real interval" ;;
  *) fail "CPU sample window is missing" ;;
esac

# ----------------------------------------------------------- payload safety

log ""
log "Payload validation"
expect_error "process.list rejects a negative limit"  "INVALID_PAYLOAD" "process.list" '{"limit":-1}'
expect_error "process.list rejects an unknown sort"   "INVALID_PAYLOAD" "process.list" '{"sort_by":"disk"}'
expect_error "process.list rejects unknown fields"    "INVALID_PAYLOAD" "process.list" '{"order":"asc"}'
expect_error "metrics.disk rejects traversal"         "INVALID_PAYLOAD" "metrics.disk" '{"mount_point":"/var/../../etc"}'
expect_error "metrics.disk rejects a relative path"   "INVALID_PAYLOAD" "metrics.disk" '{"mount_point":"relative/path"}'
expect_error "metrics.disk rejects an unmounted path" "NOT_FOUND"       "metrics.disk" '{"mount_point":"/definitely/not/mounted"}'

log ""
log "Command injection"
expect_error "service name with a shell separator is refused" "INVALID_PAYLOAD" "service.status" '{"name":"nginx; rm -rf /"}'
expect_error "service name that looks like an option is refused" "INVALID_PAYLOAD" "service.status" '{"name":"--version"}'
expect_error "service name with substitution is refused" "INVALID_PAYLOAD" "service.status" '{"name":"$(id)"}'
expect_error "service name with a path is refused" "INVALID_PAYLOAD" "service.status" '{"name":"../../etc/passwd"}'

log ""
log "Operation allowlist"
# The diagnostic client checks the allowlist before opening a connection, so a
# non-allowlisted operation never reaches the privileged process at all. The
# agent-side rejection is covered by the socket tests in
# agent/internal/socket/server_test.go, which speak raw protocol.
expect_local_refusal "an unregistered operation is refused"    "website.create"
expect_local_refusal "a shell-shaped operation is refused"     "agent.ping; id"
expect_local_refusal "a traversal-shaped operation is refused" "../../bin/sh"
expect_local_refusal "a case-mismatched operation is refused"  "METRICS.CPU"

# ------------------------------------------------------------ authentication

log ""
log "Authentication"
wrong="$(AGENT_TOKEN=wrong-token-that-is-long-enough-to-pass "$AGENT" -call metrics.memory 2>&1 || true)"
case "$wrong" in
  *'"code": "UNAUTHORIZED"'*) pass "a wrong token is refused" ;;
  *) fail "a wrong token was not refused: $(printf '%s' "$wrong" | head -4 | tr '\n' ' ')" ;;
esac
# The refusal must not disclose host data.
case "$wrong" in
  *total_bytes*) fail "a refused caller received metric data" ;;
  *) pass "a refused caller receives no data" ;;
esac

# ------------------------------------------------------------------- jobs

log ""
log "Asynchronous execution"
submitted="$("$AGENT" -call process.list -payload '{"limit":5}' -async 2>&1 || true)"
job_id="$(printf '%s' "$submitted" | sed -n 's/.*"job_id": "\(job_[0-9a-f]*\)".*/\1/p')"

if [ -n "$job_id" ]; then
  pass "an async submission returns a job id"
else
  fail "no job id was returned: $(printf '%s' "$submitted" | head -6 | tr '\n' ' ')"
fi

if [ -n "$job_id" ]; then
  finished=0
  attempt=0
  while [ $attempt -lt 50 ]; do
    status="$(call job.status "{\"job_id\":\"$job_id\"}")"
    case "$status" in
      *'"state": "SUCCESS"'*) finished=1; break ;;
      *'"state": "FAILED"'*|*'"state": "CANCELLED"'*) break ;;
    esac
    attempt=$((attempt + 1))
    sleep 1
  done

  if [ $finished -eq 1 ]; then
    pass "the job runs to completion and carries its result"
  else
    fail "the job did not succeed"
  fi

  expect_error "job.status rejects an unknown job" "NOT_FOUND" "job.status" '{"job_id":"job_000000000000000000000000"}'
  expect_error "job.status requires a job id"      "INVALID_PAYLOAD" "job.status" '{}'
fi

expect_success "job.list reports the job table" "job.list" "" "jobs"

# ------------------------------------------------------------------ audit

log ""
log "Audit trail"
audit_log="${AGENT_AUDIT_LOG:-/var/log/jothost/agent-audit.log}"
if [ -f "$audit_log" ]; then
  pass "the agent keeps its own audit log"

  if grep -q '"operation":"metrics.memory"' "$audit_log"; then
    pass "successful operations are audited"
  else
    fail "no audit record for metrics.memory"
  fi

  if grep -q '"status":"DENIED"' "$audit_log"; then
    pass "refused callers are audited"
  else
    fail "no audit record for a refused caller"
  fi

  if grep -q '"status":"FAILURE"' "$audit_log"; then
    pass "failed operations are audited"
  else
    fail "no audit record for a failed operation"
  fi

  # The shared secret must never reach the audit trail.
  if [ -n "${AGENT_TOKEN:-}" ] && grep -qF "$AGENT_TOKEN" "$audit_log"; then
    fail "the agent token leaked into the audit log"
  else
    pass "the audit log contains no token"
  fi

  # Peer identity comes from the kernel, so every record must carry one.
  if grep -q '"peer_uid"' "$audit_log"; then
    pass "audit records identify the calling process"
  else
    fail "audit records do not identify the caller"
  fi
else
  fail "the audit log is missing at $audit_log"
fi

# ------------------------------------------------------------------ result

log ""
if [ "$failures" -gt 0 ]; then
  log "FAILED: $failures check(s) did not pass"
  exit 1
fi
log "All Phase 2 Host Agent checks passed"
