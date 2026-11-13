#!/usr/bin/env bash
# A device token for the App Store screenshot user ("store-screenshots"; never
# a real person's account), written straight into the install's database in
# the same form a real phone sign-in makes: "mn_app_" plus 32 random bytes
# (URL-safe base64, unpadded), stored only as its sha256 in app_tokens. Nothing
# secret is printed except the token itself, to stdout, for the caller to keep
# in a private file. Run from anywhere on the server; it uses the compose
# project's Postgres container.
#   TOKEN=$(scripts/test-session.sh)
#   scripts/test-session.sh --end      # sign the screenshot user's devices out
set -euo pipefail
cd "$(dirname "$0")/../.."
psql() { docker compose exec -T postgres psql -U movienight -d movienight -v ON_ERROR_STOP=1 -qAt "$@"; }
if [ "${1:-}" = "--end" ]; then
  psql -c "DELETE FROM app_tokens WHERE user_id = (SELECT id FROM users WHERE username = 'store-screenshots')" >/dev/null
  exit 0
fi
token="mn_app_$(head -c 32 /dev/urandom | base64 -w0 | tr '+/' '-_' | tr -d '=')"
hash=$(printf '%s' "$token" | sha256sum | cut -d' ' -f1)
psql >/dev/null <<SQL
INSERT INTO users (username, display_name, is_admin, created_at)
  VALUES ('store-screenshots', 'Sam Rivera', false, now())
  ON CONFLICT (username) DO NOTHING;
INSERT INTO app_tokens (user_id, name, token_hash, created_at, scope)
  SELECT id, 'App Store screenshots', '$hash', now(), 'app' FROM users WHERE username = 'store-screenshots';
SQL
echo "$token"
