"""Signing in the phone app: confirm on the web page, swap the code for a
device token (PKCE), use it, sign the device out."""
import datetime as dt
import re
import secrets

from app.services.devices import challenge_of
from tests.conftest import client


async def sign_in_app(c, verifier, redirect="movienight://auth"):
    challenge = challenge_of(verifier)
    page = await c.get("/app/connect", params={"challenge": challenge, "redirect": redirect, "device": "Test phone"})
    assert page.status_code == 200 and "Sign in the app?" in page.text
    form = dict(re.findall(r'name="(\w+)" value="([^"]*)"', page.text))
    r = await c.post("/app/connect", data=form, follow_redirects=False)
    assert r.status_code == 303 and r.headers["location"].startswith("movienight://auth?code=")
    return r.headers["location"].split("code=", 1)[1]


async def app_token(c) -> str:
    verifier = secrets.token_urlsafe(48)
    code = await sign_in_app(c, verifier)
    async with client() as app:
        return (await app.post("/api/v1/app/token", json={"code": code, "verifier": verifier})).json()["token"]


async def test_app_sign_in_round_trip(alice):
    verifier = secrets.token_urlsafe(48)
    code = await sign_in_app(alice, verifier)
    async with client() as app:  # the phone: no proxy header
        bad = await app.post("/api/v1/app/token", json={"code": code, "verifier": secrets.token_urlsafe(48)})
        assert bad.status_code == 400  # someone else's verifier
        ok = await app.post("/api/v1/app/token", json={"code": code, "verifier": verifier})
        assert ok.status_code == 200
        token = ok.json()["token"]
        again = await app.post("/api/v1/app/token", json={"code": code, "verifier": verifier})
        assert again.status_code == 400  # one use only
    async with client(Authorization=f"Bearer {token}") as phone:
        me = (await phone.get("/api/v1/me")).json()
        assert me["user"] == "alice"
        assert (await phone.post("/api/v1/title/movie/603/watchlist")).json()["watchlist"] is True
    devices = (await alice.get("/api/v1/me/devices")).json()
    assert [d["name"] for d in devices] == ["Test phone"] and devices[0]["last_used_at"]
    assert (await alice.delete(f"/api/v1/me/devices/{devices[0]['id']}")).status_code == 200
    async with client(Authorization=f"Bearer {token}") as phone:
        assert (await phone.get("/api/v1/me")).status_code == 401  # signed out


async def test_connect_page_refuses_other_redirects_and_forged_forms(alice):
    challenge = challenge_of(secrets.token_urlsafe(48))
    r = await alice.get("/app/connect", params={"challenge": challenge, "redirect": "https://evil.example/cb"})
    assert r.status_code == 400
    r = await alice.post("/app/connect", data={"challenge": challenge, "redirect": "movienight://auth", "device": "x",
                                               "ts": "1", "sig": "0" * 64}, follow_redirects=False)
    assert r.status_code == 400  # no valid signature: the page has to be shown and confirmed first


async def test_bad_bearer_is_refused(alice):
    async with client(Authorization="Bearer mn_app_nope") as phone:
        assert (await phone.get("/api/v1/me")).status_code == 401


async def test_unused_and_old_device_tokens_run_out(alice):
    from sqlalchemy import update

    from app.core.db import session
    from app.models import AppToken
    from app.services.devices import token_idle, token_max_age

    now = dt.datetime.now(dt.timezone.utc)
    for created, used, why in ((now - token_max_age() - dt.timedelta(days=1), now, "it signed in 365 days ago"),  # in use, but over a year old
                               (now - dt.timedelta(days=200), now - token_idle() - dt.timedelta(days=1),
                                "it wasn't used for 90 days")):  # not used for months
        token = await app_token(alice)
        async with session() as db:
            await db.execute(update(AppToken).values(created_at=created, last_used_at=used))
            await db.commit()
        async with client(Authorization=f"Bearer {token}") as phone:
            r = await phone.get("/api/v1/me")
            assert r.status_code == 401 and why in r.json()["detail"]  # says why, in plain words
        assert (await alice.get("/api/v1/me/devices")).json() == []  # and it is gone from the list
    token = await app_token(alice)
    async with client(Authorization=f"Bearer {token}") as phone:
        assert (await phone.get("/api/v1/me")).status_code == 200  # a fresh one works


