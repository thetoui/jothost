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

# The Agent writes vhosts here and reloads nginx to apply them. Starting nginx
# first means a reload during website creation has something to reload.
if command -v nginx >/dev/null 2>&1; then
  mkdir -p /run/nginx /etc/nginx/conf.d
  if nginx -t >/dev/null 2>&1; then
    nginx
    echo "agent-entrypoint: nginx started"
  else
    # A broken base config must not stop the Agent: metrics, services, and
    # every other operation still work, and website operations will report
    # the failure rather than the whole container refusing to start.
    echo "agent-entrypoint: nginx configuration is invalid; websites will be unavailable" >&2
    nginx -t >&2 || true
  fi
fi

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
  mariadbd --user=mysql --datadir=/var/lib/mysql \
    --socket=/run/mysqld/mysqld.sock --skip-networking >/dev/null 2>&1 &

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

  su postgres -c "pg_ctl -D /var/lib/postgresql/data -w -t 30 -l /tmp/postgres.log start" \
    >/dev/null 2>&1 || {
    echo "agent-entrypoint: PostgreSQL did not start; the engine will be unavailable" >&2
    cat /tmp/postgres.log >&2 2>/dev/null || true
    return 0
  }
  echo "agent-entrypoint: PostgreSQL ready"
}

start_mariadb
start_postgres

exec /usr/local/bin/jothost-agent "$@"
