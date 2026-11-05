"""MDBList API (https://api.mdblist.com, docs: mdblist.docs.apiary.io).

Used for: curated lists (top lists, list search, a list's items), outside
scores for a title (IMDb, Rotten Tomatoes, Metacritic, Letterboxd, Trakt)
and a search fallback that finds titles TMDB's search misses.

Movie Night sets no allowance of its own on these calls (the owner's rule:
no usage limits; the old MDBLIST_DAILY_BUDGET is gone). MDBList has its own
limit for the API key (its free plan: 1,000 requests a day). When MDBList
refuses (HTTP 429, or an answer saying a limit was reached) MDBListRefused
is raised, the refusal is remembered for admins (refusal(), shown in
Profile) and MDBList is left alone for DOWN_FOR seconds. Results are cached
by the callers.

Nothing depends on MDBList answering: followed lists keep the titles they
have, title pages show the scores kept here (with a plain note when they
couldn't be refreshed), and search goes on without its fallback. When
MDBList doesn't answer (no connection, a timeout, a server error) it is
also left alone for DOWN_FOR seconds, so nothing waits on it again in the
meantime.
"""
from __future__ import annotations

import datetime as dt
import logging
import re
import time
from typing import Any
from urllib.parse import urlparse

import httpx

log = logging.getLogger("movienight.mdblist")
API = "https://api.mdblist.com"
DOWN_FOR = 600.0  # seconds MDBList is left alone after a failure or a refusal (10 minutes)
_down_until = 0.0
# The last refusal, for admins (status.notices): {"at": ISO time, "why": words}.
# In memory: a restart forgets it; the next answer from MDBList clears it.
_refused: dict[str, str] | None = None


def is_down() -> bool:
    return time.monotonic() < _down_until


def refusal() -> dict[str, str] | None:
    return dict(_refused) if _refused else None


