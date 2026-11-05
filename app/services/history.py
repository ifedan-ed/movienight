"""A person's watch history, stats and the "Rate what you just watched"
prompt.

The same watch can arrive twice (a manual mark that Jellyfin later reports
too, or older Trakt + Jellyfin rows), so display and counts merge rows for
the same title (and episode) on the same day.
"""
from __future__ import annotations

import datetime as dt
from typing import Any, Iterable

from sqlalchemy import delete, func, select, update
from sqlalchemy.ext.asyncio import AsyncSession

from app.models import HistoryEntry, UserRating, WatchlistEntry
from app.services import titles, users

RATE_DISMISSED_KEY = "rate_prompt_dismissed"


def _aware(t: dt.datetime) -> dt.datetime:
    return t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)


def _day(t: dt.datetime) -> str:
    return _aware(t).date().isoformat()


def watch_key(h: HistoryEntry) -> tuple:
    return (h.media_type, h.tmdb_id or h.title, h.season, h.episode, _day(h.watched_at))


def group_rows(rows: Iterable[HistoryEntry]) -> list[dict[str, Any]]:
    """Rows (newest first) -> one entry per (title/episode, day)."""
    out: dict[tuple, dict[str, Any]] = {}
    for h in rows:
        k = watch_key(h)
        if k not in out:
            out[k] = {
                "ids": [h.id],
                "media_type": h.media_type,
                "key": titles.tkey("tv" if h.media_type == "episode" else "movie", h.tmdb_id) if h.tmdb_id else None,
                "title": h.title,
                "season": h.season,
                "episode": h.episode,
                "episode_title": h.episode_title,
                "watched_at": _aware(h.watched_at).isoformat(),
                "sources": [h.source],
            }
        else:
            out[k]["ids"].append(h.id)
            if h.source not in out[k]["sources"]:
                out[k]["sources"].append(h.source)
    return list(out.values())


async def grouped(db: AsyncSession, user_id: int, media: str | None = None) -> list[dict[str, Any]]:
    """All of a person's watches, newest first, merged per title and day."""
    q = select(HistoryEntry).where(HistoryEntry.user_id == user_id).order_by(HistoryEntry.watched_at.desc(), HistoryEntry.id.desc())
    if media == "movie":
        q = q.where(HistoryEntry.media_type == "movie")
    elif media == "tv":
        q = q.where(HistoryEntry.media_type == "episode")
    return group_rows(await db.scalars(q))


async def title_watches(db: AsyncSession, user_id: int, media_type: str, tmdb_id: int) -> list[dict[str, Any]]:
    q = (select(HistoryEntry)
         .where(HistoryEntry.user_id == user_id, HistoryEntry.tmdb_id == tmdb_id,
                HistoryEntry.media_type == ("movie" if media_type == "movie" else "episode"))
         .order_by(HistoryEntry.watched_at.desc(), HistoryEntry.id.desc()))
    return group_rows(await db.scalars(q))


async def episode_watches(db: AsyncSession, user_id: int, show_id: int) -> dict[tuple[int, int], list[dict[str, Any]]]:
    """(season, episode) -> that episode's merged watches."""
    out: dict[tuple[int, int], list[dict[str, Any]]] = {}
    for g in await title_watches(db, user_id, "tv", show_id):
        if g["season"] is not None and g["episode"] is not None:
            out.setdefault((g["season"], g["episode"]), []).append(g)
    return out


async def mark_watched(db: AsyncSession, user_id: int, media_type: str, tmdb_id: int, title: str,
                       when: dt.datetime | None = None, season: int | None = None, episode: int | None = None,
                       episode_title: str | None = None, source: str = "manual") -> HistoryEntry:
    entry = HistoryEntry(user_id=user_id, media_type="movie" if media_type == "movie" else "episode", tmdb_id=tmdb_id,
                         season=season, episode=episode, episode_title=episode_title, title=title or "",
                         watched_at=when or dt.datetime.now(dt.timezone.utc), source=source)
    db.add(entry)
    if media_type == "movie":
        # Marked watched here (by hand, or "Finished it?"): off the watchlist,
        # as on Trakt. Plays from Jellyfin, Kodi or other players never do this.
        await db.execute(update(WatchlistEntry)
                         .where(WatchlistEntry.user_id == user_id, WatchlistEntry.media_type == "movie",
                                WatchlistEntry.tmdb_id == tmdb_id, WatchlistEntry.removed_at.is_(None))
                         .values(removed_at=dt.datetime.now(dt.timezone.utc)))
    await db.commit()
    return entry


