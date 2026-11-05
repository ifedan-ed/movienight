"""Home tabs, their rows, "See all" grids, Discover and the Home rows settings."""
from __future__ import annotations

from typing import Literal

from fastapi import APIRouter, Depends, Query
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db, user_state
from app.core.config import settings
from app.models import User
from app.services import home_config, lists, rows, titles
from app.services.cards import cards
from app.services.titles import MOVIE_GENRES, TV_GENRES

router = APIRouter(tags=["home"])

Tab = Literal["for_you", "movies", "shows"]
RowKey = Query(pattern=r"^[a-z_]+(:[a-z0-9:]+)?$", max_length=60)


@router.get("/home", response_model=s.HomeOut)
async def home(tab: Tab = "for_you", db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """The tab's enabled rows, in order; the page then loads each one."""
    visible = await home_config.visible_lists(db, user.id)
    names = {l.id: l.name for l in visible}
    return {
        "tab": tab,
        "rows": [
            {"key": r["key"], "title": home_config.row_title(tab, r["key"], names),
             "multi": r["key"] in ("because", "ai"), "style": "landscape" if r["key"] == "continue" else "poster"}
            for r in await home_config.get_config(db, user.id, tab, visible) if r["on"]
        ],
    }


@router.get("/hero", response_model=s.CardsOut)
async def hero(tab: Tab = "for_you", db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    st = await user_state(db, user)
    return {"items": await rows.hero(db, user, tab, st)}


@router.get("/rows/{key}", response_model=s.RowsOut)
async def row(key: str, tab: Tab = "for_you", db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    st = await user_state(db, user)
    try:
        built = await rows.build_rows(db, user, tab, key, st)
    except rows.TMDBDown:
        # TMDB couldn't send this row and no copy is kept: the row says so.
        return {"rows": [], "note": rows.TMDB_DOWN_ROW}
    # "Picked for you" with no rows says why (being made, or a failed rebuild
    # and when it is tried again) instead of silently showing nothing.
    return {"rows": built, "note": rows.ai_note(user.id, bool(built)) if key == "ai" else None}


@router.get("/browse", response_model=s.PageOut)
async def browse(key: str = RowKey, tab: Tab = "for_you", page: int = Query(1, ge=1),
                 db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Grid behind a row's "See all", one page at a time."""
    st = await user_state(db, user)
    items, more = await rows.load_items(db, user, tab, key, st, page=page, size=rows.PAGE_SIZE)
    names = {l.id: l.name for l in await home_config.visible_lists(db, user.id)}
    note = rows.end_note(page, items, more) if key.partition(":")[0] in rows.TMDB_KINDS else None
    return {"title": home_config.row_title(tab, key, names), "items": items, "more": more, "page": page, "note": note}


@router.get("/discover", response_model=s.PageOut)
async def discover(
    media: s.MediaType = s.MediaType.movie,
    genre: str | None = Query(None, pattern=r"^\d{1,6}$"),
    provider: str | None = Query(None, pattern=r"^\d{1,6}$"),
    year_from: int | None = Query(None, ge=1870, le=2100),
    year_to: int | None = Query(None, ge=1870, le=2100),
    min_rating: float | None = Query(None, ge=0, le=10),
    language: str | None = Query(None, pattern=r"^[a-zA-Z]{2,3}$"),
    sort: str | None = Query(None, max_length=settings.name_max_chars),  # NAME_MAX_CHARS
    page: int = Query(1, ge=1),
    db: AsyncSession = Depends(get_db),
    user: User = Depends(current_user),
):
    """Filtered TMDB browsing (the Discover tab); the same filters make a smart list."""
    f = lists.clean_filters({"media": media.value, "genre": genre, "provider": provider, "year_from": year_from,
                             "year_to": year_to, "min_rating": min_rating, "language": language, "sort": sort})
    params = lists.tmdb_filters(f, settings.watch_region)
    results = await rows.discover(clients.tmdb(), f["media"], page, **params)
    st = await user_state(db, user)
    items, more = cards(await titles.upsert_results(db, results, f["media"]), st), rows.tmdb_more(results, page)
    return {"items": items, "more": more, "page": page, "note": rows.end_note(page, items, more)}


async def _config_out(db: AsyncSession, user: User, tab: str) -> dict:
    visible = await home_config.visible_lists(db, user.id)
    by_id = {l.id: l for l in visible}
    names = {l.id: l.name for l in visible}
    out = []
    for r in await home_config.get_config(db, user.id, tab, visible):
        entry = {**r, "title": home_config.row_title(tab, r["key"], names)}
        if r["key"].startswith("list:"):
            lst = by_id.get(int(r["key"].split(":")[1]))
            entry.update(list=True, subtitle=home_config.list_by_line(lst) if lst else None,
                         group=home_config.list_group(lst) if lst else None)
        out.append(entry)
    return {"tab": tab, "rows": out}


@router.get("/home/config", response_model=s.HomeConfigOut)
async def get_config(tab: Tab = "for_you", db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    return await _config_out(db, user, tab)


@router.put("/home/config", response_model=s.HomeConfigOut)
async def put_config(body: s.HomeConfigIn, tab: Tab = "for_you", db: AsyncSession = Depends(get_db),
                     user: User = Depends(current_user)):
    await home_config.save_config(db, user.id, tab, [{"key": r.key, "on": r.on} for r in body.rows])
    return await _config_out(db, user, tab)


@router.post("/home/config/reset", response_model=s.HomeConfigOut)
async def reset_config(tab: Literal["for_you", "movies", "shows", "all"] = "for_you", db: AsyncSession = Depends(get_db),
                       user: User = Depends(current_user)):
    for t in (home_config.TABS if tab == "all" else (tab,)):
        await home_config.reset_config(db, user.id, t)
    return await _config_out(db, user, "for_you" if tab == "all" else tab)


@router.get("/genres", response_model=s.GenresOut)
async def genres(media: s.MediaType = s.MediaType.movie):
    table = TV_GENRES if media == s.MediaType.tv else MOVIE_GENRES
    return {"genres": [{"id": k, "name": v} for k, v in sorted(table.items(), key=lambda x: x[1])]}


@router.get("/providers", response_model=s.ProvidersOut)
async def providers():
    return {"providers": [{"id": k, "name": v} for k, v in home_config.PROVIDERS.items()]}
