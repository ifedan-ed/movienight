"""Kodi signing in with a code shown on the TV and confirmed on the web; its
token reports plays and can do nothing else."""
import datetime as dt
import re

from sqlalchemy import update

from app.core.db import session
from app.models import PairCode
from tests.conftest import client


async def pair(alice, device="Kodi on SHIELD"):
    async with client(csrf=False) as tv:  # Kodi: no proxy header, no browser header
        start = await tv.post("/api/v1/scrobble/pair", json={"device": device})
        assert start.status_code == 200
        body = start.json()
        assert re.fullmatch(r"[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}", body["code"]) and body["expires_in"] == 600
        assert (await tv.post("/api/v1/scrobble/pair/poll", json={"poll": body["poll"]})).json() == {"status": "waiting", "token": None, "user": None, "account": None}
        confirm = await alice.post("/api/v1/me/devices/pair", json={"code": body["code"].lower().replace("-", " ")})
        assert confirm.status_code == 200 and confirm.json()["device"] == device
        got = (await tv.post("/api/v1/scrobble/pair/poll", json={"poll": body["poll"]})).json()
        assert got["status"] == "ok" and got["user"] == "alice" and got["token"].startswith("mn_kodi_")
        assert got["account"] == "alice"  # the username the add-on keeps its waiting watches under
        assert (await tv.post("/api/v1/scrobble/pair/poll", json={"poll": body["poll"]})).status_code == 410  # handed over once
        return got["token"], body["code"]


async def test_kodi_pairs_and_reports_a_watch(alice):
    token, _ = await pair(alice)
    async with client(csrf=False, Authorization=f"Bearer {token}") as tv:
        r = await tv.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603, "source": "kodi", "ref": "kodi-1"})
        assert r.json() == {"ok": True, "recorded": True, "detail": "Movie 603"}
        again = await tv.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603, "source": "kodi", "ref": "kodi-1"})
        assert again.json()["recorded"] is False
    hist = (await alice.get("/api/v1/history")).json()
    assert hist["items"][0]["card"]["title"] == "Movie 603"


async def test_kodi_token_opens_nothing_else(alice):
    token, _ = await pair(alice)
    async with client(Authorization=f"Bearer {token}") as tv:
        assert (await tv.get("/api/v1/me")).status_code == 401
        assert (await tv.post("/api/v1/title/movie/603/watchlist")).status_code == 401


async def test_kodi_shows_in_devices_and_can_be_signed_out(alice):
    token, _ = await pair(alice)
    devs = (await alice.get("/api/v1/me/devices")).json()
    assert [(d["name"], d["kind"]) for d in devs] == [("Kodi on SHIELD", "scrobble")]
    assert (await alice.delete(f"/api/v1/me/devices/{devs[0]['id']}")).status_code == 200
    async with client(csrf=False, Authorization=f"Bearer {token}") as tv:
        r = await tv.post("/api/v1/scrobble", json={"media_type": "movie", "tmdb_id": 603})
        assert r.status_code == 401


async def test_wrong_used_and_expired_codes(alice, bob):
    assert (await alice.post("/api/v1/me/devices/pair", json={"code": "ABCD-EFGH"})).status_code == 404
    _, code = await pair(alice)
    assert (await bob.post("/api/v1/me/devices/pair", json={"code": code})).status_code == 404  # collected and gone
    async with client(csrf=False) as tv:
        body = (await tv.post("/api/v1/scrobble/pair", json={"device": "Kodi"})).json()
    async with session() as db:
        await db.execute(update(PairCode).values(expires_at=dt.datetime.now(dt.timezone.utc) - dt.timedelta(seconds=1)))
        await db.commit()
    assert (await alice.post("/api/v1/me/devices/pair", json={"code": body["code"]})).status_code == 404
    async with client(csrf=False) as tv:
        assert (await tv.post("/api/v1/scrobble/pair/poll", json={"poll": body["poll"]})).status_code == 410


async def test_a_code_confirmed_once_cannot_be_confirmed_again(alice, bob):
    async with client(csrf=False) as tv:
        body = (await tv.post("/api/v1/scrobble/pair", json={"device": "Kodi"})).json()
    assert (await alice.post("/api/v1/me/devices/pair", json={"code": body["code"]})).status_code == 200
    assert (await bob.post("/api/v1/me/devices/pair", json={"code": body["code"]})).status_code == 409


async def test_one_address_cannot_block_pairing_for_every_tv():
    # X-Forwarded-For is never believed (the forward-auth proxy path is
    # gone, 2026-10-16): a caller counts by its connection's own address,
    # so one address is limited and any other address still can pair.
    from app.core.config import settings
    limit = settings.pair_starts_per_address
    got = []
    async with client(csrf=False, peer="10.240.38.20") as flood:  # one address, many pair starts
        for _ in range(limit + 1):
            got.append((await flood.post("/api/v1/scrobble/pair", json={})).status_code)
    assert got == [200] * limit + [429]
    # A made-up X-Forwarded-For doesn't dodge the limit either: it is not
    # believed, so the same address is still the one that counts.
    async with client(csrf=False, peer="10.240.38.20", **{"X-Forwarded-For": "198.51.100.7"}) as spoof:
        assert (await spoof.post("/api/v1/scrobble/pair", json={})).status_code == 429
    # Any other real address still can.
    async with client(csrf=False, peer="10.240.38.99") as tv:
        assert (await tv.post("/api/v1/scrobble/pair", json={"device": "Kodi"})).status_code == 200


async def test_a_caller_past_the_proxy_cant_dodge_the_pairing_limit_with_x_forwarded_for():
    # A caller reaching the app directly (another container on the shared
    # network) writes any X-Forwarded-For it likes: none of them are
    # believed, so the connection's own address counts.
    from app.core.config import settings
    limit = settings.pair_starts_per_address
    got = []
    for n in range(limit + 1):
        async with client(csrf=False, peer="10.240.38.7", **{"X-Forwarded-For": f"198.51.100.{n}"}) as direct:
            got.append((await direct.post("/api/v1/scrobble/pair", json={})).status_code)
    assert got == [200] * limit + [429]
    # Another address still can pair.
    async with client(csrf=False, peer="10.240.38.8") as tv:
        assert (await tv.post("/api/v1/scrobble/pair", json={"device": "Kodi"})).status_code == 200


async def test_an_episode_from_a_tvdb_scraped_library(alice):
    token, _ = await pair(alice)
    async with client(csrf=False, Authorization=f"Bearer {token}") as tv:
        r = await tv.post("/api/v1/scrobble", json={"media_type": "episode", "tvdb_id": 121361, "season": 1, "episode": 2,
                                                    "title": "Some Show", "source": "kodi", "ref": "kodi-ep-1"})
        assert r.json() == {"ok": True, "recorded": True, "detail": "Show 1399"}


async def test_kodi_can_download_the_add_on_without_signing_in():
    import io
    import zipfile
    async with client(csrf=False) as tv:
        index = await tv.get("/kodi/")
        assert index.status_code == 200
        name = __import__("re").search(r'href="(service\.movienight-[\d.]+\.zip)"', index.text).group(1)
        z = await tv.get(f"/kodi/{name}")
        assert z.status_code == 200 and z.headers["content-type"] == "application/zip"
        files = zipfile.ZipFile(io.BytesIO(z.content)).namelist()
        assert "service.movienight/addon.xml" in files and "service.movienight/resources/lib/service.py" in files
        assert not any(f.endswith(".pyc") for f in files)
        assert (await tv.get("/kodi/other.zip")).status_code == 404
