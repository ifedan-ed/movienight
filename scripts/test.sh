#!/usr/bin/env bash
# Run the test suite against a throwaway Postgres container (removed after).
#   scripts/test.sh            all tests
#   scripts/test.sh -k lists   pytest arguments pass through
set -euo pipefail
cd "$(dirname "$0")/.."
NAME="movienight-test-pg-$$"
# The database lives in memory (--tmpfs) and `rm -v` removes it with any
# storage: a stopped test database must never leave a volume behind.
PORT="${TEST_PG_PORT:-55434}"
docker run -d --rm --name "$NAME" --tmpfs /var/lib/postgresql/data:rw -e POSTGRES_USER=movienight -e POSTGRES_PASSWORD=test -e POSTGRES_DB=movienight \
  -p "127.0.0.1:${PORT}:5432" pgvector/pgvector:pg17 >/dev/null
trap 'docker rm -f -v "$NAME" >/dev/null 2>&1 || true' EXIT
# The image starts a temporary server for its init scripts, then the real one.
for _ in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 1
done
TEST_DATABASE_URL="postgresql+psycopg://movienight:test@127.0.0.1:${PORT}/movienight" \
  "${PYTHON:-.venv/bin/python}" -m pytest -q "$@"
