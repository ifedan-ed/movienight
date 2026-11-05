"""Lists.

A person's own lists (owner_id = them): hand-made lists they edit, and
smart lists (saved TMDB filters, run live).

Catalog lists everyone on the install sees (owner_id NULL):
- Shared lists: fixed copies of curated lists (source "copy"); nobody edits
  them, they don't change.
- MDBList lists: followed on MDBList (source "follow") and kept in step
  with the original by a daily sync.

Anyone can star a list (a filter on the Lists tab) and show it on Home.
"""
from __future__ import annotations

import datetime as dt
import json
import logging
from typing import Any

from sqlalchemy import Select, delete, func, or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.config import settings
from app.models import ListFavorite, ListItem, Title, User, UserList
from app.services import home_config, titles

log = logging.getLogger("movienight.lists")

SORTS = ("list", "added", "title", "year", "rating")


class ListError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


def visible_clause(user_id: int):
    return or_(UserList.owner_id == user_id, UserList.owner_id.is_(None))


async def get_visible(db: AsyncSession, user_id: int, list_id: int) -> UserList:
    lst = await db.get(UserList, list_id)
    if lst is None or (lst.owner_id is not None and lst.owner_id != user_id):
        raise ListError(404, "List not found")
    return lst


def can_edit_items(lst: UserList, user_id: int) -> bool:
    return lst.owner_id == user_id and lst.kind == "list"


def can_delete(lst: UserList, user: User) -> bool:
    return lst.owner_id == user.id or (lst.owner_id is None and user.is_admin)


async def counts(db: AsyncSession, list_ids: list[int]) -> dict[int, dict[str, int]]:
    """list id -> {"movie": n, "tv": n}."""
    out: dict[int, dict[str, int]] = {i: {"movie": 0, "tv": 0} for i in list_ids}
    if list_ids:
        for list_id, media, n in await db.execute(
                select(ListItem.list_id, ListItem.media_type, func.count())
                .where(ListItem.list_id.in_(list_ids)).group_by(ListItem.list_id, ListItem.media_type)):
            out[list_id][media] = n
    return out


async def starred(db: AsyncSession, user_id: int) -> set[int]:
    return set(await db.scalars(select(ListFavorite.list_id).where(ListFavorite.user_id == user_id)))


async def summaries(db: AsyncSession, user: User) -> tuple[list[dict[str, Any]], list[str]]:
    """Every list the person sees, in Lists-tab order, plus the title keys of
    collage posters the cache is still missing."""
    lists = await home_config.visible_lists(db, user.id)
    pinned = set(await home_config.pinned_lists(db, user.id))
    stars = await starred(db, user.id)
    per_media = await counts(db, [l.id for l in lists])
    # First five items of each list, for the collage.
    first: dict[int, list[str]] = {}
    if lists:
        ranked = (select(ListItem.list_id, ListItem.media_type, ListItem.tmdb_id,
                         func.row_number().over(partition_by=ListItem.list_id,
                                                order_by=(ListItem.position, ListItem.id)).label("n"))
                  .where(ListItem.list_id.in_([l.id for l in lists])).subquery())
        for list_id, media, tid, _n in await db.execute(select(ranked).where(ranked.c.n <= 5)):
            first.setdefault(list_id, []).append(titles.tkey(media, tid))
    metas = await titles.get_many(db, [k for keys in first.values() for k in keys])
    missing: list[str] = []
    out = []
    for lst in lists:
        keys = first.get(lst.id, [])
        missing += [k for k in keys if k not in metas]
        c = per_media.get(lst.id, {"movie": 0, "tv": 0})
        out.append({
            "id": lst.id,
            "name": lst.name,
            "description": lst.description or "",
            "kind": lst.kind,
            "source": lst.source,
            "origin": lst.origin,
            "group": home_config.list_group(lst),
            "by": home_config.list_by_line(lst),
            "likes": lst.likes,
            "count": None if lst.kind == "smart" else c["movie"] + c["tv"],
            "movies": c["movie"],
            "shows": c["tv"],
            "posters": [metas[k]["poster_path"] for k in keys if k in metas and metas[k].get("poster_path")],
            "pinned": lst.id in pinned,
            "starred": lst.id in stars,
            "editable": can_edit_items(lst, user.id),
            "deletable": can_delete(lst, user),
        })
    return out, missing


