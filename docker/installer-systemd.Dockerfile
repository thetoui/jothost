# A Debian-family host running systemd, for installing the panel onto.
#
# The installer chooses its package manager and its service manager from what
# it finds, and for a long time every check of it ran on Alpine with OpenRC.
# That left the systemd and apt branches - roughly half the installer, and the
# half most operators will actually run - never once executed. When they first
# were, HTTPS turned out not to work on Debian at all.
#
# One image, several bases. PRD.md names "Ubuntu / Debian" as the production
# target, so Debian 12 and both current Ubuntu LTS releases are built from this
# file with BASE_IMAGE. They differ where it matters to the installer - nginx
# 1.18, 1.22 and 1.24 all predate the HTTP/2 directive change in 1.25.1, and
# PostgreSQL and PHP are different major versions on each - which is the reason
# to run all three rather than trust one to stand for the others.
#
# systemd is PID 1 here rather than a daemon started by a script. That is the
# whole point: `systemctl start` against a system where systemd is not running
# fails in a way that says nothing about whether the unit file was right, and a
# test that accepted it would be checking the installer's error handling
# instead of its work.
ARG BASE_IMAGE=debian:12
FROM ${BASE_IMAGE}

# Nothing else. The installer has to bring nginx, PostgreSQL, Redis and the
# rest with it, and a host that already had them would hide the step that
# installs them.
#
# ca-certificates because the installer fetches over HTTPS, curl because the
# checks afterwards ask the panel over HTTP whether it is really there, and
# netcat because one of them stands a listener on the panel's own port to show
# the proxy reaches that port rather than merely failing to reach anything.
RUN apt-get update \
 && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv ca-certificates curl procps netcat-openbsd \
 && rm -rf /var/lib/apt/lists/*

# The units these distributions start by default that a container has no use
# for. Left in, getty respawns on ttys that do not exist and systemd never
# reaches a settled state, so waiting for "running" times out. Masking a unit
# a given release does not ship is harmless.
RUN systemctl mask getty.target getty-static.service console-getty.service \
      systemd-logind.service systemd-udevd.service \
      systemd-udev-trigger.service systemd-journald-audit.socket

STOPSIGNAL SIGRTMIN+3

CMD ["/lib/systemd/systemd"]
