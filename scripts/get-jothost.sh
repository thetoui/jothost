#!/bin/sh
# get-jothost.sh - download a JotHost Panel release, prove it is the one this
# project published, and only then run its installer.
#
#   curl -fsSL https://github.com/thetoui/jothost/releases/latest/download/get-jothost.sh \
#     | sudo sh -s -- install --domain panel.example.com --email you@example.com
#
#   sh get-jothost.sh --version 1.0.0 install --domain panel.example.com
#   sh get-jothost.sh --version 1.0.0 --verify-only
#
# Everything after this script's own options is handed to install.sh
# unchanged, so any install.sh command works (install, update, repair, ...).
#
# Two checks, and nothing runs until both pass:
#
#   1. The archive matches the SHA-256 published beside it. That catches a
#      truncated or corrupted download, with a message that says so.
#   2. The archive carries a Sigstore signature made by THIS repository's
#      release workflow, for THIS tag. The signing identity is GitHub's OIDC
#      token for the workflow run - there is no signing key anywhere to steal -
#      and verification fails for a file signed by anybody else, including a
#      different workflow in the same repository or a different tag.
#
# The checksum alone would prove nothing against somebody who can replace the
# files on the release page, because they would replace the checksum too.
# The signature is the check that holds.
#
# cosign does the verifying. If the host has none, a pinned version is
# downloaded and checked against its SHA-256 below before it is run.

set -eu

REPO="${JOTHOST_REPO:-thetoui/jothost}"
ISSUER="https://token.actions.githubusercontent.com"
WORKFLOW=".github/workflows/release.yml"

# Pinned cosign, and the SHA-256 Sigstore publishes for it
# (cosign_checksums.txt on the v3.1.3 release, checked against the binary).
COSIGN_VERSION="v3.1.3"
COSIGN_SHA256="4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71"

VERSION=""
VERIFY_ONLY=0

say()  { printf '%s\n' "$*"; }
die()  { printf 'get-jothost: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --version)
      [ $# -ge 2 ] || die "--version needs a value, for example --version 1.0.0"
      VERSION="${2#v}"; shift 2 ;;
    --version=*)
      VERSION="${1#--version=}"; VERSION="${VERSION#v}"; shift ;;
    --verify-only)
      VERIFY_ONLY=1; shift ;;
    -h|--help)
      sed -n '2,30p' "$0" 2>/dev/null || true; exit 0 ;;
    *)
      break ;;
  esac
done

case "$(uname -m)" in
  x86_64|amd64) ;;
  *) die "releases are built for amd64 only; this host is $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL --retry 3 -o "$2" "$1"; }
  fetch_stdout() { curl -fsSL --retry 3 "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -q -O "$2" "$1"; }
  fetch_stdout() { wget -q -O - "$1"; }
else
  die "curl or wget is required"
fi
for tool in tar sha256sum; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done

if [ -z "$VERSION" ]; then
  VERSION="$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n 1)"
  [ -n "$VERSION" ] || die "could not find the latest release of $REPO; pass --version"
fi
case "$VERSION" in
  *[!0-9A-Za-z.-]*|'') die "that is not a version: $VERSION" ;;
esac

WORK="$(mktemp -d)"
# Cleanup on exit; a signal exits (which runs it). A handler that only
# cleaned up would let dash carry on running after Ctrl-C.
trap 'rm -rf "$WORK"' EXIT
trap 'exit 130' INT TERM

NAME="jothost-$VERSION-linux-amd64"
# JOTHOST_DOWNLOAD_BASE points at a mirror. It changes where the files come
# from and nothing about what is accepted: the signature must still be this
# repository's release workflow for this tag, so a mirror cannot substitute
# anything.
BASE="${JOTHOST_DOWNLOAD_BASE:-https://github.com/$REPO/releases/download/v$VERSION}"

say "Downloading JotHost Panel $VERSION"
for file in "$NAME.tar.gz" "$NAME.tar.gz.sha256" "$NAME.tar.gz.sigstore.json"; do
  fetch "$BASE/$file" "$WORK/$file" || die "could not download $BASE/$file"
done

say "Checking the checksum"
( cd "$WORK" && sha256sum -c "$NAME.tar.gz.sha256" >/dev/null 2>&1 ) ||
  die "$NAME.tar.gz does not match its published SHA-256; the download is damaged. Nothing was run."

COSIGN="$(command -v cosign 2>/dev/null || true)"
if [ -z "$COSIGN" ]; then
  say "Fetching cosign $COSIGN_VERSION to check the signature"
  fetch "https://github.com/sigstore/cosign/releases/download/$COSIGN_VERSION/cosign-linux-amd64" \
    "$WORK/cosign" || die "could not download cosign"
  printf '%s  %s\n' "$COSIGN_SHA256" "$WORK/cosign" | sha256sum -c - >/dev/null 2>&1 ||
    die "the cosign binary does not match its pinned SHA-256. Nothing was run."
  chmod 0755 "$WORK/cosign"
  COSIGN="$WORK/cosign"
fi

say "Checking the signature"
"$COSIGN" verify-blob \
  --bundle "$WORK/$NAME.tar.gz.sigstore.json" \
  --certificate-identity "https://github.com/$REPO/$WORKFLOW@refs/tags/v$VERSION" \
  --certificate-oidc-issuer "$ISSUER" \
  "$WORK/$NAME.tar.gz" >/dev/null 2>&1 ||
  die "$NAME.tar.gz is not signed by $REPO's release workflow for v$VERSION. Nothing was run."

say "Verified: JotHost Panel $VERSION, signed by $REPO's release workflow."
if [ "$VERIFY_ONLY" -eq 1 ]; then
  exit 0
fi

tar -C "$WORK" -xzf "$WORK/$NAME.tar.gz"
# Run rather than exec, so the downloaded files are cleaned up afterwards.
# The installer copies what it keeps into place before it finishes.
"$WORK/$NAME/install.sh" "$@"
