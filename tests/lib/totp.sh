#!/bin/sh
# totp SECRET [UNIX_TIME] - prints the RFC 6238 code for a base32 secret.
#
# For shell suites that have to get past two-factor for real. POSIX sh, awk and
# openssl only, because it runs on Alpine's busybox as well as on Debian and
# Ubuntu, and none of them ship oathtool.
#
# Six digits, thirty-second steps, HMAC-SHA1: the parameters
# api/internal/secrets/totp.go issues.

totp() {
  secret="$1"
  now="${2:-$(date +%s)}"

  # base32 to hex, in awk: busybox has no base32 decoder to lean on.
  key_hex=$(printf '%s' "$secret" | tr 'a-z' 'A-Z' | tr -d '= ' | awk '
    BEGIN { alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"; bits = ""; hex = "" }
    {
      for (i = 1; i <= length($0); i++) {
        v = index(alphabet, substr($0, i, 1)) - 1
        if (v < 0) { exit 1 }
        for (b = 4; b >= 0; b--) { bits = bits (int(v / 2 ^ b) % 2) }
      }
    }
    END {
      for (i = 1; i + 7 <= length(bits); i += 8) {
        byte = 0
        for (j = 0; j < 8; j++) { byte = byte * 2 + substr(bits, i + j, 1) }
        hex = hex sprintf("%02x", byte)
      }
      print hex
    }')
  [ -n "$key_hex" ] || return 1

  counter=$((now / 30))
  # The counter as eight big-endian bytes, written as octal escapes, which
  # every printf understands.
  message=""
  shift_by=56
  while [ "$shift_by" -ge 0 ]; do
    message="$message\\$(printf '%03o' $(((counter >> shift_by) & 255)))"
    shift_by=$((shift_by - 8))
  done

  digest=$(printf "$message" | openssl dgst -sha1 -mac HMAC -macopt "hexkey:$key_hex" -binary |
    od -An -v -tu1 | tr -s ' \n' ' ')
  # shellcheck disable=SC2086
  set -- $digest
  [ "$#" -eq 20 ] || return 1

  # Dynamic truncation: the low nibble of the last byte picks where to read.
  eval "last=\${20}"
  offset=$((last & 15))
  i=0
  for byte in "$@"; do
    case "$i" in
      "$offset")           b0=$byte ;;
      "$((offset + 1))")   b1=$byte ;;
      "$((offset + 2))")   b2=$byte ;;
      "$((offset + 3))")   b3=$byte ;;
    esac
    i=$((i + 1))
  done
  printf '%06d\n' $(( (((b0 & 127) << 24) | (b1 << 16) | (b2 << 8) | b3) % 1000000 ))
}