def _mark_down(why: str) -> None:
    global _down_until
    _down_until = time.monotonic() + DOWN_FOR
    log.warning("MDBList isn't answering (%s); left alone for %d minutes", why, DOWN_FOR // 60)


def _mark_refused(why: str) -> None:
    global _down_until, _refused
    _down_until = time.monotonic() + DOWN_FOR
    _refused = {"at": dt.datetime.now(dt.timezone.utc).isoformat(), "why": why}
    log.warning("MDBList refused Movie Night's requests (%s); left alone for %d minutes", why, DOWN_FOR // 60)


class ListItems(list):
    """A list's titles as (media_type, tmdb_id, title, year), and `no_tmdb`:
    how many items MDBList sent without a TMDB id. Movie Night knows titles
    by their TMDB id, so those can't be shown; the list page says how many."""

    def __init__(self, items=(), no_tmdb: int = 0):
        super().__init__(items)
        self.no_tmdb = no_tmdb


class MDBListError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


class MDBListRefused(MDBListError):
    """MDBList said no: its own limit for this API key (HTTP 429, or an
    answer saying a limit was reached). Not Movie Night's limit: it has none."""

    def __init__(self, message: str):
        super().__init__(429, message)


# What callers say when MDBList refuses, in plain words.
REFUSED = "MDBList refused Movie Night's requests just now (its own limit for this install's key)"


def parse_list_url(text: str) -> dict[str, str] | None:
    """mdblist.com/lists/<user>/<slug> -> {"user", "slug"};
    a bare number -> {"id"}. Anything else -> None."""
    raw = (text or "").strip()
    if raw.isdigit():
        return {"id": raw}
    if "://" not in raw:
        if not re.match(r"^(www\.)?mdblist\.com/", raw, re.I):
            return None
        raw = "https://" + raw
    p = urlparse(raw)
    if (p.hostname or "").lower() not in ("mdblist.com", "www.mdblist.com"):
        return None
    parts = [x for x in p.path.split("/") if x]
    if len(parts) >= 3 and parts[0] == "lists":
        return {"user": parts[1], "slug": parts[2]}
    return None


def list_summary(obj: dict[str, Any]) -> dict[str, Any]:
    return {
        "mdblist_id": obj.get("id"),
        "name": obj.get("name") or "Untitled list",
        "slug": obj.get("slug"),
        "user": obj.get("user_name") or "",
        "description": obj.get("description") or "",
        "media": {"movie": "movie", "show": "tv"}.get(obj.get("mediatype") or "", None),
        "item_count": obj.get("items"),
        "likes": obj.get("likes"),
        "dynamic": bool(obj.get("dynamic")),
    }


def ratings_of(media_info: dict[str, Any]) -> dict[str, Any]:
    """MDBList media info -> the scores the title page shows."""
    out: dict[str, Any] = {"mdblist_score": media_info.get("score")}
    for r in media_info.get("ratings") or []:
        src, value, votes = r.get("source"), r.get("value"), r.get("votes")
        if value is None and src in ("tomatoes", "popcorn", "metacritic"):
            value = r.get("score")  # MDBList sometimes fills only the 0-100 score
        if value is None:
            continue
        if src == "imdb":
            out["imdb_rating"], out["imdb_votes"] = value, votes
        elif src == "tomatoes":
            out["rt_critics"] = int(value)
            if r.get("url"):
                out["rt_url"] = "https://www.rottentomatoes.com" + r["url"]
        elif src == "popcorn":
            out["rt_audience"] = int(value)
        elif src == "metacritic":
            out["metacritic"] = int(value)
        elif src == "letterboxd":
            out["letterboxd"] = float(value)
        elif src == "trakt":
            out["trakt_rating"], out["trakt_votes"] = round(float(value) / 10, 1), votes
    return out


class MDBListClient:
    def __init__(self, api_key: str, client: httpx.AsyncClient | None = None):
        self.api_key = api_key
        self._client = client or httpx.AsyncClient(timeout=30.0)

    @property
    def enabled(self) -> bool:
        return bool(self.api_key)

    async def _request(self, method: str, path: str, json: Any = None, wait: float = 10.0,
                       **params: Any) -> tuple[Any, httpx.Headers]:
        global _refused
        if not self.api_key:
            raise MDBListError(503, "MDBList is not configured")
        if is_down():
            if _refused:
                raise MDBListRefused(f"{REFUSED}; it is asked again in a few minutes")
            raise MDBListError(503, "MDBList isn't answering; asked again later")
        query = {"apikey": self.api_key}
        query.update({k: v for k, v in params.items() if v is not None})
        try:
            resp = await self._client.request(method, f"{API}{path}", params=query, json=json,
                                              headers={"User-Agent": "MovieNight/2.0"}, timeout=wait)
        except httpx.TransportError as exc:  # no connection, or a timeout
            _mark_down(type(exc).__name__)
            raise MDBListError(503, "MDBList didn't answer") from exc
        if resp.status_code == 404:
            _refused = None
            return None, resp.headers
        # MDBList's own limit for this key (which may be shared with other
        # apps): a 429, or a 4xx whose answer says a limit was reached.
        if resp.status_code == 429 or (400 <= resp.status_code < 500 and "limit" in resp.text.lower()):
            _mark_refused(f"HTTP {resp.status_code}")
            raise MDBListRefused(REFUSED)
        if resp.status_code >= 500:
            _mark_down(f"HTTP {resp.status_code}")
        if resp.status_code >= 400:
            raise MDBListError(resp.status_code, resp.text[:200])
        try:
            data = resp.json()
        except ValueError as exc:
            _mark_down("an answer that isn't JSON")
            raise MDBListError(502, "MDBList's answer wasn't JSON") from exc
        _refused = None  # it answers again
        return data, resp.headers

    async def top_lists(self) -> list[dict[str, Any]]:
        data, _ = await self._request("GET", "/lists/top")
        return [list_summary(x) for x in data or []]

    async def search_lists(self, query: str) -> list[dict[str, Any]]:
        data, _ = await self._request("GET", "/lists/search", query=query)
        return [list_summary(x) for x in data or []]

    async def get_list(self, ref: dict[str, str]) -> dict[str, Any] | None:
        path = f"/lists/{ref['id']}" if ref.get("id") else f"/lists/{ref['user']}/{ref['slug']}"
        data, _ = await self._request("GET", path)
        if isinstance(data, list):
            data = data[0] if data else None
        return list_summary(data) if data else None

    # Every title of the list, however many (1,000 a request): no cap (the old
    # 3,000 for a followed list and 1,000 for a preview or the nightly copy
    # are gone). See docs/limits.md.
    async def list_items(self, list_id: int | str) -> ListItems:
        """Every item as (media_type, tmdb_id, title, year), in list order
        (1,000 per request), and how many came without a TMDB id."""
        out = ListItems()
        offset, cursor = 0, None
        cursors: set[str] = set()
        while True:
            if cursor in cursors:
                raise MDBListError(502, "MDBList's pages of this list went round in a circle")
            if cursor:
                cursors.add(cursor)
            page = {"cursor": cursor} if cursor else {"offset": offset}
            data, headers = await self._request("GET", f"/lists/{list_id}/items", wait=30.0, limit=1000, unified="true", **page)
            rows = data if isinstance(data, list) else ((data or {}).get("movies") or []) + ((data or {}).get("shows") or [])
            for it in rows:
                tmdb = (it.get("ids") or {}).get("tmdb") or it.get("id")
                if not tmdb:
                    out.no_tmdb += 1  # can't be shown without it; counted, and the list page says so
                    continue
                media = "tv" if it.get("mediatype") == "show" else "movie"
                out.append((media, int(tmdb), it.get("title") or "", it.get("release_year")))
            if headers.get("x-has-more") != "true" or not rows:
                break
            offset += len(rows)
            cursor = headers.get("x-next-cursor") or None
        return out

    async def media_info(self, media: str, tmdb_id: int) -> dict[str, Any] | None:
        data, _ = await self._request("GET", f"/tmdb/{'show' if media == 'tv' else 'movie'}/{tmdb_id}")
        return data

    async def search(self, query: str, limit: int = 10) -> list[tuple[str, int, str, int | None]]:
        data, _ = await self._request("GET", "/search/any", wait=6.0, query=query, limit=limit, sort_by_score="true")
        out = []
        for it in (data or {}).get("search") or []:
            tmdb = (it.get("ids") or {}).get("tmdbid")
            if tmdb:
                out.append(("tv" if it.get("type") == "show" else "movie", int(tmdb), it.get("title") or "", it.get("year")))
        return out