async def test_an_expired_device_leaves_the_list_without_being_used_again(alice):
    from sqlalchemy import update

    from app.core.db import session
    from app.models import AppToken
    from app.services.devices import token_idle

    await app_token(alice)
    async with session() as db:
        await db.execute(update(AppToken).values(last_used_at=dt.datetime.now(dt.timezone.utc) - token_idle() - dt.timedelta(days=1)))
        await db.commit()
    await app_token(alice)  # a live one, made after
    devices = (await alice.get("/api/v1/me/devices")).json()
    assert len(devices) == 1  # the phone that went quiet is not shown as signed in


async def test_an_admin_can_sign_out_all_of_someones_devices(alice, bob, capsys):
    import pytest

    from scripts import sign_out_devices

    tokens = [await app_token(alice), await app_token(alice)]
    bobs = await app_token(bob)
    await sign_out_devices.main("Alice")
    assert capsys.readouterr().out.strip() == "Signed out 2 devices of Alice"
    with pytest.raises(SystemExit):
        await sign_out_devices.main("nobody")
    for token in tokens:
        async with client(Authorization=f"Bearer {token}") as phone:
            assert (await phone.get("/api/v1/me")).status_code == 401
    async with client(Authorization=f"Bearer {bobs}") as phone:
        assert (await phone.get("/api/v1/me")).status_code == 200  # nobody else's


async def test_admin_rights_taken_away_reach_the_app_too(monkeypatch):
    from app.core.config import settings

    monkeypatch.setattr(settings, "admin_users", "carol")
    async with client("carol") as carol:
        token = await app_token(carol)
    async with client(Authorization=f"Bearer {token}") as phone:
        assert (await phone.get("/api/v1/me")).json()["is_admin"] is True
        monkeypatch.setattr(settings, "admin_users", "")  # taken off ADMIN_USERS
        assert (await phone.get("/api/v1/me")).json()["is_admin"] is False
        assert (await phone.post("/api/v1/sync/library")).status_code == 403


async def test_play_can_return_to_the_app(alice, up):
    from tests.conftest import sync_library
    from tests.test_shows_play_sync import link_jellyfin

    await sync_library()
    await link_jellyfin(alice, up)
    # Built-in players get no link back; a player someone adds can ask for one
    # with {done}, which points at the app or the web, wherever Play was pressed.
    await alice.put("/api/v1/me/players", json={"custom": [{"name": "Back", "template": "backplayer://play?url={url}&done={done}"}],
                                                "enabled": ["custom1"], "default_player": "custom1"})
    out = (await alice.post("/api/v1/title/movie/603/play", json={"player": "custom1", "return_to": "app"})).json()
    assert "done=movienight%3A%2F%2Fplay%2F" in out["url"]
    web = (await alice.post("/api/v1/title/movie/603/play", json={"player": "custom1"})).json()
    assert "done=https%3A%2F%2Fmn.test%2Fplay%2F" in web["url"]


async def test_signed_in_devices_show_when_they_run_out(alice, monkeypatch):
    from sqlalchemy import update

    from app.core.config import settings
    from app.core.db import session
    from app.models import AppToken

    await app_token(alice)
    [d] = (await alice.get("/api/v1/me/devices")).json()
    created = dt.datetime.fromisoformat(d["created_at"])
    assert d["expires_because"] == "unused"
    assert abs(dt.datetime.fromisoformat(d["expires_at"]) - (created + dt.timedelta(days=90))) < dt.timedelta(seconds=5)
    # Used every day for ten months: what is left is the year since it signed in.
    async with session() as db:
        await db.execute(update(AppToken).values(created_at=created - dt.timedelta(days=300), last_used_at=created))
        await db.commit()
    [d] = (await alice.get("/api/v1/me/devices")).json()
    assert d["expires_because"] == "age"
    # Both are settings (DEVICE_IDLE_DAYS, DEVICE_MAX_DAYS).
    monkeypatch.setattr(settings, "device_max_days", 730)
    assert (await alice.get("/api/v1/me/devices")).json()[0]["expires_because"] == "unused"
