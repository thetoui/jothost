#!/bin/sh
# Stage 2 external verification (docs/STAGE2_RUNBOOK.md section 8).
#
# Checks the parts of a real deployment that are visible from the public
# internet: DNS resolution, forward/reverse DNS agreement, the panel's
# certificate, and a mail domain's SPF, DKIM and DMARC records. Run it from any
# machine that can reach the internet — it needs the host to exist and its DNS
# to be published, so it is a staging tool, not a CI check.
#
# It deliberately does not judge two things a script cannot: whether a test
# message actually landed in an inbox (open the inbox), and whether the host
# recovered from a reboot (watch it). Those stay manual in the runbook.
#
# Needs: dig (bind/knot dnsutils), openssl, curl. Standard tools; no panel code.
#
#   sh verify.sh --panel panel.example.com --site site.example.com \
#     --ip 203.0.113.10 --mail-host mail.example.com \
#     --mail-domain example.com --dkim-selector default
#
# --site, --ip and the mail flags are optional; a check whose inputs are absent
# is skipped rather than failed.

set -u

PANEL="" SITE="" IP="" MAIL_HOST="" MAIL_DOMAIN="" DKIM_SELECTOR="default"
RESOLVER="${RESOLVER:-1.1.1.1}"   # a public resolver, so we see what the world sees
CERT_MIN_DAYS="${CERT_MIN_DAYS:-1}"

while [ $# -gt 0 ]; do
  case "$1" in
    --panel)         PANEL="$2"; shift 2 ;;
    --site)          SITE="$2"; shift 2 ;;
    --ip)            IP="$2"; shift 2 ;;
    --mail-host)     MAIL_HOST="$2"; shift 2 ;;
    --mail-domain)   MAIL_DOMAIN="$2"; shift 2 ;;
    --dkim-selector) DKIM_SELECTOR="$2"; shift 2 ;;
    -h|--help)       sed -n '2,30p' "$0"; exit 0 ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

for tool in dig openssl curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FATAL: $tool is required"; exit 2; }
done
[ -n "$PANEL" ] || { echo "FATAL: --panel is required"; exit 2; }

passes=0 failures=0 skips=0
pass() { printf '  PASS  %s\n' "$1"; passes=$((passes + 1)); }
fail() { printf '  FAIL  %s\n' "$1"; failures=$((failures + 1)); }
skip() { printf '  ....  %s (skipped: %s)\n' "$1" "$2"; skips=$((skips + 1)); }

dig_short() { dig +short "$@" "@$RESOLVER" 2>/dev/null; }

# --------------------------------------------------------------- 1. DNS

echo "1. DNS, as a public resolver sees it ($RESOLVER)"
panel_ip=$(dig_short A "$PANEL" | grep -E '^[0-9.]+$' | head -n 1)
if [ -n "$panel_ip" ]; then
  pass "$PANEL resolves to $panel_ip"
  if [ -n "$IP" ] && [ "$panel_ip" != "$IP" ]; then
    fail "$PANEL resolves to $panel_ip, not the expected $IP"
  fi
else
  fail "$PANEL does not resolve to an A record"
fi

if [ -n "$SITE" ]; then
  site_ip=$(dig_short A "$SITE" | grep -E '^[0-9.]+$' | head -n 1)
  [ -n "$site_ip" ] && pass "$SITE resolves to $site_ip" || fail "$SITE does not resolve"
else
  skip "website DNS" "no --site"
fi

# ----------------------------------------------------- 2. reverse DNS

