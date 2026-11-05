"""The weekly digest, the month in review and the year in review, by email.

Each one is built from the person's own records for the period (what they
watched, rated and loved, what arrived in the library, what is coming from
their watchlist, a few picks) and the install's AI model writes the words
around it. Without a model it still goes out, with plain wording. A year is
too many films to list, so its email sums the year up instead: the numbers,
month by month, the best-rated films, the most-watched shows, the first and
the last film.

Who gets them: everyone with an email address, on by default (the install
is invite-only, so that is its members); each kind is switched off in
Profile → Emails, or with the "Turn this email off" link at the foot of
every digest (also its List-Unsubscribe header). An empty one is never sent
by the schedule: nothing watched, rated or favourited, and (for a week or a
month) nothing new in the library (is_empty). See docs/limits.md (Background jobs).
"""
from __future__ import annotations

import datetime as dt
import html
import json
import logging
from collections import Counter
from typing import Any
from zoneinfo import ZoneInfo

from sqlalchemy import func, select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.clients.mailer import MaybeSent
from app.core.config import settings
from app.models import Digest, Favorite, HistoryEntry, LibraryItem, User, UserRating, WatchlistEntry
from app.services import history, titles, users

log = logging.getLogger("movienight.digest")

KINDS = ("week", "month", "year")
SETTING = {"week": "digest_weekly", "month": "digest_monthly", "year": "digest_yearly"}
WORD = {"week": "week", "month": "month", "year": "year"}
EMAIL_KEY = "digest_email"
IMG = "https://image.tmdb.org/t/p/w185"


def tz() -> ZoneInfo:
    return ZoneInfo(settings.timezone)


def last_period(kind: str, now: dt.datetime | None = None) -> tuple[dt.datetime, dt.datetime]:
    """The last full week (Monday to Sunday), calendar month or calendar year, local time."""
    local = (now or dt.datetime.now(dt.timezone.utc)).astimezone(tz())
    midnight = local.replace(hour=0, minute=0, second=0, microsecond=0)
    if kind == "week":
        end = midnight - dt.timedelta(days=midnight.weekday())
        start = end - dt.timedelta(days=7)
    elif kind == "year":
        end = midnight.replace(month=1, day=1)
        start = end.replace(year=end.year - 1)
    else:
        end = midnight.replace(day=1)
        start = (end - dt.timedelta(days=1)).replace(day=1)
    return start, end


def period_label(kind: str, start: dt.datetime, end: dt.datetime) -> str:
    last = end - dt.timedelta(days=1)
    if kind == "year":
        return str(start.year)
    if kind == "month":
        return start.strftime("%B %Y")
    if start.month == last.month:
        return f"{start.strftime('%b')} {start.day}–{last.day}"
    return f"{start.strftime('%b')} {start.day} – {last.strftime('%b')} {last.day}"


def _local(t: dt.datetime) -> dt.datetime:
    return (t if t.tzinfo else t.replace(tzinfo=dt.timezone.utc)).astimezone(tz())


