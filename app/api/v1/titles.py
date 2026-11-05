"""A single title: page data, live request status, scores, seasons and
episodes, playing, and every action on it (request, watchlist, watched,
rating with notes, favourite, lists) or on one episode (tick, heart, rating)."""
from __future__ import annotations

import datetime as dt
import logging
from zoneinfo import ZoneInfo

from fastapi import APIRouter, Depends, HTTPException, Path, Query
from sqlalchemy import delete, select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db, user_state
from app.core import cache
from app.core.config import settings
from app.models import EpisodeFavorite, EpisodeRating, Favorite, User, UserRating, WatchlistEntry
from app.services import detail, episodes, history, lists, media_requests, playback, rows, scores, titles
from app.services.cards import cards

log = logging.getLogger("movienight.titles")
router = APIRouter(prefix="/title/{media}/{tmdb_id}", tags=["titles"])

TmdbId = Path(gt=0, le=10_000_000_000)


def _now() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def parse_when(value: str | None) -> dt.datetime | None:
    """ISO date or datetime -> aware datetime. A bare date means that evening
    in the install's time zone."""
    if not value:
        return None
    try:
        when = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise HTTPException(status_code=422, detail="watched_at must be an ISO date") from exc
    if when.tzinfo is None:
        if len(value) <= 10:
            when = when.replace(hour=20)
        when = when.replace(tzinfo=ZoneInfo(settings.timezone))
    if when > _now() + dt.timedelta(days=1):
        raise HTTPException(status_code=422, detail="watched_at is in the future")
    if when.year < 1900:
        raise HTTPException(status_code=422, detail="watched_at is too early")
    return when


def _tv_only(media: s.MediaType) -> None:
    if media != s.MediaType.tv:
        raise HTTPException(status_code=404, detail="Only shows have seasons")


@router.get("", response_model=s.TitleDetail)
async def get_title(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                    user: User = Depends(current_user)):
    st = await user_state(db, user)
    try:
        data = await detail.build(db, user, media.value, tmdb_id, st)
    except Exception as exc:
        log.warning("detail %s/%s failed", media.value, tmdb_id, exc_info=True)
        raise HTTPException(status_code=502, detail="Couldn't reach TMDB for this title") from exc
    if data is None:
        raise HTTPException(status_code=404, detail="Title not found")
    return data


@router.get("/status", response_model=s.TitleState)
async def get_status(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                     user: User = Depends(current_user)):
    st = await user_state(db, user)
    return await detail.live_status(db, media.value, tmdb_id, st)


@router.get("/scores", response_model=s.ScoresOut)
async def get_scores(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                     user: User = Depends(current_user)):
    """IMDb, Rotten Tomatoes, Metacritic, Letterboxd and Trakt scores, and a
    plain note when MDBList couldn't refresh them (it refused or didn't
    answer)."""
    got, note = await scores.scores_and_note(db, media.value, tmdb_id)
    return {"scores": got, "note": note}


@router.get("/related", response_model=s.RelatedOut)
async def get_related(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                      user: User = Depends(current_user)):
    """"More like this" and, for movies, the rest of the collection."""
    m = media.value
    st = await user_state(db, user)
    tmdb = clients.tmdb()
    recs = await rows.recommendations(tmdb, m, tmdb_id)
    out: dict = {"recommendations": cards(await titles.upsert_results(db, recs, m), st), "collection": None}
    d = await detail.details_cached(tmdb, m, tmdb_id)
    coll = (d or {}).get("belongs_to_collection")
    if coll and coll.get("id"):
        try:
            c = await cache.cached(f"collection:{coll['id']}", 6 * 3600.0, lambda: tmdb.collection(coll["id"]))
            parts = sorted((c or {}).get("parts") or [], key=lambda p: p.get("release_date") or "9999")
            out["collection"] = {"name": coll.get("name"), "items": cards(await titles.upsert_results(db, parts, "movie"), st)}
        except Exception:
            log.info("collection %s unavailable", coll.get("id"))
    return out


