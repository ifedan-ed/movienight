"""A throwaway Movie Night for testing the Jellyfin plugin: fake TMDB/Seerr,
real database, listening on the Docker bridge address only (MN_HOST) so the
test Jellyfin container can reach it and the internet can't."""
from __future__ import annotations

import os
import sys

REPO = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
os.environ["MOVIENIGHT_NO_ENV_FILE"] = "1"
os.environ["DATABASE_URL"] = os.environ["TEST_DATABASE_URL"]
os.environ.update({
    "RUN_JOBS": "false", "SECRET_KEY": "plugintest-secret", "DRY_RUN": "true", "OWNER_USERNAME": "owner",
    "JELLYFIN_USER_ID": "c" * 32, "JELLYFIN_WEBHOOK_SECRET": os.environ["HOOK_SECRET"], "MDBLIST_API_KEY": "",
    "TMDB_API_KEY": "test", "SEERR_BASE_URL": "http://seerr.test", "SEERR_API_KEY": "test", "JELLYFIN_API_KEY": "test",
    "PUBLIC_URL": "http://127.0.0.1:8141", "LITELLM_API_KEY": "", "SMTP_HOST": "",
})
sys.path.insert(0, REPO)
os.chdir(REPO)

import uvicorn  # noqa: E402

from app import clients  # noqa: E402
from app.core.db import run_migrations  # noqa: E402
from tests import fakes  # noqa: E402


def main() -> None:
    run_migrations(os.environ["DATABASE_URL"])
    clients.set_overrides(tmdb=fakes.FakeTMDB(), jellyfin=fakes.FakeJellyfin(), seerr=fakes.FakeSeerr(),
                          mdblist=fakes.FakeMDBList(), meili=fakes.FakeMeili(), litellm=fakes.FakeAI(), mailer=fakes.FakeMailer())
    from app.main import app

    # Every plugin event as it arrives, for the test log.
    async def log_events(scope, receive, send):
        if scope["type"] == "http" and scope["path"].endswith("/scrobble/jellyfin/events"):
            chunks = []
            more = True
            while more:
                msg = await receive()
                chunks.append(msg.get("body", b""))
                more = msg.get("more_body", False)
            raw = b"".join(chunks)
            print("PLUGIN EVENT", raw.decode()[:900], flush=True)
            sent = False
            async def replay():
                nonlocal sent
                if sent:
                    return {"type": "http.disconnect"}
                sent = True
                return {"type": "http.request", "body": raw, "more_body": False}
            return await app(scope, replay, send)
        return await app(scope, receive, send)

    uvicorn.run(log_events, host=os.environ["MN_HOST"], port=8141, log_level="info")


if __name__ == "__main__":
    main()
