"""Shared route dependencies: the database session, the signed-in person and
their per-request title state."""
from __future__ import annotations

from collections.abc import AsyncIterator
import re

from fastapi import Depends, HTTPException, Request
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.core import logs
from app.core.config import settings
from app.core.db import SessionLocal
from app.models import User
from app.services import devices, users
from app.services.state import UserState, load_user_state, seerr_media_map

# A digest email address a person types (app/api/v1/account.py).
EMAIL = re.compile(r"^[^@\s]{1,64}@[^@\s]{1,255}$")

async def get_db() -> AsyncIterator[AsyncSession]:
    async with SessionLocal() as s:
        yield s


async def current_user(request: Request, db: AsyncSession = Depends(get_db)) -> User:
    """The person signed in at the proxy (Authentik sends the username in a
    header the proxy sets on every request; believed only from the proxy
    itself), or the phone app's device token. Created on their first visit."""
    user = await _signed_in(request, db)
    logs.set_user(user.id)  # the access log names people by their numeric id only
    return user


async def _signed_in(request: Request, db: AsyncSession) -> User:
    auth = request.headers.get("authorization", "")
    if auth.lower().startswith("bearer "):
        # The phone app: its own device token instead of the proxy's header.
        try:
            user = await devices.user_for_token(db, auth[7:].strip())
        except devices.DeviceError as exc:  # its sign-in ran out: say why
            raise HTTPException(status_code=401, detail=str(exc)) from exc
        if user is None:
            raise HTTPException(status_code=401, detail="This device was signed out; sign in again")
        return user
    # A Movie Night OIDC session (app/api/v1/auth_oidc.py): the app's own
    # sign-in. A valid session is the only browser identity now that the
    # forward-auth proxy path is gone (2026-10-16).
    session = request.scope.get("session")
    if session:
        uid = session.get("uid")
        if uid is not None:
            user = await db.get(User, int(uid))
            # A session from before a password change or reset (session_epoch)
            # no longer counts: that signs every other browser out.
            if user is not None and session.get("ep", 0) == (user.session_epoch or 0):
                return user
            session.pop("uid", None)  # the account went away or was signed out: sign out
            session.pop("ep", None)
    # Local development and tests only (AUTH_DEV_ALLOWED): the caller in
    # X-Dev-User, or AUTH_DEV_USER when the header is absent. A test flag,
    # not a sign-in: never set on a server others can reach.
    username = None
    if settings.auth_dev_allowed:
        username = users.clean_username(request.headers.get("x-dev-user")) \
            or users.clean_username(settings.auth_dev_user)
    if not username:
        raise HTTPException(status_code=401, detail="Not signed in")
    alias_of = settings.aliases.get(username)
    if alias_of:
        # A second sign-in account of the same person: their own email and
        # name headers belong to the other account, so leave them out.
        return await users.get_or_create(db, alias_of)
    return await users.get_or_create(db, username, f"{username}@example.org", username)


async def require_admin(user: User = Depends(current_user)) -> User:
    if not user.is_admin:
        raise HTTPException(status_code=403, detail="Only the install's admins can do that")
    return user


async def user_state(db: AsyncSession, user: User, force_seerr: bool = False) -> UserState:
    return await load_user_state(db, user.id, await seerr_media_map(clients.seerr(), force=force_seerr))


def media_or_404(media: str) -> str:
    if media not in ("movie", "tv"):
        raise HTTPException(status_code=404, detail="unknown media type")
    return media