async def remove_entries(db: AsyncSession, user_id: int, ids: list[int], media_type: str | None = None,
                         tmdb_id: int | None = None) -> int:
    """Delete a person's watches by id; with a title given, only that title's."""
    q = delete(HistoryEntry).where(HistoryEntry.user_id == user_id, HistoryEntry.id.in_(ids))
    if tmdb_id is not None:
        q = q.where(HistoryEntry.tmdb_id == tmdb_id,
                    HistoryEntry.media_type == ("movie" if media_type == "movie" else "episode"))
    n = (await db.execute(q)).rowcount or 0
    await db.commit()
    return n


async def remove_episode(db: AsyncSession, user_id: int, show_id: int, season: int, episode: int) -> list[HistoryEntry]:
    """Untick an episode: every watch of it goes. Returns the removed rows."""
    rows = list(await db.scalars(select(HistoryEntry).where(
        HistoryEntry.user_id == user_id, HistoryEntry.media_type == "episode", HistoryEntry.tmdb_id == show_id,
        HistoryEntry.season == season, HistoryEntry.episode == episode)))
    for r in rows:
        await db.delete(r)
    await db.commit()
    return rows


async def stats(db: AsyncSession, user_id: int) -> dict[str, Any]:
    rows = await grouped(db, user_id)
    movies = [r for r in rows if r["media_type"] == "movie"]
    episodes = [r for r in rows if r["media_type"] == "episode" and r["episode"] is not None]
    keys = [r["key"] for r in movies if r["key"]]
    show_keys = {r["key"] for r in episodes if r["key"]}
    metas = await titles.get_many(db, keys + list(show_keys))
    minutes = sum((metas.get(k) or {}).get("runtime") or 0 for k in keys)
    minutes += sum((metas.get(r["key"]) or {}).get("runtime") or 30 for r in episodes if r["key"])
    year = str(dt.date.today().year)
    ratings = await db.scalar(select(func.count()).select_from(UserRating).where(UserRating.user_id == user_id))
    watchlist = await db.scalar(select(func.count()).select_from(WatchlistEntry)
                                .where(WatchlistEntry.user_id == user_id, WatchlistEntry.removed_at.is_(None)))
    return {
        "movies": len({r["key"] or r["title"] for r in movies}),
        "movie_plays": len(movies),
        "episodes": len(episodes),
        "shows": len(show_keys),
        "hours": round(minutes / 60),
        "this_year": len([r for r in movies if r["watched_at"][:4] == year]),
        "ratings": ratings or 0,
        "watchlist": watchlist or 0,
    }


async def rate_prompt(db: AsyncSession, user_id: int, days: int = 21) -> dict[str, Any] | None:
    """The most recent movie watched in the last `days` days that has no
    rating and wasn't dismissed (every watch of those days is looked at, not
    only the 20 newest)."""
    dismissed = set(await users.get_setting(db, user_id, RATE_DISMISSED_KEY, []) or [])
    rated = set(await db.scalars(select(UserRating.tmdb_id).where(UserRating.user_id == user_id, UserRating.media_type == "movie")))
    since = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=days)
    for h in await db.scalars(select(HistoryEntry)
                              .where(HistoryEntry.user_id == user_id, HistoryEntry.media_type == "movie",
                                     HistoryEntry.tmdb_id.isnot(None), HistoryEntry.watched_at >= since)
                              .order_by(HistoryEntry.watched_at.desc())):
        key = titles.tkey("movie", h.tmdb_id)
        if h.tmdb_id in rated or key in dismissed:
            continue
        return {"key": key, "media_type": "movie", "tmdb_id": h.tmdb_id, "title": h.title}
    return None


async def dismiss_rate_prompt(db: AsyncSession, user_id: int, key: str) -> None:
    dismissed = list(await users.get_setting(db, user_id, RATE_DISMISSED_KEY, []) or [])
    if key not in dismissed:
        dismissed.append(key)
    # Every "Not now" is kept (no cap), so a dismissed film is never offered again.
    await users.set_setting(db, user_id, RATE_DISMISSED_KEY, dismissed)
