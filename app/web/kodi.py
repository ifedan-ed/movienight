"""The Kodi add-on, for Kodi to install by address (Install from zip file,
with https://<movie night>/kodi/ added as a source): /kodi/ lists it and the
zip is built from kodi/service.movienight in the image. Reachable without
signing in, because Kodi can't, and it holds nothing private: the add-on
signs in with a code the person confirms."""
from __future__ import annotations

import io
import re
import zipfile
from functools import lru_cache
from pathlib import Path

from fastapi import APIRouter, HTTPException
from fastapi.responses import HTMLResponse, RedirectResponse, Response

router = APIRouter(include_in_schema=False)
ADDON_ID = "service.movienight"
ADDON = Path(__file__).resolve().parents[2] / "kodi" / ADDON_ID


@lru_cache(maxsize=1)
def version() -> str:
    m = re.search(r'<addon\b[^>]*\bversion="([^"]+)"', (ADDON / "addon.xml").read_text(encoding="utf-8"))
    return m.group(1) if m else "0"


def zip_name() -> str:
    return f"{ADDON_ID}-{version()}.zip"


@lru_cache(maxsize=1)
def build_zip() -> bytes:
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        for f in sorted(ADDON.rglob("*")):
            if f.is_file() and "__pycache__" not in f.parts and f.suffix != ".pyc":
                z.write(f, f"{ADDON_ID}/{f.relative_to(ADDON).as_posix()}")
    return buf.getvalue()


@router.get("/kodi")
async def kodi_root():
    return RedirectResponse("/kodi/", status_code=301)


@router.get("/kodi/", response_class=HTMLResponse)
async def kodi_index():
    # A plain directory listing: Kodi's file browser reads links whose text
    # matches the address.
    name = zip_name()
    return HTMLResponse(f'<!doctype html><html><head><meta charset="utf-8"><title>Index of /kodi/</title></head>'
                        f'<body><h1>Index of /kodi/</h1><pre><a href="{name}">{name}</a></pre></body></html>',
                        headers={"Cache-Control": "no-cache"})


@router.get("/kodi/{name}")
async def kodi_zip(name: str):
    if name != zip_name():
        raise HTTPException(status_code=404, detail="Not found")
    return Response(build_zip(), media_type="application/zip",
                    headers={"Content-Disposition": f'attachment; filename="{name}"', "Cache-Control": "no-cache"})
