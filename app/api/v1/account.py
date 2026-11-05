"""The person's own connections and choices: their Jellyfin account (Quick
Connect or password), players and subtitle language, the scrobble token for
other players (the Kodi add-on), and the digest emails."""
from __future__ import annotations

import logging

from fastapi import APIRouter, Depends, HTTPException, Path
from fastapi.responses import HTMLResponse
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import EMAIL, current_user, get_db
from app.clients.jellyfin import JellyfinError
from app.clients.mailer import MaybeSent
from app.core import crypto
from app.models import Digest, User
from app.services import digest, playback, users

log = logging.getLogger("movienight.account")
router = APIRouter(prefix="/me", tags=["account"])


def _jf():
    jf = clients.jellyfin()
    if not jf.enabled:
        raise HTTPException(status_code=503, detail="Jellyfin isn't set up on this install")
    return jf


async def _link(db: AsyncSession, user: User, auth: dict) -> None:
    try:
        await users.link_jellyfin(db, user, auth)
    except users.LinkError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc


# ---------------------------------------------------------------------------
# Jellyfin account
# ---------------------------------------------------------------------------


@router.get("/jellyfin", response_model=s.JellyfinLink)
async def jellyfin_status(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    from app.api.v1.profile import jellyfin_link

    out = await jellyfin_link(db, user)
    try:
        out["quick_connect"] = await _jf().quick_connect_enabled()
    except Exception:
        out["quick_connect"] = None
    return out


@router.post("/jellyfin/quick-connect", response_model=s.QuickConnectOut)
async def quick_connect_start(user: User = Depends(current_user)):
    """A code to type into a Jellyfin app where you're signed in
    (Settings → Quick Connect)."""
    try:
        got = await _jf().quick_connect_initiate(users.device_id(user))
    except JellyfinError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    return {"code": got.get("Code"), "secret": got.get("Secret")}


@router.post("/jellyfin/quick-connect/check", response_model=s.QuickConnectStateOut)
async def quick_connect_check(body: s.QuickConnectIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    jf = _jf()
    try:
        if not await jf.quick_connect_ready(body.secret, users.device_id(user)):
            return {"linked": False}
        auth = await jf.quick_connect_finish(body.secret, users.device_id(user))
    except JellyfinError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    await _link(db, user, auth)
    return {"linked": True, "user_name": user.jellyfin_user_name}


@router.post("/jellyfin/password", response_model=s.QuickConnectStateOut)
async def password_link(body: s.PasswordLinkIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Sign in to Jellyfin once; only the resulting token is kept (encrypted),
    never the password."""
    try:
        auth = await _jf().sign_in(body.username.strip(), body.password, users.device_id(user))
    except JellyfinError as exc:
        # 401 is reserved for this device's own Movie Night token; a wrong
        # Jellyfin password is just a rejected request, not a sign-out.
        raise HTTPException(status_code=422 if exc.status == 401 else 502, detail=str(exc)) from exc
    await _link(db, user, auth)
    return {"linked": True, "user_name": user.jellyfin_user_name}


@router.delete("/jellyfin", response_model=s.Ok)
async def unlink(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    token = await users.unlink_jellyfin(db, user)
    if token:
        await clients.jellyfin().sign_out(token, users.device_id(user))
    return {}


# ---------------------------------------------------------------------------
# Players
# ---------------------------------------------------------------------------


@router.get("/players", response_model=s.PlayerSettingsOut)
async def get_players(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    return await playback.player_settings(db, user)


@router.put("/players", response_model=s.PlayerSettingsOut)
async def put_players(body: s.PlayerSettingsIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    try:
        return await playback.save_player_settings(db, user, [p.model_dump() for p in body.custom], body.default_player,
                                                   body.subtitle_language, body.enabled, body.ask_finished)
    except playback.PlayError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc


# ---------------------------------------------------------------------------
# Scrobble token (Kodi add-on and other players)
# ---------------------------------------------------------------------------


@router.post("/scrobble-token", response_model=s.TokenOut)
async def new_scrobble_token(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """A new token for other players to report watches; shown once. Making a
    new one turns the old one off."""
    token = "mn_" + crypto.new_token(30)
    user.scrobble_token_hash = crypto.token_hash(token)
    await db.commit()
    return {"token": token}


@router.delete("/scrobble-token", response_model=s.Ok)
async def drop_scrobble_token(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    user.scrobble_token_hash = None
    await db.commit()
    return {}


# ---------------------------------------------------------------------------
# Digest emails
# ---------------------------------------------------------------------------


async def _email_settings(db: AsyncSession, user: User) -> dict:
    return {"weekly": await digest.wants(db, user, "week"), "monthly": await digest.wants(db, user, "month"),
            "yearly": await digest.wants(db, user, "year"), "address": await digest.recipient(db, user)}


@router.get("/emails", response_model=s.EmailSettings)
async def get_emails(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    return await _email_settings(db, user)


@router.put("/emails", response_model=s.EmailSettings)
async def put_emails(body: s.EmailSettingsIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    if body.weekly is not None:
        await users.set_setting(db, user.id, digest.SETTING["week"], body.weekly)
    if body.monthly is not None:
        await users.set_setting(db, user.id, digest.SETTING["month"], body.monthly)
    if body.yearly is not None:
        await users.set_setting(db, user.id, digest.SETTING["year"], body.yearly)
    if body.address is not None:
        address = body.address.strip()
        if address and not EMAIL.match(address):
            raise HTTPException(status_code=422, detail="That doesn't look like an email address")
        if address:
            await users.set_setting(db, user.id, digest.EMAIL_KEY, address)
        else:
            await users.delete_setting(db, user.id, digest.EMAIL_KEY)
    return await _email_settings(db, user)


def _digest_out(d: Digest) -> dict:
    return {"id": d.id, "kind": d.kind, "subject": d.subject, "period_start": d.period_start.isoformat(),
            "period_end": d.period_end.isoformat(), "model": d.model,
            "sent_at": d.sent_at.isoformat() if d.sent_at else None, "sent_to": d.sent_to}


@router.get("/digests", response_model=list[s.DigestOut])
async def list_digests(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    # All of them, newest first (none is deleted; docs/limits.md).
    return [_digest_out(d) for d in await db.scalars(select(Digest).where(Digest.user_id == user.id)
                                                     .order_by(Digest.period_start.desc()))]


@router.post("/digests/{kind}", response_model=s.DigestOut)
async def make_digest(kind: str = Path(pattern=r"^(week|month|year)$"), db: AsyncSession = Depends(get_db),
                      user: User = Depends(current_user)):
    """Last week's, month's or year's digest, made now if it doesn't exist yet."""
    return _digest_out(await digest.build(db, user, kind))


@router.get("/digests/{digest_id}/html", response_class=HTMLResponse)
async def digest_html(digest_id: int = Path(gt=0), db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    d = await db.get(Digest, digest_id)
    if d is None or d.user_id != user.id:
        raise HTTPException(status_code=404, detail="Not found")
    # The email's own inline styles; nothing else may load or run. Movie
    # Night itself may show it in a frame (the preview).
    return HTMLResponse(d.html, headers={
        "Content-Security-Policy": "default-src 'none'; img-src https://image.tmdb.org; style-src 'unsafe-inline'; "
                                   "frame-ancestors 'self'; base-uri 'none'; form-action 'none'",
        "X-Frame-Options": "SAMEORIGIN"})


@router.post("/digests/{digest_id}/send", response_model=s.SentOut)
async def send_digest(digest_id: int = Path(gt=0), db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    d = await db.get(Digest, digest_id)
    if d is None or d.user_id != user.id:
        raise HTTPException(status_code=404, detail="Not found")
    try:
        return {"sent_to": await digest.send(db, user, d)}
    except RuntimeError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc
    except MaybeSent as exc:
        # Recorded as sent (digest.send): say so, so nobody sends it twice blindly.
        raise HTTPException(status_code=502, detail="The email server stopped answering after it was handed the "
                                                    "email, so it has probably arrived: check your inbox before "
                                                    "sending it again") from exc
    except Exception as exc:
        log.warning("digest send failed", exc_info=True)
        raise HTTPException(status_code=502, detail="The email server didn't take it — try again") from exc