# ---------------------------------------------------------------------------
# A person's own lists
# ---------------------------------------------------------------------------


async def create_list(db: AsyncSession, user_id: int, name: str, description: str | None = None) -> UserList:
    name = (name or "").strip()
    if not name:
        raise ListError(422, "A list needs a name")
    lst = UserList(owner_id=user_id, name=name, description=(description or "").strip() or None, kind="list", source="app")
    db.add(lst)
    await db.commit()
    return lst


SMART_SORTS = {"popularity.desc", "vote_average.desc", "primary_release_date.desc", "first_air_date.desc", "revenue.desc"}


def clean_filters(f: dict[str, Any]) -> dict[str, Any]:
    out: dict[str, Any] = {"media": "tv" if f.get("media") == "tv" else "movie"}
    for k in ("genre", "provider"):
        if f.get(k) and str(f[k]).isdigit():
            out[k] = str(f[k])
    for k in ("year_from", "year_to", "runtime_min", "runtime_max"):
        try:
            if f.get(k) not in (None, ""):
                out[k] = int(f[k])
        except (TypeError, ValueError):
            pass
    try:
        if f.get("min_rating") not in (None, ""):
            out["min_rating"] = float(f["min_rating"])
    except (TypeError, ValueError):
        pass
    if f.get("language") and str(f["language"]).isalpha():
        out["language"] = str(f["language"])[:5].lower()
    if f.get("sort") in SMART_SORTS:
        out["sort"] = f["sort"]
    return out


def tmdb_filters(f: dict[str, Any], watch_region: str = "US") -> dict[str, Any]:
    """Saved smart-list (or Discover) filters -> TMDB discover params."""
    tv = f.get("media") == "tv"
    p: dict[str, Any] = {}
    if f.get("genre"):
        p["with_genres"] = f["genre"]
    if f.get("provider"):
        p["with_watch_providers"] = f["provider"]
        p["watch_region"] = watch_region
    date = "first_air_date" if tv else "primary_release_date"
    if f.get("year_from"):
        p[f"{date}.gte"] = f"{f['year_from']}-01-01"
    if f.get("year_to"):
        p[f"{date}.lte"] = f"{f['year_to']}-12-31"
    if f.get("runtime_min"):
        p["with_runtime.gte"] = f["runtime_min"]
    if f.get("runtime_max"):
        p["with_runtime.lte"] = f["runtime_max"]
    if f.get("min_rating"):
        p["vote_average.gte"] = f["min_rating"]
        # A minimum rating also needs 100 votes, so a 9/10 from three people
        # doesn't count (docs/limits.md).
        p["vote_count.gte"] = 100
    if f.get("language"):
        p["with_original_language"] = f["language"]
    sort = f.get("sort") or "popularity.desc"
    if tv and sort == "primary_release_date.desc":
        sort = "first_air_date.desc"
    if not tv and sort == "first_air_date.desc":
        sort = "primary_release_date.desc"
    p["sort_by"] = sort
    return p


async def create_smart_list(db: AsyncSession, user_id: int, name: str, filters: dict[str, Any]) -> UserList:
    name = (name or "").strip()
    if not name:
        raise ListError(422, "A list needs a name")
    lst = UserList(owner_id=user_id, name=name, kind="smart", source="app", filters_json=json.dumps(clean_filters(filters)))
    db.add(lst)
    await db.commit()
    return lst


async def smart_results(tmdb, lst: UserList, page: int = 1) -> list[dict[str, Any]]:
    from app.core import cache

    f = json.loads(lst.filters_json or "{}")
    media = f.get("media", "movie")
    params = tmdb_filters(f, settings.watch_region)
    key = f"smart:{media}:{page}:{json.dumps(params, sort_keys=True)}"
    try:
        return await cache.cached(key, 1800.0, lambda: tmdb.discover(media, page=page, **params))
    except Exception as exc:
        log.warning("smart list %s failed", lst.id, exc_info=True)
        copy = cache.peek(key)
        if copy is None:
            # Said where the cards would be, never an empty list (rows.TMDBDown).
            from app.services.rows import TMDB_DOWN, TMDBDown
            raise TMDBDown(TMDB_DOWN) from exc
        return copy


