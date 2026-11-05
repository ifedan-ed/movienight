"""Recommendation rows for one person: "Because you watched X" (TMDB
recommendations for their last fifteen distinct titles, dealt into three sets
of five) and fifteen AI-curated rows, also shown five at a time (the
install's model picks themes and films from their history, ratings and
comments; picks are matched to TMDB and kept in ai_row_cache, rebuilt as
services/rows.py decides: AI_ROWS_CACHE_TTL_HOURS, AI_REFRESH_AFTER_WATCHES)."""
from __future__ import annotations

import asyncio
import datetime as dt
import json
import logging
import re
from typing import Any

from sqlalchemy import delete, func, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.config import settings
from app.models import AIRowCache, HistoryEntry, UserRating
from app.services import titles

log = logging.getLogger("movienight.recommend")

SETS = 3  # Home shows one set at a time and moves on to the next
ROWS_PER_SET = 5
MAX_AI_ROWS = SETS * ROWS_PER_SET
MAX_ITEMS_PER_AI_ROW = 12
AI_CACHE_VERSION = 4
PIN_RECENT = dt.timedelta(hours=24)
SEED_PAGE = 600  # history rows read at a time for "Because you watched" (as many pages as it takes)


async def recent_seeds(db: AsyncSession, user_id: int, media_type: str | None,
                       limit: int = SETS * ROWS_PER_SET) -> list[tuple[str, int, str, dt.datetime]]:
    """Last `limit` distinct titles watched, newest first: (media, tmdb_id,
    title, last watched). media_type None means movies and shows mixed. The
    history is read SEED_PAGE rows at a time until `limit` titles are found
    or it ends (no cap: a long binge of one show no longer hides the titles
    watched before it)."""
    q = (select(HistoryEntry.media_type, HistoryEntry.tmdb_id, HistoryEntry.title, HistoryEntry.watched_at)
         .where(HistoryEntry.user_id == user_id, HistoryEntry.tmdb_id.isnot(None))
         .order_by(HistoryEntry.watched_at.desc(), HistoryEntry.id.desc()))
    if media_type == "movie":
        q = q.where(HistoryEntry.media_type == "movie")
    elif media_type == "tv":
        q = q.where(HistoryEntry.media_type == "episode")
    # A title they rated 5/10 or lower shouldn't drive "Because you watched".
    disliked = {(m, t) for m, t in await db.execute(select(UserRating.media_type, UserRating.tmdb_id)
                                                    .where(UserRating.user_id == user_id, UserRating.rating <= 5))}
    seen: set[tuple[str, int]] = set()
    out: list[tuple[str, int, str, dt.datetime]] = []
    offset = 0
    while len(out) < limit:
        page = list(await db.execute(q.offset(offset).limit(SEED_PAGE)))
        for hmedia, tmdb_id, title, when in page:
            media = "tv" if hmedia == "episode" else "movie"
            if (media, tmdb_id) in seen or (media, tmdb_id) in disliked:
                continue
            seen.add((media, tmdb_id))
            out.append((media, tmdb_id, title, when if when.tzinfo else when.replace(tzinfo=dt.timezone.utc)))
            if len(out) >= limit:
                break
        if len(page) < SEED_PAGE:
            break
        offset += SEED_PAGE
    return out


def because_set(pool: list[tuple[str, int, str, dt.datetime]], index: int,
                now: dt.datetime | None = None) -> list[tuple[str, int, str]]:
    """The "Because you watched" rows for one set. The pool (newest first)
    is dealt round-robin into SETS sets, so each set mixes recent and older
    watches; anything watched in the last day leads every set."""
    now = now or dt.datetime.now(dt.timezone.utc)
    pinned = [p for p in pool[:SETS] if now - p[3] <= PIN_RECENT]
    sets = [pool[k::SETS] for k in range(SETS)]
    chosen = sets[index % SETS] if pool else []
    out = pinned + [p for p in chosen if p not in pinned]
    return [(m, t, name) for m, t, name, _ in out[:ROWS_PER_SET]]


def ai_set(rows: list[dict[str, Any]], index: int) -> list[dict[str, Any]]:
    """One set of five AI rows (all of them when the model gave five or fewer)."""
    if len(rows) <= ROWS_PER_SET:
        return rows
    sets = [rows[i:i + ROWS_PER_SET] for i in range(0, len(rows), ROWS_PER_SET)]
    return sets[index % len(sets)]


# ---------------------------------------------------------------------------
# AI rows
# ---------------------------------------------------------------------------

