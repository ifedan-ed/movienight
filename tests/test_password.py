"""Sign-in with a username and password (PASSWORD_SIGNIN; app/web/signin.py,
app/services/passwords.py): the sign-in page and its form, wrong passwords and
the per-username lockout, where a signed-out browser is sent, the account
page's password change signing other browsers out, and sign-out."""
from __future__ import annotations

import re

import pytest
from sqlalchemy import select

from app.core.config import settings
from app.core.db import SessionLocal
from app.models import User
from app.services import passwords
from tests.conftest import client

PW = "correct horse battery"


@pytest.fixture(autouse=True)
def password_on(monkeypatch):
    monkeypatch.setattr(settings, "password_signin", True, raising=False)
    passwords._fails.clear()
    yield
    passwords._fails.clear()


async def make_user(name="carol", password=PW) -> User:
    async with SessionLocal() as db:
        u = User(username=name, display_name=name.title())
        db.add(u)
        await db.flush()
        await passwords.set_password(db, u, password)
        await db.refresh(u)
        return u


def anon():
    # A browser: no dev user, and form posts don't carry the API's header.
    return client(None, csrf=False)


async def form_token(c, path="/signin") -> str:
    r = await c.get(path)
    m = re.search(r'name="csrf" value="([^"]+)"', r.text)
    assert m, r.text
    return m.group(1)


async def sign_in(c, username="carol", password=PW, next_="/"):
    token = await form_token(c)
    return await c.post("/signin", data={"username": username, "password": password, "csrf": token, "next": next_},
                        follow_redirects=False)


def test_hash_round_trip():
    h = passwords.hash_password(PW)
    assert h.startswith("scrypt$") and PW not in h
    assert passwords.check_password(PW, h)
    assert not passwords.check_password(PW + "x", h)
    assert not passwords.check_password(PW, "garbage")
    assert not passwords.check_password(PW, None)


async def test_auth_config_says_what_is_offered():
    async with anon() as c:
        r = await c.get("/api/v1/auth/config")
    assert r.json() == {"password": True, "oidc": False, "oidc_button_label": "Sign in"}


async def test_signed_out_page_goes_to_signin_with_next():
    async with anon() as c:
        r = await c.get("/lists?x=1", headers={"Accept": "text/html"}, follow_redirects=False)
    assert r.status_code in (302, 307)
    assert r.headers["location"] == "/signin?next=/lists%3Fx%3D1"


async def test_signin_page_shows_form_and_oidc_when_both(monkeypatch):
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    monkeypatch.setattr(settings, "oidc_button_label", "Sign in with SSO", raising=False)
    async with anon() as c:
        r = await c.get("/signin?next=/lists")
    assert r.status_code == 200
    assert 'name="password"' in r.text
    assert "Sign in with SSO" in r.text and "/api/v1/auth/oidc/login?next=/lists" in r.text
    assert "script-src 'none'" in r.headers["content-security-policy"]


async def test_oidc_only_still_goes_straight_to_provider(monkeypatch):
    monkeypatch.setattr(settings, "password_signin", False, raising=False)
    monkeypatch.setattr(settings, "oidc_issuer", "https://sso.test", raising=False)
    monkeypatch.setattr(settings, "oidc_client_id", "cid", raising=False)
    async with anon() as c:
        r = await c.get("/", headers={"Accept": "text/html"}, follow_redirects=False)
    assert r.headers["location"].startswith("/api/v1/auth/oidc/login?next=/")


async def test_sign_in_and_use_the_api():
    await make_user()
    async with anon() as c:
        r = await sign_in(c, next_="/lists")
        assert r.status_code == 303 and r.headers["location"] == "/lists"
        me = await c.get("/api/v1/me")
    assert me.status_code == 200 and me.json()["user"] == "carol"


async def test_username_is_case_insensitive():
    await make_user()
    async with anon() as c:
        assert (await sign_in(c, username="  Carol ")).status_code == 303


