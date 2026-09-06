#!/bin/sh
# JotHost Panel — production installer.
#
#   ./install.sh install --domain panel.example.com --email you@example.com
#
# One command and a domain. When it finishes, that domain serves the panel over
# HTTPS, the administrator account exists, every service is enabled at boot, and
# the firewall is closed to everything but SSH and the web. Nothing else has to
# be configured for the panel to work.
#
# ---------------------------------------------------------------------------
# What this script is, and the rules it keeps
#
# It is run by a human, as root, on a machine they own. That is a different
# situation from CLAUDE.md section 6, which governs commands the *panel* builds
# from a request: nothing here comes from an untrusted caller. What does come
# from outside is the small set of options below, and each one is validated
# before it reaches a command line — a domain that is not a domain is refused,
# not quoted and hoped for.
#
# Beyond that, four rules shape the whole file:
#
#   * **Every step is idempotent.** The installer reconciles rather than
#     assumes: it may be run twice, or against a half-finished install, and the
#     second run does what the first one did not. That is also what makes
#     `repair` a real command rather than a hopeful one.
#
#   * **Secrets are generated once and never regenerated.** An update that
#     minted a new encryption key would leave every stored two-factor secret
#     and every encrypted credential undecryptable, and the failure would not
#     appear until somebody tried to sign in.
#
#   * **It refuses rather than guesses.** An unknown distribution, a missing
#     init system, a domain that is not one, artefacts that are not there: each
#     stops the run and says what it wanted. A panel installed on a host it
#     does not understand is a panel that half works.
#
#   * **It does not claim success it has not checked.** The last step asks the
#     panel, over the network, on the name it was given, whether it is
#     answering. Until that passes, the install has not succeeded.
#
# ---------------------------------------------------------------------------
# Commands
#
#   install     lay everything down and start it
#   update      replace the binaries and frontend, keeping configuration
#   repair      re-run the reconciling steps against an existing install
#   uninstall   remove the panel; --purge also removes its data
#   status      report what is installed and what is running
#
# Run with --help for the options.

set -eu

# --------------------------------------------------------------- constants

INSTALLER_VERSION="1.0"

# Where everything lives. These paths are the contract between the installer,
# the service units and the panel's own configuration; changing one means
# changing all three.
PREFIX=/opt/jothost
CONFIG_DIR=/etc/jothost
STATE_DIR=/var/lib/jothost
LOG_DIR=/var/log/jothost
RUN_DIR=/run/jothost
SITE_ROOT=/var/www

API_BIN="$PREFIX/bin/jothost-api"
AGENT_BIN="$PREFIX/bin/jothost-agent"
FRONTEND_DIR="$PREFIX/frontend"
MIGRATIONS_DIR="$PREFIX/migrations"

API_ENV="$CONFIG_DIR/api.env"
AGENT_ENV="$CONFIG_DIR/agent.env"
INSTALL_ENV="$CONFIG_DIR/install.env"

# The unprivileged account the API runs as, and the group that reaches the
# Agent's socket. The Agent itself runs as root: it is the only part of the
# panel that may, and the socket is how everything else asks it for privilege.
API_USER=jothost-api
PANEL_GROUP=jothost

PG_DB=jothost
PG_USER=jothost

# Where the panel's own vhost goes. The numeric prefix keeps it first, so a
# request for a name no site claims lands on the panel rather than on whichever
# customer site nginx happened to read first.
PANEL_VHOST_NAME=00-jothost-panel.conf

# The webroot the ACME challenge is served from. Shared with the panel's own
# certificate renewals, and the same shape Phase 6 uses for website
# certificates.
ACME_ROOT=/var/www/_acme

# Defaults, overridable by the options below.
DOMAIN=""
EMAIL=""
ADMIN_USER=admin
ARTEFACT_DIR=""
SELF_SIGNED=0
SKIP_FIREWALL=0
SKIP_TLS=0
MINIMAL=0
ASSUME_YES=0
PURGE=0
HEALTH_HOST=""

# ------------------------------------------------------------------ output

# Colour only when a terminal is attached: these lines are read from a log as
# often as from a screen, and escape codes in a log are noise.
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_BOLD=$(printf '\033[1m'); C_DIM=$(printf '\033[2m')
  C_OK=$(printf '\033[32m'); C_WARN=$(printf '\033[33m')
  C_ERR=$(printf '\033[31m'); C_OFF=$(printf '\033[0m')
else
  C_BOLD=''; C_DIM=''; C_OK=''; C_WARN=''; C_ERR=''; C_OFF=''
fi

STEP_NUMBER=0
WARNINGS=0

say()  { printf '%s\n' "$*"; }
step() {
  STEP_NUMBER=$((STEP_NUMBER + 1))
  printf '\n%s[%2d]%s %s%s%s\n' "$C_DIM" "$STEP_NUMBER" "$C_OFF" "$C_BOLD" "$*" "$C_OFF"
}
ok()   { printf '     %s✓%s %s\n' "$C_OK" "$C_OFF" "$*"; }
info() { printf '     %s·%s %s\n' "$C_DIM" "$C_OFF" "$*"; }
warn() {
  WARNINGS=$((WARNINGS + 1))
  printf '     %s!%s %s\n' "$C_WARN" "$C_OFF" "$*" >&2
}

# die stops the run and says what to do next.
#
# An installer that fails silently, or that fails with a shell error from three
# functions deep, leaves somebody with a half-built machine and no idea which
# half. Every refusal in this file names what was wanted.
die() {
  printf '\n%serror:%s %s\n' "$C_ERR" "$C_OFF" "$*" >&2
  printf '\n%s\n' "Nothing further has been changed. After fixing the above, run:" >&2
  printf '  %s repair\n\n' "$0" >&2
  exit 1
}

usage() {
  cat <<'USAGE'
JotHost Panel installer

Usage:
  install.sh install   --domain DOMAIN [options]
  install.sh update    [--from DIR]
  install.sh repair
  install.sh uninstall [--purge]
  install.sh status

Options:
  --domain DOMAIN     the name the panel is served on          (required to install)
  --email ADDRESS     for the certificate authority's expiry notices
  --admin-user NAME   the first administrator's username       (default: admin)
  --from DIR          where the built artefacts are            (default: beside this script)
  --self-signed       do not ask a certificate authority; issue a local certificate
  --no-tls            serve plain HTTP only (for a host behind another terminator)
  --no-firewall       leave the firewall alone
  --minimal           install only what the panel itself needs, not PHP
  --health-host HOST  resolve the domain to this address for the final check
  --yes               do not ask for confirmation
  --help              this text

The administrator's password is generated and printed once, at the end. It is
never written to disk, and there is no second chance to read it.
USAGE
}

# --------------------------------------------------------------- arguments

parse_args() {
  COMMAND="${1:-}"
  [ $# -gt 0 ] && shift

  case "$COMMAND" in
    install|update|repair|uninstall|status) ;;
    help|--help|-h|'') usage; exit 0 ;;
    *) usage >&2; die "unknown command \"$COMMAND\"" ;;
  esac

  while [ $# -gt 0 ]; do
    case "$1" in
      --domain)      DOMAIN="${2:-}"; shift 2 ;;
      --email)       EMAIL="${2:-}"; shift 2 ;;
      --admin-user)  ADMIN_USER="${2:-}"; shift 2 ;;
      --from)        ARTEFACT_DIR="${2:-}"; shift 2 ;;
      --health-host) HEALTH_HOST="${2:-}"; shift 2 ;;
      --self-signed) SELF_SIGNED=1; shift ;;
      --no-tls)      SKIP_TLS=1; shift ;;
      --no-firewall) SKIP_FIREWALL=1; shift ;;
      --minimal)     MINIMAL=1; shift ;;
      --purge)       PURGE=1; shift ;;
      --yes|-y)      ASSUME_YES=1; shift ;;
      --help|-h)     usage; exit 0 ;;
      *)             usage >&2; die "unknown option \"$1\"" ;;
    esac
  done
}

# --------------------------------------------------------------- validation

# valid_domain refuses anything that is not a hostname.
#
# The domain reaches an nginx directive, a certbot argument and a URL. Every
# one of those is a place where a value carrying a space, a newline or a
# semicolon stops being a value — so the check is an allowlist of the shape a
# hostname has, not a search for characters to reject.
valid_domain() {
  case "$1" in
    ''|*[!a-zA-Z0-9.-]*) return 1 ;;
    .*|-*|*.|*-)         return 1 ;;
    *.*)                 ;;
    *)                   return 1 ;;
  esac
  [ "${#1}" -le 253 ]
}

valid_email() {
  case "$1" in
    ''|*[!a-zA-Z0-9.@_+-]*) return 1 ;;
    *@*.*)                  return 0 ;;
    *)                      return 1 ;;
  esac
}

valid_username() {
  case "$1" in
    ''|*[!a-z0-9._-]*) return 1 ;;
    [a-z0-9]*)         [ "${#1}" -ge 3 ] && [ "${#1}" -le 32 ] ;;
    *)                 return 1 ;;
  esac
}

