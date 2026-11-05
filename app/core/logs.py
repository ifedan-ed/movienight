"""What the server writes to its log, and what it never writes.

The owner's rule: the log keeps only what debugging needs. So:

- One access line per request, written here instead of uvicorn's own
  (which printed every path with its query string, so search text and
  tokens): method, the route's pattern (`/api/v1/titles/{media}/{tmdb_id}`,
  never the ids or the query string), status, time taken, the request id
  and the person's internal numeric id.
- Every other line names people by their numeric id only, titles by
  nothing at all (or an internal row id), and never carries search text,
  names, usernames or email addresses.
- As a safety net, every line (tracebacks included) has query strings cut
  out of anything that looks like a URL or a path, and email addresses
  masked, so an upstream error that quotes its URL (TMDB's search, with
  its api_key and the words searched) can't leak them; and the values a
  database error quotes (a clashing key, a whole failing row, a bad
  input) are cut too.

Each request gets an id (X-Request-ID on the response, `[rid]` on every
line written while it runs), so one request's lines can be found together.
Docker keeps the log, rotated by size (docker-compose.yml); docs/limits.md,
"Data kept, and data deleted", says what is kept and for how long.
"""
from __future__ import annotations

import contextvars
import logging
import re
import secrets
import time
from typing import Any

from starlette.types import ASGIApp, Message, Receive, Scope, Send

# The current request's {"rid": ..., "uid": ...}. A dict, so current_user can
# fill in the person's id from inside the request and the access line sees it.
_request: contextvars.ContextVar[dict[str, Any] | None] = contextvars.ContextVar("movienight_request", default=None)

FORMAT = "%(asctime)s %(levelname)s %(name)s [%(rid)s]: %(message)s"

# A URL or an absolute path followed by a query string: keep the path, drop
# the query. ("/api/v1/search?q=kids treasure map" -> "/api/v1/search?…")
_QUERY = re.compile(r"((?:https?://|/)[^\s'\"<>?]*)\?[^\s'\"<>]*")
_EMAIL = re.compile(r"[\w.+-]+@[\w-]+(?:\.[\w-]+)+")
# A database error's DETAIL quotes the values of the row it clashed with:
# "Key (username)=(alice) already exists" (also "... conflicts with
# existing key (...)=(...)").
_KEY_VALUES = re.compile(r"((?i:key) \([^)]*\)=)\((?:[^()]|\([^()]*\))*\)")
# A NOT NULL or CHECK violation quotes the whole row: "Failing row contains
# (1, alice, Alice Smith, ...)." A value may hold brackets, full stops and
# line breaks (a comment), so everything is cut up to what follows a
# database error's DETAIL (SQLAlchemy's "[SQL: ...]" and "(Background on
# this error ...)", Postgres's HINT or CONTEXT, the next exception of a
# traceback) or the end of the text, then back to the last ")" before it.
_FAILING_ROW = re.compile(
    r"(Failing row contains )\((?:(?!\n\[SQL|\n\(Background on this error|\nHINT:|\nCONTEXT:|\n\nThe above exception"
    r"|\n\nDuring handling|\nTraceback \(most recent).)*\)", re.S)
# "invalid input syntax for type integer: "abc"" quotes the value it got.
_BAD_INPUT = re.compile(r'(invalid input (?:syntax|value) for (?:type|enum) [^:\n]+: )"(?:[^"\n]|"")*"')


def redact(text: str) -> str:
    text = _EMAIL.sub("[email]", _QUERY.sub(r"\1?…", text))
    text = _FAILING_ROW.sub(r"\1(…)", text)
    return _BAD_INPUT.sub(r'\1"…"', _KEY_VALUES.sub(r"\1(…)", text))


def set_user(user_id: int | None) -> None:
    """The signed-in person's internal id, for this request's access line."""
    ctx = _request.get()
    if ctx is not None:
        ctx["uid"] = user_id


def request_id() -> str | None:
    ctx = _request.get()
    return ctx["rid"] if ctx else None


class _Context(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        if not hasattr(record, "rid"):
            record.rid = request_id() or "-"
        return True


class _DropAll(logging.Filter):
    def filter(self, record: logging.LogRecord) -> bool:
        return False


class RedactingFormatter(logging.Formatter):
    """Formats as `inner` would (or with FORMAT), then redacts the result,
    traceback included."""

    def __init__(self, inner: logging.Formatter | None = None):
        super().__init__(FORMAT)
        self.inner = inner

    def format(self, record: logging.LogRecord) -> str:
        if not hasattr(record, "rid"):
            record.rid = request_id() or "-"
        return redact(self.inner.format(record) if self.inner else super().format(record))


def setup() -> None:
    """Called once at import of app.main (after uvicorn set up its own
    logging): our format with the request id, redaction on every handler,
    and uvicorn's access log replaced by AccessLog below."""
    root = logging.getLogger()
    if not root.handlers:
        logging.basicConfig(level=logging.INFO)
    root.setLevel(logging.INFO)
    for handler in root.handlers:
        if not isinstance(handler.formatter, RedactingFormatter):
            handler.setFormatter(RedactingFormatter())
        handler.addFilter(_Context())
    for name in ("uvicorn", "uvicorn.error"):
        for handler in logging.getLogger(name).handlers:
            if not isinstance(handler.formatter, RedactingFormatter):
                handler.setFormatter(RedactingFormatter(handler.formatter))
    # uvicorn's access line has the full path with its query string. The
    # Dockerfile also runs uvicorn with --no-access-log; the filter covers a
    # uvicorn started any other way (development, the browser tests).
    uvicorn_access = logging.getLogger("uvicorn.access")
    uvicorn_access.disabled = True
    if not any(isinstance(f, _DropAll) for f in uvicorn_access.filters):
        uvicorn_access.addFilter(_DropAll())
    logging.getLogger("httpx").setLevel(logging.WARNING)  # one line per upstream call (with its URL) is noise


access_log = logging.getLogger("movienight.access")


class AccessLog:
    """Pure ASGI: gives each request an id, and writes one line when it ends
    with the route's pattern, status, time and the person's numeric id."""

    def __init__(self, app: ASGIApp):
        self.app = app

    async def __call__(self, scope: Scope, receive: Receive, send: Send) -> None:
        if scope["type"] != "http":
            await self.app(scope, receive, send)
            return
        ctx = {"rid": secrets.token_hex(6), "uid": None}
        token = _request.set(ctx)
        started = time.perf_counter()
        status = 500

        async def send_with_id(message: Message) -> None:
            nonlocal status
            if message["type"] == "http.response.start":
                status = message["status"]
                message["headers"] = [*(message.get("headers") or []), (b"x-request-id", ctx["rid"].encode())]
            await send(message)

        try:
            await self.app(scope, receive, send_with_id)
        finally:
            route = scope.get("route")
            path = getattr(route, "path", None) or _generic(scope.get("path", ""))
            access_log.info("%s %s %d %.0fms user=%s", scope.get("method", "-"), path, status,
                            (time.perf_counter() - started) * 1000, ctx["uid"] if ctx["uid"] is not None else "-")
            _request.reset(token)


def _generic(path: str) -> str:
    """A path no route matched (static files, the app shell, 404s): kept as
    is up to its first segment that looks like an id or a name."""
    if path.startswith("/static/") or path in ("/", "/healthz", "/sw.js"):
        return path
    first = path.strip("/").split("/", 1)[0]
    return f"/{first}/…" if first and "/" in path.strip("/") else ("/" + first if first else "/")