echo "2. Forward and reverse DNS agree"
if [ -n "$IP" ] && [ -n "$MAIL_HOST" ]; then
  ptr=$(dig_short -x "$IP" | sed 's/\.$//' | head -n 1)
  fwd=$(dig_short A "$MAIL_HOST" | grep -E '^[0-9.]+$' | head -n 1)
  if [ "$ptr" = "$MAIL_HOST" ]; then
    pass "$IP resolves back to $MAIL_HOST"
  else
    fail "$IP resolves back to '${ptr:-nothing}', not $MAIL_HOST (mail will be spam-filed)"
  fi
  if [ "$fwd" = "$IP" ]; then
    pass "$MAIL_HOST resolves forward to $IP"
  else
    fail "$MAIL_HOST resolves to '${fwd:-nothing}', not $IP"
  fi
else
  skip "reverse DNS" "need --ip and --mail-host"
fi

# ----------------------------------------------------- 3. certificate

echo "3. The panel's certificate"
cert=$(echo | openssl s_client -connect "$PANEL:443" -servername "$PANEL" 2>/dev/null)
if [ -z "$cert" ]; then
  fail "could not open a TLS connection to $PANEL:443"
else
  # Trusted by this machine's CA store (a self-signed cert fails verification).
  verify=$(echo | openssl s_client -connect "$PANEL:443" -servername "$PANEL" -verify_return_error 2>&1)
  if printf '%s' "$verify" | grep -q "Verify return code: 0"; then
    pass "the certificate is trusted (verifies against the public CA store)"
  else
    fail "the certificate is not trusted — still self-signed, or an untrusted issuer"
  fi
  issuer=$(printf '%s' "$cert" | openssl x509 -noout -issuer 2>/dev/null)
  case "$issuer" in
    *"Let's Encrypt"*|*"Let’s Encrypt"*) pass "issued by Let's Encrypt" ;;
    *) fail "unexpected issuer: ${issuer:-none}" ;;
  esac
  # Not expiring within the window. openssl does the comparison itself, so this
  # needs no date parsing — busybox's date cannot read openssl's date format,
  # and this script is meant to run anywhere.
  not_after=$(printf '%s' "$cert" | openssl x509 -noout -enddate 2>/dev/null | sed 's/notAfter=//')
  if printf '%s' "$cert" | openssl x509 -noout -checkend "$((CERT_MIN_DAYS * 86400))" >/dev/null 2>&1; then
    pass "not expiring within $CERT_MIN_DAYS day(s) (expires $not_after)"
  else
    fail "expires within $CERT_MIN_DAYS day(s), or the date is unreadable: ${not_after:-none}"
  fi
fi

# ----------------------------------------------------- 4. mail records

echo "4. Mail authentication records"
if [ -n "$MAIL_DOMAIN" ]; then
  mx=$(dig_short MX "$MAIL_DOMAIN")
  [ -n "$mx" ] && pass "MX: $(printf '%s' "$mx" | tr '\n' ' ')" || fail "no MX record for $MAIL_DOMAIN"

  spf=$(dig_short TXT "$MAIL_DOMAIN" | grep -i 'v=spf1')
  [ -n "$spf" ] && pass "SPF: $spf" || fail "no SPF (v=spf1) TXT record for $MAIL_DOMAIN"

  dmarc=$(dig_short TXT "_dmarc.$MAIL_DOMAIN" | grep -i 'v=DMARC1')
  [ -n "$dmarc" ] && pass "DMARC: $dmarc" || fail "no DMARC record at _dmarc.$MAIL_DOMAIN"

  dkim=$(dig_short TXT "${DKIM_SELECTOR}._domainkey.$MAIL_DOMAIN" | grep -i 'v=DKIM1\|p=')
  [ -n "$dkim" ] && pass "DKIM present at ${DKIM_SELECTOR}._domainkey" \
    || fail "no DKIM record at ${DKIM_SELECTOR}._domainkey.$MAIL_DOMAIN"
else
  skip "mail records" "no --mail-domain"
fi

# ------------------------------------------------------------- result

echo ""
echo "Checked externally: $passes passed, $failures failed, $skips skipped."
echo "Not checked here (see the runbook): inbox placement of a test message,"
echo "and recovery from a reboot — both are yours to observe."
[ "$failures" -eq 0 ]
