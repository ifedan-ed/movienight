"""Profile tab: who you are, stats, history, ratings, requests, the rate
prompt, manual syncs and the full data export."""
from __future__ import annotations

import datetime as dt
import logging
from typing import Literal

from fastapi import APIRouter, Depends, HTTPException, Query
from fastapi.responses import JSONResponse
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db, require_admin, user_state
from app.clients.jellyfin import IncompleteRead
from app.core import cache
from app.core.config import settings
from app.models import EpisodeFavorite, EpisodeRating, Favorite, HistoryEntry, ListItem, User, UserList, UserRating, WatchlistEntry
from app.services import episodes, history, lists, media_requests, status, sync, titles
from app.services.cards import card, cards
from app.services.state import download_progress, seerr_status_label

log = logging.getLogger("movienight.profile")
router = APIRouter(tags=["profile"])

PAGE = 40  # history, ratings and requests rows per page, every page reachable (docs/limits.md)


async def jellyfin_link(db: AsyncSession, user: User) -> dict:
    return {"linked": bool(user.jellyfin_user_id), "can_play": bool(user.jellyfin_token_enc), "user_name": user.jellyfin_user_name,
            "linked_at": user.jellyfin_linked_at.isoformat() if user.jellyfin_linked_at else None,
            "server": settings.jellyfin_public_url, "plays_notice": await status.plays_notice(db, user)}