confirm() {
  [ "$ASSUME_YES" = 1 ] && return 0
  printf '\n%s [y/N] ' "$1"
  read -r reply </dev/tty 2>/dev/null || reply=n
  case "$reply" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# --------------------------------------------------------------- detection

# detect_host works out what this machine is before anything is changed.
#
# Everything below branches on these three answers, and each of them is a
# refusal rather than a guess when it cannot be found: an installer that
# assumed Debian on an unknown host would lay down a package list nothing
# understands and fail halfway through.
detect_host() {
  step "Looking at this machine"

  [ "$(id -u)" = "0" ] || die "this installer must run as root (try: sudo $0 $COMMAND ...)"
  ok "running as root"

  uname_s=$(uname -s 2>/dev/null || echo unknown)
  [ "$uname_s" = "Linux" ] || die "JotHost Panel runs on Linux; this is $uname_s"

  OS_ID=""; OS_VERSION=""; OS_NAME=""
  if [ -r /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    OS_ID="${ID:-}"
    OS_VERSION="${VERSION_ID:-}"
    OS_NAME="${PRETTY_NAME:-$OS_ID}"
  fi
  [ -n "$OS_ID" ] || die "/etc/os-release does not say what this distribution is"

  # The family decides the package manager and the paths. ID_LIKE is consulted
  # so a derivative — Ubuntu, Rocky, Linux Mint — is recognised as the thing it
  # derives from rather than refused for having its own name.
  case "$OS_ID ${ID_LIKE:-}" in
    *debian*|*ubuntu*) OS_FAMILY=debian ;;
    *alpine*)          OS_FAMILY=alpine ;;
    *rhel*|*fedora*|*centos*) OS_FAMILY=rhel ;;
    *) die "unsupported distribution: $OS_NAME. Supported: Debian/Ubuntu, Alpine, RHEL/Rocky/Alma" ;;
  esac
  ok "$OS_NAME ($OS_FAMILY)"

  ARCH=$(uname -m)
  case "$ARCH" in
    x86_64|amd64)  ARCH=amd64 ;;
    aarch64|arm64) ARCH=arm64 ;;
    *) die "unsupported architecture: $ARCH. Supported: x86_64, aarch64" ;;
  esac
  ok "$ARCH"

  detect_init
  case "$INIT_SYSTEM:$INIT_RUNNING" in
    systemd:1) ok "init system: systemd" ;;
    openrc:1)  ok "init system: OpenRC" ;;
    *)         info "no init system is supervising this machine yet" ;;
  esac
}

# detect_init works out what will supervise the panel's two services.
#
# Two questions rather than one: *which* init system this host uses, and
# whether it is currently running. They come apart in exactly one situation — a
# container or a chroot, where the init system is installed and something else
# is pid 1 — and that situation is worth handling rather than refusing, because
# it is how an image is built and how this installer is tested.
#
# It is called twice: once before anything is installed, for the report, and
# again afterwards, because on a minimal Alpine the supervisor is a package
# this installer has just laid down.
detect_init() {
  INIT_SYSTEM=none
  INIT_RUNNING=0

  if command -v systemctl >/dev/null 2>&1; then
    INIT_SYSTEM=systemd
    [ -d /run/systemd/system ] && INIT_RUNNING=1
  elif command -v rc-service >/dev/null 2>&1; then
    INIT_SYSTEM=openrc
    # OpenRC keeps its state under /run, which a container starts empty. Unlike
    # systemd, that state can simply be created — which is what a real boot
    # does — after which rc-service works normally. Doing it here means the
    # services below are supervised by the same thing that will supervise them
    # on the next boot, rather than by a fallback that behaves differently.
    if [ -f /run/openrc/softlevel ]; then
      INIT_RUNNING=1
    else
      mkdir -p /run/openrc
      touch /run/openrc/softlevel
      rc-status >/dev/null 2>&1 || true
      [ -f /run/openrc/softlevel ] && INIT_RUNNING=1
    fi
  fi
}

# --------------------------------------------------------------- artefacts

# find_artefacts locates the built binaries, frontend and migrations.
#
# Beside the script by default, which is how a release archive unpacks: the
# installer is shipped inside the thing it installs, so the ordinary case needs
# no path at all.
find_artefacts() {
  if [ -z "$ARTEFACT_DIR" ]; then
    script_dir=$(cd "$(dirname "$0")" 2>/dev/null && pwd) || script_dir=.
    ARTEFACT_DIR="$script_dir"
  fi
  [ -d "$ARTEFACT_DIR" ] || die "no artefact directory at $ARTEFACT_DIR"

  for required in bin/jothost-api bin/jothost-agent frontend migrations; do
    [ -e "$ARTEFACT_DIR/$required" ] ||
      die "$ARTEFACT_DIR is missing $required — build the artefacts with: make dist"
  done
  [ -f "$ARTEFACT_DIR/frontend/index.html" ] ||
    die "$ARTEFACT_DIR/frontend has no index.html; the frontend was not built"

  ARTEFACT_VERSION=$(head -n 1 "$ARTEFACT_DIR/VERSION" 2>/dev/null || echo unknown)
}

# --------------------------------------------------------------- packages

pkg_install() {
  [ $# -gt 0 ] || return 0
  case "$OS_FAMILY" in
    debian)
      DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@" >/dev/null ;;
    alpine)
      apk add --no-cache "$@" >/dev/null ;;
    rhel)
      if command -v dnf >/dev/null 2>&1; then dnf install -y "$@" >/dev/null
      else yum install -y "$@" >/dev/null; fi ;;
  esac
}

pkg_refresh() {
  case "$OS_FAMILY" in
    debian) DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null ;;
    alpine) apk update >/dev/null ;;
    rhel)   : ;;
  esac
}

# install_dependencies lays down what the *panel itself* needs, and no more.
#
# Not every package the panel can manage: PHP versions, BIND, Postfix, Dovecot,
# MariaDB and the rest are installed by the panel, on request, through the
# Agent — that machinery already exists and is how an operator adds a second
# PHP version six months from now. Installing all of it here would make a
# minutes-long install into an hour and put daemons on the machine nobody asked
# for.
#
# The exception is PHP, which is installed by default and skippable with
# --minimal: a hosting panel whose first website cannot run PHP is one whose
# first task is an install the operator did not expect to do.
install_dependencies() {
  step "Installing what the panel needs"

  pkg_refresh

  # The PHP list is what an ordinary application expects to find, not a minimal
  # interpreter. A panel whose first WordPress install fails on a missing gd or
  # a missing intl has not saved anybody the trouble of installing PHP; it has
  # moved the trouble to a place where the error message is somebody else's.
  case "$OS_FAMILY" in
    debian)
      base="nginx postgresql redis-server certbot openssl ca-certificates curl cron"
      php="php-fpm php-cli php-mysql php-pgsql php-mbstring php-xml php-curl php-zip
           php-gd php-intl php-bcmath php-soap php-opcache" ;;
    alpine)
      base="nginx postgresql postgresql-contrib redis certbot openssl ca-certificates curl openrc dcron"
      php="php83-fpm php83-cli php83-pdo php83-pdo_mysql php83-pdo_pgsql php83-mysqli
           php83-mbstring php83-xml php83-simplexml php83-dom php83-curl php83-zip
           php83-gd php83-session php83-opcache php83-openssl php83-fileinfo
           php83-iconv php83-phar php83-tokenizer php83-ctype php83-posix
           php83-exif php83-intl php83-bcmath php83-sodium" ;;
    rhel)
      base="nginx postgresql-server redis certbot openssl ca-certificates curl cronie"
      php="php-fpm php-cli php-mysqlnd php-pgsql php-mbstring php-xml php-gd
           php-intl php-bcmath php-soap php-opcache" ;;
  esac

  info "core: nginx, PostgreSQL, Redis, certbot"
  pkg_install $base || die "the core packages could not be installed"
  ok "core packages installed"

  # The supervisor may be one of the packages just installed: on a minimal
  # Alpine, OpenRC is. Everything below starts services, so what supervises
  # them has to be re-established here rather than at the end.
  detect_init
  case "$INIT_SYSTEM:$INIT_RUNNING" in
    none:*) warn "still no init system; the panel's services will not start at boot" ;;
    *:1)    info "services will be supervised by $INIT_SYSTEM" ;;
    *)      info "$INIT_SYSTEM is installed but is not supervising this machine" ;;
  esac

  if [ "$MINIMAL" = 1 ]; then
    info "PHP skipped (--minimal); the panel can install it later"
  else
    if pkg_install $php 2>/dev/null; then
      ok "PHP installed, so the first website can run an application"
    else
      # Not fatal. The panel installs PHP itself from its own page, and a
      # distribution that names its packages differently must not stop the
      # install of everything else.
      warn "PHP could not be installed from this distribution's packages; add it from the panel's PHP page"
    fi
  fi
}

# --------------------------------------------------------- accounts & paths

