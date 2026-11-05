"""Per-title state shown on every card: in the Jellyfin library, requested /
downloading in Seerr (shared by everyone on the install), and the person's
own watchlist, watches, rating and favourite.

The Seerr side comes from one cached pull of Seerr's /media table, and the
library from one cached map, so a page of 300 cards costs no per-card calls.
"""
from __future__ import annotations

import datetime as dt
from dataclasses import dataclass, field
from typing import Any

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.clients.seerr import MEDIA_AVAILABLE, MEDIA_PARTIAL, MEDIA_PENDING, MEDIA_PROCESSING
from app.core import cache
from app.core.config import settings
from app.models import Favorite, HistoryEntry, LibraryItem, UserRating, WatchlistEntry

SEERR_MAP_KEY = "seerr:media-map"
SEERR_MAP_TTL = 90.0  # seconds; a request made here refreshes it at once (force=True)
LIBRARY_KEY = "library-map"
LIBRARY_TTL = 120.0  # seconds; the library sync clears it (forget_library)

Key = tuple[str, int]


def download_progress(download_status: list[dict[str, Any]] | None) -> dict[str, Any] | None:
    """Seerr's downloadStatus (from Radarr/Sonarr's queue) -> one progress
    figure for the button: percent done and time left."""
    items = [d for d in (download_status or []) if d.get("size")]
    if not items:
        return None
    size = sum(d.get("size") or 0 for d in items)
    left = sum(d.get("sizeLeft") or 0 for d in items)
    pct = round(100 * (size - left) / size) if size else 0
    eta = min((d.get("estimatedCompletionTime") for d in items if d.get("estimatedCompletionTime")), default=None)
    return {"percent": max(0, min(100, pct)), "eta": eta, "status": items[0].get("status")}


def seerr_status_label(status: int | None) -> str | None:
    return {
        MEDIA_PENDING: "pending",
        MEDIA_PROCESSING: "processing",
        MEDIA_PARTIAL: "partial",
        MEDIA_AVAILABLE: "available",
    }.get(status or 0)


async def seerr_media_map(seerr, force: bool = False) -> dict[Key, dict[str, Any]]:
    """(media_type, tmdb_id) -> {status, jellyfin_id, progress} for every
    title Seerr knows about. Cached briefly; force=True after a request."""
    if not getattr(seerr, "enabled", True):
        return {}
    if force:
        cache.invalidate(SEERR_MAP_KEY)

    async def load():
        out: dict[Key, dict[str, Any]] = {}
        skip = 0
        while True:
            page = await seerr.media_page(take=500, skip=skip)
            results = page.get("results") or []
            for m in results:
                if not m.get("tmdbId"):
                    continue
                media = "tv" if m.get("mediaType") == "tv" else "movie"
                out[(media, int(m["tmdbId"]))] = {
                    "status": m.get("status"),
                    "jellyfin_id": m.get("jellyfinMediaId"),
                    "progress": download_progress(m.get("downloadStatus")),
                }
            skip += len(results)
            total = (page.get("pageInfo") or {}).get("results") or 0
            # Every one of Seerr's media, however many (no cap: the old 10,000
            # left later titles without a status). docs/limits.md.
            if not results or skip >= total:
                break
        return out

    try:
        return await cache.cached(SEERR_MAP_KEY, SEERR_MAP_TTL, load)
    except Exception:
        return cache.peek(SEERR_MAP_KEY) or {}


async def library_map(db: AsyncSession) -> dict[Key, str]:
    """(media_type, tmdb_id) -> Jellyfin item id for the whole library."""
    async def load():
        out: dict[Key, str] = {}
        rows = await db.execute(select(LibraryItem.media_type, LibraryItem.tmdb_id, LibraryItem.jellyfin_id)
                                .where(LibraryItem.tmdb_id.isnot(None)))
        for media, tmdb_id, jf_id in rows:
            out.setdefault((media, tmdb_id), jf_id)
        return out

    return await cache.cached(LIBRARY_KEY, LIBRARY_TTL, load)


def forget_library() -> None:
    cache.invalidate(LIBRARY_KEY)


@dataclass
class UserState:
    user_id: int
    library: dict[Key, str] = field(default_factory=dict)  # -> jellyfin id
    watchlist: set[Key] = field(default_factory=set)
    watched: dict[Key, int] = field(default_factory=dict)  # movie: plays; show: episodes seen
    ratings: dict[Key, int] = field(default_factory=dict)
    favorites: set[Key] = field(default_factory=set)
    seerr: dict[Key, dict[str, Any]] = field(default_factory=dict)


def _day(t: dt.datetime) -> str:
    return (t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)).date().isoformat()


async def load_user_state(db: AsyncSession, user_id: int, seerr_map: dict[Key, dict[str, Any]] | None = None) -> UserState:
    st = UserState(user_id=user_id, seerr=seerr_map or {}, library=await library_map(db))
    for media, tmdb_id in await db.execute(select(WatchlistEntry.media_type, WatchlistEntry.tmdb_id)
                                           .where(WatchlistEntry.user_id == user_id, WatchlistEntry.removed_at.is_(None))):
        st.watchlist.add((media, tmdb_id))
    # The same watch can arrive from two places on the same day (a manual
    # mark and a Jellyfin play): count it once.
    seen: dict[Key, set] = {}
    for media, tmdb_id, season, episode, when in await db.execute(
            select(HistoryEntry.media_type, HistoryEntry.tmdb_id, HistoryEntry.season, HistoryEntry.episode,
                   HistoryEntry.watched_at)
            .where(HistoryEntry.user_id == user_id, HistoryEntry.tmdb_id.isnot(None))):
        k = ("tv" if media == "episode" else media, tmdb_id)
        seen.setdefault(k, set()).add((season, episode) if media == "episode" else _day(when))
    st.watched = {k: len(v) for k, v in seen.items()}
    for media, tmdb_id, rating in await db.execute(select(UserRating.media_type, UserRating.tmdb_id, UserRating.rating)
                                                   .where(UserRating.user_id == user_id)):
        st.ratings[(media, tmdb_id)] = rating
    for media, tmdb_id in await db.execute(select(Favorite.media_type, Favorite.tmdb_id)
                                           .where(Favorite.user_id == user_id, Favorite.removed_at.is_(None))):
        st.favorites.add((media, tmdb_id))
    return st


def jellyfin_web_url(item_id: str | None) -> str | None:
    return f"{settings.jellyfin_public_url.rstrip('/')}/web/#/details?id={item_id}" if item_id else None


def title_state(st: UserState, media_type: str, tmdb_id: int) -> dict[str, Any]:
    k = ("tv" if media_type == "tv" else "movie", int(tmdb_id))
    s = st.seerr.get(k) or {}
    jf_id = st.library.get(k)
    status = seerr_status_label(s.get("status"))
    if not jf_id and status == "available" and s.get("jellyfin_id"):
        jf_id = s["jellyfin_id"]
    return {
        "in_library": bool(jf_id),
        "jellyfin_id": jf_id,
        "jellyfin_url": jellyfin_web_url(jf_id),
        # what the request button shows when not in the library
        "request": None if jf_id and status in (None, "available") else status,
        "progress": s.get("progress"),
        "watchlist": k in st.watchlist,
        "watched": st.watched.get(k, 0),
        "rating": st.ratings.get(k),
        "favorite": k in st.favorites,
    }