AI_SYSTEM_PROMPT = (
    "You curate themed movie rows for a personal movie app's home screen, like "
    f"a streaming service. Given the user's watch history and ratings, invent "
    f"exactly {MAX_AI_ROWS} themed rows. Spread the rows across the whole of their taste: "
    "each row should draw on a different part of the history and the highest "
    "ratings, not only on the most recent film, and no two rows should feel alike. "
    "Each row has a short, specific theme title (max 6 words, no clichés like "
    f"'Hidden Gems') and {MAX_ITEMS_PER_AI_ROW} real, existing feature films that fit the theme and "
    "that the user has NOT already watched; a film appears in one row only. Give "
    "each film as 'Title (Year)'. Mix well-known and lesser-known picks. Respond "
    'with strict JSON only: {"rows": [{"theme": "...", "titles": ["Title (Year)", ...]}]}'
)


def summarize_history_for_prompt(watched: list[str], ratings: list[tuple[str, int]], max_items: int = 60,
                                 notes: list[tuple[str, int, str]] | None = None, max_notes: int = 20) -> str:
    lines = []
    if watched:
        lines.append("Recently watched: " + ", ".join(watched[:max_items]))
    if ratings:
        top = sorted(ratings, key=lambda r: r[1], reverse=True)[:max_items]
        lines.append("Ratings out of 10: " + ", ".join(f"{t} ({s})" for t, s in top))
    if notes:
        lines.append("Their own comments: " + " | ".join(f"{t} ({s}/10): {n}" for t, s, n in notes[:max_notes]))
    return "\n".join(lines) if lines else "No history yet."


def parse_ai_response(raw: Any) -> list[dict[str, Any]]:
    if isinstance(raw, str):
        raw = json.loads(raw)
    rows = raw.get("rows") if isinstance(raw, dict) else raw
    if not isinstance(rows, list):
        raise ValueError("AI response did not contain a 'rows' list")
    cleaned = []
    for row in rows[:MAX_AI_ROWS]:
        if not isinstance(row, dict):
            continue
        theme = str(row.get("theme") or "").strip()
        picks = [t.strip() for t in (row.get("titles") or []) if isinstance(t, str) and t.strip()]
        if theme and picks:
            cleaned.append({"theme": theme if len(theme) <= 80 else theme[:80].rstrip() + "…", "titles": picks[:MAX_ITEMS_PER_AI_ROW]})
    if not cleaned:
        raise ValueError("AI response had no usable rows")
    return cleaned


def split_title_year(text: str) -> tuple[str, int | None]:
    m = re.match(r"^(.*?)\s*\((\d{4})\)\s*$", text)
    if m:
        return m.group(1).strip(), int(m.group(2))
    return text.strip(), None


def _norm(s: str | None) -> str:
    return re.sub(r"[^a-z0-9]+", "", (s or "").lower())


def _year_of(r: dict[str, Any]) -> int | None:
    d = r.get("release_date") or ""
    return int(d[:4]) if d[:4].isdigit() else None


async def resolve_title(tmdb, text: str) -> dict[str, Any] | None:
    """'Title (Year)' -> TMDB movie result (exact title+year, then title with
    a nearby year, then exact title, then TMDB's top hit)."""
    name, year = split_title_year(text)
    if not name:
        return None
    try:
        movies = await tmdb.search_movie(name, year)
        if not movies and year:
            movies = await tmdb.search_movie(name)
    except Exception:
        return None
    if not movies:
        return None
    target = _norm(name)
    for r in movies:
        if _norm(r.get("title")) == target and (year is None or _year_of(r) == year):
            return r
    if year is not None:
        for r in movies:
            ry = _year_of(r)
            if ry and abs(ry - year) <= 1 and (target in _norm(r.get("title")) or _norm(r.get("title")) in target):
                return r
    for r in movies:
        if _norm(r.get("title")) == target or _norm(r.get("original_title")) == target:
            return r
    return movies[0]


