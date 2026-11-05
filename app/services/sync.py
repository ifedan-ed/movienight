"""Syncs from Jellyfin, all additive to people's own records:

- the install's library -> library_items + the Meilisearch index (shared).
  Only a complete, successful read of the whole library changes it, and
  even then an empty one never empties it (sync_library).
- each linked person's Jellyfin plays, all of them -> their history
  (source "jellyfin")

A Jellyfin play that matches a watch already marked here (same title or
episode, within a day and a half) is folded into that row instead of
becoming a second one.
"""
from __future__ import annotations

import datetime as dt
import logging
from typing import Any

from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.clients.jellyfin import IncompleteRead, JellyfinError
from app.clients.meili import library_doc
from app.core import cache
from app.models import HistoryEntry, LibraryItem, User
from app.services import status
from app.services.state import forget_library

log = logging.getLogger("movienight.sync")

# A Jellyfin play this close to a watch marked here (by hand or "Finished
# it?") is that same watch: it is merged into that row, not added again.
# This and the other sync limits below are listed in docs/limits.md.
CLAIM_WINDOW = dt.timedelta(hours=36)


def parse_time(value: str | None) -> dt.datetime | None:
    if not value:
        return None
    try:
        t = dt.datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return None
    return t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)


def _tmdb(item: dict) -> int | None:
    raw = (item.get("ProviderIds") or {}).get("Tmdb")
    return int(raw) if raw and str(raw).isdigit() else None


# ---------------------------------------------------------------------------
# Library
# ---------------------------------------------------------------------------


class LibraryNotRead(Exception):
    """The library read wasn't complete (or listed nothing at all), so the
    library was left exactly as it was. The message says why, in plain
    words; admins see it in Profile (services/status.py)."""


async def _not_read(db: AsyncSession, problem: str) -> LibraryNotRead:
    await db.rollback()
    kept = await db.scalar(select(func.count()).select_from(LibraryItem)) or 0
    await status.record(db, status.LIBRARY, ok=False, at=status.now_iso(), problem=problem, kept=kept)
    log.warning("library sync: nothing changed: %s (%d titles kept)", problem, kept)
    return LibraryNotRead(f"{problem}. Nothing was removed; the library was left as it was ({kept:,} titles)")


async def sync_library(db: AsyncSession, jellyfin, meili, user_id: str) -> dict[str, int]:
    """Make library_items (and the library search index) match Jellyfin.

    The whole library is read first, page by page with no cap
    (JellyfinClient.library_items). Only after a complete, successful read
    are titles Jellyfin no longer lists removed. A read that fails or comes
    back incomplete (an error, a 404, an empty or non-JSON answer, a page
    that stops short) changes nothing, and nor does a complete read that
    lists no titles at all while titles are kept here: that is far likelier
    a broken answer, or a JELLYFIN_USER_ID user who lost access, than a
    library emptied on purpose. Either way LibraryNotRead is raised, the
    log says why and admins see it in Profile. See docs/limits.md."""
    try:
        items = await jellyfin.library_items(user_id)
    except Exception as exc:
        why = str(exc) if isinstance(exc, JellyfinError) else f"Jellyfin couldn't be read ({type(exc).__name__})"
        raise await _not_read(db, f"the library read was incomplete: {why}") from exc
    existing = {li.jellyfin_id: li for li in await db.scalars(select(LibraryItem))}
    if existing and not any(isinstance(it, dict) and it.get("Id") for it in items):
        raise await _not_read(db, "Jellyfin listed no movies or series at all" if not items
                              else f"Jellyfin listed {len(items):,} items, none of them with an id")
    try:
        removed, no_id, seen = await _apply(db, items, existing)
    except Exception as exc:
        log.exception("library sync: saving failed")
        raise await _not_read(db, f"saving the library failed ({type(exc).__name__})") from exc
    if no_id:
        log.warning("library sync: %d items came without an id and weren't stored", no_id)
    await status.record(db, status.LIBRARY, ok=True, at=status.now_iso(), last_good_at=status.now_iso(), problem=None,
                        items=len(seen), removed=len(removed), no_id=no_id, kept=len(seen))

    # Search index: send only new or changed documents, so the embedder only
    # works on what changed.
    indexed = 0
    if meili is not None:
        try:
            await meili.ensure_index()
            docs = [d for d in (library_doc(it) for it in items if isinstance(it, dict)) if d]
            current = await meili.indexed_texts()
            changed = [d for d in docs if current.get(d["id"]) != d["searchText"]]
            for i in range(0, len(changed), 500):
                await meili.replace_documents(changed[i:i + 500])
            await meili.delete_documents([i for i in current if i not in seen])
            indexed = len(changed)
        except Exception:
            log.exception("search index update failed")
    return {"items": len(seen), "removed": len(removed), "indexed": indexed, **({"without_id": no_id} if no_id else {})}