async def add_item(db: AsyncSession, lst: UserList, media_type: str, tmdb_id: int) -> bool:
    exists = await db.scalar(select(ListItem.id).where(ListItem.list_id == lst.id, ListItem.media_type == media_type,
                                                       ListItem.tmdb_id == tmdb_id))
    if exists:
        return False
    top = await db.scalar(select(func.max(ListItem.position)).where(ListItem.list_id == lst.id)) or 0
    db.add(ListItem(list_id=lst.id, media_type=media_type, tmdb_id=tmdb_id, position=top + 1))
    await db.commit()
    return True


async def remove_item(db: AsyncSession, lst: UserList, media_type: str, tmdb_id: int) -> bool:
    n = (await db.execute(delete(ListItem).where(ListItem.list_id == lst.id, ListItem.media_type == media_type,
                                                 ListItem.tmdb_id == tmdb_id))).rowcount or 0
    await db.commit()
    return n > 0


async def delete_list(db: AsyncSession, lst: UserList) -> None:
    await db.delete(lst)  # items, stars cascade
    await db.commit()


async def copy_to_mine(db: AsyncSession, user_id: int, lst: UserList) -> UserList:
    """A person's own editable copy of any list they can see."""
    mine = UserList(owner_id=user_id, name=lst.name, description=lst.description, kind="list", source="app")
    db.add(mine)
    await db.flush()
    items = list(await db.execute(select(ListItem.media_type, ListItem.tmdb_id)
                                  .where(ListItem.list_id == lst.id).order_by(ListItem.position, ListItem.id)))
    db.add_all([ListItem(list_id=mine.id, media_type=m, tmdb_id=t, position=i) for i, (m, t) in enumerate(items)])
    await db.commit()
    return mine


async def memberships(db: AsyncSession, user_id: int, media_type: str, tmdb_id: int) -> list[dict[str, Any]]:
    """The person's own lists a title can go in (curated lists are fixed)."""
    own = list(await db.scalars(select(UserList).where(UserList.owner_id == user_id, UserList.kind == "list")
                                .order_by(UserList.created_at, UserList.id)))
    inside = set(await db.scalars(select(ListItem.list_id).where(ListItem.list_id.in_([l.id for l in own] or [0]),
                                                                 ListItem.media_type == media_type, ListItem.tmdb_id == tmdb_id)))
    return [{"id": l.id, "name": l.name, "in_list": l.id in inside, "source": l.source} for l in own]


async def set_star(db: AsyncSession, user_id: int, lst: UserList, on: bool) -> bool:
    row = await db.get(ListFavorite, (user_id, lst.id))
    if on and row is None:
        db.add(ListFavorite(user_id=user_id, list_id=lst.id))
    elif not on and row is not None:
        await db.delete(row)
    await db.commit()
    return on


# ---------------------------------------------------------------------------
# Items of a list, filtered and sorted
# ---------------------------------------------------------------------------


def items_query(list_id: int, media: str | None, sort: str) -> Select:
    q = (select(ListItem.media_type, ListItem.tmdb_id)
         .outerjoin(Title, (Title.media_type == ListItem.media_type) & (Title.tmdb_id == ListItem.tmdb_id))
         .where(ListItem.list_id == list_id))
    if media in ("movie", "tv"):
        q = q.where(ListItem.media_type == media)
    if sort == "added":
        q = q.order_by(ListItem.added_at.desc(), ListItem.id.desc())
    elif sort == "title":
        q = q.order_by(func.lower(Title.title).asc().nulls_last(), ListItem.position)
    elif sort == "year":
        q = q.order_by(Title.release_date.desc().nulls_last(), ListItem.position)
    elif sort == "rating":
        q = q.order_by(Title.vote_average.desc().nulls_last(), ListItem.position)
    else:
        q = q.order_by(ListItem.position, ListItem.id)
    return q


async def item_keys(db: AsyncSession, list_id: int, media: str | None = None, sort: str = "list") -> list[str]:
    return [titles.tkey(m, t) for m, t in await db.execute(items_query(list_id, media, sort if sort in SORTS else "list"))]


