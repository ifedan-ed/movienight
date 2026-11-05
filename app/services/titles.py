"""TMDB title metadata, cached in the `titles` table.

Discover, search and details payloads warm the cache as they pass through
(upsert_results), so posters for watchlist, list and history rows are almost
always local. Anything still missing is fetched with ensure(); refresh_stale()
(daily job) keeps cached details from going stale.
"""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import logging
from typing import Any, Iterable

from sqlalchemy import case, func, or_, select, tuple_
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app.models import Title

log = logging.getLogger("movienight.titles")

MOVIE_GENRES = {
    28: "Action", 12: "Adventure", 16: "Animation", 35: "Comedy", 80: "Crime", 99: "Documentary",
    18: "Drama", 10751: "Family", 14: "Fantasy", 36: "History", 27: "Horror", 10402: "Music",
    9648: "Mystery", 10749: "Romance", 878: "Science Fiction", 10770: "TV Movie", 53: "Thriller",
    10752: "War", 37: "Western",
}
TV_GENRES = {
    10759: "Action & Adventure", 16: "Animation", 35: "Comedy", 80: "Crime", 99: "Documentary",
    18: "Drama", 10751: "Family", 10762: "Kids", 9648: "Mystery", 10763: "News", 10764: "Reality",
    10765: "Sci-Fi & Fantasy", 10766: "Soap", 10767: "Talk", 10768: "War & Politics", 37: "Western",
}

# How long cached details stay fresh: titles out lately change more (dates,
# posters, scores) than old ones.
FRESH_RECENT = dt.timedelta(days=3)
FRESH_OLD = dt.timedelta(days=30)


def tkey(media_type: str, tmdb_id: int) -> str:
    return f"{'tv' if media_type == 'tv' else 'movie'}:{int(tmdb_id)}"


def split_key(key: str) -> tuple[str, int]:
    media, _, tid = key.partition(":")
    return ("tv" if media == "tv" else "movie"), int(tid)


def _year(date: str | None) -> int | None:
    if date and len(date) >= 4 and date[:4].isdigit():
        return int(date[:4])
    return None


def normalize(r: dict[str, Any], media_type: str | None = None) -> dict[str, Any] | None:
    """One TMDB result (discover, search, details, credits) -> plain metadata
    dict. Returns None for people and anything without an id."""
    media = media_type or r.get("media_type")
    if media is None:
        media = "tv" if ("first_air_date" in r or ("name" in r and "title" not in r)) else "movie"
    if media not in ("movie", "tv") or not r.get("id"):
        return None
    date = (r.get("release_date") if media == "movie" else r.get("first_air_date")) or None
    if r.get("genres"):
        genre_names = [g.get("name") for g in r["genres"] if isinstance(g, dict) and g.get("name")]
    else:
        table = TV_GENRES if media == "tv" else MOVIE_GENRES
        genre_names = [table[g] for g in r.get("genre_ids") or [] if g in table]
    runtime = r.get("runtime")
    if media == "tv" and not runtime and r.get("episode_run_time"):
        runtime = (r.get("episode_run_time") or [None])[0]
    return {
        "key": tkey(media, r["id"]),
        "media_type": media,
        "tmdb_id": int(r["id"]),
        "title": (r.get("title") if media == "movie" else r.get("name")) or r.get("title") or r.get("name") or "",
        "original_title": r.get("original_title") or r.get("original_name"),
        "year": _year(date),
        "release_date": date,
        "poster_path": r.get("poster_path"),
        "backdrop_path": r.get("backdrop_path"),
        "overview": r.get("overview"),
        "genres": genre_names,
        "runtime": runtime or None,
        "vote_average": r.get("vote_average"),
        "vote_count": r.get("vote_count"),
        "imdb_id": r.get("imdb_id") or (r.get("external_ids") or {}).get("imdb_id"),
    }


def row_to_meta(t: Title) -> dict[str, Any]:
    return {
        "key": t.key,
        "media_type": t.media_type,
        "tmdb_id": t.tmdb_id,
        "title": t.title,
        "original_title": t.original_title,
        "year": t.year,
        "release_date": t.release_date,
        "poster_path": t.poster_path,
        "backdrop_path": t.backdrop_path,
        "overview": t.overview,
        "genres": json.loads(t.genres_json) if t.genres_json else [],
        "runtime": t.runtime,
        "vote_average": t.vote_average,
        "vote_count": t.vote_count,
        "imdb_id": t.imdb_id,
        "detailed": bool(t.detailed),
    }


def _row(meta: dict[str, Any], detailed: bool, now: dt.datetime) -> dict[str, Any]:
    return {
        "key": meta["key"],
        "media_type": meta["media_type"],
        "tmdb_id": meta["tmdb_id"],
        "title": meta.get("title") or "",
        "original_title": meta.get("original_title") or None,
        "year": meta.get("year"),
        "release_date": meta.get("release_date") or None,
        "poster_path": meta.get("poster_path") or None,
        "backdrop_path": meta.get("backdrop_path") or None,
        "overview": meta.get("overview") or None,
        "genres_json": json.dumps(meta["genres"]) if meta.get("genres") else None,
        "runtime": meta.get("runtime") or None,
        "vote_average": meta.get("vote_average"),
        "vote_count": meta.get("vote_count"),
        "imdb_id": meta.get("imdb_id") or None,
        "detailed": detailed,
        "fetched_at": now,
    }


