#!/usr/bin/env bash
# Runs the Go tests against a throwaway Postgres (pgvector/pgvector:pg17, the
# production image) and a throwaway Valkey, both removed afterwards (their
# data lives in memory: --tmpfs, so nothing is left behind even on a crash).
#
#   scripts/test.sh                      every package
#   scripts/test.sh ./internal/web/...   go test arguments pass through
#
# Packages run one at a time (-p 1): they share the one database. CPU use is
# capped too (GOMAXPROCS=4), so a test run never starves the host.
set -euo pipefail
cd "$(dirname "$0")/.."
PG_PORT="${TEST_PG_PORT:-55440}"
KV_PORT="${TEST_VALKEY_PORT:-55441}"
PG="movienight-go-test-pg-$$"
KV="movienight-go-test-kv-$$"
cleanup() { docker rm -f -v "$PG" "$KV" >/dev/null 2>&1 || true; }
trap cleanup EXIT

docker run -d --rm --name "$PG" --tmpfs /var/lib/postgresql/data:rw \
  -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD=test -e POSTGRES_DB=movienight \
  -p "127.0.0.1:${PG_PORT}:5432" pgvector/pgvector:pg17 >/dev/null
docker run -d --rm --name "$KV" --tmpfs /data:rw -p "127.0.0.1:${KV_PORT}:6379" \
  valkey/valkey:9.1.2-alpine valkey-server --save "" --appendonly no >/dev/null

# The Postgres image starts a temporary server for its init scripts, then
# the real one: wait for the second "ready".
for _ in $(seq 1 90); do
  [ "$(docker logs "$PG" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done
for _ in $(seq 1 30); do
  docker exec "$KV" valkey-cli ping >/dev/null 2>&1 && break
  sleep 1
done

export TEST_DATABASE_URL="postgresql://movienight:test@127.0.0.1:${PG_PORT}/movienight"
export TEST_VALKEY_URL="redis://127.0.0.1:${KV_PORT}/0"
if [ "$#" -eq 0 ]; then set -- ./...; fi
GOFLAGS="${GOFLAGS:-}" scripts/go.sh test -p 1 -count=1 "$@"
