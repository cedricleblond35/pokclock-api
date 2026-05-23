#!/bin/sh
set -eu

# Builds DATABASE_URL from PG_PASSWORD_FILE (Swarm secret path) + PG_* pieces.
# Docker env values are NOT run through a shell, so substitutions like
# $(cat /run/secrets/...) would land in the container as literal text.
# This script does the read in shell, then exec's the Go binary.

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