async def upsert(db: AsyncSession, metas: Iterable[dict[str, Any] | None], detailed: bool = False) -> None:
    """Insert or update cached titles. Never blanks out what a richer payload
    gave before (posters, runtime, IMDb id), so a list that only knows names
    can't wipe a poster."""
    by_key: dict[str, dict[str, Any]] = {}
    for m in metas:
        if m:
            by_key[m["key"]] = m
    if not by_key:
        return
    now = dt.datetime.now(dt.timezone.utc)
    rows = [_row(m, detailed, now) for m in by_key.values()]
    for i in range(0, len(rows), 1000):
        stmt = pg_insert(Title).values(rows[i:i + 1000])
        ex = stmt.excluded
        keep = lambda col: func.coalesce(getattr(ex, col), getattr(Title, col))  # noqa: E731
        stmt = stmt.on_conflict_do_update(index_elements=[Title.key], set_={
            "title": func.coalesce(func.nullif(ex.title, ""), Title.title),
            **{c: keep(c) for c in ("original_title", "year", "release_date", "poster_path", "backdrop_path",
                                     "overview", "genres_json", "runtime", "vote_average", "vote_count", "imdb_id")},
            "detailed": or_(Title.detailed, ex.detailed),
            "fetched_at": case((ex.detailed, ex.fetched_at), else_=Title.fetched_at),
        })
        await db.execute(stmt)
    await db.commit()


async def upsert_results(db: AsyncSession, results: Iterable[dict[str, Any]], media_type: str | None = None) -> list[dict[str, Any]]:
    """Normalize a TMDB result list, cache it, and return the metas in order
    (people and malformed entries dropped)."""
    metas = [m for m in (normalize(r, media_type) for r in results) if m]
    try:
        await upsert(db, metas)
    except Exception:  # cache warming must never break a page
        await db.rollback()
        log.exception("title cache upsert failed")
    return metas


async def get_many(db: AsyncSession, keys: Iterable[str]) -> dict[str, dict[str, Any]]:
    keys = list(dict.fromkeys(keys))
    out: dict[str, dict[str, Any]] = {}
    for i in range(0, len(keys), 1000):
        for t in await db.scalars(select(Title).where(Title.key.in_(keys[i:i + 1000]))):
            out[t.key] = row_to_meta(t)
    return out


async def fetch_basic(tmdb, keys: list[str], concurrency: int = 8) -> dict[str, dict[str, Any] | None | bool]:
    """TMDB details for each key: a payload, None (TMDB has no such title) or
    False (TMDB trouble, try again later). Network only, no database."""
    sem = asyncio.Semaphore(concurrency)

    async def one(k: str):
        media, tid = split_key(k)
        async with sem:
            try:
                return await tmdb.basic(media, tid)
            except Exception:
                return False

    return dict(zip(keys, await asyncio.gather(*(one(k) for k in keys))))


async def ensure(db: AsyncSession, tmdb, keys: Iterable[str], max_fetch: int = 80) -> dict[str, dict[str, Any]]:
    """Metadata for every key, fetching up to max_fetch missing ones from
    TMDB in parallel. Keys that still have no metadata are left out."""
    keys = list(dict.fromkeys(keys))
    have = await get_many(db, keys)
    # Fetch what we have never seen, and seeded rows (a list gives names
    # only) that still lack a poster and were never fetched in full.
    missing = [k for k in keys if k not in have or (not have[k].get("poster_path") and not have[k].get("detailed"))][:max_fetch]
    if not missing:
        return have
    got = await fetch_basic(tmdb, missing)
    metas = []
    for k, payload in got.items():
        if payload:
            metas.append(normalize(payload, split_key(k)[0]))
        elif payload is None:
            # TMDB has no such title: remember that, so it is not fetched
            # again on every page view.
            media, tid = split_key(k)
            metas.append({"key": k, "media_type": media, "tmdb_id": tid, "title": (have.get(k) or {}).get("title") or ""})
    if metas:
        await upsert(db, metas, detailed=True)
        have.update(await get_many(db, [m["key"] for m in metas if m]))
    return have


async def refresh_stale(db: AsyncSession, tmdb, batch: int = 400) -> tuple[int, int]:
    """Re-fetch every cached title whose details have gone stale, oldest
    first, `batch` at a time, until none is left: no nightly cap (the old 400
    a night is gone). Returns (refreshed, not fetched): a title TMDB couldn't
    send (trouble, or it no longer has it) stays as it was and is tried at
    the next run."""
    # "Released lately" means within 120 days: those go stale after
    # FRESH_RECENT (3 days), the rest after FRESH_OLD (30). See docs/limits.md
    # (Background jobs).
    now = dt.datetime.now(dt.timezone.utc)
    recent_since = (now - dt.timedelta(days=120)).date().isoformat()
    stale = (select(Title.key, Title.fetched_at)
             .where(Title.detailed.is_(True))
             .where(or_(Title.fetched_at < now - FRESH_OLD,
                        (Title.release_date >= recent_since) & (Title.fetched_at < now - FRESH_RECENT)))
             .order_by(Title.fetched_at, Title.key).limit(batch))
    refreshed = missed = 0
    after = None  # (fetched_at, key) of the last title looked at: a refreshed one is fresh, a failed one is passed by
    while True:
        rows = list(await db.execute(stale if after is None else stale.where(tuple_(Title.fetched_at, Title.key) > after)))
        if not rows:
            return refreshed, missed
        got = await fetch_basic(tmdb, [k for k, _ in rows])
        metas = [normalize(p, split_key(k)[0]) for k, p in got.items() if p]
        await upsert(db, metas, detailed=True)
        refreshed += len(metas)
        missed += len(rows) - len(metas)
        after = (rows[-1][1], rows[-1][0])
