"""Async client for the install's Jellyfin server.

Two kinds of access:
- the install API key (admin): library, anyone's plays, resume and next up,
  marking items played. It never leaves the server.
- a person's own access token, from linking their Jellyfin account (Quick
  Connect or password). It goes into their play links (stream and subtitle
  URLs for VLC, Infuse, ...), which is how Jellyfin's own apps do it.
"""
from __future__ import annotations

import datetime as dt
from typing import Any
from urllib.parse import urlencode

import httpx

CLIENT_NAME = "Movie Night"
CLIENT_VERSION = "2.0"


class JellyfinError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


class IncompleteRead(JellyfinError):
    """A whole-list read (the library, a person's plays) that didn't come back
    whole: an error, a 404, an empty or non-JSON body, an answer without its
    item list or count, a page that stopped short, or a list that changed
    while it was being read. The message says which, in plain words."""

    def __init__(self, message: str, status: int = 502):
        super().__init__(status, message)


# Whole-list reads go page by page, this many items a page, until Jellyfin's
# own count (TotalRecordCount) is reached: there is no cap on how many items
# are read (the old 20,000 for the library and 2,000 for a person's plays are
# gone). See docs/limits.md.
LIBRARY_PAGE = 500  # library items carry Overview and People, so pages stay small
PLAYED_PAGE = 1000
# "Continue watching" and "Next up" are read whole too (the old 20 of each
# is gone), this many a page.
CONTINUE_PAGE = 100


def auth_header(token: str | None = None, device_id: str = "movienight-server", device: str = "Movie Night") -> str:
    """Jellyfin 10.9+/12.x want the MediaBrowser form; sign-in calls also
    need the client/device fields."""
    parts = [f'Client="{CLIENT_NAME}"', f'Device="{device}"', f'DeviceId="{device_id}"', f'Version="{CLIENT_VERSION}"']
    if token:
        parts.append(f'Token="{token}"')
    return "MediaBrowser " + ", ".join(parts)


