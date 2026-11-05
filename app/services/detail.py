"""The title page: TMDB details (people, trailer, where to stream, seasons),
Seerr's request/download status, and the person's own state."""
from __future__ import annotations

import asyncio
import datetime as dt
from typing import Any

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.core import cache
from app.core.config import settings
from app.models import User, UserRating
from app.services import history, lists, scores, titles, watch_progress
from app.services.state import UserState, download_progress, jellyfin_web_url, seerr_status_label, title_state

DETAIL_TTL = 6 * 3600.0
SEERR_INFO_TTL = 45.0
ONGOING = {"Returning Series", "In Production", "Planned", "Pilot"}


async def details_cached(tmdb, media: str, tmdb_id: int, fresh: bool = False) -> dict[str, Any] | None:
    key = f"detail:{media}:{tmdb_id}"
    if fresh:
        cache.invalidate(key)
    return await cache.cached(key, DETAIL_TTL, lambda: tmdb.details(media, tmdb_id))


async def seerr_info(media: str, tmdb_id: int, fresh: bool = False) -> dict[str, Any] | None:
    """Seerr's live record for a title (None when Seerr is off or never
    heard of it)."""
    seerr = clients.seerr()
    if not seerr.enabled:
        return None
    key = f"seerr-info:{media}:{tmdb_id}"
    if fresh:
        cache.invalidate(key)
    try:
        return await cache.cached(key, SEERR_INFO_TTL, lambda: seerr.media_info(media, tmdb_id))
    except Exception:
        return cache.peek(key)


def _certification(d: dict[str, Any], media: str, region: str) -> str | None:
    if media == "movie":
        for r in (d.get("release_dates") or {}).get("results", []):
            if r.get("iso_3166_1") == region:
                for rd in r.get("release_dates") or []:
                    if rd.get("certification"):
                        return rd["certification"]
    else:
        for r in (d.get("content_ratings") or {}).get("results", []):
            if r.get("iso_3166_1") == region and r.get("rating"):
                return r["rating"]
    return None


def _providers(d: dict[str, Any], region: str) -> dict[str, Any]:
    wp = ((d.get("watch/providers") or {}).get("results") or {}).get(region)
    if not wp:
        return {"stream": [], "rent": [], "buy": [], "link": None}

    def pick(kind: str) -> list[dict[str, Any]]:
        return [{"id": p.get("provider_id"), "name": p.get("provider_name"), "logo": p.get("logo_path")}
                for p in sorted(wp.get(kind) or [], key=lambda p: p.get("display_priority") or 99)]

    return {"stream": pick("flatrate"), "rent": pick("rent"), "buy": pick("buy"), "link": wp.get("link")}


def _trailer(d: dict[str, Any]) -> str | None:
    vids = [v for v in (d.get("videos") or {}).get("results") or [] if v.get("site") == "YouTube" and v.get("key")]
    vids.sort(key=lambda v: (not v.get("official"), v.get("type") != "Trailer"))
    for kind in ("Trailer", "Teaser"):
        for v in vids:
            if v.get("type") == kind:
                return f"https://www.youtube.com/watch?v={v['key']}"
    return f"https://www.youtube.com/watch?v={vids[0]['key']}" if vids else None


def request_status(st: dict[str, Any], media_info: dict[str, Any] | None) -> dict[str, Any]:
    """Fold Seerr's live mediaInfo into the title state the button reads."""
    if media_info:
        status = seerr_status_label(media_info.get("status"))
        if status == "available" and media_info.get("jellyfinMediaId") and not st.get("jellyfin_id"):
            st["jellyfin_id"] = media_info["jellyfinMediaId"]
            st["jellyfin_url"] = jellyfin_web_url(st["jellyfin_id"])
            st["in_library"] = True
        st["request"] = None if st.get("in_library") and status in (None, "available") else status
        st["progress"] = download_progress(media_info.get("downloadStatus"))
        reqs = media_info.get("requests") or []
        if reqs:
            st["requested_at"] = max(r.get("createdAt") or "" for r in reqs) or None
    return st


async def community(db: AsyncSession, user: User, media: str, tmdb_id: int) -> dict[str, Any]:
    """Everyone's ratings and comments for a title on this install."""
    rows = await db.execute(select(UserRating, User.username, User.display_name).join(User, User.id == UserRating.user_id)
                            .where(UserRating.media_type == media, UserRating.tmdb_id == tmdb_id)
                            .order_by(UserRating.rated_at.desc()))
    mine, others, total, n = None, [], 0, 0
    for r, username, display in rows:
        total, n = total + r.rating, n + 1
        entry = {"name": display or username, "rating": r.rating, "review": r.review,
                 "rated_at": r.rated_at.isoformat(), "mine": r.user_id == user.id}
        if entry["mine"]:
            mine = entry
        else:
            others.append(entry)
    # Everyone on the install sees these, by display name: all of them, newest
    # first (no cap), with the average and count (docs/limits.md).
    return {"average": round(total / n, 1) if n else None, "count": n, "mine": mine, "others": others}


