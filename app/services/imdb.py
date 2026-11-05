"""IMDb's own ratings, kept here. IMDb publishes every rated title's rating
and vote count as a free file, refreshed daily (datasets.imdbws.com,
title.ratings.tsv.gz, about 9 MB; for personal, non-commercial use). The
nightly job loads all of it into imdb_ratings, and the title page reads a
title's rating there by its IMDb id, so no outside service is asked when a
page opens.

A load fills a new table and swaps it in at the end, in one transaction:
the title page reads yesterday's ratings until today's are complete, and a
broken or cut-short download changes nothing."""
from __future__ import annotations

import logging
import zlib
from collections.abc import AsyncIterable
from typing import Any

import httpx
from sqlalchemy import select, text
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.db import engine, session
from app.models import ImdbRating

log = logging.getLogger("movienight.imdb")

URL = "https://datasets.imdbws.com/title.ratings.tsv.gz"
HEADER = b"tconst\taverageRating\tnumVotes"
# A new file must hold at least this share of the titles already loaded.
KEEP_SHARE = 0.9


class ImdbFileError(Exception):
    pass


async def rating(db: AsyncSession, imdb_id: str | None) -> dict[str, Any] | None:
    """{"imdb_rating", "imdb_votes"} for an IMDb id (tt...), or None."""
    if not imdb_id:
        return None
    row = await db.get(ImdbRating, imdb_id)
    return {"imdb_rating": row.rating, "imdb_votes": row.votes} if row else None


async def loaded() -> bool:
    async with session() as db:
        return await db.scalar(select(ImdbRating.tconst).limit(1)) is not None


async def load(chunks: AsyncIterable[bytes]) -> int:
    """Replace imdb_ratings with the gzipped TSV arriving in `chunks`.
    Returns how many titles were loaded; raises ImdbFileError (keeping the
    old ratings) when the file is not what IMDb sends."""
    unzip = zlib.decompressobj(16 + zlib.MAX_WBITS)
    async with engine.begin() as conn:
        before = (await conn.execute(text("SELECT count(*) FROM imdb_ratings"))).scalar_one()
        await conn.execute(text("DROP TABLE IF EXISTS imdb_ratings_load"))
        await conn.execute(text("CREATE TABLE imdb_ratings_load (LIKE imdb_ratings INCLUDING DEFAULTS)"))
        pg = (await conn.get_raw_connection()).driver_connection
        async with pg.cursor() as cur:
            async with cur.copy("COPY imdb_ratings_load (tconst, rating, votes) FROM STDIN") as copy:
                head: bytes | None = b""  # until the header line has gone by
                async for chunk in chunks:
                    data = unzip.decompress(chunk)
                    if head is not None:
                        head += data
                        if b"\n" not in head:
                            continue
                        first, _, data = head.partition(b"\n")
                        if first.rstrip(b"\r") != HEADER:
                            raise ImdbFileError(f"not IMDb's ratings file (first line {first[:60]!r})")
                        head = None
                    if data:
                        await copy.write(data)
                if head is not None:
                    raise ImdbFileError("the file was empty")
                if not unzip.eof:
                    raise ImdbFileError("the download was cut short")
        after =(await conn.execute(text("SELECT count(*) FROM imdb_ratings_load"))).scalar_one()
        if after == 0 or after < before * KEEP_SHARE:
            raise ImdbFileError(f"the new file has {after} titles against {before} loaded")
        await conn.execute(text("ALTER TABLE imdb_ratings_load ADD CONSTRAINT imdb_ratings_load_pkey PRIMARY KEY (tconst)"))
        await conn.execute(text("DROP TABLE imdb_ratings"))
        await conn.execute(text("ALTER TABLE imdb_ratings_load RENAME TO imdb_ratings"))
        await conn.execute(text("ALTER TABLE imdb_ratings RENAME CONSTRAINT imdb_ratings_load_pkey TO imdb_ratings_pkey"))
    return after


async def refresh(http: httpx.AsyncClient, url: str = URL) -> int:
    """Download today's file and load it."""
    async with http.stream("GET", url, timeout=httpx.Timeout(120.0, connect=15.0)) as resp:
        resp.raise_for_status()
        return await load(resp.aiter_bytes())
