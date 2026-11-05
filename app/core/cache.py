"""In-process TTL cache for upstream calls (TMDB pages and details, Seerr's
media map, Jellyfin rows, MDBList answers, the AI's search guesses). One web
process, so a dict is enough; a per-key lock stops a cold page from firing
the same upstream call several times at once.

Each caller picks its own TTL in seconds (the list is in docs/limits.md),
or a ttl_for(value) that picks it from the answer (the AI's guesses keep a
real answer a day and a failed or empty one only a minute). Everything is
lost on a restart, and there is no size cap: an expired entry stays in
memory until the same key is asked for again or invalidate() drops it."""
from __future__ import annotations

import asyncio
import time
from collections.abc import Awaitable, Callable
from typing import Any

_store: dict[str, tuple[float, Any]] = {}
_locks: dict[str, asyncio.Lock] = {}


async def cached(key: str, ttl: float, fn: Callable[[], Awaitable[Any]],
                 ttl_for: Callable[[Any], float] | None = None) -> Any:
    hit = _store.get(key)
    if hit and hit[0] > time.monotonic():
        return hit[1]
    lock = _locks.setdefault(key, asyncio.Lock())
    async with lock:
        hit = _store.get(key)
        if hit and hit[0] > time.monotonic():
            return hit[1]
        value = await fn()
        _store[key] = (time.monotonic() + (ttl_for(value) if ttl_for else ttl), value)
        return value


def invalidate(prefix: str = "") -> None:
    for k in [k for k in _store if k.startswith(prefix)]:
        _store.pop(k, None)


def peek(key: str) -> Any:
    hit = _store.get(key)
    return hit[1] if hit and hit[0] > time.monotonic() else None
