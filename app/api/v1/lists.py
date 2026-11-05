"""Lists tab: watchlist, favourites, ratings, the person's own lists,
Shared lists, MDBList lists (explore, preview, follow), stars and Home
pins, and each list's page (Movies/Shows, sorting)."""
from __future__ import annotations

import json
import logging
from typing import Literal

from fastapi import APIRouter, Depends, HTTPException, Path, Query
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db, user_state
from app.clients.mdblist import REFUSED, MDBListRefused, parse_list_url
from app.core import cache
from app.core.config import settings
from app.models import User, UserList
from app.services import home_config, lists, mdblist_copy, rows, titles
from app.services.cards import cards

log = logging.getLogger("movienight.lists")
router = APIRouter(tags=["lists"])

ListId = Path(gt=0, le=2_000_000_000)
Sort = Literal["list", "added", "title", "year", "rating"]


def _http(exc: lists.ListError) -> HTTPException:
    return HTTPException(status_code=exc.status, detail=str(exc))


async def _visible(db: AsyncSession, user: User, list_id: int) -> UserList:
    try:
        return await lists.get_visible(db, user.id, list_id)
    except lists.ListError as exc:
        raise _http(exc) from exc


async def _posters(db: AsyncSession, keys: list[str]) -> list[str]:
    metas = await titles.ensure(db, clients.tmdb(), keys[:5], max_fetch=5)
    return [metas[k]["poster_path"] for k in keys[:5] if k in metas and metas[k].get("poster_path")]


