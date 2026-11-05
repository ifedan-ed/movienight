"""Everything worth finding, not only what's in the library.

TMDB's own search only matches titles and names, so a description ("kids
with a treasure map trying to save their homes") finds nothing there. This
keeps a second Meilisearch index, "catalog", with every movie and show on
TMDB that has enough ratings (5 by default: about 200,000 titles, films in
every language), each as title | original title | (year) | genres | plot, the
same text shape and embedder as the library index. Search asks it alongside
the library and TMDB's live title search.

Built a year at a time from TMDB's discover pages (about 4,000 calls) and
refreshed weekly. Meilisearch only embeds a title again when its text
changes, so a refresh costs little beyond the new releases. TMDB is asked for
every title (include_adult): its default filter hides more than the titles it
marks adult (Tinto Brass's Cheeky, for one). HIDE_ADULT_TITLES leaves out the
ones it marks adult.
"""
from __future__ import annotations

import asyncio
import datetime as dt
import hashlib
import json
import logging
import math
import re
import unicodedata
from typing import Any

from sqlalchemy.dialects.postgresql import insert as pg_insert

from app.clients.meili import CATALOG
from app.core.config import settings
from app.core.db import session
from app.models import AppSetting
from app.services import spelling
from app.services.titles import MOVIE_GENRES, TV_GENRES

log = logging.getLogger("movienight.catalog")

FIRST_YEAR = 1900
# TMDB's discover serves at most 500 pages (10,000 titles) of one query, an
# outside limit Movie Night can't raise. A year with more (CATALOG_*_MIN_VOTES
# set low) is read in date ranges, halved until each fits, so every title is
# read; only a single day with more than 10,000 counts as a failed read (and
# then nothing is removed). See docs/limits.md, "Technical limits".
MAX_PAGES = 500
BATCH = 2000
STATUS_KEY = "catalog_status"
# What a card needs, kept with each document so a hit becomes a card without
# another TMDB call.
TMDB_FIELDS = ("id", "title", "name", "original_title", "original_name", "release_date", "first_air_date",
               "poster_path", "backdrop_path", "overview", "genre_ids", "vote_average", "vote_count")


def catalog_doc(r: dict[str, Any], media: str) -> dict[str, Any] | None:
    """One TMDB discover result -> one catalog document (None without an id
    or a title)."""
    tid = r.get("id")
    title = ((r.get("title") if media == "movie" else r.get("name")) or "").strip()
    if not tid or not title:
        return None
    original = ((r.get("original_title") if media == "movie" else r.get("original_name")) or title).strip()
    date = (r.get("release_date") if media == "movie" else r.get("first_air_date")) or ""
    year = int(date[:4]) if date[:4].isdigit() else None
    table = TV_GENRES if media == "tv" else MOVIE_GENRES
    genres = [table[g] for g in r.get("genre_ids") or [] if g in table]
    overview = " ".join((r.get("overview") or "").split())
    text = " | ".join(filter(None, [
        title,
        original if original != title else None,
        f"({year})" if year else None,
        ", ".join(genres) if genres else None,
        overview,
    ]))
    doc = {
        "id": f"{'m' if media == 'movie' else 't'}{int(tid)}",
        "tmdb_id": int(tid),
        "media_type": media,
        "title": title,
        "original_title": original,
        "year": year,
        "genres": genres,
        "overview": overview,
        "votes": int(r.get("vote_count") or 0),
        "searchText": text,
        "tmdb": {**{k: r[k] for k in TMDB_FIELDS if r.get(k) is not None}, "media_type": media},
    }
    doc["h"] = hashlib.sha1(json.dumps(doc, sort_keys=True).encode()).hexdigest()[:16]
    return doc


async def fetch(tmdb, media: str, min_votes: int, first_year: int = FIRST_YEAR, concurrency: int = 6) -> tuple[list[dict[str, Any]], int]:
    """Every TMDB title of one kind with at least `min_votes` ratings, a year
    at a time; a year past discover's 500 pages is read in date ranges that
    fit (MAX_PAGES). Returns the results and how many pages failed (a page
    that couldn't be read, or a day too big to read whole)."""
    year_param = "primary_release_year" if media == "movie" else "first_air_date_year"
    date_param = "primary_release_date" if media == "movie" else "first_air_date"
    # A stable order: popularity changes while the pages are walked, and a
    # title that moves between pages is skipped (then taken for gone).
    order = "original_title.asc" if media == "movie" else "original_name.asc"
    sem = asyncio.Semaphore(concurrency)
    failed = 0

    async def page(where: dict[str, Any], n: int) -> dict[str, Any] | None:
        nonlocal failed
        async with sem:
            try:
                return await tmdb.discover_page(media, page=n, sort_by=order, include_adult=True,
                                                **where, **{"vote_count.gte": min_votes})
            except Exception:
                failed += 1
                log.warning("catalog: %s %s page %d failed", media, where, n, exc_info=True)
                return None

    async def walk(where: dict[str, Any], first: dict[str, Any], pages: int) -> list[dict[str, Any]]:
        rest = await asyncio.gather(*(page(where, n) for n in range(2, pages + 1)))
        return [r for d in (first, *rest) if d for r in d.get("results") or []]

    async def span(lo: dt.date, hi: dt.date) -> list[dict[str, Any]]:
        """The titles first released (or aired) from lo to hi, halving the
        range until it fits in MAX_PAGES."""
        nonlocal failed
        where = {f"{date_param}.gte": lo.isoformat(), f"{date_param}.lte": hi.isoformat()}
        first = await page(where, 1)
        if not first:
            return []
        pages = int(first.get("total_pages") or 1)
        if pages > MAX_PAGES:
            if lo < hi:
                mid = lo + (hi - lo) // 2
                a, b = await asyncio.gather(span(lo, mid), span(mid + dt.timedelta(days=1), hi))
                return a + b
            # One day with more than 10,000 titles can't be split further:
            # read what TMDB serves, and count it as a failed read so no
            # title is taken for gone.
            failed += 1
            log.warning("catalog: %s on %s has %d pages, more than TMDB serves (%d); nothing will be removed",
                        media, lo, pages, MAX_PAGES)
            pages = MAX_PAGES
        return await walk(where, first, pages)

    async def one_year(year: int) -> list[dict[str, Any]]:
        first = await page({year_param: year}, 1)
        if not first:
            return []
        pages = int(first.get("total_pages") or 1)
        if pages > MAX_PAGES:
            return await span(dt.date(year, 1, 1), dt.date(year, 12, 31))
        return await walk({year_param: year}, first, pages)

    out: list[dict[str, Any]] = []
    years = list(range(first_year, dt.date.today().year + 2))
    for i in range(0, len(years), 8):
        for chunk in await asyncio.gather(*(one_year(y) for y in years[i:i + 8])):
            out.extend(chunk)
    return out, failed