create_accounts() {
  step "Creating the panel's accounts and directories"

  if ! getent group "$PANEL_GROUP" >/dev/null 2>&1; then
    groupadd --system "$PANEL_GROUP" 2>/dev/null || addgroup -S "$PANEL_GROUP"
    ok "group $PANEL_GROUP created"
  else
    info "group $PANEL_GROUP already exists"
  fi

  # The API runs as this account and never as root. It is the process reachable
  # from the network, so what it can do on the host is what an unauthenticated
  # request could reach if it were ever wrong — and what it can do is ask the
  # Agent, over a socket, for the operations the Agent is willing to perform.
  if ! id "$API_USER" >/dev/null 2>&1; then
    if command -v useradd >/dev/null 2>&1; then
      useradd --system --gid "$PANEL_GROUP" --home-dir "$STATE_DIR" \
        --shell /sbin/nologin "$API_USER"
    else
      adduser -S -D -H -G "$PANEL_GROUP" -s /sbin/nologin "$API_USER"
    fi
    ok "user $API_USER created"
  else
    info "user $API_USER already exists"
  fi
  API_UID=$(id -u "$API_USER")

  # Two kinds of directory, and the difference is what is in them.
  #
  # /opt/jothost holds binaries, static files and migrations — nothing secret,
  # and nginx has to be able to walk into it to serve the frontend. A 0750
  # root:jothost prefix makes every page a 403, and the way that presents is
  # confusing: the API answers, the domain resolves, and the panel is blank.
  for dir in "$PREFIX" "$PREFIX/bin"; do
    mkdir -p "$dir"
    chown root:root "$dir"
    chmod 0755 "$dir"
  done

  # /etc/jothost holds the database password, the encryption key and the Agent
  # token; /var/lib holds the Agent's state; /run holds the socket. The API
  # reads through the group and nothing else on the machine reads at all.
  for dir in "$CONFIG_DIR" "$STATE_DIR" "$RUN_DIR"; do
    mkdir -p "$dir"
    chown root:"$PANEL_GROUP" "$dir"
    chmod 0750 "$dir"
  done

  # The log directory is the exception, and it has to be: the API runs as an
  # unprivileged account and *writes* here, while the Agent writes here as
  # root. At 0750 the API cannot create its own log file — and the way that
  # fails is the worst kind, because the supervisor reports the service
  # started and the process is gone before it can say why.
  #
  # 2770: group-writable, and setgid so a file the Agent creates as root is
  # still group-owned and still readable by the API.
  mkdir -p "$LOG_DIR"
  chown root:"$PANEL_GROUP" "$LOG_DIR"
  chmod 2770 "$LOG_DIR"
  mkdir -p "$SITE_ROOT" "$ACME_ROOT"
  chmod 0755 "$SITE_ROOT" "$ACME_ROOT"
  ok "directories under $PREFIX, $CONFIG_DIR, $STATE_DIR"
}

# --------------------------------------------------------------- artefacts

install_artefacts() {
  step "Installing the panel"

  install -m 0755 -o root -g root "$ARTEFACT_DIR/bin/jothost-api" "$API_BIN"
  install -m 0755 -o root -g root "$ARTEFACT_DIR/bin/jothost-agent" "$AGENT_BIN"
  ok "binaries in $PREFIX/bin"

  rm -rf "$FRONTEND_DIR.new"
  mkdir -p "$FRONTEND_DIR.new"
  cp -R "$ARTEFACT_DIR/frontend/." "$FRONTEND_DIR.new/"
  # Swapped rather than copied over: nginx serves this directory, and copying
  # into it in place means a visitor mid-update gets a page whose assets are
  # half old and half new.
  rm -rf "$FRONTEND_DIR.old"
  [ -d "$FRONTEND_DIR" ] && mv "$FRONTEND_DIR" "$FRONTEND_DIR.old"
  mv "$FRONTEND_DIR.new" "$FRONTEND_DIR"
  rm -rf "$FRONTEND_DIR.old"
  chmod -R a+rX "$FRONTEND_DIR"
  ok "frontend in $FRONTEND_DIR"

  rm -rf "$MIGRATIONS_DIR"
  cp -R "$ARTEFACT_DIR/migrations" "$MIGRATIONS_DIR"
  chmod -R a+rX "$MIGRATIONS_DIR"
  cp "$ARTEFACT_DIR/VERSION" "$PREFIX/VERSION" 2>/dev/null || true
  ok "migrations in $MIGRATIONS_DIR (version $ARTEFACT_VERSION)"
}

# --------------------------------------------------------------- datastores

pg_major() {
  psql --version 2>/dev/null | sed -n 's/.*PostgreSQL) \([0-9][0-9]*\).*/\1/p'
}

pg_data_dir() {
  case "$OS_FAMILY" in
    alpine) major=$(pg_major); [ -n "$major" ] || major=""
            [ -n "$major" ] && echo "/var/lib/postgresql/$major/data" || echo /var/lib/postgresql/data ;;
    rhel)   echo /var/lib/pgsql/data ;;
    debian) echo "" ;;   # Debian's packaging owns the cluster layout.
  esac
}

# pg_initialise creates the cluster, each distribution's own way.
#
# Not initdb into a path this script chose. Every distribution puts the cluster
# somewhere different — Alpine under the major version, RHEL under
# /var/lib/pgsql, Debian's packaging makes one on install — and each ships the
# supported way of creating it. Guessing the layout produces a cluster the
# distribution's own init script cannot find, which is a machine with two
# PostgreSQL installations and one of them invisible.
pg_initialise() {
  case "$OS_FAMILY" in
    alpine)
      data_dir=$(pg_data_dir)
      [ -d "$data_dir/base" ] && return 0
      info "initialising the cluster with Alpine's own setup"
      if [ "$INIT_RUNNING" = 1 ]; then
        rc-service postgresql setup >/dev/null 2>&1 || return 1
      else
        /etc/init.d/postgresql setup >/dev/null 2>&1 || return 1
      fi ;;
    rhel)
      [ -f /var/lib/pgsql/data/PG_VERSION ] && return 0
      info "initialising the cluster with postgresql-setup"
      postgresql-setup --initdb >/dev/null 2>&1 || return 1 ;;
    debian)
      # The package creates a cluster when it is installed. If one is somehow
      # missing, pg_createcluster is Debian's own way to make it.
      pg_lsclusters 2>/dev/null | tail -n +2 | grep -q . && return 0
      major=$(pg_major)
      [ -n "$major" ] || return 1
      info "creating the $major cluster with pg_createcluster"
      pg_createcluster "$major" main >/dev/null 2>&1 || return 1 ;;
  esac
  return 0
}

# setup_postgres makes sure a cluster exists, is running, and holds the panel's
# role and database.
#
# The role's password is generated hex, which is not merely convenient: it
# means the value can be written into SQL and a connection URL without any
# quoting question, and a quoting question in a password is how an installer
# creates a role nobody can authenticate as.
setup_postgres() {
  step "Preparing PostgreSQL"

  pg_initialise || die "the PostgreSQL cluster could not be created"

  service_start_now postgresql || service_start_now postgres ||
    die "PostgreSQL would not start; the panel has nowhere to keep its records"

  waited=0
  while [ "$waited" -lt 60 ]; do
    su postgres -c "psql -tAc 'SELECT 1'" >/dev/null 2>&1 && break
    sleep 1; waited=$((waited + 1))
  done
  [ "$waited" -lt 60 ] || die "PostgreSQL did not start answering within 60 seconds"
  ok "PostgreSQL is answering"

  # The password is only generated when the role is being created. A second run
  # must not change it, or the password in api.env would stop matching the one
  # in the database — an install that broke itself by being run twice.
  if su postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='$PG_USER'\"" 2>/dev/null | grep -q 1; then
    info "role $PG_USER already exists; its password is left alone"
    PG_PASSWORD=""
  else
    PG_PASSWORD=$(generate_secret 24)
    # On stdin, not in argv: a password on a command line is visible in the
    # process list to every account on this machine for as long as it runs.
    su postgres -c "psql -v ON_ERROR_STOP=1 -q" >/dev/null <<SQL || die "the panel's database role could not be created"
CREATE ROLE $PG_USER LOGIN PASSWORD '$PG_PASSWORD';
SQL
    ok "role $PG_USER created"
  fi

  if su postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='$PG_DB'\"" 2>/dev/null | grep -q 1; then
    info "database $PG_DB already exists"
  else
    su postgres -c "psql -v ON_ERROR_STOP=1 -q" >/dev/null <<SQL || die "the panel's database could not be created"
CREATE DATABASE $PG_DB OWNER $PG_USER;
SQL
    ok "database $PG_DB created"
  fi
}

setup_redis() {
  step "Preparing Redis"

  # Loopback only. Redis has no authentication configured here and needs none:
  # nothing off this machine can reach it, which is a stronger statement than a
  # password on an interface open to the internet.
  for conf in /etc/redis/redis.conf /etc/redis.conf /etc/redis/redis.conf.default; do
    [ -f "$conf" ] || continue
    if ! grep -qE '^[[:space:]]*bind[[:space:]]+127\.0\.0\.1' "$conf"; then
      cp "$conf" "$conf.jothost-backup" 2>/dev/null || true
      sed -i 's/^[[:space:]]*bind .*/bind 127.0.0.1 -::1/' "$conf" 2>/dev/null || true
      info "$conf bound to the loopback"
    fi
    break
  done

  service_start_now redis || service_start_now redis-server ||
    die "Redis would not start; sessions and rate limiting have nowhere to live"

  waited=0
  while [ "$waited" -lt 30 ]; do
    redis-cli ping 2>/dev/null | grep -q PONG && break
    sleep 1; waited=$((waited + 1))
  done
  [ "$waited" -lt 30 ] || die "Redis did not start answering within 30 seconds"
  ok "Redis is answering"
}

