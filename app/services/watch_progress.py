"""Watch progress: where each person is in a film or episode they started
and haven't finished, from the Movie Night plugin for Jellyfin (so any app
that plays through Jellyfin counts, Kodi's Jellyfin add-on included). One
row per person and film/episode; cleared when it is finished."""
from __future__ import annotations

import datetime as dt
from typing import Any

from sqlalchemy import delete, select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app.models import WatchProgress

# Past this share of the runtime a stop counts as finished.
DONE_SHARE = 0.9


def key_for(media_type: str, tmdb_id: int, season: int | None, episode: int | None) -> str:
    return f"movie:{tmdb_id}" if media_type == "movie" else f"tv:{tmdb_id}:{season or 0}:{episode or 0}"


async def save(db: AsyncSession, user_id: int, entry: dict[str, Any], position_s: int, duration_s: int | None,
               state: str, client: str | None, device: str | None, jellyfin_item_id: str | None) -> None:
    values = dict(user_id=user_id, key=key_for(entry["media_type"], entry["tmdb_id"], entry["season"], entry["episode"]),
                  media_type=entry["media_type"], tmdb_id=entry["tmdb_id"], season=entry["season"], episode=entry["episode"],
                  title=entry.get("title") or "", episode_title=(entry.get("episode_title") or None),
                  position_s=max(0, int(position_s)), duration_s=duration_s, state=state,
                  client=client or None, device=device or None,
                  jellyfin_item_id=jellyfin_item_id, updated_at=dt.datetime.now(dt.timezone.utc))
    stmt = pg_insert(WatchProgress).values(**values)
    update = {k: v for k, v in values.items() if k not in ("user_id", "key")}
    await db.execute(stmt.on_conflict_do_update(constraint="uq_watch_progress", set_=update))
    await db.commit()


async def clear(db: AsyncSession, user_id: int, entry: dict[str, Any]) -> None:
    key = key_for(entry["media_type"], entry["tmdb_id"], entry["season"], entry["episode"])
    await db.execute(delete(WatchProgress).where(WatchProgress.user_id == user_id, WatchProgress.key == key))
    await db.commit()


def finished(position_s: int | None, duration_s: int | None) -> bool:
    return bool(position_s and duration_s and position_s >= DONE_SHARE * duration_s)


async def for_title(db: AsyncSession, user_id: int, media: str, tmdb_id: int) -> dict[str, Any] | None:
    """Where this person is in a film, or in the show's latest started episode."""
    q = select(WatchProgress).where(WatchProgress.user_id == user_id)
    if media == "movie":
        q = q.where(WatchProgress.key == f"movie:{tmdb_id}")
    else:
        q = q.where(WatchProgress.media_type == "episode", WatchProgress.tmdb_id == tmdb_id)
    row = await db.scalar(q.order_by(WatchProgress.updated_at.desc()).limit(1))
    if row is None:
        return None
    percent = round(100 * row.position_s / row.duration_s) if row.duration_s else None
    return {"season": row.season, "episode": row.episode, "episode_title": row.episode_title,
            "position_s": row.position_s, "duration_s": row.duration_s, "percent": percent, "state": row.state,
            "client": row.client, "device": row.device, "updated_at": row.updated_at.isoformat()}
