"""Rating one episode of a show: the stars next to each episode's heart on a
season page, on the website and in the phone app. Same scale as a title's
rating (1 to 10, two points a star); each person's are their own, and an
episode's rating is not the show's."""
from sqlalchemy import func, select

from app import models
from app.core.db import session

EP = "/api/v1/title/tv/1399/episode"


def ratings(season_page):
    return {e["number"]: e["rating"] for e in season_page["episodes"] if e["rating"] is not None}


async def test_an_episode_can_be_rated_rerated_and_cleared(alice):
    r = await alice.put(f"{EP}/1/3/rating", json={"rating": 8})
    assert r.status_code == 200, r.text
    assert r.json() == {"ok": True, "rating": 8}
    # POST does the same as PUT (the title's own rating is a POST).
    assert (await alice.post(f"{EP}/1/3/rating", json={"rating": 7})).json() == {"ok": True, "rating": 7}
    await alice.put(f"{EP}/2/1/rating", json={"rating": 10})
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == {3: 7}
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/2")).json()) == {1: 10}
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(models.EpisodeRating)) == 2  # re-rating is one row
    r = await alice.delete(f"{EP}/1/3/rating")
    assert r.status_code == 200 and r.json() == {"ok": True, "rating": None}
    assert (await alice.delete(f"{EP}/1/3/rating")).json()["rating"] is None  # already gone: no error
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == {}
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/2")).json()) == {1: 10}


async def test_ratings_and_removals_at_once_never_fail(alice):
    """Two devices, or a phone's outbox replaying, may send PUT, DELETE and
    PUT for one episode at the same moment: each is one statement, so none
    is refused (a 409 would make the phone drop the change) or fails (500),
    and at most one row is left."""
    import asyncio
    calls = []
    for i in range(25):
        if i % 3 == 1:
            calls.append(alice.delete(f"{EP}/1/5/rating"))
        else:
            calls.append(alice.put(f"{EP}/1/5/rating", json={"rating": 1 + i % 10}))
    rs = await asyncio.gather(*calls)
    assert [r.status_code for r in rs] == [200] * 25, [r.text for r in rs if r.status_code != 200]
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(models.EpisodeRating)) <= 1
    # And a last PUT afterwards is what stays.
    await alice.put(f"{EP}/1/5/rating", json={"rating": 6})
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == {5: 6}


async def test_hearts_and_unhearts_at_once_never_fail(alice):
    import asyncio
    calls = [alice.delete(f"{EP}/1/5/favorite") if i % 3 == 1 else alice.post(f"{EP}/1/5/favorite") for i in range(25)]
    rs = await asyncio.gather(*calls)
    assert [r.status_code for r in rs] == [200] * 25, [r.text for r in rs if r.status_code != 200]
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(models.EpisodeFavorite)) <= 1


async def test_the_scale_is_a_titles(alice):
    for bad in (0, 11, -1, 4.5, "x", None):
        assert (await alice.put(f"{EP}/1/1/rating", json={"rating": bad})).status_code == 422, bad
    assert (await alice.put(f"{EP}/1/1/rating", json={})).status_code == 422
    for ok in (1, 10):
        assert (await alice.put(f"{EP}/1/1/rating", json={"rating": ok})).json()["rating"] == ok


async def test_episode_ratings_are_per_person_and_not_the_shows(alice, bob):
    await alice.put(f"{EP}/1/1/rating", json={"rating": 9})
    assert ratings((await bob.get("/api/v1/title/tv/1399/season/1")).json()) == {}
    # The show itself is not rated by rating one of its episodes, and the
    # Ratings count (Profile) and Your ratings stay the titles'.
    assert (await alice.get("/api/v1/title/tv/1399/status")).json()["rating"] is None
    assert (await alice.get("/api/v1/me")).json()["stats"]["ratings"] == 0
    rated = (await alice.get("/api/v1/collection/ratings")).json()
    assert rated["count"] == 0 and rated["items"] == []
    # Others don't see it with the title's ratings either.
    community = (await bob.get("/api/v1/title/tv/1399")).json()["community"]
    assert community["count"] == 0 and community["others"] == [] and community["average"] is None


async def test_rating_the_show_leaves_its_episodes_alone(alice):
    await alice.put(f"{EP}/1/2/rating", json={"rating": 4})
    await alice.post("/api/v1/title/tv/1399/rating", json={"rating": 9})
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == {2: 4}
    await alice.delete("/api/v1/title/tv/1399/rating")
    assert ratings((await alice.get("/api/v1/title/tv/1399/season/1")).json()) == {2: 4}


