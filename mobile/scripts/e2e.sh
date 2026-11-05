#!/usr/bin/env bash
# Run a test workflow on EAS with the screenshot user's device token (from
# scripts/test-session.sh, kept in a private file): it is stored as the EAS
# secret MAESTRO_E2E_ACCESS (development environment; masked in logs), the
# workflow is started and waited for. End the token afterwards.
#   T=$(mktemp); scripts/test-session.sh > "$T"
#   scripts/e2e.sh "$T" .eas/workflows/store-screenshots.yml
#   scripts/test-session.sh --end; rm "$T"
# Needs EXPO_TOKEN.
set -euo pipefail
cd "$(dirname "$0")/.."
FILE="${1:?token file}"
mask() { sed -E 's/[A-Za-z0-9_-]{40,}/<token>/g'; }
eas() { npx -y eas-cli@latest "$@" 2>&1 | mask; }
eas env:create --name MAESTRO_E2E_ACCESS --value "$(cat "$FILE")" --environment development --visibility secret --scope project --force --non-interactive >/dev/null
eas workflow:run "${2:-.eas/workflows/store-screenshots.yml}" "${@:3}" --non-interactive --wait
