"""Liking (favouriting) one episode of a show: the heart on each episode of
a season page, on the website and in the phone app. Each person's are
their own, and an episode's heart is not the show's."""
from sqlalchemy import func, select

from app import models
from app.core.db import session

EP = "/api/v1/title/tv/1399/episode"


def hearts(season_page):
    return [e["number"] for e in season_page["episodes"] if e["favorite"]]


async def test_an_episode_can_be_liked_and_unliked(alice):
    r = await alice.post(f"{EP}/1/3/favorite")
    assert r.status_code == 200, r.text
    assert r.json() == {"ok": True, "favorite": True}
    assert (await alice.post(f"{EP}/1/3/favorite")).json()["favorite"] is True  # twice is still one
    await alice.post(f"{EP}/2/1/favorite")
    assert hearts((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == [3]
    assert hearts((await alice.get("/api/v1/title/tv/1399/season/2")).json()) == [1]
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(models.EpisodeFavorite)) == 2
    r = await alice.delete(f"{EP}/1/3/favorite")
    assert r.status_code == 200 and r.json() == {"ok": True, "favorite": False}
    assert (await alice.delete(f"{EP}/1/3/favorite")).json()["favorite"] is False  # already gone: no error
    assert hearts((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == []
    assert hearts((await alice.get("/api/v1/title/tv/1399/season/2")).json()) == [1]


async def test_episode_hearts_are_per_person_and_not_the_shows(alice, bob):
    await alice.post(f"{EP}/1/1/favorite")
    assert hearts((await bob.get("/api/v1/title/tv/1399/season/1")).json()) == []
    # The show itself is not favourited by liking one of its episodes.
    assert (await alice.get("/api/v1/title/tv/1399/status")).json()["favorite"] is False
    favs = (await alice.get("/api/v1/collection/favorites")).json()
    assert favs["count"] == 0 and favs["items"] == []


async def test_only_shows_have_episodes_to_like(alice):
    assert (await alice.post("/api/v1/title/movie/603/episode/1/1/favorite")).status_code == 404
    assert (await alice.post(f"{EP}/-1/1/favorite")).status_code == 422
    assert (await alice.post(f"{EP}/1/2147483648/favorite")).status_code == 422


async def test_a_season_numbered_by_its_year_can_be_liked(alice):
    assert (await alice.post(f"{EP}/2023/1/favorite")).json()["favorite"] is True


async def test_episode_hearts_are_in_the_export_and_go_with_the_account(alice):
    await alice.post(f"{EP}/1/2/favorite")
    data = (await alice.get("/api/v1/export")).json()
    assert [{k: e[k] for k in ("tmdb_id", "season", "episode")} for e in data["episode_favorites"]] == [
        {"tmdb_id": 1399, "season": 1, "episode": 2}]
    assert data["episode_favorites"][0]["added_at"]
    async with session() as db:
        await db.delete(await db.scalar(select(models.User).where(models.User.username == "alice")))
        await db.commit()
        assert await db.scalar(select(func.count()).select_from(models.EpisodeFavorite)) == 0


def test_migration_0013_goes_down_and_up():
    """The new table's migration has a working down path (and up again)."""
    import os

    from alembic import command
    from alembic.config import Config
    from sqlalchemy import create_engine, inspect

    from app.core.db import ROOT

    url = os.environ["DATABASE_URL"]
    cfg = Config(str(ROOT / "alembic.ini"))
    cfg.set_main_option("script_location", str(ROOT / "migrations"))
    cfg.attributes.update(skip_logging=True, url=url)
    eng = create_engine(url)
    try:
        command.downgrade(cfg, "0012")
        assert "episode_favorites" not in inspect(eng).get_table_names()
        command.upgrade(cfg, "head")
        assert "episode_favorites" in inspect(eng).get_table_names()
    finally:
        eng.dispose()
