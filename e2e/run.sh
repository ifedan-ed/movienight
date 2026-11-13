#!/usr/bin/env bash
# Browser tests (WebKit = iPhone, Chromium = Android, Chromium desktop)
# against the Go server with fake upstreams: a throwaway Postgres and Valkey
# (as server/scripts/test.sh starts them, removed afterwards), and the app
# served by server/internal/server/e2e_serve_test.go (E2E_SERVE=1) inside the
# pinned Go image. Needs Docker and Playwright with its browsers.
#
#   e2e/run.sh                       ports 55435/55436 and 8139
#   E2E_PG_PORT=.. E2E_VALKEY_PORT=.. E2E_PORT=.. e2e/run.sh
set -euo pipefail
cd "$(dirname "$0")/.."
PG_PORT="${E2E_PG_PORT:-55435}"
KV_PORT="${E2E_VALKEY_PORT:-55436}"
export E2E_PORT="${E2E_PORT:-8139}"
PG="movienight-e2e-pg-$$"
KV="movienight-e2e-kv-$$"
APP="movienight-e2e-app-$$"
cleanup() {
  # The server runs in its own named container: killing the docker client
  # alone would leave it serving.
  docker rm -f -v "$PG" "$KV" "$APP" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run -d --rm --name "$PG" --tmpfs /var/lib/postgresql/data:rw \
  -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD=test -e POSTGRES_DB=movienight \
  -p "127.0.0.1:${PG_PORT}:5432" pgvector/pgvector:pg17 >/dev/null
docker run -d --rm --name "$KV" --tmpfs /data:rw -p "127.0.0.1:${KV_PORT}:6379" \
  valkey/valkey:9.1.2-alpine valkey-server --save "" --appendonly no >/dev/null
for _ in $(seq 1 90); do
  [ "$(docker logs "$PG" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done

# The same image and mounts as server/scripts/go.sh.
docker run -d --rm --name "$APP" --network host \
  -v /root/go/pkg/mod:/go/pkg/mod -v /root/.cache/movienight-go-build:/root/.cache/go-build \
  -v "$(pwd)":/repo -w /repo/server -e GOMAXPROCS=4 -e E2E_SERVE=1 -e E2E_PORT \
  -e TEST_DATABASE_URL="postgresql://movienight:test@127.0.0.1:${PG_PORT}/movienight" \
  -e TEST_VALKEY_URL="redis://127.0.0.1:${KV_PORT}/0" \
  golang:1.27.3-trixie go test -count=1 -run TestServeForBrowserTests -timeout 60m ./internal/server/ >/dev/null
# The first build can take a few minutes.
for _ in $(seq 1 600); do curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null && break; sleep 1; done
if ! curl -sf "http://127.0.0.1:${E2E_PORT}/healthz" >/dev/null; then
  docker logs "$APP" 2>&1 | tail -30
  echo "the e2e server did not start"
  exit 1
fi

# Playwright from e2e/node_modules (cd e2e && npm install && npx playwright
# install webkit chromium), or a copy installed elsewhere: set PLAYWRIGHT_DIR
# to that node_modules directory. Nothing is guessed.
export NODE_PATH="${NODE_PATH:-${PLAYWRIGHT_DIR:-$(pwd)/e2e/node_modules}}"
node e2e/flows.mjs "http://127.0.0.1:${E2E_PORT}"
