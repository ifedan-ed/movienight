"""Signing in the Movie Night phone app.

The app can't use the browser's Movie Night session cookie, so it signs in once
through a page it opens in a browser (/app/connect): the person confirms,
the page hands the app a one-time code, and the app swaps the code for its
own token, proving with a PKCE verifier that it is the app that asked. The
token goes in `Authorization: Bearer mn_app_...` on every API call; Profile
lists the signed-in devices so any can be signed out.
"""
from __future__ import annotations

import base64
import datetime as dt
import hashlib
import hmac
import re
import secrets
import time

from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core import crypto
from app.core.config import settings
from app.models import AppCode, AppToken, PairCode, User
from app.services import users

TOKEN_PREFIX = "mn_app_"
SCROBBLE_PREFIX = "mn_kodi_"  # TV apps: may only report what was watched
# Every lifetime and limit in this file is listed in docs/limits.md
# ("Sign-in, phones and TVs").
# The one-time code /app/connect hands the phone: valid 5 minutes, works
# once, and only with the PKCE verifier of the app that asked. Expired codes
# are deleted whenever a new one is made (make_code).
CODE_TTL = dt.timedelta(minutes=5)
FORM_TTL = 600  # seconds a connect page stays valid (10 minutes)
CHALLENGE = re.compile(r"^[A-Za-z0-9_-]{43,128}$")
# A device signs in again after DEVICE_IDLE_DAYS (90) unused, and every
# device DEVICE_MAX_DAYS (365) after it signed in: the sign-in provider never
# sees these tokens, so someone removed there (or a lost phone) doesn't keep
# access for good. An expired token is deleted when it is next used (and the
# device is told why, in plain words), or when its owner's device list is
# read (drop_expired); the list shows each device's date (expiry). Applies
# to phones (mn_app_) and TVs (mn_kodi_) alike.


def token_idle() -> dt.timedelta:
    return dt.timedelta(days=settings.device_idle_days)


def token_max_age() -> dt.timedelta:
    return dt.timedelta(days=settings.device_max_days)


def _aware(t: dt.datetime) -> dt.datetime:
    return t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)


def expiry(device: AppToken) -> tuple[dt.datetime, str]:
    """When this device's sign-in runs out, and why then: "unused" (that many
    days without being used) or "age" (that many days after it signed in)."""
    idle_end = _aware(device.last_used_at or device.created_at) + token_idle()
    age_end = _aware(device.created_at) + token_max_age()
    return (idle_end, "unused") if idle_end <= age_end else (age_end, "age")


def expired_message(device: AppToken) -> str:
    what = "This TV" if device.scope == "scrobble" else "This phone"
    if expiry(device)[1] == "unused":
        return f"{what}'s sign-in ran out: it wasn't used for {settings.device_idle_days} days. Sign in again."
    return f"{what}'s sign-in ran out: it signed in {settings.device_max_days} days ago. Sign in again."


class DeviceError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


def redirect_ok(url: str) -> bool:
    """Only back to the app itself (and Expo Go while developing)."""
    if re.match(r"^movienight://[A-Za-z0-9/_-]*$", url or ""):
        return True
    return settings.app_dev_redirects and bool(re.match(r"^exp://[A-Za-z0-9.:_-]+(/--)?/[A-Za-z0-9/_-]*$", url or ""))


def challenge_of(verifier: str) -> str:
    return base64.urlsafe_b64encode(hashlib.sha256(verifier.encode()).digest()).rstrip(b"=").decode()


def form_sig(username: str, challenge: str, redirect: str, device: str, ts: int) -> str:
    msg = f"{username}|{challenge}|{redirect}|{device}|{ts}".encode()
    return hmac.new(("movienight:connect:" + settings.secret_key).encode(), msg, hashlib.sha256).hexdigest()


def check_form(username: str, challenge: str, redirect: str, device: str, ts: int, sig: str) -> None:
    if not CHALLENGE.match(challenge or "") or not redirect_ok(redirect):
        raise DeviceError(400, "This sign-in link isn't valid")
    if abs(time.time() - ts) > FORM_TTL or not hmac.compare_digest(sig, form_sig(username, challenge, redirect, device, ts)):
        raise DeviceError(400, "This sign-in page has expired; start again from the app")


async def make_code(db: AsyncSession, user: User, challenge: str, device: str) -> str:
    code = crypto.new_token(32)
    now = dt.datetime.now(dt.timezone.utc)
    await db.execute(delete(AppCode).where(AppCode.expires_at < now))
    db.add(AppCode(code_hash=crypto.token_hash(code), user_id=user.id, challenge=challenge, device=device or "Movie Night app",
                   expires_at=now + CODE_TTL))
    await db.commit()
    return code


async def redeem(db: AsyncSession, code: str, verifier: str) -> tuple[str, User, AppToken]:
    row = await db.get(AppCode, crypto.token_hash(code))
    now = dt.datetime.now(dt.timezone.utc)
    if row is None or row.expires_at < now:
        raise DeviceError(400, "That sign-in code expired; sign in again")
    if not hmac.compare_digest(challenge_of(verifier), row.challenge):
        raise DeviceError(400, "That sign-in code was meant for another app")
    await db.delete(row)  # one use only
    token = TOKEN_PREFIX + crypto.new_token(32)
    device = AppToken(user_id=row.user_id, name=row.device, token_hash=crypto.token_hash(token))
    db.add(device)
    await db.commit()
    user = await db.get(User, row.user_id)
    return token, user, device