@router.get("/me", response_model=s.MeOut)
async def me(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    return {
        "user": user.username,
        "display_name": user.display_name,
        "email": user.email,
        "is_admin": user.is_admin,
        "stats": await history.stats(db, user.id),
        "jellyfin": await jellyfin_link(db, user),
        "features": {"requests": clients.seerr().enabled or settings.dry_run, "mdblist": settings.mdblist_enabled,
                     "ai": settings.ai_enabled, "email": settings.email_enabled, "dry_run": settings.dry_run},
        "scrobble_token_set": bool(user.scrobble_token_hash),
        "admin_notices": await status.notices(db) if user.is_admin else [],
        "limits": {"text_max_chars": settings.text_max_chars, "search_max_chars": settings.search_max_chars,
                   "name_max_chars": settings.name_max_chars, "reported_max_chars": settings.reported_max_chars,
                   "batch_max_items": settings.batch_max_items},
    }


@router.get("/history", response_model=s.HistoryOut)
async def get_history(page: int = Query(1, ge=1), media: s.MediaType | None = None,
                      db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    allrows = await history.grouped(db, user.id, media.value if media else None)
    window = allrows[(page - 1) * PAGE: page * PAGE]
    st = await user_state(db, user)
    metas = await titles.ensure(db, clients.tmdb(), [w["key"] for w in window if w["key"]], max_fetch=PAGE)
    # An episode's own heart and rating (not the show's), for Recently watched and History.
    def ep_key(w: dict) -> tuple[int, int, int] | None:
        if w["media_type"] != "episode" or not w["key"] or w["season"] is None or w["episode"] is None:
            return None
        return titles.split_key(w["key"])[1], w["season"], w["episode"]
    own = await episodes.marks(db, user.id, [k for w in window if (k := ep_key(w))])
    items = []
    for w in window:
        item = {**w, "card": card(metas[w["key"]], st) if w["key"] in metas else None}
        if (k := ep_key(w)) is not None:
            item.update(episode_favorite=own[k]["favorite"], episode_rating=own[k]["rating"])
        items.append(item)
    return {"items": items, "more": page * PAGE < len(allrows), "page": page, "total": len(allrows)}


@router.post("/history/remove", response_model=s.Ok)
async def remove_history(body: s.IdsIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Delete watches from the History page (any title, including ones with
    no TMDB match)."""
    await history.remove_entries(db, user.id, body.ids)
    return {}


@router.get("/ratings", response_model=s.RatingsOut)
async def get_ratings(page: int = Query(1, ge=1), db: AsyncSession = Depends(get_db),
                      user: User = Depends(current_user)):
    rated = list(await db.execute(select(UserRating.media_type, UserRating.tmdb_id)
                                  .where(UserRating.user_id == user.id).order_by(UserRating.rated_at.desc())))
    window = rated[(page - 1) * PAGE: page * PAGE]
    keys = [titles.tkey(m, t) for m, t in window]
    st = await user_state(db, user)
    metas = await titles.ensure(db, clients.tmdb(), keys, max_fetch=PAGE)
    return {"items": cards([metas.get(k) for k in keys], st), "more": page * PAGE < len(rated), "page": page, "total": len(rated)}


@router.get("/requests", response_model=s.RequestsOut)
async def get_requests(mine: bool = False, page: int = Query(1, ge=1), db: AsyncSession = Depends(get_db),
                       user: User = Depends(current_user)):
    """Seerr requests, newest first (everyone's, or yours), with live status
    and download progress: 40 a page, every page reachable ("more"), and
    how many in all."""
    skip = (page - 1) * PAGE
    try:
        results, total = await media_requests.recent(db, user, mine_only=mine, take=PAGE, skip=skip)
    except Exception as exc:
        raise HTTPException(status_code=502, detail="Couldn't reach Seerr") from exc
    st = await user_state(db, user)
    entries = []
    for r in results:
        m = r.get("media") or {}
        if m.get("tmdbId"):
            entries.append((titles.tkey("tv" if m.get("mediaType") == "tv" else "movie", m["tmdbId"]), r, m))
    metas = await titles.ensure(db, clients.tmdb(), [k for k, _, _ in entries], max_fetch=PAGE)
    out = []
    for k, r, m in entries:
        if k not in metas:
            continue  # counted in not_shown below
        c = card(metas[k], st)
        c.update({
            "requested_at": r.get("createdAt"),
            "request_status": media_requests.REQUEST_STATUS.get(r.get("status")),
            "media_status": seerr_status_label(m.get("status")),
            "progress": download_progress(m.get("downloadStatus")),
            "requested_by": (r.get("requestedBy") or {}).get("displayName"),
        })
        out.append(c)
    # A request Seerr has no TMDB id for, or TMDB can't find, can't be shown
    # as a card; the page says how many (not_shown) instead of hiding them.
    return {"items": out, "page": page, "more": skip + len(results) < total, "total": total,
            "not_shown": len(results) - len(out)}


@router.get("/rate-prompt", response_model=s.RatePromptOut)
async def rate_prompt(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    p = await history.rate_prompt(db, user.id)
    if p is None:
        return {"item": None}
    meta = (await titles.ensure(db, clients.tmdb(), [p["key"]], max_fetch=1)).get(p["key"]) or {}
    return {"item": {**p, "title": meta.get("title") or p["title"], "poster": meta.get("poster_path")}}


@router.post("/rate-prompt/dismiss", response_model=s.Ok)
async def dismiss_rate_prompt(body: s.KeyIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    await history.dismiss_rate_prompt(db, user.id, body.key)
    return {}


@router.post("/sync/plays", response_model=s.SyncOut)
async def sync_plays(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Pull your Jellyfin plays now (it also happens every few minutes)."""
    if not user.jellyfin_user_id:
        raise HTTPException(status_code=409, detail="Connect your Jellyfin account first")
    try:
        added = await sync.sync_plays(db, clients.jellyfin(), user)
    except IncompleteRead as exc:
        log.warning("manual plays sync: %s", exc)
        raise HTTPException(status_code=502, detail=f"{exc}. Nothing was added — try again") from exc
    except Exception as exc:
        log.warning("manual plays sync failed", exc_info=True)
        raise HTTPException(status_code=502, detail="Jellyfin didn't answer — try again") from exc
    return {"counts": {"plays": added}, "note": status.plays_undated(await status.get(db, status.plays(user.id)))}


@router.post("/sync/{what}", response_model=s.SyncOut)
async def run_sync(what: Literal["library", "lists"], db: AsyncSession = Depends(get_db), user: User = Depends(require_admin)):
    """Install-wide: rescan the Jellyfin library, or re-sync MDBList lists."""
    try:
        if what == "library":
            if not settings.jellyfin_user_id:
                raise HTTPException(status_code=409, detail="JELLYFIN_USER_ID isn't set")
            counts = await sync.sync_library(db, clients.jellyfin(), clients.meili(), settings.jellyfin_user_id)
        else:
            counts = {"lists": await lists.sync_all_mdblist(db, clients.mdblist())}
    except HTTPException:
        raise
    except sync.LibraryNotRead as exc:
        raise HTTPException(status_code=502, detail=str(exc)) from exc
    except Exception as exc:
        log.warning("manual sync %s failed", what, exc_info=True)
        raise HTTPException(status_code=502, detail=f"{what} sync failed ({exc.__class__.__name__})") from exc
    cache.invalidate("jf:")
    return {"counts": counts}


@router.get("/export", response_class=JSONResponse)
async def export(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Everything Movie Night keeps for you, as one JSON file."""
    def t(media, tid):
        return {"media_type": media, "tmdb_id": tid}

    uid = user.id
    own_lists = list(await db.scalars(select(UserList).where(UserList.owner_id == uid)))
    items: dict[int, list] = {}
    for list_id, media, tid in await db.execute(select(ListItem.list_id, ListItem.media_type, ListItem.tmdb_id)
                                                .where(ListItem.list_id.in_([l.id for l in own_lists] or [0]))
                                                .order_by(ListItem.list_id, ListItem.position)):
        items.setdefault(list_id, []).append(t(media, tid))
    data = {
        "exported_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "user": user.username,
        "watchlist": [{**t(w.media_type, w.tmdb_id), "added_at": w.added_at.isoformat(), "source": w.source}
                      for w in await db.scalars(select(WatchlistEntry).where(WatchlistEntry.user_id == uid, WatchlistEntry.removed_at.is_(None)))],
        "ratings": [{**t(r.media_type, r.tmdb_id), "rating": r.rating, "review": r.review, "rated_at": r.rated_at.isoformat(),
                     "source": r.source} for r in await db.scalars(select(UserRating).where(UserRating.user_id == uid))],
        "favorites": [t(f.media_type, f.tmdb_id) for f in await db.scalars(select(Favorite).where(Favorite.user_id == uid, Favorite.removed_at.is_(None)))],
        "episode_favorites": [{"tmdb_id": e.tmdb_id, "season": e.season, "episode": e.episode, "added_at": e.added_at.isoformat(),
                               "source": e.source}
                              for e in await db.scalars(select(EpisodeFavorite).where(EpisodeFavorite.user_id == uid)
                                                        .order_by(EpisodeFavorite.tmdb_id, EpisodeFavorite.season, EpisodeFavorite.episode))],
        "episode_ratings": [{"tmdb_id": e.tmdb_id, "season": e.season, "episode": e.episode, "rating": e.rating,
                             "rated_at": e.rated_at.isoformat(), "source": e.source}
                            for e in await db.scalars(select(EpisodeRating).where(EpisodeRating.user_id == uid)
                                                      .order_by(EpisodeRating.tmdb_id, EpisodeRating.season, EpisodeRating.episode))],
        "history": [{"media_type": h.media_type, "tmdb_id": h.tmdb_id, "title": h.title, "season": h.season, "episode": h.episode,
                     "watched_at": h.watched_at.isoformat(), "source": h.source}
                    for h in await db.scalars(select(HistoryEntry).where(HistoryEntry.user_id == uid).order_by(HistoryEntry.watched_at))],
        "lists": [{"name": l.name, "kind": l.kind, "description": l.description, "filters": l.filters_json, "items": items.get(l.id, [])}
                  for l in own_lists],
    }
    return JSONResponse(data, headers={"Content-Disposition": "attachment; filename=movienight-export.json"})