@router.get("/lists", response_model=s.ListsOut)
async def all_lists(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    out, missing = await lists.summaries(db, user)
    if missing:  # first view of a list: fetch its collage posters, then redo
        await titles.ensure(db, clients.tmdb(), missing, max_fetch=40)
        out, _ = await lists.summaries(db, user)
    wl = await rows.db_keys(db, user.id, "watchlist", None)
    fav = await rows.db_keys(db, user.id, "favorites", None)
    rated = await rows.db_keys(db, user.id, "ratings", None)
    return {
        "watchlist": {"count": len(wl), "posters": await _posters(db, wl)},
        "favorites": {"count": len(fav), "posters": await _posters(db, fav)},
        "ratings": {"count": len(rated), "posters": await _posters(db, rated)},
        "lists": out,
    }


@router.post("/lists", response_model=s.CreatedOut)
async def create_list(body: s.ListIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    try:
        lst = await lists.create_list(db, user.id, body.name, body.description)
    except lists.ListError as exc:
        raise _http(exc) from exc
    return {"id": lst.id}


@router.post("/lists/smart", response_model=s.CreatedOut)
async def create_smart(body: s.SmartIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    try:
        lst = await lists.create_smart_list(db, user.id, body.name, body.filters.model_dump(mode="json"))
    except lists.ListError as exc:
        raise _http(exc) from exc
    return {"id": lst.id}


def _origin_url(lst: UserList) -> str | None:
    if lst.origin == "mdblist" and lst.origin_user and lst.origin_slug:
        return f"https://mdblist.com/lists/{lst.origin_user}/{lst.origin_slug}"
    return None


@router.get("/lists/{list_id}", response_model=s.ListOut)
async def get_list(list_id: int = ListId, page: int = Query(1, ge=1), media: s.MediaType | None = None,
                   sort: Sort = "list", db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    lst = await _visible(db, user, list_id)
    st = await user_state(db, user)
    m = media.value if media else None
    items, more = await rows.load_items(db, user, "for_you", f"list:{list_id}", st, page=page, size=rows.PAGE_SIZE,
                                        media=m, sort=sort)
    per_media = (await lists.counts(db, [lst.id]))[lst.id]
    return {
        "id": lst.id,
        "name": lst.name,
        "description": lst.description or "",
        "kind": lst.kind,
        "source": lst.source,
        "origin": lst.origin,
        "group": home_config.list_group(lst),
        "by": home_config.list_by_line(lst),
        "origin_url": _origin_url(lst),
        "likes": lst.likes,
        "count": None if lst.kind == "smart" else per_media["movie"] + per_media["tv"],
        "movies": per_media["movie"],
        "shows": per_media["tv"],
        "not_shown": lst.not_shown or 0,
        "filters": json.loads(lst.filters_json) if lst.filters_json else None,
        "pinned": lst.id in await home_config.pinned_lists(db, user.id),
        "starred": lst.id in await lists.starred(db, user.id),
        "editable": lists.can_edit_items(lst, user.id),
        "deletable": lists.can_delete(lst, user),
        "synced_at": lst.synced_at.isoformat() if lst.synced_at else None,
        "media": m,
        "sort": sort,
        "items": items,
        "more": more,
        "page": page,
        "note": rows.end_note(page, items, more) if lst.kind == "smart" else None,  # a smart list is TMDB's discover
    }


@router.patch("/lists/{list_id}", response_model=s.ListFlagsOut)
async def patch_list(body: s.ListPatch, list_id: int = ListId, db: AsyncSession = Depends(get_db),
                     user: User = Depends(current_user)):
    """Rename (own lists), show on Home, star."""
    lst = await _visible(db, user, list_id)
    if body.name is not None or body.description is not None:
        if lst.owner_id != user.id:
            raise HTTPException(status_code=409, detail="Shared and MDBList lists keep their own names")
        if body.name is not None and body.name.strip():
            lst.name = body.name.strip()
        if body.description is not None:
            lst.description = body.description.strip() or None
        await db.commit()
    if body.pinned is not None:
        await home_config.set_pinned(db, user.id, list_id, body.pinned)
    if body.starred is not None:
        await lists.set_star(db, user.id, lst, body.starred)
    return {"pinned": list_id in await home_config.pinned_lists(db, user.id),
            "starred": list_id in await lists.starred(db, user.id)}


@router.delete("/lists/{list_id}", response_model=s.Ok)
async def delete_list(list_id: int = ListId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    lst = await _visible(db, user, list_id)
    if not lists.can_delete(lst, user):
        raise HTTPException(status_code=403, detail="Only the install's admins can remove a shared or MDBList list")
    await lists.delete_list(db, lst)
    return {}


@router.post("/lists/{list_id}/items", response_model=s.AddedOut)
async def add_item(body: s.ListItemIn, list_id: int = ListId, db: AsyncSession = Depends(get_db),
                   user: User = Depends(current_user)):
    lst = await _visible(db, user, list_id)
    if not lists.can_edit_items(lst, user.id):
        raise HTTPException(status_code=409, detail="Curated lists are fixed; add it to one of your own lists")
    return {"added": await lists.add_item(db, lst, body.media_type.value, body.tmdb_id)}


@router.delete("/lists/{list_id}/items/{media}/{tmdb_id}", response_model=s.Ok)
async def remove_item(media: s.MediaType, list_id: int = ListId, tmdb_id: int = Path(gt=0, le=10_000_000_000),
                      db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    lst = await _visible(db, user, list_id)
    if not lists.can_edit_items(lst, user.id):
        raise HTTPException(status_code=409, detail="Curated lists are fixed")
    await lists.remove_item(db, lst, media.value, tmdb_id)
    return {}


@router.post("/lists/{list_id}/copy", response_model=s.CreatedOut)
async def copy_list(list_id: int = ListId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """An editable copy of any list, as one of the person's own."""
    lst = await _visible(db, user, list_id)
    if lst.kind != "list":
        raise HTTPException(status_code=409, detail="Smart lists run their filters live; there is nothing to copy")
    mine = await lists.copy_to_mine(db, user.id, lst)
    return {"id": mine.id}


@router.post("/lists/{list_id}/sync", response_model=s.CountOut)
async def sync_list(list_id: int = ListId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    lst = await _visible(db, user, list_id)
    try:
        n = await lists.sync_mdblist(db, clients.mdblist(), lst)
    except lists.ListError as exc:
        raise _http(exc) from exc
    except MDBListRefused as exc:
        raise HTTPException(status_code=503, detail=f"{REFUSED}. The titles already here are kept; try again later") from exc
    except Exception as exc:
        log.warning("MDBList sync failed", exc_info=True)
        raise HTTPException(status_code=502, detail="MDBList didn't answer — try again") from exc
    return {"count": n}


@router.get("/collection/{kind}", response_model=s.CollectionOut)
async def personal_collection(kind: Literal["watchlist", "favorites", "rated", "ratings"], page: int = Query(1, ge=1),
                              media: s.MediaType | None = None, db: AsyncSession = Depends(get_db),
                              user: User = Depends(current_user)):
    """Watchlist / favourites / ratings as a grid."""
    st = await user_state(db, user)
    m = media.value if media else None
    items, more = await rows.load_items(db, user, "for_you", kind, st, page=page, size=rows.PAGE_SIZE, media=m)
    names = {"watchlist": "Watchlist", "favorites": "Favorites", "rated": "Your top rated", "ratings": "Your ratings"}
    return {"name": names[kind], "count": len(await rows.db_keys(db, user.id, kind, m)), "items": items, "more": more, "page": page}


# ---------------------------------------------------------------------------
# MDBList: explore, preview, follow
# ---------------------------------------------------------------------------


async def _mdb(db: AsyncSession) -> mdblist_copy.WithCopy:
    """MDBList's lists, with Movie Night's copy answering while MDBList
    can't, including when this install has no MDBList key (any more)."""
    mdb = clients.mdblist()
    if not mdb.enabled and not await mdblist_copy.has_copy(db):
        raise HTTPException(status_code=503, detail="MDBList isn't set up on this install")
    return mdblist_copy.WithCopy(mdb, db)


async def _mark(db: AsyncSession, found: list[dict]) -> list[dict]:
    followed = await lists.followed_mdblist_ids(db)
    return [{**x, "followed_id": followed.get(str(x.get("mdblist_id")))} for x in found]


@router.get("/mdblist/lists", response_model=s.MDBListExploreOut)
async def mdblist_explore(q: str = Query("", max_length=settings.name_max_chars), db: AsyncSession = Depends(get_db),
                          user: User = Depends(current_user)):
    """No query: MDBList's top lists. A link or list id: that list. Any
    other text: list search. Answered from Movie Night's own copy while
    MDBList can't (services/mdblist_copy.py)."""
    try:
        mdb = await _mdb(db)
    except HTTPException:  # no key and no copy: an empty page, no notice
        return {"mode": "browse", "results": []}
    q = q.strip()
    try:
        ref = parse_list_url(q)
        if ref:
            summary = await mdb.get_list(ref)
            if summary is None:
                return {"mode": "link", "error": "That MDBList list wasn't found — it may be private."}
            return {"mode": "link", "results": await _mark(db, [summary])}
        if q:
            found = await cache.cached(f"mdblist:search:{q.lower()}", 3600.0, lambda: mdb.search_lists(q))
            return {"mode": "search", "results": await _mark(db, found)}
        top = await cache.cached("mdblist:top", 6 * 3600.0, mdb.top_lists)
        return {"mode": "browse", "results": await _mark(db, top)}
    except MDBListRefused:
        return {"mode": "browse", "error": f"{REFUSED}, and Movie Night has no copy of this yet. Try again later."}
    except Exception as exc:
        log.warning("MDBList explore failed", exc_info=True)
        raise HTTPException(status_code=502, detail="MDBList didn't answer — try again") from exc


@router.get("/mdblist/lists/{mdblist_id}", response_model=s.MDBListPreviewOut)
async def mdblist_preview(mdblist_id: int = Path(gt=0, le=2_000_000_000), db: AsyncSession = Depends(get_db),
                          user: User = Depends(current_user)):
    """A list before following it: details and its titles as cards (from
    Movie Night's own copy while MDBList can't answer)."""
    mdb = await _mdb(db)
    try:
        # Both cached 1 h; the preview has every title of the list. See
        # docs/limits.md.
        summary = await cache.cached(f"mdblist:list:{mdblist_id}", 3600.0, lambda: mdb.get_list({"id": str(mdblist_id)}))
        entries = await cache.cached(f"mdblist:items:{mdblist_id}", 3600.0, lambda: mdb.list_items(mdblist_id))
    except MDBListRefused as exc:
        raise HTTPException(status_code=503, detail=f"{REFUSED}, and Movie Night has no copy of this list yet. Try again later") from exc
    except Exception as exc:
        raise HTTPException(status_code=502, detail="MDBList didn't answer — try again") from exc
    if summary is None:
        raise HTTPException(status_code=404, detail="That MDBList list wasn't found — it may be private.")
    keys = list(dict.fromkeys(titles.tkey(m, t) for m, t, _, _ in entries))
    await titles.upsert(db, [{"key": titles.tkey(m, t), "media_type": m, "tmdb_id": t, "title": n, "year": y} for m, t, n, y in entries])
    st = await user_state(db, user)
    metas = await titles.ensure(db, clients.tmdb(), keys[:60], max_fetch=60)
    metas.update(await titles.get_many(db, keys[60:]))
    return {"list": (await _mark(db, [summary]))[0], "count": len(keys), "items": cards([metas.get(k) for k in keys], st),
            "not_shown": getattr(entries, "no_tmdb", 0)}


@router.post("/mdblist/follow", response_model=s.FollowOut)
async def mdblist_follow(body: s.FollowIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Add an MDBList list to the install's MDBList lists (kept in step daily)."""
    # Anyone signed in may follow a list, for everyone on the install; it is
    # then synced every night. No allowance (docs/limits.md).
    ref = parse_list_url(body.ref)
    if ref is None:
        raise HTTPException(status_code=422, detail="Paste an mdblist.com/lists/… link or a list number")
    try:
        lst = await lists.follow_mdblist(db, await _mdb(db), ref)
    except lists.ListError as exc:
        raise _http(exc) from exc
    except MDBListRefused as exc:
        raise HTTPException(status_code=503, detail=f"{REFUSED}, and Movie Night has no copy of this list yet. Try again later") from exc
    except HTTPException:
        raise
    except Exception as exc:
        log.warning("follow failed", exc_info=True)
        raise HTTPException(status_code=502, detail="MDBList didn't answer — try again") from exc
    per_media = (await lists.counts(db, [lst.id]))[lst.id]
    return {"id": lst.id, "count": per_media["movie"] + per_media["tv"]}
