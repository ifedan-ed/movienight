"""The phone app's sign-in (code -> token) and the list of signed-in devices."""
from __future__ import annotations

from fastapi import APIRouter, Depends, HTTPException, Path
from sqlalchemy import delete, select
from sqlalchemy.ext.asyncio import AsyncSession

from app.api import schemas as s
from app.api.deps import current_user, get_db
from app.models import AppToken, User
from app.services import devices

router = APIRouter(tags=["app"])


@router.post("/app/token", response_model=s.AppTokenOut)
async def app_token(body: s.AppTokenIn, db: AsyncSession = Depends(get_db)):
    """Swap the one-time code from /app/connect for this device's token."""
    try:
        token, user, device = await devices.redeem(db, body.code, body.verifier)
    except devices.DeviceError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    return {"token": token, "user": user.username, "device_id": device.id}


@router.get("/me/devices", response_model=list[s.DeviceOut])
async def my_devices(db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    await devices.drop_expired(db, user.id)
    rows = await db.scalars(select(AppToken).where(AppToken.user_id == user.id).order_by(AppToken.created_at.desc()))
    # expires_at: when the device's sign-in runs out unless it is used before
    # (expires_because "unused"), or for good ("age"). docs/limits.md.
    return [{"id": d.id, "name": d.name, "created_at": d.created_at.isoformat(),
             "last_used_at": d.last_used_at.isoformat() if d.last_used_at else None,
             "kind": "scrobble" if d.scope == "scrobble" else "app",
             "expires_at": devices.expiry(d)[0].isoformat(), "expires_because": devices.expiry(d)[1]} for d in rows]


@router.post("/me/devices/pair", response_model=s.PairConfirmOut)
async def pair_device(body: s.PairConfirmIn, db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    """Confirm the code Kodi (or another TV app) shows: it can then report
    what you watch, and nothing else."""
    try:
        device = await devices.confirm_pair(db, user, body.code)
    except devices.DeviceError as exc:
        raise HTTPException(status_code=exc.status, detail=str(exc)) from exc
    return {"device": device}


@router.delete("/me/devices/{device_id}", response_model=s.Ok)
async def sign_out_device(device_id: int = Path(gt=0), db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    n = (await db.execute(delete(AppToken).where(AppToken.id == device_id, AppToken.user_id == user.id))).rowcount
    await db.commit()
    if not n:
        raise HTTPException(status_code=404, detail="Not found")
    return {}
