# syntax=docker/dockerfile:1.7

### Stage 1 — build ###########################################################
FROM golang:1.24-alpine AS builder
WORKDIR /src

# Cache des modules : on copie go.mod / go.sum d'abord pour profiter du cache
# Docker quand le code change sans toucher aux dépendances.
COPY go.mod go.sum ./
RUN go mod download

# Source
COPY . .

# Build SHA injecté pour /api/health et logs.
ARG BUILD_SHA=dev
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64

RUN go build \
    -trimpath \
    -ldflags="-s -w -X main.buildSHA=${BUILD_SHA}" \
    -o /out/api ./cmd/api

### Stage 2 — runtime #########################################################
FROM alpine:3.20 AS runtime
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S app -g 1001 \
    && adduser -S app -G app -u 1001

WORKDIR /app
COPY --from=builder --chown=app:app /out/api /app/api

# Entrypoint embedded inline so the image build never depends on a context file.
# Builds DATABASE_URL from PG_PASSWORD_FILE (Swarm secret path) at container
# start, because Docker env values are not run through a shell.
RUN cat > /app/entrypoint.sh <<'EOF' && chmod +x /app/entrypoint.sh && chown app:app /app/entrypoint.sh
#!/bin/sh
set -eu

if [ -n "${PG_PASSWORD_FILE:-}" ]; then
  if [ ! -r "$PG_PASSWORD_FILE" ]; then
    echo "entrypoint: PG_PASSWORD_FILE=$PG_PASSWORD_FILE not readable" >&2
    exit 1
  fi
  PG_PASSWORD=$(cat "$PG_PASSWORD_FILE")
  : "${PG_USER:=pokclock}"
  : "${PG_HOST:=postgres}"
  : "${PG_PORT:=5432}"
  : "${PG_DB:=pokclock}"
  : "${PG_SSLMODE:=disable}"
  export DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@${PG_HOST}:${PG_PORT}/${PG_DB}?sslmode=${PG_SSLMODE}"
fi

exec /app/api "$@"
EOF

USER app
EXPOSE 8080

ENV PORT=8080 \
    LOG_LEVEL=info

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/app/entrypoint.sh"]
