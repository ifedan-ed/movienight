"""People: who is signed in (from their Movie Night OIDC session), their own
settings, and their link to a Jellyfin account."""
from __future__ import annotations

import datetime as dt
import json
from typing import Any

from sqlalchemy import delete, select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.exc import IntegrityError
from sqlalchemy.ext.asyncio import AsyncSession

from app.core import crypto
from app.core.config import settings
from app.models import User, UserSetting
from app.services import status

# last_seen_at is written at most this often, so an active person doesn't
# cost a database write on every request (docs/limits.md).
SEEN_EVERY = dt.timedelta(minutes=10)


def _now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def clean_username(raw: str | None) -> str | None:
    name = (raw or "").strip().lower()
    if not name or len(name) > 200 or any(ord(ch) < 32 for ch in name):
        return None
    return name


async def get_or_create(db: AsyncSession, username: str, email: str | None = None, name: str | None = None) -> User:
    """The person behind this request, created on their first visit."""
    user = await db.scalar(select(User).where(User.username == username))
    if user is None:
        user = User(username=username, email=email or None, display_name=name or None,
                    is_admin=username in settings.admins, last_seen_at=_now())
        db.add(user)
        try:
            await db.commit()
        except IntegrityError:  # two first requests at once
            await db.rollback()
            user = await db.scalar(select(User).where(User.username == username))
        return user
    changed = False
    if email and email != user.email:
        user.email, changed = email, True
    if name and name != user.display_name:
        user.display_name, changed = name, True
    if follow_admins(user):
        changed = True
    if user.last_seen_at is None or _now() - user.last_seen_at > SEEN_EVERY:
        user.last_seen_at, changed = _now(), True
    if changed:
        await db.commit()
    return user


async def get_or_create_oidc(db: AsyncSession, sub: str, email: str | None = None,
                             name: str | None = None, username: str | None = None) -> User:
    """The person behind an OIDC sign-in, matched by the provider's subject
    ("sub"): a changed email or username keeps the same Movie Night account.
    An existing user with the same username is linked to the subject rather
    than creating a second account."""
    if not sub:
        raise ValueError("an OIDC subject is required")
    user = await db.scalar(select(User).where(User.oidc_sub == sub))
    if user is not None:
        changed = False
        if email and email != user.email:
            user.email, changed = email, True
        if name and name != user.display_name:
            user.display_name, changed = name, True
        if follow_admins(user):
            changed = True
        if user.last_seen_at is None or _now() - user.last_seen_at > SEEN_EVERY:
            user.last_seen_at, changed = _now(), True
        if changed:
            await db.commit()
        return user
    clean = (clean_username(username) or clean_username((email or "").split("@")[0])
             or f"user-{sub[:24]}")
    user = await db.scalar(select(User).where(User.username == clean))
    if user is None:
        user = User(username=clean, email=email or None, display_name=name or None,
                    is_admin=clean in settings.admins, oidc_sub=sub, last_seen_at=_now())
        db.add(user)
    else:  # an existing proxy/local person, now also signing in with OIDC
        user.oidc_sub = sub
        if email and not user.email:
            user.email = email
        if name and not user.display_name:
            user.display_name = name
    try:
        await db.commit()
    except IntegrityError:  # a racing first sign-in
        await db.rollback()
        user = await db.scalar(select(User).where(User.oidc_sub == sub))
    return user


def follow_admins(user: User) -> bool:
    """Admin rights follow ADMIN_USERS and OWNER_USERNAME both ways: someone
    taken off them loses the rights on their next request. True if changed."""
    admin = user.username in settings.admins
    if user.is_admin == admin:
        return False
    user.is_admin = admin
    return True


# ---------------------------------------------------------------------------
# Per-person settings (JSON values)
# ---------------------------------------------------------------------------


async def get_setting(db: AsyncSession, user_id: int, key: str, default: Any = None) -> Any:
    row = await db.get(UserSetting, (user_id, key))
    if row is None:
        return default
    try:
        return json.loads(row.value)
    except ValueError:
        return default


async def set_setting(db: AsyncSession, user_id: int, key: str, value: Any) -> None:
    raw = json.dumps(value)
    stmt = pg_insert(UserSetting).values(user_id=user_id, key=key, value=raw)
    await db.execute(stmt.on_conflict_do_update(index_elements=[UserSetting.user_id, UserSetting.key], set_={"value": raw}))
    await db.commit()


async def delete_setting(db: AsyncSession, user_id: int, key: str) -> None:
    await db.execute(delete(UserSetting).where(UserSetting.user_id == user_id, UserSetting.key == key))
    await db.commit()


# ---------------------------------------------------------------------------
# Jellyfin link
# ---------------------------------------------------------------------------


def device_id(user: User) -> str:
    """One Jellyfin "device" per person, so their session stands alone."""
    return f"movienight-{user.id}"


def jellyfin_token(user: User) -> str | None:
    return crypto.decrypt(user.jellyfin_token_enc)


class LinkError(Exception):
    pass


async def link_jellyfin(db: AsyncSession, user: User, auth: dict[str, Any]) -> User:
    """Save the result of a Jellyfin sign-in (Quick Connect or password):
    their Jellyfin user and an encrypted token. The password is never kept."""
    jf_user = auth.get("User") or {}
    jf_id, token = jf_user.get("Id"), auth.get("AccessToken")
    if not jf_id or not token:
        raise LinkError("Jellyfin didn't return an account")
    other = await db.scalar(select(User).where(User.jellyfin_user_id == jf_id, User.id != user.id))
    if other is not None:
        raise LinkError("That Jellyfin account is already connected to someone else here")
    if user.jellyfin_user_id and user.jellyfin_user_id != jf_id:
        user.seerr_user_id = None  # a different Jellyfin account means a different Seerr user
    user.jellyfin_user_id = jf_id
    user.jellyfin_user_name = jf_user.get("Name")
    user.jellyfin_token_enc = crypto.encrypt(token)
    user.jellyfin_linked_at = _now()
    await db.commit()
    await status.forget(db, status.plays(user.id))  # an old account's failed reads are no longer news
    return user


async def unlink_jellyfin(db: AsyncSession, user: User) -> str | None:
    """Forget the link; returns the old token so the caller can sign it out."""
    token = jellyfin_token(user)
    user.jellyfin_user_id = None
    user.jellyfin_user_name = None
    user.jellyfin_token_enc = None
    user.jellyfin_linked_at = None
    user.seerr_user_id = None
    await db.commit()
    await status.forget(db, status.plays(user.id))
    return token