def _episode_ref(e: dict[str, Any] | None) -> dict[str, Any] | None:
    if not e:
        return None
    return {"season": e.get("season_number"), "episode": e.get("episode_number"), "name": e.get("name"),
            "air_date": e.get("air_date")}


async def build(db: AsyncSession, user: User, media: str, tmdb_id: int, ust: UserState) -> dict[str, Any] | None:
    d, mi = await asyncio.gather(details_cached(clients.tmdb(), media, tmdb_id), seerr_info(media, tmdb_id))
    if not d:
        return None
    meta = titles.normalize(d, media)
    await titles.upsert(db, [meta], detailed=True)
    region = settings.watch_region
    credits = d.get("credits") or {}
    # Everyone TMDB lists, in its order (no cut: the old first 20 of the cast
    # and 3 directors, writers, studios, networks and languages are gone; the
    # pages scroll the cast sideways).
    cast = [{"id": c.get("id"), "name": c.get("name"), "character": c.get("character"), "photo": c.get("profile_path")}
            for c in credits.get("cast") or []]
    crew = credits.get("crew") or []
    directors = list(dict.fromkeys(c.get("name") for c in crew if c.get("job") == "Director"))
    writers = list(dict.fromkeys(c.get("name") for c in crew if c.get("department") == "Writing"))
    seasons = []
    if media == "tv":
        directors = [c.get("name") for c in d.get("created_by") or []]
        mi_seasons = {s.get("seasonNumber"): s.get("status") for s in ((mi or {}).get("seasons") or [])}
        watched = await history.episode_watches(db, user.id, tmdb_id)
        for s in d.get("seasons") or []:
            n = s.get("season_number")
            if n is None:
                continue
            seen = sum(1 for (sn, _e) in watched if sn == n)
            seasons.append({
                "number": n, "name": s.get("name"), "episodes": s.get("episode_count"),
                "air_date": s.get("air_date"), "poster": s.get("poster_path"),
                "status": seerr_status_label(mi_seasons.get(n)), "watched": seen,
            })
        # Specials last, as Jellyfin and Wako show them.
        seasons.sort(key=lambda s: (s["number"] == 0, s["number"]))
    state = request_status(title_state(ust, media, tmdb_id), mi)
    coll = d.get("belongs_to_collection")
    return {
        **meta,
        "poster": meta.get("poster_path"),
        "backdrop": meta.get("backdrop_path"),
        "vote": round(meta["vote_average"], 1) if meta.get("vote_average") else None,
        "tagline": d.get("tagline"),
        "status_text": d.get("status"),
        "ongoing": media == "tv" and d.get("status") in ONGOING,
        "certification": _certification(d, media, region),
        "directors": directors,
        "director_label": "Created by" if media == "tv" else "Directed by",
        "writers": writers,
        "cast": cast,
        "studios": [c.get("name") for c in d.get("production_companies") or []],
        "networks": [n.get("name") for n in d.get("networks") or []],
        "languages": [l.get("english_name") or l.get("name") for l in d.get("spoken_languages") or []],
        "trailer": _trailer(d),
        "providers": _providers(d, region),
        "collection": {"id": coll.get("id"), "name": coll.get("name"), "poster": coll.get("poster_path"),
                       "backdrop": coll.get("backdrop_path")} if coll else None,
        "seasons": seasons,
        "number_of_seasons": d.get("number_of_seasons"),
        "number_of_episodes": d.get("number_of_episodes"),
        "last_episode": _episode_ref(d.get("last_episode_to_air")),
        "next_episode": _episode_ref(d.get("next_episode_to_air")),
        "imdb_url": f"https://www.imdb.com/title/{meta['imdb_id']}/" if meta.get("imdb_id") else None,
        "tmdb_url": f"https://www.themoviedb.org/{media}/{tmdb_id}",
        "jellyfin_url": state.get("jellyfin_url"),
        "state": state,
        "scores": await scores.cached_scores(db, media, tmdb_id),
        "community": await community(db, user, media, tmdb_id),
        "watches": await history.title_watches(db, user.id, media, tmdb_id),
        "lists": await lists.memberships(db, user.id, media, tmdb_id),
        "resume": await watch_progress.for_title(db, user.id, media, tmdb_id),
    }


async def live_status(db: AsyncSession, media: str, tmdb_id: int, ust: UserState) -> dict[str, Any]:
    """Fresh request/download state for the polling button."""
    mi = await seerr_info(media, tmdb_id, fresh=True)
    return request_status(title_state(ust, media, tmdb_id), mi)


def aired(date: str | None, today: dt.date | None = None) -> bool:
    if not date:
        return False
    try:
        return dt.date.fromisoformat(date[:10]) <= (today or dt.date.today())
    except ValueError:
        return False
