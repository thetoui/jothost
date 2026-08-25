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
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 jothost

COPY --from=builder /out/jothost-agent /usr/local/bin/jothost-agent

# No EXPOSE: the Agent listens only on a Unix socket, never on TCP
# (ARCHITECTURE.md section 10).

# The agent has no HTTP endpoint, so the health check speaks the agent
# protocol over its own Unix socket.
HEALTHCHECK --interval=10s --timeout=5s --start-period=5s --retries=5 \
  CMD ["/usr/local/bin/jothost-agent", "-ping"]

ENTRYPOINT ["/usr/local/bin/jothost-agent"]
