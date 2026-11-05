"""Who is signed in, and the request-level protections."""
import secrets

import pytest
from sqlalchemy import func, select

from app.core import security
from app.core.config import settings
from app.core.db import session
from app.models import User
from tests.conftest import client


async def test_first_visit_creates_the_person(alice):
    me = (await alice.get("/api/v1/me")).json()
    assert me["user"] == "alice" and me["email"] == "alice@example.org"
    assert me["is_admin"] is False
    assert me["stats"]["movies"] == 0


async def test_owner_is_admin_and_alias_is_the_same_person(owner):
    assert (await owner.get("/api/v1/me")).json()["is_admin"] is True
    await owner.post("/api/v1/title/movie/603/watchlist")
    async with client("owner-alt") as alt:
        me = (await alt.get("/api/v1/me")).json()
    assert me["user"] == "owner" and me["stats"]["watchlist"] == 1


async def test_admin_rights_follow_the_settings_both_ways(monkeypatch):
    from app.core.config import settings

    monkeypatch.setattr(settings, "admin_users", "carol")
    async with client("carol") as carol:
        assert (await carol.get("/api/v1/me")).json()["is_admin"] is True
        monkeypatch.setattr(settings, "admin_users", "")  # taken off ADMIN_USERS
        assert (await carol.get("/api/v1/me")).json()["is_admin"] is False
        assert (await carol.post("/api/v1/sync/library")).status_code == 403


async def test_no_sign_in_is_refused():
    async with client() as anon:
        assert (await anon.get("/api/v1/me")).status_code == 401


async def test_the_api_description_needs_a_sign_in(alice):
    async with client(Authorization="Bearer mn_app_madeup") as fake:  # the proxy lets this through unchecked
        assert (await fake.get("/api/v1/openapi.json")).status_code == 401
        assert (await fake.get("/api/v1/docs")).status_code == 401
    assert "/api/v1/me" in (await alice.get("/api/v1/openapi.json")).json()["paths"]
    assert "swagger-ui" in (await alice.get("/api/v1/docs")).text


async def test_writes_need_the_csrf_header():
    async with client("alice", csrf=False) as c:
        r = await c.post("/api/v1/title/movie/603/watchlist")
    assert r.status_code == 403


async def test_dot_segments_and_encoded_slashes_are_refused():
    async with client("alice") as c:
        for path in ("/api/v1/scrobble/%2e%2e/me", "/api/v1/scrobble//me", "/api/v1/scrobble/..%2fme"):
            r = await c.get(path)
            assert r.status_code == 400, path


async def test_security_headers(alice):
    r = await alice.get("/api/v1/me")
    assert "script-src 'self'" in r.headers["content-security-policy"]
    assert r.headers["x-frame-options"] == "DENY"
    assert r.headers["x-content-type-options"] == "nosniff"


async def test_admin_only_actions(alice, owner):
    assert (await alice.post("/api/v1/sync/library")).status_code == 403
    r = await owner.post("/api/v1/sync/library")
    assert r.status_code == 200 and r.json()["counts"]["items"] == 2


# ---------------------------------------------------------------------------
# The sign-in headers sign nobody in (2026-10-16: the forward-auth proxy path
# was removed; the app believes only its OIDC session and the tokens). A
# caller from anywhere — the proxy's own address, another container on the
# shared network, loopback — cannot authenticate with a header.
# ---------------------------------------------------------------------------

OTHER_CONTAINER = "10.240.38.7"  # another container on the shared network
PROXY = "10.240.38.1"  # that network's gateway: the host, where Caddy runs


async def _people() -> int:
    async with session() as db:
        return await db.scalar(select(func.count()).select_from(User))


async def test_identity_headers_sign_nobody_in(up):
    # Before: this was the owner, admin rights and all.
    headers = {"X-Authentik-Username": "owner", "X-Authentik-Email": "owner@example.org", "X-Authentik-Name": "Owner"}
    for peer in (OTHER_CONTAINER, PROXY, "127.0.0.1"):
        async with client(peer=peer, **headers) as sender:
            assert (await sender.get("/api/v1/me")).status_code == 401, peer
            assert (await sender.post("/api/v1/sync/library")).status_code == 401, peer
            assert (await sender.get("/api/v1/openapi.json")).status_code == 401, peer
    # X-Forwarded-For doesn't help either.
    async with client(peer=OTHER_CONTAINER, **{"X-Forwarded-For": PROXY}) as sender:
        assert (await sender.get("/api/v1/me")).status_code == 401
    assert await _people() == 0  # nobody was created or changed


async def test_tokens_still_work_from_anywhere(up):
    # The phone app, Kodi, the Jellyfin plugin and other players never sign
    # in with a browser session: their tokens are checked on their own,
    # wherever the call comes from.
    from tests.test_devices import sign_in_app
    from tests.test_jellyfin_plugin import MOVIE, jf_user
    from tests.test_kodi import pair
    from tests.test_shows_play_sync import link_jellyfin

    verifier = secrets.token_urlsafe(48)
    async with client("alice") as alice:  # a code needs a signed-in person
        code = await sign_in_app(alice, verifier)
    async with client(peer=OTHER_CONTAINER) as phone:  # the phone swaps its code: no sign-in headers
        token = (await phone.post("/api/v1/app/token", json={"code": code, "verifier": verifier})).json()["token"]
    # A phone token works from anywhere; it carries whose it is.
    async with client(peer=OTHER_CONTAINER, Authorization=f"Bearer {token}") as p:
        assert (await p.get("/api/v1/me")).json()["user"] == "alice"
