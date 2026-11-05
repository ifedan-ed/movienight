"""Async client for Seerr (Jellyseerr/Overseerr): requests and their
download status. Titles, discovery and search come from TMDB directly.

Requests carry X-Api-User so each one is made as the person asking (their
Seerr user is created from their Jellyfin account). DRY_RUN is enforced
here: create_request() then never sends the POST.
"""
from __future__ import annotations

from typing import Any

import httpx

# Seerr media status (mediaInfo.status)
MEDIA_UNKNOWN, MEDIA_PENDING, MEDIA_PROCESSING, MEDIA_PARTIAL, MEDIA_AVAILABLE = 1, 2, 3, 4, 5


class SeerrError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status
        self.message = message


def _media_path(media_type: str) -> str:
    return "tv" if media_type == "tv" else "movie"


class SeerrClient:
    def __init__(self, base_url: str, api_key: str, client: httpx.AsyncClient | None = None, dry_run: bool = True):
        self.base_url = base_url.rstrip("/") + "/api/v1" if base_url else ""
        self.api_key = api_key
        self.dry_run = dry_run
        self._client = client or httpx.AsyncClient(timeout=20.0)

    @property
    def enabled(self) -> bool:
        return bool(self.base_url and self.api_key)

    def _headers(self, as_user: int | None = None) -> dict[str, str]:
        headers = {"X-Api-Key": self.api_key}
        if as_user:
            headers["X-Api-User"] = str(as_user)
        return headers

    async def _get(self, path: str, **params: Any) -> Any:
        if not self.enabled:
            raise SeerrError(503, "Seerr isn't set up on this install")
        clean = {k: v for k, v in params.items() if v is not None and v != ""}
        resp = await self._client.get(f"{self.base_url}{path}", params=clean, headers=self._headers())
        if resp.status_code == 404:
            return None
        resp.raise_for_status()
        return resp.json()

    @staticmethod
    def _message(resp: httpx.Response) -> str:
        try:
            return (resp.json().get("message") or resp.text)[:300]
        except ValueError:
            return resp.text[:300]

    # -- status ------------------------------------------------------------

    async def media_page(self, take: int = 500, skip: int = 0) -> dict[str, Any]:
        return await self._get("/media", take=take, skip=skip, filter="all", sort="mediaAdded") or {}

    async def media_info(self, media_type: str, tmdb_id: int) -> dict[str, Any] | None:
        """Seerr's live record for one title (requests, download progress,
        per-season status); None when Seerr has never seen it."""
        data = await self._get(f"/{_media_path(media_type)}/{tmdb_id}")
        return (data or {}).get("mediaInfo")

    async def requests_page(self, take: int = 50, skip: int = 0, filter_: str = "all", requested_by: int | None = None) -> dict[str, Any]:
        return await self._get("/request", take=take, skip=skip, filter=filter_, sort="added", requestedBy=requested_by) or {}

    # -- people ------------------------------------------------------------

    async def users(self) -> list[dict[str, Any]]:
        out: list[dict[str, Any]] = []
        skip = 0
        while True:
            page = await self._get("/user", take=100, skip=skip) or {}
            results = page.get("results") or []
            out += results
            skip += len(results)
            if not results or skip >= ((page.get("pageInfo") or {}).get("results") or 0):
                return out

    async def import_jellyfin_user(self, jellyfin_user_id: str) -> list[dict[str, Any]]:
        """Create the Seerr user for a Jellyfin account (Seerr's own
        import; the new user gets Seerr's default permissions)."""
        if not self.enabled:
            raise SeerrError(503, "Seerr isn't set up on this install")
        resp = await self._client.post(f"{self.base_url}/user/import-from-jellyfin",
                                       json={"jellyfinUserIds": [jellyfin_user_id]}, headers=self._headers())
        if resp.status_code >= 400:
            raise SeerrError(resp.status_code, self._message(resp))
        data = resp.json()
        return data if isinstance(data, list) else []

    # -- requests ------------------------------------------------------------

    async def create_request(self, media_id: int, media_type: str = "movie", as_user: int | None = None,
                             seasons: list[int] | None = None) -> dict[str, Any]:
        """POST a request. Returns Seerr's request object, or
        {"already": True, ...} when Seerr answers 409 (it already has one).
        In dry-run mode no network call is made."""
        if self.dry_run:
            return {"dry_run": True, "mediaId": media_id, "mediaType": media_type, "status": "would_request"}
        if not self.enabled:
            raise SeerrError(503, "Seerr isn't set up on this install")
        body: dict[str, Any] = {"mediaType": _media_path(media_type), "mediaId": media_id}
        if media_type == "tv":
            body["seasons"] = seasons or "all"
        resp = await self._client.post(f"{self.base_url}/request", json=body, headers=self._headers(as_user))
        if resp.status_code == 409:
            return {"already": True, "mediaId": media_id, "mediaType": media_type}
        if resp.status_code >= 400:
            raise SeerrError(resp.status_code, self._message(resp))
        return resp.json()
