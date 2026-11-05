"""TMDB (The Movie Database) API v3: titles, discovery, search, people.

Movie Night reads TMDB directly (faster, more filters) and keeps Seerr only
for requests and download status. Responses are TMDB's own snake_case
shapes; services/titles.py turns them into cached title rows.
"""
from __future__ import annotations

import asyncio
import datetime as dt
import logging
from typing import Any

import httpx

log = logging.getLogger("movienight.tmdb")
API = "https://api.themoviedb.org/3"


class TMDBError(Exception):
    pass


class TMDBClient:
    def __init__(self, api_key: str, language: str = "en-US", region: str = "US", client: httpx.AsyncClient | None = None,
                 hide_adult: bool = False):
        self.api_key = api_key
        self.language = language
        self.region = region
        self.hide_adult = hide_adult
        self._client = client or httpx.AsyncClient(timeout=20.0)

    @property
    def enabled(self) -> bool:
        return bool(self.api_key)

    async def _get(self, path: str, **params: Any) -> dict[str, Any] | None:
        if not self.api_key:
            raise TMDBError("TMDB_API_KEY is not set")
        query = {"api_key": self.api_key, "language": self.language}
        query.update({k: v for k, v in params.items() if v is not None and v != ""})
        for attempt in range(3):
            resp = await self._client.get(f"{API}{path}", params=query)
            if resp.status_code == 404:
                return None
            if resp.status_code == 429 and attempt < 2:
                await asyncio.sleep(float(resp.headers.get("retry-after", "1") or 1))
                continue
            resp.raise_for_status()
            return resp.json()
        return None

    @staticmethod
    def _results(data: dict[str, Any] | None, media: str | None = None) -> list[dict[str, Any]]:
        out = list((data or {}).get("results") or [])
        if media:
            for r in out:
                r.setdefault("media_type", media)
        return out

    def keep(self, rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
        """Search, Home, Discover and smart lists ask TMDB for everything
        (include_adult=true): its own filter hides more than the titles it
        marks adult (Tinto Brass's Cheeky, under every name). Only with
        hide_adult (HIDE_ADULT_TITLES) are the titles TMDB marks adult
        dropped."""
        return [r for r in rows if not r.get("adult")] if self.hide_adult else rows

    # -- lists of titles -------------------------------------------------------

    async def search_multi(self, query: str, page: int = 1) -> list[dict[str, Any]]:
        return (await self.search_multi_page(query, page))[0]

    async def search_multi_page(self, query: str, page: int = 1) -> tuple[list[dict[str, Any]], int]:
        """One page of TMDB's title and people search, and how many pages it
        has (search's "Show more" reads the next one)."""
        data = await self._get("/search/multi", query=query, page=page, include_adult="true")
        return self.keep(self._results(data)), int((data or {}).get("total_pages") or 0)

    async def trending(self, media: str = "all", window: str = "week", page: int = 1) -> list[dict[str, Any]]:
        return self._results(await self._get(f"/trending/{media}/{window}", page=page),
                             None if media == "all" else media)

    async def popular(self, media: str, page: int = 1) -> list[dict[str, Any]]:
        return self._results(await self._get(f"/{media}/popular", page=page, region=self.region), media)

    async def top_rated(self, media: str, page: int = 1) -> list[dict[str, Any]]:
        return self._results(await self._get(f"/{media}/top_rated", page=page, region=self.region), media)

    async def upcoming(self, media: str, page: int = 1) -> list[dict[str, Any]]:
        # Releases (films: theatrical, region WATCH_REGION) and first air dates
        # in the next 120 days only (docs/limits.md).
        today = dt.date.today()
        if media == "movie":
            return self.keep(self._results(await self._get(
                "/discover/movie", page=page, region=self.region, sort_by="popularity.desc", include_adult="true",
                with_release_type="2|3", **{"release_date.gte": today.isoformat(),
                                            "release_date.lte": (today + dt.timedelta(days=120)).isoformat()}), "movie"))
        return self.keep(self._results(await self._get(
            "/discover/tv", page=page, sort_by="popularity.desc", include_adult="true",
            **{"first_air_date.gte": today.isoformat(),
               "first_air_date.lte": (today + dt.timedelta(days=120)).isoformat()}), "tv"))

    async def discover(self, media: str, page: int = 1, **filters: Any) -> list[dict[str, Any]]:
        """filters use TMDB's own names (with_genres, with_watch_providers,
        watch_region, primary_release_date.gte, vote_average.gte, ...)."""
        path = "/discover/tv" if media == "tv" else "/discover/movie"
        return self.keep(self._results(await self._get(path, page=page, include_adult="true", **filters), media))

    async def discover_page(self, media: str, page: int = 1, include_adult: bool = False,
                            **filters: Any) -> dict[str, Any] | None:
        """One raw discover page (results plus total_pages), for walking a
        whole slice of the catalog. With include_adult, the results still
        carry TMDB's adult mark for the caller to drop (see keep)."""
        path = "/discover/tv" if media == "tv" else "/discover/movie"
        return await self._get(path, page=page, include_adult="true" if include_adult else "false", **filters)

    async def recommendations(self, media: str, tmdb_id: int, page: int = 1) -> list[dict[str, Any]]:
        return self._results(await self._get(f"/{media}/{tmdb_id}/recommendations", page=page), media)

    async def similar(self, media: str, tmdb_id: int, page: int = 1) -> list[dict[str, Any]]:
        return self._results(await self._get(f"/{media}/{tmdb_id}/similar", page=page), media)

    # -- single things ---------------------------------------------------------

    async def basic(self, media: str, tmdb_id: int) -> dict[str, Any] | None:
        """Details for the title cache (cards, lists, history): one small call."""
        data = await self._get(f"/{media}/{tmdb_id}", append_to_response="external_ids" if media == "tv" else None)
        if data is not None:
            data["media_type"] = media
        return data

    async def search_movie(self, query: str, year: int | None = None) -> list[dict[str, Any]]:
        return self.keep(self._results(await self._get("/search/movie", query=query, year=year, include_adult="true"), "movie"))

    async def search_tv(self, query: str, year: int | None = None) -> list[dict[str, Any]]:
        return self.keep(self._results(await self._get("/search/tv", query=query, first_air_date_year=year, include_adult="true"), "tv"))

    async def details(self, media: str, tmdb_id: int) -> dict[str, Any] | None:
        extra = "credits,videos,external_ids,watch/providers,keywords," + (
            "release_dates" if media == "movie" else "content_ratings")
        data = await self._get(f"/{media}/{tmdb_id}", append_to_response=extra)
        if data is not None:
            data["media_type"] = media
        return data

    async def season(self, tv_id: int, season_number: int) -> dict[str, Any] | None:
        return await self._get(f"/tv/{tv_id}/season/{season_number}")

    async def collection(self, collection_id: int) -> dict[str, Any] | None:
        return await self._get(f"/collection/{collection_id}")

    async def person(self, person_id: int) -> dict[str, Any] | None:
        return await self._get(f"/person/{person_id}", append_to_response="combined_credits")

    async def find_imdb(self, imdb_id: str) -> dict[str, Any] | None:
        return await self.find_external(imdb_id, "imdb_id")

    async def find_external(self, external_id: str, source: str) -> dict[str, Any] | None:
        """A title by another site's id (imdb_id, tvdb_id)."""
        data = await self._get(f"/find/{external_id}", external_source=source)
        if not data:
            return None
        for key, media in (("movie_results", "movie"), ("tv_results", "tv")):
            if data.get(key):
                hit = data[key][0]
                hit["media_type"] = media
                return hit
        return None

    async def genres(self, media: str) -> list[dict[str, Any]]:
        data = await self._get(f"/genre/{media}/list")
        return (data or {}).get("genres") or []

    async def watch_providers(self, media: str) -> list[dict[str, Any]]:
        data = await self._get(f"/watch/providers/{media}", watch_region=self.region)
        return (data or {}).get("results") or []