async def _apply(db: AsyncSession, items: list[dict[str, Any]], existing: dict[str, LibraryItem]) -> tuple[list[str], int, set[str]]:
    """Write a complete read: every title in it, and what Jellyfin no
    longer lists removed. One commit, so a failure changes nothing."""
    now = dt.datetime.now(dt.timezone.utc)
    seen: set[str] = set()
    no_id = 0
    for it in items:
        jf_id = it.get("Id") if isinstance(it, dict) else None
        if not jf_id:
            no_id += 1  # can't be stored without its id; counted and logged below
            continue
        seen.add(jf_id)
        row = existing.get(jf_id)
        if row is None:
            row = LibraryItem(jellyfin_id=jf_id)
            db.add(row)
        row.media_type = "tv" if it.get("Type") == "Series" else "movie"
        row.tmdb_id = _tmdb(it)
        row.title = it.get("Name") or ""
        row.year = it.get("ProductionYear")
        row.date_added = parse_time(it.get("DateCreated"))
        row.synced_at = now
    # A complete read: what Jellyfin no longer lists is gone.
    removed = [k for k in existing if k not in seen]
    for i in range(0, len(removed), 5000):
        await db.execute(delete(LibraryItem).where(LibraryItem.jellyfin_id.in_(removed[i:i + 5000])))
    await db.commit()
    forget_library()
    return removed, no_id, seen


# ---------------------------------------------------------------------------
# A person's Jellyfin plays -> their history
# ---------------------------------------------------------------------------


def play_ref(jf_id: str, when: dt.datetime) -> str:
    """One Jellyfin play: the item and when it was last played. The plugin and
    the plays sync build the same reference, so a play is recorded once."""
    return f"{str(jf_id).replace('-', '').lower()}@{when.isoformat()}"


async def unclaimed_watches(db: AsyncSession, user_id: int) -> list[HistoryEntry]:
    """Watches marked here (by hand, or "Finished it?") that Jellyfin may now report too."""
    # Only the last 60 days of them: a Jellyfin play older than that is added as its own row.
    return list(await db.scalars(select(HistoryEntry).where(
        HistoryEntry.user_id == user_id, HistoryEntry.source.in_(("manual", "player")), HistoryEntry.source_ref.is_(None),
        HistoryEntry.watched_at >= dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=60))))


def place_play(db: AsyncSession, user_id: int, when: dt.datetime, ref: str, entry: dict[str, Any],
               unclaimed: list[HistoryEntry]) -> bool:
    """Record one Jellyfin play: a matching watch marked here (within
    CLAIM_WINDOW) takes the play's reference, otherwise it becomes a new
    history row. True when a row was added."""
    twin = next((h for h in unclaimed
                 if h.media_type == entry["media_type"] and h.tmdb_id == entry["tmdb_id"] and entry["tmdb_id"]
                 and h.season == entry["season"] and h.episode == entry["episode"]
                 and abs((h.watched_at if h.watched_at.tzinfo else h.watched_at.replace(tzinfo=dt.timezone.utc)) - when) <= CLAIM_WINDOW),
                None)
    if twin is not None:
        twin.source, twin.source_ref = "jellyfin", ref
        unclaimed.remove(twin)
        return False
    db.add(HistoryEntry(user_id=user_id, watched_at=when, source="jellyfin", source_ref=ref, **entry))
    return True


