"""Movie Night's Jellyfin plugin catalog (app/web/jellyfin_catalog.py): a
repository Jellyfin can read without signing in, whose zip matches the
checksum Jellyfin checks it against and holds the plugin, and the plugin's
picture."""
import hashlib
import io
import zipfile

from app.web import jellyfin_catalog as catalog
from tests.conftest import client


async def test_catalog_lists_the_plugin_and_serves_it(tmp_path, monkeypatch):
    dll = tmp_path / "Jellyfin.Plugin.MovieNight.dll"
    dll.write_bytes(b"MZ stand-in for the built plugin")
    monkeypatch.setattr(catalog, "DLL", dll)
    catalog.build_zip.cache_clear()
    try:
        async with client(csrf=False) as jellyfin:  # no sign-in, as Jellyfin calls it
            r = await jellyfin.get("/jellyfin/manifest.json")
            assert r.status_code == 200
            [package] = r.json()
            assert package["guid"] == "5bf59830-f994-4e33-8e31-0b4570f490b2"
            assert (package["name"], package["owner"], package["category"]) == ("Movie Night", "movienight", "General")
            assert package["imageUrl"] == "https://mn.test/jellyfin/movie-night.png"
            [version] = package["versions"]
            assert version["targetAbi"] == "12.1.0.0"
            assert version["sourceUrl"] == f"https://mn.test/jellyfin/movie-night_{version['version']}.zip"

            got = await jellyfin.get(version["sourceUrl"].removeprefix("https://mn.test"))
            assert got.status_code == 200 and got.headers["content-type"] == "application/zip"
            assert hashlib.md5(got.content).hexdigest() == version["checksum"]
            assert zipfile.ZipFile(io.BytesIO(got.content)).read("Jellyfin.Plugin.MovieNight.dll") == b"MZ stand-in for the built plugin"
            # The same plugin gives the same zip, so the checksum holds after a restart.
            catalog.build_zip.cache_clear()
            assert hashlib.md5(catalog.build_zip()).hexdigest() == version["checksum"]

            picture = await jellyfin.get("/jellyfin/movie-night.png")
            assert picture.status_code == 200 and picture.headers["content-type"] == "image/png"
            assert picture.content[:8] == b"\x89PNG\r\n\x1a\n"
            assert (await jellyfin.get("/jellyfin/other.zip")).status_code == 404
            assert (await jellyfin.get("/jellyfin")).headers["location"] == "/jellyfin/manifest.json"
    finally:
        catalog.build_zip.cache_clear()