async def test_wrong_password_and_unknown_user_say_the_same():
    await make_user()
    async with anon() as c:
        bad = await sign_in(c, password="nope nope nope")
        nobody = await sign_in(c, username="nobody")
    assert bad.status_code == nobody.status_code == 401
    assert "don&#39;t match" in bad.text and "don&#39;t match" in nobody.text


async def test_lockout_after_five_wrong_passwords():
    await make_user()
    async with anon() as c:
        for _ in range(passwords.FAIL_LIMIT):
            assert (await sign_in(c, password="wrong password!")).status_code == 401
        locked = await sign_in(c)  # even the right one, while locked
    assert locked.status_code == 429


async def test_form_without_its_token_is_refused():
    await make_user()
    async with anon() as c:
        await c.get("/signin")
        r = await c.post("/signin", data={"username": "carol", "password": PW, "csrf": "forged"}, follow_redirects=False)
    assert r.status_code == 400


async def test_next_never_leaves_the_site():
    await make_user()
    async with anon() as c:
        r = await sign_in(c, next_="//evil.example/x")
    assert r.headers["location"] == "/"


async def test_password_off_refuses_the_form(monkeypatch):
    await make_user()
    async with anon() as c:
        token = await form_token(c)
        monkeypatch.setattr(settings, "password_signin", False, raising=False)
        r = await c.post("/signin", data={"username": "carol", "password": PW, "csrf": token}, follow_redirects=False)
    assert r.status_code == 404


async def test_user_without_password_cannot_sign_in_with_one():
    async with SessionLocal() as db:
        db.add(User(username="dave"))
        await db.commit()
    async with anon() as c:
        assert (await sign_in(c, username="dave", password="")).status_code == 401


async def test_change_password_signs_other_browsers_out():
    await make_user()
    async with anon() as one, anon() as two:
        await sign_in(one)
        await sign_in(two)
        token = await form_token(one, "/account")
        r = await one.post("/account/password", data={"current": PW, "new": "a brand new password", "again": "a brand new password",
                                                       "csrf": token}, follow_redirects=False)
        assert r.status_code == 303 and r.headers["location"] == "/account?done=1"
        assert (await one.get("/api/v1/me")).status_code == 200  # the one that changed it stays in
        assert (await two.get("/api/v1/me")).status_code == 401  # the other is out
    async with anon() as c:
        assert (await sign_in(c, password="a brand new password")).status_code == 303


async def test_change_password_checks_current_and_length():
    await make_user()
    async with anon() as c:
        await sign_in(c)
        token = await form_token(c, "/account")
        wrong = await c.post("/account/password", data={"current": "not it at all", "new": "x" * 12, "again": "x" * 12, "csrf": token})
        short = await c.post("/account/password", data={"current": PW, "new": "short", "again": "short", "csrf": token})
        differ = await c.post("/account/password", data={"current": PW, "new": "x" * 12, "again": "y" * 12, "csrf": token})
    assert wrong.status_code == 401
    assert short.status_code == 400 and "at least 10" in short.text
    assert differ.status_code == 400


async def test_sign_out():
    await make_user()
    async with anon() as c:
        await sign_in(c)
        token = await form_token(c, "/account")
        r = await c.post("/signout", data={"csrf": token}, follow_redirects=False)
        assert r.headers["location"] == "/signin"
        assert (await c.get("/api/v1/me")).status_code == 401


async def test_cli_add_and_reset(monkeypatch, capsys):
    import io

    from app import cli
    monkeypatch.setattr("sys.stdin", io.StringIO(PW + "\n"))
    await cli.main(["user", "add", "Erin", "--name", "Erin E", "--stdin"])
    async with SessionLocal() as db:
        u = await db.scalar(select(User).where(User.username == "erin"))
        assert u and u.display_name == "Erin E" and passwords.check_password(PW, u.password_hash)
    monkeypatch.setattr("sys.stdin", io.StringIO("another long password\n"))
    await cli.main(["user", "password", "erin", "--stdin"])
    async with anon() as c:
        assert (await sign_in(c, username="erin", password="another long password")).status_code == 303
    await cli.main(["user", "list"])
    assert "erin" in capsys.readouterr().out