async def gather(db: AsyncSession, user: User, kind: str, start: dt.datetime, end: dt.datetime) -> dict[str, Any]:
    tmdb = clients.tmdb()
    rows = list(await db.scalars(select(HistoryEntry)
                                 .where(HistoryEntry.user_id == user.id, HistoryEntry.watched_at >= start, HistoryEntry.watched_at < end)
                                 .order_by(HistoryEntry.watched_at)))
    watches = history.group_rows(reversed(rows))  # newest first, merged per title and day
    watches.reverse()
    ratings = {(r.media_type, r.tmdb_id): r for r in await db.scalars(select(UserRating).where(UserRating.user_id == user.id))}

    movie_keys = [w["key"] for w in watches if w["media_type"] == "movie" and w["key"]]
    show_keys = [w["key"] for w in watches if w["media_type"] == "episode" and w["key"]]
    lib_rows = list(await db.execute(select(LibraryItem.media_type, LibraryItem.tmdb_id)
                                     .where(LibraryItem.date_added >= start, LibraryItem.date_added < end, LibraryItem.tmdb_id.isnot(None))
                                     .order_by(LibraryItem.date_added.desc()).limit(12)))
    lib_keys = [titles.tkey(m, t) for m, t in lib_rows]
    lib_total = await db.scalar(select(func.count()).select_from(LibraryItem).where(
        LibraryItem.date_added >= start, LibraryItem.date_added < end, LibraryItem.tmdb_id.isnot(None))) or 0
    soon_rows = list(await db.execute(select(WatchlistEntry.media_type, WatchlistEntry.tmdb_id)
                                      .where(WatchlistEntry.user_id == user.id, WatchlistEntry.removed_at.is_(None))))
    soon_keys = [titles.tkey(m, t) for m, t in soon_rows]
    metas = await titles.ensure(db, tmdb, list(dict.fromkeys(movie_keys + show_keys + lib_keys)), max_fetch=80)
    metas.update(await titles.get_many(db, soon_keys))

    def title_info(key: str) -> dict[str, Any]:
        m = metas.get(key) or {}
        media, tid = titles.split_key(key)
        return {"key": key, "media_type": media, "tmdb_id": tid, "title": m.get("title") or "", "year": m.get("year"),
                "poster": m.get("poster_path"), "genres": m.get("genres") or [], "runtime": m.get("runtime"),
                "overview": (m.get("overview") or "")[:300]}

    films = []
    for w in watches:
        if w["media_type"] != "movie":
            continue
        info = title_info(w["key"]) if w["key"] else {"key": None, "title": w["title"], "year": None, "poster": None, "genres": [], "runtime": None}
        r = ratings.get(("movie", info.get("tmdb_id"))) if w["key"] else None
        films.append({**info, "title": info["title"] or w["title"], "watched_on": _local(dt.datetime.fromisoformat(w["watched_at"])).strftime("%a %b %-d"),
                      "rating": r.rating if r else None, "review": _short(r.review, 400) if r and r.review else None})

    shows: dict[str, dict[str, Any]] = {}
    for w in watches:
        if w["media_type"] != "episode":
            continue
        k = w["key"] or w["title"]
        s = shows.setdefault(k, {**(title_info(w["key"]) if w["key"] else {"key": None, "title": w["title"], "poster": None, "genres": [], "runtime": None}), "episodes": []})
        s["title"] = s["title"] or w["title"]
        if w["season"] is not None and w["episode"] is not None:
            s["episodes"].append((w["season"], w["episode"]))
    show_list = []
    for s in shows.values():
        eps = sorted(set(s["episodes"]))
        span = f"S{eps[0][0]:02d}E{eps[0][1]:02d}" + (f" – S{eps[-1][0]:02d}E{eps[-1][1]:02d}" if len(eps) > 1 else "") if eps else ""
        show_list.append({**s, "episodes": len(eps) or 1, "span": span})
    show_list.sort(key=lambda s: -s["episodes"])

    minutes = sum(f.get("runtime") or 0 for f in films) + sum((s.get("runtime") or 30) * s["episodes"] for s in show_list)
    rated_now = [r for r in ratings.values() if start <= _local(r.rated_at) < end]
    rated_now.sort(key=lambda r: -r.rating)
    fav_rows = list(await db.execute(select(Favorite.media_type, Favorite.tmdb_id)
                                     .where(Favorite.user_id == user.id, Favorite.removed_at.is_(None),
                                            Favorite.added_at >= start, Favorite.added_at < end)))
    today = dt.date.today()
    # "Coming from your watchlist" looks this many days ahead.
    horizon = today + dt.timedelta(days={"week": 21, "month": 45, "year": 60}[kind])
    coming = []
    for k in soon_keys:
        m = metas.get(k) or {}
        rd = m.get("release_date")
        if rd and today.isoformat() <= rd <= horizon.isoformat():
            coming.append({**title_info(k), "date": dt.date.fromisoformat(rd).strftime("%b %-d")})
    coming.sort(key=lambda c: c["date"])
    genres = Counter(g for f in films for g in f["genres"])
    picks = await _picks(db, user, metas, {f["key"] for f in films})
    best = max((f for f in films if f.get("rating")), key=lambda f: f["rating"], default=None)
    year = _year(watches, films, show_list, genres, len(rated_now), len(fav_rows)) if kind == "year" else None
    library = [title_info(k) for k in lib_keys if (metas.get(k) or {}).get("poster_path")][:8]
    # Lists cut short in the email say how many more there are ("more"), so
    # nothing is left out without a word.
    return {
        "kind": kind, "label": period_label(kind, start, end),
        "name": user.display_name or user.username,
        "films": films, "shows": show_list[:12],
        "totals": {"films": len(films), "episodes": sum(s["episodes"] for s in show_list), "hours": round(minutes / 60)},
        "rated": [{"title": (metas.get(titles.tkey(r.media_type, r.tmdb_id)) or {}).get("title") or "", "rating": r.rating} for r in rated_now[:8]],
        "rated_total": len(rated_now),
        "new_favorites": len(fav_rows),
        "library": library,
        "coming": coming[:6],
        "more": {"shows": max(0, len(show_list) - 12), "library": max(0, lib_total - len(library)),
                 "coming": max(0, len(coming) - 6)},
        "top_genres": [g for g, _ in genres.most_common(3)],
        "best": best,
        "picks": picks,
        "year": year,
    }


def _short(text: str, n: int) -> str:
    """The first n characters of a long comment in the email, with "…" so it
    is plain that there is more (the whole one is on the title's page)."""
    text = text.strip()
    return text if len(text) <= n else text[:n].rstrip() + "…"


def is_empty(data: dict[str, Any]) -> bool:
    """Nothing watched, rated or favourited in the period, and (for a week
    or a month) nothing new in the library: the schedule doesn't send such
    a digest (picks and "coming from your watchlist" alone don't make one
    worth an email). The year in review has no "New in the library"
    section, so for a year only the person's own watching, rating and
    favourites count: a year with nothing of theirs is empty, however much
    the library grew."""
    t, more = data.get("totals") or {}, data.get("more") or {}
    own = (t.get("films") or t.get("episodes") or data.get("rated_total", len(data.get("rated") or []))
           or data.get("new_favorites"))
    if data.get("kind") == "year":
        return not own
    return not (own or data.get("library") or more.get("library"))


