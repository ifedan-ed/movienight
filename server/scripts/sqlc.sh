#!/usr/bin/env bash
# Regenerates internal/store from db/queries (and the schema in db/schema.sql
# plus db/migrations) with sqlc 1.27, run from its official image.
set -euo pipefail
cd "$(dirname "$0")/.."
exec docker run --rm -v "$PWD":/src -w /src sqlc/sqlc:1.27.0 generate