# ----------------------------------------------------------------- secrets

# generate_secret produces hex from the kernel's entropy.
#
# Hex rather than base64 so the value can be written into a URL, a shell
# variable and an SQL literal without escaping anything. openssl is preferred
# and /dev/urandom is the fallback, because a host without openssl must still
# get a real secret rather than a weak one.
generate_secret() {
  bytes="${1:-32}"
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex "$bytes"
  else
    od -An -tx1 -N "$bytes" /dev/urandom | tr -d ' \n'
  fi
}

# env_value reads one variable out of an env file that already exists.
env_value() {
  file="$1"; key="$2"
  [ -f "$file" ] || return 1
  # The quotes the file is written with are stripped here, so a caller gets
  # the value rather than the value plus its punctuation.
  sed -n "s/^$key=//p" "$file" | head -n 1 | sed 's/^"//; s/"$//'
}

# write_configuration produces the two env files the services read.
#
# The rule that governs this whole function: **a secret is generated once.**
# Every value below is taken from the existing file when there is one, and
# minted only when there is not. An update that produced a new ENCRYPTION_KEY
# would leave every stored two-factor secret and every encrypted credential
# undecryptable — and nothing would notice until somebody tried to sign in.
write_configuration() {
  step "Writing the configuration"

  ENCRYPTION_KEY=$(env_value "$API_ENV" ENCRYPTION_KEY || true)
  [ -n "$ENCRYPTION_KEY" ] || ENCRYPTION_KEY=$(generate_secret 32)

  AGENT_TOKEN=$(env_value "$API_ENV" AGENT_TOKEN || true)
  [ -n "$AGENT_TOKEN" ] || AGENT_TOKEN=$(generate_secret 32)

  if [ -z "${PG_PASSWORD:-}" ]; then
    existing_url=$(env_value "$API_ENV" DATABASE_URL || true)
    PG_PASSWORD=$(printf '%s' "$existing_url" | sed -n 's|^postgres://[^:]*:\([^@]*\)@.*|\1|p')
    [ -n "$PG_PASSWORD" ] ||
      die "the database role exists but its password is not in $API_ENV; run: $0 uninstall --purge, then install again"
  fi

  saved_domain=$(env_value "$INSTALL_ENV" PANEL_DOMAIN || true)
  [ -n "$DOMAIN" ] || DOMAIN="$saved_domain"

  panel_url="https://$DOMAIN"
  [ "$SKIP_TLS" = 1 ] && panel_url="http://$DOMAIN"

  # Every value is quoted, and that is not tidiness.
  #
  # This file is read two ways: systemd's EnvironmentFile, which is happy
  # either way, and `.` in a shell — the OpenRC init script and every command
  # the installer runs as the API's account. An unquoted value containing a
  # space is two words to a shell, and "TOTP_ISSUER=JotHost Panel" becomes an
  # attempt to run a command called Panel. Quoting is what makes one file
  # readable by both, and systemd strips the quotes it finds.
  umask 077
  cat > "$API_ENV" <<EOF
# JotHost Panel — API configuration.
#
# Written by the installer. Secrets here are generated once and preserved
# across updates; replacing ENCRYPTION_KEY makes every stored two-factor secret
# and encrypted credential unreadable.
JOTHOST_ENV="production"
LOG_LEVEL="info"
API_HTTP_ADDR="127.0.0.1:8080"
PANEL_URL="$panel_url"

DATABASE_URL="postgres://$PG_USER:$PG_PASSWORD@127.0.0.1:5432/$PG_DB?sslmode=disable"
REDIS_URL="redis://127.0.0.1:6379/0"
MIGRATIONS_DIR="$MIGRATIONS_DIR"
AUTO_MIGRATE="true"

AGENT_SOCKET="$RUN_DIR/agent.sock"
AGENT_TIMEOUT="30s"
AGENT_TOKEN="$AGENT_TOKEN"

ENCRYPTION_KEY="$ENCRYPTION_KEY"
TOTP_ISSUER="JotHost Panel"
EOF
  chown root:"$PANEL_GROUP" "$API_ENV"
  chmod 0640 "$API_ENV"
  ok "$API_ENV (0640 root:$PANEL_GROUP)"

  cat > "$AGENT_ENV" <<EOF
# JotHost Panel — Agent configuration.
#
# The Agent runs as root. AGENT_ALLOWED_UIDS is the second lock on its socket:
# the socket is 0660 and group-owned, and this says which account may speak
# through it even so.
#
# The paths below are the ones that differ between distributions. The Agent
# defaults suit Debian; on Alpine, nginx reads its virtual hosts from a
# different directory and cron keeps its files somewhere else. Writing them
# here is what makes the Agent's defaults right for *this* machine rather than
# for the machine they were written against.
LOG_LEVEL="info"
AGENT_SOCKET="$RUN_DIR/agent.sock"
AGENT_SOCKET_GROUP="$PANEL_GROUP"
AGENT_TOKEN="$AGENT_TOKEN"
AGENT_ALLOWED_UIDS="$API_UID"
AGENT_SITE_ROOT="$SITE_ROOT"
AGENT_AUDIT_LOG="$LOG_DIR/agent-audit.log"
AGENT_NGINX_SITES_DIR="$(nginx_sites_dir)"
AGENT_CRON_SPOOL_DIR="$(cron_spool_dir)"
AGENT_WEB_GROUP="$(web_group)"
EOF
  chown root:root "$AGENT_ENV"
  chmod 0600 "$AGENT_ENV"
  ok "$AGENT_ENV (0600 root:root)"

  cat > "$INSTALL_ENV" <<EOF
# What the installer laid down. Read by update, repair and uninstall.
PANEL_DOMAIN=$DOMAIN
PANEL_TLS=$([ "$SKIP_TLS" = 1 ] && echo none || echo enabled)
PANEL_VERSION=$ARTEFACT_VERSION
INSTALLER_VERSION=$INSTALLER_VERSION
INIT_SYSTEM=$INIT_SYSTEM
INIT_RUNNING=$INIT_RUNNING
OS_FAMILY=$OS_FAMILY
INSTALLED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)
EOF
  chown root:"$PANEL_GROUP" "$INSTALL_ENV"
  chmod 0640 "$INSTALL_ENV"
  umask 022
}

# --------------------------------------------------------- host conventions

# nginx_sites_dir is where a virtual host goes on this machine.
#
# It is not a matter of taste. Alpine's nginx.conf includes conf.d at the *top*
# level and http.d inside `http {}`; Debian's includes conf.d inside `http {}`
# and has sites-enabled besides. A vhost in the wrong one is not a vhost that
# is ignored — it is nginx refusing to start, because `server` and `map` are
# not allowed at the main level, and that takes down every site on the host.
#
# The Agent is told this too, so the panel writes its customers' vhosts into
# the same directory the installer writes the panel's own.
nginx_sites_dir() {
  if [ -d /etc/nginx/http.d ]; then
    echo /etc/nginx/http.d
  else
    echo /etc/nginx/conf.d
  fi
}

cron_spool_dir() {
  if [ -d /etc/crontabs ]; then
    echo /etc/crontabs
  else
    echo /var/spool/cron/crontabs
  fi
}

# web_group is the group a site's files are group-owned by, so the web server
# can read what it serves.
#
# Getting this wrong produces a site that is provisioned, valid, and answers
# 403 to every visitor — a failure that is invisible until the first request.
web_group() {
  for candidate in nginx www-data http apache; do
    if getent group "$candidate" >/dev/null 2>&1; then
      echo "$candidate"
      return 0
    fi
  done
  echo nginx
}

# ------------------------------------------------------------------ nginx

# configure_nginx writes the panel's own vhost.
#
# The panel is not a website in the panel, and that is deliberate: a site the
# Agent rewrites is a site whose vhost could be replaced while the panel is
# using it to serve the page somebody is clicking in. This file is the
# installer's, marked as such, and nothing else writes it.
configure_nginx() {
  step "Configuring nginx for $DOMAIN"

  sites_dir=$(nginx_sites_dir)
  mkdir -p "$sites_dir" "$ACME_ROOT/.well-known/acme-challenge"

  # Debian's default site answers for every name on port 80, and it is first in
  # the include order. Left in place it catches the panel's own domain.
  for default_site in /etc/nginx/sites-enabled/default /etc/nginx/conf.d/default.conf; do
    if [ -f "$default_site" ]; then
      mv "$default_site" "$default_site.jothost-disabled"
      info "moved aside $default_site"
    fi
  done

  write_panel_vhost "$sites_dir/$PANEL_VHOST_NAME" plain
  nginx -t >/dev/null 2>&1 || {
    nginx -t 2>&1 | sed 's/^/     /' >&2
    die "nginx rejected the configuration"
  }
  service_start_now nginx || die "nginx would not start"
  service_reload nginx
  ok "the panel's vhost is installed and nginx accepted it"
}