async def _save_status(status: dict[str, Any]) -> None:
    value = json.dumps(status)
    async with session() as db:
        stmt = pg_insert(AppSetting).values(key=STATUS_KEY, value=value)
        await db.execute(stmt.on_conflict_do_update(index_elements=[AppSetting.key], set_={"value": value}))
        await db.commit()


async def status() -> dict[str, Any] | None:
    async with session() as db:
        row = await db.get(AppSetting, STATUS_KEY)
    try:
        return json.loads(row.value) if row else None
    except ValueError:
        return None


async def sync(tmdb, meili, movie_min_votes: int = 5, tv_min_votes: int = 5) -> dict[str, Any]:
    """Bring the catalog index in step with TMDB: new and changed titles are
    sent (Meilisearch embeds them in the background), titles that left are
    removed, but only after a fetch with no failed pages."""
    started = dt.datetime.now(dt.timezone.utc)
    await meili.ensure_catalog_index()
    docs: dict[str, dict[str, Any]] = {}
    failed = 0
    for media, votes in (("movie", movie_min_votes), ("tv", tv_min_votes)):
        results, bad = await fetch(tmdb, media, votes)
        failed += bad
        for r in results:
            d = None if settings.hide_adult_titles and r.get("adult") else catalog_doc(r, media)
            if d:
                docs[d["id"]] = d
    await spelling.save_vocab(spelling.count_words(d["searchText"] for d in docs.values()))
    current = await meili.field_values("h", CATALOG)
    changed = [d for d in docs.values() if current.get(d["id"]) != d["h"]]
    tasks = []
    for i in range(0, len(changed), BATCH):
        tasks.append(await meili.replace_documents(changed[i:i + BATCH], index=CATALOG))
    gone = [i for i in current if i not in docs] if not failed else []
    for i in range(0, len(gone), BATCH):
        await meili.delete_documents(gone[i:i + BATCH], index=CATALOG)
    result = {"at": started.isoformat(), "titles": len(docs), "changed": len(changed), "removed": len(gone),
              "failed_pages": failed, "tasks": [t for t in tasks if t is not None]}
    await _save_status(result)
    return result


# ---------------------------------------------------------------------------
# Ranking search results from the library, the catalog and TMDB's title search
# ---------------------------------------------------------------------------

_ARTICLE = re.compile(r"^(the|a|an) ")


def norm(text: str | None) -> str:
    """Lowercase, no accents or punctuation, no leading article."""
    t = unicodedata.normalize("NFKD", text or "")
    t = "".join(ch for ch in t if not unicodedata.combining(ch)).lower()
    t = re.sub(r"[^a-z0-9]+", " ", t).strip()
    return _ARTICLE.sub("", t)


def title_match(query: str, *titles: str | None) -> float:
    """How well a query names a title: 1.0 exactly, 0.9 the start of it,
    0.85 every word of the query in it, else 0."""
    q = norm(query)
    if not q:
        return 0.0
    words = q.split()
    best = 0.0
    for title in titles:
        t = norm(title)
        if not t:
            continue
        if t == q:
            return 1.0
        if t.startswith(q + " ") or (len(t) >= 4 and q.startswith(t + " ")):
            best = max(best, 0.9)
        elif all(w in t.split() for w in words):
            best = max(best, 0.85)
    return best


def popularity_bonus(votes: int | None) -> float:
    """A nudge (up to 0.15, full at 10,000 ratings) toward titles many people
    have rated: someone describing a film from memory usually means a known
    one. Tuned on described-plot searches (2026-10-02): Jaws went from 7th
    to 2nd, Toy Story from 20th to 4th; an exact title (1.0) still wins."""
    return 0.15 * min(1.0, math.log10((votes or 0) + 1) / 4)


async def _main() -> None:  # pragma: no cover - operator command
    """python -m app.services.catalog: refresh now and follow the embedding."""
    import time

    from app import clients

    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(message)s")
    meili = clients.meili()
    result = await sync(clients.tmdb(), meili, settings.catalog_movie_min_votes, settings.catalog_tv_min_votes)
    log.info("sent: %s", {k: v for k, v in result.items() if k != "tasks"})
    started = time.monotonic()
    for uid in result["tasks"]:
        while (t := await meili.task(uid))["status"] in ("enqueued", "processing"):
            await asyncio.sleep(20)
        stats = await meili.stats(CATALOG) or {}
        log.info("task %s %s; %s documents searchable, %.0f min so far", uid, t["status"],
                 stats.get("numberOfDocuments"), (time.monotonic() - started) / 60)


if __name__ == "__main__":  # pragma: no cover
    asyncio.run(_main())
