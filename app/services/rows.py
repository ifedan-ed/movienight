"""Loaders for every Home row and for the "See all" grids behind them.

A row key plus the tab decides what loads (see home_config). TMDB calls are
cached in-process; personal rows come straight from the person's records.
"""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import logging
import random
from typing import Any
from zoneinfo import ZoneInfo

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.core import cache
from app.core.config import settings
from app.models import AIRowCache, Favorite, HistoryEntry, LibraryItem, User, UserRating, WatchlistEntry
from app.services import home_config, lists as lists_svc, recommend, titles
from app.services.cards import cards
from app.services.state import UserState

log = logging.getLogger("movienight.rows")

# Sizes, cache times (seconds) and the rotation below are listed in docs/limits.md.
ROW_SIZE = 24  # titles in one Home row
PAGE_SIZE = 40  # titles per page of a "See all" grid (and of a list's page)
DISCOVER_TTL = 1800.0  # seconds (30 min) a TMDB discover/trending/upcoming page is cached
RECS_TTL = 12 * 3600.0  # seconds (12 h) a title's TMDB recommendations are cached
# TMDB serves at most 500 pages of one list (an outside limit Movie Night
# can't raise). A grid that reaches it ends there and says so (TMDB_END).
TMDB_LAST_PAGE = 500
TMDB_END = ("TMDB lists only the first 10,000 titles here (its 500 pages). Narrow it down (a genre, a year, a "
            "streaming service) to see others.")
TMDB_KINDS = ("trending", "popular", "upcoming", "top_rated", "provider", "genre", "recs")


def tmdb_more(results: list, page: int) -> bool:
    """More pages of a TMDB list: it sent some, and this isn't its last page."""
    return bool(results) and page < TMDB_LAST_PAGE


def end_note(page: int, items: list, more: bool) -> str | None:
    """TMDB_END on the last page TMDB serves, when a TMDB grid stops there."""
    return TMDB_END if page >= TMDB_LAST_PAGE and items and not more else None


def tab_media(tab: str) -> str | None:
    return home_config.TAB_MEDIA.get(tab)


class TMDBDown(Exception):
    """TMDB couldn't send a page of one of its lists and no copy of it is
    kept. A grid (a "See all", Discover, a smart list) answers 502 with
    TMDB_DOWN, which the website and the phone show where the next cards
    would be, and a Home row says TMDB_DOWN_ROW under its title, instead of
    ending as if the list ended there (docs/limits.md, Caches)."""


TMDB_DOWN = "TMDB didn't answer just now, so this couldn't be loaded. Try again in a moment."
TMDB_DOWN_ROW = "TMDB didn't answer just now; this row comes back when it does."


async def tmdb_list(key: str, fn) -> list[dict[str, Any]]:
    """A cached TMDB result page; on trouble, a copy still cached, else
    TMDBDown (never an empty page that would end a grid without a word)."""
    try:
        return await cache.cached(key, DISCOVER_TTL, fn)
    except Exception as exc:
        log.warning("upstream row %s failed", key, exc_info=True)
        copy = cache.peek(key)
        if copy is None:
            raise TMDBDown(TMDB_DOWN) from exc
        return copy


async def discover(tmdb, media: str, page: int, **filters) -> list[dict[str, Any]]:
    fkey = json.dumps(filters, sort_keys=True)
    return await tmdb_list(f"discover:{media}:{page}:{fkey}", lambda: tmdb.discover(media, page=page, **filters))


async def trending(tmdb, media: str | None, page: int) -> list[dict[str, Any]]:
    m = media or "all"
    return await tmdb_list(f"trending:{m}:{page}", lambda: tmdb.trending(m, "week", page=page))


async def recommendations(tmdb, media: str, tmdb_id: int, page: int = 1, strict: bool = False) -> list[dict[str, Any]]:
    """TMDB's recommendations for a title. On trouble: nothing for a title's
    page and "Because you watched" (the section is left out), TMDBDown for
    the "See all" grid behind them (`strict`)."""
    async def load():
        recs = await tmdb.recommendations(media, tmdb_id, page=page)
        if not recs and page == 1:
            recs = await tmdb.similar(media, tmdb_id)
        return recs

    try:
        return await cache.cached(f"recs:{media}:{tmdb_id}:{page}", RECS_TTL, load)
    except Exception as exc:
        if strict:
            log.warning("recommendations %s:%d page %d failed", media, tmdb_id, page, exc_info=True)
            raise TMDBDown(TMDB_DOWN) from exc
        return []


