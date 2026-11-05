"""Play sessions: what was started in an outside player, and the
"Finished it?" answer that records the watch."""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException, Path
from sqlalchemy.ext.asyncio import AsyncSession

from app import clients
from app.api import schemas as s
from app.api.deps import current_user, get_db
from app.models import PlaySession, User
from app.services import playback, titles

router = APIRouter(prefix="/play", tags=["play"])

SessionId = Path(pattern=r"^[A-Za-z0-9_\-]{16,40}$")


async def _out(db: AsyncSession, ps: PlaySession) -> dict:
    media = "movie" if ps.media_type == "movie" else "tv"
    key = titles.tkey(media, ps.tmdb_id) if ps.tmdb_id else None
    meta = (await titles.ensure(db, clients.tmdb(), [key], max_fetch=1)).get(key) if key else None
    return {"id": ps.id, "media_type": ps.media_type, "tmdb_id": ps.tmdb_id, "season": ps.season, "episode": ps.episode,
            "title": ps.title, "player": ps.player, "started_at": ps.started_at.isoformat(), "key": key,
            "poster": (meta or {}).get("poster_path")}


@router.get("/open", response_model=s.OpenSessionOut)
async def open_session(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Back in Movie Night: the Play to ask "Finished it?" about now, if any."""
    ps, ask = await playback.returned(db, user)
    return {"session": await _out(db, ps) if ps and ask else None}


@router.post("/{session_id}/back", response_model=s.BackOut)
async def back(session_id: str = SessionId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """The player sent the person back from this Play: whether to ask now."""
    try:
        ps, ask = await playback.returned(db, user, session_id)
    except playback.PlayError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    return {"ask": ask, "session": await _out(db, ps)}


@router.get("/{session_id}", response_model=s.PlaySessionOut)
async def get_session(session_id: str = SessionId, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    ps = await db.get(PlaySession, session_id)
    if ps is None or ps.user_id != user.id:
        raise HTTPException(status_code=404, detail="Play not found")
    return await _out(db, ps)


@router.post("/{session_id}/finish", response_model=s.Ok)
async def finish(body: s.FinishIn, session_id: str = SessionId, db: AsyncSession = Depends(get_db),
                 user: User = Depends(current_user)):
    try:
        await playback.finish(db, user, session_id, body.outcome)
    except playback.PlayError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    return {}
