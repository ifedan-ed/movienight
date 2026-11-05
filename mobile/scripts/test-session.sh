#!/usr/bin/env bash
# A device token for the App Store screenshot user ("store-screenshots"; never
# a real person's account), minted inside the running server the way the
# app's sign-in makes one. Nothing secret is printed except the token itself,
# to stdout, for the caller to keep in a private file.
#   TOKEN=$(scripts/test-session.sh)
#   scripts/test-session.sh --end      # sign the screenshot user's devices out
set -euo pipefail
if [ "${1:-}" = "--end" ]; then
  docker exec -i movienight-web-1 python - <<'PY'
import asyncio
from sqlalchemy import delete, select
from app.core.db import SessionLocal
from app.models import AppToken, User
async def main():
    async with SessionLocal() as db:
        user = await db.scalar(select(User).where(User.username == "store-screenshots"))
        if user:
            await db.execute(delete(AppToken).where(AppToken.user_id == user.id))
            await db.commit()
asyncio.run(main())
PY
  exit 0
fi
docker exec -i movienight-web-1 python - <<'PY'
import asyncio
from app.core import crypto
from app.core.db import SessionLocal
from app.models import AppToken
from app.services import devices, users
async def main():
    async with SessionLocal() as db:
        user = await users.get_or_create(db, "store-screenshots", None, "Sam Rivera")
        # The same form as a real sign-in's token (the proxy only lets
        # "mn_app_" tokens past the sign-in gate).
        token = devices.TOKEN_PREFIX + crypto.new_token(32)
        db.add(AppToken(user_id=user.id, name="App Store screenshots", token_hash=crypto.token_hash(token)))
        await db.commit()
        print(token)
asyncio.run(main())
PY
