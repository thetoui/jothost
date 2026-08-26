#!/bin/sh
# Starts the managed host's nginx, then the Agent.
#
# In production these are separate: nginx is a systemd unit and the Agent is
# another, each supervised independently. A container has one process tree, so
# nginx is started here in the background and the Agent runs in the foreground
# as PID 1 — which keeps the container's lifecycle tied to the Agent, the
# process this container exists to run.

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

exec /usr/local/bin/jothost-agent "$@"