async def test_only_shows_have_episodes_to_rate(alice):
    assert (await alice.put("/api/v1/title/movie/603/episode/1/1/rating", json={"rating": 5})).status_code == 404
    assert (await alice.delete("/api/v1/title/movie/603/episode/1/1/rating")).status_code == 404
    assert (await alice.put(f"{EP}/-1/1/rating", json={"rating": 5})).status_code == 422
    assert (await alice.put(f"{EP}/1/2147483648/rating", json={"rating": 5})).status_code == 422
    # A season numbered by its year is fine.
    assert (await alice.put(f"{EP}/2023/1/rating", json={"rating": 6})).json()["rating"] == 6


async def test_episode_ratings_are_in_the_export_and_go_with_the_account(alice):
    await alice.put(f"{EP}/1/2/rating", json={"rating": 6})
    data = (await alice.get("/api/v1/export")).json()
    assert [{k: e[k] for k in ("tmdb_id", "season", "episode", "rating")} for e in data["episode_ratings"]] == [
        {"tmdb_id": 1399, "season": 1, "episode": 2, "rating": 6}]
    assert data["episode_ratings"][0]["rated_at"] and data["episode_ratings"][0]["source"] == "app"
    async with session() as db:
        await db.delete(await db.scalar(select(models.User).where(models.User.username == "alice")))
        await db.commit()
        assert await db.scalar(select(func.count()).select_from(models.EpisodeRating)) == 0


def test_migration_0014_goes_down_and_up():
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
        command.downgrade(cfg, "0013")
        names = inspect(eng).get_table_names()
        assert "episode_ratings" not in names and "episode_favorites" in names
        command.upgrade(cfg, "head")
        assert "episode_ratings" in inspect(eng).get_table_names()
    finally:
        eng.dispose()


async def test_recently_watched_and_history_carry_the_episodes_own_heart_and_rating(alice):
    """Recently watched (Profile) and History show an episode with its own
    heart and rating, never the show's."""
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 1, "episode": 1}, {"season": 1, "episode": 2}]})
    await alice.post("/api/v1/title/tv/1399/rating", json={"rating": 3})  # the show's own rating
    await alice.post("/api/v1/title/tv/1399/favorite")  # and its heart
    await alice.put(f"{EP}/1/2/rating", json={"rating": 9})
    await alice.post(f"{EP}/1/2/favorite")
    items = (await alice.get("/api/v1/history?page=1")).json()["items"]
    own = {x["episode"]: (x["episode_favorite"], x["episode_rating"]) for x in items if x["media_type"] == "episode"}
    assert own == {1: (False, None), 2: (True, 9)}
    # The card is still the show's (its poster, its key), for the way back.
    assert all(x["card"]["key"] == "tv:1399" for x in items)


async def test_up_next_carries_the_episodes_own_heart_and_rating(alice):
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 1, "episode": 1}]})
    await alice.post("/api/v1/title/tv/1399/rating", json={"rating": 3})
    nxt = (await alice.get("/api/v1/title/tv/1399/next")).json()["next"]
    assert (nxt["season"], nxt["episode"], nxt["favorite"], nxt["rating"]) == (1, 2, False, None)
    await alice.put(f"{EP}/1/2/rating", json={"rating": 7})
    await alice.post(f"{EP}/1/2/favorite")
    nxt = (await alice.get("/api/v1/title/tv/1399/next")).json()["next"]
    assert (nxt["season"], nxt["episode"], nxt["favorite"], nxt["rating"]) == (1, 2, True, 7)


async def test_someone_elses_heart_and_rating_are_never_shown_as_yours(alice, bob):
    """Bob likes and rates the very episodes Alice watched and has up next:
    her History, Up next and season page still show none of it."""
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 1, "episode": 1}]})
    for n in (1, 2):
        await bob.put(f"{EP}/1/{n}/rating", json={"rating": 9})
        await bob.post(f"{EP}/1/{n}/favorite")
    items = (await alice.get("/api/v1/history?page=1")).json()["items"]
    assert [(x["episode"], x["episode_favorite"], x["episode_rating"]) for x in items] == [(1, False, None)]
    nxt = (await alice.get("/api/v1/title/tv/1399/next")).json()["next"]
    assert (nxt["episode"], nxt["favorite"], nxt["rating"]) == (2, False, None)
    page = (await alice.get("/api/v1/title/tv/1399/season/1")).json()
    assert ratings(page) == {} and not any(e["favorite"] for e in page["episodes"])
    # Bob's own are still his.
    assert ratings((await bob.get("/api/v1/title/tv/1399/season/1")).json()) == {1: 9, 2: 9}
