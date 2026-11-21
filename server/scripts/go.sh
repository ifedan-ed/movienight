#!/usr/bin/env bash
# Runs the go tool inside the pinned Go image (golang:1.27.3-trixie) with
# ffmpeg added (scripts/dev.Dockerfile, built here once and kept), with the
# host's module cache mounted so modules download once. Uses the host network
# so tests can reach the throwaway Postgres and Valkey that test.sh starts on
# 127.0.0.1. The whole repository is mounted (read by the tests: web/ is the
# website the server serves, docs/openapi.json the description to match).
# Examples:
#   scripts/go.sh build ./...
#   scripts/go.sh vet ./...
set -euo pipefail
cd "$(dirname "$0")/.."
IMAGE="movienight-go-dev:1.27.3-ffmpeg7.1.5"
if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  docker build -q -t "$IMAGE" -f scripts/dev.Dockerfile scripts >/dev/null
fi
exec docker run --rm --network host \
  -v /root/go/pkg/mod:/go/pkg/mod \
  -v /root/.cache/movienight-go-build:/root/.cache/go-build \
  -v "$(cd .. && pwd)":/repo -w /repo/server \
  -e GOFLAGS="${GOFLAGS:-}" -e GOMAXPROCS="${GOMAXPROCS:-4}" -e TEST_DATABASE_URL -e TEST_VALKEY_URL \
  "$IMAGE" go "$@"
