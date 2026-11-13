#!/usr/bin/env bash
# The Kodi add-on's own tests (test_addon.py), against stand-ins for Kodi's
# modules (kodi_stubs/). They need no server: they run in a throwaway Python
# image with pytest, so nothing is installed on the host.
#   kodi/tests/run.sh            every test
#   kodi/tests/run.sh -k queue   pytest arguments pass through
set -euo pipefail
cd "$(dirname "$0")/../.."
exec docker run --rm -v "$PWD/kodi":/kodi:ro -w /kodi -e PYTHONDONTWRITEBYTECODE=1 python:3.13-slim \
  sh -c 'pip install -q --disable-pip-version-check --root-user-action=ignore pytest==8.4.2 && python -m pytest -q -p no:cacheprovider tests "$@"' sh "$@"
