"""Request-level protections.

- Sign-in is the app's own (app/api/v1/auth_oidc.py): the browser signs in
  at the OIDC provider and Movie Night keeps a signed session cookie
  (starlette SessionMiddleware, SECRET_KEY). The forward-auth proxy header
  trust (X-Authentik-*, TRUSTED_PROXIES, MOVIENIGHT_PROXY_SECRET) was
  removed with that switch (2026-10-16); the app now believes no
  identity header, only its session and the tokens.
- The app binds 127.0.0.1 only, but other containers on its shared
  networks can reach it directly: nothing but a valid session or token
  signs a request in.
- CSRF: any write to /api must carry X-Requested-With: movienight. A custom
  header can't be sent cross-site without a CORS preflight, which this app
  never grants, so a hostile page can't make the browser write here.
- Scrobble endpoints (/api/v1/scrobble...) are called by other servers
  (Jellyfin, Kodi), not the browser: each call must carry a token instead
  (see app/api/v1/scrobble.py). The same token paths serve the phone app
  (Bearer mn_app_..., /api/v1/app/token) and the public files under /kodi
  and /jellyfin (/privacy and /support are static pages Caddy serves).
- Paths with dot segments or doubled slashes are refused outright, so no
  path trick can step from the scrobble prefix to anything else.
- Security headers, including a strict Content-Security-Policy (no inline
  script; images only from here and TMDB).
"""
from __future__ import annotations

import functools
import logging

from starlette.types import ASGIApp, Message, Receive, Scope, Send

from app.core.config import settings

log = logging.getLogger("movienight.security")

CSRF_HEADER = b"x-requested-with"
CSRF_VALUE = b"movienight"
SAFE_METHODS = {"GET", "HEAD", "OPTIONS"}
SCROBBLE_PREFIX = "/api/v1/scrobble"
DOCS_PREFIXES = ("/api/v1/docs", "/api/v1/redoc")

CSP = (
    "default-src 'self'; script-src 'self'; style-src 'self'; "
    "img-src 'self' data: https://image.tmdb.org; connect-src 'self'; font-src 'self'; "
    "manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; "
    "form-action 'self'; frame-ancestors 'none'"
)
HEADERS = [
    (b"x-content-type-options", b"nosniff"),
    (b"referrer-policy", b"same-origin"),
    (b"x-frame-options", b"DENY"),
    (b"permissions-policy", b"camera=(), microphone=(), geolocation=(), payment=()"),
    (b"cross-origin-opener-policy", b"same-origin"),
]


def bad_path(path: str, raw: bytes) -> bool:
    low = raw.lower()
    return ("/../" in path or "/./" in path or path.endswith(("/..", "/.")) or "//" in path
            or b"%2e" in low or b"%2f" in low or b"%5c" in low or "\\" in path)


# ---------------------------------------------------------------------------
# Who may say who is signed in: the app itself (an OIDC session,
# app/api/v1/auth_oidc.py) or a token (phone, TV, scrobble, Jellyfin).
# The forward-auth proxy trust (X-Authentik-* headers, TRUSTED_PROXIES,
# MOVIENIGHT_PROXY_SECRET, from_proxy) was removed with the switch to
# native OIDC (2026-10-16).
# ---------------------------------------------------------------------------


class SecurityMiddleware:
    """Pure ASGI (no BaseHTTPMiddleware), so streaming and background tasks
    behave normally."""

    def __init__(self, app: ASGIApp):
        self.app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        path: str = scope.get("path", "")
        if bad_path(path, scope.get("raw_path") or path.encode()):
            await _json(send, 400, b'{"detail":"Bad path"}')
            return
        method: str = scope.get("method", "GET")
        if method not in SAFE_METHODS and path.startswith("/api/") and not path.startswith(SCROBBLE_PREFIX):
            headers = dict(scope.get("headers") or [])
            if headers.get(CSRF_HEADER) != CSRF_VALUE:
                await _json(send, 403, b'{"detail":"Missing X-Requested-With: movienight header"}')
                return

        async def send_with_headers(message: Message) -> None:
            if message["type"] == "http.response.start":
                hdrs = list(message.get("headers") or [])
                present = {k.lower() for k, _ in hdrs}
                for k, v in HEADERS:
                    if k not in present:
                        hdrs.append((k, v))
                if not path.startswith(DOCS_PREFIXES) and b"content-security-policy" not in present:
                    hdrs.append((b"content-security-policy", CSP.encode()))
                message["headers"] = hdrs
            await send(message)

        await self.app(scope, receive, send_with_headers)


async def _json(send: Send, status: int, body: bytes) -> None:
    await send({"type": "http.response.start", "status": status,
                "headers": [(b"content-type", b"application/json"), (b"content-length", str(len(body)).encode())]})
    await send({"type": "http.response.body", "body": body})