async def db_keys(db: AsyncSession, user_id: int, key: str, media: str | None) -> list[str]:
    """Ordered title keys for the personal rows (and the shared library)."""
    def keep(m: str) -> bool:
        return media is None or m == media

    if key == "watchlist":
        q = (select(WatchlistEntry.media_type, WatchlistEntry.tmdb_id)
             .where(WatchlistEntry.user_id == user_id, WatchlistEntry.removed_at.is_(None))
             .order_by(WatchlistEntry.added_at.desc()))
    elif key == "favorites":
        q = (select(Favorite.media_type, Favorite.tmdb_id)
             .where(Favorite.user_id == user_id, Favorite.removed_at.is_(None)).order_by(Favorite.added_at.desc()))
    elif key == "rated":
        q = (select(UserRating.media_type, UserRating.tmdb_id).where(UserRating.user_id == user_id, UserRating.rating >= 7)
             .order_by(UserRating.rating.desc(), UserRating.rated_at.desc()))
    elif key == "ratings":
        q = (select(UserRating.media_type, UserRating.tmdb_id).where(UserRating.user_id == user_id)
             .order_by(UserRating.rated_at.desc()))
    elif key == "recent":
        q = (select(LibraryItem.media_type, LibraryItem.tmdb_id).where(LibraryItem.tmdb_id.isnot(None))
             .order_by(LibraryItem.date_added.desc().nulls_last()))
    else:
        return []
    return list(dict.fromkeys(titles.tkey(m, t) for m, t in await db.execute(q) if keep(m)))


async def _unwatched_keys(db: AsyncSession, media: str | None, st: UserState) -> list[str]:
    rows = await db.execute(select(LibraryItem.media_type, LibraryItem.tmdb_id).where(LibraryItem.tmdb_id.isnot(None)))
    keys = [titles.tkey(m, t) for m, t in rows if (media is None or m == media) and (m, t) not in st.watched]
    keys = list(dict.fromkeys(keys))
    # A different slice every day, stable within the day.
    random.Random(dt.date.today().toordinal() * 1000 + st.user_id).shuffle(keys)
    return keys


async def cards_for_keys(db: AsyncSession, keys: list[str], st: UserState, offset: int, size: int) -> tuple[list[dict], bool]:
    window = keys[offset:offset + size]
    metas = await titles.ensure(db, clients.tmdb(), window, max_fetch=size)
    return cards([metas.get(k) for k in window], st), offset + size < len(keys)


async def load_items(db: AsyncSession, user: User, tab: str, key: str, st: UserState, page: int = 1,
                     size: int = ROW_SIZE, media: str | None = None, sort: str = "list") -> tuple[list[dict], bool]:
    """Cards for one single row key (not the multi rows) at a page."""
    tmdb = clients.tmdb()
    media = media or tab_media(tab)
    kind, _, arg = key.partition(":")
    offset = (page - 1) * size

    if kind in ("watchlist", "favorites", "rated", "ratings", "recent"):
        return await cards_for_keys(db, await db_keys(db, user.id, kind, media), st, offset, size)
    if kind == "unwatched":
        return await cards_for_keys(db, await _unwatched_keys(db, media, st), st, offset, size)
    if kind == "list":
        try:
            lst = await lists_svc.get_visible(db, user.id, int(arg))
        except (lists_svc.ListError, ValueError):
            return [], False
        if lst.kind == "smart":
            results = await lists_svc.smart_results(tmdb, lst, page)
            f = json.loads(lst.filters_json or "{}")
            return cards(await titles.upsert_results(db, results, f.get("media", "movie")), st), tmdb_more(results, page)
        return await cards_for_keys(db, await lists_svc.item_keys(db, lst.id, media, sort), st, offset, size)
    if kind == "recs":
        rmedia, _, rid = arg.partition(":")
        if rmedia not in ("movie", "tv") or not rid.isdigit():
            return [], False
        results = await recommendations(tmdb, rmedia, int(rid), page, strict=True)
        return cards(await titles.upsert_results(db, results, rmedia), st), tmdb_more(results, page)

    # TMDB rows; For You falls back to movies where one media is needed.
    m = media or "movie"
    if kind == "trending":
        results = await trending(tmdb, media, page)
    elif kind == "popular":
        results = await discover(tmdb, m, page, sort_by="popularity.desc")
    elif kind == "upcoming":
        results = await tmdb_list(f"upcoming:{m}:{page}", lambda: tmdb.upcoming(m, page=page))
    elif kind == "top_rated":
        # Only titles with enough votes (2,000 for films, 500 for shows), so a
        # 10/10 from a handful of people doesn't top the row.
        results = await discover(tmdb, m, page, sort_by="vote_average.desc", **{"vote_count.gte": 2000 if m == "movie" else 500})
    elif kind == "provider" and arg.isdigit():
        results = await discover(tmdb, m, page, sort_by="popularity.desc", with_watch_providers=arg, watch_region=settings.watch_region)
    elif kind == "genre" and arg.isdigit():
        results = await discover(tmdb, m, page, sort_by="popularity.desc", with_genres=arg)
    else:
        return [], False
    metas = await titles.upsert_results(db, results, None if kind == "trending" and media is None else m)
    return cards(metas, st), tmdb_more(results, page)


