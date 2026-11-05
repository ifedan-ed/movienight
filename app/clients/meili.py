"""Meilisearch: hybrid meaning + typo-tolerant search over two indexes.

- "library": the Jellyfin library. Documents use the same searchText shape as
  an earlier movie-search page (title | original title | (year) | genres |
  overview | Cast/crew), which is what the 2026-10-02 embedder benchmark
  scored, and the embedder reads only that field.
- "catalog": every movie and show on TMDB with enough ratings (see
  services/catalog.py), same embedder and text shape minus the cast, so a
  description also finds what you don't have yet.
"""
from __future__ import annotations

from typing import Any

import httpx

INDEX = "library"
CATALOG = "catalog"


def people_names(item: dict[str, Any], limit: int = 8) -> list[str]:
    names: list[str] = []
    for p in item.get("People") or []:
        name = p.get("Name")
        if name and name not in names:
            names.append(name)
        if len(names) >= limit:
            break
    return names


def library_doc(item: dict[str, Any]) -> dict[str, Any] | None:
    """One Jellyfin movie/series -> one search document (None without an Id)."""
    item_id = item.get("Id")
    if not item_id:
        return None
    year = item.get("ProductionYear")
    genres = item.get("Genres") or []
    people = people_names(item)
    overview = item.get("Overview") or ""
    name = item.get("Name") or ""
    original = item.get("OriginalTitle") or name
    tmdb_raw = (item.get("ProviderIds") or {}).get("Tmdb")
    text = " | ".join(filter(None, [
        name,
        original if original != name else None,
        f"({year})" if year else None,
        ", ".join(genres) if genres else None,
        overview,
        ("Cast/crew: " + ", ".join(people)) if people else None,
    ]))
    return {
        "id": item_id,
        "tmdb_id": int(tmdb_raw) if tmdb_raw and str(tmdb_raw).isdigit() else None,
        "media_type": "tv" if item.get("Type") == "Series" else "movie",
        "title": name,
        "original_title": original,
        "overview": overview,
        "genres": genres,
        "people": people,
        "year": year,
        "searchText": text,
    }


