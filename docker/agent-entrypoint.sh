#!/bin/sh
# Starts the managed host's services, then the Agent.
#
# In production these are separate: nginx, MariaDB and PostgreSQL are systemd
# units and the Agent is another, each supervised independently. A container has
# one process tree, so the servers are started here in the background and the
# Agent runs in the foreground as PID 1 — which keeps the container's lifecycle
# tied to the Agent, the process this container exists to run.
#
# Every command here is a fixed string written in this file. Nothing in it comes
# from a request, so the shell used to sequence them is not an execution path an
# attacker can reach (CLAUDE.md section 6 governs commands built from input).

set -eu

# ----------------------------------------------------------------- OpenRC
#
# The daemons below are started *through* OpenRC rather than directly, so that
# one thing owns each of them.
#
# That is what makes the panel's service manager real here. A daemon started
# behind the init system's back is one the init system reports as stopped and
# refuses to stop — two owners for one process, and a panel whose buttons fail
# for reasons an operator cannot see. On a real Alpine host OpenRC starts these
# at boot and the same arrangement holds.
#
# OpenRC keeps its state under /run, which a container starts empty. On a real
# host the boot sequence builds it; here that has to be asked for, and until it
# exists every rc-service call answers "already starting".
if command -v rc-status >/dev/null 2>&1; then
  mkdir -p /run/openrc
  touch /run/openrc/softlevel
  # Building the dependency cache is what creates the state directories.
  rc-status >/dev/null 2>&1 || true
  echo "agent-entrypoint: OpenRC ready"
fi

# start_service NAME — start one daemon through OpenRC, or directly if OpenRC
# is not there.
#
# The fallback matters for the image without OpenRC and for anyone running the
# binary outside a container: a missing init system must degrade to "started,
# not managed" rather than to "not started".
start_service() {
  name="$1"

  if command -v rc-service >/dev/null 2>&1 && [ -f "/etc/init.d/$name" ]; then
    if rc-service "$name" start >/dev/null 2>&1; then
      echo "agent-entrypoint: $name started (OpenRC)"
      return 0
    fi
    echo "agent-entrypoint: $name did not start under OpenRC" >&2
    rc-service "$name" start 2>&1 | tail -3 >&2 || true
    return 1
  fi

  return 1
}

# The Agent writes vhosts here and reloads nginx to apply them. Starting nginx
# first means a reload during website creation has something to reload.
if command -v nginx >/dev/null 2>&1; then
  mkdir -p /run/nginx /etc/nginx/conf.d
  if nginx -t >/dev/null 2>&1; then
    start_service nginx || {
      nginx
      echo "agent-entrypoint: nginx started directly"
    }
  else
    # A broken base config must not stop the Agent: metrics, services, and
    # every other operation still work, and website operations will report
    # the failure rather than the whole container refusing to start.
    echo "agent-entrypoint: nginx configuration is invalid; websites will be unavailable" >&2
    nginx -t >&2 || true
  fi
fi

# ------------------------------------------------------- system accounts
#
# Development only, and only because of how this container is built.
#
# The panel creates a Unix account per website. Those live in /etc/passwd,
# which is part of the image's filesystem — so rebuilding the image deletes
# every account while /var/www, which is a volume, keeps every site's files.
# The result is a host whose sites exist on disk, are listed in the panel, and
# cannot be served because the account that owns them is gone.
#
# On a real machine none of this applies: the host is not rebuilt from an image
# and /etc/passwd is simply a file that persists. Here the panel-created lines
# are copied to a volume and put back on start.
#
# Only accounts this panel creates are touched: web_* for websites and
# jothost_* for the panel's own service accounts. Nothing the base image
# defines is ever written.

ACCOUNTS_DIR=/var/lib/jothost/accounts

restore_accounts() {
  [ -d "$ACCOUNTS_DIR" ] || return 0

  for file in passwd group shadow; do
    saved="$ACCOUNTS_DIR/$file"
    [ -f "$saved" ] || continue

    while IFS= read -r line; do
      name="${line%%:*}"
      [ -n "$name" ] || continue
      # Already present in this image's copy: leave it alone.
      if cut -d: -f1 "/etc/$file" | grep -qx "$name"; then
        continue
      fi
      printf '%s\n' "$line" >> "/etc/$file"
      echo "agent-entrypoint: restored $file entry for $name"
    done < "$saved"
  done
}

save_accounts() {
  mkdir -p "$ACCOUNTS_DIR"
  for file in passwd group shadow; do
    grep -E '^(web_|jothost_)' "/etc/$file" > "$ACCOUNTS_DIR/$file.tmp" 2>/dev/null || true
    mv "$ACCOUNTS_DIR/$file.tmp" "$ACCOUNTS_DIR/$file" 2>/dev/null || true
  done
  chmod 600 "$ACCOUNTS_DIR/shadow" 2>/dev/null || true
}

restore_accounts
save_accounts

# The panel creates accounts while running, and this container has no way to be
# told when. A periodic copy is crude, but it is a development convenience
# rather than a mechanism anything depends on.
( while true; do sleep 30; save_accounts; done ) &

# ------------------------------------------------------------------- MariaDB

