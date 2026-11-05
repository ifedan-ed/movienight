"""Which rows each Home tab shows a person, in what order (the "Home rows"
screen). Each tab's config is a list of {"key", "on"} in their settings.

Row keys are read per tab: "trending" on the Movies tab is trending movies,
on For You movies and shows together. Lists are "list:<id>" rows on the For
You tab: their own lists, Shared lists and MDBList lists.
"""
from __future__ import annotations

from typing import Any

from sqlalchemy import case, or_, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models import UserList
from app.services import users
from app.services.titles import MOVIE_GENRES, TV_GENRES

TABS = ("for_you", "movies", "shows")
TAB_MEDIA = {"for_you": None, "movies": "movie", "shows": "tv"}

# TMDB watch-provider ids (US)
PROVIDERS = {
    8: "Netflix", 337: "Disney+", 9: "Prime Video", 350: "Apple TV+", 1899: "Max",
    15: "Hulu", 531: "Paramount+", 386: "Peacock",
}

BASE_ROWS = {
    "continue": "Up next",
    "recent": "Recently added",
    "watchlist": "Plan to watch",
    "because": "Because you watched",
    "ai": "Picked for you",
    "trending": "Trending",
    "popular": "Popular",
    "upcoming": "Coming soon",
    "top_rated": "Top rated",
    "unwatched": "In your library, not watched yet",
    "favorites": "Favorites",
    "rated": "Your top rated",
}

DEFAULTS: dict[str, list[str]] = {
    "for_you": [
        "continue", "recent", "watchlist", "because", "ai", "trending", "upcoming",
        "provider:8", "provider:337", "provider:9", "provider:350", "provider:1899", "unwatched", "favorites",
    ],
    "movies": [
        "continue", "recent", "watchlist", "because", "trending", "popular", "upcoming", "top_rated",
        "genre:28", "genre:35", "genre:53", "genre:18", "genre:878", "genre:27", "genre:16", "genre:99",
        "provider:8", "provider:337", "provider:9", "provider:350", "provider:1899",
    ],
    "shows": [
        "continue", "recent", "watchlist", "because", "trending", "popular", "upcoming", "top_rated",
        "genre:18", "genre:35", "genre:80", "genre:10765", "genre:16", "genre:99", "genre:10764",
        "provider:8", "provider:337", "provider:9", "provider:350", "provider:1899",
    ],
}

GROUPS = ("mine", "shared", "mdblist")


def list_group(lst: UserList) -> str:
    """mine: the person's own lists; shared: fixed copies of curated lists;
    mdblist: lists kept in step with MDBList."""
    if lst.owner_id is not None:
        return "mine"
    if lst.source == "follow" or lst.origin == "mdblist":
        return "mdblist"
    return "shared"


def list_by_line(lst: UserList) -> str:
    group = list_group(lst)
    if group == "mine":
        return "Smart list" if lst.kind == "smart" else "Your list"
    if group == "mdblist":
        return f"MDBList · {lst.origin_user}" if lst.origin_user else "MDBList"
    return "Shared list"


async def visible_lists(db: AsyncSession, user_id: int) -> list[UserList]:
    """A person's own lists first, then Shared lists, then MDBList lists."""
    order = case((UserList.owner_id.isnot(None), 0), (UserList.source == "follow", 2), else_=1)
    q = (select(UserList).where(or_(UserList.owner_id == user_id, UserList.owner_id.is_(None)))
         .order_by(order, UserList.created_at, UserList.id))
    return list(await db.scalars(q))


def row_title(tab: str, key: str, list_names: dict[int, str] | None = None) -> str:
    kind, _, arg = key.partition(":")
    if kind == "provider":
        return f"Popular on {PROVIDERS.get(int(arg), 'streaming')}"
    if kind == "genre":
        table = TV_GENRES if tab == "shows" else MOVIE_GENRES
        return table.get(int(arg), "Genre")
    if kind == "list":
        return (list_names or {}).get(int(arg), "List")
    if kind == "recs":
        return "More like this"
    title = BASE_ROWS.get(key, key)
    if key == "trending" and tab == "for_you":
        return "Trending now"
    return title


