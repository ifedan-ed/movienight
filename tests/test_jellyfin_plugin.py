"""The Movie Night plugin for Jellyfin: playback events keep each person's
watch progress (shown on the title page); played marks keep history, once,
whichever of the plugin and the plays sync sees a play first."""
from sqlalchemy import func, select

from app.core.db import session
from app.models import HistoryEntry, User, WatchProgress
from app.services import sync
from tests.conftest import client
from tests.test_shows_play_sync import link_jellyfin

MOVIE = {"id": "a1" * 16, "type": "Movie", "name": "Movie 603", "tmdb": "603", "runtime_s": 6000}
EPISODE = {"id": "e5" * 16, "type": "Episode", "name": "Episode 5", "series_id": "51" * 16, "series_name": "Show 1399",
           "season": 1, "episode": 5, "series_tmdb": "1399", "runtime_s": 3300}


async def jf_user(username="alice") -> str:
    async with session() as db:
        return await db.scalar(select(User.jellyfin_user_id).where(User.username == username))


async def send(event, item, user_id, token="hook-secret", **extra):
    async with client(csrf=False, **{"X-MovieNight-Token": token}) as hook:
        return await hook.post("/api/v1/scrobble/jellyfin/events", json={"event": event, "user_id": user_id, "item": item, **extra})


async def history_count(**where) -> int:
    async with session() as db:
        q = select(func.count()).select_from(HistoryEntry)
        for k, v in where.items():
            q = q.where(getattr(HistoryEntry, k) == v)
        return await db.scalar(q)


async def test_progress_from_playback_shows_on_the_title_page(alice, up):
    await link_jellyfin(alice, up)
    uid = await jf_user()
    assert (await send("start", MOVIE, uid, position_s=0, client="Kodi", device="Living room")).status_code == 202
    assert (await send("progress", MOVIE, uid, position_s=1500, client="Kodi")).status_code == 202
    r = (await alice.get("/api/v1/title/movie/603")).json()["resume"]
    assert r["position_s"] == 1500 and r["percent"] == 25 and r["state"] == "playing" and r["client"] == "Kodi"
    await send("pause", MOVIE, uid, position_s=1600)
    assert (await alice.get("/api/v1/title/movie/603")).json()["resume"]["state"] == "paused"
    await send("stop", MOVIE, uid, position_s=1620)
    r = (await alice.get("/api/v1/title/movie/603")).json()["resume"]
    assert r["state"] == "stopped" and r["position_s"] == 1620
    # Only the person who played it sees it.
    async with client("bob") as bob:
        assert (await bob.get("/api/v1/title/movie/603")).json()["resume"] is None


async def test_a_finished_stop_clears_progress_and_played_records_the_watch_once(alice, up):
    await link_jellyfin(alice, up)
    uid = await jf_user()
    await send("progress", MOVIE, uid, position_s=3000)
    await send("stop", MOVIE, uid, position_s=5900)  # past 90 %: finished
    assert (await alice.get("/api/v1/title/movie/603")).json()["resume"] is None
    when = "2026-10-04T20:15:30.1234567Z"
    await send("played", MOVIE, uid, last_played=when)
    await send("played", MOVIE, uid, last_played=when)  # Jellyfin can report it twice
    assert await history_count(source="jellyfin", tmdb_id=603) == 1
    # The plays sync later reports the same play: still one watch.
    up.jellyfin.played_items = [{"Id": MOVIE["id"], "Type": "Movie", "Name": "Movie 603", "ProviderIds": {"Tmdb": "603"},
                                 "UserData": {"LastPlayedDate": when}}]
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        assert await sync.sync_plays(db, up.jellyfin, user) == 0
    assert await history_count(source="jellyfin", tmdb_id=603) == 1


async def test_episodes_by_the_shows_id_and_unplayed_keeps_history(alice, up):
    await link_jellyfin(alice, up)
    uid = await jf_user()
    await send("progress", EPISODE, uid, position_s=600, client="Swiftfin")
    r = (await alice.get("/api/v1/title/tv/1399")).json()["resume"]
    assert (r["season"], r["episode"], r["episode_title"], r["client"]) == (1, 5, "Episode 5", "Swiftfin")
    await send("played", EPISODE, uid, last_played="2026-10-04T21:00:00.0000000Z")
    assert await history_count(media_type="episode", tmdb_id=1399, season=1, episode=5) == 1
    assert (await alice.get("/api/v1/title/tv/1399")).json()["resume"] is None
    await send("progress", EPISODE, uid, position_s=60)
    await send("unplayed", EPISODE, uid)
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(WatchProgress)) == 0
    assert await history_count(media_type="episode", tmdb_id=1399, season=1, episode=5) == 1


async def test_a_watch_marked_here_is_claimed_not_doubled(alice, up):
    await link_jellyfin(alice, up)
    uid = await jf_user()
    await alice.post("/api/v1/title/movie/603/watched", json={"watched_at": "2026-10-04T20:00:00Z"})
    await send("played", MOVIE, uid, last_played="2026-10-04T20:40:00.0000000Z")
    assert await history_count(tmdb_id=603) == 1
    assert await history_count(tmdb_id=603, source="jellyfin") == 1


async def test_events_need_the_secret_and_a_known_person(alice, up):
    await link_jellyfin(alice, up)
    assert (await send("start", MOVIE, await jf_user(), token="wrong")).status_code == 401
    assert (await send("start", MOVIE, "0" * 32)).status_code == 202  # nobody here uses that Jellyfin account
    no_ids = {**MOVIE, "tmdb": None}
    up.tmdb.find_imdb = lambda imdb_id: None
    assert (await send("start", no_ids, await jf_user())).status_code == 202  # no TMDB id: nothing kept
    async with session() as db:
        assert await db.scalar(select(func.count()).select_from(WatchProgress)) == 0
