"""Movie Night: the API (/api/v1) and the web app that uses it."""
from __future__ import annotations

import hashlib
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import quote

from fastapi import Depends, FastAPI, HTTPException, Request
from fastapi.encoders import jsonable_encoder
from fastapi.exceptions import RequestValidationError
from fastapi.openapi.docs import get_swagger_ui_html
from fastapi.responses import FileResponse, HTMLResponse, JSONResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from fastapi.templating import Jinja2Templates
from starlette.exceptions import HTTPException as StarletteHTTPException
from starlette.middleware.sessions import SessionMiddleware

from app import clients
from app.api.deps import current_user
from app.api.v1 import router as api_v1
from app.core import logs
from app.core.config import settings
from app.core.db import init_db
from app.core.security import SecurityMiddleware
from app.services import rows
from app.web import connect as app_connect
from app.web import jellyfin_catalog, kodi, signin

# What is logged, and what never is (search text, titles, names, emails,
# query strings): app/core/logs.py and docs/limits.md.
logs.setup()

WEB = Path(__file__).resolve().parents[1] / "web"
STATIC = WEB / "static"


class RevalidatedStatic(StaticFiles):
    """Static files that the browser re-checks (cheap 304s) so module imports
    without a ?v= never run stale code after a deploy."""

    def file_response(self, *args, **kwargs):
        resp = super().file_response(*args, **kwargs)
        resp.headers["Cache-Control"] = "no-cache"
        return resp


def _asset_version() -> str:
    """Changes whenever a static file changes, so phones never keep stale JS."""
    h = hashlib.sha1()
    for p in sorted(STATIC.rglob("*")):
        if p.is_file():
            h.update(str(p.relative_to(STATIC)).encode())
            h.update(p.read_bytes())
    return h.hexdigest()[:10]


@asynccontextmanager
async def lifespan(_app: FastAPI):
    await init_db()
    scheduler = None
    if settings.run_jobs:
        from app.jobs import start_scheduler

        scheduler = start_scheduler()
    yield
    if scheduler is not None:
        scheduler.shutdown(wait=False)
    await clients.close()


# The API description and its docs are served below, behind sign-in.
app = FastAPI(title="Movie Night", version="2.0", lifespan=lifespan, openapi_url=None, docs_url=None, redoc_url=None)
app.add_middleware(SecurityMiddleware)
app.add_middleware(logs.AccessLog)  # outermost: one line per request, with its id
# Sign-in with OIDC (optional): keeps the signed session cookie the callback
# sets. Harmless while OIDC is off. Same secret as everything else.
app.add_middleware(SessionMiddleware, secret_key=settings.secret_key or "movienight",
                   max_age=settings.oidc_session_days * 86400, same_site="lax",
                   # The app sits behind a TLS proxy over a plain http hop,
                   # so the session cookie must not require https here.
                   # Safe: the app binds 127.0.0.1 only.
                   https_only=False)
app.include_router(api_v1)
app.include_router(app_connect.router)
app.include_router(signin.router)
app.include_router(kodi.router)
app.include_router(jellyfin_catalog.router)
app.mount("/static", RevalidatedStatic(directory=str(STATIC)), name="static")
templates = Jinja2Templates(directory=str(WEB))
VERSION = _asset_version()


def _plain(error: dict) -> str:
    """One validation error as a sentence a person can act on: "Your comment
    is too long: at most 5,000 characters." Technical limits say so in plain
    words when hit (docs/limits.md)."""
    field = next((str(p) for p in reversed(error.get("loc") or ()) if isinstance(p, str) and p not in ("body", "query", "path")), "")
    name = {"review": "Your comment", "description": "The description", "q": "A search", "name": "The name"}.get(
        field, field.replace("_", " ").capitalize() or "It")
    ctx, kind = error.get("ctx") or {}, error.get("type", "")
    # Limits the owner keeps (docs/limits.md; app/api/schemas.py PlayerSettingsIn).
    if kind == "too_long" and field == "custom":
        return f"You can have up to {ctx.get('max_length', 0):,} players of your own."
    if kind == "too_long" and field == "enabled":
        return f"Up to {ctx.get('max_length', 0):,} players can be ticked."
    if kind == "string_too_long":
        return f"{name} is too long: at most {ctx.get('max_length', 0):,} characters."
    if kind == "too_long":
        return f"{name}: at most {ctx.get('max_length', 0):,} at a time."
    return f"{name}: {error.get('msg', 'not valid')}."


