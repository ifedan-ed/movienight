"""Outside scores for a title page.

- IMDb: IMDb's own rating, from the IMDb file loaded every night
  (services/imdb.py), so it shows even when MDBList is down or not set up.
- Rotten Tomatoes critics and audience, Metacritic, Letterboxd, Trakt (and
  IMDb for a title the IMDb file lacks): from MDBList, kept in title_extras
  and asked again after a week. When MDBList can't answer or refuses (its
  own limit for the key), the kept scores show and the page says so in a
  plain note (scores_and_note); a score MDBList stops sending is kept rather
  than cleared.
- Rotten Tomatoes critics for a library title MDBList has none for: from
  Jellyfin."""
from __future__ import annotations

import datetime as dt
import logging
from typing import Any

from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.clients.mdblist import MDBListRefused, ratings_of
from app.core import cache
from app.core.config import settings
from app.models import Title, TitleExtra
from app.services import imdb
from app.services.state import library_map
from app.services.titles import tkey

log = logging.getLogger("movienight.scores")

FRESH = dt.timedelta(days=7)
FIELDS = ("imdb_rating", "imdb_votes", "rt_critics", "rt_audience", "rt_url", "metacritic", "letterboxd",
          "trakt_rating", "trakt_votes", "mdblist_score")


def _out(row: TitleExtra | None) -> dict[str, Any] | None:
    if row is None:
        return None
    out = {f: getattr(row, f) for f in FIELDS}
    return out if any(v is not None for v in out.values()) else None


def _fresh(row: TitleExtra | None) -> bool:
    if row is None:
        return False
    at = row.fetched_at if row.fetched_at.tzinfo else row.fetched_at.replace(tzinfo=dt.timezone.utc)
    return at > dt.datetime.now(dt.timezone.utc) - FRESH


def _with(out: dict[str, Any] | None, extra: dict[str, Any] | None) -> dict[str, Any] | None:
    if not extra:
        return out
    return {**(out or {f: None for f in FIELDS}), **extra}


async def _imdb(db: AsyncSession, media: str, tmdb_id: int) -> dict[str, Any] | None:
    """IMDb's own rating, by the title's IMDb id (from TMDB)."""
    t = await db.get(Title, tkey(media, tmdb_id))
    return await imdb.rating(db, t.imdb_id if t else None)


async def cached_scores(db: AsyncSession, media: str, tmdb_id: int) -> dict[str, Any] | None:
    """What is kept here, without asking anyone."""
    return _with(_out(await db.get(TitleExtra, tkey(media, tmdb_id))), await _imdb(db, media, tmdb_id))


def _note(row: TitleExtra | None, why: str) -> str:
    kept = _out(row)
    tail = (f"showing the ones kept from {row.fetched_at:%b %-d, %Y}" if kept and row is not None
            else "there are none kept for this title yet")
    return f"Rotten Tomatoes, Metacritic, Letterboxd and Trakt scores couldn't be refreshed: {why}. Movie Night asks again later; {tail}."


async def _from_mdblist(db: AsyncSession, media: str, tmdb_id: int) -> tuple[dict[str, Any] | None, str | None]:
    """The stored scores while fresh, else a new MDBList lookup (falls back to
    the stored ones when it can't, with a plain note saying why)."""
    key = tkey(media, tmdb_id)
    row = await db.get(TitleExtra, key)
    mdb = clients.mdblist()
    if _fresh(row) or not mdb.enabled:
        return _out(row), None
    try:
        info = await mdb.media_info(media, tmdb_id)
    except MDBListRefused:
        return _out(row), _note(row, "MDBList refused Movie Night's requests (its own limit for this install's key)")
    except Exception:
        log.warning("MDBList scores for %s failed", key, exc_info=True)
        return _out(row), _note(row, "MDBList didn't answer")
    # Only the scores MDBList sent: one it leaves out keeps its kept value.
    values: dict[str, Any] = {k: v for k, v in (ratings_of(info) if info else {}).items() if k in FIELDS and v is not None}
    values["fetched_at"] = dt.datetime.now(dt.timezone.utc)
    stmt = pg_insert(TitleExtra).values(key=key, **values)
    await db.execute(stmt.on_conflict_do_update(index_elements=[TitleExtra.key], set_=values))
    await db.commit()
    return _out(await db.get(TitleExtra, key, populate_existing=True)), None


async def _jellyfin_critics(db: AsyncSession, media: str, tmdb_id: int) -> int | None:
    """Rotten Tomatoes critics score that Jellyfin's OMDb plugin keeps for
    titles in the library (CriticRating)."""
    jf_id = (await library_map(db)).get((media, tmdb_id))
    jf = clients.jellyfin()
    if not jf_id or not settings.jellyfin_user_id or not jf.enabled:
        return None
    try:
        item = await cache.cached(f"jf-critic:{jf_id}", 86400.0,
                                  lambda: jf.item(settings.jellyfin_user_id, jf_id, fields="ProviderIds"))
    except Exception:
        return None
    value = (item or {}).get("CriticRating")
    return int(value) if isinstance(value, (int, float)) else None


async def scores_and_note(db: AsyncSession, media: str, tmdb_id: int) -> tuple[dict[str, Any] | None, str | None]:
    """Scores for the title page: MDBList's (fresh or kept) with IMDb's own
    rating over its IMDb one; when there is no Rotten Tomatoes score, a
    title in the library uses Jellyfin's. The note, when there is one, says
    in plain words why MDBList's scores couldn't be refreshed."""
    mdb_scores, note = await _from_mdblist(db, media, tmdb_id)
    out = _with(mdb_scores, await _imdb(db, media, tmdb_id))
    if out is None or out.get("rt_critics") is None:
        critics = await _jellyfin_critics(db, media, tmdb_id)
        if critics is not None:
            out = {**(out or {f: None for f in FIELDS}), "rt_critics": critics}
    return out, note
