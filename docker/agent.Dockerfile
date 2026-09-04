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
#
# fail2ban is preinstalled for the same reason the databases are: the panel can
# install it, and a validator that is not installed is a validator that gets
# stubbed. Phase 18 asks fail2ban itself whether a configuration is acceptable
# and reads the resulting policy back out of the running daemon, and neither
# check means anything against a mock.
#
# Apache is preinstalled here even though the panel installs it on demand, and
# the two facts are not in conflict.
#
# On a real host, installing Apache when hybrid mode is switched on is right: a
# machine serving everything from nginx should not carry a second web server it
# never starts. In this image it is wrong, because the panel's *record* of the
# mode lives in Postgres — which has a volume and survives everything — while
# the package lives in this container's writable layer, which is discarded every
# time the image is rebuilt. The two then disagree: the panel says "hybrid" and
# the host has no Apache, and the first thing anybody notices is that creating a
# website fails.
#
# The package list is the one agent/internal/operations/apache.go installs;
# apache2-proxy is not optional, because PHP runs through mod_proxy_fcgi.
#
# openssh-client is here for a different reason from openssh-server: Phase 14
# sends backups to another machine with the sftp client, and a destination kind
# that is only ever exercised against a mock is a destination kind nobody has
# actually used. The integration test points an SFTP destination at this
# container's own sshd, which is a real SSH connection with a real host key
# check over a real key pair.
#
# OpenSSH is here because Phase 17 configures it, and configuring sshd is the
# one thing in this panel that can lock an operator out of their own machine.
# Every change is validated by asking sshd itself, and a validator that is not
# installed is a validator that gets stubbed — so the container runs the real
# server, on its own isolated network, where a change that breaks it breaks
# nothing anybody needs.
#
# busybox-openrc brings the init script for crond, which Alpine's base image has
# the daemon for and no way to start. Phase 10 schedules jobs by writing crontab
# entries, so a host where nothing read them would let the panel accept jobs that
# never run — and the integration test proves a job fires by waiting for one.
#
# php84 is the command-line interpreter, as distinct from php84-fpm above. A web
# server needs only FPM, which is why the CLI is a separate package on every
# distribution; a scheduled PHP job runs the CLI, so this host needs both.
#
# bind is the authoritative name server Phase 13 drives, with bind-tools for
# named-checkzone and dig and bind-dnssec-tools for the DS records a registrar
# asks for. It is baked in for the same reason as everything else in this
# paragraph: the zones the panel records live in Postgres and survive a rebuild,
# and a host with the records and no server to answer them is a domain that has
# quietly stopped resolving.
#
# nodejs and npm are here for the reason Apache is, further up. Phase 9 can
# install them through the host's package manager, and on a real host that is
# the right moment to do it. In this image it left the panel's record and the
# host disagreeing after every rebuild: the node_apps rows survive in Postgres
# while the runtime they need goes away with the writable layer, so an
# application the panel still lists as running has nothing to run it. The
# installer is not made redundant by this: it is what a real host uses, and its
# own tests still cover it.
#
# OpenRC is this host's init system, and the one the panel's service manager
# drives here. Alpine has no systemd — it is not a package that exists — so a
# panel speaking only systemd could report what runs on an Alpine host and
# change none of it.
# Postfix, Dovecot and Rspamd are baked in for the same reason the databases
# are: this container is the managed host, and Phase 26 has to drive real
# daemons. A mail server is the one thing in this panel that cannot be tested
# against a mock at all - the failures that matter are an open relay, a
# passwd-file Dovecot will not read, and a milter that silently stops signing,
# and every one of those is a property of a running server rather than of a
# generated file.
#
# ClamAV is deliberately *not* here. Its signature database is several hundred
# megabytes, virus scanning is off by default, and the panel installs it on
# demand - which is the code path a real host takes.
#
# The php84 extensions are Roundcube's. Webmail is a PHP application served
# from a website the panel created, and a missing extension is an installation
# that unpacks cleanly and then answers every request with a blank page.
RUN apk add --no-cache ca-certificates tzdata nginx shadow \
      iptables ip6tables ufw \
      openrc busybox-openrc \
      openssh-server openssh-keygen openssh-client \
      fail2ban \
      apache2 apache2-proxy \
      proftpd proftpd-utils proftpd-openrc \
      proftpd-mod_tls proftpd-mod_quotatab proftpd-mod_quotatab_file \
      bind bind-tools bind-openrc bind-dnssec-tools \
      nodejs npm \
      php82-fpm php83-fpm php84-fpm \
      php84 \
      php82-opcache php83-opcache php84-opcache \
      php82-session php83-session php84-session \
      php84-pdo php84-pdo_sqlite php84-dom php84-xml php84-mbstring \
      php84-iconv php84-openssl php84-zip php84-intl php84-gd php84-fileinfo \
      php84-ctype \
      postfix postfix-pcre \
      dovecot dovecot-lmtpd dovecot-pop3d dovecot-pigeonhole-plugin \
      rspamd rspamd-client rspamd-openrc \
      certbot \
      mariadb mariadb-client \
      postgresql16 postgresql16-client \
    && addgroup -g 10001 jothost \
    && mkdir -p /etc/nginx/conf.d /var/www /run/nginx /run/php-fpm \
                /var/mail/vhosts /var/lib/jothost/mail \
                /var/lib/jothost/backups /backups \
                /etc/jothost/ssl /var/www/.acme-challenge/.well-known/acme-challenge \
                /run/mysqld /var/lib/mysql /run/postgresql /var/lib/postgresql/data \
    && chown -R mysql:mysql /run/mysqld /var/lib/mysql \
    && chown -R postgres:postgres /run/postgresql /var/lib/postgresql \
    && chmod 0700 /var/lib/postgresql/data \
    && rm -f /etc/nginx/http.d/default.conf \
    && rm -f /etc/php82/php-fpm.d/www.conf \
             /etc/php83/php-fpm.d/www.conf \
             /etc/php84/php-fpm.d/www.conf

# OpenRC starts PostgreSQL from its own configuration, which expects the cluster
# under a version directory. This image keeps it one level up, so OpenRC is told
# where to look — otherwise the service manager would start a second, empty
# cluster beside the one the panel's databases are in.
COPY docker/openrc/postgresql.conf /etc/conf.d/postgresql

# crond writes to a file rather than to syslog, because this container runs no
# syslog daemon — and because the panel's log viewer already looks for exactly
# this path as its "cron" source. Without it, cron's own record of what it ran
# would go nowhere.
COPY docker/openrc/crond.conf /etc/conf.d/crond

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
