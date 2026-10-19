#!/usr/bin/env bash
# Runs the stack locally: gateway (127.0.0.1:8081) + trader (127.0.0.1:8080),
# and the hedge app (127.0.0.1:8082) when a PostgreSQL is configured.
#
#   scripts/dev.sh            build web + binaries, run until Ctrl-C
#   HTTPS_PROXY=http://127.0.0.1:7892 scripts/dev.sh   (venues behind a proxy)
#
# Hedge app: point it at any PostgreSQL once, as a superuser; it creates its
# own role and database and stores its URL in data/hedger/database.url:
#   POSTGRES_ADMIN_URL=postgres://postgres:pw@127.0.0.1:5432/postgres?sslmode=disable scripts/dev.sh
# Later runs reuse data/hedger/database.url. Without either, the hedge app is
# skipped and the trader runs alone.
#
# State lives in ./data (gitignored): gateway token, master key, encrypted
# accounts, trader password hash, hedger token and database URL. The trader
# password is printed once on the first run; change it with:
#   go run ./cmd/trader passwd
set -euo pipefail
cd "$(dirname "$0")/.."

mkdir -p data bin
(cd web && { [ -d node_modules ] || npm ci --no-audit --no-fund; } && npm run build)
go build -o bin/gateway ./cmd/gateway
go build -o bin/trader ./cmd/trader
go build -o bin/hedger ./cmd/hedger

PIDS=()
trap 'kill "${PIDS[@]}" 2>/dev/null || true' EXIT

GATEWAY_DATA_DIR=data/gateway bin/gateway &
PIDS+=($!)

HEDGER=()
if [ -n "${POSTGRES_ADMIN_URL:-}" ] && [ ! -s data/hedger/database.url ]; then
  HEDGER_DATA_DIR=data/hedger bin/hedger initdb
fi
if [ -n "${HEDGER_DATABASE_URL:-}" ] || [ -s data/hedger/database.url ]; then
  HEDGER_DATA_DIR=data/hedger GATEWAY_TOKEN_FILE=data/gateway/token bin/hedger &
  PIDS+=($!)
  HEDGER=(HEDGER_URL=http://127.0.0.1:8082 HEDGER_TOKEN_FILE=data/hedger/token)
else
  echo "Hedge app disabled: set POSTGRES_ADMIN_URL once to enable it (see scripts/dev.sh)." >&2
fi

env TRADER_DATA_DIR=data/trader TRADER_WEB_DIR=web/dist GATEWAY_TOKEN_FILE=data/gateway/token "${HEDGER[@]}" bin/trader
