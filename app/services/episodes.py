"""Seasons and episodes of a show for one person: TMDB's episode list, which
ones they have seen (ticks), liked (hearts) and rated, what is in the Jellyfin
library, and the next episode. Ticking one also marks it played in their
Jellyfin (and unticking unplays it), so Jellyfin's Next Up agrees."""
from __future__ import annotations

import asyncio
import datetime as dt
import logging
from typing import Any

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.core import cache
from app.core.config import settings
from app.models import EpisodeFavorite, EpisodeRating, User
from app.services import history, titles
from app.services.detail import aired, details_cached
from app.services.state import library_map

log = logging.getLogger("movienight.episodes")

SEASON_TTL = 6 * 3600.0


async def season_cached(show_id: int, number: int) -> dict[str, Any] | None:
    tmdb = clients.tmdb()
    return await cache.cached(f"season:{show_id}:{number}", SEASON_TTL, lambda: tmdb.season(show_id, number))


def library_user(user: User) -> str | None:
    """Whose Jellyfin view to read: the person's own, else the install owner's."""
    return user.jellyfin_user_id or settings.jellyfin_user_id or None


async def jellyfin_episodes(db: AsyncSession, user: User, show_id: int, season: int | None) -> dict[tuple[int, int], dict[str, Any]]:
    """(season, episode) -> Jellyfin episode item, for a show in the library."""
    series_id = (await library_map(db)).get(("tv", show_id))
    uid = library_user(user)
    if not series_id or not uid:
        return {}
    jf = clients.jellyfin()
    try:
        items = await cache.cached(f"jf:eps:{uid}:{series_id}:{season}", 120.0, lambda: jf.episodes(series_id, uid, season))
    except Exception:
        log.warning("Jellyfin episodes for %s failed", series_id, exc_info=True)
        return {}
    out = {}
    for it in items:
        s, e = it.get("ParentIndexNumber"), it.get("IndexNumber")
        if s is not None and e is not None and it.get("LocationType") != "Virtual":
            out[(s, e)] = it
    return out


async def season_page(db: AsyncSession, user: User, show_id: int, number: int) -> dict[str, Any] | None:
    data, jf_eps = await asyncio.gather(season_cached(show_id, number), jellyfin_episodes(db, user, show_id, number))
    if not data:
        return None
    watched = await history.episode_watches(db, user.id, show_id)
    liked = set(await db.scalars(select(EpisodeFavorite.episode).where(
        EpisodeFavorite.user_id == user.id, EpisodeFavorite.tmdb_id == show_id, EpisodeFavorite.season == number)))
    rated = dict((await db.execute(select(EpisodeRating.episode, EpisodeRating.rating).where(
        EpisodeRating.user_id == user.id, EpisodeRating.tmdb_id == show_id, EpisodeRating.season == number))).all())
    today = dt.date.today()
    episodes = []
    for e in data.get("episodes") or []:
        n = e.get("episode_number")
        seen = watched.get((number, n), [])
        jf = jf_eps.get((number, n))
        episodes.append({
            "number": n,
            "name": e.get("name") or f"Episode {n}",
            "overview": e.get("overview") or None,
            "air_date": e.get("air_date"),
            "aired": aired(e.get("air_date"), today),
            "still": e.get("still_path"),
            "runtime": e.get("runtime"),
            "vote": round(e["vote_average"], 1) if e.get("vote_average") else None,
            "watched": len(seen),
            "watch_ids": [i for g in seen for i in g["ids"]],
            "last_watched_at": seen[0]["watched_at"] if seen else None,
            "in_library": jf is not None,
            "jellyfin_id": jf.get("Id") if jf else None,
            "favorite": n in liked,
            "rating": rated.get(n),
        })
    return {"show_id": show_id, "number": number, "name": data.get("name"), "overview": data.get("overview") or None,
            "air_date": data.get("air_date"), "poster": data.get("poster_path"), "episodes": episodes}


