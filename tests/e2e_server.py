"""A Movie Night for the browser tests: the real app on a throwaway database
with the fake upstreams from tests/fakes.py (no internet, no real Jellyfin).
Started by e2e/run.sh."""
from __future__ import annotations

import os
import sys

os.environ["MOVIENIGHT_NO_ENV_FILE"] = "1"
os.environ["DATABASE_URL"] = os.environ["TEST_DATABASE_URL"]
os.environ.update({
    "RUN_JOBS": "false", "SECRET_KEY": "e2e-secret", "DRY_RUN": "true", "OWNER_USERNAME": "owner",
    "JELLYFIN_USER_ID": "c" * 32, "JELLYFIN_WEBHOOK_SECRET": "hook-secret", "MDBLIST_API_KEY": "test",
    "TMDB_API_KEY": "test", "SEERR_BASE_URL": "http://seerr.test", "SEERR_API_KEY": "test", "JELLYFIN_API_KEY": "test",
    "PUBLIC_URL": "http://127.0.0.1:8139", "LITELLM_API_KEY": "", "SMTP_HOST": "",
})
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

import asyncio  # noqa: E402

import uvicorn  # noqa: E402

from app import clients  # noqa: E402
from app.core.db import run_migrations, session  # noqa: E402
from app.services import sync  # noqa: E402
from tests import fakes  # noqa: E402


def main() -> None:
    run_migrations(os.environ["DATABASE_URL"])
    jf = fakes.FakeJellyfin()
    jf.qc_ready = True  # Quick Connect is approved as soon as it's asked
    jf.single_account = False  # each browser is its own Jellyfin user
    clients.set_overrides(tmdb=fakes.FakeTMDB(), jellyfin=jf, seerr=fakes.FakeSeerr(), mdblist=fakes.FakeMDBList(),
                          meili=fakes.FakeMeili(), litellm=fakes.FakeAI(), mailer=fakes.FakeMailer())

    async def seed():
        async with session() as db:
            await sync.sync_library(db, jf, None, "c" * 32)

    asyncio.run(seed())
    from app.main import app

    uvicorn.run(app, host="127.0.0.1", port=int(os.environ.get("E2E_PORT", "8139")), log_level="warning")


if __name__ == "__main__":
    main()