# write_panel_vhost renders the vhost, with or without TLS.
#
# Two passes: plain HTTP first so the ACME challenge can be answered, then
# again with the certificate once there is one. A vhost naming a certificate
# that does not exist stops nginx from starting at all — which on a host that
# is already serving customer sites takes all of them down, not just the panel.
write_panel_vhost() {
  target="$1"; mode="$2"

  {
    cat <<EOF
# Managed by the JotHost Panel installer. Manual edits are overwritten.
#
# The panel: a static single-page application, and the API behind /api.
# Everything else on this host is a website the panel manages, in its own file.

server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;
    server_tokens off;

    # The ACME challenge is always served over plain HTTP, including after the
    # redirect below exists: a renewal has to be answerable without a valid
    # certificate, which is the state a renewal is fixing.
    location ^~ /.well-known/acme-challenge/ {
        root $ACME_ROOT;
        default_type "text/plain";
    }
EOF

    if [ "$mode" = "tls" ]; then
      cat <<EOF

    location / {
        return 301 https://\$host\$request_uri;
    }
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name $DOMAIN;
    server_tokens off;

    ssl_certificate     $TLS_CERT;
    ssl_certificate_key $TLS_KEY;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:JotHostSSL:10m;
    ssl_session_timeout 1d;
EOF
      # HSTS only with a certificate a browser will trust. Sent from a
      # self-signed host it would pin visitors to an HTTPS they cannot accept.
      if [ "${TLS_TRUSTED:-0}" = 1 ]; then
        cat <<'EOF'

    # Six months. Sent only because the certificate is one a browser trusts.
    add_header Strict-Transport-Security "max-age=15552000" always;
EOF
      fi
    fi

    cat <<EOF

    root $FRONTEND_DIR;
    index index.html;

    client_max_body_size 512m;

    add_header X-Content-Type-Options "nosniff" always;
    add_header X-Frame-Options "DENY" always;
    add_header Referrer-Policy "no-referrer" always;

    # The API. 127.0.0.1 because that is all it listens on: the panel's own
    # process is not reachable from the network except through this server.
    location /api/ {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 300s;
        proxy_buffering off;
    }

    # Grafana, when it is installed. Proxied under the panel's own name rather
    # than published on one of its own, which is the difference between an
    # embedded chart and a cross-origin frame: same-origin means the browser
    # sends Grafana's session cookie as a first-party cookie, so no SameSite
    # relaxation is needed anywhere.
    #
    # Grafana still authenticates its own visitors. This proxy carries the
    # request; it does not vouch for whoever sent it.
    location /grafana/ {
        proxy_pass http://127.0.0.1:3000/;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;

        # Grafana's live tail and its alerting stream are websockets.
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";

        # The server block sends X-Frame-Options: DENY, which is right for the
        # panel and would make every embedded chart a blank box. Overridden
        # here to SAMEORIGIN: the frame is the panel's own page, and anybody
        # else framing Grafana is still refused.
        proxy_hide_header X-Frame-Options;
        add_header X-Frame-Options "SAMEORIGIN" always;
    }

    # phpMyAdmin, when it has been installed. The upstream is the panel's own
    # nginx on loopback, a separate process from this one: a website whose
    # configuration nginx refuses cannot take the database console down with
    # it, and nothing the website system enumerates can see the panel's own
    # configuration.
    #
    # Served here rather than only on its own hostname: phpMyAdmin's login is a POST carrying a CSRF token
    # bound to the session cookie set on the page the form came from, and
    # reading that page is something only same-origin JavaScript may do. Off
    # this origin, "open this database" could never be more than a login form
    # with the username filled in.
    #
    # phpMyAdmin still authenticates its own visitors with a real database
    # account. This proxy carries the request; it does not vouch for whoever
    # sent it.
    location /phpmyadmin/ {
        proxy_pass http://127.0.0.1:8791/;
        proxy_http_version 1.1;
        proxy_set_header Host phpmyadmin.internal;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_read_timeout 120s;

        # phpMyAdmin sees this request with the /phpmyadmin/ prefix stripped,
        # so the redirects it issues are root-relative and carry no prefix: a
        # sign-in answers 302 Location: /index.php?route=/&db=... and the
        # browser follows that to the panel's own single-page application. The
        # operator ends up back in the panel, never signed in, having done
        # nothing wrong.
        #
        # PmaAbsoluteUri does not cover this. It is set, and the Location
        # header still comes back without the prefix, so nginx has to put it
        # back.
        #
        # It was invisible to every check written before a real browser drove
        # this: they asked for the database page by its full URL instead of
        # following where phpMyAdmin sent them.
        # $http_host, not a bare path: a path-only replacement makes nginx
        # rebuild the URL from its own listening port, which is not the port
        # the browser asked on wherever the two differ - and the operator is
        # redirected to a port nothing answers. This echoes back exactly the
        # host and port the request arrived with.
        proxy_redirect / \$scheme://\$http_host/phpmyadmin/;
    }

    location = /healthz { proxy_pass http://127.0.0.1:8080; proxy_set_header Host \$host; }
    location = /readyz  { proxy_pass http://127.0.0.1:8080; proxy_set_header Host \$host; }

    # Hashed asset filenames, so they may be cached for a long time. index.html
    # must not be: it is what names the current assets, and a cached one points
    # a returning visitor at files an update has already replaced.
    location /assets/ {
        expires 1y;
        add_header Cache-Control "public, immutable";
    }

    location / {
        try_files \$uri \$uri/ /index.html;
        add_header Cache-Control "no-cache";
    }

    location ~ /\\. {
        deny all;
        access_log off;
    }
}
EOF
  } > "$target"
  chmod 0644 "$target"
}

# --------------------------------------------------------------------- TLS

# setup_tls obtains the certificate the panel is served with.
#
# certbot, because that is the ACME client the panel already uses for website
# certificates (Phase 6): one client, one account, one place certificates live,
# and renewal that an operator only has to understand once.
#
# The fallback is the point of the function. A machine whose DNS does not yet
# point here, or whose port 80 is unreachable, cannot pass an HTTP-01
# challenge — and that is the ordinary case an hour after a server is created.
# Refusing to finish would leave a panel nobody can reach; issuing a self-signed
# certificate and *saying so plainly* leaves a working panel and one clear next
# step.
setup_tls() {
  if [ "$SKIP_TLS" = 1 ]; then
    step "TLS"
    warn "serving plain HTTP (--no-tls). Sign-in credentials cross the network in the clear unless something in front terminates TLS."
    return 0
  fi

  step "Obtaining a certificate for $DOMAIN"

  live_dir="/etc/letsencrypt/live/$DOMAIN"
  if [ -f "$live_dir/fullchain.pem" ] && [ -f "$live_dir/privkey.pem" ]; then
    TLS_CERT="$live_dir/fullchain.pem"
    TLS_KEY="$live_dir/privkey.pem"
    TLS_TRUSTED=1
    ok "an existing certificate for $DOMAIN is already installed"
    install_renewal_hook
    return 0
  fi

  if [ "$SELF_SIGNED" = 1 ]; then
    info "--self-signed given; not asking a certificate authority"
    issue_self_signed
    return 0
  fi

  if ! command -v certbot >/dev/null 2>&1; then
    warn "certbot is not installed, so no certificate could be requested"
    issue_self_signed
    return 0
  fi

  email_args="--register-unsafely-without-email"
  if [ -n "$EMAIL" ]; then
    email_args="--email $EMAIL"
  else
    info "no --email given; the certificate authority will not be able to warn about expiry"
  fi

  # shellcheck disable=SC2086
  if certbot certonly --webroot -w "$ACME_ROOT" -d "$DOMAIN" \
       --non-interactive --agree-tos $email_args --keep-until-expiring >/dev/null 2>&1; then
    TLS_CERT="$live_dir/fullchain.pem"
    TLS_KEY="$live_dir/privkey.pem"
    TLS_TRUSTED=1
    ok "certificate issued for $DOMAIN"
    install_renewal_hook
  else
    warn "the certificate authority could not verify $DOMAIN — check that its DNS points here and that port 80 is reachable"
    issue_self_signed
  fi
}

# issue_self_signed produces a certificate that encrypts and proves nothing.
#
# It is named as such everywhere it is reported, because a self-signed
# certificate makes a browser warn on every visit and a person who does not know
# why will learn to click through warnings — which is a worse habit than the
# missing certificate.
issue_self_signed() {
  cert_dir="$STATE_DIR/tls"
  mkdir -p "$cert_dir"
  chmod 0700 "$cert_dir"
  TLS_CERT="$cert_dir/$DOMAIN.crt"
  TLS_KEY="$cert_dir/$DOMAIN.key"
  TLS_TRUSTED=0

  if [ ! -f "$TLS_CERT" ]; then
    openssl req -x509 -newkey rsa:2048 -nodes -days 825 \
      -keyout "$TLS_KEY" -out "$TLS_CERT" \
      -subj "/CN=$DOMAIN" -addext "subjectAltName=DNS:$DOMAIN" >/dev/null 2>&1 ||
      die "a self-signed certificate could not be generated"
    chmod 0600 "$TLS_KEY"
  fi
  warn "using a SELF-SIGNED certificate: the panel is encrypted but browsers will warn. Once DNS points here, run: $0 repair"
}

# install_renewal_hook makes a renewed certificate reach nginx.
#
# certbot renews on its own timer and writes the new file. Without this, nginx
# goes on serving the old certificate out of memory until something restarts it
# — which is usually the day it expires.
install_renewal_hook() {
  hook_dir=/etc/letsencrypt/renewal-hooks/deploy
  mkdir -p "$hook_dir"
  cat > "$hook_dir/jothost-reload-nginx.sh" <<'HOOK'
#!/bin/sh
# Installed by the JotHost Panel installer: a renewed certificate is only in
# use once nginx has re-read it.
nginx -t >/dev/null 2>&1 && nginx -s reload >/dev/null 2>&1
HOOK
  chmod 0755 "$hook_dir/jothost-reload-nginx.sh"
  info "nginx will reload when the certificate renews"
}

# --------------------------------------------------------------- services

# The service definitions.
#
# The API is hardened as far as an application that talks to a socket and a
# database can be: no new privileges, a private /tmp, a read-only filesystem
# with two named exceptions. The Agent is not, and cannot be — it exists to
# change the machine, and confining it would be confining the thing the panel
# uses to do its work.

write_systemd_units() {
  cat > /etc/systemd/system/jothost-agent.service <<EOF
[Unit]
Description=JotHost Panel host agent
Documentation=https://github.com/thetoui/jothost
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$AGENT_BIN
EnvironmentFile=$AGENT_ENV
Restart=always
RestartSec=3
# The Agent is root by design: it is the only privileged part of the panel, and
# everything else asks it through a socket. Confining it would confine the
# thing that does the work.
User=root
RuntimeDirectory=jothost
RuntimeDirectoryMode=0750
StandardOutput=journal
StandardError=journal
SyslogIdentifier=jothost-agent

[Install]
WantedBy=multi-user.target
EOF

  cat > /etc/systemd/system/jothost-api.service <<EOF
[Unit]
Description=JotHost Panel API
Documentation=https://github.com/thetoui/jothost
After=network-online.target postgresql.service redis-server.service redis.service jothost-agent.service
Wants=network-online.target
Requires=jothost-agent.service

[Service]
Type=simple
ExecStart=$API_BIN serve
EnvironmentFile=$API_ENV
User=$API_USER
Group=$PANEL_GROUP
Restart=always
RestartSec=3
StartLimitIntervalSec=60
StartLimitBurst=5

# This is the process reachable from the network. What it may do on the host is
# what a request could reach if the panel were ever wrong.
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectControlGroups=true
RestrictSUIDSGID=true
RestrictRealtime=true
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
LockPersonality=true
ReadWritePaths=$STATE_DIR $LOG_DIR

StandardOutput=journal
StandardError=journal
SyslogIdentifier=jothost-api

[Install]
WantedBy=multi-user.target
EOF
}

write_openrc_units() {
  cat > /etc/init.d/jothost-agent <<EOF
#!/sbin/openrc-run
# Installed by the JotHost Panel installer.
name="jothost-agent"
description="JotHost Panel host agent"
command="$AGENT_BIN"
command_background=true
pidfile="/run/jothost-agent.pid"
output_log="$LOG_DIR/agent.log"
error_log="$LOG_DIR/agent.log"

depend() {
    need net
    before jothost-api
}

start_pre() {
    checkpath --directory --owner root:$PANEL_GROUP --mode 0750 $RUN_DIR
    set -a
    . $AGENT_ENV
    set +a
}
EOF
  chmod 0755 /etc/init.d/jothost-agent

  cat > /etc/init.d/jothost-api <<EOF
#!/sbin/openrc-run
# Installed by the JotHost Panel installer.
name="jothost-api"
description="JotHost Panel API"
command="$API_BIN"
command_args="serve"
command_user="$API_USER:$PANEL_GROUP"
command_background=true
pidfile="/run/jothost-api.pid"
output_log="$LOG_DIR/api.log"
error_log="$LOG_DIR/api.log"

depend() {
    need net jothost-agent
}

start_pre() {
    set -a
    . $API_ENV
    set +a
}
EOF
  chmod 0755 /etc/init.d/jothost-api
}

# service_start_now starts a system service through whatever supervises it.
#
# Used for the dependencies — PostgreSQL, Redis, nginx — whose unit names differ
# between distributions. A name that does not exist is not an error here: the
# caller tries the next one.
service_start_now() {
  name="$1"

  if [ "$INIT_RUNNING" = 1 ]; then
    case "$INIT_SYSTEM" in
      systemd)
        systemctl list-unit-files "$name.service" >/dev/null 2>&1 || return 1
        systemctl enable "$name" >/dev/null 2>&1 || true
        systemctl start "$name" >/dev/null 2>&1 || return 1
        return 0 ;;
      openrc)
        [ -f "/etc/init.d/$name" ] || return 1
        rc-update add "$name" default >/dev/null 2>&1 || true
        rc-service "$name" start >/dev/null 2>&1 && return 0
        rc-service "$name" status >/dev/null 2>&1 && return 0
        return 1 ;;
    esac
  fi

  # Nothing is supervising this machine. The daemon is started directly so this
  # run still produces a working panel; it is enabled above as well, so a real
  # boot picks it up. install_services says both halves out loud.
  if [ -f "/etc/init.d/$name" ]; then
    "/etc/init.d/$name" start >/dev/null 2>&1 && return 0
  fi
  case "$name" in
    postgresql|postgres)
      command -v pg_ctl >/dev/null 2>&1 || return 1
      data_dir=$(pg_data_dir)
      [ -n "$data_dir" ] || data_dir=/var/lib/postgresql/data
      mkdir -p /run/postgresql && chown postgres:postgres /run/postgresql
      su postgres -c "pg_ctl -D $data_dir -w -t 30 -l $LOG_DIR/postgres.log start" >/dev/null 2>&1
      return $? ;;
    redis|redis-server)
      command -v redis-server >/dev/null 2>&1 || return 1
      redis-server --daemonize yes --bind 127.0.0.1 >/dev/null 2>&1
      return $? ;;
    nginx)
      command -v nginx >/dev/null 2>&1 || return 1
      mkdir -p /run/nginx
      nginx >/dev/null 2>&1 || pgrep nginx >/dev/null 2>&1
      return $? ;;
  esac
  return 1
}

