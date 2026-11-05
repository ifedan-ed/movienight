""""Finished it?": asked once at most per Play, and only once the film could
have ended; a newer Play or an early hand-back from the player closes a Play
without a question; and it can be switched off."""
import datetime as dt

from app.core.db import session
from app.models import PlaySession
from tests.conftest import sync_library
from tests.test_shows_play_sync import link_jellyfin


async def _ready(c, up):
    await sync_library()
    await link_jellyfin(c, up)


async def _play(c, tmdb=603):
    return (await c.post(f"/api/v1/title/movie/{tmdb}/play", json={"player": "infuse"})).json()["session_id"]


async def _age(sid, minutes):
    async with session() as db:
        ps = await db.get(PlaySession, sid)
        ps.started_at = ps.started_at - dt.timedelta(minutes=minutes)
        await db.commit()


async def _row(sid):
    async with session() as db:
        return await db.get(PlaySession, sid)


async def test_back_from_the_player_early_closes_without_asking(alice, up):
    await _ready(alice, up)
    sid = await _play(alice)
    await _age(sid, 5)
    back = (await alice.post(f"/api/v1/play/{sid}/back")).json()
    assert back["ask"] is False and back["session"]["id"] == sid
    row = await _row(sid)
    assert row.outcome == "not_finished" and row.asked_at is None
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None


async def test_asked_once_when_the_film_could_have_ended(alice, up):
    await _ready(alice, up)
    sid = await _play(alice)
    await _age(sid, 180)
    assert (await alice.post(f"/api/v1/play/{sid}/back")).json()["ask"] is True
    assert (await alice.post(f"/api/v1/play/{sid}/back")).json()["ask"] is False
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None
    assert (await alice.post(f"/api/v1/play/{sid}/finish", json={"outcome": "watched"})).status_code == 200
    assert (await _row(sid)).outcome == "watched"


async def test_coming_back_waits_for_the_film_and_closes_nothing(alice, up):
    await _ready(alice, up)
    sid = await _play(alice)
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None  # the player is still opening
    await _age(sid, 20)
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None  # too soon for a 100-minute film
    assert (await _row(sid)).finished_at is None  # a glance back mid-film closes nothing
    await _age(sid, 180)
    assert (await alice.get("/api/v1/play/open")).json()["session"]["id"] == sid
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None  # once


async def test_a_new_play_closes_the_one_before(alice, up):
    await _ready(alice, up)
    first = await _play(alice)
    await _age(first, 180)
    await _play(alice)
    row = await _row(first)
    assert row.finished_at is not None and row.outcome == "not_finished" and row.asked_at is None
    assert (await alice.post(f"/api/v1/play/{first}/back")).json()["ask"] is False


async def test_a_player_that_never_opened_is_never_asked_about(alice, up):
    # The phone app closes the Play it started when the phone has no such player.
    await _ready(alice, up)
    sid = await _play(alice)
    assert (await alice.post(f"/api/v1/play/{sid}/finish", json={"outcome": "dismissed"})).status_code == 200
    await _age(sid, 180)
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None
    assert (await alice.get("/api/v1/history")).json()["items"] == []


async def test_switched_off(alice, up):
    await _ready(alice, up)
    assert (await alice.get("/api/v1/me/players")).json()["ask_finished"] is True
    assert (await alice.put("/api/v1/me/players", json={"ask_finished": False})).json()["ask_finished"] is False
    sid = await _play(alice)
    await _age(sid, 180)
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None
    assert (await _row(sid)).outcome == "dismissed"
    assert (await alice.get("/api/v1/me/players")).json()["ask_finished"] is False


async def test_swiftfin_opens_the_title_in_swiftfin_and_nobody_is_asked(alice, up):
    await _ready(alice, up)
    r = (await alice.post("/api/v1/title/movie/603/play", json={"player": "swiftfin"})).json()
    server, user, kind, item = r["url"].removeprefix("swiftfin://").split("/")
    assert server == "5e1f" + "0" * 28 and kind == "item" and len(user) == 32 and "-" not in item
    await _age(r["session_id"], 300)
    assert (await alice.get("/api/v1/play/open")).json()["session"] is None  # Jellyfin records Swiftfin's plays itself


async def test_streamyfin_opens_the_title_in_streamyfin(alice, up):
    await _ready(alice, up)
    r = (await alice.post("/api/v1/title/movie/603/play", json={"player": "streamyfin"})).json()
    assert r["url"].startswith("streamyfin://items/page?id=") and len(r["url"].rsplit("=", 1)[1]) == 32