async def continue_row(db: AsyncSession, user: User, tab: str, st: UserState) -> list[dict[str, Any]]:
    """The person's Jellyfin "Continue watching" + "Next up", as landscape
    items: every one of them (no cap; the old 20 of each is gone)."""
    if not user.jellyfin_user_id:
        return []
    jf, uid = clients.jellyfin(), user.jellyfin_user_id
    media = tab_media(tab)

    async def load():
        resume, nxt = await asyncio.gather(jf.resume(uid), jf.next_up(uid), return_exceptions=True)
        return (resume if isinstance(resume, list) else []), (nxt if isinstance(nxt, list) else [])

    try:
        # 60 s; "Finished it?", an episode tick, a synced Jellyfin play or a
        # plugin event clears it sooner.
        resume, nxt = await cache.cached(f"jf:continue:{user.id}", 60.0, load)
    except Exception:
        return []
    series_tmdb = {jf_id: tid for jf_id, tid in await db.execute(
        select(LibraryItem.jellyfin_id, LibraryItem.tmdb_id).where(LibraryItem.media_type == "tv"))}
    entries: list[dict[str, Any]] = []
    seen_series: set[str] = set()
    for item in resume + nxt:
        kind = item.get("Type")
        if kind == "Movie":
            if media == "tv":
                continue
            tid = (item.get("ProviderIds") or {}).get("Tmdb")
            key = titles.tkey("movie", int(tid)) if tid and str(tid).isdigit() else None
            subtitle = None
        elif kind == "Episode":
            if media == "movie":
                continue
            sid = item.get("SeriesId")
            if sid in seen_series:
                continue
            seen_series.add(sid)
            tid = series_tmdb.get(sid)
            key = titles.tkey("tv", tid) if tid else None
            se = f"S{item.get('ParentIndexNumber', '?')} · E{item.get('IndexNumber', '?')}"
            subtitle = f"{se} – {item.get('Name')}" if item.get("Name") else se
        else:
            continue
        ud = item.get("UserData") or {}
        entries.append({
            "key": key,
            "jellyfin_id": item.get("Id"),
            "title": item.get("SeriesName") if kind == "Episode" else item.get("Name"),
            "subtitle": subtitle,
            "progress": round(ud.get("PlayedPercentage") or 0),
            "image_item": item.get("Id"),
            "series_item": item.get("SeriesId"),
            "runtime_min": round((item.get("RunTimeTicks") or 0) / 600_000_000) or None,
            "season": item.get("ParentIndexNumber") if kind == "Episode" else None,
            "episode": item.get("IndexNumber") if kind == "Episode" else None,
        })
    metas = await titles.ensure(db, clients.tmdb(), [e["key"] for e in entries if e["key"]], max_fetch=len(entries))
    for e in entries:
        meta = metas.get(e["key"]) if e["key"] else None
        e["backdrop"] = (meta or {}).get("backdrop_path")
        e["poster"] = (meta or {}).get("poster_path")
        if e["key"]:
            e["media_type"], e["tmdb_id"] = titles.split_key(e["key"])
    return entries