async def marks(db: AsyncSession, user_id: int, eps: list[tuple[int, int, int]]) -> dict[tuple[int, int, int], dict[str, Any]]:
    """(show tmdb_id, season, episode) -> that person's own heart and rating
    of the episode: {"favorite": bool, "rating": int | None}. For the places
    that show an episode on its own (Recently watched, History, Up next), so
    they show the episode's, not the show's."""
    want = set(eps)
    out = {k: {"favorite": False, "rating": None} for k in want}
    shows = {t for t, _, _ in want}
    if not shows:
        return out
    for t, s, e in await db.execute(select(EpisodeFavorite.tmdb_id, EpisodeFavorite.season, EpisodeFavorite.episode).where(
            EpisodeFavorite.user_id == user_id, EpisodeFavorite.tmdb_id.in_(shows))):
        if (t, s, e) in want:
            out[(t, s, e)]["favorite"] = True
    for t, s, e, r in await db.execute(select(EpisodeRating.tmdb_id, EpisodeRating.season, EpisodeRating.episode, EpisodeRating.rating).where(
            EpisodeRating.user_id == user_id, EpisodeRating.tmdb_id.in_(shows))):
        if (t, s, e) in want:
            out[(t, s, e)]["rating"] = r
    return out


async def next_episode(db: AsyncSession, user: User, show_id: int) -> dict[str, Any] | None:
    """The episode to play next: after the last one they ticked, or S1E1.
    None when they are caught up with what has aired."""
    d = await details_cached(clients.tmdb(), "tv", show_id)
    if not d:
        return None
    counts = {s["season_number"]: s.get("episode_count") or 0 for s in d.get("seasons") or [] if s.get("season_number")}
    if not counts:
        return None
    last = d.get("last_episode_to_air") or {}
    last_aired = (last.get("season_number") or 0, last.get("episode_number") or 0)
    watched = await history.episode_watches(db, user.id, show_id)
    regular = [k for k in watched if k[0] >= 1]
    if regular:
        s, e = max(regular)
        s, e = (s, e + 1) if e + 1 <= counts.get(s, 0) else (s + 1, 1)
        while s in counts and counts[s] == 0:
            s += 1
    else:
        s, e = min(counts), 1
    if s not in counts or (s, e) > last_aired:
        return None
    started = bool(regular)
    return {"season": s, "episode": e, "label": f"{'Continue' if started else 'Play'} S{s:02d}E{e:02d}"}


async def _jellyfin_marks(user: User, items: list[dict[str, Any]], played: bool, when: dt.datetime | None) -> None:
    jf = clients.jellyfin()
    for it in items:
        try:
            if played:
                await jf.mark_played(user.jellyfin_user_id, it["Id"], when)
            else:
                await jf.mark_unplayed(user.jellyfin_user_id, it["Id"])
        except Exception:
            log.warning("Jellyfin %s for %s failed", "played" if played else "unplayed", it.get("Id"), exc_info=True)
    cache.invalidate(f"jf:continue:{user.id}")


async def set_watched(db: AsyncSession, user: User, show_id: int, eps: list[tuple[int, int]], watched: bool,
                      when: dt.datetime | None = None) -> int:
    """Tick (watched=True) or untick episodes. Returns how many changed."""
    eps = list(dict.fromkeys(eps))
    done = await history.episode_watches(db, user.id, show_id)
    meta = (await titles.ensure(db, clients.tmdb(), [titles.tkey("tv", show_id)], max_fetch=1)).get(titles.tkey("tv", show_id)) or {}
    names: dict[tuple[int, int], str] = {}
    for season in {s for s, _ in eps}:
        data = await season_cached(show_id, season)
        for e in (data or {}).get("episodes") or []:
            names[(season, e.get("episode_number"))] = e.get("name")
    changed = []
    for s, e in eps:
        if watched and (s, e) not in done:
            await history.mark_watched(db, user.id, "tv", show_id, meta.get("title") or "", when, season=s, episode=e,
                                       episode_title=names.get((s, e)))
            changed.append((s, e))
        elif not watched and (s, e) in done:
            await history.remove_episode(db, user.id, show_id, s, e)
            changed.append((s, e))
    if changed and user.jellyfin_user_id:
        jf_items = []
        for season in {s for s, _ in changed}:
            got = await jellyfin_episodes(db, user, show_id, season)
            jf_items += [got[k] for k in changed if k in got]
        if jf_items:
            asyncio.create_task(_jellyfin_marks(user, jf_items, watched, when))
    return len(changed)
