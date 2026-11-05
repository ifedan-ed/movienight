"""Scrobbling: other apps telling Movie Night what was watched.

- POST /scrobble: a finished watch from another player (the Kodi add-on, a
  phone shortcut), sent with the person's own scrobble token.
- POST /scrobble/pair, /scrobble/pair/poll: Kodi signing in with a code the
  person confirms on the web; it gets a token that can only report plays.
- POST /scrobble/jellyfin: the Jellyfin Webhook plugin, sent with the
  install's webhook secret; it names the Jellyfin user, and Movie Night
  pulls that person's plays at once instead of waiting for the next sync.

These are called server-to-server, so the token is the only key (the app
believes no sign-in header). Tokens go in a header, never the URL.
"""
from __future__ import annotations

import asyncio
import datetime as dt
import ipaddress
import json
import logging
import time
from typing import Any

from fastapi import APIRouter, Depends, HTTPException, Request
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import get_db
from app.clients.jellyfin import IncompleteRead
from app.core import cache, crypto, logs, security
from app.core.config import settings
from app.core.db import session
from app.models import HistoryEntry, LibraryItem, Title, User
from app.services import devices, sync, titles, watch_progress

log = logging.getLogger("movienight.scrobble")
router = APIRouter(prefix="/scrobble", tags=["scrobble"])


def _token(request: Request) -> str:
    auth = request.headers.get("authorization", "")
    got = auth[7:].strip() if auth.lower().startswith("bearer ") else request.headers.get("x-movienight-token", "").strip()
    if not got or len(got) > 200:
        raise HTTPException(status_code=401, detail="Bad or missing token")
    return got


async def token_user(request: Request, db: AsyncSession = Depends(get_db)) -> User:
    """The person's own scrobble token, or a device token: Kodi's
    (reporting only) or the phone app's."""
    token = _token(request)
    user = await db.scalar(select(User).where(User.scrobble_token_hash == crypto.token_hash(token)))
    if user is None:
        try:
            user = await devices.user_for_token(db, token, scopes=("scrobble", "app"))
        except devices.DeviceError as exc:  # its sign-in ran out: say why
            raise HTTPException(status_code=401, detail=str(exc)) from exc
    if user is None:
        # Said on the TV (the Kodi add-on shows the server's words).
        raise HTTPException(status_code=401, detail="This device was signed out of Movie Night; sign in again")
    logs.set_user(user.id)
    return user


# ---------------------------------------------------------------------------
# Signing in a TV app: it shows a code, the person confirms it on the web
# (POST /api/v1/me/devices/pair), and the app collects its token here.
# ---------------------------------------------------------------------------

# Each address may start a few sign-ins while a code lasts (a TV or two and a
# retry), so no one caller can use up the waiting codes and block every TV:
# at most PAIR_STARTS_PER_ADDRESS (10) per address (an IPv6 /64 counts as
# one) in any 10 minutes (devices.PAIR_TTL), then 429. A security measure,
# not a usage limit. Kept in this process's memory, so a restart forgets it.
# Listed in docs/limits.md ("Technical limits").
_pair_starts: dict[str, list[float]] = {}


def _client(request: Request) -> str:
    """Who is asking: the connection's own address (an IPv6 address counts by
    its /64). X-Forwarded-For is never believed: any caller reaching the app
    directly (another container on a shared Docker network) can write it,
    and a new one on every request would be a new address each time, never
    limited. The forward-auth proxy that once justified trusting it from the
    proxy's address is gone with the native-OIDC switch (2026-10-16)."""
    ip = request.client.host if request.client else ""
    try:
        addr = ipaddress.ip_address(ip)
    except ValueError:
        return ip
    return str(ipaddress.ip_network(f"{addr}/64", strict=False)) if addr.version == 6 else str(addr)


@router.post("/pair", response_model=s.PairStartOut)
async def pair_start(body: s.PairStartIn, request: Request, db: AsyncSession = Depends(get_db)):
    now, window = time.monotonic(), devices.PAIR_TTL.total_seconds()
    for k in [k for k, starts in _pair_starts.items() if now - starts[-1] >= window]:
        del _pair_starts[k]
    who = _client(request)
    recent = [t for t in _pair_starts.get(who, []) if now - t < window]
    if len(recent) >= settings.pair_starts_per_address:
        raise HTTPException(status_code=429, detail="Too many sign-ins from here; try again in a few minutes")
    _pair_starts[who] = recent + [now]
    try:
        code, poll = await devices.start_pair(db, body.device)
    except devices.DeviceError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    # The TV polls /pair/poll every `interval` seconds until the code runs out.
    return {"code": code, "poll": poll, "expires_in": int(devices.PAIR_TTL.total_seconds()), "interval": 3}


@router.post("/pair/poll", response_model=s.PairPollOut)
async def pair_poll(body: s.PairPollIn, db: AsyncSession = Depends(get_db)):
    try:
        got = await devices.poll_pair(db, body.poll)
    except devices.DeviceError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    if got is None:
        return {"status": "waiting"}
    token, user = got
    return {"status": "ok", "token": token, "user": user.display_name or user.username, "account": user.username}