class JellyfinClient:
    def __init__(self, base_url: str, api_key: str, public_url: str | None = None, client: httpx.AsyncClient | None = None):
        self.base_url = base_url.rstrip("/")
        self.public_url = (public_url or base_url).rstrip("/")
        self.api_key = api_key
        # A full-library pull (for search indexing) with Overview/People on
        # every item can take a while; the other endpoints are small.
        self._client = client or httpx.AsyncClient(timeout=60.0)

    @property
    def enabled(self) -> bool:
        return bool(self.base_url and self.api_key)

    def _headers(self, token: str | None = None, device_id: str = "movienight-server") -> dict[str, str]:
        return {"Authorization": auth_header(token or self.api_key or None, device_id)}

    async def _request(self, method: str, path: str, *, token: str | None = None, device_id: str = "movienight-server",
                       json: Any = None, timeout: float | None = None, **params: Any) -> Any:
        resp = await self._client.request(
            method, f"{self.base_url}{path}",
            params={k: v for k, v in params.items() if v is not None},
            json=json, headers=self._headers(token, device_id),
            timeout=timeout or httpx.USE_CLIENT_DEFAULT,
        )
        if resp.status_code in (401, 403):
            raise JellyfinError(resp.status_code, "Jellyfin refused the sign-in")
        if resp.status_code == 404:
            return None
        if resp.status_code >= 400:
            raise JellyfinError(resp.status_code, resp.text[:200])
        if not resp.content:
            return {}
        try:
            return resp.json()
        except ValueError:
            return {}

    async def _get(self, path: str, timeout: float | None = None, **params: Any) -> Any:
        return await self._request("GET", path, timeout=timeout, **params)

    async def _items_page(self, path: str, timeout: float | None, **params: Any) -> tuple[list[dict[str, Any]], int]:
        """One page of an Items query, strictly: (items, Jellyfin's total).
        Anything but a 200 with a JSON item list and a count raises
        IncompleteRead (unlike _request, which takes a 404 or an empty body for
        "nothing")."""
        try:
            resp = await self._client.get(f"{self.base_url}{path}", params={k: v for k, v in params.items() if v is not None},
                                          headers=self._headers(), timeout=timeout or httpx.USE_CLIENT_DEFAULT)
        except httpx.HTTPError as exc:
            raise IncompleteRead(f"Jellyfin didn't answer ({type(exc).__name__})") from exc
        if resp.status_code != 200:
            raise IncompleteRead(f"Jellyfin answered HTTP {resp.status_code}", status=resp.status_code)
        if not resp.content:
            raise IncompleteRead("Jellyfin sent an empty answer")
        try:
            data = resp.json()
        except ValueError as exc:
            raise IncompleteRead("Jellyfin's answer wasn't JSON") from exc
        items = data.get("Items") if isinstance(data, dict) else None
        total = data.get("TotalRecordCount") if isinstance(data, dict) else None
        if not isinstance(items, list) or not isinstance(total, int) or isinstance(total, bool):
            raise IncompleteRead("Jellyfin's answer had no item list or no count")
        return items, total

    async def _all_items(self, path: str, page_size: int, timeout: float | None = None, **params: Any) -> list[dict[str, Any]]:
        """Every item of an Items query, page by page, or IncompleteRead.

        Complete means: every page answered properly, the items add up to
        Jellyfin's count, the count didn't change between the first and the
        last page, and no item came back on two pages (which happens when
        the list shifts under a paged read). Anything else would make a
        title look gone that isn't, so it is refused as a whole."""
        out: list[dict[str, Any]] = []
        seen: set[str] = set()
        first_total: int | None = None
        while True:
            page, total = await self._items_page(path, timeout, StartIndex=len(out), Limit=page_size,
                                                 EnableTotalRecordCount="true", **params)
            if first_total is None:
                first_total = total
            elif total != first_total:
                raise IncompleteRead(f"Jellyfin's list changed while it was read ({first_total} items, then {total}); read again later")
            ids = {str(it.get("Id")) for it in page if isinstance(it, dict) and it.get("Id")}
            if ids & seen:
                raise IncompleteRead("Jellyfin's list moved while it was read (an item came back twice); read again later")
            seen |= ids
            out.extend(page)
            if len(out) >= total:
                break
            if not page:
                raise IncompleteRead(f"Jellyfin stopped after {len(out)} of {total} items")
        if len(out) != total:
            raise IncompleteRead(f"Jellyfin sent {len(out)} items but counts {total}")
        return out

    # -- linking a person's account ------------------------------------------

    async def quick_connect_enabled(self) -> bool:
        resp = await self._client.get(f"{self.base_url}/QuickConnect/Enabled", headers={"Authorization": auth_header()})
        return resp.status_code == 200 and resp.text.strip().lower() == "true"

    async def quick_connect_initiate(self, device_id: str) -> dict[str, Any]:
        """-> {"Secret", "Code", ...}. The person types Code into a Jellyfin
        app where they are signed in (Settings -> Quick Connect)."""
        resp = await self._client.post(f"{self.base_url}/QuickConnect/Initiate",
                                       headers={"Authorization": auth_header(None, device_id)})
        if resp.status_code >= 400:
            raise JellyfinError(resp.status_code, "Quick Connect is turned off on this Jellyfin server")
        return resp.json()

    async def quick_connect_ready(self, secret: str, device_id: str) -> bool:
        resp = await self._client.get(f"{self.base_url}/QuickConnect/Connect", params={"secret": secret},
                                      headers={"Authorization": auth_header(None, device_id)})
        if resp.status_code in (400, 404):
            raise JellyfinError(404, "That code expired — start again")
        resp.raise_for_status()
        return bool(resp.json().get("Authenticated"))

    async def quick_connect_finish(self, secret: str, device_id: str) -> dict[str, Any]:
        resp = await self._client.post(f"{self.base_url}/Users/AuthenticateWithQuickConnect", json={"Secret": secret},
                                       headers={"Authorization": auth_header(None, device_id)})
        if resp.status_code >= 400:
            raise JellyfinError(resp.status_code, "Quick Connect wasn't approved")
        return resp.json()  # {"AccessToken", "User": {"Id", "Name"}, ...}

    async def sign_in(self, username: str, password: str, device_id: str) -> dict[str, Any]:
        resp = await self._client.post(f"{self.base_url}/Users/AuthenticateByName",
                                       json={"Username": username, "Pw": password},
                                       headers={"Authorization": auth_header(None, device_id)})
        if resp.status_code in (400, 401, 403):
            raise JellyfinError(401, "Wrong Jellyfin username or password")
        if resp.status_code >= 400:
            raise JellyfinError(resp.status_code, "Jellyfin didn't answer")
        return resp.json()

    async def sign_out(self, token: str, device_id: str) -> None:
        try:
            await self._client.post(f"{self.base_url}/Sessions/Logout", headers={"Authorization": auth_header(token, device_id)})
        except httpx.HTTPError:
            pass

    async def me(self, token: str, device_id: str) -> dict[str, Any] | None:
        return await self._request("GET", "/Users/Me", token=token, device_id=device_id)

    # -- library and a person's plays (install API key) -----------------------

    async def library_items(self, user_id: str) -> list[dict[str, Any]]:
        """Every movie and series, with what the search index and the
        library table need: all of them, page by page (LIBRARY_PAGE a page,
        120 s each), or IncompleteRead. A complete read may still be empty;
        the library sync never empties the library on one
        (sync.sync_library, docs/limits.md)."""
        return await self._all_items(
            f"/Users/{user_id}/Items",
            LIBRARY_PAGE,
            timeout=120.0,
            IncludeItemTypes="Movie,Series",
            Recursive="true",
            Fields="Overview,Genres,People,ProviderIds,ProductionYear,OriginalTitle,DateCreated",
            EnableImages="false",
            # A steady order, so pages don't shift as titles are renamed; new
            # titles come last.
            SortBy="DateCreated,SortName",
            SortOrder="Ascending",
        )

    async def _every_page(self, path: str, **params: Any) -> list[dict[str, Any]]:
        """Every item of a list query, page by page (CONTINUE_PAGE a page)
        until Jellyfin's own count is reached: no cap on how many."""
        out: list[dict[str, Any]] = []
        while True:
            data = await self._get(path, StartIndex=len(out), Limit=CONTINUE_PAGE, EnableTotalRecordCount="true", **params)
            items = (data or {}).get("Items") or []
            out.extend(items)
            total = (data or {}).get("TotalRecordCount")
            if not items or not isinstance(total, int) or len(out) >= total:
                return out

    async def resume(self, user_id: str) -> list[dict[str, Any]]:
        """Every partly watched movie and episode ("Continue watching")."""
        return await self._every_page(f"/Users/{user_id}/Items/Resume", MediaTypes="Video", Fields="ProviderIds,Overview")

    async def next_up(self, user_id: str) -> list[dict[str, Any]]:
        """The next episode of every show in progress ("Next up")."""
        return await self._every_page("/Shows/NextUp", UserId=user_id, Fields="ProviderIds,Overview")

    async def played(self, user_id: str, item_types: str = "Movie,Episode") -> list[dict[str, Any]]:
        """Every played movie/episode of this person (no cap), page by page,
        or IncompleteRead."""
        return await self._all_items(
            f"/Users/{user_id}/Items",
            PLAYED_PAGE,
            IncludeItemTypes=item_types,
            Filters="IsPlayed",
            # By when the item arrived, not when it was played: a replay
            # during the read then doesn't shift the pages.
            SortBy="DateCreated,SortName",
            SortOrder="Ascending",
            Recursive="true",
            Fields="ProviderIds,UserData",
            EnableImages="false",
        )

    async def server_id(self) -> str | None:
        """The server's own id (Swiftfin's links name the server by it)."""
        resp = await self._client.get(f"{self.base_url}/System/Info/Public")
        resp.raise_for_status()
        return (resp.json() or {}).get("Id") or None

    async def item(self, user_id: str, item_id: str, fields: str = "ProviderIds,MediaSources") -> dict[str, Any] | None:
        return await self._get(f"/Users/{user_id}/Items/{item_id}", Fields=fields)

    async def episodes(self, series_id: str, user_id: str, season: int | None = None) -> list[dict[str, Any]]:
        data = await self._get(f"/Shows/{series_id}/Episodes", UserId=user_id, Season=season,
                               Fields="ProviderIds", EnableImages="false")
        return (data or {}).get("Items", [])

    async def mark_played(self, user_id: str, item_id: str, when: dt.datetime | None = None) -> None:
        params = {}
        if when is not None:
            params["datePlayed"] = when.astimezone(dt.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")
        await self._request("POST", f"/Users/{user_id}/PlayedItems/{item_id}", **params)

    async def mark_unplayed(self, user_id: str, item_id: str) -> None:
        await self._request("DELETE", f"/Users/{user_id}/PlayedItems/{item_id}")

    async def image(self, item_id: str, kind: str = "Primary", max_width: int = 400) -> tuple[bytes, str] | None:
        resp = await self._client.get(
            f"{self.base_url}/Items/{item_id}/Images/{kind}",
            params={"maxWidth": max_width, "quality": 85},
            headers=self._headers(),
        )
        if resp.status_code != 200:
            return None
        return resp.content, resp.headers.get("content-type", "image/jpeg")

    # -- links handed to players (a person's own token) ------------------------

    def web_url(self, item_id: str) -> str:
        return f"{self.public_url}/web/#/details?id={item_id}"

    def stream_url(self, item_id: str, media_source_id: str, container: str | None, token: str) -> str:
        """The original file, untouched (players read embedded subtitle and
        audio tracks themselves)."""
        ext = f".{container.split(',')[0]}" if container else ""
        q = urlencode({"static": "true", "mediaSourceId": media_source_id, "api_key": token})
        return f"{self.public_url}/Videos/{item_id}/stream{ext}?{q}"

    def subtitle_url(self, item_id: str, media_source_id: str, index: int, token: str, fmt: str = "srt") -> str:
        """One subtitle track as a file (Jellyfin extracts embedded ones)."""
        q = urlencode({"api_key": token})
        return f"{self.public_url}/Videos/{item_id}/{media_source_id}/Subtitles/{index}/0/Stream.{fmt}?{q}"
