"""Turn title metadata + a person's state into the card dicts the UI renders."""
from __future__ import annotations

from typing import Any, Iterable

from app.services.state import UserState, title_state


def card(meta: dict[str, Any], st: UserState) -> dict[str, Any]:
    vote = meta.get("vote_average")
    return {
        "key": meta["key"],
        "media_type": meta["media_type"],
        "tmdb_id": meta["tmdb_id"],
        "title": meta.get("title") or "",
        "year": meta.get("year"),
        "poster": meta.get("poster_path"),
        "backdrop": meta.get("backdrop_path"),
        "vote": round(vote, 1) if vote else None,
        **title_state(st, meta["media_type"], meta["tmdb_id"]),
    }


def cards(metas: Iterable[dict[str, Any] | None], st: UserState, limit: int | None = None) -> list[dict[str, Any]]:
    out: list[dict[str, Any]] = []
    seen: set[str] = set()
    for meta in metas:
        if not meta or meta["key"] in seen:
            continue
        seen.add(meta["key"])
        out.append(card(meta, st))
        if limit and len(out) >= limit:
            break
    return out