def unsubscribe_url(kind: str) -> str:
    """Opens Profile → Emails on the website and turns this kind off there."""
    return f"{settings.public_url.rstrip('/')}/settings/emails?off={kind}"


def _year(watches: list[dict[str, Any]], films: list[dict[str, Any]], shows: list[dict[str, Any]],
          genres: Counter, rated: int, loved: int) -> dict[str, Any]:
    """A year summed up: films and episodes month by month, the busiest
    month, the best-rated films (ties: the earlier watch), the most-watched
    shows, the first and the last film, and the favourite genres."""
    months = [{"films": 0, "episodes": 0} for _ in range(12)]
    for w in watches:
        m = _local(dt.datetime.fromisoformat(w["watched_at"])).month - 1
        months[m]["films" if w["media_type"] == "movie" else "episodes"] += 1
    busiest = max(range(12), key=lambda i: (months[i]["films"] + months[i]["episodes"], -i))
    has_any = any(m["films"] + m["episodes"] for m in months)
    ranked = sorted((f for f in films if f.get("rating")), key=lambda f: -f["rating"])
    return {
        "months": months,
        "busiest": dt.date(2000, busiest + 1, 1).strftime("%B") if has_any else None,
        "best_films": ranked[:8],
        "top_shows": shows[:6],
        "first_film": films[0] if films else None,
        "last_film": films[-1] if len(films) > 1 else None,
        "genres": [g for g, _ in genres.most_common(5)],
        "rated": rated,
        "loved": loved,
    }


async def _picks(db: AsyncSession, user: User, metas: dict[str, dict], exclude: set[str]) -> list[dict[str, Any]]:
    """Up to four titles to watch next: unwatched library titles on their
    watchlist first, then their latest AI picks."""
    from app.models import AIRowCache

    keys: list[str] = []
    lib = {titles.tkey(m, t) for m, t in await db.execute(select(LibraryItem.media_type, LibraryItem.tmdb_id).where(LibraryItem.tmdb_id.isnot(None)))}
    watched = {titles.tkey("tv" if m == "episode" else "movie", t) for m, t in await db.execute(
        select(HistoryEntry.media_type, HistoryEntry.tmdb_id).where(HistoryEntry.user_id == user.id, HistoryEntry.tmdb_id.isnot(None)))}
    for m, t in await db.execute(select(WatchlistEntry.media_type, WatchlistEntry.tmdb_id)
                                 .where(WatchlistEntry.user_id == user.id, WatchlistEntry.removed_at.is_(None))
                                 .order_by(WatchlistEntry.added_at.desc())):
        k = titles.tkey(m, t)
        if k in lib and k not in watched:
            keys.append(k)
    cached = await db.scalar(select(AIRowCache).where(AIRowCache.user_id == user.id).order_by(AIRowCache.generated_at.desc()).limit(1))
    if cached:
        try:
            for row in json.loads(cached.payload_json).get("rows", [])[:2]:
                keys += [k for k in row.get("keys", [])[:3]]
        except ValueError:
            pass
    keys = [k for k in dict.fromkeys(keys) if k not in watched and k not in exclude][:4]
    have = await titles.ensure(db, clients.tmdb(), keys, max_fetch=4)
    return [{"key": k, "title": have[k]["title"], "year": have[k].get("year"), "poster": have[k].get("poster_path"),
             "in_library": k in lib, "overview": (have[k].get("overview") or "")[:300]} for k in keys if k in have]


# ---------------------------------------------------------------------------
# Words
# ---------------------------------------------------------------------------

AI_SYSTEM = (
    "You write the short text of a personal film-and-TV email for one person, from their own viewing data. "
    "Write like a friend who knows films well: specific, warm, plain, no hype, no emoji, no exclamation marks, "
    "no clichés ('cinematic journey', 'binge-worthy'), never invent facts about the person beyond the data. "
    "Mention real titles from the data. Second person. Return strict JSON only: "
    '{"subject": "under 70 characters, specific to this period", '
    '"intro": "2 to 4 sentences about what they watched and what stood out", '
    '"film_notes": {"<exact film title>": "one sentence, at most 22 words, about that film or why it fits their taste"}, '
    '"picks_line": "one sentence introducing the suggestions"}'
)


def _ai_input(data: dict[str, Any]) -> str:
    films = data["films"]
    if data["kind"] == "year":
        # The year's best, then every other film of the year by name only (a
        # year of films is too many to describe one by one, but none is left
        # out: names alone stay small).
        best = {f["title"] for f in data["year"]["best_films"]}
        films = data["year"]["best_films"] + [{"title": f["title"]} for f in data["films"] if f["title"] not in best]
    slim = {
        "period": f"{WORD[data['kind']]} of {data['label']}",
        "films": [{k: f.get(k) for k in ("title", "year", "watched_on", "rating", "review", "genres") if f.get(k) is not None} for f in films],
        "shows": [{"title": s["title"], "episodes": s["episodes"], "span": s["span"]} for s in data["shows"]],
        "totals": data["totals"],
        "ratings_given": data["rated"],
        "top_genres": data["top_genres"],
        "suggestions": [{"title": p["title"], "year": p.get("year"), "in_library": p["in_library"]} for p in data["picks"]],
        "coming_from_watchlist": [{"title": c["title"], "date": c["date"]} for c in data["coming"]],
    }
    if data["kind"] == "year":
        y = data["year"]
        slim["year_in_numbers"] = {"films_and_episodes_by_month": [m["films"] + m["episodes"] for m in y["months"]],
                                   "busiest_month": y["busiest"], "favourite_genres": y["genres"],
                                   "first_film": (y["first_film"] or {}).get("title"), "last_film": (y["last_film"] or {}).get("title")}
    return json.dumps(slim, ensure_ascii=False)