service_reload() {
  name="$1"
  if [ "$INIT_RUNNING" = 1 ]; then
    case "$INIT_SYSTEM" in
      systemd) systemctl reload "$name" >/dev/null 2>&1 && return 0
               systemctl restart "$name" >/dev/null 2>&1 && return 0 ;;
      openrc)  rc-service "$name" reload >/dev/null 2>&1 && return 0
               rc-service "$name" restart >/dev/null 2>&1 && return 0 ;;
    esac
  fi
  if [ "$name" = nginx ]; then
    nginx -s reload >/dev/null 2>&1 || nginx >/dev/null 2>&1 || true
  fi
  return 0
}

install_services() {
  step "Installing the panel's services"

  # The supervisor may have arrived with the packages: on a minimal Alpine,
  # OpenRC is one of the things this installer has just laid down.
  detect_init

  case "$INIT_SYSTEM" in
    systemd) write_systemd_units; ok "systemd units written" ;;
    openrc)  write_openrc_units;  ok "OpenRC init scripts written" ;;
    none)    warn "no init system on this host: the services cannot be supervised or started at boot" ;;
  esac

  if [ "$INIT_RUNNING" = 1 ]; then
    case "$INIT_SYSTEM" in
      systemd)
        systemctl daemon-reload
        systemctl enable jothost-agent jothost-api >/dev/null 2>&1
        systemctl restart jothost-agent
        wait_for_socket
        systemctl restart jothost-api ;;
      openrc)
        rc-update add jothost-agent default >/dev/null 2>&1 || true
        rc-update add jothost-api default >/dev/null 2>&1 || true
        rc-service jothost-agent restart >/dev/null 2>&1 || rc-service jothost-agent start >/dev/null 2>&1
        wait_for_socket
        rc-service jothost-api restart >/dev/null 2>&1 || rc-service jothost-api start >/dev/null 2>&1 ;;
    esac
    ok "jothost-agent and jothost-api are enabled and running"
    return 0
  fi

  # Enabled for the next real boot, started directly for now. Both halves are
  # said out loud, because "enabled" and "running" are different claims and a
  # host where only one of them holds is one somebody needs to know about.
  case "$INIT_SYSTEM" in
    systemd) systemctl enable jothost-agent jothost-api >/dev/null 2>&1 || true ;;
    openrc)  rc-update add jothost-agent default >/dev/null 2>&1 || true
             rc-update add jothost-api default >/dev/null 2>&1 || true ;;
  esac
  start_directly
  warn "nothing is supervising this machine, so the services were started directly. They are enabled and will be supervised after a real boot."
}