async def history_summary(db: AsyncSession, user_id: int) -> tuple[str, dict[str, Any]]:
    """The prompt's view of the person, and what it covers (`basis`, shown
    under "Picked for you", so nothing is cut without a word)."""
    # What the model is sent: titles from their latest film watches (the
    # AI_PROMPT_TITLES (60) newest distinct ones listed), their 60 best film
    # ratings and their AI_PROMPT_COMMENTS (20) latest comments (the first
    # AI_PROMPT_COMMENT_CHARS (300) characters of each, with "…" where one
    # was longer). Never their name or email address. This keeps the prompt
    # within a model's context window; the rows say what was used, how many
    # comments were shortened included. See docs/limits.md (Recommendations).
    n_titles, n_notes, n_chars = settings.ai_prompt_titles, settings.ai_prompt_comments, settings.ai_prompt_comment_chars
    watched: list[str] = []
    for title, in await db.execute(select(HistoryEntry.title)
                                   .where(HistoryEntry.user_id == user_id, HistoryEntry.media_type == "movie")
                                   .order_by(HistoryEntry.watched_at.desc()).limit(max(200, n_titles * 3))):
        if title and title not in watched:  # two sources can log the same watch
            watched.append(title)
    rated = list(await db.execute(select(UserRating.tmdb_id, UserRating.rating, UserRating.review, UserRating.rated_at)
                                  .where(UserRating.user_id == user_id, UserRating.media_type == "movie")
                                  .order_by(UserRating.rated_at.desc())))
    names = await titles.get_many(db, [titles.tkey("movie", t) for t, *_ in rated])
    ratings = [(names[titles.tkey("movie", t)]["title"], r) for t, r, *_ in rated if titles.tkey("movie", t) in names]
    notes = [(names[titles.tkey("movie", t)]["title"], r, " ".join((rev or "").split()))
             for t, r, rev, _ in rated if rev and titles.tkey("movie", t) in names]
    shortened = sum(1 for _, _, text in notes[:n_notes] if len(text) > n_chars)
    notes = [(t, r, text if len(text) <= n_chars else text[:n_chars].rstrip() + "…") for t, r, text in notes]
    films_total = await db.scalar(select(func.count(func.distinct(HistoryEntry.title)))
                                  .where(HistoryEntry.user_id == user_id, HistoryEntry.media_type == "movie")) or 0
    basis = {"films": min(len(watched), n_titles), "films_total": films_total, "ratings": min(len(ratings), n_titles),
             "ratings_total": len(ratings), "comments": min(len(notes), n_notes), "comments_total": len(notes),
             "comments_shortened": shortened, "comment_chars": n_chars}
    return summarize_history_for_prompt(watched, ratings, max_items=n_titles, notes=notes, max_notes=n_notes), basis


def basis_line(basis: dict[str, Any] | None) -> str | None:
    """'From your 60 latest of 340 films, 60 best of 210 ratings and 20
    latest of 45 comments (3 shortened to their first 300 characters)':
    what the AI read, said under the rows."""
    if not basis:
        return None

    def part(n: int, total: int, kind: str, which: str) -> str | None:
        if not total:
            return None
        if total == 1:
            kind = kind[:-1]  # "your 1 film"
        return f"your {total} {kind}" if n >= total else f"your {n} {which} of {total} {kind}"

    comments = part(basis["comments"], basis["comments_total"], "comments", "latest")
    cut = basis.get("comments_shortened") or 0
    if comments and cut:
        chars = f"{basis.get('comment_chars', 300):,}"
        if cut == 1:
            comments += f" (1 shortened to its first {chars} characters)"
        else:
            comments += f" ({'all' if cut == basis['comments'] else cut} shortened to their first {chars} characters)"
    bits = [b for b in (part(basis["films"], basis["films_total"], "films", "latest"),
                        part(basis["ratings"], basis["ratings_total"], "film ratings", "best"),
                        comments) if b]
    if not bits:
        return None
    return "From " + (", ".join(bits[:-1]) + " and " + bits[-1] if len(bits) > 1 else bits[0])


async def generate_ai_rows(db: AsyncSession, user_id: int, tmdb, litellm) -> list[dict[str, Any]]:
    """Ask the model for MAX_AI_ROWS (15) themed rows of up to 12 films from
    the person's history and ratings, match each pick on TMDB, and store the
    result as their AI rows, replacing the ones before (a pick TMDB can't
    match, or any TMDB error, is skipped). When no pick at all was found,
    nothing is stored and the rows before stay; rows.py counts that as a
    failed rebuild and backs off. Not counted against any allowance."""
    summary, basis = await history_summary(db, user_id)
    raw = await litellm.chat_json(AI_SYSTEM_PROMPT, f"User's history:\n{summary}\n\nGenerate the {MAX_AI_ROWS} rows now.")
    rows = parse_ai_response(raw)
    # Match every pick on TMDB at once (network only), then cache them.
    sem = asyncio.Semaphore(8)

    async def match(text: str):
        async with sem:
            return await resolve_title(tmdb, text)

    found = await asyncio.gather(*(asyncio.gather(*(match(t) for t in row["titles"])) for row in rows))
    out: list[dict[str, Any]] = []
    used: set[str] = set()
    for row, results in zip(rows, found):
        metas = await titles.upsert_results(db, [r for r in results if r], "movie")
        keys = [k for k in dict.fromkeys(m["key"] for m in metas) if k not in used]
        used.update(keys)
        if keys:
            out.append({"theme": row["theme"], "keys": keys})
    if not out:
        log.info("AI rows for user %s: none of the picks were found on TMDB; nothing stored", user_id)
        return out
    new = AIRowCache(user_id=user_id, model=settings.litellm_model,
                     payload_json=json.dumps({"v": AI_CACHE_VERSION, "rows": out, "basis": basis}))
    db.add(new)
    await db.flush()
    # Only the newest is kept: the ones before it are never read again.
    await db.execute(delete(AIRowCache).where(AIRowCache.user_id == user_id, AIRowCache.id != new.id))
    await db.commit()
    log.info("AI rows for user %s: %d rows", user_id, len(out))
    return out
