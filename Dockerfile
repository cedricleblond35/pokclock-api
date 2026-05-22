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

USER app
EXPOSE 8080

ENV PORT=8080 \
    LOG_LEVEL=info

HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD wget --quiet --tries=1 --spider http://127.0.0.1:8080/api/health || exit 1

ENTRYPOINT ["/app/api"]