def _when(value: str | None) -> dt.datetime:
    if not value:
        return dt.datetime.now(dt.timezone.utc)
    try:
        when = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise HTTPException(status_code=422, detail="watched_at must be ISO 8601") from exc
    when = when if when.tzinfo else when.replace(tzinfo=dt.timezone.utc)
    # A day of slack for a sender's clock or time zone; later is refused.
    if when > dt.datetime.now(dt.timezone.utc) + dt.timedelta(days=1):
        raise HTTPException(status_code=422, detail="watched_at is in the future")
    return when


def _ref(ref: str) -> str:
    """The reference kept with the watch (history.source_ref, 100 characters,
    part of a unique index): as sent when it fits, else its sha256, so a
    longer one (up to REPORTED_MAX_CHARS, or a title in the made-up one) is
    still recorded once and never cut."""
    return ref if len(ref) <= 100 else "sha256:" + crypto.token_hash(ref)


@router.post("", response_model=s.ScrobbleOut)
async def scrobble(body: s.ScrobbleIn, db: AsyncSession = Depends(get_db), user: User = Depends(token_user)):
    tmdb_id = body.tmdb_id
    if tmdb_id is None and body.imdb_id:
        hit = await db.scalar(select(Title).where(Title.imdb_id == body.imdb_id).limit(1))
        if hit is None:
            found = await clients.tmdb().find_imdb(body.imdb_id)
            if found:
                tmdb_id = int(found["id"])
        else:
            tmdb_id = hit.tmdb_id
    if tmdb_id is None and body.tvdb_id and body.media_type == "episode":
        found = await clients.tmdb().find_external(str(body.tvdb_id), "tvdb_id")
        if found and found.get("media_type") == "tv":
            tmdb_id = int(found["id"])
    if tmdb_id is None and not body.title:
        raise HTTPException(status_code=422, detail="Send tmdb_id, imdb_id or tvdb_id, or at least a title")
    when = _when(body.watched_at)
    media = "movie" if body.media_type == "movie" else "tv"
    ref = _ref(body.ref or f"{body.media_type}:{tmdb_id or body.title}:{body.season}:{body.episode}:{when.date().isoformat()}")
    if await db.scalar(select(HistoryEntry.id).where(HistoryEntry.user_id == user.id, HistoryEntry.source == body.source,
                                                     HistoryEntry.source_ref == ref)):
        return {"recorded": False, "detail": "already recorded"}
    title = body.title or ""
    if tmdb_id:
        key = titles.tkey(media, tmdb_id)
        meta = (await titles.ensure(db, clients.tmdb(), [key], max_fetch=1)).get(key)
        title = (meta or {}).get("title") or title
    db.add(HistoryEntry(user_id=user.id, media_type=body.media_type, tmdb_id=tmdb_id, season=body.season, episode=body.episode,
                        title=title, watched_at=when, source=body.source, source_ref=ref))
    await db.commit()
    log.info("scrobble from %s for user %d recorded", body.source, user.id)  # never the title (docs/limits.md, "Logs")
    return {"recorded": True, "detail": title or None}


# ---------------------------------------------------------------------------
# Jellyfin Webhook plugin
# ---------------------------------------------------------------------------

_pending: dict[int, asyncio.Task] = {}
JELLYFIN_EVENTS = {"PlaybackStop", "UserDataSaved", "ItemMarkedPlayed", "MarkPlayed"}


async def _sync_soon(user_id: int) -> None:
    """Pull one person's Jellyfin plays shortly (several events for one watch
    arrive together, so wait a few seconds and sync once)."""
    await asyncio.sleep(5)  # seconds; events arriving meanwhile join this one sync
    try:
        async with session() as db:
            user = await db.get(User, user_id)
            if user is not None:
                added = await sync.sync_plays(db, clients.jellyfin(), user)
                if added:
                    log.info("jellyfin webhook: %d new plays for user %d", added, user.id)
    except IncompleteRead as exc:
        # One line, no traceback; kept for admins and the person (sync_plays).
        log.warning("webhook plays sync for user %d: nothing added: %s", user_id, exc)
    except Exception:
        log.exception("webhook plays sync failed")
    finally:
        _pending.pop(user_id, None)


@router.post("/jellyfin", status_code=202, response_model=s.Ok)
async def jellyfin_webhook(request: Request, db: AsyncSession = Depends(get_db)):
    secret = settings.jellyfin_webhook_secret
    if not secret:
        raise HTTPException(status_code=503, detail="The Jellyfin webhook isn't set up (JELLYFIN_WEBHOOK_SECRET)")
    if not crypto.same(_token(request), secret):
        raise HTTPException(status_code=401, detail="Bad or missing token")
    # The plugin's content type depends on its version; read the body as JSON either way.
    raw = await request.body()
    try:
        payload: dict[str, Any] = json.loads(raw or b"{}")
    except ValueError as exc:
        raise HTTPException(status_code=400, detail="Body must be JSON") from exc
    if not isinstance(payload, dict):
        raise HTTPException(status_code=400, detail="Body must be a JSON object")
    event = str(payload.get("NotificationType") or payload.get("Event") or "")
    jf_user = str(payload.get("UserId") or "").replace("-", "").lower()
    if event and event not in JELLYFIN_EVENTS:
        return {}
    if not jf_user:
        return {}
    user = await db.scalar(select(User).where(User.jellyfin_user_id == jf_user))
    if user is not None and user.id not in _pending:
        _pending[user.id] = asyncio.create_task(_sync_soon(user.id))
    return {}


