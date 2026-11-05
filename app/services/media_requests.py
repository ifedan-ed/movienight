"""Requests to the install's Seerr, made as the person asking.

Everyone shares one Seerr. Each person's Seerr user is the one Seerr made
from their Jellyfin account (Seerr's own Jellyfin import), so requests,
quotas and approvals are theirs and nobody needs to sign in to Seerr. The
install owner may request without a linked Jellyfin (as the API key's user).
"""
from __future__ import annotations

import logging
from typing import Any

from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.clients.seerr import SeerrError
from app.core.config import settings
from app.models import RequestRecord, User

log = logging.getLogger("movienight.requests")


class RequestError(Exception):
    def __init__(self, status: int, message: str):
        super().__init__(message)
        self.status = status


async def seerr_user_id(db: AsyncSession, user: User) -> int | None:
    """The person's Seerr user, found or created from their Jellyfin account."""
    if user.seerr_user_id:
        return user.seerr_user_id
    if not user.jellyfin_user_id:
        return None
    seerr = clients.seerr()
    match = next((u for u in await seerr.users() if u.get("jellyfinUserId") == user.jellyfin_user_id), None)
    if match is None:
        created = await seerr.import_jellyfin_user(user.jellyfin_user_id)
        match = next((u for u in created if u.get("jellyfinUserId") == user.jellyfin_user_id), created[0] if created else None)
        if match:
            log.info("created Seerr user %s for user %d", match.get("id"), user.id)
    if not match or not match.get("id"):
        return None
    user.seerr_user_id = int(match["id"])
    await db.commit()
    return user.seerr_user_id


async def create(db: AsyncSession, user: User, media: str, tmdb_id: int, seasons: list[int] | None = None) -> dict[str, Any]:
    seerr = clients.seerr()
    if not seerr.enabled and not seerr.dry_run:
        raise RequestError(503, "Requests aren't set up on this install")
    as_user = None
    if not seerr.dry_run:
        try:
            as_user = await seerr_user_id(db, user)
        except SeerrError as exc:
            raise RequestError(502, f"Seerr: {exc.message}") from exc
        if as_user is None and not user.is_admin:
            raise RequestError(409, "Connect your Jellyfin account first (Profile → Jellyfin) so requests are yours")
    try:
        result = await seerr.create_request(tmdb_id, media, as_user=as_user, seasons=seasons)
    except SeerrError as exc:
        raise RequestError(400 if exc.status < 500 else 502, exc.message) from exc
    if not result.get("already"):
        db.add(RequestRecord(user_id=user.id, media_type=media, tmdb_id=tmdb_id, seerr_request_id=result.get("id"),
                             status="dry_run" if result.get("dry_run") else "requested", dry_run=bool(result.get("dry_run"))))
        await db.commit()
    # Who asked, by numeric id, never what (docs/limits.md, "Logs").
    log.info("request by user %d: %s", user.id, "already there" if result.get("already") else
             f"new (Seerr request {result.get('id')})" if not result.get("dry_run") else "new (test mode)")
    return result


REQUEST_STATUS = {1: "pending", 2: "approved", 3: "declined", 4: "failed", 5: "completed"}


async def recent(db: AsyncSession, user: User, mine_only: bool = False, take: int = 40,
                 skip: int = 0) -> tuple[list[dict[str, Any]], int]:
    """Seerr requests, newest first (everyone's, or only the person's), a
    page of `take` from `skip`, and how many there are in all (the page
    shows "Show more" until every one is shown)."""
    seerr = clients.seerr()
    if not seerr.enabled:
        return [], 0
    by = None
    if mine_only:
        by = await seerr_user_id(db, user)
        if by is None and not user.is_admin:
            return [], 0
    page = await seerr.requests_page(take=take, skip=skip, requested_by=by)
    results = page.get("results") or []
    return results, int((page.get("pageInfo") or {}).get("results") or skip + len(results))


def settings_summary() -> dict[str, Any]:
    return {"enabled": clients.seerr().enabled, "dry_run": settings.dry_run}