async def build_rows(db: AsyncSession, user: User, tab: str, key: str, st: UserState) -> list[dict[str, Any]]:
    """One Home row key -> one or more rendered rows."""
    tmdb = clients.tmdb()
    media = tab_media(tab)

    if key == "continue":
        items = await continue_row(db, user, tab, st)
        return [{"id": key, "title": home_config.row_title(tab, key), "style": "landscape", "items": items}] if items else []

    if key == "because":
        seeds = recommend.because_set(await recommend.recent_seeds(db, user.id, media), rotation_index())
        recs = await asyncio.gather(*(recommendations(tmdb, m, tid) for m, tid, _ in seeds))
        out = []
        for (m, tid, name), results in zip(seeds, recs):
            metas = await titles.upsert_results(db, results, m)
            items = [c for c in cards(metas, st) if not c["watched"]][:ROW_SIZE]
            if items:
                out.append({
                    "id": f"because:{m}:{tid}", "kicker": "Because you watched", "title": name, "style": "poster",
                    "items": items, "more": {"key": f"recs:{m}:{tid}", "title": f"Because you watched {name}"},
                })
        return out

    if key == "ai":
        out = []
        got, basis = await ai_rows_nonblocking(db, user)
        chosen = recommend.ai_set(got, rotation_index())
        metas = await titles.ensure(db, tmdb, [k for row in chosen for k in row["keys"]], max_fetch=60)
        for i, row in enumerate(chosen):
            items = [c for c in cards([metas.get(k) for k in row["keys"]], st) if not c["watched"]]
            if items:
                out.append({"id": f"ai:{i}", "kicker": "Picked for you", "title": row["theme"], "style": "poster", "items": items})
        if out:  # what the AI read, under the first row (docs/limits.md, Recommendations)
            out[0]["subtitle"] = recommend.basis_line(basis)
        return out

    items, more = await load_items(db, user, tab, key, st)
    if not items:
        return []
    row: dict[str, Any] = {"id": key, "style": "poster", "items": items}
    if key.startswith("list:"):
        lst = await lists_svc.get_visible(db, user.id, int(key.split(":")[1]))
        row.update(title=lst.name, subtitle=home_config.list_by_line(lst), list_id=lst.id, group=home_config.list_group(lst))
    else:
        row["title"] = home_config.row_title(tab, key)
    if more or len(items) >= ROW_SIZE:
        row["more"] = {"key": key, "title": row["title"]}
    return [row]


# ---------------------------------------------------------------------------
# Rotation: which set of "Because you watched" / "Picked for you" rows is up
# ---------------------------------------------------------------------------

def part_of_day(now: dt.datetime | None = None) -> tuple[int, int]:
    """(part, day): morning 5am-noon = 0, noon-6pm = 1, night 6pm-5am = 2,
    in the install's time zone; the small hours count with the evening before."""
    local = (now or dt.datetime.now(dt.timezone.utc)).astimezone(ZoneInfo(settings.timezone))
    part = 0 if 5 <= local.hour < 12 else 1 if 12 <= local.hour < 18 else 2
    day = local.date().toordinal() - (1 if local.hour < 5 else 0)
    return part, day


def rotation_index(now: dt.datetime | None = None) -> int:
    """The set of "Because you watched" / "Picked for you" rows on show.
    Morning, noon and night each get a different set, and the sets move on
    by one each day, so someone who only opens the app at night still sees
    every set within three days."""
    part, day = part_of_day(now)
    return (part + day) % recommend.SETS


# ---------------------------------------------------------------------------
# AI rows: served from the cache, rebuilt in the background
# ---------------------------------------------------------------------------

_ai_tasks: dict[int, asyncio.Task] = {}
# A rebuild that fails (the model errors, or none of its picks is found on
# TMDB) isn't tried again at once: the next one waits AI_RETRY_FIRST, then
# twice as long after each failure in a row, at most AI_RETRY_MAX; a good
# rebuild resets it. Meanwhile the rows before keep showing, or, with none,
# Home says why in plain words (ai_note). In memory: a restart forgets it.
# See docs/limits.md (Recommendations).
AI_RETRY_FIRST = dt.timedelta(minutes=10)
AI_RETRY_MAX = dt.timedelta(hours=6)
_ai_failures: dict[int, dict[str, Any]] = {}