def plain_words(data: dict[str, Any]) -> dict[str, Any]:
    t = data["totals"]
    what = WORD[data["kind"]]
    parts = []
    if t["films"]:
        parts.append(f"{t['films']} film{'s' if t['films'] != 1 else ''}")
    if t["episodes"]:
        parts.append(f"{t['episodes']} episode{'s' if t['episodes'] != 1 else ''}")
    if not parts:
        return {"subject": f"Your {what} in film: a quiet one", "intro": f"Nothing watched this {what}. Here is what is waiting for you.",
                "film_notes": {}, "picks_line": "A few ideas for next time:"}
    return {"subject": f"Your {what} in film: {' and '.join(parts)}", "intro": f"This {what} you watched {' and '.join(parts)}.",
            "film_notes": {}, "picks_line": "Up next, maybe:"}


async def write_words(data: dict[str, Any]) -> tuple[dict[str, Any], str | None]:
    ai = clients.litellm()
    if not ai.enabled:
        return plain_words(data), None
    try:
        raw = await ai.chat_json(AI_SYSTEM, _ai_input(data))
        words = plain_words(data)
        if isinstance(raw, dict):
            for k in ("subject", "intro", "picks_line"):
                if isinstance(raw.get(k), str) and raw[k].strip():
                    words[k] = raw[k].strip()
            if isinstance(raw.get("film_notes"), dict):
                words["film_notes"] = {str(k): str(v).strip() for k, v in raw["film_notes"].items() if v}
        words["subject"] = _short(words["subject"], 120)  # a model's long subject ends with "…"
        return words, ai.model
    except Exception:
        log.warning("digest words from the model failed; using plain wording", exc_info=True)
        return plain_words(data), None


# ---------------------------------------------------------------------------
# Email
# ---------------------------------------------------------------------------

C = {"bg": "#f4f1ec", "card": "#ffffff", "ink": "#1d1a16", "soft": "#6b635a", "line": "#e6e0d7", "accent": "#b5452b"}


def _e(s: Any) -> str:
    return html.escape(str(s or ""))


def _link(key: str | None) -> str:
    if not key:
        return settings.public_url
    media, tid = titles.split_key(key)
    return f"{settings.public_url.rstrip('/')}/title/{media}/{tid}"


def _poster(path: str | None, w: int = 60) -> str:
    if not path:
        return f'<div style="width:{w}px;height:{int(w * 1.5)}px;background:{C["line"]};border-radius:6px"></div>'
    return f'<img src="{IMG}{_e(path)}" width="{w}" height="{int(w * 1.5)}" alt="" style="display:block;border-radius:6px;border:0">'


def _stars(n: int | None) -> str:
    return f'<span style="color:{C["accent"]};font-weight:600">★ {n}/10</span>' if n else ""


def _more(n: int, what: str) -> str:
    """"…and 3 more shows": a list cut short says so."""
    if not n:
        return ""
    return f'<div style="font:13px/1.5 Helvetica,Arial,sans-serif;color:{C["soft"]};margin:6px 0 4px">…and {n} more {_e(what)}</div>'


def _section(title: str, body: str) -> str:
    return (f'<tr><td style="padding:26px 28px 6px;font:600 12px/1.3 Helvetica,Arial,sans-serif;letter-spacing:.12em;'
            f'text-transform:uppercase;color:{C["soft"]}">{_e(title)}</td></tr><tr><td style="padding:0 28px">{body}</td></tr>')


def _grid(items: list[dict[str, Any]], caption) -> str:
    """Posters four to a row (email clients need tables)."""
    blocks = []
    for n in range(0, len(items), 4):
        blocks.append("<tr>" + "".join(
            f'<td valign="top" width="25%" style="padding:6px 8px 10px 0"><a href="{_link(i.get("key"))}" style="text-decoration:none;color:{C["ink"]}">'
            f'{_poster(i.get("poster"), 96)}<div style="font:600 13px/1.3 Helvetica,Arial,sans-serif;margin-top:6px">{_e(i.get("title"))}</div>'
            f'<div style="font:12px/1.3 Helvetica,Arial,sans-serif;color:{C["soft"]}">{caption(i)}</div></a></td>'
            for i in items[n:n + 4]) + "</tr>")
    return f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%">{"".join(blocks)}</table>'