def catalog(tab: str, lists: list[UserList]) -> list[str]:
    """Every row key this tab can show."""
    keys = list(BASE_ROWS)
    if tab in ("movies", "shows"):
        keys = [k for k in keys if k not in ("ai", "favorites", "rated")]
        table = TV_GENRES if tab == "shows" else MOVIE_GENRES
        keys += [f"genre:{g}" for g in table]
    keys += [f"provider:{p}" for p in PROVIDERS]
    if tab == "for_you":
        keys += [f"list:{l.id}" for l in lists]
    return keys


def _setting_key(tab: str) -> str:
    return f"home_rows:{tab}"


def merge(saved: list[dict[str, Any]] | None, tab: str, available: list[str]) -> list[dict[str, Any]]:
    """Saved order merged with the catalog: unknown keys dropped (a deleted
    list), rows that did not exist when the order was saved appended off."""
    if not saved:
        on = DEFAULTS[tab]
        saved = [{"key": k, "on": True} for k in on] + [{"key": k, "on": False} for k in available if k not in on]
    allowed = set(available)
    seen: set[str] = set()
    out: list[dict[str, Any]] = []
    for entry in saved:
        k = entry.get("key")
        if k in allowed and k not in seen:
            seen.add(k)
            out.append({"key": k, "on": bool(entry.get("on"))})
    out += [{"key": k, "on": False} for k in available if k not in seen]
    return out


async def get_config(db: AsyncSession, user_id: int, tab: str, lists: list[UserList] | None = None) -> list[dict[str, Any]]:
    tab = tab if tab in TABS else "for_you"
    if lists is None:
        lists = await visible_lists(db, user_id)
    saved = await users.get_setting(db, user_id, _setting_key(tab))
    return merge(saved, tab, catalog(tab, lists))


async def save_config(db: AsyncSession, user_id: int, tab: str, rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    tab = tab if tab in TABS else "for_you"
    lists = await visible_lists(db, user_id)
    available = set(catalog(tab, lists))
    clean = [{"key": r["key"], "on": bool(r.get("on"))} for r in rows if r.get("key") in available]
    await users.set_setting(db, user_id, _setting_key(tab), clean)
    return await get_config(db, user_id, tab, lists)


async def reset_config(db: AsyncSession, user_id: int, tab: str) -> list[dict[str, Any]]:
    """Back to defaults, keeping pinned lists pinned."""
    pinned = await pinned_lists(db, user_id) if tab == "for_you" else []
    await users.delete_setting(db, user_id, _setting_key(tab))
    for list_id in pinned:
        await set_pinned(db, user_id, list_id, True)
    return await get_config(db, user_id, tab)


async def pinned_lists(db: AsyncSession, user_id: int) -> list[int]:
    return [int(r["key"].split(":")[1]) for r in await get_config(db, user_id, "for_you")
            if r["key"].startswith("list:") and r["on"]]


async def set_pinned(db: AsyncSession, user_id: int, list_id: int, on: bool) -> None:
    """Show/hide a list on For You. A newly pinned list goes right after the
    last pinned list, or after "Plan to watch", so it is near the top."""
    rows = [r for r in await get_config(db, user_id, "for_you") if r["key"] != f"list:{list_id}"]
    entry = {"key": f"list:{list_id}", "on": on}
    if on:
        anchor = max((i for i, r in enumerate(rows) if r["key"].startswith("list:") and r["on"]), default=None)
        if anchor is None:
            anchor = next((i for i, r in enumerate(rows) if r["key"] == "watchlist"), -1)
        rows.insert(anchor + 1, entry)
    else:
        rows.append(entry)
    await save_config(db, user_id, "for_you", rows)
