# JotHost Host Agent image.
# In production the Agent runs as root on the host; in development it runs in a
# container so all Linux-specific behaviour is exercised on Linux
# (ARCHITECTURE.md section 16).

# ---------- builder ----------
FROM golang:1.23-alpine AS builder

ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src
COPY shared/ ./shared/
COPY agent/ ./agent/

WORKDIR /src/agent
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/jothost/panel/shared/version.Version=${VERSION} \
        -X github.com/jothost/panel/shared/version.Commit=${COMMIT} \
        -X github.com/jothost/panel/shared/version.BuildDate=${BUILD_DATE}" \
      -o /out/jothost-agent ./cmd/agent

# ---------- dev ----------
FROM golang:1.23-alpine AS dev
WORKDIR /src/agent
ENV GOCACHE=/tmp/gocache GOFLAGS=-mod=mod
CMD ["go", "run", "./cmd/agent"]

# ---------- runtime ----------
FROM alpine:3.21 AS runtime
# The Agent runs as root, but the socket it creates is group-owned by jothost
# so the unprivileged API can reach it without world-writable permissions.
# The GID must match the API image's jothost user.
#
# nginx, shadow, and PHP are installed because this container *is* the managed
# host in development: the Agent provisions websites here, so the tools it
# drives have to be present. In production the Agent runs on a real host that
# already has them. shadow provides useradd/userdel, which BusyBox's adduser
# cannot fully replace for system accounts.
#
# Three PHP versions are installed so per-site version selection is exercised
# against real binaries rather than mocked, which is what TASKS.md Phase 5 asks
# for. The default www.conf pools are removed: each listens on a TCP port as
# "nobody" and would run alongside the per-site pools the panel writes.
#
# certbot is installed so its code path is exercised as far as it can be here.
# It cannot actually issue in this container: ACME needs public DNS and a
# reachable challenge, and a private network has neither. Self-signed
# certificates are what the tests drive end to end (docs/PHASE6.md section 3.1).
#
# MariaDB and PostgreSQL are both installed for the same reason: this container
# is the managed host, so Phase 8 has to drive real servers. Both rather than
# one, because the two providers differ in exactly the places most likely to be
# wrong — PostgreSQL has no CREATE DATABASE IF NOT EXISTS, no user/host pairs,
# and per-schema rather than per-database privileges — and a provider only ever
# exercised against a mock is a provider nobody has actually run.
#
# This PostgreSQL is not the panel's own. The control-plane database is a
# separate container, and the distinction matters: dropping a database on this
# host must never be able to reach the panel's own tables.
# ufw and iptables are the firewall the panel manages (Phase 16). They are in
# the image rather than installed on demand because a firewall the panel can
# only manage after a download is one that is missing exactly when it is needed.
RUN apk add --no-cache ca-certificates tzdata nginx shadow       iptables ip6tables ufw \
      php82-fpm php83-fpm php84-fpm \
      php82-opcache php83-opcache php84-opcache \
      php82-session php83-session php84-session \
      certbot \
      mariadb mariadb-client \
      postgresql16 postgresql16-client \
    && addgroup -g 10001 jothost \
    && mkdir -p /etc/nginx/conf.d /var/www /run/nginx /run/php-fpm \
                /etc/jothost/ssl /var/www/.acme-challenge/.well-known/acme-challenge \
                /run/mysqld /var/lib/mysql /run/postgresql /var/lib/postgresql/data \
    && chown -R mysql:mysql /run/mysqld /var/lib/mysql \
    && chown -R postgres:postgres /run/postgresql /var/lib/postgresql \
    && chmod 0700 /var/lib/postgresql/data \
    && rm -f /etc/nginx/http.d/default.conf \
    && rm -f /etc/php82/php-fpm.d/www.conf \
             /etc/php83/php-fpm.d/www.conf \
             /etc/php84/php-fpm.d/www.conf

COPY --from=builder /out/jothost-agent /usr/local/bin/jothost-agent
COPY docker/nginx/host.conf /etc/nginx/nginx.conf
COPY docker/agent-entrypoint.sh /usr/local/bin/agent-entrypoint

RUN chmod +x /usr/local/bin/agent-entrypoint

# The Agent itself still listens only on a Unix socket (ARCHITECTURE.md
# section 10). Ports 80 and 443 belong to the nginx this container manages,
# which serves the websites the panel creates — not the panel itself.
EXPOSE 80 443

# The agent has no HTTP endpoint, so the health check speaks the agent
# protocol over its own Unix socket.
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=5 \
  CMD ["/usr/local/bin/jothost-agent", "-ping"]

ENTRYPOINT ["/usr/local/bin/agent-entrypoint"]
