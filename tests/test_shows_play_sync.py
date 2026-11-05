"""Seasons and episode ticks, playing in another player, Jellyfin plays,
scrobbling and the Jellyfin link."""
import datetime as dt

from sqlalchemy import select

from app.core.db import session
from app.models import HistoryEntry, PlaySession, User
from app.services import sync
from tests.conftest import client, sync_library


async def link_jellyfin(c, f):
    f.jellyfin.qc_ready = True
    qc = (await c.post("/api/v1/me/jellyfin/quick-connect")).json()
    assert qc["code"] == "123456"
    return (await c.post("/api/v1/me/jellyfin/quick-connect/check", json={"secret": qc["secret"]})).json()


async def test_episode_ticks_and_next_episode(alice):
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 1, "episode": 1}, {"season": 1, "episode": 2}]})
    s1 = (await alice.get("/api/v1/title/tv/1399/season/1")).json()
    assert [e["watched"] for e in s1["episodes"][:3]] == [1, 1, 0]
    nxt = (await alice.get("/api/v1/title/tv/1399/next")).json()["next"]
    assert (nxt["season"], nxt["episode"], nxt["label"]) == (1, 3, "Continue S01E03")
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 1, "episode": 1}], "watched": False})
    s1 = (await alice.get("/api/v1/title/tv/1399/season/1")).json()
    assert [e["watched"] for e in s1["episodes"][:2]] == [0, 1]
    seasons = (await alice.get("/api/v1/title/tv/1399")).json()["seasons"]
    assert seasons[0]["number"] == 1 and seasons[0]["watched"] == 1


async def test_ticks_mark_jellyfin_when_linked(alice, up):
    await sync_library()
    await link_jellyfin(alice, up)
    await alice.post("/api/v1/title/tv/1399/episodes", json={"episodes": [{"season": 2, "episode": 4}]})
    import asyncio
    await asyncio.sleep(0.05)  # the Jellyfin mark runs in the background
    assert ("played", "f" * 32, "ep204".ljust(32, "0")) in up.jellyfin.marked


async def test_link_jellyfin_once_per_person(alice, bob, up):
    r = await link_jellyfin(alice, up)
    assert r == {"ok": True, "linked": True, "user_name": "alice-jf"}
    j = (await alice.get("/api/v1/me/jellyfin")).json()
    assert j["linked"] and j["can_play"]
    r = await bob.post("/api/v1/me/jellyfin/quick-connect/check", json={"secret": "secret-123456"})
    assert r.status_code == 409  # the same Jellyfin account can't be linked twice
    wrong = await bob.post("/api/v1/me/jellyfin/password", json={"username": "x", "password": "wrong"})
    assert wrong.status_code == 422 and "Wrong Jellyfin" in wrong.json()["detail"]  # a plain error, not a sign-out
    async with session() as db:
        token_enc = await db.scalar(select(User.jellyfin_token_enc).where(User.username == "alice"))
    assert token_enc and "user-token" not in token_enc  # stored encrypted


async def test_play_needs_a_link_then_builds_the_player_link(alice, up):
    await sync_library()
    info = (await alice.get("/api/v1/title/movie/603/play")).json()
    assert info["available"] and not info["linked"]
    assert (await alice.post("/api/v1/title/movie/603/play", json={"player": "vlc"})).status_code == 409
    await link_jellyfin(alice, up)
    info = (await alice.get("/api/v1/title/movie/603/play")).json()
    assert info["chosen_subtitle"] == 3  # the English file beats the embedded track
    out = (await alice.post("/api/v1/title/movie/603/play", json={"player": "vlc", "subtitle": 3})).json()
    assert out["url"].startswith("vlc-x-callback://x-callback-url/stream?url=https%3A%2F%2Fjf.example%2FVideos%2F")
    assert "Subtitles%2F3%2F0%2FStream.srt" in out["url"] and "x-success" not in out["url"]  # nothing pulls you out of the player
    # An older app still asking for the subtitle inside the video gets the
    # original file (which has a size the player can seek in) and the subtitle link.
    old = (await alice.post("/api/v1/title/movie/603/play", json={"player": "infuse", "subtitle": 2, "embed_subtitle": True})).json()
    assert "static%3Dtrue" in old["url"] and "subtitleMethod" not in old["url"] and "&sub=" in old["url"]
    # Starting the second Play closed the first without a question; the
    # second is asked about once the film could have ended.
    async with session() as db:
        first = await db.get(PlaySession, out["session_id"])
        assert first.finished_at is not None and first.outcome == "not_finished"
        later = await db.get(PlaySession, old["session_id"])
        later.started_at = later.started_at - dt.timedelta(hours=3)
        await db.commit()
    s = (await alice.get("/api/v1/play/open")).json()["session"]
    assert s is not None and s["id"] == old["session_id"]
    await alice.post(f"/api/v1/play/{old['session_id']}/finish", json={"outcome": "watched"})
    hist = (await alice.get("/api/v1/history")).json()
    assert hist["total"] == 1 and hist["items"][0]["sources"] == ["player"]
    assert ("played", "f" * 32, "a" * 32) in up.jellyfin.marked


async def test_players_settings(alice):
    bad = await alice.put("/api/v1/me/players", json={"custom": [{"name": "Evil", "template": "javascript:alert(1)//{url}"}]})
    assert bad.status_code == 422
    ok = (await alice.put("/api/v1/me/players", json={"custom": [{"name": "nPlayer", "template": "nplayer-{url_raw}"}],
                                                        "enabled": ["vlc", "custom1"], "default_player": "custom1"})).json()
    assert [p["id"] for p in ok["players"] if p["enabled"]] == ["vlc", "custom1"] and ok["default_player"] == "custom1"


async def test_jellyfin_plays_fold_into_a_manual_mark(alice, up):
    await sync_library()
    await link_jellyfin(alice, up)
    w = (await alice.post("/api/v1/title/movie/603/watched", json={})).json()
    when = dt.datetime.now(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.0000000Z")
    up.jellyfin.played_items = [{"Id": "a" * 32, "Type": "Movie", "Name": "Movie 603", "ProviderIds": {"Tmdb": "603"},
                                 "UserData": {"LastPlayedDate": when}}]
    async with session() as db:
        user = await db.scalar(select(User).where(User.username == "alice"))
        assert await sync.sync_plays(db, up.jellyfin, user) == 0  # claimed the manual row, didn't add one
        rows = list(await db.scalars(select(HistoryEntry).where(HistoryEntry.user_id == user.id)))
    assert len(rows) == 1 and rows[0].id == w["id"] and rows[0].source == "jellyfin"


async def test_scrobble_token_and_webhook(alice):
    tok = (await alice.post("/api/v1/me/scrobble-token")).json()["token"]
    async with client(csrf=False, Authorization=f"Bearer {tok}") as kodi:
        r = (await kodi.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603, "source": "kodi", "ref": "p1"})).json()
        assert r["recorded"] is True and r["detail"] == "Movie 603"
        assert (await kodi.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603, "source": "kodi", "ref": "p1"})).json()["recorded"] is False
    async with client(csrf=False, Authorization="Bearer nope") as bad:
        assert (await bad.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603})).status_code == 401
    async with client(csrf=False, **{"X-MovieNight-Token": "hook-secret"}) as hook:
        assert (await hook.post("/api/v1/scrobble/jellyfin", content=b'{"NotificationType":"PlaybackStop","UserId":"x"}')).status_code == 202
    async with client(csrf=False, **{"X-MovieNight-Token": "wrong"}) as hook:
        assert (await hook.post("/api/v1/scrobble/jellyfin", content=b"{}")).status_code == 401
