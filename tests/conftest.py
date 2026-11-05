"""Test setup: a throwaway Postgres (scripts/test.sh starts one), the real
migrations, fake upstreams (tests/fakes.py), and API clients that act as a
signed-in person the way the sign-in proxy would."""
from __future__ import annotations

import os

# Settings are read at import, so the environment is fixed first; the
# install's own .env is never read.
os.environ["MOVIENIGHT_NO_ENV_FILE"] = "1"
os.environ["DATABASE_URL"] = os.environ.get("TEST_DATABASE_URL", "postgresql+psycopg://movienight:test@127.0.0.1:55434/movienight")
os.environ.update({
    "RUN_JOBS": "false", "SECRET_KEY": "test-secret", "DRY_RUN": "true",
    "OWNER_USERNAME": "owner", "USER_ALIASES": "owner-alt=owner", "JELLYFIN_USER_ID": "c" * 32,
    "JELLYFIN_WEBHOOK_SECRET": "hook-secret", "MDBLIST_API_KEY": "test", "TMDB_API_KEY": "test",
    "SEERR_BASE_URL": "http://seerr.test", "SEERR_API_KEY": "test", "JELLYFIN_API_KEY": "test",
    "PUBLIC_URL": "https://mn.test", "LITELLM_API_KEY": "", "SMTP_HOST": "smtp.test",
    # The forward-auth proxy path is gone: tests sign in as the dev user
    # (AUTH_DEV_USER) instead of the proxy's header, swapping it per fixture.
    "AUTH_DEV_ALLOWED": "true",
    "AUTH_DEV_USER": "",
})

from types import SimpleNamespace  # noqa: E402

import httpx  # noqa: E402
import pytest  # noqa: E402
from sqlalchemy import text  # noqa: E402

from app import clients  # noqa: E402
from app.api.v1 import scrobble  # noqa: E402
from app.core import cache  # noqa: E402
from app.core.config import settings  # noqa: E402
from app.core.db import engine, run_migrations  # noqa: E402
from app.main import app  # noqa: E402
from app.services import spelling  # noqa: E402
from tests import fakes  # noqa: E402

TABLES = ["users", "user_settings", "app_settings", "app_tokens", "app_codes", "pair_codes", "titles", "title_extras", "imdb_ratings", "library_items", "lists", "list_items",
          "list_favorites", "watchlist", "user_ratings", "favorites", "episode_favorites", "episode_ratings", "history", "requests", "ai_row_cache", "digests",
          "play_sessions", "watch_progress"]


@pytest.fixture(scope="session", autouse=True)
def schema():
    run_migrations(os.environ["DATABASE_URL"])


@pytest.fixture(autouse=True)
async def up():
    async with engine.begin() as conn:
        await conn.execute(text("TRUNCATE " + ", ".join(TABLES) + " RESTART IDENTITY CASCADE"))
    cache._store.clear()
    cache._locks.clear()
    scrobble._pair_starts.clear()
    spelling._cache.update(at=0.0, vocab={})
    import importlib
    mdblist_module = importlib.import_module("app.clients.mdblist")
    mdblist_module._down_until, mdblist_module._refused = 0.0, None
    from app.services import rows
    rows._ai_failures.clear()
    rows._ai_tasks.clear()
    f = SimpleNamespace(tmdb=fakes.FakeTMDB(), jellyfin=fakes.FakeJellyfin(), seerr=fakes.FakeSeerr(),
                        mdblist=fakes.FakeMDBList(), meili=fakes.FakeMeili(), litellm=fakes.FakeAI(), mailer=fakes.FakeMailer())
    clients.set_overrides(**vars(f))
    yield f
    clients.set_overrides()
    await engine.dispose()


def client(user: str | None = None, csrf: bool = True, peer: str = "127.0.0.1", **headers: str) -> httpx.AsyncClient:
    """A caller as `user` (the dev sign-in, AUTH_DEV_ALLOWED in tests: the
    forward-auth proxy path is gone) or anonymous."""
    h = dict(headers)
    if user:
        h.setdefault("X-Dev-User", user)
    if csrf:
        h["X-Requested-With"] = "movienight"
    return httpx.AsyncClient(transport=httpx.ASGITransport(app=app, client=(peer, 40000)), base_url="http://test", headers=h)


@pytest.fixture
async def alice():
    async with client("alice") as c:
        yield c


@pytest.fixture
async def bob():
    async with client("bob") as c:
        yield c


@pytest.fixture
async def owner():
    async with client("owner") as c:
        yield c


async def sync_library():
    """Fill library_items from the fake Jellyfin (movie 603, show 1399)."""
    from app.core.db import session
    from app.services import sync

    async with session() as db:
        await sync.sync_library(db, clients.jellyfin(), None, "c" * 32)
