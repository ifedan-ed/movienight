"""Movie Night's own copy of MDBList's lists: the top lists, every list
opened or searched for, and each one's titles. Whatever MDBList sends is
saved as it passes, and the nightly job copies every top list's titles, so
the MDBList lists page, a list's titles and Follow keep working while MDBList
is down or refuses Movie Night's requests (its own limit for the key).

Saved in app_settings as JSON:
  mdblist:copy:top          {"lists": [summary, ...]}
  mdblist:copy:list:<id>    {"summary": summary, "entries": [[media, tmdb_id, title, year], ...]}
"""
from __future__ import annotations

import json
import logging
from typing import Any

from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.ext.asyncio import AsyncSession

from app.clients.mdblist import ListItems, MDBListRefused
from app.models import AppSetting

log = logging.getLogger("movienight.mdblist_copy")

TOP = "mdblist:copy:top"
LIST = "mdblist:copy:list:"


async def _load(db: AsyncSession, key: str) -> dict[str, Any] | None:
    row = await db.get(AppSetting, key)
    try:
        return json.loads(row.value) if row else None
    except ValueError:
        return None


async def _save(db: AsyncSession, key: str, value: dict[str, Any]) -> None:
    text = json.dumps(value)
    stmt = pg_insert(AppSetting).values(key=key, value=text)
    await db.execute(stmt.on_conflict_do_update(index_elements=[AppSetting.key], set_={"value": text}))
    await db.commit()


async def _update_list(db: AsyncSession, list_id: Any, **fields: Any) -> None:
    key = f"{LIST}{int(list_id)}"
    await _save(db, key, {**(await _load(db, key) or {}), **fields})


async def _summaries(db: AsyncSession) -> list[dict[str, Any]]:
    """Every saved list's details, top lists first, each list once."""
    out: dict[Any, dict[str, Any]] = {}
    for s in (await _load(db, TOP) or {}).get("lists") or []:
        out.setdefault(s.get("mdblist_id"), s)
    for (value,) in await db.execute(select(AppSetting.value).where(AppSetting.key.like(LIST + "%"))):
        try:
            s = json.loads(value).get("summary")
        except ValueError:
            continue
        if s:
            out.setdefault(s.get("mdblist_id"), s)
    return list(out.values())


async def has_copy(db: AsyncSession) -> bool:
    return await db.get(AppSetting, TOP) is not None


class WithCopy:
    """MDBList's list calls (the ones lists.py and the lists API make), with
    the copy saved from each answer and answering in MDBList's place when it
    can't. Without a copy, MDBList's own error goes on as before."""

    def __init__(self, mdb, db: AsyncSession):
        self.mdb = mdb
        self.db = db

    @property
    def enabled(self) -> bool:
        return self.mdb.enabled

    async def top_lists(self) -> list[dict[str, Any]]:
        try:
            top = await self.mdb.top_lists()
        except Exception:
            saved = await _load(self.db, TOP)
            if saved is None:
                raise
            return saved["lists"]
        if top:  # an empty answer never replaces the copy
            await _save(self.db, TOP, {"lists": top})
        return top

    async def search_lists(self, query: str) -> list[dict[str, Any]]:
        try:
            found = await self.mdb.search_lists(query)
        except Exception:
            saved = await _summaries(self.db)
            if not saved:
                raise
            words = query.lower().split()
            return [s for s in saved if all(w in f"{s.get('name', '')} {s.get('description', '')} {s.get('user', '')}".lower()
                                            for w in words)]
        for s in found:
            if s.get("mdblist_id"):
                await _update_list(self.db, s["mdblist_id"], summary=s)
        return found

    async def get_list(self, ref: dict[str, str]) -> dict[str, Any] | None:
        try:
            summary = await self.mdb.get_list(ref)
        except Exception:
            for s in await _summaries(self.db):
                if ("id" in ref and str(s.get("mdblist_id")) == ref["id"]) or \
                        ("slug" in ref and s.get("slug") == ref["slug"] and s.get("user") == ref.get("user")):
                    return s
            raise
        if summary and summary.get("mdblist_id"):
            await _update_list(self.db, summary["mdblist_id"], summary=summary)
        return summary

    async def list_items(self, list_id: int | str) -> list[tuple[str, int, str, int | None]]:
        try:
            entries = await self.mdb.list_items(list_id)
        except Exception:
            saved = await _load(self.db, f"{LIST}{int(list_id)}")
            if not saved or saved.get("entries") is None:
                raise
            return ListItems((tuple(e) for e in saved["entries"]), saved.get("no_tmdb") or 0)
        if entries:
            await _update_list(self.db, list_id, entries=[list(e) for e in entries], no_tmdb=getattr(entries, "no_tmdb", 0))
        return entries


async def refresh(db: AsyncSession, mdb) -> int:
    """Nightly: MDBList's top lists and every title in each. Returns how
    many lists were copied; stops (keeping the rest of the copy) when
    MDBList stops answering or refuses (its own limit for the key)."""
    n = 0
    try:
        top = await mdb.top_lists()
        if top:
            await _save(db, TOP, {"lists": top})
        for s in top:
            if not s.get("mdblist_id"):
                continue
            # Every title of each top list (no cap; docs/limits.md).
            entries = await mdb.list_items(s["mdblist_id"])
            await _update_list(db, s["mdblist_id"], summary=s,
                               **({"entries": [list(e) for e in entries], "no_tmdb": getattr(entries, "no_tmdb", 0)} if entries else {}))
            n += 1
    except MDBListRefused:
        log.warning("MDBList copy stopped after %d lists: MDBList refused (its own limit for the key); the rest of the copy is kept", n)
    except Exception:
        log.warning("MDBList copy stopped after %d lists", n, exc_info=True)
    return n