@app.exception_handler(StarletteHTTPException)
async def http_exception(request: Request, exc: StarletteHTTPException) -> JSONResponse:
    """A browser navigation that needs a sign-in goes to the OIDC provider;
    API callers keep the plain JSON error. Only when OIDC is configured."""
    if exc.status_code == 401 and settings.signin_enabled and "text/html" in (request.headers.get("accept") or ""):
        return RedirectResponse(_sign_in_url(request))
    return JSONResponse(status_code=exc.status_code, content={"detail": exc.detail})


@app.exception_handler(rows.TMDBDown)
async def tmdb_down(_request: Request, exc: rows.TMDBDown) -> JSONResponse:
    """A page of a TMDB list that TMDB couldn't send (and no copy is kept):
    502 with a plain sentence, which the grids show where the next cards
    would be, instead of an empty page that ends them without a word."""
    return JSONResponse(status_code=502, content={"detail": str(exc)})


@app.exception_handler(RequestValidationError)
async def validation_error(_request: Request, exc: RequestValidationError) -> JSONResponse:
    """422 with a plain sentence in `detail` (the website and the phone show
    it as is); the details stay under `errors`."""
    errors = exc.errors()
    return JSONResponse(status_code=422, content={"detail": " ".join(_plain(e) for e in errors[:3]) or "That isn't valid.",
                                                  "errors": jsonable_encoder(errors)})


@app.get("/healthz", include_in_schema=False)
async def healthz() -> dict:
    return {"status": "ok"}


# The proxy lets through anything that looks like an app token (only the API
# checks it), so these need a real sign-in like every other API route.
@app.get("/api/v1/openapi.json", include_in_schema=False, dependencies=[Depends(current_user)])
async def openapi() -> JSONResponse:
    return JSONResponse(app.openapi())


@app.get("/api/v1/docs", include_in_schema=False, dependencies=[Depends(current_user)])
async def docs() -> HTMLResponse:
    return get_swagger_ui_html(openapi_url="/api/v1/openapi.json", title="Movie Night API")


@app.get("/sw.js", include_in_schema=False)
async def service_worker() -> FileResponse:
    """At the root so it may look after every page (PWA)."""
    return FileResponse(STATIC / "sw.js", media_type="text/javascript",
                        headers={"Cache-Control": "no-cache", "Service-Worker-Allowed": "/"})


@app.get("/{path:path}", response_class=HTMLResponse, include_in_schema=False)
async def shell(request: Request, path: str):
    """Every page is the same app shell; the router in app.js draws it."""
    if path.startswith(("api/", "static/")):
        raise HTTPException(status_code=404)
    # With OIDC on, a signed-out visitor goes to the provider, and comes back
    # to this same page afterwards (_sign_in_url carries it as `next`).
    if settings.signin_enabled and not _has_identity(request):
        return RedirectResponse(_sign_in_url(request))
    return templates.TemplateResponse(request, "index.html", {"v": VERSION, "jf": settings.jellyfin_public_url.rstrip("/")},
                                      headers={"Cache-Control": "no-cache"})


def _sign_in_url(request: Request) -> str:
    """Where a signed-out browser goes, carrying this request's own path and
    query as `next`, so the person comes back to it after signing in (the
    phone app's /app/connect?challenge=… round-trip depends on it): the
    sign-in page when passwords are on (it offers OIDC too, if set up), else
    straight to the OIDC provider (/api/v1/auth/oidc/login keeps `next` in
    the session and the callback reads it back)."""
    target = request.url.path + (f"?{request.url.query}" if request.url.query else "")
    start = "/signin" if settings.password_signin else "/api/v1/auth/oidc/login"
    return start + "?next=" + quote(target, safe="/")


def _has_identity(request: Request) -> bool:
    """Cheaply: is this request signed in — a Movie Night OIDC session, or a
    dev user (local development and tests). The browser signs in with OIDC
    (app/api/v1/auth_oidc.py); the phone app, Kodi and Jellyfin use tokens,
    which the routes below check themselves."""
    session = request.scope.get("session") or {}
    if session.get("uid") is not None:
        return True
    return bool(settings.auth_dev_user)
