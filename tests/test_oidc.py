"""Sign-in with OIDC (app/api/v1/auth_oidc.py, app/services/users.get_or_create_oidc).

The full HTTP redirect/token flow needs a provider; here the config endpoint
and the person-matching logic are covered (matched by subject, linking an
existing username rather than creating a second account).
"""
from __future__ import annotations

from app.core.config import settings
from app.core.db import session
from app.services import users


async def test_oidc_config_off(owner):
    r = await owner.get("/api/v1/auth/oidc/config")
    assert r.status_code == 200
    assert r.json() == {"enabled": False, "button_label": "Sign in"}


async def test_oidc_config_on(owner, monkeypatch):
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(settings, "oidc_button_label", "SSO", raising=False)
    r = await owner.get("/api/v1/auth/oidc/config")
    assert r.json() == {"enabled": True, "button_label": "SSO"}


async def test_oidc_login_redirects_to_provider(owner, monkeypatch):
    # The route hands off to the OIDC client's redirect (authlib builds the
    # real authorize URL from the provider's discovery document, which needs
    # the network). Stub the client so the route itself is what's tested.
    class _Stub:
        async def authorize_redirect(self, request, redirect_uri):
            from fastapi.responses import RedirectResponse
            assert redirect_uri.endswith("/api/v1/auth/oidc/callback")
            return RedirectResponse("https://sso.test/authorize?state=x", status_code=302)

    from app.api.v1 import auth_oidc
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(auth_oidc, "_client", lambda: _Stub())
    r = await owner.get("/api/v1/auth/oidc/login", follow_redirects=False)
    assert r.status_code == 302
    assert r.headers["location"].startswith("https://sso.test/authorize")


# ---------------------------------------------------------------------------
# Coming back to where the sign-in started: the phone app's
# /app/connect?challenge=… round-trip is the one that depends on it most
# (app/main.py, _sign_in_url; app/api/v1/auth_oidc.py).
# ---------------------------------------------------------------------------


async def test_app_connect_signed_out_carries_the_page_as_next(monkeypatch):
    from tests.conftest import client as make
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    url = "/app/connect?challenge=" + "A" * 43 + "&redirect=movienight%3A%2F%2Fauth&device=Phone"
    async with make() as anon:  # nobody signed in
        r = await anon.get(url, headers={"Accept": "text/html"}, follow_redirects=False)
    assert r.status_code in (302, 307)
    loc = r.headers["location"]
    assert loc.startswith("/api/v1/auth/oidc/login?next="), loc
    assert loc.startswith("/api/v1/auth/oidc/login?next=/app/connect") and "movienight" in loc


async def test_oidc_login_keeps_next_in_the_session(monkeypatch):
    seen = {}

    class _Stub:
        async def authorize_redirect(self, request, redirect_uri):
            from fastapi.responses import RedirectResponse
            seen["next"] = request.session.get("next")
            return RedirectResponse("https://sso.test/authorize", status_code=302)

    from app.api.v1 import auth_oidc
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(auth_oidc, "_client", lambda: _Stub())
    from tests.conftest import client as make
    async with make() as c:
        r = await c.get("/api/v1/auth/oidc/login?next=%2Fapp%2Fconnect%3Fchallenge%3Dx%26redirect%3Dmovienight%253A%252F%252Fauth",
                        follow_redirects=False)
    assert r.status_code == 302
    assert seen["next"] == "/app/connect?challenge=x&redirect=movienight%3A%2F%2Fauth"


async def test_oidc_callback_returns_to_next(up, monkeypatch):
    class _Stub:
        async def authorize_redirect(self, request, redirect_uri):
            from fastapi.responses import RedirectResponse
            return RedirectResponse("https://sso.test/authorize", status_code=302)

        async def authorize_access_token(self, request):
            return {"userinfo": {"sub": "sub-roundtrip", "email": "rt@example.org",
                                 "name": "Round Trip", "preferred_username": "roundtrip"}}

    from app.api.v1 import auth_oidc
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(auth_oidc, "_client", lambda: _Stub())
    from tests.conftest import client as make
    async with make() as c:
        # /login keeps `next` in the session cookie; the callback reads it back.
        await c.get("/api/v1/auth/oidc/login?next=%2Fapp%2Fconnect%3Fchallenge%3Dx", follow_redirects=False)
        r = await c.get("/api/v1/auth/oidc/callback?code=abc&state=x", follow_redirects=False)
    assert r.status_code == 303
    assert r.headers["location"] == "/app/connect?challenge=x"


async def test_oidc_callback_refuses_an_offsite_next(up, monkeypatch):
    class _Stub:
        async def authorize_redirect(self, request, redirect_uri):
            from fastapi.responses import RedirectResponse
            return RedirectResponse("https://sso.test/authorize", status_code=302)

        async def authorize_access_token(self, request):
            return {"userinfo": {"sub": "sub-evil", "email": "evil@example.org", "name": "E"}}

    from app.api.v1 import auth_oidc
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(auth_oidc, "_client", lambda: _Stub())
    from tests.conftest import client as make
    async with make() as c:
        await c.get("/api/v1/auth/oidc/login?next=%2F%2Fevil.example.com", follow_redirects=False)
        r = await c.get("/api/v1/auth/oidc/callback?code=abc&state=x", follow_redirects=False)
    assert r.status_code == 303 and r.headers["location"] == "/"


async def test_get_or_create_oidc(up):
    async with session() as db:
        u1 = await users.get_or_create_oidc(db, "sub-1", "a@example.org", "Alice", "alice")
        assert u1.username == "alice"
        assert u1.email == "a@example.org"
        # same subject: same person, email updated, no duplicate
        u2 = await users.get_or_create_oidc(db, "sub-1", "a2@example.org", "Alice", "alice")
        assert u2.id == u1.id and u2.email == "a2@example.org"
    # a different subject with the same username links, not a second account
    async with session() as db:
        u3 = await users.get_or_create_oidc(db, "sub-2", None, None, "alice")
        assert u3.id == u1.id and u3.oidc_sub == "sub-2"


async def test_get_or_create_oidc_username_fallback(up):
    async with session() as db:
        u = await users.get_or_create_oidc(db, "sub-xyz", "Bob@Example.org", None, None)
        assert u.username == "bob"
        assert u.email == "Bob@Example.org"