# ---------------------------------------------------------------------------
# The Movie Night plugin for Jellyfin (jellyfin-plugin/ in this repository)
# ---------------------------------------------------------------------------


def _int(value: str | None) -> int | None:
    return int(value) if value and str(value).isdigit() else None


async def _tmdb_by_imdb(db: AsyncSession, imdb_id: str | None, media: str) -> int | None:
    if not imdb_id:
        return None
    hit = await db.scalar(select(Title.tmdb_id).where(Title.imdb_id == imdb_id, Title.media_type == media).limit(1))
    if hit:
        return hit
    found = await clients.tmdb().find_imdb(imdb_id)
    return int(found["id"]) if found and found.get("media_type") == media else None


async def _plugin_entry(db: AsyncSession, it: s.JellyfinPluginItem) -> dict[str, Any] | None:
    """The film, or the show plus season and episode, by TMDB id (from the
    item's own ids, the library, or IMDb/TheTVDB through TMDB)."""
    if it.type == "Movie":
        tmdb_id = _int(it.tmdb) or await _tmdb_by_imdb(db, it.imdb, "movie")
        if not tmdb_id:
            return None
        return dict(media_type="movie", tmdb_id=tmdb_id, season=None, episode=None, episode_title=None, title=it.name)
    tmdb_id = _int(it.series_tmdb)
    if not tmdb_id and it.series_id:
        tmdb_id = await db.scalar(select(LibraryItem.tmdb_id).where(
            LibraryItem.jellyfin_id == it.series_id.replace("-", "").lower()))
    if not tmdb_id:
        tmdb_id = await _tmdb_by_imdb(db, it.series_imdb, "tv")
    if not tmdb_id and it.series_tvdb:
        found = await clients.tmdb().find_external(it.series_tvdb, "tvdb_id")
        tmdb_id = int(found["id"]) if found and found.get("media_type") == "tv" else None
    if not tmdb_id:
        return None
    return dict(media_type="episode", tmdb_id=tmdb_id, season=it.season, episode=it.episode, episode_title=it.name,
                title=it.series_name or "")


@router.post("/jellyfin/events", status_code=202, response_model=s.Ok)
async def jellyfin_plugin_event(body: s.JellyfinPluginEvent, request: Request, db: AsyncSession = Depends(get_db)):
    """Events from the Movie Night plugin for Jellyfin, for every app that
    plays through Jellyfin: playback start, progress, pause and stop keep
    watch progress; played and unplayed marks keep history. Same shared
    secret as the webhook (X-MovieNight-Token)."""
    secret = settings.jellyfin_webhook_secret
    if not secret:
        raise HTTPException(status_code=503, detail="The Jellyfin webhook isn't set up (JELLYFIN_WEBHOOK_SECRET)")
    if not crypto.same(_token(request), secret):
        raise HTTPException(status_code=401, detail="Bad or missing token")
    user = await db.scalar(select(User).where(User.jellyfin_user_id == body.user_id.replace("-", "").lower()))
    if user is None:
        return {}
    entry = await _plugin_entry(db, body.item)
    if entry is None:
        log.info("plugin event for user %d: no TMDB id for Jellyfin item %s", user.id, body.item.id)
        return {}
    item_id = body.item.id.replace("-", "").lower()
    duration = body.item.runtime_s
    if body.event in ("start", "progress", "pause"):
        await watch_progress.save(db, user.id, entry, body.position_s or 0, duration,
                                  "paused" if body.event == "pause" else "playing", body.client, body.device, item_id)
    elif body.event == "stop":
        if body.played_to_completion or watch_progress.finished(body.position_s, duration):
            await watch_progress.clear(db, user.id, entry)
        else:
            await watch_progress.save(db, user.id, entry, body.position_s or 0, duration, "stopped",
                                      body.client, body.device, item_id)
    elif body.event == "played":
        await watch_progress.clear(db, user.id, entry)
        when = sync.parse_time(body.last_played) or dt.datetime.now(dt.timezone.utc)
        ref = sync.play_ref(item_id, when)
        if not await db.scalar(select(HistoryEntry.id).where(HistoryEntry.user_id == user.id, HistoryEntry.source == "jellyfin",
                                                             HistoryEntry.source_ref == ref)):
            if sync.place_play(db, user.id, when, ref, entry, await sync.unclaimed_watches(db, user.id)):
                log.info("jellyfin plugin: a play recorded for user %d", user.id)
            await db.commit()
    else:  # unplayed: the history stays, the progress goes
        await watch_progress.clear(db, user.id, entry)
    cache.invalidate(f"jf:continue:{user.id}")
    return {}
