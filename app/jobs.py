"""Background jobs, run on the web process's own event loop (one process,
small workload). Times are in the install's time zone (TIMEZONE). The first
library sync runs 20 s after start (`soon` below; the other first runs are
counted from it); RUN_JOBS=false turns every job off. See docs/limits.md.

- every 5 min, first 60 s after start: each linked person's Jellyfin plays
  -> their history (the webhook makes this instant; this catches the rest)
- every 30 min: the Jellyfin library + search index (only a complete read
  changes it; services/sync.py)
- daily 02:30: IMDb's ratings loaded from IMDb's own daily file; loaded
  3 min 20 s after start when there are none yet
- daily 04:30: MDBList lists kept in step with their originals, and Movie
  Night's own copy of MDBList's top lists (services/mdblist_copy.py)
- daily 05:00: every stale title's details refreshed from TMDB (no cap)
- Sunday 03:00: the search catalog (every rated TMDB title) refreshed; built
  2 min 20 s after start when it has never been built
- Monday 09:00: weekly digest; 1st of the month 09:30: month in review;
  1 January 10:30: year in review (only with SMTP_HOST set). APScheduler
  keeps its schedule in memory, so a run due while the app was down (a
  restart or a deploy across 09:00) would be lost, and a run in which a
  digest couldn't be sent (SMTP down) isn't done: every hour, first 4 min
  20 s after start, any kind whose latest run isn't done is run again
  (digest.missed); a digest already sent is never sent twice. One digest
  run at a time (_digest_lock)
"""
from __future__ import annotations

import asyncio
import datetime as dt
import logging

from apscheduler.schedulers.asyncio import AsyncIOScheduler

from app import clients
from app.core import cache
from app.core.config import settings
from app.core.db import session
from app.services import catalog, digest, imdb, lists, mdblist_copy, sync, titles

log = logging.getLogger("movienight.jobs")


async def plays_job() -> None:
    if not clients.jellyfin().enabled:
        return
    async with session() as db:
        try:
            added = await sync.sync_all_plays(db, clients.jellyfin())
            if any(added.values()):
                log.info("jellyfin plays, new per user id: %s", {k: v for k, v in added.items() if v})
        except Exception:
            log.exception("jellyfin plays sync failed")


async def library_job() -> None:
    if not clients.jellyfin().enabled or not settings.jellyfin_user_id:
        return
    async with session() as db:
        try:
            log.info("library sync: %s", await sync.sync_library(db, clients.jellyfin(), clients.meili(), settings.jellyfin_user_id))
            cache.invalidate("jf:")
        except sync.LibraryNotRead:
            pass  # nothing changed; already logged and shown to admins in Profile
        except Exception:
            log.exception("library sync failed")


async def mdblist_job() -> None:
    if not settings.mdblist_enabled:
        return
    async with session() as db:
        try:
            log.info("MDBList lists synced: %d", await lists.sync_all_mdblist(db, clients.mdblist()))
        except Exception:
            log.exception("MDBList lists sync failed")
    async with session() as db:
        log.info("MDBList top lists copied: %d", await mdblist_copy.refresh(db, clients.mdblist()))


async def imdb_job(only_if_missing: bool = False) -> None:
    if not settings.imdb_ratings_enabled:
        return
    try:
        if only_if_missing and await imdb.loaded():
            return
        log.info("IMDb ratings loaded: %d titles", await imdb.refresh(clients.http()))
    except Exception:
        log.exception("IMDb ratings load failed; the ones already here stay")


async def titles_job() -> None:
    async with session() as db:
        try:
            refreshed, missed = await titles.refresh_stale(db, clients.tmdb())
            log.info("stale titles refreshed: %d%s", refreshed,
                     f" ({missed} TMDB couldn't send; tried again at the next run)" if missed else "")
        except Exception:
            log.exception("title refresh failed")


