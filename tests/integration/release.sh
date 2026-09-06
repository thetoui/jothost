#!/bin/sh
# The release archive is what it says it is.
#
# A release that names one version and ships another is the kind of thing
# nobody notices until a bug report cites a build that was never published, and
# then nobody can tell which code the reporter was running. The version is
# compiled into the binaries at build time and written on the archive, and
# these checks are what stop those two drifting apart.
#
# Nothing here trusts the build. The archive is unpacked, the binary is run,
# and it is asked what it is.
#
# Run with:  make release-verify

set -eu

RELEASE_DIR="${RELEASE_DIR:-/release}"
SRC_DIR="${SRC_DIR:-/src}"

failures=0

log()  { printf '%s\n' "$*"; }
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }

# A control establishes that the request reached the thing under test. A check
# against an archive that is not there proves nothing.
# POSIX sh has no local variables, so every name assigned in here is a global.
# Called with a plain "name" it overwrote the archive name this whole script is
# built around, and every check below then looked for a file called "the
# archive is present". Not hypothetical: it happened here, having already
# happened once in the console checks.
control() {
  ctrl_label="$1"; ctrl_got="$2"; ctrl_want="$3"
  if [ "$ctrl_got" = "$ctrl_want" ]; then
    printf '  ctrl  %s\n' "$ctrl_label"
  else
    printf '  CTRL  %s (expected %s, got %s) - the checks below it prove nothing\n' \
      "$ctrl_label" "$ctrl_want" "$ctrl_got"
    failures=$((failures + 1))
  fi
}

log 'The release archive'
log '==================='

if [ ! -f "$SRC_DIR/VERSION" ]; then
  log 'No VERSION file; there is nothing to release.'
  exit 1
fi
version="$(head -1 "$SRC_DIR/VERSION" | tr -d '[:space:]')"
archive_name="jothost-$version-linux-amd64"
archive="$RELEASE_DIR/$archive_name.tar.gz"

log ''
log "1. It exists, and is named for $version"
control 'the archive is present' "$([ -f "$archive" ] && echo yes || echo no)" yes
if [ ! -f "$archive" ]; then
  log ''
  log "Build it first:  make release"
  exit 1
fi

log ''
log '2. The checksum matches'
# Not a formality. This is the only thing an operator can check before they run
# a script as root on their server.
if [ -f "$archive.sha256" ]; then
  if ( cd "$RELEASE_DIR" && sha256sum -c "$archive_name.tar.gz.sha256" >/dev/null 2>&1 ); then
    pass 'sha256sum -c accepts the archive'
  else
    fail 'the checksum does not match the archive'
  fi
else
  fail 'no .sha256 beside the archive'
fi

log ''
log '3. It unpacks into one directory named for the version'
work="$(mktemp -d)"
tar -C "$work" -xzf "$archive"
entries="$(ls -1 "$work" | wc -l | tr -d ' ')"
control 'the archive unpacked' "$entries" 1
if [ -d "$work/$archive_name" ]; then
  pass "it unpacks to $archive_name, so two versions cannot overwrite each other"
else
  fail "the archive does not contain a directory called $archive_name"
fi
root="$work/$archive_name"

log ''
log '4. Everything the installer needs is in it'
for required in install.sh VERSION bin/jothost-api bin/jothost-agent migrations frontend; do
  if [ -e "$root/$required" ]; then
    pass "$required"
  else
    fail "$required is missing, so the installer cannot run"
  fi
done

if [ -x "$root/install.sh" ]; then
  pass 'install.sh is executable'
else
  fail 'install.sh is not executable'
fi

log ''
log '5. The binaries say what the archive says'
# Asked, not assumed. The version is stamped in at build time by a -ldflags
# argument, and a build that quietly lost that flag produces binaries claiming
# to be a development build inside an archive named for a release.
for binary in jothost-api jothost-agent; do
  if [ ! -x "$root/bin/$binary" ]; then
    fail "$binary is not executable"
    continue
  fi
  reported="$("$root/bin/$binary" version 2>/dev/null | head -1 || true)"
  case "$reported" in
    *"$version"*)
      pass "$binary reports $version" ;;
    '')
      fail "$binary reported nothing when asked its version" ;;
    *)
      fail "$binary reports '$reported', not $version" ;;
  esac
done

log ''
log '6. It is a release build, not a development one'
for binary in jothost-api jothost-agent; do
  [ -x "$root/bin/$binary" ] || continue
  reported="$("$root/bin/$binary" version 2>/dev/null || true)"
  case "$reported" in
    *dev*|*unknown*)
      fail "$binary still carries a development stamp: $reported" ;;
    *)
      pass "$binary carries no development stamp" ;;
  esac
done

log ''
log '7. The binaries run on a host that is not the one that built them'
# Statically linked on purpose: a dynamically linked Go binary built on Alpine
# does not run on Debian, and the failure is a bare "not found" that names
# nothing. This container is not the build container.
for binary in jothost-api jothost-agent; do
  [ -x "$root/bin/$binary" ] || continue
  if "$root/bin/$binary" version >/dev/null 2>&1; then
    pass "$binary runs here"
  else
    fail "$binary will not run on this host"
  fi
done

log ''
log '8. Nothing secret was packaged'
# An archive is copied onto other people's servers. A stray .env or key from
# the build machine would be published with it.
found=''
for pattern in '*.env' '.env' 'id_rsa' '*.pem' '*.key' 'agent.env' 'api.env'; do
  hits="$(find "$root" -name "$pattern" 2>/dev/null || true)"
  [ -n "$hits" ] && found="$found $hits"
done
if [ -n "$found" ]; then
  fail "the archive carries files that look like secrets:$found"
else
  pass 'no environment files, keys or certificates in the archive'
fi

rm -rf "$work"

log ''
if [ "$failures" -eq 0 ]; then
  log 'All release checks passed.'
else
  log "$failures check(s) failed."
  exit 1
fi
