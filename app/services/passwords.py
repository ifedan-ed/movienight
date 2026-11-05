"""Sign-in with a username and password (PASSWORD_SIGNIN; app/web/signin.py).

Passwords are kept only as scrypt hashes (Python's hashlib, no extra
dependency), in the form ``scrypt$N$r$p$salt$hash`` (salt and hash base64),
so the cost can be raised later and old hashes still check. Accounts are made
by the install's admin (app/cli.py); there is no sign-up.

Guessing is slowed per username: after FAIL_LIMIT wrong passwords in
FAIL_WINDOW, that username is refused until the window has passed. The app
runs as one process (Dockerfile), so an in-memory count is enough; it resets
on restart. It is per username, not per address: behind the proxy every
request comes from the same address (uvicorn --no-proxy-headers).
"""
from __future__ import annotations

import base64
import hashlib
import hmac
import secrets
import time

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.config import settings
from app.models import User
from app.services import users

# scrypt cost: 2^15 × 8 × 1 takes ~0.1 s and 32 MiB here; maxmem must allow it.
N, R, P = 2 ** 15, 8, 1
MAXMEM = 64 * 1024 * 1024
MAX_LENGTH = 200  # longer is refused, so a huge password can't be used to load the server

FAIL_LIMIT = 5
FAIL_WINDOW = 15 * 60  # seconds
_fails: dict[str, list[float]] = {}

# Checked against when the username doesn't exist or has no password, so a
# wrong username takes as long as a wrong password.
_DUMMY = None


class PasswordError(ValueError):
    """A password that can't be set, in words a person can act on."""


class TooManyTries(Exception):
    """This username had too many wrong passwords just now."""


def hash_password(password: str) -> str:
    salt = secrets.token_bytes(16)
    digest = hashlib.scrypt(password.encode(), salt=salt, n=N, r=R, p=P, maxmem=MAXMEM, dklen=32)
    b64 = lambda b: base64.b64encode(b).decode()  # noqa: E731
    return f"scrypt${N}${R}${P}${b64(salt)}${b64(digest)}"


def check_password(password: str, stored: str | None) -> bool:
    """Whether `password` matches the stored hash. Never raises."""
    try:
        kind, n, r, p, salt, digest = (stored or "").split("$")
        if kind != "scrypt":
            return False
        want = base64.b64decode(digest)
        got = hashlib.scrypt(password.encode(), salt=base64.b64decode(salt), n=int(n), r=int(r), p=int(p),
                             maxmem=MAXMEM, dklen=len(want))
        return hmac.compare_digest(got, want)
    except (ValueError, TypeError):
        return False


def _dummy() -> str:
    global _DUMMY
    if _DUMMY is None:
        _DUMMY = hash_password(secrets.token_urlsafe(16))
    return _DUMMY


def validate(password: str) -> None:
    """Refuses a password too short or too long to set."""
    if len(password) < settings.password_min_length:
        raise PasswordError(f"Use at least {settings.password_min_length} characters.")
    if len(password) > MAX_LENGTH:
        raise PasswordError(f"Use at most {MAX_LENGTH} characters.")


def _recent_fails(username: str) -> list[float]:
    cutoff = time.monotonic() - FAIL_WINDOW
    kept = [t for t in _fails.get(username, []) if t > cutoff]
    if kept:
        _fails[username] = kept
    else:
        _fails.pop(username, None)
    return kept


async def authenticate(db: AsyncSession, raw_username: str, password: str) -> User | None:
    """The person whose username and password these are, or None.
    Raises TooManyTries while the username is locked out."""
    username = users.clean_username(raw_username) or ""
    if len(_recent_fails(username)) >= FAIL_LIMIT:
        raise TooManyTries()
    user = await db.scalar(select(User).where(User.username == username)) if username else None
    ok = len(password) <= MAX_LENGTH and check_password(password, user.password_hash if user and user.password_hash else _dummy())
    if not (ok and user and user.password_hash):
        _fails.setdefault(username, []).append(time.monotonic())
        return None
    _fails.pop(username, None)
    return user


async def set_password(db: AsyncSession, user: User, password: str) -> None:
    """Sets (or changes) the password and signs every browser of this person
    out (session_epoch), the one that changed it included."""
    validate(password)
    user.password_hash = hash_password(password)
    user.session_epoch = (user.session_epoch or 0) + 1
    await db.commit()


async def clear_password(db: AsyncSession, user: User) -> None:
    """Removes the password (the person can then sign in only with OIDC, if
    that is set up) and signs their browsers out."""
    user.password_hash = None
    user.session_epoch = (user.session_epoch or 0) + 1
    await db.commit()
