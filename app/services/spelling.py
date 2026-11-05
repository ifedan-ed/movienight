"""Typos in a search, fixed before it is embedded.

Meilisearch's keyword search forgives a typo or two per word, but the
meaning model reads "trasure" as noise, and a described plot full of typos
("kids seraching for trasure with a amp") finds nothing. Each word is
checked against the catalog's own vocabulary (every title and plot, with
how often each word appears) and replaced by the commonest word one or two
letters away. A real but rare word is kept unless a far commoner one is a
single letter away ("amp" -> "map"). Search then runs on both the original
and the fixed wording, so a fix never loses anything.
"""
from __future__ import annotations

import html
import json
import re
import time
from collections import Counter
from collections.abc import Iterable

from sqlalchemy.dialects.postgresql import insert as pg_insert

from app.core.db import session
from app.models import AppSetting

KEY = "catalog_vocab"
WORD = re.compile(r"[a-z]+")
LETTERS = "abcdefghijklmnopqrstuvwxyz"
MIN_COUNT = 3        # words seen fewer times are left out of the vocabulary
COMMON = 30          # a word seen this often is never "corrected"
RARE_RATIO = 20      # a rare real word gives way only to one this much commoner
_cache: dict = {"at": 0.0, "vocab": {}}


def count_words(texts: Iterable[str]) -> dict[str, int]:
    counts: Counter[str] = Counter()
    for text in texts:
        # TMDB's plots carry HTML entities ("&amp;"): read as text, not words.
        counts.update(WORD.findall(html.unescape(text or "").lower()))
    return {w: n for w, n in counts.items() if n >= MIN_COUNT and len(w) <= 30}


def edits1(word: str) -> set[str]:
    splits = [(word[:i], word[i:]) for i in range(len(word) + 1)]
    deletes = [a + b[1:] for a, b in splits if b]
    transposes = [a + b[1] + b[0] + b[2:] for a, b in splits if len(b) > 1]
    replaces = [a + c + b[1:] for a, b in splits if b for c in LETTERS]
    inserts = [a + c + b for a, b in splits for c in LETTERS]
    return set(deletes + transposes + replaces + inserts)


def correct_word(word: str, vocab: dict[str, int]) -> str:
    if len(word) < 3 or vocab.get(word, 0) >= COMMON:
        return word
    near = [c for c in edits1(word) if c in vocab]
    best = max(near, key=lambda c: vocab[c], default=None)
    if word in vocab:
        return best if best and vocab[best] >= RARE_RATIO * vocab[word] else word
    if len(word) > 12:
        return best or word
    # Two letters away wins only by a wide margin ("thive": thieves over
    # thrive), and only a word of about the same length with the same first
    # letter: otherwise "the" would answer every short typo.
    far = {c for e in edits1(word) for c in edits1(e)
           if c in vocab and c[0] == word[0] and len(c) >= len(word) - 1}
    best_far = max(far, key=lambda c: vocab[c], default=None)
    if best and (not best_far or vocab[best_far] < 8 * vocab[best]):
        return best
    return best_far or word


def correct(query: str, vocab: dict[str, int]) -> str:
    """The query with its typos fixed (lowercase), or as it was when nothing
    needed fixing or there is no vocabulary yet."""
    words = WORD.findall(query.lower())
    if not vocab or not words:
        return query
    out: list[str] = []
    i = 0
    while i < len(words):
        w = words[i]
        # A word split by a stray space: "fo r" -> "for".
        if i + 1 < len(words) and vocab.get(w, 0) < COMMON and len(words[i + 1]) <= 2 and vocab.get(w + words[i + 1], 0) >= COMMON:
            out.append(w + words[i + 1])
            i += 2
            continue
        out.append(correct_word(w, vocab))
        i += 1
    fixed = " ".join(out)
    return query if fixed == " ".join(words) else fixed


async def save_vocab(vocab: dict[str, int]) -> None:
    value = json.dumps(vocab, separators=(",", ":"))
    async with session() as db:
        stmt = pg_insert(AppSetting).values(key=KEY, value=value)
        await db.execute(stmt.on_conflict_do_update(index_elements=[AppSetting.key], set_={"value": value}))
        await db.commit()
    _cache.update(at=time.monotonic(), vocab=vocab)


async def vocab() -> dict[str, int]:
    """The vocabulary, read once an hour at most."""
    if _cache["vocab"] and time.monotonic() - _cache["at"] < 3600:
        return _cache["vocab"]
    async with session() as db:
        row = await db.get(AppSetting, KEY)
    try:
        loaded = json.loads(row.value) if row else {}
    except ValueError:
        loaded = {}
    _cache.update(at=time.monotonic(), vocab=loaded)
    return loaded


async def _main() -> None:  # pragma: no cover - operator command
    """python -m app.services.spelling: build the vocabulary from the index now."""
    from app import clients
    from app.clients.meili import CATALOG

    texts = await clients.meili().field_values("searchText", CATALOG)
    words = count_words(texts.values())
    await save_vocab(words)
    print(f"vocabulary: {len(words):,} words from {len(texts):,} titles")


if __name__ == "__main__":  # pragma: no cover
    import asyncio

    asyncio.run(_main())
