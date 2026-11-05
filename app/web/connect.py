"""/app/connect: the page the phone app opens to sign in. The person confirms,
and the app gets a one-time code back on its own address
(movienight://auth?code=...)."""
from __future__ import annotations

import time
from pathlib import Path
from urllib.parse import quote

from fastapi import APIRouter, Depends, Form, Query, Request
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.templating import Jinja2Templates
from sqlalchemy.ext.asyncio import AsyncSession

from app.api.deps import current_user, get_db
from app.core.config import settings
from app.models import User
from app.services import devices

router = APIRouter(include_in_schema=False)
templates = Jinja2Templates(directory=str(Path(__file__).resolve().parents[2] / "web"))


# The form's answer is a redirect back to the app, so the page may submit to
# the app's own address too (some browsers apply form-action to redirects).
CSP = ("default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; object-src 'none'; base-uri 'none'; "
       "form-action 'self' movienight: exp:; frame-ancestors 'none'")


def _page(request: Request, status: int = 200, **ctx) -> HTMLResponse:
    return templates.TemplateResponse(request, "connect.html", ctx, status_code=status,
                                      headers={"Cache-Control": "no-store", "Content-Security-Policy": CSP})


@router.get("/app/connect", response_class=HTMLResponse)
async def connect_page(request: Request, challenge: str = Query("", max_length=200), redirect: str = Query("", max_length=300),
                       device: str = Query("Movie Night app", max_length=settings.reported_max_chars), user: User = Depends(current_user)):
    if not devices.CHALLENGE.match(challenge) or not devices.redirect_ok(redirect):
        return _page(request, 400, error="This sign-in link isn't valid. Start again from the app.")
    ts = int(time.time())
    return _page(request, name=user.display_name or user.username, device=device, challenge=challenge, redirect=redirect,
                 ts=ts, sig=devices.form_sig(user.username, challenge, redirect, device, ts))


@router.post("/app/connect")
async def connect(request: Request, challenge: str = Form(..., max_length=200), redirect: str = Form(..., max_length=300),
                  device: str = Form("Movie Night app", max_length=settings.reported_max_chars), ts: int = Form(...), sig: str = Form(..., max_length=128),
                  db: AsyncSession = Depends(get_db), user: User = Depends(current_user)):
    try:
        devices.check_form(user.username, challenge, redirect, device, ts, sig)
    except devices.DeviceError as exc:
        return _page(request, exc.status, error=str(exc))
    code = await devices.make_code(db, user, challenge, device)
    sep = "&" if "?" in redirect else "?"
    return RedirectResponse(f"{redirect}{sep}code={quote(code)}", status_code=303)