# start_directly runs the two services without a supervisor.
#
# For a host whose init system is not running — a container, chiefly. The units
# are still installed and enabled; this only makes *this* run produce a panel
# that answers.
start_directly() {
  mkdir -p "$RUN_DIR"
  chown root:"$PANEL_GROUP" "$RUN_DIR"
  chmod 0750 "$RUN_DIR"

  pkill -f "$AGENT_BIN" >/dev/null 2>&1 || true
  pkill -f "$API_BIN" >/dev/null 2>&1 || true

  ( set -a; . "$AGENT_ENV"; set +a; nohup "$AGENT_BIN" >>"$LOG_DIR/agent.log" 2>&1 & )
  wait_for_socket
  ( set -a; . "$API_ENV"; set +a
    if command -v setpriv >/dev/null 2>&1; then
      nohup setpriv --reuid="$API_USER" --regid="$PANEL_GROUP" --clear-groups \
        "$API_BIN" serve >>"$LOG_DIR/api.log" 2>&1 &
    else
      nohup su -s /bin/sh "$API_USER" -c "$API_BIN serve" >>"$LOG_DIR/api.log" 2>&1 &
    fi )
}

wait_for_socket() {
  waited=0
  while [ "$waited" -lt 30 ]; do
    [ -S "$RUN_DIR/agent.sock" ] && return 0
    sleep 1; waited=$((waited + 1))
  done
  warn "the Agent's socket did not appear at $RUN_DIR/agent.sock"
  return 1
}

# ---------------------------------------------------------------- database

# prepare_database applies the migrations and creates the administrator.
#
# Run as the API's own account, not as root: it proves the credentials the API
# will actually use, against the database it will actually reach. Doing it as
# root would prove that root can connect, which is not the question.
prepare_database() {
  step "Preparing the panel's database"

  as_api "$API_BIN migrate up" || die "the migrations could not be applied"
  ok "schema up to date"

  ADMIN_PASSWORD=""
  if as_api "$API_BIN create-admin" \
       "JOTHOST_ADMIN_USERNAME=$ADMIN_USER" \
       "JOTHOST_ADMIN_PASSWORD=$1" \
       "JOTHOST_ADMIN_EMAIL=$EMAIL" >/dev/null 2>&1; then
    ADMIN_PASSWORD="$1"
    ok "administrator \"$ADMIN_USER\" created"
  else
    info "an account named \"$ADMIN_USER\" already exists; it was left alone"
  fi
}

# as_api runs a command as the API's account with its environment loaded.
#
# The environment is exported into the child rather than passed on the command
# line, for the reason api/cmd/api/admin.go gives: a password in argv is
# visible in the process list to every account on this machine.
as_api() {
  command_line="$1"; shift
  extra=""
  for pair in "$@"; do
    extra="$extra
export $pair"
  done
  su -s /bin/sh "$API_USER" <<EOF
set -a
. $API_ENV
set +a$extra
$command_line
EOF
}

# ---------------------------------------------------------------- firewall

# configure_firewall closes the machine to everything but SSH and the web.
#
# SSH first, always, and that order is the whole safety of the step: a firewall
# enabled before SSH is allowed is an operator locked out of a machine they may
# have no console for. CLAUDE.md section 19 requires a firewall change to be
# reversible; here the reversal is that nothing is enabled until the rule that
# keeps the operator connected is in place.
configure_firewall() {
  if [ "$SKIP_FIREWALL" = 1 ]; then
    step "Firewall"
    info "left alone (--no-firewall)"
    return 0
  fi

  step "Configuring the firewall"

  if command -v ufw >/dev/null 2>&1; then
    ufw allow OpenSSH >/dev/null 2>&1 || ufw allow 22/tcp >/dev/null 2>&1
    ufw allow 80/tcp >/dev/null 2>&1
    ufw allow 443/tcp >/dev/null 2>&1
    if ufw status 2>/dev/null | head -n 1 | grep -q inactive; then
      ufw --force enable >/dev/null 2>&1 || warn "ufw would not enable"
    fi
    ok "ufw allows SSH, HTTP and HTTPS"
  elif command -v firewall-cmd >/dev/null 2>&1; then
    firewall-cmd --permanent --add-service=ssh >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-service=http >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-service=https >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
    ok "firewalld allows SSH, HTTP and HTTPS"
  else
    # Not a failure. The panel's own firewall page manages ufw, and a host
    # without one is a host whose provider usually has a firewall in front.
    warn "no ufw or firewalld found; nothing was changed. Manage the firewall from the panel once it is running."
  fi
}

# ------------------------------------------------------------ health check

# health_check is what decides whether the install succeeded.
#
# Not "the commands returned zero" — this asks the panel, over the network, on
# the name it was given, whether it is answering. An installer that reported
# success without this would be reporting that it finished, which is a
# different claim from the one an operator needs.
health_check() {
  step "Checking that the panel answers"

  waited=0
  api_ok=0
  while [ "$waited" -lt 60 ]; do
    if curl -fsS --max-time 5 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
      api_ok=1; break
    fi
    sleep 2; waited=$((waited + 2))
  done
  if [ "$api_ok" = 1 ]; then
    ok "the API is healthy"
  else
    show_service_logs
    die "the API did not become healthy within 60 seconds"
  fi

  # readyz is the stronger question: healthz says the process is up, readyz
  # says it can reach PostgreSQL, Redis and the Agent. An install that passed
  # the first and failed the second is one that looks finished and works for
  # nothing.
  if curl -fsS --max-time 10 http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    ok "it can reach PostgreSQL, Redis and the Agent"
  else
    show_service_logs
    die "the API is running but is not ready: it cannot reach one of PostgreSQL, Redis or the Agent"
  fi

  scheme=https
  [ "$SKIP_TLS" = 1 ] && scheme=http
  resolve=""
  [ -n "$HEALTH_HOST" ] && resolve="--resolve $DOMAIN:443:$HEALTH_HOST --resolve $DOMAIN:80:$HEALTH_HOST"

  # -k because a self-signed certificate is a state this installer creates on
  # purpose; what is being checked here is that nginx serves the panel on the
  # right name, not that a certificate authority signed it.
  # The status code is captured separately from the body, because "could not be
  # reached" and "answered with 403" are different problems with different
  # fixes, and a check that reported the first for both would send somebody to
  # look at DNS when the answer was a permission on a directory.
  # shellcheck disable=SC2086
  code=$(curl -s -o /tmp/jothost-panel-body -w '%{http_code}' -k --max-time 15 \
    $resolve "$scheme://$DOMAIN/" 2>/dev/null || echo 000)
  body=$(cat /tmp/jothost-panel-body 2>/dev/null || true)
  rm -f /tmp/jothost-panel-body

  case "$code" in
    200)
      case "$body" in
        *'<div id="root"'*|*'<title>'*) ok "$DOMAIN serves the panel" ;;
        *) warn "$DOMAIN answered, but not with the panel's page" ;;
      esac ;;
    000)
      warn "$DOMAIN could not be reached from this machine. The panel is running; check DNS and the firewall." ;;
    403)
      warn "$DOMAIN answered 403: nginx cannot read $FRONTEND_DIR. Run: $0 repair" ;;
    *)
      warn "$DOMAIN answered HTTP $code rather than serving the panel" ;;
  esac

  # shellcheck disable=SC2086
  if curl -fsSk --max-time 15 $resolve "$scheme://$DOMAIN/api/v1/health" >/dev/null 2>&1; then
    ok "the API is reachable through nginx at $scheme://$DOMAIN/api/v1"
  else
    warn "the API is not reachable through nginx on $DOMAIN"
  fi
}

show_service_logs() {
  say ""
  say "The last lines from the services:"
  if [ "${INIT_SYSTEM:-}" = systemd ] && [ "${INIT_RUNNING:-0}" = 1 ]; then
    journalctl -u jothost-api -n 20 --no-pager 2>/dev/null | sed 's/^/     /' || true
  else
    tail -n 20 "$LOG_DIR/api.log" 2>/dev/null | sed 's/^/     /' || true
  fi
}

# ---------------------------------------------------------------- commands