def _months(months: list[dict[str, int]]) -> str:
    """Twelve bars, one a month, as table cells (email clients draw no charts)."""
    top = max((m["films"] + m["episodes"] for m in months), default=0) or 1
    cells = []
    for i, m in enumerate(months):
        n = m["films"] + m["episodes"]
        h = max(2, round(70 * n / top)) if n else 2
        cells.append(f'<td valign="bottom" align="center" style="padding:0 2px;font:11px/1.2 Helvetica,Arial,sans-serif;color:{C["soft"]}">'
                     f'<div style="font-weight:600;color:{C["ink"]};margin-bottom:3px">{n or ""}</div>'
                     f'<div style="height:{h}px;background:{C["accent"] if n else C["line"]};border-radius:3px 3px 0 0"></div>'
                     f'<div style="margin-top:4px">{dt.date(2000, i + 1, 1).strftime("%b")[0]}</div></td>')
    return f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%"><tr>{"".join(cells)}</tr></table>'


def _year_sections(data: dict[str, Any], words: dict[str, Any]) -> list[str]:
    y, t = data["year"], data["totals"]
    parts = []
    facts = [f"{t['films']} film{'s' if t['films'] != 1 else ''}", f"{t['episodes']} episode{'s' if t['episodes'] != 1 else ''}",
             f"about {t['hours']} hours"]
    lines = [" · ".join(facts)]
    if y["busiest"]:
        lines.append(f"Busiest month: <b>{_e(y['busiest'])}</b>")
    if y["genres"]:
        lines.append("Favourite genres: " + ", ".join(_e(g) for g in y["genres"]))
    if y["rated"] or y["loved"]:
        lines.append(" · ".join(b for b in (f"{y['rated']} rated" if y["rated"] else "", f"{y['loved']} new favourite{'s' if y['loved'] != 1 else ''}" if y["loved"] else "") if b))
    parts.append(_section("The year in numbers", f'<div style="font:14px/1.7 Helvetica,Arial,sans-serif;color:{C["ink"]}">{"<br>".join(lines)}</div>'))
    if any(m["films"] + m["episodes"] for m in y["months"]):
        parts.append(_section("Month by month", _months(y["months"])))
    if y["best_films"]:
        notes = words.get("film_notes") or {}
        parts.append(_section("Your best films", _grid(y["best_films"], lambda f: f'★ {f["rating"]}/10')
                              + "".join(f'<div style="font:13px/1.5 Helvetica,Arial,sans-serif;color:{C["ink"]};margin:4px 0"><b>{_e(f["title"])}</b>: {_e(notes[f["title"]])}</div>'
                                        for f in y["best_films"] if notes.get(f["title"]))))
    if y["top_shows"]:
        items = "".join(
            f'<tr><td valign="top" width="64" style="padding:10px 14px 10px 0"><a href="{_link(s.get("key"))}">{_poster(s.get("poster"))}</a></td>'
            f'<td valign="top" style="padding:10px 0;border-bottom:1px solid {C["line"]};font:14px/1.45 Helvetica,Arial,sans-serif;color:{C["ink"]}">'
            f'<a href="{_link(s.get("key"))}" style="color:{C["ink"]};text-decoration:none;font-weight:600;font-size:15px">{_e(s["title"])}</a><br>'
            f'<span style="color:{C["soft"]}">{s["episodes"]} episode{"s" if s["episodes"] != 1 else ""}</span></td></tr>' for s in y["top_shows"])
        parts.append(_section("Most watched shows", f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%">{items}</table>'))
    ends = [(label, f) for label, f in (("First film of the year", y["first_film"]), ("Last film of the year", y["last_film"])) if f]
    if ends:
        parts.append(_section("First and last", _grid([{**f, "label": label} for label, f in ends], lambda f: _e(f"{f['label']} · {f['watched_on']}"))))
    return parts


def render(data: dict[str, Any], words: dict[str, Any]) -> tuple[str, str]:
    t = data["totals"]
    more = data.get("more") or {}
    heading = {"week": "Your week in film", "month": "Your month in review", "year": "Your year in review"}[data["kind"]]
    stat_bits = [b for b, n in ((f"{t['films']} film{'s' if t['films'] != 1 else ''}", t["films"]),
                                (f"{t['episodes']} episode{'s' if t['episodes'] != 1 else ''}", t["episodes"]),
                                (f"{t['hours']} h", t["hours"])) if n] or ["nothing watched"]
    parts = [
        f'<tr><td style="padding:28px 28px 0;font:600 12px/1 Helvetica,Arial,sans-serif;letter-spacing:.16em;text-transform:uppercase;color:{C["accent"]}">Movie Night</td></tr>',
        f'<tr><td style="padding:8px 28px 0;font:700 28px/1.2 Georgia,\'Times New Roman\',serif;color:{C["ink"]}">{heading}</td></tr>',
        f'<tr><td style="padding:4px 28px 0;font:15px/1.4 Helvetica,Arial,sans-serif;color:{C["soft"]}">{_e(data["label"])} · {" · ".join(stat_bits)}</td></tr>',
        f'<tr><td style="padding:18px 28px 0;font:16px/1.6 Georgia,\'Times New Roman\',serif;color:{C["ink"]}">{_e(words["intro"])}</td></tr>',
    ]
    if data["kind"] == "year":
        parts += _year_sections(data, words)
    if data["films"] and data["kind"] != "year":
        notes = words.get("film_notes") or {}
        items = "".join(
            f'<tr><td valign="top" width="64" style="padding:10px 14px 10px 0"><a href="{_link(f.get("key"))}">{_poster(f.get("poster"))}</a></td>'
            f'<td valign="top" style="padding:10px 0;border-bottom:1px solid {C["line"]};font:14px/1.45 Helvetica,Arial,sans-serif;color:{C["ink"]}">'
            f'<a href="{_link(f.get("key"))}" style="color:{C["ink"]};text-decoration:none;font-weight:600;font-size:15px">{_e(f["title"])}</a>'
            f'{f" <span style=\"color:{C["soft"]}\">({f["year"]})</span>" if f.get("year") else ""}<br>'
            f'<span style="color:{C["soft"]}">{_e(f["watched_on"])}</span> {_stars(f.get("rating"))}'
            f'{f"<div style=\"margin-top:4px\">{_e(notes.get(f["title"]))}</div>" if notes.get(f["title"]) else ""}'
            f'{f"<div style=\"margin-top:4px;color:{C["soft"]};font-style:italic\">“{_e(f["review"])}”</div>" if f.get("review") else ""}'
            f'</td></tr>' for f in data["films"])
        parts.append(_section("Films", f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%">{items}</table>'))
    if data["shows"] and data["kind"] != "year":
        items = "".join(
            f'<tr><td valign="top" width="64" style="padding:10px 14px 10px 0"><a href="{_link(s.get("key"))}">{_poster(s.get("poster"))}</a></td>'
            f'<td valign="top" style="padding:10px 0;border-bottom:1px solid {C["line"]};font:14px/1.45 Helvetica,Arial,sans-serif;color:{C["ink"]}">'
            f'<a href="{_link(s.get("key"))}" style="color:{C["ink"]};text-decoration:none;font-weight:600;font-size:15px">{_e(s["title"])}</a><br>'
            f'<span style="color:{C["soft"]}">{s["episodes"]} episode{"s" if s["episodes"] != 1 else ""}{" · " + _e(s["span"]) if s["span"] else ""}</span></td></tr>'
            for s in data["shows"])
        parts.append(_section("Shows", f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%">{items}</table>'
                              + _more(more.get("shows", 0), "shows")))
    if data["kind"] == "month" and (data.get("best") or data["top_genres"]):
        bits = []
        if data.get("best"):
            bits.append(f'Film of the month: <b>{_e(data["best"]["title"])}</b> {_stars(data["best"]["rating"])}')
        if data["top_genres"]:
            bits.append("Most watched: " + ", ".join(_e(g) for g in data["top_genres"]))
        parts.append(_section("The month", f'<div style="font:14px/1.6 Helvetica,Arial,sans-serif;color:{C["ink"]}">{"<br>".join(bits)}</div>'))
    if data["picks"]:
        parts.append(_section("Up next", f'<div style="font:14px/1.5 Helvetica,Arial,sans-serif;color:{C["ink"]};margin-bottom:8px">{_e(words["picks_line"])}</div>'
                              + _grid(data["picks"], lambda i: "In your library" if i.get("in_library") else (str(i.get("year") or "")))))
    if (data["library"] or more.get("library")) and data["kind"] != "year":
        parts.append(_section("New in the library", _grid(data["library"], lambda i: str(i.get("year") or ""))
                              + _more(more.get("library", 0), "new in the library")))
    if data["coming"]:
        parts.append(_section("Coming from your watchlist", _grid(data["coming"], lambda i: _e(i.get("date")))
                              + _more(more.get("coming", 0), "coming from your watchlist")))
    off = unsubscribe_url(data["kind"])
    parts.append(f'<tr><td style="padding:26px 28px 28px;font:12px/1.5 Helvetica,Arial,sans-serif;color:{C["soft"]}">'
                 f'Sent by <a href="{settings.public_url}" style="color:{C["soft"]}">Movie Night</a> to everyone here who has '
                 f'it on. <a href="{_e(off)}" style="color:{C["accent"]};font-weight:600">Turn this email off</a> '
                 f'(or switch any of them in Profile → Emails).</td></tr>')
    body = (f'<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width">'
            f'<title>{_e(words["subject"])}</title></head><body style="margin:0;padding:0;background:{C["bg"]}">'
            f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="background:{C["bg"]}"><tr><td align="center" style="padding:24px 12px">'
            f'<table role="presentation" cellpadding="0" cellspacing="0" width="100%" style="max-width:600px;background:{C["card"]};border-radius:14px">'
            f'{"".join(parts)}</table></td></tr></table></body></html>')

    lines = [heading, f"{data['label']} · {' · '.join(stat_bits)}", "", words["intro"], ""]
    if data["kind"] == "year":
        y = data["year"]
        if y["busiest"]:
            lines.append(f"Busiest month: {y['busiest']}")
        if y["genres"]:
            lines.append("Favourite genres: " + ", ".join(y["genres"]))
        lines.append("")
        if y["best_films"]:
            lines.append("YOUR BEST FILMS")
            lines += [f"- {f['title']}: {f['rating']}/10" for f in y["best_films"]]
            lines.append("")
        if y["top_shows"]:
            lines.append("MOST WATCHED SHOWS")
            lines += [f"- {s['title']}: {s['episodes']} episodes" for s in y["top_shows"]]
            lines.append("")
    if data["films"] and data["kind"] != "year":
        lines.append("FILMS")
        for f in data["films"]:
            lines.append(f"- {f['title']}{f' ({f['year']})' if f.get('year') else ''}, {f['watched_on']}{f' · {f['rating']}/10' if f.get('rating') else ''}")
        lines.append("")
    if data["shows"] and data["kind"] != "year":
        lines.append("SHOWS")
        lines += [f"- {s['title']}: {s['episodes']} episodes {s['span']}" for s in data["shows"]]
        if more.get("shows"):
            lines.append(f"...and {more['shows']} more shows")
        lines.append("")
    if data["picks"]:
        lines.append("UP NEXT")
        lines += [f"- {p['title']}" for p in data["picks"]]
    lines += ["", f"Movie Night: {settings.public_url}", f"Turn this email off: {unsubscribe_url(data['kind'])}"]
    return body, "\n".join(lines)


# ---------------------------------------------------------------------------
# Build, store, send
# ---------------------------------------------------------------------------


async def build(db: AsyncSession, user: User, kind: str, start: dt.datetime | None = None,
                end: dt.datetime | None = None, rebuild: bool = False, skip_empty: bool = False) -> Digest | None:
    """This period's digest, made now if it doesn't exist yet. With
    skip_empty (the schedule), None for an empty one, which is then neither
    written (no model call) nor kept."""
    if start is None or end is None:
        start, end = last_period(kind)
    # Built once per person and period, then kept for good (HTML and data).
    existing = await db.scalar(select(Digest).where(Digest.user_id == user.id, Digest.kind == kind, Digest.period_start == start))
    if existing is not None and not rebuild:
        if skip_empty and is_empty(json.loads(existing.data_json).get("data") or {"totals": {}}):
            return None
        return existing
    data = await gather(db, user, kind, start, end)
    if skip_empty and is_empty(data):
        return None
    words, model = await write_words(data)
    page, text = render(data, words)
    values = {"user_id": user.id, "kind": kind, "period_start": start, "period_end": end, "subject": words["subject"],
              "html": page, "data_json": json.dumps({"data": data, "words": words, "text": text}, default=str), "model": model,
              "created_at": dt.datetime.now(dt.timezone.utc)}
    stmt = pg_insert(Digest).values(**values)
    await db.execute(stmt.on_conflict_do_update(constraint="uq_digest_period",
                                                set_={k: v for k, v in values.items() if k not in ("user_id", "kind", "period_start")}))
    await db.commit()
    return await db.scalar(select(Digest).where(Digest.user_id == user.id, Digest.kind == kind, Digest.period_start == start)
                           .execution_options(populate_existing=True))


async def recipient(db: AsyncSession, user: User) -> str | None:
    return (await users.get_setting(db, user.id, EMAIL_KEY)) or user.email


async def send(db: AsyncSession, user: User, digest: Digest, to: str | None = None, scheduled: bool = False) -> str:
    """Email it. A send whose fate is unknown counts as sent: it is recorded
    so, then MaybeSent is raised. `scheduled`: the schedule's own try,
    counted against DIGEST_SEND_TRIES ("Send it to me now" is the person's
    own and always goes)."""
    mailer = clients.mailer()
    if not mailer.enabled:
        raise RuntimeError("Email isn't set up on this install (SMTP_HOST)")
    address = to or await recipient(db, user)
    if not address:
        raise RuntimeError("No email address for this person")
    text = json.loads(digest.data_json).get("text") or digest.subject
    # Recorded, and committed, before the SMTP call: a send that never
    # reports back (the process stopped midway, or the commit after a
    # successful send failed) is then known, and the next run takes it as
    # sent (settle_unknown) rather than sending it again every hour.
    digest.sending_since = dt.datetime.now(dt.timezone.utc)
    if scheduled:
        digest.send_tries = (digest.send_tries or 0) + 1
    await db.commit()
    try:
        # Mail apps show an "Unsubscribe" button for this header.
        await mailer.send(address, digest.subject, digest.html, text,
                          headers={"List-Unsubscribe": f"<{unsubscribe_url(digest.kind)}>"})
    except MaybeSent:
        # The connection broke after the email was handed over: it may have
        # arrived, so it is never sent again (at most once, not twice).
        digest.sent_at, digest.sent_to, digest.sending_since = dt.datetime.now(dt.timezone.utc), address, None
        await db.commit()
        log.warning("%s digest %d for user %d: the mail server didn't answer after it was handed the email; "
                    "counted as sent, not sent again", digest.kind, digest.id, user.id)
        raise
    except Exception:
        # A clear failure: nothing was accepted, so a later try is safe.
        digest.sending_since = None
        await db.commit()
        raise
    digest.sent_at, digest.sent_to, digest.sending_since = dt.datetime.now(dt.timezone.utc), address, None
    await db.commit()
    log.info("%s digest %d for user %d sent", digest.kind, digest.id, user.id)  # never the address
    return address


async def settle_unknown(db: AsyncSession, user: User, digest: Digest) -> None:
    """A send that started and never reported back: taken as sent (it may
    have gone out), so it is never sent twice."""
    digest.sent_at, digest.sending_since = digest.sending_since, None
    digest.sent_to = digest.sent_to or await recipient(db, user)
    await db.commit()
    log.warning("%s digest %d for user %d: a send started at %s never reported back (the app stopped midway?); "
                "counted as sent, not sent again", digest.kind, digest.id, user.id, digest.sent_at.isoformat())


async def wants(db: AsyncSession, user: User, kind: str) -> bool:
    # On unless switched off (Profile → Emails, or the link in every digest):
    # everyone with an email address (from the OIDC ID token, or set by
    # hand) gets every kind. An empty one is skipped (run_scheduled). See
    # docs/limits.md (Background jobs).
    return bool(await users.get_setting(db, user.id, SETTING[kind], True))


# When each kind goes out, local time (TIMEZONE), on the day after its
# period ends: Monday 09:00, the 1st at 09:30, 1 January at 10:30. The
# schedule (app/jobs.py) is built from these.
RUN_AT = {"week": dt.time(9, 0), "month": dt.time(9, 30), "year": dt.time(10, 30)}
# app_settings key holding the start of the last period each kind was run
# for in full (the schedule's own run, or a catch-up): written only when no
# digest of the run failed, so a failed one is tried again (missed).
DONE_KEY = "digest_done:"


def due(kind: str, now: dt.datetime | None = None) -> tuple[dt.datetime, dt.datetime]:
    """The latest period of this kind and when its run was due."""
    start, end = last_period(kind, now)
    t = RUN_AT[kind]
    return start, end.replace(hour=t.hour, minute=t.minute)


async def _done_for(db: AsyncSession, kind: str) -> str | None:
    from app.models import AppSetting
    row = await db.get(AppSetting, DONE_KEY + kind, populate_existing=True)
    return row.value if row else None


async def mark_done(db: AsyncSession, kind: str, start: dt.datetime) -> None:
    from app.models import AppSetting
    stmt = pg_insert(AppSetting).values(key=DONE_KEY + kind, value=start.isoformat())
    await db.execute(stmt.on_conflict_do_update(index_elements=[AppSetting.key], set_={"value": start.isoformat()}))
    await db.commit()


async def missed(db: AsyncSession, now: dt.datetime | None = None) -> list[str]:
    """The kinds whose latest run is due but not done: it was due while
    Movie Night wasn't running (a restart or a deploy across Monday 09:00,
    say: APScheduler keeps its schedule in memory, so a run missed while
    down is never made by itself), or a digest of it couldn't be built or
    sent (SMTP down, say: run_scheduled then leaves it not done). The
    catch-up (app/jobs.py, every hour) runs these again; a digest already
    sent is never sent twice (sent_at). Only the latest period counts: once
    the next one is due, a digest of the one before that never went out
    stays unsent. Before this was kept (no record of a kind at all), the
    record starts at the latest period, taken as done, so nothing old is
    sent."""
    out = []
    now = now or dt.datetime.now(dt.timezone.utc)
    for kind in KINDS:
        start, due_at = due(kind, now)
        done = await _done_for(db, kind)
        if done is None:
            await mark_done(db, kind, start)
            continue
        if now >= due_at and done != start.isoformat():
            out.append(kind)
    return out


async def run_scheduled(db: AsyncSession, kind: str) -> int:
    """Build and send this period's digest to everyone who wants it, unless
    it would be empty (nothing watched, rated or favourited, and for a week
    or a month nothing new in the library: is_empty). The run is recorded
    as done only when no digest failed; otherwise the hourly catch-up
    (missed) runs it again, and everyone who already has theirs is skipped
    (sent_at), so only the failed ones are tried. Each digest at most
    once: a send whose fate is unknown counts as sent (send), and one that
    clearly failed DIGEST_SEND_TRIES times is given up on for the period
    (an ERROR in the log), so the run can be done."""
    start = last_period(kind)[0]
    tries = settings.digest_send_tries
    sent = failed = 0
    # By id, each person loaded afresh: a failed digest rolls the session
    # back, which expires everyone loaded with it, and reading an expired
    # person stopped the run for everyone after them.
    for uid in list(await db.scalars(select(User.id).order_by(User.id))):
        try:
            user = await db.get(User, uid)
            if user is None:
                continue
            if not await wants(db, user, kind) or not await recipient(db, user):
                continue
            d = await build(db, user, kind, skip_empty=True)
            if d is None:
                log.info("%s digest for user %d not sent: nothing watched or new", kind, uid)
                continue
            if d.sent_at is not None:
                continue
            if d.sending_since is not None:
                await settle_unknown(db, user, d)
                continue
            if d.send_tries >= tries:
                continue  # given up on for this period (logged when it happened)
            try:
                await send(db, user, d, scheduled=True)
                sent += 1
            except MaybeSent:
                sent += 1  # recorded as sent, and logged, by send
            except Exception:
                # Read before the rollback below, which expires d.
                if d.send_tries < tries:
                    raise
                log.error("%s digest %d for user %d not sent after %d tries; not tried again for this period "
                          "(Send it to me now in Profile still works)", kind, d.id, uid, d.send_tries, exc_info=True)
                await db.rollback()
        except Exception:
            failed += 1
            await db.rollback()
            log.exception("%s digest for user %d failed", kind, uid)
    if failed:
        log.warning("%s digests: %d couldn't be built or sent; tried again at the next hourly catch-up", kind, failed)
    else:
        await mark_done(db, kind, start)
    return sent