@router.post("/request", response_model=s.RequestOut)
async def request_title(media: s.MediaType, body: s.RequestIn | None = None, tmdb_id: int = TmdbId,
                        db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Send the request to Seerr as this person. A 409 from Seerr (it already
    has one) is not an error: the button moves to that request's status.
    Shows can ask for some seasons only."""
    m = media.value
    seasons = (body.seasons if body else None) if m == "tv" else None
    try:
        result = await media_requests.create(db, user, m, tmdb_id, seasons=seasons or None)
    except media_requests.RequestError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    except Exception as exc:
        log.warning("request %s/%s failed", m, tmdb_id, exc_info=True)
        raise HTTPException(status_code=502, detail="Couldn't reach Seerr — try again") from exc
    st = await user_state(db, user, force_seerr=True)
    state = await detail.live_status(db, m, tmdb_id, st)
    if result.get("dry_run"):
        state["request"] = "dry_run"
    elif state.get("request") is None and not state.get("in_library"):
        state["request"] = "pending"  # Seerr has the request but hasn't indexed it yet
    return {"ok": True, "already": bool(result.get("already")), "state": state}


@router.post("/watchlist", response_model=s.WatchlistOut)
async def add_watchlist(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                        user: User = Depends(current_user)):
    row = await db.scalar(select(WatchlistEntry).where(WatchlistEntry.user_id == user.id, WatchlistEntry.media_type == media.value,
                                                       WatchlistEntry.tmdb_id == tmdb_id))
    if row is None:
        db.add(WatchlistEntry(user_id=user.id, media_type=media.value, tmdb_id=tmdb_id, source="app"))
    elif row.removed_at is not None:
        row.removed_at, row.added_at = None, _now()
    await db.commit()
    return {"watchlist": True}


@router.delete("/watchlist", response_model=s.WatchlistOut)
async def remove_watchlist(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                           user: User = Depends(current_user)):
    row = await db.scalar(select(WatchlistEntry).where(WatchlistEntry.user_id == user.id, WatchlistEntry.media_type == media.value,
                                                       WatchlistEntry.tmdb_id == tmdb_id))
    if row is not None and row.removed_at is None:
        row.removed_at = _now()
        await db.commit()
    return {"watchlist": False}


@router.post("/watched", response_model=s.WatchesOut)
async def add_watched(body: s.WatchedIn, media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                      user: User = Depends(current_user)):
    when = parse_when(body.watched_at)
    key = titles.tkey(media.value, tmdb_id)
    meta = (await titles.ensure(db, clients.tmdb(), [key], max_fetch=1)).get(key) or {}
    entry = await history.mark_watched(db, user.id, media.value, tmdb_id, meta.get("title") or "", when)
    return {"id": entry.id, "watches": await history.title_watches(db, user.id, media.value, tmdb_id)}


@router.post("/unwatch", response_model=s.WatchesOut)
async def remove_watched(body: s.IdsIn, media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                         user: User = Depends(current_user)):
    await history.remove_entries(db, user.id, body.ids, media.value, tmdb_id)
    return {"watches": await history.title_watches(db, user.id, media.value, tmdb_id)}


@router.post("/rating", response_model=s.RatingOut)
async def set_rating(body: s.RatingIn, media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                     user: User = Depends(current_user)):
    review = (body.review or "").strip() or None
    row = await db.scalar(select(UserRating).where(UserRating.user_id == user.id, UserRating.media_type == media.value,
                                                   UserRating.tmdb_id == tmdb_id))
    if row is None:
        db.add(UserRating(user_id=user.id, media_type=media.value, tmdb_id=tmdb_id, rating=body.rating, review=review, source="app"))
    else:
        row.rating, row.rated_at, row.source = body.rating, _now(), "app"
        if body.review is not None:
            row.review = review
        review = row.review
    await db.commit()
    return {"rating": body.rating, "review": review}


@router.delete("/rating", response_model=s.RatingOut)
async def clear_rating(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                       user: User = Depends(current_user)):
    row = await db.scalar(select(UserRating).where(UserRating.user_id == user.id, UserRating.media_type == media.value,
                                                   UserRating.tmdb_id == tmdb_id))
    if row is not None:
        await db.delete(row)
        await db.commit()
    return {"rating": None}


@router.post("/favorite", response_model=s.FavoriteOut)
async def add_favorite(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                       user: User = Depends(current_user)):
    row = await db.scalar(select(Favorite).where(Favorite.user_id == user.id, Favorite.media_type == media.value,
                                                 Favorite.tmdb_id == tmdb_id))
    if row is None:
        db.add(Favorite(user_id=user.id, media_type=media.value, tmdb_id=tmdb_id, source="app"))
    elif row.removed_at is not None:
        row.removed_at, row.added_at = None, _now()
    await db.commit()
    return {"favorite": True}


@router.delete("/favorite", response_model=s.FavoriteOut)
async def remove_favorite(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                          user: User = Depends(current_user)):
    row = await db.scalar(select(Favorite).where(Favorite.user_id == user.id, Favorite.media_type == media.value,
                                                 Favorite.tmdb_id == tmdb_id))
    if row is not None and row.removed_at is None:
        row.removed_at = _now()
        await db.commit()
    return {"favorite": False}


@router.get("/lists", response_model=s.ListsForTitleOut)
async def title_lists(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                      user: User = Depends(current_user)):
    return {"lists": await lists.memberships(db, user.id, media.value, tmdb_id)}


# ---------------------------------------------------------------------------
# Seasons and episodes
# ---------------------------------------------------------------------------


@router.get("/season/{number}", response_model=s.SeasonOut)
async def get_season(media: s.MediaType, number: int = Path(ge=0, le=s.DB_INT), tmdb_id: int = TmdbId,
                     db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    _tv_only(media)
    try:
        page = await episodes.season_page(db, user, tmdb_id, number)
    except Exception as exc:
        log.warning("season %s/%s failed", tmdb_id, number, exc_info=True)
        raise HTTPException(status_code=502, detail="Couldn't reach TMDB for this season") from exc
    if page is None:
        raise HTTPException(status_code=404, detail="Season not found")
    return page


@router.get("/next", response_model=s.NextEpisodeOut)
async def get_next(media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                   user: User = Depends(current_user)):
    _tv_only(media)
    nxt = await episodes.next_episode(db, user, tmdb_id)
    if nxt:
        # The show page's Up next line shows (and changes) this episode's own heart and rating.
        nxt = {**nxt, **(await episodes.marks(db, user.id, [(tmdb_id, nxt["season"], nxt["episode"])]))[(tmdb_id, nxt["season"], nxt["episode"])]}
    return {"next": nxt}


@router.post("/episodes", response_model=s.ChangedOut)
async def mark_episodes(body: s.EpisodesIn, media: s.MediaType, tmdb_id: int = TmdbId,
                        db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Tick or untick episodes (a whole season is a list of them)."""
    _tv_only(media)
    when = parse_when(body.watched_at)
    n = await episodes.set_watched(db, user, tmdb_id, [(e.season, e.episode) for e in body.episodes], body.watched, when)
    return {"changed": n}


def _episode_row(model, user_id: int, tmdb_id: int, season: int, episode: int):
    """WHERE for one person's row of one episode (episode_favorites, episode_ratings)."""
    return (model.user_id == user_id, model.tmdb_id == tmdb_id, model.season == season, model.episode == episode)


# Each write on one episode is a single statement (an upsert or a delete), so
# taps from two devices, or a phone's outbox replaying, can't race each other
# into an error: the last one to reach the database wins.


@router.post("/episode/{season}/{episode}/favorite", response_model=s.FavoriteOut)
async def like_episode(media: s.MediaType, season: int = Path(ge=0, le=s.DB_INT), episode: int = Path(ge=0, le=s.DB_INT),
                       tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Like one episode (the heart on the season page). Liking it again
    changes nothing. It doesn't favourite the show."""
    _tv_only(media)
    await db.execute(pg_insert(EpisodeFavorite)
                     .values(user_id=user.id, tmdb_id=tmdb_id, season=season, episode=episode, added_at=_now(), source="app")
                     .on_conflict_do_nothing(constraint="uq_episode_favorite"))
    await db.commit()
    return {"favorite": True}


@router.delete("/episode/{season}/{episode}/favorite", response_model=s.FavoriteOut)
async def unlike_episode(media: s.MediaType, season: int = Path(ge=0, le=s.DB_INT), episode: int = Path(ge=0, le=s.DB_INT),
                         tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Unlike one episode: its row is deleted. An episode that wasn't
    liked is not an error."""
    _tv_only(media)
    await db.execute(delete(EpisodeFavorite).where(*_episode_row(EpisodeFavorite, user.id, tmdb_id, season, episode)))
    await db.commit()
    return {"favorite": False}


@router.put("/episode/{season}/{episode}/rating", response_model=s.EpisodeRatingOut)
@router.post("/episode/{season}/{episode}/rating", response_model=s.EpisodeRatingOut)
async def rate_episode(body: s.EpisodeRatingIn, media: s.MediaType, season: int = Path(ge=0, le=s.DB_INT),
                       episode: int = Path(ge=0, le=s.DB_INT), tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
                       user: User = Depends(current_user)):
    """Rate one episode, 1 to 10 like a title (the stars next to its heart on
    the season page); rating it again replaces the rating. PUT or POST. It
    doesn't rate the show, and it isn't counted in Ratings, Your ratings, the
    community average, the AI picks or digests."""
    _tv_only(media)
    now = _now()
    await db.execute(pg_insert(EpisodeRating)
                     .values(user_id=user.id, tmdb_id=tmdb_id, season=season, episode=episode, rating=body.rating,
                             rated_at=now, source="app")
                     .on_conflict_do_update(constraint="uq_episode_rating",
                                            set_={"rating": body.rating, "rated_at": now, "source": "app"}))
    await db.commit()
    return {"rating": body.rating}


@router.delete("/episode/{season}/{episode}/rating", response_model=s.EpisodeRatingOut)
async def unrate_episode(media: s.MediaType, season: int = Path(ge=0, le=s.DB_INT), episode: int = Path(ge=0, le=s.DB_INT),
                         tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Remove one episode's rating: its row is deleted. An episode that
    wasn't rated is not an error."""
    _tv_only(media)
    await db.execute(delete(EpisodeRating).where(*_episode_row(EpisodeRating, user.id, tmdb_id, season, episode)))
    await db.commit()
    return {"rating": None}


# ---------------------------------------------------------------------------
# Playing
# ---------------------------------------------------------------------------


@router.get("/play", response_model=s.PlayInfo)
async def play_info(media: s.MediaType, tmdb_id: int = TmdbId, season: int | None = Query(None, ge=0, le=s.DB_INT),
                    episode: int | None = Query(None, ge=0, le=s.DB_INT), db: AsyncSession = Depends(get_db),
                    user: User = Depends(current_user)):
    try:
        return await playback.play_info(db, user, media.value, tmdb_id, season, episode)
    except Exception as exc:
        log.warning("play info %s/%s failed", media.value, tmdb_id, exc_info=True)
        raise HTTPException(status_code=502, detail="Couldn't reach Jellyfin") from exc


@router.post("/play", response_model=s.PlayOut)
async def play(body: s.PlayIn, media: s.MediaType, tmdb_id: int = TmdbId, db: AsyncSession = Depends(get_db),
               user: User = Depends(current_user)):
    try:
        return await playback.start(db, user, media.value, tmdb_id, body.season, body.episode, body.player,
                                    body.subtitle, body.return_to)
    except playback.PlayError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