do_install() {
  [ -n "$DOMAIN" ] || die "--domain is required (the name the panel will be served on)"
  valid_domain "$DOMAIN" || die "\"$DOMAIN\" is not a domain name"
  [ -z "$EMAIL" ] || valid_email "$EMAIL" || die "\"$EMAIL\" is not an email address"
  valid_username "$ADMIN_USER" ||
    die "\"$ADMIN_USER\" is not a usable username (3-32 characters of lowercase letters, digits, dot, dash or underscore)"

  detect_host
  find_artefacts

  say ""
  say "  ${C_BOLD}JotHost Panel $ARTEFACT_VERSION${C_OFF}"
  say "  domain        $DOMAIN"
  say "  administrator $ADMIN_USER"
  say "  artefacts     $ARTEFACT_DIR"
  say "  host          $OS_NAME, $ARCH, init: $INIT_SYSTEM"
  confirm "Install onto this machine?" || { say "Nothing was changed."; exit 0; }

  install_dependencies
  create_accounts
  install_artefacts
  setup_postgres
  setup_redis
  write_configuration
  configure_nginx
  setup_tls

  # The vhost is written twice on purpose: once without TLS so the challenge
  # above could be answered, and now with the certificate that produced.
  if [ "$SKIP_TLS" != 1 ]; then
    sites_dir=$(nginx_sites_dir)
    write_panel_vhost "$sites_dir/$PANEL_VHOST_NAME" tls
    nginx -t >/dev/null 2>&1 || {
      nginx -t 2>&1 | sed 's/^/     /' >&2
      die "nginx rejected the configuration with the certificate in it"
    }
    service_reload nginx
  fi

  install_services
  generated_password=$(generate_secret 12)
  prepare_database "$generated_password"
  configure_firewall
  health_check

  report_success
}

do_update() {
  detect_host
  find_artefacts
  load_install_env

  step "Updating to $ARTEFACT_VERSION"
  info "from $(head -n 1 "$PREFIX/VERSION" 2>/dev/null || echo unknown)"

  install_artefacts
  # The configuration is rewritten so a new setting gains its default, and
  # every secret in it is carried across unchanged. See write_configuration.
  create_accounts
  write_configuration
  install_services
  as_api "$API_BIN migrate up" >/dev/null || die "the migrations could not be applied"
  ok "schema up to date"
  health_check

  say ""
  say "${C_OK}Updated to $ARTEFACT_VERSION.${C_OFF}"
}

# do_repair re-runs the reconciling steps against an install that exists.
#
# Everything in this file is idempotent, so "repair" is not a separate
# mechanism: it is the ordinary steps, run again, on a machine where somebody
# has changed something by hand or where an earlier run stopped halfway. It is
# also how a self-signed certificate is replaced once DNS points at the host.
do_repair() {
  detect_host
  load_install_env
  [ -x "$API_BIN" ] || die "no installation found at $PREFIX; run: $0 install --domain ..."

  ARTEFACT_VERSION=$(head -n 1 "$PREFIX/VERSION" 2>/dev/null || echo unknown)

  create_accounts
  setup_postgres
  setup_redis
  write_configuration
  configure_nginx
  setup_tls
  if [ "$SKIP_TLS" != 1 ]; then
    sites_dir=$(nginx_sites_dir)
    write_panel_vhost "$sites_dir/$PANEL_VHOST_NAME" tls
    nginx -t >/dev/null 2>&1 && service_reload nginx
  fi
  install_services
  as_api "$API_BIN migrate up" >/dev/null || warn "the migrations could not be applied"
  health_check

  say ""
  say "${C_OK}Repair complete.${C_OFF}"
}

# do_uninstall removes the panel, and by default removes nothing else.
#
# The customers' websites, their databases and their mail stay where they are:
# an uninstall of the *panel* that deleted what the panel was managing would be
# the single most destructive thing this script could do, and nobody typing
# "uninstall" is asking for it. --purge is the deliberate second answer, and it
# is confirmed separately.
do_uninstall() {
  detect_host
  load_install_env || true

  say ""
  say "This removes the JotHost Panel: its binaries, services and configuration."
  if [ "$PURGE" = 1 ]; then
    say "${C_ERR}--purge also deletes the panel's database and its state directory.${C_OFF}"
    say "Websites in $SITE_ROOT and their own databases are NOT touched, even with --purge."
  else
    say "The panel's database and state are kept. Add --purge to remove those too."
  fi
  confirm "Remove the panel?" || { say "Nothing was changed."; exit 0; }

  step "Stopping the services"
  case "$INIT_SYSTEM" in
    systemd)
      systemctl disable --now jothost-api jothost-agent >/dev/null 2>&1 || true
      rm -f /etc/systemd/system/jothost-api.service /etc/systemd/system/jothost-agent.service
      systemctl daemon-reload >/dev/null 2>&1 || true ;;
    openrc)
      rc-service jothost-api stop >/dev/null 2>&1 || true
      rc-service jothost-agent stop >/dev/null 2>&1 || true
      rc-update del jothost-api default >/dev/null 2>&1 || true
      rc-update del jothost-agent default >/dev/null 2>&1 || true
      rm -f /etc/init.d/jothost-api /etc/init.d/jothost-agent ;;
  esac
  pkill -f "$API_BIN" >/dev/null 2>&1 || true
  pkill -f "$AGENT_BIN" >/dev/null 2>&1 || true
  ok "services stopped and removed"

  step "Removing the panel's own files"
  sites_dir=$(nginx_sites_dir)
  rm -f "$sites_dir/$PANEL_VHOST_NAME"
  nginx -t >/dev/null 2>&1 && service_reload nginx
  rm -rf "$PREFIX"
  ok "$PREFIX and the panel's vhost removed"

  if [ "$PURGE" = 1 ]; then
    step "Purging the panel's data"
    su postgres -c "psql -v ON_ERROR_STOP=1 -q" >/dev/null 2>&1 <<SQL || warn "the panel's database could not be dropped"
DROP DATABASE IF EXISTS $PG_DB;
DROP ROLE IF EXISTS $PG_USER;
SQL
    rm -rf "$STATE_DIR" "$CONFIG_DIR"
    ok "database, state and configuration removed"
    say ""
    say "Websites in $SITE_ROOT were left alone, along with their own databases."
  else
    info "$CONFIG_DIR and $STATE_DIR kept; install again to reuse them"
  fi

  say ""
  say "${C_OK}JotHost Panel removed.${C_OFF}"
}

do_status() {
  detect_host >/dev/null 2>&1 || true
  load_install_env || true

  say ""
  say "${C_BOLD}JotHost Panel${C_OFF}"
  if [ -x "$API_BIN" ]; then
    say "  installed     $(head -n 1 "$PREFIX/VERSION" 2>/dev/null || echo unknown) at $PREFIX"
  else
    say "  installed     no"
    exit 0
  fi
  say "  domain        ${PANEL_DOMAIN:-unknown}"
  say "  configuration $CONFIG_DIR"

  for service in jothost-agent jothost-api nginx; do
    state=unknown
    if [ "${INIT_RUNNING:-0}" = 1 ] && [ "${INIT_SYSTEM:-none}" = systemd ]; then
      state=$(systemctl is-active "$service" 2>/dev/null || echo inactive)
    elif [ "${INIT_RUNNING:-0}" = 1 ] && [ "${INIT_SYSTEM:-none}" = openrc ]; then
      rc-service "$service" status >/dev/null 2>&1 && state=active || state=inactive
    else
      pgrep -f "$service" >/dev/null 2>&1 && state=running || state=stopped
    fi
    say "  $service$(printf '%*s' $((16 - ${#service})) '')$state"
  done

  if curl -fsS --max-time 5 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then
    say "  api           healthy"
  else
    say "  api           not answering"
  fi
  if curl -fsS --max-time 5 http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    say "  dependencies  ready"
  else
    say "  dependencies  not ready"
  fi
  say ""
}

load_install_env() {
  [ -f "$INSTALL_ENV" ] || return 1
  # shellcheck disable=SC1090
  . "$INSTALL_ENV"
  [ -n "$DOMAIN" ] || DOMAIN="${PANEL_DOMAIN:-}"
  [ "${PANEL_TLS:-}" = none ] && SKIP_TLS=1
  return 0
}

report_success() {
  scheme=https
  [ "$SKIP_TLS" = 1 ] && scheme=http

  say ""
  say "${C_OK}${C_BOLD}JotHost Panel is installed and running.${C_OFF}"
  say ""
  say "  ${C_BOLD}$scheme://$DOMAIN${C_OFF}"
  say ""
  if [ -n "$ADMIN_PASSWORD" ]; then
    say "  username  $ADMIN_USER"
    say "  password  ${C_BOLD}$ADMIN_PASSWORD${C_OFF}"
    say ""
    say "  ${C_WARN}This password is shown once and is not stored anywhere.${C_OFF}"
    say "  Sign in, change it, and turn on two-factor authentication."
  else
    say "  The administrator account already existed; its password is unchanged."
  fi
  if [ "${TLS_TRUSTED:-1}" = 0 ]; then
    say ""
    say "  ${C_WARN}The certificate is self-signed, so browsers will warn.${C_OFF}"
    say "  Once $DOMAIN resolves to this machine, run:  $0 repair"
  fi
  if [ "$WARNINGS" -gt 0 ]; then
    say ""
    say "  ${C_WARN}$WARNINGS warning(s) above are worth reading.${C_OFF}"
  fi
  say ""
}

# -------------------------------------------------------------------- main

main() {
  parse_args "$@"
  case "$COMMAND" in
    install)   do_install ;;
    update)    do_update ;;
    repair)    do_repair ;;
    uninstall) do_uninstall ;;
    status)    do_status ;;
  esac
}

main "$@"