def _ai_failed(user_id: int, why: str) -> None:
    count = (_ai_failures.get(user_id) or {}).get("count", 0) + 1
    wait = min(AI_RETRY_FIRST * (2 ** (count - 1)), AI_RETRY_MAX)
    _ai_failures[user_id] = {"count": count, "why": why, "until": dt.datetime.now(dt.timezone.utc) + wait}
    log.warning("AI rows for user %s failed (%s); next try in %d minutes", user_id, why, wait.total_seconds() // 60)


def _in_a_while(until: dt.datetime) -> str:
    minutes = max(1, round((until - dt.datetime.now(dt.timezone.utc)).total_seconds() / 60))
    if minutes < 90:
        return f"in about {minutes} minute{'s' if minutes != 1 else ''}"
    return f"in about {round(minutes / 60)} hours"


def ai_note(user_id: int, have_rows: bool) -> str | None:
    """What Home says where "Picked for you" would be, when it has no rows."""
    if have_rows or not settings.ai_enabled:
        return None
    fail = _ai_failures.get(user_id)
    if fail:
        return (f"Picked for you couldn't be made just now: {fail['why']}. "
                f"Movie Night tries again by itself {_in_a_while(fail['until'])}.")
    task = _ai_tasks.get(user_id)
    if task is not None and not task.done():
        return "Picked for you is being made from your history; it shows here in a minute or two."
    return None


async def ai_rows_nonblocking(db: AsyncSession, user: User) -> tuple[list[dict[str, Any]], dict[str, Any] | None]:
    """Serve the person's cached AI rows (and what the AI read for them).
    When they are older than the TTL, or enough new watches came in since,
    rebuild them in the background and keep serving the cached ones
    meanwhile; after a failed rebuild, not before the back-off is over."""
    if not settings.ai_enabled:
        return [], None
    latest = await db.scalar(select(AIRowCache).where(AIRowCache.user_id == user.id)
                             .order_by(AIRowCache.generated_at.desc()).limit(1))
    payload = json.loads(latest.payload_json) if latest else None
    rows = payload["rows"] if isinstance(payload, dict) and payload.get("v") == recommend.AI_CACHE_VERSION else []
    basis = payload.get("basis") if rows else None
    stale = True
    if latest is not None and rows and latest.model == settings.litellm_model:
        generated = latest.generated_at if latest.generated_at.tzinfo else latest.generated_at.replace(tzinfo=dt.timezone.utc)
        fresh_enough = generated >= dt.datetime.now(dt.timezone.utc) - dt.timedelta(hours=settings.ai_rows_cache_ttl_hours)
        new_watches = await db.scalar(select(HistoryEntry.id)
                                      .where(HistoryEntry.user_id == user.id, HistoryEntry.watched_at > generated,
                                             HistoryEntry.watched_at <= dt.datetime.now(dt.timezone.utc))
                                      .offset(settings.ai_refresh_after_watches - 1).limit(1))
        stale = not fresh_enough or new_watches is not None
    # One rebuild at a time per person, and none while a failed one's
    # back-off lasts. A good rebuild replaces the person's rows (only the
    # newest is kept); a failed one leaves the rows before. These calls have
    # no allowance.
    task = _ai_tasks.get(user.id)
    fail = _ai_failures.get(user.id)
    waiting = fail is not None and dt.datetime.now(dt.timezone.utc) < fail["until"]
    if stale and not waiting and (task is None or task.done()):
        _ai_tasks[user.id] = asyncio.create_task(_generate(user.id))
    return rows, basis


async def _generate(user_id: int) -> None:
    from app.core.db import session

    try:
        async with session() as s:
            out = await recommend.generate_ai_rows(s, user_id, clients.tmdb(), clients.litellm())
    except Exception:
        log.exception("AI rows for user %s failed", user_id)
        _ai_failed(user_id, "the AI didn't answer")
        return
    if out:
        _ai_failures.pop(user_id, None)
    else:
        _ai_failed(user_id, "none of the AI's picks could be found on TMDB")


async def hero(db: AsyncSession, user: User, tab: str, st: UserState) -> list[dict[str, Any]]:
    """Big swipeable banner: the two newest library additions, then trending
    titles not yet watched, all with backdrops."""
    media = tab_media(tab)
    keys = (await db_keys(db, user.id, "recent", media))[:6]
    metas = await titles.ensure(db, clients.tmdb(), keys, max_fetch=6)
    lib = [c for c in cards([metas.get(k) for k in keys], st) if c.get("backdrop")][:2]
    for c in lib:
        c["hero_label"] = "New in your library"
    try:
        trend = await trending(clients.tmdb(), media, 1)
    except TMDBDown:
        trend = []  # the banner shows the library's newest alone
    tmetas = await titles.upsert_results(db, trend, media)
    taken = {c["key"] for c in lib}
    hot = [c for c in cards(tmetas, st) if c.get("backdrop") and not c["watched"] and c["key"] not in taken][:5]
    for c in hot:
        c["hero_label"] = "Trending"
    out = lib + hot
    info = await titles.get_many(db, [c["key"] for c in out])
    for c in out:
        meta = info.get(c["key"]) or {}
        c["overview"] = meta.get("overview")
        c["genres"] = (meta.get("genres") or [])[:3]
    return out
