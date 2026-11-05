"""Movie Night's Jellyfin plugin catalog. Added to Jellyfin as a repository
(Dashboard -> Plugins -> Repositories, https://<movie night>/jellyfin/manifest.json),
it installs and updates the plugin like any other, and gives it its picture,
developer and catalog on its page. The plugin is built from jellyfin-plugin/
in the image. Reachable without signing in, because Jellyfin can't, and it
holds nothing private: the plugin needs Movie Night's token in its settings."""
from __future__ import annotations

import hashlib
import io
import json
import zipfile
from functools import lru_cache
from pathlib import Path

from fastapi import APIRouter, HTTPException
from fastapi.responses import FileResponse, JSONResponse, RedirectResponse, Response

from app.core.config import settings

router = APIRouter(include_in_schema=False)
PLUGIN = Path(__file__).resolve().parents[2] / "jellyfin-plugin"
DLL = PLUGIN / "out" / "Jellyfin.Plugin.MovieNight.dll"
IMAGE = "movie-night.png"


@lru_cache(maxsize=1)
def meta() -> dict:
    return json.loads((PLUGIN / "meta.json").read_text(encoding="utf-8"))


def zip_name() -> str:
    return f"movie-night_{meta()['version']}.zip"


@lru_cache(maxsize=1)
def build_zip() -> bytes:
    # A fixed date in the zip, so the same plugin always gives the same bytes
    # and the checksum in the manifest holds across restarts.
    buf = io.BytesIO()
    with zipfile.ZipFile(buf, "w", zipfile.ZIP_DEFLATED) as z:
        z.writestr(zipfile.ZipInfo(DLL.name, date_time=(2026, 1, 1, 0, 0, 0)), DLL.read_bytes(), zipfile.ZIP_DEFLATED)
    return buf.getvalue()


def manifest() -> list[dict]:
    m, base = meta(), f"{settings.public_url.rstrip('/')}/jellyfin"
    return [{
        "guid": m["guid"],
        "name": m["name"],
        "description": m["description"],
        "overview": m["overview"],
        "owner": m["owner"],
        "category": m["category"],
        "imageUrl": f"{base}/{IMAGE}",
        "versions": [{
            "version": m["version"],
            "changelog": m["changelog"],
            "targetAbi": m["targetAbi"],
            "sourceUrl": f"{base}/{zip_name()}",
            "checksum": hashlib.md5(build_zip()).hexdigest(),  # what Jellyfin checks a download against
            "timestamp": m["timestamp"],
        }],
    }]


@router.get("/jellyfin")
async def catalog_root():
    return RedirectResponse("/jellyfin/manifest.json", status_code=301)


@router.get("/jellyfin/manifest.json")
async def catalog_manifest():
    return JSONResponse(manifest(), headers={"Cache-Control": "no-cache"})


@router.get("/jellyfin/{name}")
async def catalog_file(name: str):
    if name == IMAGE:
        return FileResponse(PLUGIN / IMAGE, media_type="image/png", headers={"Cache-Control": "public, max-age=86400"})
    if name == zip_name():
        return Response(build_zip(), media_type="application/zip",
                        headers={"Content-Disposition": f'attachment; filename="{name}"', "Cache-Control": "no-cache"})
    raise HTTPException(status_code=404, detail="Not found")