async def replace_items(db: AsyncSession, lst: UserList, entries: list[tuple[str, int, str, int | None]]) -> int:
    """Make a catalog list's items exactly these (media, tmdb_id, title,
    year) entries, in order; seeds the title cache with names."""
    seen: set[tuple[str, int]] = set()
    refs: list[tuple[str, int]] = []
    names = []
    for media, tid, name, year in entries:
        if (media, tid) in seen:
            continue
        seen.add((media, tid))
        refs.append((media, tid))
        names.append({"key": titles.tkey(media, tid), "media_type": media, "tmdb_id": tid, "title": name or "", "year": year})
    existing = {(i.media_type, i.tmdb_id): i for i in await db.scalars(select(ListItem).where(ListItem.list_id == lst.id))}
    for pos, (media, tid) in enumerate(refs):
        item = existing.pop((media, tid), None)
        if item is None:
            db.add(ListItem(list_id=lst.id, media_type=media, tmdb_id=tid, position=pos))
        else:
            item.position = pos
    for item in existing.values():
        await db.delete(item)
    # Items MDBList sent without a TMDB id (clients/mdblist.py ListItems).
    lst.not_shown = getattr(entries, "no_tmdb", 0) or None
    lst.synced_at = dt.datetime.now(dt.timezone.utc)
    await db.commit()
    await titles.upsert(db, names)
    return len(refs)


# ---------------------------------------------------------------------------
# MDBList
# ---------------------------------------------------------------------------


async def follow_mdblist(db: AsyncSession, mdb, ref: dict[str, str]) -> UserList:
    """Add an MDBList list to the install's catalog (or pick up the one
    already followed) and fill it."""
    summary = await mdb.get_list(ref)
    if summary is None or not summary.get("mdblist_id"):
        raise ListError(404, "That MDBList list wasn't found (it may be private)")
    origin_id = str(summary["mdblist_id"])
    lst = await db.scalar(select(UserList).where(UserList.origin == "mdblist", UserList.origin_id == origin_id,
                                                 UserList.owner_id.is_(None)))
    if lst is None:
        lst = UserList(owner_id=None, name=summary["name"], description=summary.get("description") or None,
                       kind="list", source="follow", origin="mdblist", origin_id=origin_id,
                       origin_user=summary.get("user") or None, origin_slug=summary.get("slug"), likes=summary.get("likes"))
        db.add(lst)
        await db.commit()
    await replace_items(db, lst, await mdb.list_items(origin_id))
    return lst


async def sync_mdblist(db: AsyncSession, mdb, lst: UserList) -> int:
    if lst.source != "follow" or lst.origin != "mdblist" or not lst.origin_id:
        raise ListError(409, "Only MDBList lists are kept in step")
    summary = await mdb.get_list({"id": lst.origin_id})
    if summary:
        lst.name = summary["name"] or lst.name
        lst.likes = summary.get("likes")
        lst.description = summary.get("description") or lst.description
    entries = await mdb.list_items(lst.origin_id)
    if not entries and await db.scalar(select(ListItem.id).where(ListItem.list_id == lst.id).limit(1)) is not None:
        # An empty answer is far likelier a hiccup at MDBList than a list
        # emptied on purpose: the titles already here stay.
        raise ListError(502, "MDBList sent this list back empty; the titles already here are kept")
    return await replace_items(db, lst, entries)


async def sync_all_mdblist(db: AsyncSession, mdb) -> int:
    n = 0
    # By id: a rollback after a failed list expires the loaded rows.
    for list_id in list(await db.scalars(select(UserList.id).where(UserList.source == "follow", UserList.origin == "mdblist"))):
        try:
            await sync_mdblist(db, mdb, await db.get(UserList, list_id))
            n += 1
        except Exception:
            await db.rollback()
            log.warning("sync of MDBList list %s failed", list_id, exc_info=True)
    return n


async def followed_mdblist_ids(db: AsyncSession) -> dict[str, int]:
    """MDBList id -> our list id, for marking explore results."""
    rows = await db.execute(select(UserList.origin_id, UserList.id)
                            .where(UserList.origin == "mdblist", UserList.owner_id.is_(None)))
    return {origin_id: list_id for origin_id, list_id in rows}
