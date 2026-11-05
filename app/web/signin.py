"""The browser's sign-in page and the person's account page (server-rendered
forms, no script).

- /signin: a username and password form when PASSWORD_SIGNIN is on, and the
  OIDC button too when OIDC is set up, so an install can offer either or
  both. A signed-out browser is sent here (app/main.py, _sign_in_url) with
  the page it wanted as `next`, the phone app's /app/connect included.
- /account: change or set the password (when passwords are on), and sign out.
- /signout: ends this browser's session.

Each form carries a token kept in the signed session cookie (`csrf`), so a
page on another site can't submit them; /api writes have their own guard
(X-Requested-With, app/core/security.py).
"""
from __future__ import annotations

import hmac
import secrets
from pathlib import Path
from urllib.parse import quote

from fastapi import APIRouter, Depends, Form, Query, Request
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.templating import Jinja2Templates
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import current_user, get_db
from app.core.config import settings
from app.models import User
from app.services import passwords

router = APIRouter(include_in_schema=False)
templates = Jinja2Templates(directory=str(Path(__file__).resolve().parents[2] / "web"))

CSP = ("default-src 'self'; script-src 'none'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; "
       "form-action 'self'; frame-ancestors 'none'")


def _local(target: str | None) -> str:
    """A path on this site to land on afterwards, never another host."""
    if not target or not target.startswith("/") or target.startswith("//") or target.startswith("/\\"):
        return "/"
    return target


def _csrf(request: Request) -> str:
    token = request.session.get("csrf")
    if not token:
        token = request.session["csrf"] = secrets.token_urlsafe(24)
    return token


def _csrf_ok(request: Request, sent: str) -> bool:
    kept = request.session.get("csrf") or ""
    return bool(kept) and hmac.compare_digest(kept, sent or "")


def _page(request: Request, template: str, status: int = 200, **ctx) -> HTMLResponse:
    ctx.setdefault("csrf", _csrf(request))
    return templates.TemplateResponse(request, template, ctx, status_code=status,
                                      headers={"Cache-Control": "no-store", "Content-Security-Policy": CSP})


def _signin(request: Request, next_: str, status: int = 200, error: str | None = None, username: str = "") -> HTMLResponse:
    oidc = ("/api/v1/auth/oidc/login?next=" + quote(next_, safe="/")) if settings.oidc_enabled else None
    return _page(request, "signin.html", status, next=next_, error=error, username=username,
                 password=settings.password_signin, oidc=oidc, oidc_label=settings.oidc_button_label)


@router.get("/signin", response_class=HTMLResponse)
async def signin_page(request: Request, next: str = Query("/", max_length=2000)):  # noqa: A002
    if not settings.signin_enabled:
        return _page(request, "signin.html", 404, error="Sign-in isn't set up on this Movie Night.", password=False, oidc=None, next="/")
    return _signin(request, _local(next))


@router.post("/signin")
async def signin(request: Request, username: str = Form("", max_length=200), password: str = Form("", max_length=1000),
                 next: str = Form("/", max_length=2000), csrf: str = Form(""), db: AsyncSession = Depends(get_db)):  # noqa: A002
    target = _local(next)
    if not settings.password_signin:
        return _signin(request, target, 404, "Signing in with a password is turned off here.")
    if not _csrf_ok(request, csrf):
        return _signin(request, target, 400, "That form had expired. Try again.", username)
    try:
        user = await passwords.authenticate(db, username, password)
    except passwords.TooManyTries:
        return _signin(request, target, 429, "Too many wrong passwords for this username. Wait 15 minutes and try again.", username)
    if user is None:
        return _signin(request, target, 401, "That username and password don't match.", username)
    request.session.clear()  # a fresh session for the new sign-in
    request.session["uid"] = user.id
    request.session["ep"] = user.session_epoch or 0
    return RedirectResponse(target, status_code=303)


@router.get("/account", response_class=HTMLResponse)
async def account_page(request: Request, user: User = Depends(current_user)):
    return _page(request, "account.html", name=user.display_name or user.username, username=user.username,
                 password=settings.password_signin, has_password=bool(user.password_hash), done=request.query_params.get("done"))


@router.post("/account/password")
async def change_password(request: Request, current: str = Form("", max_length=1000), new: str = Form("", max_length=1000),
                          again: str = Form("", max_length=1000), csrf: str = Form(""),
                          db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    def fail(msg: str, status: int = 400) -> HTMLResponse:
        return _page(request, "account.html", status, name=user.display_name or user.username, username=user.username,
                     password=settings.password_signin, has_password=bool(user.password_hash), error=msg)
    if not settings.password_signin:
        return fail("Passwords are turned off on this Movie Night.", 404)
    if not _csrf_ok(request, csrf):
        return fail("That form had expired. Try again.")
    if user.password_hash and not passwords.check_password(current, user.password_hash):
        return fail("Your current password isn't right.", 401)
    if new != again:
        return fail("The two new passwords aren't the same.")
    try:
        await passwords.set_password(db, user, new)
    except passwords.PasswordError as exc:
        return fail(str(exc))
    # Every other browser is now signed out; this one stays signed in.
    request.session["ep"] = user.session_epoch
    return RedirectResponse("/account?done=1", status_code=303)


@router.post("/signout")
async def signout(request: Request, csrf: str = Form("")):
    if _csrf_ok(request, csrf):
        request.session.clear()
    return RedirectResponse("/signin" if settings.password_signin else "/", status_code=303)
