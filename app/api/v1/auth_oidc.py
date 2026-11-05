"""Sign-in with OIDC (the app's own sign-in; app/core/config.py, the OIDC_* settings).

When OIDC_ISSUER and OIDC_CLIENT_ID are set, Movie Night does the OIDC login
itself: ``/login`` redirects to the provider, ``/callback`` exchanges the code
and reads the ID token, and a signed session cookie keeps the person signed in
(app/api/deps.py). The forward-auth proxy header path this was introduced
alongside was removed on 2026-10-16; a session (or a dev/test user) is the only
browser identity now.

Register this exact redirect URL on the provider:

    {PUBLIC_URL}/api/v1/auth/oidc/callback

Leave the OIDC_* settings empty for no browser sign-in (tokens still work).
"""
from __future__ import annotations

import logging

from authlib.integrations.starlette_client import OAuth, OAuthError
from fastapi import APIRouter, Depends, HTTPException, Request
from fastapi.responses import RedirectResponse
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import get_db
from app.core.config import settings
from app.services import users

log = logging.getLogger("movienight.oidc")
router = APIRouter(tags=["auth"])

_oauth: OAuth | None = None


def _client():
    """The registered OpenID client, or None when OIDC is not configured."""
    global _oauth
    if not settings.oidc_enabled:
        return None
    if _oauth is None:
        oauth = OAuth()
        oauth.register(
            name="oidc",
            server_metadata_url=settings.oidc_issuer.rstrip("/") + "/.well-known/openid-configuration",
            client_id=settings.oidc_client_id,
            client_secret=settings.oidc_client_secret,
            client_kwargs={"scope": settings.oidc_scopes},
        )
        _oauth = oauth
    return _oauth.oidc


def _username_hint(info: dict) -> str | None:
    """The username to give a brand-new person: the configured claim, else
    preferred_username. users.get_or_create_oidc falls back further."""
    claim = settings.oidc_username_claim.strip()
    value = (info.get(claim) if claim else None) or info.get("preferred_username")
    value = str(value or "").strip()
    return value or None


def _local(target: str | None) -> str:
    """Where to land after signing in: a path on this site, never another
    host (open-redirect guard). Anything else: the site root."""
    if not target or not target.startswith("/") or target.startswith("//") or target.startswith("/\\"):
        return "/"
    return target


@router.get("/auth/oidc/config")
async def oidc_config() -> dict:
    """Whether the web app should show a sign-in button, and its label."""
    return {"enabled": settings.oidc_enabled, "button_label": settings.oidc_button_label}


@router.get("/auth/config")
async def auth_config() -> dict:
    """Which ways to sign in this install offers: a username and password
    (PASSWORD_SIGNIN, app/web/signin.py), OIDC, or both."""
    return {"password": settings.password_signin, "oidc": settings.oidc_enabled,
            "oidc_button_label": settings.oidc_button_label}


@router.get("/auth/oidc/login")
async def oidc_login(request: Request):
    """Send the person to the provider. authlib keeps the state/nonce/PKCE
    verifier in the session cookie and checks them at the callback.

    Where to come back to (the `next` query parameter) is kept in the session
    too: the callback URL is registered exactly on the provider, so nothing
    may be appended to it (app/main.py, _sign_in_url)."""
    client = _client()
    if client is None:
        raise HTTPException(status_code=404, detail="OIDC sign-in is not configured")
    request.session["next"] = _local(request.query_params.get("next"))
    return await client.authorize_redirect(request, settings.oidc_callback_url)


@router.get("/auth/oidc/callback")
async def oidc_callback(request: Request, db: AsyncSession = Depends(get_db)):
    """Finish the sign-in: exchange the code, read the ID token, sign in."""
    client = _client()
    if client is None:
        raise HTTPException(status_code=404, detail="OIDC sign-in is not configured")
    try:
        token = await client.authorize_access_token(request)
    except OAuthError as exc:
        log.warning("OIDC sign-in failed: %s", getattr(exc, "error", exc))
        raise HTTPException(status_code=401, detail="Sign-in failed") from exc
    info = token.get("userinfo") or {}
    sub = str(info.get("sub") or "").strip()
    if not sub:
        raise HTTPException(status_code=401, detail="The provider sent no subject")
    email = str(info.get("email") or "").strip() or None
    name = str(info.get("name") or info.get("preferred_username") or "").strip() or None
    user = await users.get_or_create_oidc(db, sub, email, name, _username_hint(info))
    request.session["uid"] = user.id
    request.session["ep"] = user.session_epoch or 0
    # Back to where the sign-in started (the session kept it); a `next` on the
    # callback is honoured too, for a provider that appends it.
    target = _local(request.session.pop("next", None) or request.query_params.get("next"))
    return RedirectResponse(target, status_code=303)


@router.post("/auth/oidc/logout")
async def oidc_logout(request: Request) -> dict:
    """Sign out of Movie Night only (the provider keeps its own session)."""
    request.session.pop("uid", None)
    request.session.pop("ep", None)
    return {"ok": True}