class MeiliClient:
    def __init__(self, base_url: str, api_key: str, embedder_model: str, client: httpx.AsyncClient | None = None,
                 max_hits: int = 1000):
        self.base_url = base_url.rstrip("/")
        self.api_key = api_key
        self.embedder_model = embedder_model
        self.max_hits = max_hits  # SEARCH_MAX_HITS
        self._client = client or httpx.AsyncClient(timeout=30.0)
        self._owns_client = client is None

    def _headers(self) -> dict[str, str]:
        return {"Authorization": f"Bearer {self.api_key}"} if self.api_key else {}

    async def aclose(self) -> None:
        if self._owns_client:
            await self._client.aclose()

    def embedder_settings(self) -> dict[str, Any]:
        return {
            "source": "huggingFace",
            "model": self.embedder_model,
            "pooling": "useModel",
            "documentTemplate": "{{doc.searchText}}",
            "documentTemplateMaxBytes": 2000,
        }

    async def ensure_index(self) -> None:
        resp = await self._client.post(f"{self.base_url}/indexes", json={"uid": INDEX, "primaryKey": "id"}, headers=self._headers())
        if resp.status_code >= 400 and resp.json().get("code") != "index_already_exists":
            resp.raise_for_status()
        resp = await self._client.patch(
            f"{self.base_url}/indexes/{INDEX}/settings",
            headers=self._headers(),
            json={
                "searchableAttributes": ["title", "original_title", "overview", "genres", "people", "searchText"],
                "filterableAttributes": ["media_type", "year", "genres"],
                "embedders": {"default": self.embedder_settings()},
                # SEARCH_MAX_HITS: how deep "Show more" can go (docs/limits.md).
                "pagination": {"maxTotalHits": self.max_hits},
            },
        )
        resp.raise_for_status()

    async def ensure_catalog_index(self) -> None:
        resp = await self._client.post(f"{self.base_url}/indexes", json={"uid": CATALOG, "primaryKey": "id"}, headers=self._headers())
        if resp.status_code >= 400 and resp.json().get("code") != "index_already_exists":
            resp.raise_for_status()
        resp = await self._client.patch(
            f"{self.base_url}/indexes/{CATALOG}/settings",
            headers=self._headers(),
            json={
                "searchableAttributes": ["title", "original_title", "overview", "genres", "searchText"],
                "filterableAttributes": ["media_type", "year"],
                "embedders": {"default": self.embedder_settings()},
                "pagination": {"maxTotalHits": self.max_hits},
            },
        )
        resp.raise_for_status()

    async def replace_documents(self, docs: list[dict[str, Any]], index: str = INDEX) -> int | None:
        """Add or update documents; returns Meilisearch's task id."""
        if not docs:
            return None
        resp = await self._client.put(f"{self.base_url}/indexes/{index}/documents", json=docs, headers=self._headers(), timeout=120.0)
        resp.raise_for_status()
        return resp.json().get("taskUid")

    async def delete_documents(self, ids: list[str], index: str = INDEX) -> None:
        if ids:
            await self._client.post(f"{self.base_url}/indexes/{index}/documents/delete-batch", json=ids, headers=self._headers())

    async def field_values(self, field: str, index: str) -> dict[str, Any]:
        """id -> one field of every document in an index (empty if no index)."""
        out: dict[str, Any] = {}
        offset = 0
        while True:
            resp = await self._client.post(
                f"{self.base_url}/indexes/{index}/documents/fetch",
                json={"fields": ["id", field], "limit": 10000, "offset": offset},
                headers=self._headers(), timeout=60.0,
            )
            if resp.status_code == 404:
                return out
            resp.raise_for_status()
            page = resp.json()
            for d in page.get("results", []):
                out[d["id"]] = d.get(field)
            offset += 10000
            if offset >= int(page.get("total") or 0):
                return out

    async def task(self, uid: int) -> dict[str, Any]:
        resp = await self._client.get(f"{self.base_url}/tasks/{uid}", headers=self._headers())
        resp.raise_for_status()
        return resp.json()

    async def stats(self, index: str) -> dict[str, Any] | None:
        resp = await self._client.get(f"{self.base_url}/indexes/{index}/stats", headers=self._headers())
        if resp.status_code == 404:
            return None
        resp.raise_for_status()
        return resp.json()

    async def indexed_texts(self) -> dict[str, str]:
        """id -> searchText of everything in the index (empty if no index)."""
        resp = await self._client.post(
            f"{self.base_url}/indexes/{INDEX}/documents/fetch",
            json={"fields": ["id", "searchText"], "limit": 100000},
            headers=self._headers(),
        )
        if resp.status_code == 404:
            return {}
        resp.raise_for_status()
        return {d["id"]: d.get("searchText") for d in resp.json().get("results", [])}

    async def search(self, query: str, semantic_ratio: float = 0.8, limit: int = 20, media_type: str | None = None,
                     index: str = INDEX, scores: bool = False) -> list[dict[str, Any]]:
        """Hybrid search; if the embedder isn't ready (fresh volume, model
        still downloading) fall back to typo-tolerant keyword search. With
        `scores`, each hit carries Meilisearch's 0-1 `_rankingScore`, which
        is comparable across both indexes (same embedder, same query)."""
        body: dict[str, Any] = {"q": query, "limit": limit}
        if media_type in ("movie", "tv"):
            body["filter"] = f"media_type = {media_type}"
        if scores:
            body["showRankingScore"] = True
        try:
            resp = await self._client.post(
                f"{self.base_url}/indexes/{index}/search",
                json={**body, "hybrid": {"semanticRatio": semantic_ratio, "embedder": "default"}},
                headers=self._headers(),
            )
            resp.raise_for_status()
            return resp.json().get("hits", [])
        except httpx.HTTPStatusError:
            resp = await self._client.post(f"{self.base_url}/indexes/{index}/search", json=body, headers=self._headers())
            resp.raise_for_status()
            return resp.json().get("hits", [])