async def sync_plays(db: AsyncSession, jellyfin, user: User) -> int:
    """Add this person's new Jellyfin plays to their history.

    Every played item is read, page by page, with no cap (the old 2,000
    most recent is gone). A read that doesn't come back whole raises
    IncompleteRead and adds nothing; the next sync (5 min) reads again. The
    outcome is kept per person (status "plays:<id>"): a failed read is shown
    to admins and to the person until a later read is complete, and so is
    how many plays Jellyfin lists without a date (they can't be put on a
    timeline, so they aren't added). See docs/limits.md."""
    if not user.jellyfin_user_id:
        return 0
    name = status.plays(user.id)
    try:
        played = await jellyfin.played(user.jellyfin_user_id, "Movie,Episode")
    except Exception as exc:
        problem = str(exc) if isinstance(exc, JellyfinError) else f"Jellyfin couldn't be read ({type(exc).__name__})"
        await db.rollback()
        await status.record(db, name, ok=False, at=status.now_iso(), problem=problem,
                            status=exc.status if isinstance(exc, JellyfinError) else None)
        raise
    series_tmdb = {jf_id: tid for jf_id, tid in await db.execute(
        select(LibraryItem.jellyfin_id, LibraryItem.tmdb_id).where(LibraryItem.media_type == "tv"))}
    refs = set(await db.scalars(select(HistoryEntry.source_ref)
                                .where(HistoryEntry.user_id == user.id, HistoryEntry.source == "jellyfin")))
    unclaimed = await unclaimed_watches(db, user.id)
    added = undated = 0
    for it in played:
        jf_id = it.get("Id")
        when = parse_time((it.get("UserData") or {}).get("LastPlayedDate"))
        if not jf_id or when is None:
            # Marked played without a date (or sent without an id): nothing to
            # put on a timeline. Counted, and the person is told (status).
            undated += 1
            continue
        ref = play_ref(jf_id, when)
        if ref in refs:
            continue
        if it.get("Type") == "Episode":
            entry = dict(media_type="episode", tmdb_id=series_tmdb.get(it.get("SeriesId")),
                         season=it.get("ParentIndexNumber"), episode=it.get("IndexNumber"),
                         episode_title=it.get("Name"), title=it.get("SeriesName") or "")
        else:
            entry = dict(media_type="movie", tmdb_id=_tmdb(it), season=None, episode=None, episode_title=None,
                         title=it.get("Name") or "")
        if place_play(db, user.id, when, ref, entry, unclaimed):
            added += 1
        refs.add(ref)
    await db.commit()
    if added:
        cache.invalidate(f"jf:continue:{user.id}")
    now = status.now_iso()
    await status.record(db, name, ok=True, at=now, last_good_at=now, problem=None, status=None, undated=undated)
    return added


async def sync_all_plays(db: AsyncSession, jellyfin) -> dict[int, int]:
    """New plays per person (by numeric id: the log never names anyone)."""
    out = {}
    # By id, each person loaded afresh: a failed read rolls the session back,
    # which expires everyone loaded with it, and reading an expired person
    # stopped the sync for everyone after them.
    for uid in list(await db.scalars(select(User.id).where(User.jellyfin_user_id.isnot(None)).order_by(User.id))):
        try:
            user = await db.get(User, uid)
            if user is None or not user.jellyfin_user_id:
                continue
            out[uid] = await sync_plays(db, jellyfin, user)
        except IncompleteRead as exc:
            # Expected while Jellyfin can't give a whole answer (a deleted
            # account answers 404 every time): one line, no traceback. Admins
            # and the person see it in Profile (status.plays).
            log.warning("plays sync for user %d: nothing added: %s", uid, exc)
        except Exception:
            await db.rollback()
            log.warning("plays sync for user %d failed", uid, exc_info=True)
    return out
