# JotHost API image.
# Build context is the repository root: the api module depends on ./shared
# through a local replace directive.

# ---------- builder ----------
FROM golang:1.23-alpine AS builder

ARG VERSION=0.1.0-dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /src
COPY shared/ ./shared/
COPY api/ ./api/

WORKDIR /src/api
# CGO is disabled so the binary runs on a scratch-like runtime image.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags "-s -w \
        -X github.com/jothost/panel/shared/version.Version=${VERSION} \
        -X github.com/jothost/panel/shared/version.Commit=${COMMIT} \
        -X github.com/jothost/panel/shared/version.BuildDate=${BUILD_DATE}" \
      -o /out/jothost-api ./cmd/api

# ---------- dev ----------
# Source is bind-mounted; the API is rebuilt on container start.
FROM golang:1.23-alpine AS dev
RUN adduser -D -u 10001 jothost
WORKDIR /src/api
ENV GOCACHE=/tmp/gocache GOFLAGS=-mod=mod
CMD ["go", "run", "./cmd/api"]

# ---------- runtime ----------
FROM alpine:3.21 AS runtime
# GID 10001 must match the group that owns the Agent socket.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 jothost \
    && adduser -D -u 10001 -G jothost jothost

COPY --from=builder /out/jothost-api /usr/local/bin/jothost-api

# Migrations are read from disk at startup rather than embedded, so the
# repository layout in ARCHITECTURE.md section 3 stays the single source.
COPY migrations/ /app/migrations/

# The API is unprivileged: it never performs root operations itself
# (PRD.md section 10).
USER jothost
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/jothost-api"]