start_mariadb() {
  command -v mariadbd >/dev/null 2>&1 || return 0

  mkdir -p /run/mysqld /var/lib/mysql
  chown -R mysql:mysql /run/mysqld /var/lib/mysql

  # The data directory is a named volume, so it survives an image rebuild and
  # is initialised only the first time. Re-running install_db over a populated
  # directory is what destroys a development database.
  if [ ! -d /var/lib/mysql/mysql ]; then
    echo "agent-entrypoint: initialising MariaDB"
    mariadb-install-db --user=mysql --datadir=/var/lib/mysql --skip-test-db >/dev/null 2>&1 || {
      echo "agent-entrypoint: MariaDB could not be initialised; the engine will be unavailable" >&2
      return 0
    }
  fi

  # skip-networking: this server is reached only over its Unix socket, from the
  # Agent on the same host. No port is published and none is listened on, which
  # is the same posture as the Agent's own socket.
  start_service mariadb || {
    mariadbd --user=mysql --datadir=/var/lib/mysql \
      --socket=/run/mysqld/mysqld.sock --skip-networking >/dev/null 2>&1 &
    echo "agent-entrypoint: MariaDB started directly"
  }

  # The Agent probes each engine once at startup, so the server has to be
  # answering before the Agent runs or the panel would report MariaDB missing
  # until the next restart.
  waited=0
  while [ "$waited" -lt 30 ]; do
    if mariadb --socket=/run/mysqld/mysqld.sock --user=root \
         --batch --skip-column-names -e 'SELECT 1' >/dev/null 2>&1; then
      echo "agent-entrypoint: MariaDB ready"
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "agent-entrypoint: MariaDB did not become ready; the engine will be unavailable" >&2
}

# ---------------------------------------------------------------- PostgreSQL

start_postgres() {
  command -v pg_ctl >/dev/null 2>&1 || return 0

  mkdir -p /run/postgresql
  chown postgres:postgres /run/postgresql

  if [ ! -f /var/lib/postgresql/data/PG_VERSION ]; then
    echo "agent-entrypoint: initialising PostgreSQL"

    # Password authentication rather than trust, even for local connections.
    #
    # trust would be simpler, but it would also make every password this panel
    # sets meaningless: any role could connect as any other without one, so a
    # test that "the account can connect with its password" would pass no
    # matter what the password was. scram-sha-256 is what a real host uses, and
    # it is what makes the Agent's own pgpass path — the code that keeps the
    # admin password out of argv — actually get exercised.
    umask 077
    printf '%s\n' "${AGENT_POSTGRES_ADMIN_PASSWORD:-postgres}" > /tmp/pgpw
    chown postgres:postgres /tmp/pgpw

    su postgres -c "initdb -D /var/lib/postgresql/data \
      --auth-local=scram-sha-256 --auth-host=reject --pwfile=/tmp/pgpw" >/dev/null 2>&1 || {
      echo "agent-entrypoint: PostgreSQL could not be initialised; the engine will be unavailable" >&2
      rm -f /tmp/pgpw
      return 0
    }
    rm -f /tmp/pgpw

    # listen_addresses is left empty: like MariaDB above, this server is
    # reachable only over its Unix socket, from the Agent on the same host.
    {
      echo "listen_addresses = ''"
      echo "unix_socket_directories = '/run/postgresql'"
    } >> /var/lib/postgresql/data/postgresql.conf
  fi

  start_service postgresql || {
    su postgres -c "pg_ctl -D /var/lib/postgresql/data -w -t 30 -l /tmp/postgres.log start" \
      >/dev/null 2>&1 || {
      echo "agent-entrypoint: PostgreSQL did not start; the engine will be unavailable" >&2
      cat /tmp/postgres.log >&2 2>/dev/null || true
      return 0
    }
    echo "agent-entrypoint: PostgreSQL started directly"
  }

  # The Agent probes each engine at startup, so the server has to be answering
  # before it runs.
  waited=0
  while [ "$waited" -lt 30 ]; do
    if su postgres -c "psql -h /run/postgresql -l" >/dev/null 2>&1; then
      echo "agent-entrypoint: PostgreSQL ready"
      return 0
    fi
    sleep 1
    waited=$((waited + 1))
  done
  echo "agent-entrypoint: PostgreSQL did not become ready" >&2
}

start_mariadb
start_postgres

# ---------------------------------------------------------------------- cron
#
# The daemon that runs what the panel schedules. Started through OpenRC like
# everything else here, so the service manager can stop and start it and the
# panel's page tells the truth about it.
#
# The spool directory is created if it is missing: the Agent writes crontab
# files into it, and a missing directory is how a panel accepts jobs it can
# never write. /etc/crontabs is Alpine's, and /var/spool/cron/crontabs — the
# path every other distribution uses, and the one the Agent is configured with —
# is a symlink to it in this image.
mkdir -p /etc/crontabs
if command -v crond >/dev/null 2>&1; then
  start_service crond || {
    crond -c /etc/crontabs -L /var/log/crond.log
    echo "agent-entrypoint: crond started directly"
  }
else
  echo "agent-entrypoint: no cron daemon; scheduled jobs will not run" >&2
fi

exec /usr/local/bin/jothost-agent "$@"
