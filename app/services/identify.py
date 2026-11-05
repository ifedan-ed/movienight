"""The AI's best guess for a described plot.

"kids with a treasure map trying to save their homes" is a memory, not a
title. The meaning search matches it against TMDB's short plot summaries;
a language model knows the films themselves, the way a friend would, and
names the ones it fits. Each is looked up on TMDB and shown first.

/search suggests it (ai: true) for a description: four or more words, no
title matched; /search/ai itself doesn't check. Only with SEARCH_AI_MODEL.
A real answer is cached a day; a failed or empty one only RETRY_AFTER
seconds, so the next search asks again. There is no cap on how often it is
asked, per person or for the install (the owner's rule: no usage limits;
any future limit will be set per person).
"""
from __future__ import annotations

import asyncio
import logging
import re
from typing import Any

from app import clients
from app.clients.litellm import AIOutOfRoom
from app.core import cache
from app.core.config import settings
from app.services import catalog

log = logging.getLogger("movienight.identify")

PROMPT = (
    "You identify films and TV series from a viewer's description, which may be vague, misremembered or misspelled. "
    'Reply with JSON only: {"matches": [{"title": "...", "year": 1985, "type": "movie", "why": "a few words on what fits"}]} '
    '("type" is "movie" or "tv"). Up to 4 candidates, the most likely first. Only real, released titles; never invent one. '
    'If nothing fits, reply {"matches": []}.'
)
# No allowance of any kind: the 40-an-hour per-person cap was removed on
# 2026-10-08 (commit 451f529) and the install-wide daily allowance
# (SEARCH_AI_DAILY_LIMIT) after it. This file's cache is listed in
# docs/limits.md (Search).
KEEP_ANSWER = 86400.0  # seconds a real answer is kept (a day)
RETRY_AFTER = 60.0  # seconds a failed or empty answer is kept: the same search a minute later asks again
FAILED = "The AI didn't answer this time. Search again to ask it again."
NOTHING = "The AI had no guess for this description."
OUT_OF_ROOM = ("The AI ran out of room before it answered (it thought for too long). Search again to ask it again; "
               "an admin can give it more room (SEARCH_AI_MAX_TOKENS).")


def enabled() -> bool:
    return bool(settings.search_ai_model and settings.litellm_api_key)


# A word: a run of letters or digits in any script, an apostrophe inside it
# kept ("don't" is one word).
WORD = re.compile(r"[^\W_]+(?:['’][^\W_]+)*")
# Scripts written without spaces between words (Chinese, Japanese kana,
# Thai, Lao, Khmer, Myanmar): there, every two characters count as a word.
NO_SPACES = re.compile("[\u3400-\u4dbf\u4e00-\u9fff\uf900-\ufaff\u3040-\u30ff\u31f0-\u31ff\uff66-\uff9f"
                       "\u0e00-\u0eff\u1780-\u17ff\u1000-\u109f]")


def words(query: str) -> int:
    """How many words a search has, in any script."""
    n = 0
    for run in WORD.findall(query):
        joined = len(NO_SPACES.findall(run))
        n += (joined + 1) // 2 + (1 if joined < len(run) else 0)
    return n


def worth_asking(query: str, best_title_match: float) -> bool:
    """A description rather than a title: four or more words (in any
    script: words()), and no title matched it closely (0.85)."""
    return enabled() and words(query) >= 4 and best_title_match < 0.85


async def guesses(query: str, media: str | None) -> list[dict[str, Any]] | str | None:
    """[{title, year, type, why}], most likely first; [] when off or the
    model had nothing useful; None when the model failed; OUT_OF_ROOM when
    it ran out of output tokens before answering."""
    if not enabled():
        return []
    key = f"ai-guess:{media or 'all'}:{catalog.norm(query)}"
    # In this process's memory, per (media, normalised query); a restart
    # forgets it. A real answer for a day; a failed (None) or empty one for
    # RETRY_AFTER only, so the next search asks the model again.
    return await cache.cached(key, KEEP_ANSWER, lambda: _ask(query, media),
                              ttl_for=lambda got: KEEP_ANSWER if isinstance(got, list) and got else RETRY_AFTER)


def _short(text: str, n: int) -> str:
    """At most n characters, with "…" when it was longer (never cut without a mark)."""
    return text if len(text) <= n else text[:n].rstrip() + "…"


async def _ask(query: str, media: str | None) -> list[dict[str, Any]] | str | None:
    kind = "TV series" if media == "tv" else "film" if media == "movie" else "film or TV series"
    # One attempt only (no retry on a bad reply); clients.search_ai() caps the
    # reply at SEARCH_AI_MAX_TOKENS and LiteLLMClient waits up to 180 s for it.
    try:
        # The whole description (it is at most SEARCH_MAX_CHARS long already).
        reply = await clients.search_ai().chat_json(PROMPT, f"A {kind} described as: {query}", attempts=1)
    except AIOutOfRoom as exc:
        log.warning("search AI: %s; raise SEARCH_AI_MAX_TOKENS (now %d) to give it more", exc, settings.search_ai_max_tokens)
        return OUT_OF_ROOM
    except Exception:
        log.warning("search AI failed", exc_info=True)
        return None
    out = []
    for m in (reply.get("matches") if isinstance(reply, dict) else None) or []:
        if not isinstance(m, dict) or not str(m.get("title") or "").strip():
            continue
        kind_ = "tv" if str(m.get("type") or "").lower() in ("tv", "series", "show", "tv series") else "movie"
        if media and kind_ != media:
            continue
        year = m.get("year")
        out.append({"title": str(m["title"]).strip()[:200], "year": int(year) if str(year or "").isdigit() else None,
                    "type": kind_, "why": _short(str(m.get("why") or "").strip(), 200) or None})
    return out[:4]


async def resolve(tmdb, guess: dict[str, Any]) -> dict[str, Any] | None:
    """The TMDB title a guess names: same name, the year it gave if any."""
    search = tmdb.search_tv if guess["type"] == "tv" else tmdb.search_movie
    try:
        results = await search(guess["title"], guess["year"]) if guess["year"] else await search(guess["title"])
        if not results and guess["year"]:
            results = await search(guess["title"])
    except Exception:
        return None
    if not results:
        return None
    named = [r for r in results if catalog.title_match(guess["title"], r.get("title") or r.get("name"),
                                                       r.get("original_title") or r.get("original_name")) >= 0.85]
    return (named or results)[0]


async def best_guesses(tmdb, query: str, media: str | None) -> tuple[list[tuple[dict[str, Any], str | None]], str | None]:
    """(TMDB result, why) for each guess TMDB knows, in the model's order,
    and a plain note when there is none: the model failed, or had no guess."""
    named = await guesses(query, media)
    if named is None:
        return [], FAILED
    if isinstance(named, str):
        return [], named
    found = await asyncio.gather(*(resolve(tmdb, g) for g in named))
    seen: set[tuple[str, int]] = set()
    out = []
    for g, r in zip(named, found):
        if r is None:
            continue
        k = (r.get("media_type") or g["type"], int(r["id"]))
        if k not in seen:
            seen.add(k)
            out.append(({**r, "media_type": k[0]}, g["why"]))
    return out, (None if out else NOTHING)
