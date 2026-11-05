#!/usr/bin/env bash
# Browser tests (WebKit = iPhone, Chromium = Android) against a throwaway
# Movie Night with fake upstreams. Needs Docker and Playwright with browsers.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME="movienight-e2e-pg-$$"
PG_PORT="${E2E_PG_PORT:-55435}"
export E2E_PORT="${E2E_PORT:-8139}"
docker run -d --rm --name "$NAME" --tmpfs /var/lib/postgresql/data:rw -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD=test -e POSTGRES_DB=movienight \
  -p "127.0.0.1:${PG_PORT}:5432" pgvector/pgvector:pg17 >/dev/null
SERVER_PID=""
cleanup() { [ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true; docker rm -f -v "$NAME" >/dev/null 2>&1 || true; }
trap cleanup EXIT
for _ in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done
TEST_DATABASE_URL="postgresql+psycopg://movienight:test@127.0.0.1:${PG_PORT}/movienight" \
  "${PYTHON:-.venv/bin/python}" tests/e2e_server.py &
SERVER_PID=$!
for _ in $(seq 1 60); do curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null && break; sleep 1; done
# Playwright from e2e/node_modules (cd e2e && npm install && npx playwright
# install webkit chromium), or a copy installed elsewhere (PLAYWRIGHT_DIR).
# The last fallback is another project's copy on this server (quiz/e2e).
export NODE_PATH="${NODE_PATH:-$(pwd)/e2e/node_modules:${PLAYWRIGHT_DIR:-/home/youruser/docker/quiz/e2e/node_modules}}"
node e2e/flows.mjs "http://127.0.0.1:${E2E_PORT}"