async def catalog_job(only_if_missing: bool = False) -> None:
    if not settings.catalog_enabled or not clients.tmdb().enabled:
        return
    try:
        if only_if_missing and await catalog.status():
            return
        log.info("search catalog: %s", await catalog.sync(clients.tmdb(), clients.meili(),
                                                           settings.catalog_movie_min_votes, settings.catalog_tv_min_votes))
    except Exception:
        log.exception("search catalog refresh failed")


# One digest run at a time: the catch-up after a start (4 min 20 s) can come
# while the scheduled run is still writing and sending (a start just before
# Monday 09:00). Run together, both would find the same digest not sent yet
# and send it twice; one after the other, the catch-up sees the run made.
_digest_lock = asyncio.Lock()


async def _run_digests(kind: str) -> None:
    async with session() as db:
        log.info("%s digests sent: %d", kind, await digest.run_scheduled(db, kind))


async def digest_job(kind: str) -> None:
    if not settings.email_enabled:
        return
    async with _digest_lock:
        await _run_digests(kind)


async def digest_catch_up(now: dt.datetime | None = None) -> None:
    """A digest run that was due while the app was down, or one in which a
    digest couldn't be built or sent, made now (every hour): every digest
    is built once per period and sent once, so a run made twice sends
    nothing twice. Waits for a scheduled run still going, then looks again
    at what isn't done."""
    if not settings.email_enabled:
        return
    async with _digest_lock:
        async with session() as db:
            kinds = await digest.missed(db, now)
        for kind in kinds:
            log.warning("%s digests not all sent (due while Movie Night wasn't running, or a send failed); "
                        "sending them now", kind)
            await _run_digests(kind)


def start_scheduler() -> AsyncIOScheduler:
    scheduler = AsyncIOScheduler(timezone=settings.timezone)
    soon = dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=20)
    scheduler.add_job(library_job, "interval", minutes=30, id="library", next_run_time=soon)
    scheduler.add_job(plays_job, "interval", minutes=5, id="plays", next_run_time=soon + dt.timedelta(seconds=40))
    scheduler.add_job(imdb_job, "cron", hour=2, minute=30, id="imdb")
    scheduler.add_job(imdb_job, "date", run_date=soon + dt.timedelta(minutes=3), id="imdb-first", args=[True])
    scheduler.add_job(mdblist_job, "cron", hour=4, minute=30, id="mdblist")
    scheduler.add_job(titles_job, "cron", hour=5, minute=0, id="titles")
    scheduler.add_job(catalog_job, "cron", day_of_week="sun", hour=3, minute=0, id="catalog")
    scheduler.add_job(catalog_job, "date", run_date=soon + dt.timedelta(minutes=2), id="catalog-first", args=[True])
    # A digest run late (the event loop busy) still runs, once, within the
    # hour; one missed while the app was down is made by digest_catch_up.
    late = {"misfire_grace_time": 3600, "coalesce": True}
    week, month, year = digest.RUN_AT["week"], digest.RUN_AT["month"], digest.RUN_AT["year"]
    scheduler.add_job(digest_job, "cron", day_of_week="mon", hour=week.hour, minute=week.minute, id="digest-week",
                      args=["week"], **late)
    scheduler.add_job(digest_job, "cron", day=1, hour=month.hour, minute=month.minute, id="digest-month", args=["month"], **late)
    scheduler.add_job(digest_job, "cron", month=1, day=1, hour=year.hour, minute=year.minute, id="digest-year", args=["year"],
                      **late)
    # Every hour, not only after a start: a run whose sends failed (SMTP down)
    # is left not done (digest.run_scheduled), and this sends what is still
    # unsent once the failure clears, until the next period is due. Never
    # one digest twice: a send whose fate is unknown (the connection broke
    # after the email was handed over) counts as sent, and one person's is
    # tried at most DIGEST_SEND_TRIES times (3) in all, then given up on for
    # the period with an ERROR (app/services/digest.py, send, run_scheduled).
    scheduler.add_job(digest_catch_up, "interval", hours=1, id="digest-catch-up",
                      next_run_time=soon + dt.timedelta(minutes=4), **late)
    scheduler.start()
    return scheduler