async def drop_expired(db: AsyncSession, user_id: int) -> None:
    """Forget this person's devices whose token can never work again (the same
    limits as user_for_token), so Signed-in devices lists only live ones."""
    now = dt.datetime.now(dt.timezone.utc)
    await db.execute(delete(AppToken).where(
        AppToken.user_id == user_id,
        (AppToken.created_at < now - token_max_age())
        | (func.coalesce(AppToken.last_used_at, AppToken.created_at) < now - token_idle())))
    await db.commit()


async def user_for_token(db: AsyncSession, token: str, scopes: tuple[str, ...] = ("app",)) -> User | None:
    """The person behind a device token, if it is one of `scopes` (the phone
    app's tokens open the whole API; Kodi's only reporting plays). An
    expired one is deleted and raises DeviceError(401) saying why."""
    if not token.startswith((TOKEN_PREFIX, SCROBBLE_PREFIX)) or len(token) > 200:
        return None
    device = await db.scalar(select(AppToken).where(AppToken.token_hash == crypto.token_hash(token)))
    if device is None or device.scope not in scopes:
        return None
    now = dt.datetime.now(dt.timezone.utc)
    if now >= expiry(device)[0]:
        message = expired_message(device)
        await db.delete(device)
        await db.commit()
        raise DeviceError(401, message)
    # "Last used" is written at most every 10 minutes, so busy devices don't
    # cost a database write per request; DEVICE_IDLE_DAYS is far longer anyway.
    if device.last_used_at is None or now - device.last_used_at > dt.timedelta(minutes=10):
        device.last_used_at = now
        await db.commit()
    user = await db.get(User, device.user_id)
    if user is not None and users.follow_admins(user):
        await db.commit()
    return user


async def sign_out_everywhere(db: AsyncSession, username: str) -> int:
    """An admin's action: every phone and TV of one person signed out (someone
    removed from the sign-in, the App Review account after review)."""
    user = await db.scalar(select(User).where(User.username == username.strip().lower()))
    if user is None:
        raise DeviceError(404, f"Nobody called {username} has used Movie Night")
    n = (await db.execute(delete(AppToken).where(AppToken.user_id == user.id))).rowcount or 0
    await db.commit()
    return n


# ---------------------------------------------------------------------------
# TV apps (Kodi): a short code on the screen, confirmed on the web
# ---------------------------------------------------------------------------

# A TV's code (8 characters, shown as ABCD-2345) lasts 10 minutes. The TV
# polls for its token (every 3 s, see scrobble.pair_start); the token is
# handed over once and then forgotten here.
PAIR_TTL = dt.timedelta(minutes=10)
PAIR_ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"  # no 0/O, 1/I/L
# Unexpired codes waiting at once, across the whole install
# (PAIR_CODES_WAITING, 200); one more gets 429 "Too many sign-ins are
# waiting". With PAIR_STARTS_PER_ADDRESS (scrobble.py) no single address can
# use them all up. A security measure, not a usage limit.


def normalize_code(code: str) -> str:
    return re.sub(r"[^A-Z0-9]", "", (code or "").upper())


def new_pair_code() -> str:
    raw = "".join(secrets.choice(PAIR_ALPHABET) for _ in range(8))
    return f"{raw[:4]}-{raw[4:]}"


async def start_pair(db: AsyncSession, device: str) -> tuple[str, str]:
    """A new code for the TV to show, and the secret it polls with."""
    now = dt.datetime.now(dt.timezone.utc)
    await db.execute(delete(PairCode).where(PairCode.expires_at < now))
    if (await db.scalar(select(func.count()).select_from(PairCode)) or 0) >= settings.pair_codes_waiting:
        raise DeviceError(429, "Too many sign-ins are waiting; try again in a few minutes")
    code, poll = new_pair_code(), crypto.new_token(32)
    db.add(PairCode(code_hash=crypto.token_hash(normalize_code(code)), poll_hash=crypto.token_hash(poll),
                    device=device.strip() or "Kodi", expires_at=now + PAIR_TTL))
    await db.commit()
    return code, poll


async def confirm_pair(db: AsyncSession, user: User, code: str) -> str:
    """The person typed the TV's code: make that device a reporting-only
    token, kept for the TV to collect. Returns the device's name."""
    row = await db.get(PairCode, crypto.token_hash(normalize_code(code)))
    if row is None or row.expires_at < dt.datetime.now(dt.timezone.utc):
        raise DeviceError(404, "That code isn't right or has run out; check the TV")
    if row.user_id is not None:
        raise DeviceError(409, "That code was already used")
    token = SCROBBLE_PREFIX + crypto.new_token(32)
    db.add(AppToken(user_id=user.id, name=row.device, token_hash=crypto.token_hash(token), scope="scrobble"))
    row.user_id = user.id
    row.token_enc = crypto.encrypt(token)
    await db.commit()
    return row.device


async def poll_pair(db: AsyncSession, poll: str) -> tuple[str, User] | None:
    """The TV asking: None while waiting; its token and person once confirmed
    (handed over once, then forgotten here)."""
    row = await db.scalar(select(PairCode).where(PairCode.poll_hash == crypto.token_hash(poll)))
    if row is None or row.expires_at < dt.datetime.now(dt.timezone.utc):
        raise DeviceError(410, "This code ran out; start again on the TV")
    if row.user_id is None or not row.token_enc:
        return None
    token = crypto.decrypt(row.token_enc)
    user = await db.get(User, row.user_id)
    await db.delete(row)
    await db.commit()
    if not token or user is None:
        raise DeviceError(410, "This code ran out; start again on the TV")
    return token, user
